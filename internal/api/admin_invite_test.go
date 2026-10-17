package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// inviteAccounts adds the invite routes to fakeAccounts: two invited
// accounts (7 is a super_admin, 8 a translator) and call counters.
type inviteAccounts struct {
	*fakeAccounts
	invited, reissued int
	inviteActor       string
}

const superTarget, translatorTarget = 7, 8

func (a *inviteAccounts) AdminUsers(context.Context) ([]store.AdminUser, error) {
	return []store.AdminUser{
		{ID: superTarget, Username: "new-super", Status: "invited", Roles: []string{"super_admin"}},
		{ID: translatorTarget, Username: "new-tr", Status: "invited", Roles: []string{"translator"}},
	}, nil
}

func (a *inviteAccounts) InviteAdmin(_ context.Context, in store.AdminUserInput, actor string) (store.AdminUser, string, error) {
	a.invited++
	a.inviteActor = actor
	return store.AdminUser{ID: 9, Username: in.Username, Roles: in.Roles}, "admi_test_token", nil
}

func (a *inviteAccounts) ReissueAdminInvite(_ context.Context, id int64, actor string) (string, error) {
	a.reissued++
	return "admi_reissued", nil
}

func (a *inviteAccounts) ActiveAdminPrincipal(_ context.Context, id int64) (store.AdminPrincipal, error) {
	if p, _ := testPrincipal(testAdminToken("super-a")); p.ID == id {
		return p, nil
	}
	return store.AdminPrincipal{}, errNotFound
}

// A single super admin must not be able to mint a second super_admin
// account (an invite, or a fresh enrollment link for one) without a second
// admin: that account could then approve the first admin's own
// dual-approval requests (role changes, 2FA resets), defeating them.
func TestSuperAdminEnrollmentLinksNeedSecondAdmin(t *testing.T) {
	f := newFakeAdmins()
	acc := &inviteAccounts{fakeAccounts: &fakeAccounts{f: f, inactive: map[int64]bool{}}}
	srv := &Server{adminAuth: f, adminAccts: acc, accounts: &stubAccounts{}, adminToken: "break-glass-secret"}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	superA, superB := adminAs("super-a"), adminAs("super-b")
	superInvite := `{"username":"puppet","email":"puppet@example.test","display_name":"","roles":["super_admin"]}`

	// Inviting a super_admin is held for approval; nothing is created.
	rec := doH(mux, "POST", "/api/admin/admin-users", superInvite, superA)
	if rec.Code != http.StatusAccepted || acc.invited != 0 || strings.Contains(rec.Body.String(), "enroll_url") {
		t.Fatalf("super_admin invite: %d %s (invites %d)", rec.Code, rec.Body, acc.invited)
	}
	var created struct {
		Approval approvalItem `json:"approval"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.Approval.ID == 0 || created.Approval.Route != "POST /api/admin/admin-users" || !strings.Contains(created.Approval.Summary, "超级管理员") {
		t.Fatalf("approval %+v", created.Approval)
	}
	// The requester cannot approve it; another super admin can, and the
	// invite then runs as the requester with the enrollment link in the result.
	path := fmt.Sprintf("/api/admin/approvals/%d/approve", created.Approval.ID)
	if rec := doH(mux, "POST", path, "", superA); rec.Code != http.StatusConflict || acc.invited != 0 {
		t.Fatalf("self approval: %d %s", rec.Code, rec.Body)
	}
	rec = doH(mux, "POST", path, "", superB)
	if rec.Code != 200 || acc.invited != 1 || acc.inviteActor != "super-a" || !strings.Contains(rec.Body.String(), `"enroll_url":"/admin/enroll#admi_test_token"`) {
		t.Fatalf("approved invite: %d %s (invites %d, actor %q)", rec.Code, rec.Body, acc.invited, acc.inviteActor)
	}

	// Other roles are invited at once, as before.
	rec = doH(mux, "POST", "/api/admin/admin-users", `{"username":"tr","email":"tr@example.test","roles":["translator"]}`, superA)
	if rec.Code != 200 || acc.invited != 2 || !strings.Contains(rec.Body.String(), "enroll_url") {
		t.Fatalf("translator invite: %d %s", rec.Code, rec.Body)
	}

	// A new link for an invited super_admin is held too; for others it is not.
	rec = doH(mux, "POST", fmt.Sprintf("/api/admin/admin-users/%d/invite", superTarget), "", superA)
	if rec.Code != http.StatusAccepted || acc.reissued != 0 {
		t.Fatalf("super_admin reissue: %d %s (reissued %d)", rec.Code, rec.Body, acc.reissued)
	}
	rec = doH(mux, "POST", fmt.Sprintf("/api/admin/admin-users/%d/invite", translatorTarget), "", superA)
	if rec.Code != 200 || acc.reissued != 1 {
		t.Fatalf("translator reissue: %d %s (reissued %d)", rec.Code, rec.Body, acc.reissued)
	}
	if rec := doH(mux, "POST", "/api/admin/admin-users/99/invite", "", superA); rec.Code != http.StatusNotFound {
		t.Errorf("unknown admin reissue: %d %s", rec.Code, rec.Body)
	}

	// Break-glass stays the documented bypass (logged on every use).
	rec = doH(mux, "POST", "/api/admin/admin-users", superInvite, map[string]string{"Authorization": "Bearer break-glass-secret"})
	if rec.Code != 200 || acc.invited != 3 {
		t.Fatalf("break-glass invite: %d %s", rec.Code, rec.Body)
	}
}
