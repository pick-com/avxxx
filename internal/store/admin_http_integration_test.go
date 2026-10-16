package store_test

// End-to-end AdminUsers flow through the real HTTP handler and database:
// password + TOTP sign-in, the session cookie, role enforcement, a role
// change through dual approval, the op log and break-glass. Rows are scoped
// to a per-run suffix (shared AVXXX_TEST_DATABASE_URL).

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/api"
	"avxxx/internal/store"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type adminClient struct {
	t      *testing.T
	h      http.Handler
	cookie string
}

func (c *adminClient) do(method, path, body string) (int, map[string]any, *httptest.ResponseRecorder) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Admin-CSRF", "1")
	req.Header.Set("Content-Type", "application/json")
	if c.cookie != "" {
		req.Header.Set("Cookie", c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}

// signIn runs password + TOTP and keeps the session cookie.
func signIn(t *testing.T, h http.Handler, login, password, secret string, at time.Time) *adminClient {
	t.Helper()
	c := &adminClient{t: t, h: h}
	code, out, rec := c.do("POST", "/api/admin-auth/login", fmt.Sprintf(`{"login":%q,"password":%q}`, login, password))
	if code != 200 {
		t.Fatalf("login %s: %d %s", login, code, rec.Body)
	}
	totp, _ := adminauth.Code(secret, at)
	code, _, rec = c.do("POST", "/api/admin-auth/verify", fmt.Sprintf(`{"challenge":%q,"code":%q}`, out["challenge"], totp))
	if code != 200 {
		t.Fatalf("verify %s: %d %s", login, code, rec.Body)
	}
	set := rec.Header().Get("Set-Cookie")
	if !strings.Contains(set, "HttpOnly") || !strings.Contains(set, "SameSite=Strict") || !strings.Contains(set, "Path=/api/admin") {
		t.Fatalf("cookie attributes: %q", set)
	}
	if strings.Contains(rec.Body.String(), "adms_") {
		t.Fatal("session token leaked in the response body")
	}
	c.cookie = strings.Split(set, ";")[0]
	return c
}

func TestAdminHTTPIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano() % 1e9)
	pw := "correct horse battery"
	_, rootSecret, _, err := s.BootstrapAdmin(ctx, store.AdminUserInput{Username: "root" + sfx, Email: "root" + sfx + "@x.test", Roles: []string{"super_admin"}}, pw)
	if err != nil {
		t.Fatal(err)
	}
	_, secondSecret, _, err := s.BootstrapAdmin(ctx, store.AdminUserInput{Username: "two" + sfx, Email: "two" + sfx + "@x.test", Roles: []string{"super_admin"}}, pw)
	if err != nil {
		t.Fatal(err)
	}
	modUser, modSecret, _, err := s.BootstrapAdmin(ctx, store.AdminUserInput{Username: "mod" + sfx, Email: "mod" + sfx + "@x.test", Roles: []string{"moderator"}}, pw)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADMIN_TOKEN", "glass-"+sfx)
	h := api.New(s, nil)
	now := time.Now()
	root := signIn(t, h, "root"+sfx, pw, rootSecret, now)
	two := signIn(t, h, "two"+sfx, pw, secondSecret, now)
	mod := signIn(t, h, "mod"+sfx, pw, modSecret, now)

	code, out, _ := mod.do("GET", "/api/admin/session", "")
	if code != 200 || out["actor"] != "mod"+sfx {
		t.Fatalf("session: %d %v", code, out)
	}
	if code, _, _ := mod.do("GET", "/api/admin/admin-users", ""); code != 403 {
		t.Errorf("moderator listing admins: %d", code)
	}
	// Without the CSRF header the cookie alone is refused.
	req := httptest.NewRequest("GET", "/api/admin/session", nil)
	req.Header.Set("Cookie", mod.cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("cookie without CSRF header: %d", rec.Code)
	}

	// Role change: requested by root, refused for root, approved by two.
	path := fmt.Sprintf("/api/admin/admin-users/%d/roles", modUser.ID)
	code, out, rec = root.do("PUT", path, `{"roles":["moderator","translator"]}`)
	if code != 202 || out["approval_required"] != true {
		t.Fatalf("role change request: %d %s", code, rec.Body)
	}
	approvalID := int64(out["approval"].(map[string]any)["id"].(float64))
	approve := fmt.Sprintf("/api/admin/approvals/%d/approve", approvalID)
	if code, _, rec := root.do("POST", approve, ""); code != 409 {
		t.Errorf("self approval: %d %s", code, rec.Body)
	}
	if code, _, _ := mod.do("POST", approve, ""); code != 403 {
		t.Errorf("moderator approving a role change: %d", code)
	}
	code, out, rec = two.do("POST", approve, "")
	if code != 200 || out["result_status"] != float64(200) {
		t.Fatalf("approve: %d %s", code, rec.Body)
	}
	after, _ := s.AdminUserByID(ctx, modUser.ID)
	if strings.Join(after.Roles, ",") != "moderator,translator" {
		t.Fatalf("roles after approval: %v", after.Roles)
	}
	var actor string
	if err := s.QueryRow(ctx, `SELECT actor FROM review_events WHERE entity_type='admin_user' AND entity_id=$1 AND action='admin.roles' ORDER BY id DESC LIMIT 1`, modUser.ID).Scan(&actor); err != nil || actor != "root"+sfx {
		t.Errorf("review_events actor %q %v", actor, err)
	}
	// The moderator now has i18n write through the new role.
	if code, _, _ := mod.do("GET", "/api/admin/i18n/stats", ""); code == 403 {
		t.Error("new role not effective")
	}

	// Break-glass works, is logged with a hashed IP, and records token:<label>.
	breq := httptest.NewRequest("GET", "/api/admin/admin-users", nil)
	breq.Header.Set("Authorization", "Bearer glass-"+sfx)
	breq.Header.Set("X-Admin-User", "oncall"+sfx)
	brec := httptest.NewRecorder()
	h.ServeHTTP(brec, breq)
	if brec.Code != 200 {
		t.Fatalf("break-glass: %d", brec.Code)
	}
	code, out, _ = root.do("GET", "/api/admin/audit-log?actor=token:oncall"+sfx, "")
	items, _ := out["items"].([]any)
	if code != 200 || len(items) != 1 || items[0].(map[string]any)["ip_hash"] == "" {
		t.Fatalf("break-glass audit: %d %v", code, out)
	}
	code, out, _ = root.do("GET", "/api/admin/audit-log?actor=root"+sfx, "")
	found := map[string]bool{}
	for _, it := range out["items"].([]any) {
		found[it.(map[string]any)["action"].(string)] = true
	}
	if !found["auth.login"] || !found["approval.request"] || !found["admin_users.put"] {
		t.Errorf("root's op log actions %v", found)
	}

	// Service token: created once by a super admin, scoped, logged, revocable.
	if code, _, _ := mod.do("POST", "/api/admin/service-tokens", `{"name":"x`+sfx+`","scopes":["dashboard:read"]}`); code != 403 {
		t.Errorf("moderator creating a service token: %d", code)
	}
	code, out, rec = root.do("POST", "/api/admin/service-tokens", `{"name":"bot`+sfx+`","scopes":["dashboard:read"],"expires_in_days":7}`)
	if code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create service token: %d %s", code, rec.Body)
	}
	svc, _ := out["token"].(string)
	svcID := int64(out["service_token"].(map[string]any)["id"].(float64))
	bot := func(method, path string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+svc)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if c := bot("GET", "/api/admin/dashboard"); c != 200 {
		t.Errorf("service token in scope: %d", c)
	}
	if c := bot("GET", "/api/admin/admin-users"); c != 403 {
		t.Errorf("service token on admin users: %d", c)
	}
	code, out, _ = root.do("GET", "/api/admin/audit-log?auth=service&actor=service:bot"+sfx, "")
	if items, _ := out["items"].([]any); code != 200 || len(items) != 2 {
		t.Errorf("service token op log: %d %v", code, out)
	}
	code, out, _ = root.do("GET", "/api/admin/service-tokens", "")
	if code != 200 || strings.Contains(fmt.Sprint(out), svc) {
		t.Errorf("token list leaks the token or failed: %d", code)
	}
	if code, _, _ := root.do("POST", fmt.Sprintf("/api/admin/service-tokens/%d/revoke", svcID), ""); code != 200 {
		t.Errorf("revoke: %d", code)
	}
	if c := bot("GET", "/api/admin/dashboard"); c != 401 {
		t.Errorf("revoked service token: %d", c)
	}

	// Logout revokes the session.
	if code, _, _ := mod.do("POST", "/api/admin/session/logout", ""); code != 200 {
		t.Fatalf("logout %d", code)
	}
	if code, _, _ := mod.do("GET", "/api/admin/session", ""); code != 401 {
		t.Errorf("session after logout: %d", code)
	}
}
