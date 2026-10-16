package api

// Back-office authentication and authorization (artboard AdminUsers, tech
// plan 3.6). srv.admin guards every /api/admin route:
//
//  1. Credential. A signed-in admin sends the session cookie avxxx_admin
//     (HttpOnly, SameSite=Strict, Path=/api/admin, Secure behind https) plus
//     the header X-Admin-CSRF: 1. The break-glass path sends
//     "Authorization: Bearer $ADMIN_TOKEN" (scripts, emergencies); it acts as
//     super_admin with actor "token:<X-Admin-User label>", is logged with the
//     hashed client IP on every use, and is off with ADMIN_TOKEN_DISABLED=true.
//  2. Permission. The route pattern (r.Pattern) is looked up in
//     adminRoutePerms (admin_perms.go): group + read/write. Unknown patterns
//     need super_admin.
//  3. Dual approval. Routes marked Dual are stored as an approval request
//     (202) and run when a different admin approves (admin_users.go).
//  4. Op log. Writes, break-glass requests and personal-data reads are
//     appended to admin_audit_log with the hashed client IP.
//
// Why a cookie and not a bearer token in browser storage: the session token
// is never readable by page scripts (HttpOnly), so an XSS cannot carry it
// off; TECHNICAL_DESIGN 6 forbids long-lived admin secrets in browser
// storage. CSRF: SameSite=Strict keeps the cookie off cross-site requests,
// and the required X-Admin-CSRF header cannot be sent cross-origin without a
// CORS preflight, which the API never grants (this also covers sibling
// subdomains, which SameSite treats as same-site).
//
// Public sign-in routes live under /api/admin-auth/ (outside the cookie path)
// and are rate limited per client IP on top of the per-account lockout.

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	adminCookie     = "avxxx_admin"
	adminCookiePath = "/api/admin"
	adminCSRFHeader = "X-Admin-CSRF"
	// Break-glass label header (also the old 操作人 header).
	adminLabelHeader = "X-Admin-User"
	maxApprovalBody  = 16384
)

// adminAuthStore backs srv.admin: session lookup, the op log and storing
// dual-approval requests.
type adminAuthStore interface {
	AdminBySession(ctx context.Context, token string) (store.AdminPrincipal, error)
	RecordAdminAudit(ctx context.Context, e store.AdminAuditEntry) error
	CreateAdminApproval(ctx context.Context, a store.AdminApproval) (store.AdminApproval, error)
}

// adminAccountStore backs sign-in, admin accounts, approvals, the op log
// and site users (admin_users.go).
type adminAccountStore interface {
	AdminPasswordLogin(ctx context.Context, login, password string) (string, int64, error)
	AdminSecondFactor(ctx context.Context, challenge, code, recovery string, meta store.AdminSessionMeta) (store.AdminSession, int64, error)
	RevokeAdminSession(ctx context.Context, token string) error
	AdminInviteInfo(ctx context.Context, token string) (store.AdminInvite, error)
	CompleteAdminEnrollment(ctx context.Context, token, password, code string) (string, []string, error)
	RegenerateRecoveryCodes(ctx context.Context, id int64, code string) ([]string, error)
	AdminUsers(ctx context.Context) ([]store.AdminUser, error)
	InviteAdmin(ctx context.Context, in store.AdminUserInput, actor string) (store.AdminUser, string, error)
	ReissueAdminInvite(ctx context.Context, id int64, actor string) (string, error)
	SetAdminRoles(ctx context.Context, id int64, roles []string, actor string, actorID int64) (store.AdminUser, error)
	SetAdminDisabled(ctx context.Context, id int64, disabled bool, reason, actor string, actorID int64) (store.AdminUser, error)
	ResetAdminTOTP(ctx context.Context, id int64, reason, actor string, actorID int64) (string, error)
	AdminApprovals(ctx context.Context, status string, limit int) ([]store.AdminApproval, error)
	GetAdminApproval(ctx context.Context, id int64) (store.AdminApproval, error)
	ClaimAdminApproval(ctx context.Context, id, approverID int64, approver string) (store.AdminApproval, error)
	FinishAdminApproval(ctx context.Context, id int64, ok bool, resultStatus int) error
	DecideAdminApproval(ctx context.Context, id, deciderID int64, decider, note string) (store.AdminApproval, error)
	ActiveAdminPrincipal(ctx context.Context, id int64) (store.AdminPrincipal, error)
	AdminAuditLog(ctx context.Context, f store.AdminAuditFilter) ([]store.AdminAuditEntry, string, error)
	SearchSiteUsers(ctx context.Context, q, status string, limit int) ([]store.SiteUser, error)
	SiteUserByID(ctx context.Context, id int64) (store.SiteUser, error)
	BanUser(ctx context.Context, id int64, until *time.Time, reason, actor string) (store.SiteUser, error)
	UnbanUser(ctx context.Context, id int64, reason, actor string) (store.SiteUser, error)
}

// adminPrincipal is who an admin request runs as.
type adminPrincipal struct {
	store.AdminPrincipal
	// Auth is "session" or "token" (break-glass).
	Auth string
	// Label is the break-glass label (X-Admin-User), "" for sessions.
	Label string
	// ApprovalID / ApprovedBy are set while an approved request is replayed.
	ApprovalID int64
	ApprovedBy string
}

// Actor is the review_events / updated_by actor.
func (p adminPrincipal) Actor() string {
	if p.Auth == "token" {
		return "token:" + p.Label
	}
	return p.Username
}

// can reports whether p may call a route with permission rp (known = the
// pattern is in adminRoutePerms).
func (p adminPrincipal) can(rp routePerm, known bool) bool {
	if p.Auth == "token" {
		return true // break-glass is super_admin
	}
	if !known {
		return adminauth.IsSuper(p.Roles)
	}
	return adminauth.AccessOf(p.Roles, rp.Group) >= rp.Access
}

type principalKey struct{}
type replayKey struct{}

func withPrincipal(ctx context.Context, p adminPrincipal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalFrom returns the admin srv.admin authenticated for this request.
func principalFrom(ctx context.Context) (adminPrincipal, bool) {
	p, ok := ctx.Value(principalKey{}).(adminPrincipal)
	return p, ok
}

// replayPrincipal is set (only in-process) when an approved request is replayed.
func replayPrincipal(ctx context.Context) (adminPrincipal, bool) {
	p, ok := ctx.Value(replayKey{}).(adminPrincipal)
	return p, ok
}

// breakGlassLabel sanitizes X-Admin-User for "token:<label>": printable
// ASCII, at most 64 bytes, "unlabelled" when absent.
func breakGlassLabel(r *http.Request) string {
	v := strings.TrimSpace(r.Header.Get(adminLabelHeader))
	var b strings.Builder
	for _, c := range v {
		if c > 0x20 && c < 0x7f && b.Len() < 64 {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return "unlabelled"
	}
	return b.String()
}

// ipHash is the keyed hash of the client IP (LOG_HASH_KEY) for logs and the op log.
func (srv *Server) ipHash(r *http.Request) string {
	return obs.HashString("ip:" + srv.clientIP.IP(r))
}

type adminAuthError struct {
	status int
	msg    string
}

func (e *adminAuthError) Error() string { return e.msg }

// authenticateAdmin resolves the request's credential.
func (srv *Server) authenticateAdmin(r *http.Request) (adminPrincipal, error) {
	if tok := bearerToken(r); tok != "" {
		return srv.breakGlass(r, tok)
	}
	c, err := r.Cookie(adminCookie)
	if err != nil || c.Value == "" {
		return adminPrincipal{}, &adminAuthError{http.StatusUnauthorized, "unauthorized"}
	}
	if r.Header.Get(adminCSRFHeader) != "1" {
		return adminPrincipal{}, &adminAuthError{http.StatusForbidden, "missing " + adminCSRFHeader + " header"}
	}
	if srv.adminAuth == nil {
		return adminPrincipal{}, &adminAuthError{http.StatusUnauthorized, "unauthorized"}
	}
	p, err := srv.adminAuth.AdminBySession(r.Context(), c.Value)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminPrincipal{}, &adminAuthError{http.StatusUnauthorized, "session expired"}
	}
	if err != nil {
		return adminPrincipal{}, err
	}
	return adminPrincipal{AdminPrincipal: p, Auth: "session"}, nil
}

// breakGlass checks the ADMIN_TOKEN bearer. Digests are compared so the
// comparison is constant-time regardless of length.
func (srv *Server) breakGlass(r *http.Request, tok string) (adminPrincipal, error) {
	deny := &adminAuthError{http.StatusUnauthorized, "unauthorized"}
	if srv.adminToken == "" {
		return adminPrincipal{}, deny
	}
	want := sha256.Sum256([]byte(srv.adminToken))
	got := sha256.Sum256([]byte(tok))
	if subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		return adminPrincipal{}, deny
	}
	if srv.adminTokenDisabled {
		obs.L(r.Context()).Warn("admin break-glass token rejected: ADMIN_TOKEN_DISABLED", "route", r.Pattern, "ip_hash", srv.ipHash(r))
		return adminPrincipal{}, deny
	}
	label := breakGlassLabel(r)
	obs.L(r.Context()).Warn("admin break-glass token used", "route", r.Pattern, "label", label, "ip_hash", srv.ipHash(r))
	return adminPrincipal{
		AdminPrincipal: store.AdminPrincipal{Username: "token:" + label, Roles: []string{string(adminauth.RoleSuperAdmin)}},
		Auth:           "token", Label: label,
	}, nil
}

// statusRecorder captures the status code written by a handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// admin guards an /api/admin handler (see the file comment).
func (srv *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rp, known := adminRoutePerm(r.Pattern)
		p, replay := replayPrincipal(r.Context())
		if !replay {
			var err error
			if p, err = srv.authenticateAdmin(r); err != nil {
				var ae *adminAuthError
				if errors.As(err, &ae) {
					http.Error(w, ae.msg, ae.status)
					return
				}
				reply(w, nil, err)
				return
			}
		}
		if !p.can(rp, known) {
			srv.auditRoute(r, p, rp, http.StatusForbidden, "denied", nil)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			need := "super_admin"
			if known {
				need = string(rp.Group) + ":" + rp.Access.String()
			}
			json.NewEncoder(w).Encode(map[string]string{"error": "forbidden", "need": need})
			return
		}
		if rp.Dual && p.Auth == "session" && !replay {
			srv.requestApproval(w, r, p)
			return
		}
		rec := &statusRecorder{ResponseWriter: w}
		h(rec, r.WithContext(withPrincipal(r.Context(), p)))
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		if p.Auth == "token" || r.Method != http.MethodGet || rp.LogReads || replay {
			srv.auditRoute(r, p, rp, rec.status, "", nil)
		}
	}
}

// auditRoute appends one op-log row. Failures are logged, never surfaced: the
// response is already written.
func (srv *Server) auditRoute(r *http.Request, p adminPrincipal, rp routePerm, status int, action string, detail map[string]any) {
	entity, wildcard := routeEntity(r.Pattern)
	id := ""
	if wildcard != "" {
		id = r.PathValue(wildcard)
	}
	if action == "" {
		action = strings.ToLower(r.Method)
		if rp.Group != "" {
			action = string(rp.Group) + "." + action
		}
	}
	srv.recordAudit(r, p, action, entity, id, status, detail)
}

func (srv *Server) recordAudit(r *http.Request, p adminPrincipal, action, entityType, entityID string, status int, detail map[string]any) {
	if srv.adminAuth == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	e := store.AdminAuditEntry{
		Actor: p.Actor(), Auth: p.Auth, Action: action, Method: r.Method, Route: r.Pattern, Path: r.URL.Path,
		EntityType: entityType, EntityID: entityID, Status: status, IPHash: srv.ipHash(r), Detail: detail,
	}
	if e.Auth == "" {
		e.Auth = "anonymous"
		e.Actor = "anonymous"
	}
	if p.ID > 0 {
		id := p.ID
		e.AdminID = &id
	}
	if p.ApprovalID > 0 {
		id := p.ApprovalID
		e.ApprovalID = &id
		detail["approved_by"] = p.ApprovedBy
	} else if id, ok := detail["approval_id"].(int64); ok {
		e.ApprovalID = &id
	}
	if err := srv.adminAuth.RecordAdminAudit(context.WithoutCancel(r.Context()), e); err != nil {
		obs.L(r.Context()).Error("admin audit log write failed", "err", err, "route", r.Pattern)
	}
}

// requestApproval stores a Dual request for a second admin and answers 202.
func (srv *Server) requestApproval(w http.ResponseWriter, r *http.Request, p adminPrincipal) {
	if srv.adminAuth == nil {
		reply(w, nil, errUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxApprovalBody))
	if err != nil {
		reply(w, nil, badRequest("request body too large", err))
		return
	}
	a, err := srv.adminAuth.CreateAdminApproval(r.Context(), store.AdminApproval{
		Route: r.Pattern, Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body,
		Summary: approvalSummary(r.Pattern, body), RequestedBy: p.ID, RequestedByName: p.Username,
	})
	if err != nil {
		reply(w, nil, err)
		return
	}
	rp, _ := adminRoutePerm(r.Pattern)
	srv.auditRoute(r, p, rp, http.StatusAccepted, "approval.request", map[string]any{"approval_id": a.ID})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"approval_required": true, "approval": approvalView(a, p)})
}

// approvalSummary is a short human description for the queue.
func approvalSummary(pattern string, body []byte) string {
	labels := map[string]string{
		"PUT /api/admin/admin-users/{id}/roles":           "修改后台账号角色",
		"POST /api/admin/admin-users/{id}/enable":         "重新启用后台账号",
		"POST /api/admin/admin-users/{id}/reset-2fa":      "重置两步验证",
		"POST /api/admin/moderation/rating-floods/remove": "批量删除刷分评分",
	}
	s := labels[pattern]
	if s == "" {
		s = pattern
	}
	var m map[string]any
	if json.Unmarshal(body, &m) == nil {
		if reason, ok := m["reason"].(string); ok && reason != "" {
			s += "：" + reason
		}
	}
	return s
}

// approvalItem is an approval as the queue shows it.
type approvalItem struct {
	store.AdminApproval
	Body       json.RawMessage `json:"body,omitempty"`
	CanApprove bool            `json:"can_approve"`
	Mine       bool            `json:"mine"`
}

func approvalView(a store.AdminApproval, p adminPrincipal) approvalItem {
	it := approvalItem{AdminApproval: a}
	if len(a.Body) > 0 && json.Valid(a.Body) {
		it.Body = json.RawMessage(a.Body)
	}
	rp, known := adminRoutePerm(a.Route)
	it.Mine = p.Auth == "session" && p.ID == a.RequestedBy
	it.CanApprove = a.Status == "pending" && !it.Mine && p.can(rp, known)
	return it
}

// approvalHandler is the mux an approved request is replayed through: the
// admin routes only, built once.
func (srv *Server) approvalHandler() *http.ServeMux {
	srv.approvalOnce.Do(func() {
		srv.approvalMux = http.NewServeMux()
		for _, rt := range srv.routes() {
			if isAdminPattern(rt.pattern) {
				srv.approvalMux.HandleFunc(rt.pattern, rt.handler)
			}
		}
	})
	return srv.approvalMux
}

// bufferedResponse records a replayed response.
type bufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(c int) {
	if b.status == 0 {
		b.status = c
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// executeApproval replays an approved request as its requester. The
// requester must still be active and still hold the route's permission
// (srv.admin re-checks it on replay).
func (srv *Server) executeApproval(r *http.Request, a store.AdminApproval, approver adminPrincipal) (int, json.RawMessage, error) {
	requester, err := srv.adminAccts.ActiveAdminPrincipal(r.Context(), a.RequestedBy)
	if err != nil {
		return 0, nil, err
	}
	target := a.Path
	if a.Query != "" {
		target += "?" + a.Query
	}
	ctx := context.WithValue(context.WithoutCancel(r.Context()), replayKey{}, adminPrincipal{
		AdminPrincipal: requester, Auth: "session", ApprovalID: a.ID, ApprovedBy: approver.Actor(),
	})
	in, err := http.NewRequestWithContext(ctx, a.Method, target, bytes.NewReader(a.Body))
	if err != nil {
		return 0, nil, err
	}
	in.RemoteAddr = r.RemoteAddr
	if v := r.Header.Values("X-Forwarded-For"); len(v) > 0 {
		in.Header["X-Forwarded-For"] = v
	}
	if len(a.Body) > 0 {
		in.Header.Set("Content-Type", "application/json")
	}
	mux := srv.approvalHandler()
	if _, pattern := mux.Handler(in); pattern != a.Route {
		return 0, nil, &httpError{status: http.StatusConflict, msg: "the approved route no longer exists"}
	}
	out := &bufferedResponse{header: http.Header{}}
	mux.ServeHTTP(out, in)
	var result json.RawMessage
	if b := bytes.TrimSpace(out.body.Bytes()); len(b) > 0 && json.Valid(b) {
		result = json.RawMessage(b)
	}
	return out.status, result, nil
}

// ---- Public sign-in routes (/api/admin-auth/*) ----

func (srv *Server) adminAuthRoutes() []route {
	return []route{
		{"POST /api/admin-auth/login", srv.adminLogin},
		{"POST /api/admin-auth/verify", srv.adminVerify},
		{"POST /api/admin-auth/enroll/info", srv.adminEnrollInfo},
		{"POST /api/admin-auth/enroll", srv.adminEnroll},
		{"GET /api/admin/session", srv.admin(srv.adminSession)},
		{"POST /api/admin/session/logout", srv.admin(srv.adminLogout)},
		{"POST /api/admin/session/recovery-codes", srv.admin(srv.adminRecoveryCodes)},
		{"GET /api/admin/roles", srv.admin(srv.adminRoleMatrix)},
	}
}

// Sign-in attempts per client IP (password, second factor and enrollment
// together); the per-account lockout (adminauth.LockoutDuration) is separate.
const (
	adminLoginLimit  = 20
	adminLoginWindow = 15 * time.Minute
)

// adminAuthPre is shared by the public sign-in routes: the CSRF header, the
// store, the per-IP limit and a bounded strict JSON body.
func (srv *Server) adminAuthPre(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get(adminCSRFHeader) != "1" {
		http.Error(w, "missing "+adminCSRFHeader+" header", http.StatusForbidden)
		return false
	}
	if srv.adminAccts == nil {
		reply(w, nil, errUnavailable)
		return false
	}
	if srv.limiter != nil {
		ok, err := srv.limiter.Allow(r.Context(), "admin-auth:"+srv.ipHash(r), adminLoginLimit, adminLoginWindow)
		if err != nil {
			reply(w, nil, err)
			return false
		}
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(adminLoginWindow.Seconds())))
			reply(w, nil, &httpError{status: http.StatusTooManyRequests, msg: "too many attempts; try again later"})
			return false
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		reply(w, nil, badRequest("invalid JSON body", nil)) // never echo: the body holds credentials
		return false
	}
	return true
}

func anonAdmin(id int64) adminPrincipal {
	return adminPrincipal{AdminPrincipal: store.AdminPrincipal{ID: id}}
}

func lockedReply(w http.ResponseWriter, err error) {
	var le *store.AdminLockedError
	if errors.As(err, &le) {
		if s := int(time.Until(le.Until).Seconds()) + 1; s > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(s))
		}
	}
	reply(w, nil, err)
}

// adminLogin: POST /api/admin-auth/login {"login": username or email,
// "password"} → {"challenge", "expires_in"} for /verify.
func (srv *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if !srv.adminAuthPre(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Login) == "" || in.Password == "" || len(in.Login) > 254 || len(in.Password) > 1024 {
		reply(w, nil, store.ErrAdminCredentials)
		return
	}
	challenge, id, err := srv.adminAccts.AdminPasswordLogin(r.Context(), in.Login, in.Password)
	if err != nil {
		if id > 0 {
			srv.recordAudit(r, anonAdmin(id), "auth.password_failed", "admin_users", strconv.FormatInt(id, 10), statusOf(err), nil)
		}
		lockedReply(w, err)
		return
	}
	reply(w, map[string]any{"challenge": challenge, "expires_in": int(adminauth.ChallengeTTL.Seconds())}, nil)
}

func statusOf(err error) int {
	code, _ := errorStatus(err)
	return code
}

// adminVerify: POST /api/admin-auth/verify {"challenge", "code" | "recovery_code"}
// → sets the session cookie; {"admin", "permissions", "expires_at", "idle_timeout"}.
func (srv *Server) adminVerify(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge    string `json:"challenge"`
		Code         string `json:"code"`
		RecoveryCode string `json:"recovery_code"`
	}
	if !srv.adminAuthPre(w, r, &in) {
		return
	}
	if in.Challenge == "" || (in.Code == "" && in.RecoveryCode == "") {
		reply(w, nil, badRequest("challenge and code (or recovery_code) are required", nil))
		return
	}
	sess, id, err := srv.adminAccts.AdminSecondFactor(r.Context(), in.Challenge, in.Code, in.RecoveryCode,
		store.AdminSessionMeta{IPHash: srv.ipHash(r), UserAgent: r.UserAgent()})
	if err != nil {
		if id > 0 {
			srv.recordAudit(r, anonAdmin(id), "auth.second_factor_failed", "admin_users", strconv.FormatInt(id, 10), statusOf(err), nil)
		}
		lockedReply(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: adminCookie, Value: sess.Token, Path: adminCookiePath, MaxAge: int(adminauth.SessionMax.Seconds()),
		HttpOnly: true, Secure: srv.adminSecureCookie, SameSite: http.SameSiteStrictMode,
	})
	p := adminPrincipal{AdminPrincipal: sess.Admin, Auth: "session"}
	srv.recordAudit(r, p, "auth.login", "admin_users", strconv.FormatInt(sess.Admin.ID, 10), http.StatusOK, map[string]any{"method": sess.Method})
	reply(w, map[string]any{
		"admin": sess.Admin, "permissions": adminauth.Permissions(sess.Admin.Roles), "expires_at": sess.ExpiresAt,
		"idle_timeout": int(adminauth.SessionIdle.Seconds()), "method": sess.Method, "recovery_codes_left": sess.RecoveryCodesLeft,
	}, nil)
}

// adminEnrollInfo: POST /api/admin-auth/enroll/info {"token"} → who is
// enrolling and the otpauth URI to scan. POST keeps the token out of URLs.
func (srv *Server) adminEnrollInfo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if !srv.adminAuthPre(w, r, &in) {
		return
	}
	inv, err := srv.adminAccts.AdminInviteInfo(r.Context(), strings.TrimSpace(in.Token))
	w.Header().Set("Cache-Control", "no-store")
	reply(w, inv, err)
}

// adminEnroll: POST /api/admin-auth/enroll {"token", "password", "code"} →
// {"username", "recovery_codes"} (shown once). The admin then signs in.
func (srv *Server) adminEnroll(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token    string `json:"token"`
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !srv.adminAuthPre(w, r, &in) {
		return
	}
	username, codes, err := srv.adminAccts.CompleteAdminEnrollment(r.Context(), strings.TrimSpace(in.Token), in.Password, in.Code)
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.recordAudit(r, adminPrincipal{AdminPrincipal: store.AdminPrincipal{Username: username}, Auth: "session"}, "auth.enroll", "admin_users", "", http.StatusOK, nil)
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"username": username, "recovery_codes": codes}, nil)
}

// adminSession: GET /api/admin/session → the signed-in admin, permissions
// and the session policy (the UI hides what the admin cannot open).
func (srv *Server) adminSession(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	reply(w, map[string]any{
		"auth": p.Auth, "actor": p.Actor(), "admin": p.AdminPrincipal, "permissions": adminauth.Permissions(p.Roles),
		"idle_timeout": int(adminauth.SessionIdle.Seconds()), "max_age": int(adminauth.SessionMax.Seconds()),
	}, nil)
}

// adminLogout: POST /api/admin/session/logout revokes the session and clears the cookie.
func (srv *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(adminCookie); err == nil && c.Value != "" && srv.adminAccts != nil {
		if err := srv.adminAccts.RevokeAdminSession(r.Context(), c.Value); err != nil {
			reply(w, nil, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: adminCookie, Value: "", Path: adminCookiePath, MaxAge: -1, HttpOnly: true,
		Secure: srv.adminSecureCookie, SameSite: http.SameSiteStrictMode})
	reply(w, map[string]bool{"ok": true}, nil)
}

// adminRecoveryCodes: POST /api/admin/session/recovery-codes {"code"} →
// fresh recovery codes for the signed-in admin (a current TOTP code is required).
func (srv *Server) adminRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	if p.Auth != "session" || srv.adminAccts == nil {
		reply(w, nil, badRequest("sign in with an admin account", nil))
		return
	}
	var in struct {
		Code string `json:"code"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	codes, err := srv.adminAccts.RegenerateRecoveryCodes(r.Context(), p.ID, in.Code)
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"recovery_codes": codes}, err)
}

// adminRoleMatrix: GET /api/admin/roles → the 7 roles, the matrix and which
// routes need dual approval.
func (srv *Server) adminRoleMatrix(w http.ResponseWriter, r *http.Request) {
	type roleView struct {
		Role  string `json:"role"`
		Label string `json:"label"`
	}
	roles := make([]roleView, 0, len(adminauth.Roles))
	for _, ro := range adminauth.Roles {
		roles = append(roles, roleView{string(ro), adminauth.RoleLabels[ro]})
	}
	var duals []string
	for pattern, rp := range adminRoutePerms {
		if rp.Dual {
			duals = append(duals, pattern)
		}
	}
	sort.Strings(duals)
	reply(w, map[string]any{"roles": roles, "matrix": adminauth.Matrix(), "dual_approval": duals}, nil)
}
