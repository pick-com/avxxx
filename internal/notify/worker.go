// Package notify delivers notification_outbox rows (written by
// store.NotifyFanout) over Web Push and email. cmd/notifier runs Worker.
//
// Each tick the worker returns rows whose lease expired to 'pending', claims
// up to Batch due pending rows of the configured channels (FOR UPDATE SKIP
// LOCKED, status -> 'sending', attempt+1), delivers them with bounded
// concurrency and finishes each one:
//
//	sent       delivered (push: at least one subscription accepted it)
//	pending    transient failure; send_after = now + backoff(attempt)
//	failed     permanent failure, or attempt reached MaxAttempts
//	cancelled  nothing to do: channel turned off since enqueue, no push
//	           subscription / email address, or older than MaxAge
//
// Notify prefs and quiet hours were applied when the row was enqueued
// (send_after); the worker only re-checks prefs so a later opt-out wins.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/store"
	"avxxx/internal/webpush"
)

// Store is the subset of *store.Store the worker uses.
type Store interface {
	ReclaimStaleOutbox(ctx context.Context, lease time.Duration) (int64, error)
	ClaimOutbox(ctx context.Context, channels []string, limit int) ([]store.OutboxItem, error)
	FinishOutbox(ctx context.Context, it store.OutboxItem, r store.OutboxResult) (bool, error)
	PushSubscriptions(ctx context.Context, userID int64) ([]store.PushSubscription, error)
	PushDelivered(ctx context.Context, id int64) error
	PushGone(ctx context.Context, id int64) error
	PushFailed(ctx context.Context, id int64, maxFailures int) (bool, error)
}

// Pusher sends one Web Push message (*webpush.Client).
type Pusher interface {
	Send(ctx context.Context, sub webpush.Subscription, payload []byte) (webpush.Result, error)
}

// Mailer sends one email (*mailer.SMTP).
type Mailer interface {
	Send(ctx context.Context, m mailer.Message) error
}

// Options tune the worker; zero values take the defaults below.
type Options struct {
	Batch           int           // rows claimed per tick (20)
	Concurrency     int           // parallel deliveries (4)
	Poll            time.Duration // idle sleep (15s)
	Lease           time.Duration // 'sending' rows older than this are reclaimed (10m)
	ItemTimeout     time.Duration // per-row delivery budget (60s)
	MaxAttempts     int           // attempts before 'failed' (6)
	BaseBackoff     time.Duration // first retry delay, doubled per attempt (1m)
	MaxBackoff      time.Duration // retry delay cap (6h)
	MaxAge          time.Duration // undelivered rows older than this are cancelled (48h)
	MaxPushFailures int           // consecutive rejects before a subscription is dropped (5)
}

func (o Options) withDefaults() Options {
	def := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	defi := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	defi(&o.Batch, 20)
	defi(&o.Concurrency, 4)
	defi(&o.MaxAttempts, 6)
	defi(&o.MaxPushFailures, 5)
	def(&o.Poll, 15*time.Second)
	def(&o.Lease, 10*time.Minute)
	def(&o.ItemTimeout, 60*time.Second)
	def(&o.BaseBackoff, time.Minute)
	def(&o.MaxBackoff, 6*time.Hour)
	def(&o.MaxAge, 48*time.Hour)
	return o
}

// Worker delivers outbox rows. Push and/or Mail may be nil (channel disabled).
type Worker struct {
	Store    Store
	Push     Pusher
	Mail     Mailer
	Renderer *Renderer
	Opts     Options
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// Channels are the outbox channels this worker delivers.
func (w *Worker) Channels() []string {
	var ch []string
	if w.Push != nil {
		ch = append(ch, "push")
	}
	if w.Mail != nil {
		ch = append(ch, "email")
	}
	return ch
}

// Run loops until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	w.Opts = w.Opts.withDefaults()
	for ctx.Err() == nil {
		n, err := w.Tick(ctx)
		if err != nil {
			w.logf("notifier: %v", err)
		}
		if n == 0 || err != nil {
			select {
			case <-ctx.Done():
			case <-time.After(w.Opts.Poll):
			}
		}
	}
}

// Tick reclaims stale leases, claims one batch and delivers it. It returns
// the number of rows claimed.
func (w *Worker) Tick(ctx context.Context) (int, error) {
	w.Opts = w.Opts.withDefaults()
	channels := w.Channels()
	if len(channels) == 0 {
		return 0, nil
	}
	if n, err := w.Store.ReclaimStaleOutbox(ctx, w.Opts.Lease); err != nil {
		return 0, fmt.Errorf("reclaim: %w", err)
	} else if n > 0 {
		w.logf("notifier: reclaimed %d stale outbox rows", n)
	}
	items, err := w.Store.ClaimOutbox(ctx, channels, w.Opts.Batch)
	if err != nil {
		return 0, fmt.Errorf("claim: %w", err)
	}
	sem := make(chan struct{}, w.Opts.Concurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		sem <- struct{}{}
		wg.Add(1)
		go func(it store.OutboxItem) {
			defer func() { <-sem; wg.Done() }()
			w.process(ctx, it)
		}(it)
	}
	wg.Wait()
	return len(items), nil
}

// process delivers one claimed row and records the result.
func (w *Worker) process(ctx context.Context, it store.OutboxItem) {
	start := w.now()
	ictx, cancel := context.WithTimeout(ctx, w.Opts.ItemTimeout)
	res := w.deliver(ictx, it)
	cancel()
	if res.Status == store.OutboxPending && it.Attempt >= w.Opts.MaxAttempts {
		res = store.OutboxResult{Status: store.OutboxFailed,
			Error: fmt.Sprintf("gave up after %d attempts: %s", it.Attempt, res.Error)}
	}
	// Finish even if ctx was cancelled mid-delivery (shutdown), so the row is
	// not left in 'sending' until the lease expires.
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer fcancel()
	ok, err := w.Store.FinishOutbox(fctx, it, res)
	switch {
	case err != nil:
		w.logf("notifier: finish outbox id=%d: %v", it.ID, err)
	case !ok:
		w.logf("notifier: outbox id=%d lease lost; result %s dropped", it.ID, res.Status)
	default:
		w.logf("notifier: outbox id=%d user=%d channel=%s attempt=%d status=%s duration=%s error=%q",
			it.ID, it.UserID, it.Channel, it.Attempt, res.Status, w.now().Sub(start).Round(time.Millisecond), res.Error)
	}
}

// Backoff is the retry delay after attempt (1-based): base * 2^(attempt-1),
// capped at max, and never shorter than the push service's Retry-After.
func Backoff(attempt int, base, max, retryAfter time.Duration) time.Duration {
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if retryAfter > d {
		d = retryAfter
	}
	return d
}

func (w *Worker) retry(it store.OutboxItem, msg string, retryAfter time.Duration) store.OutboxResult {
	return store.OutboxResult{Status: store.OutboxPending, Error: msg,
		RetryAt: w.now().Add(Backoff(it.Attempt, w.Opts.BaseBackoff, w.Opts.MaxBackoff, retryAfter))}
}

func cancelled(msg string) store.OutboxResult {
	return store.OutboxResult{Status: store.OutboxCancelled, Error: msg}
}

func failed(msg string) store.OutboxResult {
	return store.OutboxResult{Status: store.OutboxFailed, Error: msg}
}

func (w *Worker) deliver(ctx context.Context, it store.OutboxItem) store.OutboxResult {
	if age := w.now().Sub(it.CreatedAt); age > w.Opts.MaxAge {
		return cancelled(fmt.Sprintf("stale: queued %s ago", age.Round(time.Minute)))
	}
	p, err := ParsePayload(it.Payload)
	if err != nil {
		return failed(err.Error())
	}
	enabled := false
	for _, k := range p.PrefKinds() {
		if it.Settings.NotifyPrefs.Enabled(k, it.Channel) {
			enabled = true
		}
	}
	if !enabled {
		return cancelled("channel disabled in notify_prefs")
	}
	c, err := w.Renderer.Render(p, it.Settings.UILang, it.DisplayName)
	if err != nil {
		return failed("render: " + err.Error())
	}
	switch it.Channel {
	case "push":
		if w.Push == nil {
			return w.retry(it, "push not configured", 0)
		}
		return w.deliverPush(ctx, it, p, c)
	case "email":
		if w.Mail == nil {
			return w.retry(it, "email not configured", 0)
		}
		return w.deliverEmail(ctx, it, c)
	}
	return failed("unknown channel " + it.Channel)
}

func (w *Worker) deliverPush(ctx context.Context, it store.OutboxItem, p Payload, c Content) store.OutboxResult {
	subs, err := w.Store.PushSubscriptions(ctx, it.UserID)
	if err != nil {
		return w.retry(it, "load subscriptions: "+err.Error(), 0)
	}
	if len(subs) == 0 {
		return cancelled("no push subscriptions")
	}
	body, err := PushJSON(c, p.TemplateKind(), p.Count)
	if err != nil {
		return failed(err.Error())
	}
	var delivered, gone, transient int
	var retryAfter time.Duration
	var retryErr, rejectErr string
	for _, sub := range subs {
		host := webpush.EndpointHost(sub.Endpoint)
		res, err := w.Push.Send(ctx, webpush.Subscription{Endpoint: sub.Endpoint,
			Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}}, body)
		switch res.Outcome {
		case webpush.Delivered:
			delivered++
			if err := w.Store.PushDelivered(ctx, sub.ID); err != nil {
				w.logf("notifier: push sub=%d delivered bookkeeping: %v", sub.ID, err)
			}
		case webpush.Gone:
			gone++
			w.logf("notifier: push sub=%d host=%s status=%d: subscription gone, deleting", sub.ID, host, res.Status)
			if err := w.Store.PushGone(ctx, sub.ID); err != nil {
				w.logf("notifier: delete push sub=%d: %v", sub.ID, err)
			}
		case webpush.Retry:
			transient++
			retryAfter = max(retryAfter, res.RetryAfter)
			retryErr = pushErr(host, res, err)
		case webpush.Rejected:
			rejectErr = pushErr(host, res, err)
			dropped, ferr := w.Store.PushFailed(ctx, sub.ID, w.Opts.MaxPushFailures)
			if ferr != nil {
				w.logf("notifier: push sub=%d failure bookkeeping: %v", sub.ID, ferr)
			} else if dropped {
				w.logf("notifier: push sub=%d host=%s dropped after repeated rejects", sub.ID, host)
			}
		}
	}
	switch {
	case delivered > 0:
		// At least one device got it. Retrying would duplicate the message
		// on the devices that already accepted it.
		return store.OutboxResult{Status: store.OutboxSent}
	case transient > 0:
		return w.retry(it, retryErr, retryAfter)
	case gone == len(subs):
		return cancelled("all push subscriptions expired")
	}
	return failed(rejectErr)
}

// pushErr describes a failed push without the endpoint (a capability URL).
func pushErr(host string, res webpush.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("push %s: %v", host, err)
	}
	return fmt.Sprintf("push %s: HTTP %d", host, res.Status)
}

func (w *Worker) deliverEmail(ctx context.Context, it store.OutboxItem, c Content) store.OutboxResult {
	if it.Email == "" {
		return cancelled("user has no email address")
	}
	err := w.Mail.Send(ctx, mailer.Message{To: it.Email, Subject: c.Subject, Text: c.Text, HTML: c.HTML,
		ListUnsubscribe: c.SettingsURL})
	switch {
	case err == nil:
		return store.OutboxResult{Status: store.OutboxSent}
	case mailer.IsPermanent(err):
		return failed("smtp: " + err.Error())
	case errors.Is(err, context.Canceled):
		return w.retry(it, "interrupted", 0)
	}
	return w.retry(it, "smtp: "+err.Error(), 0)
}
