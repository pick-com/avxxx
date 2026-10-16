package notify

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// resetStore is a fakeStore that also mints reset tokens.
type resetStore struct {
	*fakeStore
	mu     sync.Mutex // the worker delivers concurrently
	issued []int64
}

func (r *resetStore) IssuePasswordReset(_ context.Context, uid int64) (string, time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issued = append(r.issued, uid)
	return strings.Repeat("ab", 32), t0.Add(time.Hour), nil
}

func TestPasswordResetMail(t *testing.T) {
	reset := json.RawMessage(`{"kind":"password_reset"}`)
	row := item(1, "email", reset)
	// Transactional: email notify prefs are off by default and must not matter.
	row.Settings.NotifyPrefs["new_work"]["email"] = false
	row.Settings.NotifyPrefs["reservation"]["email"] = false
	en := item(2, "email", reset)
	en.Settings.UILang = "en"
	stale := item(3, "email", reset)
	stale.CreatedAt = t0.Add(-2 * time.Hour)
	fs := &resetStore{fakeStore: newFakeStore(row, en, stale)}
	m := &fakeMailer{}
	if _, err := worker(fs, nil, m, renderer(t)).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.finished[1].Status != "sent" || fs.finished[2].Status != "sent" || fs.finished[3].Status != "cancelled" {
		t.Fatalf("results: %+v", fs.finished)
	}
	if len(fs.issued) != 2 || len(m.sent) != 2 {
		t.Fatalf("issued %v, sent %d", fs.issued, len(m.sent))
	}
	link := "https://reel.example/reset-password#token=" + strings.Repeat("ab", 32)
	for _, msg := range m.sent {
		if msg.To != "fan@example.com" || !strings.Contains(msg.Text, link) || !strings.Contains(msg.HTML, link) || msg.ListUnsubscribe != "" {
			t.Errorf("mail = %+v", msg)
		}
	}
	subjects := m.sent[0].Subject + "|" + m.sent[1].Subject
	if !strings.Contains(subjects, "重置你的 REEL 密码") || !strings.Contains(subjects, "Reset your REEL password") {
		t.Errorf("subjects %q", subjects)
	}
}

func TestReminderTemplateAndPrefs(t *testing.T) {
	p, err := ParsePayload(payload("reservation_reminder"))
	if err != nil {
		t.Fatal(err)
	}
	if p.TemplateKind() != KindReminder || len(p.PrefKinds()) != 1 || p.PrefKinds()[0] != KindReservation {
		t.Fatalf("kind %s prefs %v", p.TemplateKind(), p.PrefKinds())
	}
	c, err := renderer(t).Render(p, "zh-CN", "")
	if err != nil || c.Subject != "你预约的《Title 1》明天上线" {
		t.Errorf("subject %q %v", c.Subject, err)
	}
	if mixed, _ := ParsePayload(payload("reservation", "reservation_reminder")); mixed.TemplateKind() != KindReservation {
		t.Errorf("mixed kind %s", mixed.TemplateKind())
	}
}

func TestPasswordResetNeedsIssuer(t *testing.T) {
	fs := newFakeStore(item(1, "email", json.RawMessage(`{"kind":"password_reset"}`)))
	if _, err := worker(fs, nil, &fakeMailer{}, renderer(t)).Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fs.finished[1].Status != "failed" {
		t.Errorf("without issuer: %+v", fs.finished[1])
	}
}
