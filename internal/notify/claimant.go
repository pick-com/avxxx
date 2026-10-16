package notify

// Transactional mail to non-users (store.MailOutboxItem, migration 048
// mail_outbox): the copyright takedown claimant and counter-notifier
// (docs/COMPLIANCE.md 4).
//
//	takedown_received  receipt with the ticket number (claimant)
//	takedown_status    the ticket's status changed (claimant; the
//	                   counter-notifier too once a counter-notice exists)
//	takedown_counter   counter-notice receipt (counter-notifier)
//
// Templates are templates/<kind>.<lang>.txt (subject + text, plain text
// only) in the language of the recipient's submission. The recipient's
// address is resolved from the ticket when the row is claimed and is never
// put in a link: links carry the ticket only, and the status lookup asks
// for the email in the form (sent as a header, not in the URL).
// Notify prefs and quiet hours do not apply; these are not notifications a
// user opted into.

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/obs"
	"avxxx/internal/store"
)

// CopyrightPath is the web page with the status lookup and counter-notice
// forms (?tab=lookup|counter&ticket=).
const CopyrightPath = "/copyright"

// Mail-only kinds: no push, no HTML part.
const (
	KindTakedownReceived = store.MailTakedownReceived
	KindTakedownStatus   = store.MailTakedownStatus
	KindTakedownCounter  = store.MailTakedownCounter
)

var mailOnlyKinds = map[string]bool{KindTakedownReceived: true, KindTakedownStatus: true, KindTakedownCounter: true}

// MailOutboxStore is implemented by *store.Store; a worker whose Store also
// implements it delivers mail_outbox rows on the email channel.
type MailOutboxStore interface {
	ReclaimStaleMailOutbox(ctx context.Context, lease time.Duration) (int64, error)
	ClaimMailOutbox(ctx context.Context, limit int) ([]store.MailOutboxItem, error)
	FinishMailOutbox(ctx context.Context, it store.MailOutboxItem, r store.OutboxResult) (bool, error)
}

type takedownData struct {
	Name, Ticket, Status, Recipient string
	StatusURL, CounterURL           string
	SLAHours                        int
}

// RenderTakedown renders a takedown mail (subject + plain text) in lang
// (the submission's language; unknown falls back to DefaultLang).
func (r *Renderer) RenderTakedown(it store.MailOutboxItem) (mailer.Message, error) {
	if !mailOnlyKinds[it.Kind] {
		return mailer.Message{}, fmt.Errorf("unknown mail kind %q", it.Kind)
	}
	lang := r.Lang(it.Kind, it.Lang)
	t, ok := r.tmpl[it.Kind+"."+lang]
	if !ok {
		return mailer.Message{}, fmt.Errorf("no %s template for %s", it.Kind, lang)
	}
	ticket := url.QueryEscape(it.Ticket)
	data := takedownData{Name: it.Name, Ticket: it.Ticket, Status: it.Status, Recipient: it.Recipient,
		StatusURL:  r.link(CopyrightPath + "?tab=lookup&ticket=" + ticket),
		CounterURL: r.link(CopyrightPath + "?tab=counter&ticket=" + ticket),
		SLAHours:   int(store.TakedownSLA / time.Hour)}
	subject, err := execText(t.text, "subject", data)
	if err != nil {
		return mailer.Message{}, err
	}
	text, err := execText(t.text, "text", data)
	if err != nil {
		return mailer.Message{}, err
	}
	return mailer.Message{Subject: strings.Join(strings.Fields(subject), " "), Text: text}, nil
}

// tickMail delivers one batch of mail_outbox rows (email channel only).
func (w *Worker) tickMail(ctx context.Context) (int, error) {
	ms, ok := w.Store.(MailOutboxStore)
	if !ok || w.Mail == nil {
		return 0, nil
	}
	if n, err := ms.ReclaimStaleMailOutbox(ctx, w.Opts.Lease); err != nil {
		return 0, fmt.Errorf("reclaim mail: %w", err)
	} else if n > 0 {
		w.logf("notifier: reclaimed %d stale mail_outbox rows", n)
	}
	items, err := ms.ClaimMailOutbox(ctx, w.Opts.Batch)
	if err != nil {
		return 0, fmt.Errorf("claim mail: %w", err)
	}
	sem := make(chan struct{}, w.Opts.Concurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(it store.MailOutboxItem) {
			defer func() { <-sem; wg.Done() }()
			w.processMail(ctx, ms, it)
		}(it)
	}
	wg.Wait()
	return len(items), nil
}

func (w *Worker) processMail(ctx context.Context, ms MailOutboxStore, it store.MailOutboxItem) {
	start := w.now()
	ictx, cancel := context.WithTimeout(ctx, w.Opts.ItemTimeout)
	res := w.deliverMail(ictx, it)
	cancel()
	if res.Status == store.OutboxPending && it.Attempt >= w.Opts.MaxAttempts {
		res = store.OutboxResult{Status: store.OutboxFailed, Error: fmt.Sprintf("gave up after %d attempts: %s", it.Attempt, res.Error)}
	}
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer fcancel()
	ok, err := ms.FinishMailOutbox(fctx, it, res)
	obs.NotifierDuration.WithLabelValues("email").Observe(w.now().Sub(start).Seconds())
	switch {
	case err != nil:
		obs.NotifierDeliveries.WithLabelValues("email", "error").Inc()
		w.logf("notifier: finish mail_outbox id=%d: %v", it.ID, err)
	case !ok:
		obs.NotifierDeliveries.WithLabelValues("email", "lease_lost").Inc()
		w.logf("notifier: mail_outbox id=%d lease lost; result %s dropped", it.ID, res.Status)
	default:
		obs.NotifierDeliveries.WithLabelValues("email", string(res.Status)).Inc()
		// No address, name or ticket in the log line.
		w.logf("notifier: mail_outbox id=%d kind=%s recipient=%s attempt=%d status=%s error=%q",
			it.ID, it.Kind, it.Recipient, it.Attempt, res.Status, res.Error)
	}
}

func (w *Worker) deliverMail(ctx context.Context, it store.MailOutboxItem) store.OutboxResult {
	if age := w.now().Sub(it.CreatedAt); age > w.Opts.MaxAge {
		return cancelled(fmt.Sprintf("stale: queued %s ago", age.Round(time.Minute)))
	}
	if it.Email == "" || it.Ticket == "" {
		return cancelled("recipient address no longer on the ticket")
	}
	msg, err := w.Renderer.RenderTakedown(it)
	if err != nil {
		return failed("render: " + err.Error())
	}
	msg.To = it.Email
	err = w.Mail.Send(ctx, msg)
	switch {
	case err == nil:
		return store.OutboxResult{Status: store.OutboxSent}
	case mailer.IsPermanent(err):
		return failed("smtp: " + err.Error())
	}
	return store.OutboxResult{Status: store.OutboxPending, Error: "smtp: " + err.Error(),
		RetryAt: w.now().Add(Backoff(it.Attempt, w.Opts.BaseBackoff, w.Opts.MaxBackoff, 0))}
}
