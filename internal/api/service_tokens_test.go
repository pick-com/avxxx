package api

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeSvcTokens is an in-memory serviceTokenStore keyed by token value.
type fakeSvcTokens struct {
	byToken map[string]store.ServiceToken
	created []store.ServiceTokenInput
}

func (f *fakeSvcTokens) ServiceTokenByToken(_ context.Context, token, _ string) (store.ServiceToken, error) {
	t, ok := f.byToken[token]
	if !ok || t.Status != "active" {
		return store.ServiceToken{}, pgx.ErrNoRows
	}
	return t, nil
}

func (f *fakeSvcTokens) CreateServiceToken(_ context.Context, in store.ServiceTokenInput, actor string, _ int64) (store.ServiceToken, string, error) {
	f.created = append(f.created, in)
	return store.ServiceToken{ID: 1, Name: in.Name, Scopes: in.Scopes, CreatedBy: actor, ExpiresAt: in.ExpiresAt, Status: "active"}, "svc_new", nil
}

func (f *fakeSvcTokens) ServiceTokens(context.Context) ([]store.ServiceToken, error) {
	return []store.ServiceToken{}, nil
}

func (f *fakeSvcTokens) RevokeServiceToken(_ context.Context, id int64, _ string) (store.ServiceToken, error) {
	return store.ServiceToken{ID: id, Status: "revoked"}, nil
}

func svcReq(method, path, tok string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.RemoteAddr = "198.51.100.7:5000"
	req.Header.Set("Authorization", "Bearer "+tok)
	return req
}

func TestServiceTokenAuth(t *testing.T) {
	f := newFakeAdmins()
	st := &fakeSvcTokens{byToken: map[string]store.ServiceToken{
		"svc_seo":     {Name: "seo-check", Scopes: []string{"POST /api/admin/seo/report"}, Status: "active"},
		"svc_ingest":  {Name: "ingest-bot", Scopes: []string{"ingest:read"}, Status: "active"},
		"svc_revoked": {Name: "old", Scopes: []string{"POST /api/admin/seo/report"}, Status: "revoked"},
	}}
	srv := &Server{adminToken: "glass", adminAuth: f, svcTokens: st}
	mux := guardedMux(srv)
	do := func(method, path, tok string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, svcReq(method, path, tok))
		return rec.Code
	}
	if c := do("POST", "/api/admin/seo/report", "svc_seo"); c != http.StatusTeapot {
		t.Fatalf("scoped route: %d", c)
	}
	a := f.audits()
	if len(a) != 1 || a[0].Auth != "service" || a[0].Actor != "service:seo-check" || a[0].IPHash == "" || a[0].Status != http.StatusTeapot {
		t.Fatalf("audit %+v", a)
	}
	cases := []struct {
		method, path, tok string
		want              int
	}{
		{"GET", "/api/admin/seo/report", "svc_seo", 403},     // not in scope
		{"GET", "/api/admin/dashboard", "svc_seo", 403},      // not in scope
		{"GET", "/api/admin/jobs", "svc_ingest", 418},        // group scope
		{"POST", "/api/admin/sources", "svc_ingest", 403},    // read scope, write route
		{"GET", "/api/admin/admin-users", "svc_ingest", 403}, // never
		{"POST", "/api/admin/seo/report", "svc_revoked", 401},
		{"POST", "/api/admin/seo/report", "svc_unknown", 401},
		{"GET", "/api/admin/session", "svc_seo", 403},
		{"POST", "/api/admin/approvals/1/approve", "svc_seo", 403},
		{"POST", "/api/admin/seo/report", "glass", 418}, // break-glass unaffected
	}
	for _, c := range cases {
		if got := do(c.method, c.path, c.tok); got != c.want {
			t.Errorf("%s %s with %s: %d, want %d", c.method, c.path, c.tok, got, c.want)
		}
	}
	// Denials are logged too.
	var denied int
	for _, e := range f.audits() {
		if e.Auth == "service" && e.Action == "denied" {
			denied++
		}
	}
	if denied < 5 {
		t.Errorf("denied service requests logged: %d", denied)
	}
	// Without a store, service tokens are refused.
	empty := guardedMux(&Server{adminAuth: f})
	rec := httptest.NewRecorder()
	empty.ServeHTTP(rec, svcReq("POST", "/api/admin/seo/report", "svc_seo"))
	if rec.Code != 401 {
		t.Errorf("no store: %d", rec.Code)
	}
}

// Even a token holding every grantable group's write scope never reaches
// admin accounts, the op log, the admin's own routes or a dual-approval route.
func TestServiceTokenNeverReachesProtectedRoutes(t *testing.T) {
	var all []string
	for _, g := range adminauth.Groups {
		if !serviceDeniedGroups[g] {
			all = append(all, string(g)+":write")
		}
	}
	f := newFakeAdmins()
	st := &fakeSvcTokens{byToken: map[string]store.ServiceToken{"svc_all": {Name: "all", Scopes: all, Status: "active"}}}
	mux := guardedMux(&Server{adminAuth: f, svcTokens: st})
	for pattern, rp := range adminRoutePerms {
		method, path, _ := strings.Cut(pattern, " ")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, svcReq(method, samplePath(path), "svc_all"))
		protected := rp.Dual || rp.Group == gAdminUsers || rp.Group == gAuditLog || rp.Group == gSelf
		want := http.StatusTeapot
		if protected {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", pattern, rec.Code, want)
		}
	}
	for _, p := range []string{"GET /api/admin/service-tokens", "POST /api/admin/service-tokens", "PUT /api/admin/admin-users/{id}/roles", "GET /api/admin/audit-log", "POST /api/admin/moderation/rating-floods/remove"} {
		if serviceAllowed([]string{p}, p) {
			t.Errorf("%s reachable by a service token scoped to it", p)
		}
	}
}

func TestValidateServiceScopes(t *testing.T) {
	got, err := validateServiceScopes([]string{"POST /api/admin/seo/report", "dashboard:read", "POST  /api/admin/seo/report"})
	if err != nil || strings.Join(got, "|") != "POST /api/admin/seo/report|dashboard:read" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range [][]string{
		nil,
		{"admin_users:read"},
		{"audit_log:read"},
		{"self:write"},
		{"nonsense:read"},
		{"dashboard:admin"},
		{"GET /api/admin/admin-users"},
		{"PUT /api/admin/admin-users/{id}/roles"},
		{"POST /api/admin/moderation/rating-floods/remove"},
		{"GET /api/admin/not-a-route"},
		{"POST /api/admin/approvals/{id}/approve"},
	} {
		if _, err := validateServiceScopes(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestCreateServiceTokenRoute(t *testing.T) {
	f := newFakeAdmins()
	st := &fakeSvcTokens{}
	srv := &Server{adminAuth: f, svcTokens: st}
	mux := http.NewServeMux()
	for _, rt := range srv.serviceTokenRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	post := func(body string, roles ...string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest("POST", "/api/admin/service-tokens", strings.NewReader(body)), "root", roles...))
		return rec
	}
	rec := post(`{"name":"seo-check","scopes":["POST /api/admin/seo/report"],"expires_in_days":30}`)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["token"] != "svc_new" || len(st.created) != 1 || time.Until(st.created[0].ExpiresAt) > 31*24*time.Hour {
		t.Fatalf("create result %v %+v", out, st.created)
	}
	if rec := post(`{"name":"x1","scopes":["GET /api/admin/audit-log"]}`); rec.Code != 400 {
		t.Errorf("protected scope: %d", rec.Code)
	}
	if rec := post(`{"name":"x1","scopes":["dashboard:read"],"expires_in_days":9999}`); rec.Code != 400 {
		t.Errorf("long expiry: %d", rec.Code)
	}
	if rec := post(`{"name":"x1","scopes":["dashboard:read"]}`, "content_editor"); rec.Code != 403 {
		t.Errorf("non-super create: %d", rec.Code)
	}
	if n := len(st.created); n != 1 {
		t.Errorf("created %d tokens", n)
	}
}
