package api

import (
	"avxxx/internal/community"
	"avxxx/internal/oidc"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeAuth is an in-memory authStore. User 1 owns token "good".
type fakeAuth struct {
	resetRequests []string
	loggedOut     []string
	states        map[string]store.OAuthState
	signIn        func(store.OAuthProfile) (int64, bool, error)
	linked        map[int64]store.OAuthProfile
	codes         map[string]int64
	exportErr     error
	revoked       []int64
	deletionPw    string
	sessionsOf    map[int64][]int64 // user -> session ids
}

func newFakeAuth() *fakeAuth {
	return &fakeAuth{states: map[string]store.OAuthState{}, linked: map[int64]store.OAuthProfile{}, codes: map[string]int64{},
		sessionsOf: map[int64][]int64{1: {10, 11}, 2: {20}}}
}

func (f *fakeAuth) RegisterSession(_ context.Context, email, _, _, _ string, m store.SessionMeta) (map[string]any, error) {
	return map[string]any{"token": "t", "remember": m.Remember, "user": map[string]any{"id": 1, "email": email}}, nil
}
func (f *fakeAuth) LoginSession(_ context.Context, email, _ string, m store.SessionMeta) (map[string]any, error) {
	return map[string]any{"token": "t", "remember": m.Remember, "ua": m.UserAgent}, nil
}
func (f *fakeAuth) Logout(_ context.Context, token string) error {
	f.loggedOut = append(f.loggedOut, token)
	return nil
}
func (f *fakeAuth) Sessions(_ context.Context, uid int64, _ string) ([]store.SessionInfo, error) {
	out := []store.SessionInfo{}
	for _, id := range f.sessionsOf[uid] {
		out = append(out, store.SessionInfo{ID: id})
	}
	return out, nil
}
func (f *fakeAuth) RevokeSession(_ context.Context, uid, id int64) (bool, error) {
	for _, s := range f.sessionsOf[uid] {
		if s == id {
			f.revoked = append(f.revoked, id)
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeAuth) RevokeOtherSessions(context.Context, int64, string) (int64, error) { return 1, nil }
func (f *fakeAuth) UpdateProfile(_ context.Context, _ int64, n string) (string, error) {
	return store.CleanDisplayName(n)
}
func (f *fakeAuth) ChangePassword(_ context.Context, _ int64, cur, _, _ string) error {
	if cur != "old-password" {
		return store.ErrWrongPassword
	}
	return nil
}
func (f *fakeAuth) RequestPasswordReset(_ context.Context, email string) error {
	f.resetRequests = append(f.resetRequests, email)
	return nil
}
func (f *fakeAuth) ResetPassword(_ context.Context, token, _ string) error {
	if token != "valid" {
		return store.ErrInvalidResetToken
	}
	return nil
}
func (f *fakeAuth) AcceptTerms(_ context.Context, _ int64, v, _ string) error {
	if !store.ValidTermsVersion(v) {
		return &store.ValidationError{Msg: "invalid terms version"}
	}
	return nil
}
func (f *fakeAuth) RequestAccountDeletion(_ context.Context, _ int64, pw string) (time.Time, error) {
	f.deletionPw = pw
	if pw != "pw" {
		return time.Time{}, store.ErrWrongPassword
	}
	return time.Unix(0, 0), nil
}
func (f *fakeAuth) ReserveDataExport(context.Context, int64) error { return f.exportErr }
func (f *fakeAuth) WriteDataExport(_ context.Context, uid int64, w io.Writer) error {
	_, err := io.WriteString(w, `{"account":{"id":1}}`)
	return err
}
func (f *fakeAuth) Interests(context.Context, int64) ([]string, error) { return []string{"a"}, nil }
func (f *fakeAuth) SetInterests(_ context.Context, _ int64, t []string, _ bool) ([]string, error) {
	return t, nil
}
func (f *fakeAuth) SaveOAuthState(_ context.Context, st store.OAuthState) error {
	f.states[st.StateHash] = st
	return nil
}
func (f *fakeAuth) TakeOAuthState(_ context.Context, provider, h string) (store.OAuthState, error) {
	st, ok := f.states[h]
	delete(f.states, h)
	if !ok || st.Provider != provider {
		return store.OAuthState{}, store.ErrOAuthState
	}
	return st, nil
}
func (f *fakeAuth) OAuthSignIn(_ context.Context, p store.OAuthProfile) (int64, bool, error) {
	if f.signIn != nil {
		return f.signIn(p)
	}
	return 7, true, nil
}
func (f *fakeAuth) LinkIdentity(_ context.Context, uid int64, p store.OAuthProfile) error {
	f.linked[uid] = p
	return nil
}
func (f *fakeAuth) Identities(context.Context, int64) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
func (f *fakeAuth) UnlinkIdentity(context.Context, int64, string) error {
	return store.ErrLastSignInMethod
}
func (f *fakeAuth) CreateOAuthLoginCode(_ context.Context, uid int64, _ string, _, _ bool) (string, error) {
	f.codes["code-1"] = uid
	return "code-1", nil
}
func (f *fakeAuth) ExchangeOAuthLoginCode(_ context.Context, code, _ string) (map[string]any, error) {
	uid, ok := f.codes[code]
	delete(f.codes, code)
	if !ok {
		return nil, store.ErrOAuthState
	}
	return map[string]any{"token": "sess", "user": map[string]any{"id": uid}}, nil
}

// fakeOIDC is an oidcProvider that accepts code "c" when the PKCE
// verifier matches the challenge it was given, and returns fixed claims.
type fakeOIDC struct {
	challenge, nonce string
	claims           oidc.Claims
}

func (p *fakeOIDC) AuthCodeURL(state, nonce, challenge string) string {
	p.challenge, p.nonce = challenge, nonce
	return "https://idp.test/auth?" + url.Values{"state": {state}, "nonce": {nonce}, "code_challenge": {challenge}}.Encode()
}
func (p *fakeOIDC) Exchange(_ context.Context, code, verifier string) (string, error) {
	if code != "c" || oidc.S256(verifier) != p.challenge {
		return "", oidc.ErrToken
	}
	return "idtoken", nil
}
func (p *fakeOIDC) Verify(_ context.Context, raw, nonce string) (oidc.Claims, error) {
	if raw != "idtoken" || nonce != p.nonce {
		return oidc.Claims{}, oidc.ErrIDToken
	}
	return p.claims, nil
}

func newAuthServer(f *fakeAuth, p *fakeOIDC) *Server {
	srv := &Server{accounts: &stubAccounts{}, auth: f, limiter: community.NewMemoryLimiter(), oidc: map[string]oidcProvider{}}
	if p != nil {
		srv.oidc[oidc.Google] = p
	}
	return srv
}

func serveAuth(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func jsonReq(method, path, body, token string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestForgotPasswordNeverRevealsAccounts(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, nil)
	var bodies []string
	for _, email := range []string{"known@example.com", "nobody@example.com"} {
		rec := serveAuth(srv, jsonReq("POST", "/api/auth/password/forgot", `{"email":"`+email+`"}`, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: status %d", email, rec.Code)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] {
		t.Errorf("responses differ: %q vs %q", bodies[0], bodies[1])
	}
	// A cross-site form post (no JSON content type) is refused.
	req := httptest.NewRequest("POST", "/api/auth/password/forgot", strings.NewReader("email=a@b.c"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := serveAuth(srv, req); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("form post: status %d, want 415", rec.Code)
	}
	// Per-address limit.
	for i := 0; i < limitForgotIP.limit; i++ {
		serveAuth(srv, jsonReq("POST", "/api/auth/password/forgot", `{"email":"x@example.com"}`, ""))
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/password/forgot", `{"email":"x@example.com"}`, "")); rec.Code != http.StatusTooManyRequests {
		t.Errorf("over limit: status %d, want 429", rec.Code)
	}
}

func TestResetPassword(t *testing.T) {
	srv := newAuthServer(newFakeAuth(), nil)
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/password/reset", `{"token":"bad","password":"newpassword"}`, "")); rec.Code != 400 {
		t.Errorf("bad token: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/password/reset", `{"token":"valid","password":"newpassword"}`, "")); rec.Code != 200 {
		t.Errorf("valid token: %d %s", rec.Code, rec.Body)
	}
}

func TestLogoutRevokesServerSession(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, nil)
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/logout", ``, "")); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/logout", ``, "good")); rec.Code != 200 || len(f.loggedOut) != 1 || f.loggedOut[0] != "good" {
		t.Errorf("logout: %d %v", rec.Code, f.loggedOut)
	}
}

func TestLoginRememberAndThrottle(t *testing.T) {
	srv := newAuthServer(newFakeAuth(), nil)
	rec := serveAuth(srv, jsonReq("POST", "/api/auth/login", `{"email":"a@b.c","password":"x","remember":false}`, ""))
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["remember"] != false {
		t.Errorf("remember=false not passed: %v", out)
	}
	rec = serveAuth(srv, jsonReq("POST", "/api/auth/login", `{"email":"a@b.c","password":"x"}`, ""))
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out["remember"] != true {
		t.Errorf("remember default: %v", out)
	}
	for i := 0; i < limitLoginEmail.limit; i++ {
		serveAuth(srv, jsonReq("POST", "/api/auth/login", `{"email":"B@x.c","password":"x"}`, ""))
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/login", `{"email":"b@x.c","password":"x"}`, "")); rec.Code != 429 {
		t.Errorf("per-account throttle: %d", rec.Code)
	}
}

func TestMeAccountRoutesRequireTokenAndOwnership(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, nil)
	for _, rt := range srv.authRoutes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if !strings.HasPrefix(path, "/api/me/") {
			continue
		}
		rec := serveAuth(srv, jsonReq(method, samplePath(path), `{}`, ""))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: %d", rt.pattern, rec.Code)
		}
	}
	// Session 20 belongs to user 2: user 1 gets a 404, nothing is revoked.
	if rec := serveAuth(srv, jsonReq("DELETE", "/api/me/sessions/20", ``, "good")); rec.Code != 404 || len(f.revoked) != 0 {
		t.Errorf("foreign session: %d revoked=%v", rec.Code, f.revoked)
	}
	if rec := serveAuth(srv, jsonReq("DELETE", "/api/me/sessions/11", ``, "good")); rec.Code != 200 || len(f.revoked) != 1 {
		t.Errorf("own session: %d revoked=%v", rec.Code, f.revoked)
	}
}

func TestAccountSelfService(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, nil)
	if rec := serveAuth(srv, jsonReq("POST", "/api/me/password", `{"current_password":"nope","new_password":"newpassword"}`, "good")); rec.Code != 403 {
		t.Errorf("wrong current password: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/me/password", `{"current_password":"old-password","new_password":"newpassword"}`, "good")); rec.Code != 200 {
		t.Errorf("change password: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("PATCH", "/api/me/profile", `{"display_name":"`+strings.Repeat("x", 41)+`"}`, "good")); rec.Code != 400 {
		t.Errorf("long nickname: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/me/terms", `{"version":"2026-10 bad"}`, "good")); rec.Code != 400 {
		t.Errorf("bad terms version: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/me/deletion", `{"password":"pw"}`, "good")); rec.Code != 400 {
		t.Errorf("deletion without confirm: %d", rec.Code)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/me/deletion", `{"password":"pw","confirm":true}`, "good")); rec.Code != 200 {
		t.Errorf("deletion: %d %s", rec.Code, rec.Body)
	}
	if rec := serveAuth(srv, jsonReq("DELETE", "/api/me/identities/google", ``, "good")); rec.Code != 409 {
		t.Errorf("unlink last method: %d", rec.Code)
	}
	rec := serveAuth(srv, jsonReq("GET", "/api/me/export", ``, "good"))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("export: %d %v", rec.Code, rec.Header())
	}
	f.exportErr = store.ErrRateLimited
	if rec := serveAuth(srv, jsonReq("GET", "/api/me/export", ``, "good")); rec.Code != 429 {
		t.Errorf("export over limit: %d", rec.Code)
	}
}

func TestProvidersHiddenWhenNotConfigured(t *testing.T) {
	srv := newAuthServer(newFakeAuth(), nil)
	rec := serveAuth(srv, httptest.NewRequest("GET", "/api/auth/providers", nil))
	if strings.TrimSpace(rec.Body.String()) != `{"providers":[]}` {
		t.Errorf("providers = %s", rec.Body)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/oauth/google/start", `{}`, "")); rec.Code != 404 {
		t.Errorf("start unconfigured: %d", rec.Code)
	}
}

// startOAuth runs the start route and returns the state from the provider
// URL plus the bind cookie.
func startOAuth(t *testing.T, srv *Server, body, token string) (string, *http.Cookie) {
	t.Helper()
	rec := serveAuth(srv, jsonReq("POST", "/api/auth/oauth/google/start", body, token))
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	var out struct{ URL string }
	json.Unmarshal(rec.Body.Bytes(), &out)
	u, _ := url.Parse(out.URL)
	var bind *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthBindCookie {
			bind = c
		}
	}
	if bind == nil || !bind.HttpOnly || bind.Path != "/api/auth/oauth/" {
		t.Fatalf("bind cookie: %+v", bind)
	}
	return u.Query().Get("state"), bind
}

func callback(srv *Server, state string, bind *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/api/auth/oauth/google/callback?"+url.Values{"state": {state}, "code": {"c"}}.Encode(), nil)
	if bind != nil {
		req.AddCookie(bind)
	}
	return serveAuth(srv, req)
}

func TestOAuthSignInFlow(t *testing.T) {
	f := newFakeAuth()
	p := &fakeOIDC{claims: oidc.Claims{Subject: "g-1", Email: "a@example.com", EmailVerified: true}}
	srv := newAuthServer(f, p)

	state, bind := startOAuth(t, srv, `{"next":"/releases/x","remember":false}`, "")
	for _, st := range f.states {
		if st.Remember || st.NextPath != "/releases/x" || st.CodeVerifier == "" || st.Nonce == "" {
			t.Errorf("stored state: %+v", st)
		}
	}
	rec := callback(srv, state, bind)
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login?next=%2Freleases%2Fx#oauth_code=code-1") {
		t.Fatalf("callback: %d %q", rec.Code, loc)
	}
	// Replaying the callback finds no state.
	if loc := callback(srv, state, bind).Header().Get("Location"); loc != "/login?oauth_error=expired" {
		t.Errorf("replay: %q", loc)
	}
	// Exchange the code once.
	rec = serveAuth(srv, jsonReq("POST", "/api/auth/oauth/exchange", `{"code":"code-1"}`, ""))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"token":"sess"`) {
		t.Errorf("exchange: %d %s", rec.Code, rec.Body)
	}
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/oauth/exchange", `{"code":"code-1"}`, "")); rec.Code != 400 {
		t.Errorf("second exchange: %d", rec.Code)
	}
}

func TestOAuthCallbackRequiresBrowserBinding(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, &fakeOIDC{claims: oidc.Claims{Subject: "g-1", Email: "a@example.com", EmailVerified: true}})
	state, _ := startOAuth(t, srv, `{}`, "")
	// Login CSRF: the attacker's state arrives in a browser without the cookie.
	if loc := callback(srv, state, nil).Header().Get("Location"); loc != "/login?oauth_error=expired" {
		t.Errorf("no cookie: %q", loc)
	}
	state, _ = startOAuth(t, srv, `{}`, "")
	if loc := callback(srv, state, &http.Cookie{Name: oauthBindCookie, Value: "forged"}).Header().Get("Location"); loc != "/login?oauth_error=expired" {
		t.Errorf("wrong cookie: %q", loc)
	}
	if len(f.codes) != 0 {
		t.Errorf("login code issued without binding: %v", f.codes)
	}
}

func TestOAuthEmailTakenIsNotLinked(t *testing.T) {
	f := newFakeAuth()
	f.signIn = func(store.OAuthProfile) (int64, bool, error) { return 0, false, store.ErrIdentityEmailTaken }
	srv := newAuthServer(f, &fakeOIDC{claims: oidc.Claims{Subject: "g-1", Email: "a@example.com", EmailVerified: true}})
	state, bind := startOAuth(t, srv, `{}`, "")
	if loc := callback(srv, state, bind).Header().Get("Location"); loc != "/login?oauth_error=email_taken" {
		t.Errorf("email taken: %q", loc)
	}
}

func TestOAuthLinkFromSettings(t *testing.T) {
	f := newFakeAuth()
	srv := newAuthServer(f, &fakeOIDC{claims: oidc.Claims{Subject: "g-9", Email: "z@example.com", EmailVerified: true}})
	if rec := serveAuth(srv, jsonReq("POST", "/api/auth/oauth/google/start", `{"link":true}`, "")); rec.Code != 401 {
		t.Errorf("link without token: %d", rec.Code)
	}
	state, bind := startOAuth(t, srv, `{"link":true}`, "good")
	loc := callback(srv, state, bind).Header().Get("Location")
	if loc != "/settings?s=account&linked=google" || f.linked[1].Subject != "g-9" {
		t.Errorf("link: %q %+v", loc, f.linked)
	}
}

func TestSafeNextPath(t *testing.T) {
	for in, want := range map[string]string{"/a": "/a", "//evil.test": "/", "https://evil.test": "/", "/\\evil": "/", "": "/", "/x\r\nSet-Cookie: a": "/"} {
		if got := safeNextPath(in); got != want {
			t.Errorf("safeNextPath(%q) = %q, want %q", in, got, want)
		}
	}
	if appleUserName(`{"name":{"firstName":"Ann","lastName":"Lee"}}`) != "Ann Lee" || appleUserName("x") != "" {
		t.Error("appleUserName")
	}
}
