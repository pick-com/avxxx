package api

// Account and auth routes (PRD 7, 15, 20; tech plan 3.6, 3.13): logout,
// password reset, Google / Apple sign-in, and the /api/me account
// self-service routes (profile, password, devices and sessions, terms, data
// export, deletion, onboarding interests).
//
// Security notes:
//   - Sessions are bearer tokens (the web keeps them in storage, never in a
//     cookie), so the JSON routes carry no ambient credentials; the unauthed
//     POSTs additionally require Content-Type: application/json, which a
//     cross-site form cannot send without a CORS preflight.
//   - The only cookie is the OAuth browser binding (HttpOnly, 10 minutes,
//     Path /api/auth/oauth/, SameSite=None; Secure over https because Apple
//     answers with a cross-site form POST, Lax over plain http in development).
//   - Every /api/me route acts on the user id resolved from the bearer token
//     only; ids in paths (sessions) are matched together with that user id.
//   - Forgot-password always answers 202 with the same body; per-address
//     (memory) and per-account (database) limits apply.

import (
	"avxxx/internal/oidc"
	"avxxx/internal/store"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// authStore is the subset of *store.Store used by the account and auth routes.
type authStore interface {
	RegisterSession(ctx context.Context, email, password, displayName, termsVersion string, adult store.Adult, meta store.SessionMeta) (map[string]any, error)
	LoginSession(ctx context.Context, email, password string, meta store.SessionMeta) (map[string]any, error)
	Logout(ctx context.Context, token string) error

	Sessions(ctx context.Context, userID int64, currentToken string) ([]store.SessionInfo, error)
	RevokeSession(ctx context.Context, userID, sessionID int64) (bool, error)
	RevokeOtherSessions(ctx context.Context, userID int64, currentToken string) (int64, error)

	UpdateProfile(ctx context.Context, userID int64, displayName string) (string, error)
	ChangePassword(ctx context.Context, userID int64, current, next, currentToken string) error
	RequestPasswordReset(ctx context.Context, email string) error
	ResetPassword(ctx context.Context, token, password string) error
	AcceptTerms(ctx context.Context, userID int64, version, source string) error
	ConfirmAdult(ctx context.Context, userID int64, adult store.Adult) error

	RequestAccountDeletion(ctx context.Context, userID int64, password string) (time.Time, error)
	ReserveDataExport(ctx context.Context, userID int64) error
	WriteDataExport(ctx context.Context, userID int64, w io.Writer) error
	Interests(ctx context.Context, userID int64) ([]string, error)
	SetInterests(ctx context.Context, userID int64, tags []string, onboarded bool) ([]string, error)

	SaveOAuthState(ctx context.Context, st store.OAuthState) error
	TakeOAuthState(ctx context.Context, provider, stateHash string) (store.OAuthState, error)
	OAuthSignIn(ctx context.Context, p store.OAuthProfile, adult store.Adult) (int64, bool, error)
	LinkIdentity(ctx context.Context, userID int64, p store.OAuthProfile) error
	Identities(ctx context.Context, userID int64) ([]map[string]any, error)
	UnlinkIdentity(ctx context.Context, userID int64, provider string) error
	CreateOAuthLoginCode(ctx context.Context, userID int64, provider string, remember, created bool) (string, error)
	ExchangeOAuthLoginCode(ctx context.Context, code, userAgent string) (map[string]any, error)
}

// oidcProvider is the part of *oidc.Provider the routes use.
type oidcProvider interface {
	AuthCodeURL(state, nonce, challenge string) string
	Exchange(ctx context.Context, code, verifier string) (string, error)
	Verify(ctx context.Context, raw, nonce string) (oidc.Claims, error)
}

// Per-address limits (in-process, like the community limits).
type rateRule struct {
	limit  int
	window time.Duration
}

var (
	limitLoginIP    = rateRule{30, 15 * time.Minute}
	limitLoginEmail = rateRule{10, 15 * time.Minute}
	limitRegisterIP = rateRule{10, time.Hour}
	limitForgotIP   = rateRule{5, time.Hour}
	limitResetIP    = rateRule{20, time.Hour}
	limitOAuthIP    = rateRule{30, 10 * time.Minute}
	limitAdultUser  = rateRule{10, time.Hour}
)

const oauthBindCookie = "reel_oauth_bind"

func (srv *Server) authRoutes() []route {
	return []route{
		{"POST /api/auth/logout", srv.logout},
		{"POST /api/auth/password/forgot", srv.forgotPassword},
		{"POST /api/auth/password/reset", srv.resetPassword},
		{"GET /api/auth/providers", srv.authProviders},
		{"POST /api/auth/oauth/{provider}/start", srv.oauthStart},
		{"GET /api/auth/oauth/{provider}/callback", srv.oauthCallback},
		{"POST /api/auth/oauth/{provider}/callback", srv.oauthCallback},
		{"POST /api/auth/oauth/exchange", srv.oauthExchange},

		{"PATCH /api/me/profile", srv.user(srv.updateProfile)},
		{"POST /api/me/password", srv.user(srv.changePassword)},
		{"GET /api/me/sessions", srv.user(srv.listSessions)},
		{"DELETE /api/me/sessions/{id}", srv.user(srv.revokeSession)},
		{"POST /api/me/sessions/revoke-others", srv.user(srv.revokeOtherSessions)},
		{"POST /api/me/terms", srv.user(srv.acceptTerms)},
		{"GET /api/me/identities", srv.user(srv.listIdentities)},
		{"DELETE /api/me/identities/{provider}", srv.user(srv.unlinkIdentity)},
		{"GET /api/me/export", srv.user(srv.exportData)},
		{"POST /api/me/deletion", srv.user(srv.requestDeletion)},
		{"GET /api/me/interests", srv.user(srv.getInterests)},
		{"PUT /api/me/interests", srv.user(srv.putInterests)},
		{"POST /api/me/adult-confirm", srv.user(srv.confirmAdult)},
	}
}

// authErr maps the account errors to client statuses.
func authErr(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, store.ErrWrongPassword):
		return &httpError{status: http.StatusForbidden, msg: store.ErrWrongPassword.Error()}
	case errors.Is(e, store.ErrInvalidResetToken):
		return badRequest(store.ErrInvalidResetToken.Error(), nil)
	case errors.Is(e, store.ErrRateLimited):
		return tooManyRequests(store.ErrRateLimited.Error())
	case errors.Is(e, store.ErrOAuthState):
		return badRequest(store.ErrOAuthState.Error(), nil)
	case errors.Is(e, store.ErrIdentityTaken), errors.Is(e, store.ErrLastSignInMethod):
		return conflict(e.Error(), nil)
	}
	return e
}

// allow applies one in-process rate rule; a limiter error fails open (the
// database-backed limits still apply where they matter).
func (srv *Server) allow(r *http.Request, key string, rule rateRule) bool {
	if srv.limiter == nil {
		return true
	}
	ok, err := srv.limiter.Allow(r.Context(), key, rule.limit, rule.window)
	return ok || err != nil
}

func (srv *Server) ip(r *http.Request) string { return srv.clientIP.IP(r) }

// requireJSON rejects non-JSON bodies on unauthenticated POSTs (CSRF hardening).
func requireJSON(r *http.Request) error {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		return &httpError{status: http.StatusUnsupportedMediaType, msg: "Content-Type must be application/json"}
	}
	return nil
}

func userAgent(r *http.Request) string { return r.Header.Get("User-Agent") }

// rememberOr reads an optional "remember" flag; absent means remember (the
// old behaviour: 30-day sessions).
func rememberOr(v *bool) bool { return v == nil || *v }

func (srv *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		reply(w, nil, errUnauthorized)
		return
	}
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.auth.Logout(r.Context(), token))
}

func (srv *Server) forgotPassword(w http.ResponseWriter, r *http.Request) {
	if err := requireJSON(r); err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	if !srv.allow(r, "forgot:"+srv.ip(r), limitForgotIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	// Same answer whether or not the account exists or was rate limited.
	if err := srv.auth.RequestPasswordReset(r.Context(), in.Email); err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (srv *Server) resetPassword(w http.ResponseWriter, r *http.Request) {
	if err := requireJSON(r); err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	if !srv.allow(r, "reset:"+srv.ip(r), limitResetIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	e := authErr(srv.auth.ResetPassword(r.Context(), in.Token, in.Password))
	reply(w, map[string]bool{"ok": e == nil}, e)
}

// authProviders lists the configured sign-in providers; the web hides the
// Google / Apple buttons for the others.
func (srv *Server) authProviders(w http.ResponseWriter, r *http.Request) {
	names := []string{}
	for name := range srv.oidc {
		names = append(names, name)
	}
	slices.Sort(names)
	reply(w, map[string]any{"providers": names}, nil)
}

// safeNextPath keeps same-site paths only.
func safeNextPath(v string) string {
	if v == "" || !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") ||
		strings.ContainsAny(v, "\r\n") || len(v) > 512 {
		return "/"
	}
	return v
}

func (srv *Server) provider(r *http.Request) (string, oidcProvider, error) {
	name := r.PathValue("provider")
	p, ok := srv.oidc[name]
	if !ok || p == nil {
		return name, nil, errNotFound
	}
	return name, p, nil
}

// oauthSecure: the bind cookie is Secure (and SameSite=None) when the
// callback is served over https.
func (srv *Server) oauthSecure(r *http.Request) bool {
	return r.TLS != nil || srv.oauthHTTPS
}

func (srv *Server) setBindCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	c := &http.Cookie{Name: oauthBindCookie, Value: value, Path: "/api/auth/oauth/", MaxAge: maxAge, HttpOnly: true}
	if srv.oauthSecure(r) {
		c.Secure, c.SameSite = true, http.SameSiteNoneMode
	} else {
		c.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, c)
}

// oauthStart creates an authorization request and answers with the provider
// URL to navigate to. With "link": true it links the provider to the signed-in
// user (bearer token required) instead of signing in.
func (srv *Server) oauthStart(w http.ResponseWriter, r *http.Request) {
	if err := requireJSON(r); err != nil {
		reply(w, nil, err)
		return
	}
	name, p, err := srv.provider(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Next     string `json:"next"`
		Remember *bool  `json:"remember"`
		Link     bool   `json:"link"`
		adultInput
	}
	if err := readJSON(r, &in, true); err != nil {
		reply(w, nil, err)
		return
	}
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	if !srv.allow(r, "oauth:"+srv.ip(r), limitOAuthIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	// 18+ (agegate.go): a sign-in that may create an account carries the
	// confirmation to the callback; without it only existing accounts can
	// sign in (a new one fails as age_required).
	var adult store.Adult
	if !in.Link && in.AdultConfirmed {
		a, err := srv.adultCheck(w, r, in.adultInput)
		if err != nil {
			reply(w, nil, err)
			return
		}
		adult = a
	}
	var linkUser int64
	if in.Link {
		if srv.accounts == nil {
			reply(w, nil, errUnauthorized)
			return
		}
		if linkUser, err = srv.userID(r); err != nil {
			reply(w, nil, err)
			return
		}
	}
	state, err1 := oidc.RandomString(32)
	nonce, err2 := oidc.RandomString(32)
	bind, err3 := oidc.RandomString(32)
	verifier, challenge, err4 := oidc.PKCE()
	if err := errors.Join(err1, err2, err3, err4); err != nil {
		reply(w, nil, err)
		return
	}
	st := store.OAuthState{StateHash: store.HashOpaque(state), Provider: name, CodeVerifier: verifier, Nonce: nonce,
		BindHash: store.HashOpaque(bind), NextPath: safeNextPath(in.Next), Remember: rememberOr(in.Remember), LinkUserID: linkUser, Adult: adult}
	if err := srv.auth.SaveOAuthState(r.Context(), st); err != nil {
		reply(w, nil, err)
		return
	}
	srv.setBindCookie(w, r, bind, int(store.OAuthStateTTL/time.Second))
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]string{"url": p.AuthCodeURL(state, nonce, challenge)}, nil)
}

// oauthCallback finishes a sign-in: GET for Google, form POST for Apple. It
// always redirects into the web app: /login#oauth_code=… (or ?oauth_error=)
// for a sign-in, /settings?s=account&linked=… for a link.
func (srv *Server) oauthCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(code string, cause error, link bool) {
		if cause != nil {
			slog.Warn("oauth callback", "provider", r.PathValue("provider"), "reason", code, "err", cause)
		}
		if link {
			http.Redirect(w, r, "/settings?s=account&link_error="+url.QueryEscape(code), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/login?oauth_error="+url.QueryEscape(code), http.StatusSeeOther)
	}
	name, p, err := srv.provider(r)
	if err != nil || srv.auth == nil {
		fail("unavailable", nil, false)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		if err := r.ParseForm(); err != nil {
			fail("failed", err, false)
			return
		}
	}
	get := func(k string) string {
		if r.Method == http.MethodPost {
			return r.PostForm.Get(k)
		}
		return r.URL.Query().Get(k)
	}
	state := get("state")
	if state == "" {
		fail("expired", nil, false)
		return
	}
	// The state is consumed first, so a replayed or forged callback finds nothing.
	st, err := srv.auth.TakeOAuthState(r.Context(), name, store.HashOpaque(state))
	if err != nil {
		fail("expired", err, false)
		return
	}
	link := st.LinkUserID > 0
	cookie, cerr := r.Cookie(oauthBindCookie)
	srv.setBindCookie(w, r, "", -1)
	if cerr != nil || subtle.ConstantTimeCompare([]byte(store.HashOpaque(cookie.Value)), []byte(st.BindHash)) != 1 {
		fail("expired", errors.New("browser binding cookie missing or mismatched"), link)
		return
	}
	if e := get("error"); e != "" {
		fail("cancelled", nil, link)
		return
	}
	code := get("code")
	if code == "" {
		fail("failed", errors.New("no code"), link)
		return
	}
	raw, err := p.Exchange(r.Context(), code, st.CodeVerifier)
	if err != nil {
		fail("failed", err, link)
		return
	}
	claims, err := p.Verify(r.Context(), raw, st.Nonce)
	if err != nil {
		fail("failed", err, link)
		return
	}
	profile := store.OAuthProfile{Provider: name, Subject: claims.Subject, Email: claims.Email, EmailVerified: claims.EmailVerified, Name: claims.Name}
	if profile.Name == "" && name == oidc.Apple {
		profile.Name = appleUserName(get("user"))
	}
	if link {
		if err := srv.auth.LinkIdentity(r.Context(), st.LinkUserID, profile); err != nil {
			if errors.Is(err, store.ErrIdentityTaken) {
				fail("identity_taken", nil, true)
			} else {
				fail("failed", err, true)
			}
			return
		}
		http.Redirect(w, r, "/settings?s=account&linked="+url.QueryEscape(name), http.StatusSeeOther)
		return
	}
	uid, created, err := srv.auth.OAuthSignIn(r.Context(), profile, st.Adult)
	switch {
	case errors.Is(err, store.ErrAdultRequired):
		fail("age_required", nil, false)
		return
	case errors.Is(err, store.ErrIdentityEmailTaken):
		fail("email_taken", nil, false)
		return
	case errors.Is(err, store.ErrNoEmail):
		fail("no_email", nil, false)
		return
	case err != nil:
		fail("failed", err, false)
		return
	}
	loginCode, err := srv.auth.CreateOAuthLoginCode(r.Context(), uid, name, st.Remember, created)
	if err != nil {
		fail("failed", err, false)
		return
	}
	// The code travels in the fragment: never sent to a server or in a Referer.
	http.Redirect(w, r, "/login?next="+url.QueryEscape(st.NextPath)+"#oauth_code="+url.QueryEscape(loginCode), http.StatusSeeOther)
}

// appleUserName reads the name Apple posts (first sign-in only) as the
// "user" form field: {"name":{"firstName":"…","lastName":"…"}}.
func appleUserName(raw string) string {
	var u struct {
		Name struct {
			FirstName string `json:"firstName"`
			LastName  string `json:"lastName"`
		} `json:"name"`
	}
	if raw == "" || json.Unmarshal([]byte(raw), &u) != nil {
		return ""
	}
	return strings.TrimSpace(u.Name.FirstName + " " + u.Name.LastName)
}

func (srv *Server) oauthExchange(w http.ResponseWriter, r *http.Request) {
	if err := requireJSON(r); err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	if !srv.allow(r, "oauth:"+srv.ip(r), limitOAuthIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	v, e := srv.auth.ExchangeOAuthLoginCode(r.Context(), in.Code, userAgent(r))
	w.Header().Set("Cache-Control", "no-store")
	reply(w, v, authErr(e))
}

// recsViewerID is viewerID for recommendations: a signed-in viewer who turned
// off Settings › 隐私 › 用观看记录改进推荐 (personalize_recs) gets the same
// non-personalized results as a visitor. A settings read error keeps
// personalization (the setting defaults to on).
func (srv *Server) recsViewerID(r *http.Request) int64 {
	uid := srv.viewerID(r)
	if uid == 0 || srv.social == nil {
		return uid
	}
	if st, err := srv.social.Settings(r.Context(), uid); err == nil && !st.PersonalizeRecs {
		return 0
	}
	return uid
}

// --- /api/me account routes --------------------------------------------------

func (srv *Server) needAuth(w http.ResponseWriter) bool {
	if srv.auth == nil {
		reply(w, nil, errUnavailable)
		return false
	}
	return true
}

func (srv *Server) updateProfile(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		DisplayName *string `json:"display_name"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.DisplayName == nil {
		reply(w, nil, badRequest("display_name is required", nil))
		return
	}
	if !srv.needAuth(w) {
		return
	}
	name, e := srv.auth.UpdateProfile(r.Context(), uid, *in.DisplayName)
	reply(w, map[string]string{"display_name": name}, e)
}

func (srv *Server) changePassword(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.needAuth(w) {
		return
	}
	if !srv.allow(r, "password:"+strconv.FormatInt(uid, 10), limitLoginEmail) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	e := authErr(srv.auth.ChangePassword(r.Context(), uid, in.Current, in.New, bearerToken(r)))
	reply(w, map[string]bool{"ok": e == nil}, e)
}

func (srv *Server) listSessions(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.needAuth(w) {
		return
	}
	v, e := srv.auth.Sessions(r.Context(), uid, bearerToken(r))
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) revokeSession(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid session id", nil))
		return
	}
	if !srv.needAuth(w) {
		return
	}
	ok, e := srv.auth.RevokeSession(r.Context(), uid, id)
	if e == nil && !ok {
		e = errNotFound // another user's session looks exactly like a missing one
	}
	reply(w, map[string]bool{"revoked": ok}, e)
}

func (srv *Server) revokeOtherSessions(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.needAuth(w) {
		return
	}
	n, e := srv.auth.RevokeOtherSessions(r.Context(), uid, bearerToken(r))
	reply(w, map[string]int64{"revoked": n}, e)
}

func (srv *Server) acceptTerms(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Version string `json:"version"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.needAuth(w) {
		return
	}
	e := srv.auth.AcceptTerms(r.Context(), uid, in.Version, "prompt")
	reply(w, map[string]string{"terms_version": in.Version}, e)
}

// confirmAdult is the one-time 18+ confirmation for accounts created before
// the site-entry gate (users.adult_confirmed_at NULL; GET /api/auth/me says
// adult_confirmed false): {"adult_confirmed": true} plus birth_year /
// birth_month unless the gate cookie is ok. Under 18 answers 403 and sets
// the denied gate cookie; the web app then signs out.
func (srv *Server) confirmAdult(w http.ResponseWriter, r *http.Request, uid int64) {
	var in adultInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.needAuth(w) {
		return
	}
	if !srv.allow(r, "adult:"+strconv.FormatInt(uid, 10), limitAdultUser) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	adult, err := srv.adultCheck(w, r, in)
	if err != nil {
		reply(w, nil, err)
		return
	}
	e := srv.auth.ConfirmAdult(r.Context(), uid, adult)
	reply(w, map[string]bool{"adult_confirmed": e == nil}, e)
}

func (srv *Server) listIdentities(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.needAuth(w) {
		return
	}
	v, e := srv.auth.Identities(r.Context(), uid)
	available := []string{}
	for name := range srv.oidc {
		available = append(available, name)
	}
	slices.Sort(available)
	reply(w, map[string]any{"items": v, "available": available}, e)
}

func (srv *Server) unlinkIdentity(w http.ResponseWriter, r *http.Request, uid int64) {
	provider := r.PathValue("provider")
	if provider != oidc.Google && provider != oidc.Apple {
		reply(w, nil, errNotFound)
		return
	}
	if !srv.needAuth(w) {
		return
	}
	e := authErr(srv.auth.UnlinkIdentity(r.Context(), uid, provider))
	reply(w, map[string]bool{"ok": e == nil}, e)
}

// exportData streams the caller's data as a JSON attachment (DataExportsPerDay
// per account; 429 beyond).
func (srv *Server) exportData(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.needAuth(w) {
		return
	}
	if err := srv.auth.ReserveDataExport(r.Context(), uid); err != nil {
		reply(w, nil, authErr(err))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="reel-data-%s.json"`, time.Now().UTC().Format("20060102")))
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := srv.auth.WriteDataExport(r.Context(), uid, w); err != nil {
		// Headers are gone; the truncated body is not valid JSON, which the client detects.
		slog.Error("data export failed", "err", err)
	}
}

func (srv *Server) requestDeletion(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Password string `json:"password"`
		Confirm  bool   `json:"confirm"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if !in.Confirm {
		reply(w, nil, badRequest("confirm must be true", nil))
		return
	}
	if !srv.needAuth(w) {
		return
	}
	if !srv.allow(r, "password:"+strconv.FormatInt(uid, 10), limitLoginEmail) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	after, e := srv.auth.RequestAccountDeletion(r.Context(), uid, in.Password)
	reply(w, map[string]any{"purge_after": after}, authErr(e))
}

func (srv *Server) getInterests(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.needAuth(w) {
		return
	}
	v, e := srv.auth.Interests(r.Context(), uid)
	reply(w, map[string]any{"tags": v}, e)
}

func (srv *Server) putInterests(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Tags      []string `json:"tags"`
		Onboarded bool     `json:"onboarded"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Tags == nil {
		reply(w, nil, badRequest("tags is required", nil))
		return
	}
	if !srv.needAuth(w) {
		return
	}
	v, e := srv.auth.SetInterests(r.Context(), uid, in.Tags, in.Onboarded)
	reply(w, map[string]any{"tags": v}, e)
}
