package api

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/store"
	"context"
	"hash/fnv"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakeAdmins is an in-memory adminAuthStore (sessions, op log, approval
// requests) for handler tests. Give a test server one (adminAuth:
// newFakeAdmins()) and authenticate requests with adminAs: the token
// "adms_test_<username>[:role,role]" signs in as that admin (super_admin
// when no roles are given).
type fakeAdmins struct {
	mu        sync.Mutex
	revoked   map[string]bool
	audit     []store.AdminAuditEntry
	approvals []store.AdminApproval
}

func newFakeAdmins() *fakeAdmins { return &fakeAdmins{revoked: map[string]bool{}} }

const testTokenPrefix = "adms_test_"

// testPrincipal decodes a test token.
func testPrincipal(token string) (store.AdminPrincipal, bool) {
	rest, ok := strings.CutPrefix(token, testTokenPrefix)
	if !ok || rest == "" {
		return store.AdminPrincipal{}, false
	}
	name, roleList, _ := strings.Cut(rest, ":")
	roles := []string{string(adminauth.RoleSuperAdmin)}
	if roleList != "" {
		roles = strings.Split(roleList, ",")
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	return store.AdminPrincipal{ID: int64(h.Sum32()%100000) + 1, Username: name, Email: name + "@example.test", Roles: roles}, true
}

func (f *fakeAdmins) AdminBySession(_ context.Context, token string) (store.AdminPrincipal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := testPrincipal(token)
	if !ok || f.revoked[token] {
		return store.AdminPrincipal{}, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeAdmins) RecordAdminAudit(_ context.Context, e store.AdminAuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.audit = append(f.audit, e)
	return nil
}

func (f *fakeAdmins) CreateAdminApproval(_ context.Context, a store.AdminApproval) (store.AdminApproval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a.ID = int64(len(f.approvals) + 1)
	a.Status = "pending"
	a.RequestedAt = time.Now()
	a.ExpiresAt = a.RequestedAt.Add(adminauth.ApprovalTTL)
	f.approvals = append(f.approvals, a)
	return a, nil
}

func (f *fakeAdmins) audits() []store.AdminAuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.AdminAuditEntry(nil), f.audit...)
}

// adminToken returns the test session token for username with roles.
func testAdminToken(username string, roles ...string) string {
	t := testTokenPrefix + username
	if len(roles) > 0 {
		t += ":" + strings.Join(roles, ",")
	}
	return t
}

// adminAs returns the headers that authenticate as a test admin (the
// session cookie and the CSRF header); the username is the recorded actor.
func adminAs(username string, roles ...string) map[string]string {
	return map[string]string{"Cookie": adminCookie + "=" + testAdminToken(username, roles...), adminCSRFHeader: "1"}
}

// asAdmin sets adminAs's headers on req.
func asAdmin(req *http.Request, username string, roles ...string) *http.Request {
	for k, v := range adminAs(username, roles...) {
		req.Header.Set(k, v)
	}
	return req
}
