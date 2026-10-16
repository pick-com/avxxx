package store

import (
	"avxxx/internal/adminauth"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestAdminUsersIntegration covers migration 042: bootstrap, two-step
// sign-in with replay protection, lockout, recovery codes, session idle
// expiry, invites and enrollment, role / status guards, 2FA reset, dual
// approvals, the op log and site-user bans.
func TestAdminUsersIntegration(t *testing.T) {
	openIsolatedTestDB(t, "adminusers")
	t.Setenv(adminauth.KeyEnv, "integration-key")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // migration 042 is idempotent
		s, err := Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var roles int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM admin_roles`).Scan(&roles); err != nil || roles != 7 {
		t.Fatalf("admin_roles: %d %v", roles, err)
	}

	// ---- Bootstrap and sign-in ----
	if _, _, _, err := s.BootstrapAdmin(ctx, AdminUserInput{Username: "root", Email: "root@x.test", Roles: []string{"super_admin"}}, "short"); err == nil {
		t.Fatal("short password accepted")
	}
	root, secret, codes, err := s.BootstrapAdmin(ctx, AdminUserInput{Username: "Root", Email: "root@x.test", DisplayName: "Root", Roles: []string{"super_admin"}}, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if root.Username != "root" || root.Status != "active" || root.TOTPEnrolled || len(codes) != adminauth.RecoveryCodeCount || root.RecoveryCodesLeft != adminauth.RecoveryCodeCount {
		t.Fatalf("bootstrap %+v", root)
	}
	var stored string
	_ = s.QueryRow(ctx, `SELECT totp_secret FROM admin_users WHERE id=$1`, root.ID).Scan(&stored)
	if !strings.HasPrefix(stored, "v1:") || strings.Contains(stored, secret) {
		t.Fatalf("TOTP secret not sealed: %q", stored)
	}
	if _, _, _, err := s.BootstrapAdmin(ctx, AdminUserInput{Username: "root", Email: "other@x.test", Roles: []string{"super_admin"}}, "correct horse battery"); err == nil {
		t.Fatal("duplicate username accepted")
	}

	if _, _, err := s.AdminPasswordLogin(ctx, "nobody", "whatever-password"); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("unknown login: %v", err)
	}
	ch, id, err := s.AdminPasswordLogin(ctx, "ROOT@x.test", "correct horse battery")
	if err != nil || id != root.ID || !strings.HasPrefix(ch, "admc_") {
		t.Fatalf("password step: %q %d %v", ch, id, err)
	}
	if _, _, err := s.AdminSecondFactor(ctx, ch, "000000", "", AdminSessionMeta{}); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("wrong code: %v", err)
	}
	code, _ := adminauth.Code(secret, time.Now())
	sess, _, err := s.AdminSecondFactor(ctx, ch, code, "", AdminSessionMeta{IPHash: "abc", UserAgent: "test"})
	if err != nil || !strings.HasPrefix(sess.Token, "adms_") || sess.Admin.Username != "root" || sess.Method != "totp" {
		t.Fatalf("second factor: %+v %v", sess, err)
	}
	if _, _, err := s.AdminSecondFactor(ctx, ch, code, "", AdminSessionMeta{}); !errors.Is(err, ErrAdminChallenge) {
		t.Fatalf("challenge reused: %v", err)
	}
	p, err := s.AdminBySession(ctx, sess.Token)
	if err != nil || p.ID != root.ID || len(p.Roles) != 1 || p.Roles[0] != "super_admin" {
		t.Fatalf("session lookup %+v %v", p, err)
	}
	// The same TOTP step is never accepted twice.
	ch2, _, _ := s.AdminPasswordLogin(ctx, "root", "correct horse battery")
	if _, _, err := s.AdminSecondFactor(ctx, ch2, code, "", AdminSessionMeta{}); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("replayed TOTP code: %v", err)
	}
	// Recovery codes work once.
	sess2, _, err := s.AdminSecondFactor(ctx, ch2, "", strings.ToUpper(codes[0]), AdminSessionMeta{})
	if err != nil || sess2.Method != "recovery_code" || sess2.RecoveryCodesLeft == nil || *sess2.RecoveryCodesLeft != adminauth.RecoveryCodeCount-1 {
		t.Fatalf("recovery code: %+v %v", sess2, err)
	}
	ch3, _, _ := s.AdminPasswordLogin(ctx, "root", "correct horse battery")
	if _, _, err := s.AdminSecondFactor(ctx, ch3, "", codes[0], AdminSessionMeta{}); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("recovery code reused: %v", err)
	}
	// Idle timeout and revocation.
	if _, err := s.Exec(ctx, `UPDATE admin_sessions SET last_seen_at=now()-interval '31 minutes' WHERE token_hash=$1`, adminauth.HashToken(sess2.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(ctx, sess2.Token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("idle session still valid: %v", err)
	}
	if _, err := s.Exec(ctx, `UPDATE admin_sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, adminauth.HashToken(sess.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdminBySession(ctx, sess.Token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("expired session still valid: %v", err)
	}

	// ---- Lockout ----
	if _, err := s.Exec(ctx, `UPDATE admin_users SET failed_attempts=0, locked_until=NULL`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < adminauth.LockoutThreshold; i++ {
		if _, _, err := s.AdminPasswordLogin(ctx, "root", "wrong password!"); !errors.Is(err, ErrAdminCredentials) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	var le *AdminLockedError
	if _, _, err := s.AdminPasswordLogin(ctx, "root", "correct horse battery"); !errors.As(err, &le) || time.Until(le.Until) < 50*time.Second {
		t.Fatalf("not locked after %d failures: %v", adminauth.LockoutThreshold, err)
	}
	if _, err := s.Exec(ctx, `UPDATE admin_users SET locked_until=now()-interval '1 second' WHERE id=$1`, root.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AdminPasswordLogin(ctx, "root", "wrong again!!"); !errors.Is(err, ErrAdminCredentials) {
		t.Fatal(err)
	}
	var locked time.Time
	_ = s.QueryRow(ctx, `SELECT locked_until FROM admin_users WHERE id=$1`, root.ID).Scan(&locked)
	if d := time.Until(locked); d < 110*time.Second || d > 125*time.Second {
		t.Fatalf("backoff after 6 failures: %v, want ~2m", d)
	}
	if _, err := s.Exec(ctx, `UPDATE admin_users SET failed_attempts=0, locked_until=NULL`); err != nil {
		t.Fatal(err)
	}

	// ---- Invite and enrollment ----
	if _, _, err := s.InviteAdmin(ctx, AdminUserInput{Username: "ed", Email: "ed@x.test", Roles: []string{"root"}}, "root"); err == nil {
		t.Fatal("unknown role accepted")
	}
	ed, inviteTok, err := s.InviteAdmin(ctx, AdminUserInput{Username: "ed", Email: "ed@x.test", Roles: []string{"content_editor", "translator"}}, "root")
	if err != nil || ed.Status != "invited" || len(ed.Roles) != 2 || ed.Roles[0] != "content_editor" {
		t.Fatalf("invite %+v %v", ed, err)
	}
	if _, _, err := s.AdminPasswordLogin(ctx, "ed", "anything at all"); !errors.Is(err, ErrAdminCredentials) {
		t.Fatalf("invited admin signed in: %v", err)
	}
	inv, err := s.AdminInviteInfo(ctx, inviteTok)
	if err != nil || inv.Username != "ed" || !strings.HasPrefix(inv.OTPAuthURI, "otpauth://totp/") {
		t.Fatalf("invite info %+v %v", inv, err)
	}
	inv2, _ := s.AdminInviteInfo(ctx, inviteTok)
	if inv2.Secret != inv.Secret {
		t.Fatal("invite secret changed between views")
	}
	if _, _, err := s.CompleteAdminEnrollment(ctx, inviteTok, "long enough password", "123456"); err == nil {
		t.Fatal("wrong enrollment code accepted")
	}
	edCode, _ := adminauth.Code(inv.Secret, time.Now())
	if _, edCodes, err := s.CompleteAdminEnrollment(ctx, inviteTok, "long enough password", edCode); err != nil || len(edCodes) != adminauth.RecoveryCodeCount {
		t.Fatalf("enroll: %v", err)
	}
	if _, err := s.AdminInviteInfo(ctx, inviteTok); !errors.Is(err, ErrAdminInvite) {
		t.Fatalf("invite reusable: %v", err)
	}
	edCh, _, err := s.AdminPasswordLogin(ctx, "ed", "long enough password")
	if err != nil {
		t.Fatal(err)
	}
	next, _ := adminauth.Code(inv.Secret, time.Now().Add(30*time.Second))
	edSess, _, err := s.AdminSecondFactor(ctx, edCh, next, "", AdminSessionMeta{})
	if err != nil {
		t.Fatalf("ed sign-in: %v", err)
	}

	// ---- Role and status guards ----
	if _, err := s.SetAdminRoles(ctx, root.ID, []string{"translator"}, "root", root.ID); err == nil {
		t.Fatal("self role change allowed")
	}
	if _, err := s.SetAdminRoles(ctx, root.ID, []string{"translator"}, "ed", ed.ID); err == nil || !strings.Contains(err.Error(), "last active super admin") {
		t.Fatalf("last super admin demoted: %v", err)
	}
	if _, err := s.SetAdminDisabled(ctx, root.ID, true, "", "ed", ed.ID); err == nil {
		t.Fatal("last super admin disabled")
	}
	up, err := s.SetAdminRoles(ctx, ed.ID, []string{"super_admin"}, "root", root.ID)
	if err != nil || len(up.Roles) != 1 || up.Roles[0] != "super_admin" {
		t.Fatalf("set roles %+v %v", up, err)
	}
	var ev int
	_ = s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='admin_user' AND entity_id=$1 AND action='admin.roles' AND actor='root'`, ed.ID).Scan(&ev)
	if ev != 1 {
		t.Fatalf("roles review_event %d", ev)
	}
	// Now root can be demoted (ed is another active super admin).
	if _, err := s.SetAdminRoles(ctx, root.ID, []string{"super_admin", "moderator"}, "ed", ed.ID); err != nil {
		t.Fatal(err)
	}
	dis, err := s.SetAdminDisabled(ctx, ed.ID, true, "left the team", "root", root.ID)
	if err != nil || dis.Status != "disabled" {
		t.Fatalf("disable %+v %v", dis, err)
	}
	if _, err := s.AdminBySession(ctx, edSess.Token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled admin's session survives: %v", err)
	}
	if _, err := s.ResetAdminTOTP(ctx, ed.ID, "", "root", root.ID); err == nil {
		t.Fatal("reset 2FA of a disabled admin")
	}
	en, err := s.SetAdminDisabled(ctx, ed.ID, false, "back", "root", root.ID)
	if err != nil || en.Status != "active" {
		t.Fatalf("enable %+v %v", en, err)
	}
	resetTok, err := s.ResetAdminTOTP(ctx, ed.ID, "lost phone", "root", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.AdminUserByID(ctx, ed.ID)
	if after.Status != "invited" || after.TOTPEnrolled || after.RecoveryCodesLeft != 0 {
		t.Fatalf("after reset %+v", after)
	}
	if _, err := s.AdminInviteInfo(ctx, resetTok); err != nil {
		t.Fatalf("reset link: %v", err)
	}
	list, err := s.AdminUsers(ctx)
	if err != nil || len(list) != 2 || list[0].Username != "root" {
		t.Fatalf("list %+v %v", list, err)
	}

	// ---- Dual approvals ----
	a, err := s.CreateAdminApproval(ctx, AdminApproval{Route: "PUT /api/admin/admin-users/{id}/roles", Method: "PUT", Path: "/api/admin/admin-users/2/roles",
		Body: []byte(`{"roles":["moderator"]}`), Summary: "x", RequestedBy: root.ID, RequestedByName: "root"})
	if err != nil || a.Status != "pending" {
		t.Fatalf("create approval %+v %v", a, err)
	}
	if _, err := s.ClaimAdminApproval(ctx, a.ID, root.ID, "root"); err == nil || !strings.Contains(err.Error(), "different admin") {
		t.Fatalf("self approval: %v", err)
	}
	c, err := s.ClaimAdminApproval(ctx, a.ID, ed.ID, "ed")
	if err != nil || c.Status != "executing" || c.DecidedBy == nil || *c.DecidedBy != "ed" || string(c.Body) != `{"roles":["moderator"]}` {
		t.Fatalf("claim %+v %v", c, err)
	}
	if _, err := s.ClaimAdminApproval(ctx, a.ID, 0, "token:x"); err == nil {
		t.Fatal("claimed twice")
	}
	if err := s.FinishAdminApproval(ctx, a.ID, true, 200); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateAdminApproval(ctx, AdminApproval{Route: "r", Method: "POST", Path: "/p", RequestedBy: root.ID, RequestedByName: "root"})
	if d, err := s.DecideAdminApproval(ctx, b.ID, root.ID, "root", "oops"); err != nil || d.Status != "cancelled" {
		t.Fatalf("cancel %+v %v", d, err)
	}
	e, _ := s.CreateAdminApproval(ctx, AdminApproval{Route: "r", Method: "POST", Path: "/p", RequestedBy: root.ID, RequestedByName: "root"})
	if _, err := s.Exec(ctx, `UPDATE admin_approvals SET expires_at=now()-interval '1 second' WHERE id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimAdminApproval(ctx, e.ID, ed.ID, "ed"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired claim: %v", err)
	}
	all, _ := s.AdminApprovals(ctx, "all", 10)
	statuses := map[string]bool{}
	for _, x := range all {
		statuses[x.Status] = true
	}
	if !statuses["executed"] || !statuses["cancelled"] || !statuses["expired"] {
		t.Fatalf("statuses %v", statuses)
	}

	// ---- Op log ----
	rootID := root.ID
	for i, act := range []string{"catalog.patch", "moderation.post", "auth.login"} {
		if err := s.RecordAdminAudit(ctx, AdminAuditEntry{AdminID: &rootID, Actor: "root", Auth: "session", Action: act, Method: "POST",
			Route: "POST /api/admin/releases/{id}/status", Path: "/api/admin/releases/9/status", EntityType: "releases", EntityID: "9", Status: 200 + i, IPHash: "h"}); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.RecordAdminAudit(ctx, AdminAuditEntry{Actor: "token:seo", Auth: "token", Action: "ingest.post", Route: "POST /api/admin/seo/report"})
	items, cursor, err := s.AdminAuditLog(ctx, AdminAuditFilter{Limit: 2})
	if err != nil || len(items) != 2 || cursor == "" || items[0].Actor != "token:seo" {
		t.Fatalf("page 1 %+v %q %v", items, cursor, err)
	}
	items, cursor, _ = s.AdminAuditLog(ctx, AdminAuditFilter{Limit: 2, Cursor: cursor})
	if len(items) != 2 || cursor != "" {
		t.Fatalf("page 2 %d %q", len(items), cursor)
	}
	if items, _, _ := s.AdminAuditLog(ctx, AdminAuditFilter{Q: "moderation"}); len(items) != 1 {
		t.Fatalf("q filter %d", len(items))
	}
	if items, _, _ := s.AdminAuditLog(ctx, AdminAuditFilter{Auth: "token"}); len(items) != 1 {
		t.Fatalf("auth filter %d", len(items))
	}
	if items, _, _ := s.AdminAuditLog(ctx, AdminAuditFilter{EntityType: "releases", EntityID: "9", Actor: "root"}); len(items) != 3 {
		t.Fatalf("entity filter %d", len(items))
	}
	if items, _, _ := s.AdminAuditLog(ctx, AdminAuditFilter{Q: "100%_"}); len(items) != 0 {
		t.Fatal("LIKE wildcards not escaped")
	}

	// ---- Site users: search, ban, unban ----
	if _, err := s.Register(ctx, "fan@x.test", "password1", "Fan One"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, "other@x.test", "password1", "Someone"); err != nil {
		t.Fatal(err)
	}
	found, err := s.SearchSiteUsers(ctx, "FAN", "", 10)
	if err != nil || len(found) != 1 || found[0].Email != "fan@x.test" || found[0].Banned {
		t.Fatalf("search %+v %v", found, err)
	}
	fan := found[0]
	if byName, _ := s.SearchSiteUsers(ctx, "n on", "", 10); len(byName) != 1 {
		t.Fatalf("display name search %d", len(byName))
	}
	if byID, _ := s.SearchSiteUsers(ctx, "#"+strconv.FormatInt(fan.ID, 10), "", 10); len(byID) != 1 {
		t.Fatalf("id search %d", len(byID))
	}
	if _, err := s.BanUser(ctx, fan.ID, nil, "", "mod"); err == nil {
		t.Fatal("ban without reason")
	}
	banned, err := s.BanUser(ctx, fan.ID, nil, "spam ring", "mod")
	if err != nil || !banned.Banned || banned.BannedUntil != nil || banned.BanReason != "spam ring" {
		t.Fatalf("ban %+v %v", banned, err)
	}
	var sessions int
	_ = s.QueryRow(ctx, `SELECT count(*) FROM user_sessions WHERE user_id=$1`, fan.ID).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("banned user keeps %d sessions", sessions)
	}
	if _, err := s.Login(ctx, "fan@x.test", "password1"); !errors.Is(err, ErrAccountBanned) {
		t.Fatalf("banned login: %v", err)
	}
	if list, _ := s.SearchSiteUsers(ctx, "", "banned", 10); len(list) != 1 {
		t.Fatalf("banned filter %d", len(list))
	}
	if _, err := s.UnbanUser(ctx, fan.ID, "appeal", "mod"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UnbanUser(ctx, fan.ID, "again", "mod"); err == nil {
		t.Fatal("unban of a user who is not banned")
	}
	if _, err := s.Login(ctx, "fan@x.test", "password1"); err != nil {
		t.Fatalf("login after unban: %v", err)
	}
	// A timed ban that has run out no longer blocks.
	past := time.Now().Add(time.Hour)
	if _, err := s.BanUser(ctx, fan.ID, &past, "cool off", "mod"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `UPDATE user_bans SET until=now()-interval '1 second' WHERE user_id=$1`, fan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Login(ctx, "fan@x.test", "password1"); err != nil {
		t.Fatalf("expired ban still blocks: %v", err)
	}
	var banEvents int
	_ = s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='user' AND entity_id=$1 AND action IN ('moderation.ban','moderation.unban') AND actor='mod'`, fan.ID).Scan(&banEvents)
	if banEvents != 3 {
		t.Fatalf("ban review_events %d", banEvents)
	}
}
