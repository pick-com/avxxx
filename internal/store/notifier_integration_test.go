package store_test

// End-to-end notifier run (fan-out -> outbox -> notify.Worker) against a
// disposable database. Lives in the store test binary (external package so it
// can import internal/notify) so it never runs concurrently with the other
// integration tests, one of which drops the schema.

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/notify"
	"avxxx/internal/store"
	"avxxx/internal/webpush"
)

type recMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (m *recMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func TestNotifierIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	sfx := fmt.Sprint(time.Now().UnixNano())
	mustExec(`UPDATE notification_outbox SET status='cancelled' WHERE status IN ('pending','sending')`)
	mustExec(`UPDATE releases SET notified_at=now() WHERE notified_at IS NULL`)

	var uid, bounced, perf int64
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,'x','Fan') RETURNING id`, "fan"+sfx+"@example.test").Scan(&uid)
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "bounce"+sfx+"@example.test").Scan(&bounced)
	s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,'演员甲') RETURNING id`, "perf-"+sfx).Scan(&perf)
	prefs := `{"new_work":{"in_app":true,"push":true,"email":true}}`
	for _, u := range []int64{uid, bounced} {
		mustExec(`INSERT INTO user_settings(user_id,notify_prefs,quiet_hours) VALUES($1,$2,NULL)`, u, prefs)
		mustExec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$2)`, u, perf)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for i := range 2 {
		var rid int64
		s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$2,'published',$3) RETURNING id`,
			fmt.Sprintf("nw-%d-%s", i, sfx), fmt.Sprintf("新作 %d", i), today).Scan(&rid)
		mustExec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,0,true)`, rid, perf)
	}
	if res, err := s.NotifyFanout(ctx, 10); err != nil || res.Releases != 2 {
		t.Fatalf("fanout = %+v, %v", res, err)
	}
	var rows int
	s.QueryRow(ctx, `SELECT count(*) FROM notification_outbox WHERE user_id IN ($1,$2) AND status='pending'`, uid, bounced).Scan(&rows)
	if rows != 4 { // push + email per user, two releases merged per day
		t.Fatalf("pending outbox rows = %d, want 4", rows)
	}

	// Fake push service: /ok accepts, /gone is expired.
	var mu sync.Mutex
	bodies := map[string][]byte{}
	ps := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies[r.URL.Path] = b
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer ps.Close()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	p256dh, authB64 := webpush.EncodeB64(ua.PublicKey().Bytes()), webpush.EncodeB64(auth)
	s.SavePushSubscription(ctx, uid, ps.URL+"/ok/"+sfx, p256dh, authB64, "test")
	s.SavePushSubscription(ctx, uid, ps.URL+"/gone/"+sfx, p256dh, authB64, "test")
	s.SavePushSubscription(ctx, bounced, ps.URL+"/gone/b"+sfx, p256dh, authB64, "test")

	pub, priv, _ := webpush.GenerateVAPID()
	v, err := webpush.NewVAPID(pub, priv, "mailto:ops@example.test")
	if err != nil {
		t.Fatal(err)
	}
	r, err := notify.NewRenderer("https://reel.example")
	if err != nil {
		t.Fatal(err)
	}
	m := &recMailer{}
	w := &notify.Worker{Store: s, Renderer: r, Push: &webpush.Client{VAPID: v, HTTP: ps.Client()},
		Mail: m, Logf: t.Logf}
	// The bounced user's mailbox rejects permanently.
	failing := &failFor{inner: m, addr: "bounce" + sfx + "@example.test"}
	w.Mail = failing
	if n, err := w.Tick(ctx); err != nil || n != 4 {
		t.Fatalf("tick = %d, %v", n, err)
	}

	status := func(u int64, ch string) (string, string, int) {
		var st, le string
		var attempt int
		s.QueryRow(ctx, `SELECT status,last_error,attempt FROM notification_outbox WHERE user_id=$1 AND channel=$2 AND merged_key IS NOT NULL ORDER BY id DESC LIMIT 1`, u, ch).Scan(&st, &le, &attempt)
		return st, le, attempt
	}
	if st, le, _ := status(uid, "push"); st != "sent" {
		t.Errorf("fan push = %s %q", st, le)
	}
	if st, le, _ := status(uid, "email"); st != "sent" {
		t.Errorf("fan email = %s %q", st, le)
	}
	if st, le, _ := status(bounced, "push"); st != "cancelled" || le != "all push subscriptions expired" {
		t.Errorf("bounced push = %s %q", st, le)
	}
	if st, le, attempt := status(bounced, "email"); st != "failed" || !strings.Contains(le, "550") || attempt != 1 {
		t.Errorf("bounced email = %s %q attempt %d", st, le, attempt)
	}
	subs, _ := s.PushSubscriptions(ctx, uid)
	if len(subs) != 1 || !strings.Contains(subs[0].Endpoint, "/ok/") {
		t.Errorf("410 subscription must be deleted: %+v", subs)
	}
	pt, err := webpush.Decrypt(bodies["/ok/"+sfx], ua, auth)
	if err != nil || !strings.Contains(string(pt), `"count":2`) {
		t.Errorf("push payload = %s, %v", pt, err)
	}
	if len(m.sent) != 1 || m.sent[0].Subject != "你关注的演员有 2 部新作" || m.sent[0].To != "fan"+sfx+"@example.test" {
		t.Errorf("mails = %+v", m.sent)
	}
	if n, _ := w.Tick(ctx); n != 0 {
		t.Errorf("second tick claimed %d rows", n)
	}
}

// failFor fails permanently for one address and delegates otherwise.
type failFor struct {
	inner notify.Mailer
	addr  string
}

func (f *failFor) Send(ctx context.Context, m mailer.Message) error {
	if m.To == f.addr {
		return &mailer.PermanentError{Err: errors.New("550 5.1.1 mailbox unavailable")}
	}
	return f.inner.Send(ctx, m)
}
