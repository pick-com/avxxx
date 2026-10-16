package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"avxxx/internal/mailer"
	"avxxx/internal/store"
	"avxxx/internal/webpush"
)

var t0 = time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

func payload(reasons ...string) json.RawMessage {
	var items []map[string]any
	for i, r := range reasons {
		items = append(items, map[string]any{"release_id": i + 1, "slug": fmt.Sprintf("abc-%d", i+1),
			"title": fmt.Sprintf("Title %d", i+1), "reason": r,
			"performers": []map[string]string{{"slug": "p", "name": "演员甲"}}})
	}
	b, _ := json.Marshal(map[string]any{"kind": "new_work", "items": items, "count": len(reasons)})
	return b
}

func renderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := NewRenderer("https://reel.example/")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRenderDigestAndLanguages(t *testing.T) {
	r := renderer(t)
	p, err := ParsePayload(payload("follow", "reservation", "follow"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.Render(p, "zh-CN", "小明")
	if err != nil {
		t.Fatal(err)
	}
	if c.Lang != "zh-CN" || c.Subject != "你关注的演员有 3 部新作" {
		t.Errorf("subject = %q (%s)", c.Subject, c.Lang)
	}
	for _, want := range []string{"小明，你好", "- Title 1（演员甲）", "https://reel.example/releases/abc-2", "[已预约]",
		"https://reel.example/settings/notifications"} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("text missing %q:\n%s", want, c.Text)
		}
	}
	if !strings.Contains(c.HTML, `<a href="https://reel.example/releases/abc-1">Title 1</a>`) {
		t.Errorf("html:\n%s", c.HTML)
	}
	if c.PushTitle != "3 部新作上线" || c.PushBody != "Title 1、Title 2、Title 3" || c.URL != "https://reel.example/following" {
		t.Errorf("push = %q / %q / %q", c.PushTitle, c.PushBody, c.URL)
	}

	en, _ := r.Render(p, "en-US", "")
	if en.Lang != "en" || en.Subject != "3 new releases from performers you follow" || !strings.HasPrefix(en.Text, "Hi,") {
		t.Errorf("en-US -> %s %q %q", en.Lang, en.Subject, en.Text[:10])
	}
	ja, _ := r.Render(p, "ja", "")
	if ja.Lang != "zh-CN" {
		t.Errorf("unknown ui_lang must fall back to zh-CN, got %s", ja.Lang)
	}

	one, _ := ParsePayload(payload("reservation"))
	if one.TemplateKind() != KindReservation {
		t.Fatal("all-reservation payload must use the reservation template")
	}
	c, _ = r.Render(one, "", "")
	if c.Subject != "你预约的《Title 1》已上线" || c.URL != "https://reel.example/releases/abc-1" {
		t.Errorf("reservation = %q %q", c.Subject, c.URL)
	}
}

func TestRenderEscapesAndTruncates(t *testing.T) {
	r := renderer(t)
	raw, _ := json.Marshal(map[string]any{"kind": "new_work", "count": 25, "items": []map[string]any{
		{"slug": "x/../y", "title": "<script>alert(1)</script>\r\nBcc: x", "reason": "follow"}}})
	p, _ := ParsePayload(raw)
	c, err := r.Render(p, "en", "<b>")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.HTML, "<script>") || strings.Contains(c.HTML, "<b>,") {
		t.Errorf("html not escaped:\n%s", c.HTML)
	}
	if strings.ContainsAny(c.Subject, "\r\n") {
		t.Errorf("subject has line breaks: %q", c.Subject)
	}
	if !strings.Contains(c.Text, "...and 24 more: https://reel.example/following") {
		t.Errorf("text:\n%s", c.Text)
	}
	if !strings.Contains(c.Text, "/releases/x%2F..%2Fy") {
		t.Errorf("slug not path-escaped:\n%s", c.Text)
	}
	if _, err := ParsePayload([]byte(`{"items":[]}`)); err == nil {
		t.Error("empty payload must fail")
	}
}

// --- fakes -------------------------------------------------------------------

type fakeStore struct {
	mu        sync.Mutex
	items     []store.OutboxItem
	subs      map[int64][]store.PushSubscription
	finished  map[int64]store.OutboxResult
	gone      []int64
	failures  map[int64]int
	ok        map[int64]int
	lostLease bool
}

func newFakeStore(items ...store.OutboxItem) *fakeStore {
	return &fakeStore{items: items, subs: map[int64][]store.PushSubscription{},
		finished: map[int64]store.OutboxResult{}, failures: map[int64]int{}, ok: map[int64]int{}}
}

func (f *fakeStore) ReclaimStaleOutbox(context.Context, time.Duration) (int64, error) { return 0, nil }
func (f *fakeStore) ClaimOutbox(_ context.Context, channels []string, limit int) ([]store.OutboxItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out, rest []store.OutboxItem
	for _, it := range f.items {
		ok := false
		for _, c := range channels {
			ok = ok || c == it.Channel
		}
		if ok && len(out) < limit {
			it.Attempt++
			out = append(out, it)
		} else {
			rest = append(rest, it)
		}
	}
	f.items = rest
	return out, nil
}
func (f *fakeStore) FinishOutbox(_ context.Context, it store.OutboxItem, r store.OutboxResult) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lostLease {
		return false, nil
	}
	f.finished[it.ID] = r
	return true, nil
}
func (f *fakeStore) PushSubscriptions(_ context.Context, uid int64) ([]store.PushSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subs[uid], nil
}
func (f *fakeStore) PushDelivered(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ok[id]++
	return nil
}
func (f *fakeStore) PushGone(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gone = append(f.gone, id)
	return nil
}
func (f *fakeStore) PushFailed(_ context.Context, id int64, max int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[id]++
	return f.failures[id] >= max, nil
}

type fakeMailer struct {
	mu   sync.Mutex
	sent []mailer.Message
	err  error
}

func (m *fakeMailer) Send(_ context.Context, msg mailer.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

func item(id int64, channel string, p json.RawMessage) store.OutboxItem {
	st := store.DefaultSettings()
	st.NotifyPrefs["new_work"]["email"] = true
	st.NotifyPrefs["reservation"]["email"] = true
	return store.OutboxItem{ID: id, UserID: 7, Channel: channel, Payload: p, CreatedAt: t0.Add(-time.Hour),
		Email: "fan@example.com", DisplayName: "Fan", Settings: st}
}

func worker(s Store, p Pusher, m Mailer, r *Renderer) *Worker {
	w := &Worker{Store: s, Renderer: r, Now: func() time.Time { return t0 }, Logf: func(string, ...any) {}}
	if p != nil {
		w.Push = p
	}
	if m != nil {
		w.Mail = m
	}
	return w
}

// --- email -------------------------------------------------------------------

func TestEmailDelivery(t *testing.T) {
	r := renderer(t)
	ja := item(2, "email", payload("follow"))
	ja.Settings.UILang = "en"
	off := item(3, "email", payload("follow"))
	off.Settings.NotifyPrefs["new_work"]["email"] = false
	noAddr := item(4, "email", payload("follow"))
	noAddr.Email = ""
	stale := item(5, "email", payload("follow"))
	stale.CreatedAt = t0.Add(-72 * time.Hour)
	pushRow := item(6, "push", payload("follow"))
	fs := newFakeStore(item(1, "email", payload("follow", "follow")), ja, off, noAddr, stale, pushRow)
	m := &fakeMailer{}
	n, err := worker(fs, nil, m, r).Tick(context.Background())
	if err != nil || n != 5 {
		t.Fatalf("Tick = %d, %v (push rows must not be claimed without a pusher)", n, err)
	}
	want := map[int64]string{1: "sent", 2: "sent", 3: "cancelled", 4: "cancelled", 5: "cancelled"}
	for id, st := range want {
		if fs.finished[id].Status != st {
			t.Errorf("row %d = %+v, want %s", id, fs.finished[id], st)
		}
	}
	if len(fs.items) != 1 || fs.items[0].ID != 6 {
		t.Errorf("push row should stay pending: %+v", fs.items)
	}
	if len(m.sent) != 2 {
		t.Fatalf("sent %d mails", len(m.sent))
	}
	var zh, en mailer.Message
	for _, s := range m.sent {
		if strings.HasPrefix(s.Subject, "New release") {
			en = s
		} else {
			zh = s
		}
	}
	if zh.Subject != "你关注的演员有 2 部新作" || zh.To != "fan@example.com" || zh.HTML == "" ||
		zh.ListUnsubscribe != "https://reel.example/settings/notifications" {
		t.Errorf("zh mail = %+v", zh)
	}
	if en.Subject != "New release: Title 1" {
		t.Errorf("en mail = %q", en.Subject)
	}
}

func TestEmailFailuresAndBackoff(t *testing.T) {
	r := renderer(t)
	m := &fakeMailer{err: errors.New("dial tcp: connection refused")}
	it := item(1, "email", payload("follow"))
	fs := newFakeStore(it)
	w := worker(fs, nil, m, r)
	w.Tick(context.Background())
	got := fs.finished[1]
	if got.Status != "pending" || !got.RetryAt.Equal(t0.Add(time.Minute)) || !strings.Contains(got.Error, "connection refused") {
		t.Fatalf("first transient failure = %+v", got)
	}

	it.Attempt = 3 // claim makes it 4: 1m * 2^3
	fs = newFakeStore(it)
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[1]; got.Status != "pending" || !got.RetryAt.Equal(t0.Add(8*time.Minute)) {
		t.Fatalf("4th attempt = %+v", got)
	}

	it.Attempt = 5 // claim makes it 6 = MaxAttempts
	fs = newFakeStore(it)
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[1]; got.Status != "failed" || !strings.HasPrefix(got.Error, "gave up after 6 attempts") {
		t.Fatalf("last attempt = %+v", got)
	}

	m.err = &mailer.PermanentError{Err: errors.New("550 no such user")}
	fs = newFakeStore(item(1, "email", payload("follow")))
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[1]; got.Status != "failed" {
		t.Fatalf("permanent = %+v", got)
	}

	fs = newFakeStore(item(1, "email", payload("follow")))
	fs.lostLease = true
	w.Store = fs
	m.err = nil
	if n, _ := w.Tick(context.Background()); n != 1 || len(fs.finished) != 0 {
		t.Fatal("a lost lease must drop the result")
	}
}

func TestBackoff(t *testing.T) {
	for _, tc := range []struct {
		attempt  int
		ra, want time.Duration
	}{
		{1, 0, time.Minute}, {2, 0, 2 * time.Minute}, {5, 0, 16 * time.Minute},
		{20, 0, 6 * time.Hour}, {1, 10 * time.Minute, 10 * time.Minute},
	} {
		if got := Backoff(tc.attempt, time.Minute, 6*time.Hour, tc.ra); got != tc.want {
			t.Errorf("Backoff(%d, ra=%v) = %v, want %v", tc.attempt, tc.ra, got, tc.want)
		}
	}
}

// --- push (real webpush.Client against an httptest push service) -------------

type fakePushService struct {
	*httptest.Server
	mu     sync.Mutex
	status map[string]int // path -> status
	bodies map[string][]byte
	auth   map[string]string
}

func newFakePushService(t *testing.T) *fakePushService {
	f := &fakePushService{status: map[string]int{}, bodies: map[string][]byte{}, auth: map[string]string{}}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		b, _ := io.ReadAll(r.Body)
		f.bodies[r.URL.Path] = b
		f.auth[r.URL.Path] = r.Header.Get("Authorization")
		if r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
			w.WriteHeader(400)
			return
		}
		st := f.status[r.URL.Path]
		if st == 0 {
			st = 201
		}
		if st == 429 {
			w.Header().Set("Retry-After", "600")
		}
		w.WriteHeader(st)
	}))
	t.Cleanup(f.Close)
	return f
}

type device struct {
	priv *ecdh.PrivateKey
	auth []byte
	sub  store.PushSubscription
}

func newDevice(t *testing.T, id int64, endpoint string) device {
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	return device{priv, auth, store.PushSubscription{ID: id, UserID: 7, Endpoint: endpoint,
		P256dh: webpush.EncodeB64(priv.PublicKey().Bytes()), Auth: webpush.EncodeB64(auth)}}
}

func pushClient(t *testing.T, ps *fakePushService) *webpush.Client {
	pub, priv, _ := webpush.GenerateVAPID()
	v, err := webpush.NewVAPID(pub, priv, "mailto:ops@reel.example")
	if err != nil {
		t.Fatal(err)
	}
	return &webpush.Client{VAPID: v, HTTP: ps.Client(), TTL: time.Hour}
}

func TestPushDelivery(t *testing.T) {
	r := renderer(t)
	ps := newFakePushService(t)
	client := pushClient(t, ps)
	ok := newDevice(t, 1, ps.URL+"/ok")
	gone := newDevice(t, 2, ps.URL+"/gone")
	expired := newDevice(t, 3, ps.URL+"/expired")
	ps.status["/gone"], ps.status["/expired"] = 410, 404

	fs := newFakeStore(item(10, "push", payload("follow", "reservation")))
	fs.subs[7] = []store.PushSubscription{ok.sub, gone.sub, expired.sub}
	var logs []string
	w := worker(fs, client, nil, r)
	w.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	if _, err := w.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fs.finished[10]; got.Status != "sent" {
		t.Fatalf("result = %+v", got)
	}
	if fs.ok[1] != 1 || len(fs.gone) != 2 {
		t.Errorf("ok=%v gone=%v", fs.ok, fs.gone)
	}
	pt, err := webpush.Decrypt(ps.bodies["/ok"], ok.priv, ok.auth)
	if err != nil {
		t.Fatal(err)
	}
	var msg PushMessage
	json.Unmarshal(pt, &msg)
	if msg.Title != "2 部新作上线" || msg.URL != "https://reel.example/following" || msg.Count != 2 || msg.Tag != "new_work" {
		t.Errorf("push message = %+v", msg)
	}
	if !strings.HasPrefix(ps.auth["/ok"], "vapid t=") {
		t.Errorf("Authorization = %q", ps.auth["/ok"])
	}
	for _, l := range logs {
		if strings.Contains(l, "/gone") || strings.Contains(l, "/expired") || strings.Contains(l, "/ok") {
			t.Errorf("log line leaks an endpoint path: %s", l)
		}
	}

	// Only expired subscriptions: cancelled.
	fs = newFakeStore(item(11, "push", payload("follow")))
	fs.subs[7] = []store.PushSubscription{gone.sub}
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[11]; got.Status != "cancelled" {
		t.Errorf("all gone = %+v", got)
	}

	// No subscriptions: cancelled.
	fs = newFakeStore(item(12, "push", payload("follow")))
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[12]; got.Status != "cancelled" || got.Error != "no push subscriptions" {
		t.Errorf("no subs = %+v", got)
	}
}

func TestPushRetryAndReject(t *testing.T) {
	r := renderer(t)
	ps := newFakePushService(t)
	client := pushClient(t, ps)
	busy := newDevice(t, 1, ps.URL+"/busy")
	bad := newDevice(t, 2, ps.URL+"/bad")
	ps.status["/busy"], ps.status["/bad"] = 429, 403

	fs := newFakeStore(item(20, "push", payload("follow")))
	fs.subs[7] = []store.PushSubscription{busy.sub, bad.sub}
	w := worker(fs, client, nil, r)
	w.Tick(context.Background())
	got := fs.finished[20]
	if got.Status != "pending" || !got.RetryAt.Equal(t0.Add(10*time.Minute)) || !strings.Contains(got.Error, "HTTP 429") {
		t.Fatalf("429 must retry honouring Retry-After: %+v", got)
	}
	if strings.Contains(got.Error, "/busy") {
		t.Errorf("last_error leaks endpoint: %q", got.Error)
	}
	if fs.failures[2] != 1 {
		t.Errorf("403 must count a subscription failure: %v", fs.failures)
	}

	// Only rejects: failed, and the subscription is dropped at the threshold.
	w.Opts.MaxPushFailures = 2
	fs = newFakeStore(item(21, "push", payload("follow")))
	fs.subs[7] = []store.PushSubscription{bad.sub}
	fs.failures[2] = 1
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[21]; got.Status != "failed" || !strings.Contains(got.Error, "HTTP 403") {
		t.Errorf("reject = %+v", got)
	}

	// Disabled push prefs: cancelled without contacting the service.
	off := item(22, "push", payload("follow"))
	off.Settings.NotifyPrefs["new_work"]["push"] = false
	fs = newFakeStore(off)
	fs.subs[7] = []store.PushSubscription{busy.sub}
	w.Store = fs
	w.Tick(context.Background())
	if got := fs.finished[22]; got.Status != "cancelled" {
		t.Errorf("disabled = %+v", got)
	}
}

func TestLoadConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := LoadConfig(env(nil))
	if err != nil || c.Push != nil || c.Mail != nil {
		t.Fatalf("empty env must disable both channels: %+v %v", c, err)
	}
	pub, priv, _ := webpush.GenerateVAPID()
	c, err = LoadConfig(env(map[string]string{"VAPID_PUBLIC_KEY": pub, "VAPID_PRIVATE_KEY": priv,
		"VAPID_SUBJECT": "mailto:ops@reel.example", "NOTIFY_MAX_ATTEMPTS": "3"}))
	if err != nil || c.Push == nil || c.Mail != nil || c.Opts.MaxAttempts != 3 {
		t.Fatalf("push only: %+v %v", c, err)
	}
	_, err = LoadConfig(env(map[string]string{"VAPID_PUBLIC_KEY": pub}))
	if err == nil {
		t.Error("partial VAPID config must fail")
	}
	_, err = LoadConfig(env(map[string]string{"VAPID_PUBLIC_KEY": pub, "VAPID_PRIVATE_KEY": "bad" + priv[3:], "VAPID_SUBJECT": "mailto:a@b"}))
	if err == nil || strings.Contains(err.Error(), priv[3:]) {
		t.Errorf("bad key error must not echo the key: %v", err)
	}
	_, err = LoadConfig(env(map[string]string{"SMTP_HOST": "smtp.example", "MAIL_FROM": "a@b.c"}))
	if err == nil || !strings.Contains(err.Error(), "BASE_URL") {
		t.Errorf("email without BASE_URL: %v", err)
	}
	c, err = LoadConfig(env(map[string]string{"SMTP_HOST": "smtp.example", "MAIL_FROM": "REEL <a@b.c>",
		"SMTP_USER": "u", "SMTP_PASSWORD": "p", "BASE_URL": "https://reel.example/"}))
	if err != nil || c.Mail == nil || c.BaseURL != "https://reel.example" {
		t.Errorf("email: %+v %v", c, err)
	}
	if _, err = LoadConfig(env(map[string]string{"NOTIFY_POLL": "soon"})); err == nil {
		t.Error("bad duration must fail")
	}
}
