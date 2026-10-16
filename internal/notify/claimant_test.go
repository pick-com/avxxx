package notify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/store"
)

var takedownLangs = []string{"zh-CN", "en", "ja", "ko", "vi", "es", "th"}

var takedownStatuses = []string{"received", "reviewing", "actioned", "rejected", "counter_received", "restored", "closed"}

func TestTakedownTemplatesEveryLanguage(t *testing.T) {
	r := renderer(t)
	for _, lang := range takedownLangs {
		for _, kind := range []string{KindTakedownReceived, KindTakedownCounter} {
			if got := r.Lang(kind, lang); got != lang {
				t.Errorf("%s: no %s template", kind, lang)
			}
			msg, err := r.RenderTakedown(store.MailOutboxItem{Kind: kind, Lang: lang, Ticket: "TD-AAAA-BBBB-CCCC-DDDD", Name: "Ann", Recipient: "claimant"})
			if err != nil {
				t.Fatalf("%s %s: %v", kind, lang, err)
			}
			if !strings.Contains(msg.Subject, "TD-AAAA-BBBB-CCCC-DDDD") || !strings.Contains(msg.Text, "TD-AAAA-BBBB-CCCC-DDDD") || msg.HTML != "" {
				t.Errorf("%s %s: %+v", kind, lang, msg)
			}
			if !strings.Contains(msg.Text, "https://reel.example/copyright?tab=lookup&ticket=TD-AAAA-BBBB-CCCC-DDDD") {
				t.Errorf("%s %s: status link missing:\n%s", kind, lang, msg.Text)
			}
		}
		seen := map[string]bool{}
		for _, st := range takedownStatuses {
			for _, rcpt := range []string{"claimant", "counter"} {
				msg, err := r.RenderTakedown(store.MailOutboxItem{Kind: KindTakedownStatus, Lang: lang, Ticket: "TD-1", Status: st, Recipient: rcpt})
				if err != nil {
					t.Fatalf("status %s %s: %v", st, lang, err)
				}
				if strings.Contains(msg.Subject, st) && st != "closed" { // the code itself means no label
					t.Errorf("status %s %s: unlabelled subject %q", st, lang, msg.Subject)
				}
				if strings.ContainsAny(msg.Subject, "\r\n") {
					t.Errorf("subject has line breaks: %q", msg.Subject)
				}
				seen[msg.Subject] = true
			}
		}
		if len(seen) != len(takedownStatuses) {
			t.Errorf("%s: %d distinct status subjects, want %d", lang, len(seen), len(takedownStatuses))
		}
	}
	// zh-TW and unknown languages fall back to the default.
	if got := r.Lang(KindTakedownStatus, "zh-TW"); got != DefaultLang {
		t.Errorf("zh-TW -> %s", got)
	}
	en, _ := r.RenderTakedown(store.MailOutboxItem{Kind: KindTakedownStatus, Lang: "en", Ticket: "TD-1", Status: "actioned", Recipient: "counter"})
	if en.Subject != "Copyright notice TD-1: Taken down" || !strings.Contains(en.Text, "that you filed a counter-notice for") {
		t.Errorf("en counter status mail = %+v", en)
	}
	if _, err := r.RenderTakedown(store.MailOutboxItem{Kind: "new_work"}); err == nil {
		t.Error("non-mail kind rendered")
	}
}

type fakeMailStore struct {
	*fakeStore
	mu    sync.Mutex
	mail  []store.MailOutboxItem
	done  map[int64]store.OutboxResult
	calls int
}

func (f *fakeMailStore) ReclaimStaleMailOutbox(context.Context, time.Duration) (int64, error) {
	return 0, nil
}
func (f *fakeMailStore) ClaimMailOutbox(_ context.Context, limit int) ([]store.MailOutboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	out := f.mail
	if len(out) > limit {
		out = out[:limit]
	}
	f.mail = f.mail[len(out):]
	for i := range out {
		out[i].Attempt++
	}
	return out, nil
}
func (f *fakeMailStore) FinishMailOutbox(_ context.Context, it store.MailOutboxItem, r store.OutboxResult) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.done[it.ID] = r
	return true, nil
}

func TestTakedownMailDelivery(t *testing.T) {
	r := renderer(t)
	mk := func(id int64, kind, lang, email string) store.MailOutboxItem {
		return store.MailOutboxItem{ID: id, Kind: kind, Recipient: "claimant", Status: "actioned", Ticket: "TD-X", Email: email, Lang: lang, CreatedAt: t0.Add(-time.Hour)}
	}
	stale := mk(4, KindTakedownStatus, "en", "a@example.com")
	stale.CreatedAt = t0.Add(-72 * time.Hour)
	fs := &fakeMailStore{fakeStore: newFakeStore(), done: map[int64]store.OutboxResult{}, mail: []store.MailOutboxItem{
		mk(1, KindTakedownReceived, "ja", "claimant@example.com"),
		mk(2, KindTakedownStatus, "es", "b@example.com"),
		mk(3, KindTakedownStatus, "en", ""), // address purged from the ticket
		stale,
	}}
	m := &fakeMailer{}
	n, err := worker(fs, nil, m, r).Tick(context.Background())
	if err != nil || n != 4 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	for id, want := range map[int64]string{1: "sent", 2: "sent", 3: "cancelled", 4: "cancelled"} {
		if fs.done[id].Status != want {
			t.Errorf("mail %d = %+v, want %s", id, fs.done[id], want)
		}
	}
	if len(m.sent) != 2 {
		t.Fatalf("sent %d", len(m.sent))
	}
	for _, s := range m.sent {
		if s.To == "" || strings.Contains(s.Text, "@example.com") || s.ListUnsubscribe != "" {
			t.Errorf("mail %+v: the address must only be the recipient", s)
		}
	}

	// Without a mailer the mail outbox is not claimed at all.
	fs2 := &fakeMailStore{fakeStore: newFakeStore(), done: map[int64]store.OutboxResult{}, mail: []store.MailOutboxItem{mk(9, KindTakedownReceived, "en", "x@example.com")}}
	if n, _ := worker(fs2, nil, nil, r).Tick(context.Background()); n != 0 || fs2.calls != 0 {
		t.Errorf("claimed without a mailer: n=%d calls=%d", n, fs2.calls)
	}

	// SMTP failures: transient retries with backoff, permanent fails.
	fs3 := &fakeMailStore{fakeStore: newFakeStore(), done: map[int64]store.OutboxResult{}, mail: []store.MailOutboxItem{mk(5, KindTakedownReceived, "en", "x@example.com")}}
	if _, err := worker(fs3, nil, &fakeMailer{err: errors.New("421 try later")}, r).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := fs3.done[5]; res.Status != store.OutboxPending || !res.RetryAt.After(t0) {
		t.Errorf("transient = %+v", res)
	}
	fs4 := &fakeMailStore{fakeStore: newFakeStore(), done: map[int64]store.OutboxResult{}, mail: []store.MailOutboxItem{mk(6, KindTakedownReceived, "en", "x@example.com")}}
	if _, err := worker(fs4, nil, &fakeMailer{err: &mailer.PermanentError{Err: errors.New("550 no such user")}}, r).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := fs4.done[6]; res.Status != store.OutboxFailed {
		t.Errorf("permanent = %+v", res)
	}
}
