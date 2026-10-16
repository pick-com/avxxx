package api

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"avxxx/internal/webpush"
)

type stubPush struct {
	saved   []string // "uid endpoint p256dh auth ua"
	deleted []string
}

func (s *stubPush) SavePushSubscription(_ context.Context, uid int64, ep, p256dh, auth, ua string) error {
	s.saved = append(s.saved, fmt.Sprint(uid, " ", ep, " ", p256dh, " ", auth, " ", ua))
	return nil
}

func (s *stubPush) DeletePushSubscription(_ context.Context, uid int64, ep string) (int64, error) {
	s.deleted = append(s.deleted, fmt.Sprint(uid, " ", ep))
	return 1, nil
}

func newPushServer(p *stubPush, cfg pushConfig) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, social: &stubSocial{}, push: p, pushCfg: cfg}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func clientKeys(t *testing.T) (string, string) {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return webpush.EncodeB64(k.PublicKey().Bytes()), webpush.EncodeB64(auth)
}

func TestPushConfigFromEnv(t *testing.T) {
	pub, _, _ := webpush.GenerateVAPID()
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c := pushConfigFromEnv(env(map[string]string{"VAPID_PUBLIC_KEY": pub}))
	if c.vapidPublicKey != pub || len(c.allowedHosts) != len(webpush.DefaultAllowedHosts) {
		t.Errorf("config = %+v", c)
	}
	if c := pushConfigFromEnv(env(map[string]string{"VAPID_PUBLIC_KEY": "nope"})); c.vapidPublicKey != "" {
		t.Error("invalid key must disable push")
	}
	if c := pushConfigFromEnv(env(map[string]string{"PUSH_ALLOWED_HOSTS": "*"})); c.allowedHosts != nil {
		t.Error("* must allow any host")
	}
	if c := pushConfigFromEnv(env(map[string]string{"PUSH_ALLOWED_HOSTS": " a.example , b.example"})); len(c.allowedHosts) != 2 || c.allowedHosts[1] != "b.example" {
		t.Errorf("hosts = %v", c.allowedHosts)
	}
}

func TestPushDisabledWithoutVAPID(t *testing.T) {
	p := &stubPush{}
	h := newPushServer(p, pushConfig{allowedHosts: webpush.DefaultAllowedHosts})
	if rec := do(h, "GET", "/api/push/vapid-public-key", "", ""); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"enabled":false}` {
		t.Errorf("key without VAPID: %d %s", rec.Code, rec.Body)
	}
	pk, auth := clientKeys(t)
	body := fmt.Sprintf(`{"endpoint":"https://fcm.googleapis.com/fcm/send/x","keys":{"p256dh":%q,"auth":%q}}`, pk, auth)
	if rec := do(h, "POST", "/api/me/push-subscriptions", "good", body); rec.Code != 404 {
		t.Errorf("subscribe without VAPID: %d", rec.Code)
	}
	if rec := do(h, "DELETE", "/api/me/push-subscriptions", "good", ""); rec.Code != 200 {
		t.Errorf("unsubscribe must keep working: %d", rec.Code)
	}
	if len(p.saved) != 0 {
		t.Error("saved while disabled")
	}
}

func TestPushSubscriptionRoutes(t *testing.T) {
	pub, _, _ := webpush.GenerateVAPID()
	p := &stubPush{}
	h := newPushServer(p, pushConfig{vapidPublicKey: pub, allowedHosts: webpush.DefaultAllowedHosts})

	rec := do(h, "GET", "/api/push/vapid-public-key", "", "")
	var key struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	json.Unmarshal(rec.Body.Bytes(), &key)
	if rec.Code != 200 || !key.Enabled || key.PublicKey != pub {
		t.Fatalf("key: %d %s", rec.Code, rec.Body)
	}

	pk, auth := clientKeys(t)
	ep := "https://fcm.googleapis.com/fcm/send/dXl:APA91b"
	body := fmt.Sprintf(`{"endpoint":%q,"expirationTime":null,"keys":{"p256dh":%q,"auth":%q}}`, ep, pk, auth)
	for _, tok := range []string{"", "bad"} {
		if rec := do(h, "POST", "/api/me/push-subscriptions", tok, body); rec.Code != 401 {
			t.Errorf("token %q: %d", tok, rec.Code)
		}
	}
	rec = do(h, "POST", "/api/me/push-subscriptions", "good", body)
	if rec.Code != 201 || len(p.saved) != 1 || !strings.HasPrefix(p.saved[0], "1 "+ep+" "+pk+" "+auth) {
		t.Fatalf("subscribe: %d %s %v", rec.Code, rec.Body, p.saved)
	}

	for name, b := range map[string]string{
		"http":         fmt.Sprintf(`{"endpoint":"http://fcm.googleapis.com/x","keys":{"p256dh":%q,"auth":%q}}`, pk, auth),
		"unknown host": fmt.Sprintf(`{"endpoint":"https://internal.corp/x","keys":{"p256dh":%q,"auth":%q}}`, pk, auth),
		"ip host":      fmt.Sprintf(`{"endpoint":"https://10.0.0.1/x","keys":{"p256dh":%q,"auth":%q}}`, pk, auth),
		"bad p256dh":   fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":"AAAA","auth":%q}}`, ep, auth),
		"bad auth":     fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":"AAAA"}}`, ep, pk),
		"no keys":      fmt.Sprintf(`{"endpoint":%q}`, ep),
		"unknown":      `{"endpoint":"x","foo":1}`,
		"empty":        ``,
	} {
		rec := do(h, "POST", "/api/me/push-subscriptions", "good", b)
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "internal.corp") {
			t.Errorf("%s: error echoes the endpoint: %s", name, rec.Body)
		}
	}
	if len(p.saved) != 1 {
		t.Errorf("invalid subscriptions saved: %v", p.saved)
	}

	rec = do(h, "DELETE", "/api/me/push-subscriptions", "good", body) // full toJSON body is accepted
	if rec.Code != 200 || p.deleted[0] != "1 "+ep {
		t.Fatalf("unsubscribe: %d %s %v", rec.Code, rec.Body, p.deleted)
	}
	rec = do(h, "DELETE", "/api/me/push-subscriptions", "good", "")
	if rec.Code != 200 || p.deleted[1] != "1 " {
		t.Fatalf("unsubscribe all: %d %v", rec.Code, p.deleted)
	}
}
