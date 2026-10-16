package api

import (
	"avxxx/internal/oidc"
	"avxxx/internal/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAgeFromBirthMonthBoundaries(t *testing.T) {
	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		year, month int
		want        int
	}{
		{2008, 9, 18},  // birthday month already passed
		{2008, 10, 17}, // born this month: not yet counted
		{2008, 11, 17},
		{2008, 12, 17},
		{2007, 10, 18},
		{2009, 1, 17},
		{2026, 10, -1}, // born this month: not yet 0
		{1990, 1, 36},
	}
	for _, c := range cases {
		got, err := ageFromBirth(c.year, c.month, now)
		if err != nil || got != c.want {
			t.Errorf("ageFromBirth(%d, %d) = %d, %v; want %d", c.year, c.month, got, err, c.want)
		}
	}
	// January: December of last year counts, the current January does not.
	jan := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
	if got, _ := ageFromBirth(2008, 12, jan); got != 18 {
		t.Errorf("Jan 2027, born Dec 2008 = %d, want 18", got)
	}
	if got, _ := ageFromBirth(2009, 1, jan); got != 17 {
		t.Errorf("Jan 2027, born Jan 2009 = %d, want 17", got)
	}
	for _, bad := range [][2]int{{2008, 0}, {2008, 13}, {2027, 1}, {2026, 11}, {1900, 5}, {0, 0}} {
		if _, err := ageFromBirth(bad[0], bad[1], now); err == nil {
			t.Errorf("ageFromBirth(%d, %d) accepted", bad[0], bad[1])
		}
	}
}

func TestAgeGateCookieSigning(t *testing.T) {
	g := ageGate{key: []byte("k1")}
	now := time.Unix(1_800_000_000, 0)
	exp := now.Add(time.Hour)
	v := g.sign(verdictOK, exp)
	if got, until := g.parse(v, now); got != verdictOK || !until.Equal(exp) {
		t.Errorf("parse(ok) = %q %v", got, until)
	}
	if got, _ := g.parse(g.sign(verdictDenied, exp), now); got != verdictDenied {
		t.Errorf("parse(denied) = %q", got)
	}
	// Tampering: verdict, expiry, signature, another key, garbage.
	parts := strings.Split(g.sign(verdictDenied, exp), ".")
	tampered := []string{
		strings.Join([]string{parts[0], "ok", parts[2], parts[3]}, "."),
		strings.Join([]string{parts[0], parts[1], "9999999999", parts[3]}, "."),
		strings.Join([]string{parts[0], parts[1], parts[2], parts[3][:len(parts[3])-2] + "AA"}, "."),
		ageGate{key: []byte("k2")}.sign(verdictOK, exp),
		"v2.ok.1.x", "ok", "", "v1.maybe." + parts[2] + "." + parts[3],
	}
	for _, s := range tampered {
		if got, _ := g.parse(s, now); got != verdictNone {
			t.Errorf("tampered %q parsed as %q", s, got)
		}
	}
	// Expiry.
	if got, _ := g.parse(v, exp); got != verdictNone {
		t.Errorf("expired cookie parsed as %q", got)
	}
}

// gateServer is an auth test server with a fixed gate key and clock.
func gateServer(now time.Time) (*Server, *fakeAuth) {
	f := newFakeAuth()
	srv := newAuthServer(f, &fakeOIDC{claims: oidc.Claims{Subject: "g-1", Email: "a@example.com", EmailVerified: true}})
	srv.ageGate = ageGate{key: []byte("test-key")}
	srv.clock = func() time.Time { return now }
	return srv, f
}

func gateCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == ageGateCookie {
			return c
		}
	}
	return nil
}

func TestPostAgeGate(t *testing.T) {
	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	srv, _ := gateServer(now)
	post := func(body string, c *http.Cookie) *httptest.ResponseRecorder {
		req := jsonReq("POST", "/api/age-gate", body, "")
		if c != nil {
			req.AddCookie(c)
		}
		return serveAuth(srv, req)
	}

	rec := post(`{"year":1990,"month":4}`, nil)
	ok := gateCookie(rec)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) || ok == nil {
		t.Fatalf("adult: %d %s", rec.Code, rec.Body)
	}
	if !ok.HttpOnly || ok.SameSite != http.SameSiteLaxMode || ok.Path != "/" || ok.MaxAge < 364*24*3600 {
		t.Errorf("ok cookie flags: %+v", ok)
	}
	if v, _ := srv.ageGate.parse(ok.Value, now); v != verdictOK {
		t.Errorf("ok cookie verdict %q", v)
	}

	// Born this month 18 years ago: not 18 yet -> denied for 24 h.
	rec = post(`{"year":2008,"month":10}`, ok)
	denied := gateCookie(rec)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"denied"`) || denied == nil || denied.MaxAge != 24*3600 {
		t.Fatalf("minor: %d %s %+v", rec.Code, rec.Body, denied)
	}

	// The denial is locked: a new answer is refused and no cookie is set.
	rec = post(`{"year":1990,"month":4}`, denied)
	if rec.Code != http.StatusForbidden || gateCookie(rec) != nil || !strings.Contains(rec.Body.String(), "locked_until") {
		t.Errorf("re-submit while denied: %d %s", rec.Code, rec.Body)
	}
	// After 24 hours the lock is gone.
	srv.clock = func() time.Time { return now.Add(24*time.Hour + time.Second) }
	if rec = post(`{"year":1990,"month":4}`, denied); rec.Code != 200 || gateCookie(rec) == nil {
		t.Errorf("after lock: %d %s", rec.Code, rec.Body)
	}
	srv.clock = func() time.Time { return now }

	// Invalid input and CSRF hardening.
	if rec = post(`{"year":2008,"month":13}`, nil); rec.Code != 400 || gateCookie(rec) != nil {
		t.Errorf("invalid month: %d", rec.Code)
	}
	req := httptest.NewRequest("POST", "/api/age-gate", strings.NewReader(`{"year":1990,"month":4}`))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec = serveAuth(srv, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: %d", rec.Code)
	}
}

func TestPostAgeGateRateLimit(t *testing.T) {
	srv, _ := gateServer(time.Now())
	var last int
	for i := 0; i <= limitAgeGateIP.limit; i++ {
		last = serveAuth(srv, jsonReq("POST", "/api/age-gate", `{"year":1990,"month":4}`, "")).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("request %d answered %d, want 429", limitAgeGateIP.limit+1, last)
	}
}

func TestAccountAdultChecks(t *testing.T) {
	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	srv, f := gateServer(now)
	okCookie := &http.Cookie{Name: ageGateCookie, Value: srv.ageGate.sign(verdictOK, now.Add(time.Hour))}
	deniedCookie := &http.Cookie{Name: ageGateCookie, Value: srv.ageGate.sign(verdictDenied, now.Add(time.Hour))}
	register := func(body string, c *http.Cookie) *httptest.ResponseRecorder {
		req := jsonReq("POST", "/api/auth/register", body, "")
		if c != nil {
			req.AddCookie(c)
		}
		return serveAuth(srv, req)
	}
	base := `"email":"a@example.com","password":"longenough"`

	if rec := register(`{`+base+`}`, okCookie); rec.Code != 400 {
		t.Errorf("no checkbox: %d", rec.Code)
	}
	if rec := register(`{`+base+`,"adult_confirmed":true}`, nil); rec.Code != 200 {
		t.Errorf("checkbox-only registration: %d", rec.Code)
	}
	if rec := register(`{`+base+`,"adult_confirmed":true,"birth_year":1990,"birth_month":4}`, deniedCookie); rec.Code != 403 {
		t.Errorf("denied cookie: %d", rec.Code)
	}
	rec := register(`{`+base+`,"adult_confirmed":true,"birth_year":2010,"birth_month":1}`, nil)
	if c := gateCookie(rec); rec.Code != 403 || c == nil || c.MaxAge != 24*3600 {
		t.Errorf("under 18: %d %+v", rec.Code, c)
	}
	if len(f.registered) != 1 {
		t.Fatalf("unexpected registrations reached the store: %+v", f.registered)
	}
	if rec := register(`{`+base+`,"adult_confirmed":true}`, okCookie); rec.Code != 200 {
		t.Errorf("ok cookie: %d %s", rec.Code, rec.Body)
	}
	if rec := register(`{`+base+`,"adult_confirmed":true,"birth_year":1990,"birth_month":4}`, nil); rec.Code != 200 {
		t.Errorf("birth date: %d %s", rec.Code, rec.Body)
	}
	want := []store.Adult{{Confirmed: true}, {Confirmed: true, BirthYM: "1990-04"}}
	if len(f.registered) != 3 || f.registered[1] != want[0] || f.registered[2] != want[1] {
		t.Errorf("registered = %+v", f.registered)
	}

	// OAuth: without the confirmation a new account fails as age_required.
	state, bind := startOAuth(t, srv, `{"next":"/"}`, "")
	if loc := callback(srv, state, bind).Header().Get("Location"); loc != "/login?oauth_error=age_required" {
		t.Errorf("unconfirmed OAuth sign-up: %q", loc)
	}
	req := jsonReq("POST", "/api/auth/oauth/google/start", `{"adult_confirmed":true}`, "")
	if rec := serveAuth(srv, req); rec.Code != 200 {
		t.Errorf("OAuth checkbox-only confirmation: %d", rec.Code)
	}
	req = jsonReq("POST", "/api/auth/oauth/google/start", `{"adult_confirmed":true}`, "")
	req.AddCookie(okCookie)
	rec = serveAuth(srv, req)
	if rec.Code != 200 {
		t.Fatalf("OAuth start with ok cookie: %d %s", rec.Code, rec.Body)
	}
	for _, st := range f.states {
		if st.Adult != (store.Adult{Confirmed: true}) {
			t.Errorf("state adult = %+v", st.Adult)
		}
	}

	// One-time confirmation for an existing account.
	confirm := func(body string, c *http.Cookie) *httptest.ResponseRecorder {
		req := jsonReq("POST", "/api/me/adult-confirm", body, "good")
		if c != nil {
			req.AddCookie(c)
		}
		return serveAuth(srv, req)
	}
	if rec := confirm(`{"adult_confirmed":true}`, nil); rec.Code != 200 {
		t.Errorf("checkbox-only confirmation: %d", rec.Code)
	}
	if rec := confirm(`{"adult_confirmed":true}`, okCookie); rec.Code != 200 {
		t.Errorf("confirm with ok cookie: %d %s", rec.Code, rec.Body)
	}
	var out map[string]bool
	if rec := confirm(`{"adult_confirmed":true,"birth_year":1980,"birth_month":2}`, nil); rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !out["adult_confirmed"] {
		t.Errorf("confirm with birth date: %d %s", rec.Code, rec.Body)
	}
	if got := f.confirmed[1]; got != (store.Adult{Confirmed: true, BirthYM: "1980-02"}) {
		t.Errorf("confirmed = %+v", got)
	}
	if rec := confirm(`{"adult_confirmed":true,"birth_year":2012,"birth_month":2}`, nil); rec.Code != 403 || gateCookie(rec) == nil {
		t.Errorf("confirm under 18: %d", rec.Code)
	}
}

func TestAgeGateKeyFailsClosedOnPublicHTTPS(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	dev := newAgeGate(env(map[string]string{"BASE_URL": "http://localhost:5173"}))
	if string(dev.key) != ageGateDevKey {
		t.Fatalf("local dev should use the development key")
	}
	for _, base := range []string{"https://javpick.com", "https://www.example.org/"} {
		g := newAgeGate(env(map[string]string{"PUBLIC_BASE_URL": base}))
		if string(g.key) == ageGateDevKey || len(g.key) != 32 {
			t.Errorf("%s: public https must not use the development key", base)
		}
	}
	if g := newAgeGate(env(map[string]string{"BASE_URL": "https://app.localhost"})); string(g.key) != ageGateDevKey {
		t.Errorf("https on a .localhost host is still local development")
	}
	if g := newAgeGate(env(map[string]string{"AGE_GATE_KEY": "k", "BASE_URL": "https://javpick.com"})); string(g.key) != "k" {
		t.Errorf("configured key must win")
	}
}
