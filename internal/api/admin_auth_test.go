package api

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// Every /api/admin route needs an explicit entry in adminRoutePerms.
// Unlisted routes fall back to super_admin only; this test lists them so
// whoever adds (or merges) a route assigns its group here.
func TestAdminRoutePermissionsCoverEveryRoute(t *testing.T) {
	registered := map[string]bool{}
	var missing []string
	for _, rt := range (&Server{}).routes() {
		if !isAdminPattern(rt.pattern) {
			continue
		}
		registered[rt.pattern] = true
		if _, ok := adminRoutePerm(rt.pattern); !ok {
			missing = append(missing, rt.pattern)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		var b strings.Builder
		for _, m := range missing {
			fmt.Fprintf(&b, "\n\t%q: wr(gSomething),", m)
		}
		t.Errorf("%d admin routes have no permission entry in adminRoutePerms (admin_perms.go); they are super_admin only until assigned:%s", len(missing), b.String())
	}
	var stale []string
	for pattern, rp := range adminRoutePerms {
		if !registered[pattern] {
			stale = append(stale, pattern)
		}
		if rp.Group == "" || (rp.Access != adminauth.Read && rp.Access != adminauth.Write) {
			t.Errorf("%s: incomplete permission %+v", pattern, rp)
		}
		if rp.Dual && rp.Access != adminauth.Write {
			t.Errorf("%s: dual approval on a read route", pattern)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("adminRoutePerms lists routes that are not registered: %v", stale)
	}
}

// guardedMux registers srv.admin(ok) under every admin pattern, so the
// wrapper's decision is tested for every route without the real handlers.
func guardedMux(srv *Server) *http.ServeMux {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	for _, rt := range (&Server{}).routes() {
		if isAdminPattern(rt.pattern) {
			mux.HandleFunc(rt.pattern, srv.admin(ok))
		}
	}
	return mux
}

func TestAdminRoleMatrixEnforcedOnEveryRoute(t *testing.T) {
	srv := &Server{adminAuth: newFakeAdmins()}
	mux := guardedMux(srv)
	for pattern, rp := range adminRoutePerms {
		method, path, _ := strings.Cut(pattern, " ")
		for _, role := range adminauth.Roles {
			req := asAdmin(httptest.NewRequest(method, samplePath(path), strings.NewReader("{}")), "u-"+string(role), string(role))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			allowed := adminauth.RoleAccess(role, rp.Group) >= rp.Access
			want := http.StatusForbidden
			if allowed {
				want = http.StatusTeapot
				if rp.Dual {
					want = http.StatusAccepted
				}
			}
			if rec.Code != want {
				t.Errorf("%s as %s: %d, want %d", pattern, role, rec.Code, want)
			}
		}
	}
}

func TestAdminSpotChecks(t *testing.T) {
	srv := &Server{adminAuth: newFakeAdmins()}
	mux := guardedMux(srv)
	do := func(method, path, user string, roles ...string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest(method, path, nil), user, roles...))
		return rec.Code
	}
	cases := []struct {
		method, path, role string
		want               int
	}{
		{"PUT", "/api/admin/i18n/ui/en/k", "translator", 418},
		{"GET", "/api/admin/releases", "translator", 403},
		{"GET", "/api/admin/releases", "ingest_ops", 418},
		{"PATCH", "/api/admin/releases/1", "ingest_ops", 403},
		{"PATCH", "/api/admin/releases/1", "content_editor", 418},
		{"POST", "/api/admin/takedowns/1/actions", "moderator", 418},
		{"POST", "/api/admin/takedowns/1/actions", "operations", 403},
		{"POST", "/api/admin/legal/1/publish", "moderator", 403},
		{"GET", "/api/admin/admin-users", "content_editor", 403},
		{"GET", "/api/admin/audit-log", "moderator", 403},
		{"GET", "/api/admin/session", "ad_sales", 418},
		{"POST", "/api/admin/home-slots", "ad_sales", 403},
		{"GET", "/api/admin/home-slots", "ad_sales", 418},
	}
	for _, c := range cases {
		if got := do(c.method, c.path, "x-"+c.role, c.role); got != c.want {
			t.Errorf("%s %s as %s: %d, want %d", c.method, c.path, c.role, got, c.want)
		}
	}
	// Several roles: the union.
	if got := do("PATCH", "/api/admin/releases/1", "multi", "translator", "content_editor"); got != 418 {
		t.Errorf("union of roles: %d", got)
	}
	// A pattern missing from the table is super_admin only.
	h := srv.admin(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	unknown := http.NewServeMux()
	unknown.HandleFunc("POST /api/admin/brand-new/{id}", h)
	for role, want := range map[string]int{"content_editor": 403, "super_admin": 418} {
		rec := httptest.NewRecorder()
		unknown.ServeHTTP(rec, asAdmin(httptest.NewRequest("POST", "/api/admin/brand-new/1", nil), "y-"+role, role))
		if rec.Code != want {
			t.Errorf("unknown route as %s: %d, want %d", role, rec.Code, want)
		}
	}
}

func TestAdminSessionCredentials(t *testing.T) {
	f := newFakeAdmins()
	srv := &Server{adminAuth: f}
	mux := guardedMux(srv)
	do := func(hdr map[string]string) int {
		req := httptest.NewRequest("GET", "/api/admin/dashboard", nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do(nil); c != 401 {
		t.Errorf("no credential: %d", c)
	}
	if c := do(adminAs("ana")); c != 418 {
		t.Errorf("session: %d", c)
	}
	h := adminAs("ana")
	delete(h, adminCSRFHeader)
	if c := do(h); c != 403 {
		t.Errorf("cookie without CSRF header: %d", c)
	}
	if c := do(map[string]string{"Cookie": adminCookie + "=adms_unknown", adminCSRFHeader: "1"}); c != 401 {
		t.Errorf("unknown session: %d", c)
	}
	f.revoked[testAdminToken("gone")] = true
	if c := do(adminAs("gone")); c != 401 {
		t.Errorf("revoked session: %d", c)
	}
	// A session token is not a bearer credential.
	if c := do(map[string]string{"Authorization": "Bearer " + testAdminToken("ana")}); c != 401 {
		t.Errorf("session token as bearer: %d", c)
	}
}

func TestBreakGlassToken(t *testing.T) {
	f := newFakeAdmins()
	srv := &Server{adminToken: "glass", adminAuth: f}
	mux := guardedMux(srv)
	do := func(method, path, tok, label string) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if label != "" {
			req.Header.Set("X-Admin-User", label)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("GET", "/api/admin/dashboard", "glass", "seo-check"); c != 418 {
		t.Fatalf("break-glass read: %d", c)
	}
	// Every use, reads included, is in the op log with the hashed IP.
	a := f.audits()
	if len(a) != 1 || a[0].Auth != "token" || a[0].Actor != "token:seo-check" || a[0].IPHash == "" || strings.Contains(a[0].IPHash, "203.0.113") {
		t.Fatalf("audit %+v", a)
	}
	// Super admin everywhere, and dual approval is bypassed (logged).
	if c := do("PUT", "/api/admin/admin-users/3/roles", "glass", ""); c != 418 {
		t.Errorf("break-glass dual route: %d", c)
	}
	if last := f.audits()[len(f.audits())-1]; last.Actor != "token:unlabelled" || last.Route != "PUT /api/admin/admin-users/{id}/roles" || last.EntityType != "admin-users" || last.EntityID != "3" {
		t.Errorf("audit %+v", last)
	}
	if c := do("GET", "/api/admin/dashboard", "glas", ""); c != 401 {
		t.Errorf("wrong token: %d", c)
	}
	// Labels are sanitized: printable ASCII, no spaces, at most 64 bytes.
	do("GET", "/api/admin/dashboard", "glass", "a b\tc"+strings.Repeat("x", 100))
	if last := f.audits()[len(f.audits())-1]; last.Actor != "token:abc"+strings.Repeat("x", 61) {
		t.Errorf("label %q", last.Actor)
	}
	srv.adminTokenDisabled = true
	if c := do("GET", "/api/admin/dashboard", "glass", ""); c != 401 {
		t.Errorf("ADMIN_TOKEN_DISABLED: %d", c)
	}
	empty := guardedMux(&Server{adminAuth: f})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer ")
	empty.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("unset ADMIN_TOKEN: %d", rec.Code)
	}
}

func TestAdminTokenDisabledFromEnv(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "glass")
	t.Setenv("ADMIN_TOKEN_DISABLED", "true")
	h := New(nil, nil)
	req := httptest.NewRequest("GET", "/api/admin/session", nil)
	req.Header.Set("Authorization", "Bearer glass")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Errorf("disabled break-glass: %d", rec.Code)
	}
}

func TestAdminWritesAreAudited(t *testing.T) {
	f := newFakeAdmins()
	srv := &Server{adminAuth: f}
	mux := guardedMux(srv)
	serve := func(req *http.Request) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	serve(asAdmin(httptest.NewRequest("GET", "/api/admin/releases", nil), "ed", "content_editor"))
	if n := len(f.audits()); n != 0 {
		t.Fatalf("plain read audited (%d rows)", n)
	}
	serve(asAdmin(httptest.NewRequest("PATCH", "/api/admin/releases/42", nil), "ed", "content_editor"))
	serve(asAdmin(httptest.NewRequest("GET", "/api/admin/site-users", nil), "mo", "moderator"))
	serve(asAdmin(httptest.NewRequest("POST", "/api/admin/legal", nil), "ed", "content_editor")) // denied
	a := f.audits()
	if len(a) != 3 {
		t.Fatalf("audit rows %d: %+v", len(a), a)
	}
	if a[0].Actor != "ed" || a[0].Auth != "session" || a[0].EntityType != "releases" || a[0].EntityID != "42" || a[0].Status != 418 || a[0].AdminID == nil || a[0].Action != "catalog.patch" {
		t.Errorf("write row %+v", a[0])
	}
	if a[1].Route != "GET /api/admin/site-users" {
		t.Errorf("personal-data read row %+v", a[1])
	}
	if a[2].Status != 403 || a[2].Action != "denied" {
		t.Errorf("denied row %+v", a[2])
	}
}

// fakeAccounts backs the approval routes with fakeAdmins' approvals.
type fakeAccounts struct {
	adminAccountStore // unimplemented methods panic
	f                 *fakeAdmins
	inactive          map[int64]bool
}

func (a *fakeAccounts) find(id int64) *store.AdminApproval {
	for i := range a.f.approvals {
		if a.f.approvals[i].ID == id {
			return &a.f.approvals[i]
		}
	}
	return nil
}

func (a *fakeAccounts) GetAdminApproval(_ context.Context, id int64) (store.AdminApproval, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	if p := a.find(id); p != nil {
		return *p, nil
	}
	return store.AdminApproval{}, errNotFound
}

func (a *fakeAccounts) AdminApprovals(_ context.Context, status string, _ int) ([]store.AdminApproval, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	return append([]store.AdminApproval(nil), a.f.approvals...), nil
}

func (a *fakeAccounts) ClaimAdminApproval(_ context.Context, id, approverID int64, approver string) (store.AdminApproval, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	p := a.find(id)
	switch {
	case p == nil:
		return store.AdminApproval{}, errNotFound
	case p.RequestedBy == approverID:
		return *p, &store.ConflictError{Msg: "a request must be approved by a different admin"}
	case p.Status != "pending":
		return *p, &store.ConflictError{Msg: "already " + p.Status}
	}
	p.Status, p.DecidedBy = "executing", &approver
	return *p, nil
}

func (a *fakeAccounts) FinishAdminApproval(_ context.Context, id int64, ok bool, code int) error {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	p := a.find(id)
	p.Status = "executed"
	if !ok {
		p.Status = "failed"
	}
	p.ResultStatus = &code
	return nil
}

func (a *fakeAccounts) DecideAdminApproval(_ context.Context, id, deciderID int64, decider, note string) (store.AdminApproval, error) {
	a.f.mu.Lock()
	defer a.f.mu.Unlock()
	p := a.find(id)
	p.Status = "rejected"
	if p.RequestedBy == deciderID {
		p.Status = "cancelled"
	}
	return *p, nil
}

func (a *fakeAccounts) ActiveAdminPrincipal(_ context.Context, id int64) (store.AdminPrincipal, error) {
	if a.inactive[id] {
		return store.AdminPrincipal{}, &store.ConflictError{Msg: "admin is disabled"}
	}
	for _, tok := range []string{testAdminToken("mod-a", "moderator"), testAdminToken("ed-c", "content_editor")} {
		if p, _ := testPrincipal(tok); p.ID == id {
			return p, nil
		}
	}
	return store.AdminPrincipal{}, errNotFound
}

func TestDualApprovalFlow(t *testing.T) {
	f := newFakeAdmins()
	acc := &fakeAccounts{f: f, inactive: map[int64]bool{}}
	m := &stubModeration{}
	srv := &Server{adminAuth: f, adminAccts: acc, moderation: m, accounts: &stubAccounts{}}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		rec := doH(mux, method, path, body, hdr)
		return rec
	}
	modA := adminAs("mod-a", "moderator")
	modB := adminAs("mod-b", "moderator")
	flood := `{"release_id":1,"score":5,"window_start":"2026-10-01T10:00:00Z","reason":"刷分"}`

	rec := do("POST", "/api/admin/moderation/rating-floods/remove", flood, modA)
	if rec.Code != http.StatusAccepted || m.floodCalls != 0 {
		t.Fatalf("request: %d %s (store calls %d)", rec.Code, rec.Body, m.floodCalls)
	}
	var created struct {
		Approval approvalItem `json:"approval"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	id := created.Approval.ID
	if id == 0 || !created.Approval.Mine || created.Approval.CanApprove || !strings.Contains(created.Approval.Summary, "刷分") {
		t.Fatalf("approval %+v", created.Approval)
	}
	path := fmt.Sprintf("/api/admin/approvals/%d/approve", id)

	// The requester cannot approve; neither can an admin without the route's group.
	if rec := do("POST", path, "", modA); rec.Code != http.StatusConflict {
		t.Errorf("self approval: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", path, "", adminAs("tr", "translator")); rec.Code != http.StatusForbidden {
		t.Errorf("translator approval: %d", rec.Code)
	}
	// The queue marks what the viewer can approve.
	rec = do("GET", "/api/admin/approvals", "", modB)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"can_approve":true`) || !strings.Contains(rec.Body.String(), `"release_id":1`) {
		t.Errorf("queue: %d %s", rec.Code, rec.Body)
	}

	rec = do("POST", path, "", modB)
	if rec.Code != 200 || m.floodCalls != 1 {
		t.Fatalf("approve: %d %s (store calls %d)", rec.Code, rec.Body, m.floodCalls)
	}
	if m.floodActor != "mod-a" {
		t.Errorf("executed as %q, want the requester mod-a", m.floodActor)
	}
	if !strings.Contains(rec.Body.String(), `"result_status":200`) || !strings.Contains(rec.Body.String(), `"status":"executed"`) {
		t.Errorf("approve body %s", rec.Body)
	}
	var replay *store.AdminAuditEntry
	for _, e := range f.audits() {
		if e.Route == "POST /api/admin/moderation/rating-floods/remove" && e.ApprovalID != nil {
			e := e
			replay = &e
		}
	}
	if replay == nil || replay.Actor != "mod-a" || replay.Detail["approved_by"] != "mod-b" || *replay.ApprovalID != id {
		t.Errorf("replay audit %+v", replay)
	}
	// Approving twice is a conflict.
	if rec := do("POST", path, "", modB); rec.Code != http.StatusConflict {
		t.Errorf("second approval: %d", rec.Code)
	}

	// A requester disabled meanwhile: the approval fails, nothing runs.
	rec = do("POST", "/api/admin/moderation/rating-floods/remove", flood, modA)
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	pa, _ := testPrincipal(testAdminToken("mod-a", "moderator"))
	acc.inactive[pa.ID] = true
	if rec := do("POST", fmt.Sprintf("/api/admin/approvals/%d/approve", created.Approval.ID), "", modB); rec.Code != http.StatusConflict || m.floodCalls != 1 {
		t.Errorf("inactive requester: %d (calls %d)", rec.Code, m.floodCalls)
	}
	acc.inactive[pa.ID] = false

	// Rejecting and cancelling.
	rec = do("POST", "/api/admin/moderation/rating-floods/remove", flood, modA)
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec := do("POST", fmt.Sprintf("/api/admin/approvals/%d/reject", created.Approval.ID), `{"note":"no"}`, adminAs("ed-c", "content_editor")); rec.Code != http.StatusForbidden {
		t.Errorf("reject without permission: %d", rec.Code)
	}
	if rec := do("POST", fmt.Sprintf("/api/admin/approvals/%d/reject", created.Approval.ID), `{"note":"withdrawn"}`, modA); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cancelled"`) {
		t.Errorf("cancel own: %d %s", rec.Code, rec.Body)
	}
}

func TestAdminAuthPublicRoutes(t *testing.T) {
	srv := &Server{adminAccts: &fakeAccounts{f: newFakeAdmins()}, limiter: community.NewMemoryLimiter()}
	mux := http.NewServeMux()
	for _, rt := range srv.adminAuthRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	// The CSRF header is required on sign-in too.
	rec := doH(mux, "POST", "/api/admin-auth/login", `{"login":"a","password":"b"}`, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("no CSRF header: %d", rec.Code)
	}
	csrf := map[string]string{adminCSRFHeader: "1"}
	// Unknown fields are refused without echoing the body (it holds credentials).
	rec = doH(mux, "POST", "/api/admin-auth/login", `{"login":"a","password":"hunter2-secret","x":1}`, csrf)
	if rec.Code != 400 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Errorf("bad body: %d %s", rec.Code, rec.Body)
	}
	// Per-IP limit across all sign-in routes.
	limited := false
	for i := 0; i < adminLoginLimit+2; i++ {
		rec = doH(mux, "POST", "/api/admin-auth/verify", `{"challenge":""}`, csrf)
		if rec.Code == http.StatusTooManyRequests {
			limited = rec.Header().Get("Retry-After") != ""
			break
		}
	}
	if !limited {
		t.Error("sign-in routes are not rate limited per IP")
	}
}

func TestLockedErrorStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	lockedReply(rec, &store.AdminLockedError{Until: time.Now().Add(90 * time.Second)})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("locked: %d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	for err, want := range map[error]int{store.ErrAdminCredentials: 401, store.ErrAdminChallenge: 401, store.ErrAdminInvite: 404, store.ErrAccountBanned: 403} {
		if got := statusOf(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}

func TestRouteEntity(t *testing.T) {
	cases := map[string][2]string{
		"POST /api/admin/releases/{id}/status":         {"releases", "id"},
		"DELETE /api/admin/moderation/mutes/{user_id}": {"mutes", "user_id"},
		"GET /api/admin/dashboard":                     {"dashboard", ""},
		"PUT /api/admin/i18n/ui/{lang}/{key}":          {"ui", "lang"},
		"POST /api/admin/splits/{event_id}/undo":       {"splits", "event_id"},
		"POST /api/admin/admin-users/{id}/reset-2fa":   {"admin-users", "id"},
	}
	for pattern, want := range cases {
		e, w := routeEntity(pattern)
		if e != want[0] || w != want[1] {
			t.Errorf("%s: %q %q", pattern, e, w)
		}
	}
}
