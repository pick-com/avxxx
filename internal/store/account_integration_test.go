package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestAccountIntegration covers migration 037 and the account self-service
// store: sessions and devices, remember-me TTLs, logout, password change and
// reset, terms, deletion with grace and purge, export, OAuth identities and
// login codes, interests, the daily cap and day-before reminders.
func TestAccountIntegration(t *testing.T) {
	openIsolatedTestDB(t, "account")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // migration 037 is idempotent
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	token := func(m map[string]any) string { return m["token"].(string) }

	// --- register / login / sessions -------------------------------------
	reg, err := s.RegisterSession(ctx, "Ann@Example.com", "password-1", " Ann ", "2026-10-01", Adult{Confirmed: true}, SessionMeta{Remember: false, UserAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/130.0 Safari/537.36"})
	must(err)
	t1 := token(reg)
	uid := reg["user"].(map[string]any)["id"].(int64)
	if exp := reg["expires_at"].(time.Time); exp.After(time.Now().Add(SessionTTLBrowser + time.Minute)) {
		t.Errorf("browser session expires %v, want ~24h", exp)
	}
	me, err := s.UserByToken(ctx, t1)
	must(err)
	if me["display_name"] != "Ann" || me["terms_version"] != "2026-10-01" || me["has_password"] != true || me["email"] != "ann@example.com" {
		t.Errorf("me = %v", me)
	}
	login, err := s.LoginSession(ctx, "ann@example.com", "password-1", SessionMeta{Remember: true, UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0) Safari/604.1"})
	must(err)
	t2 := token(login)
	if exp := login["expires_at"].(time.Time); exp.Before(time.Now().Add(SessionTTLRemember - time.Hour)) {
		t.Errorf("remembered session expires %v", exp)
	}
	sessions, err := s.Sessions(ctx, uid, t2)
	must(err)
	if len(sessions) != 2 || !sessions[0].Current || sessions[0].Device != "Safari · iPhone" || sessions[1].Device != "Chrome · macOS" {
		t.Fatalf("sessions = %+v", sessions)
	}
	// Another user cannot revoke Ann's session by id.
	other, err := s.RegisterSession(ctx, "bob@example.com", "password-2", "Bob", "", Adult{Confirmed: true}, SessionMeta{Remember: true})
	must(err)
	bob := other["user"].(map[string]any)["id"].(int64)
	if ok, err := s.RevokeSession(ctx, bob, sessions[1].ID); err != nil || ok {
		t.Errorf("foreign revoke = %v %v", ok, err)
	}
	if ok, err := s.RevokeSession(ctx, uid, sessions[1].ID); err != nil || !ok {
		t.Errorf("own revoke = %v %v", ok, err)
	}
	if _, err := s.UserIDByToken(ctx, t1); err == nil {
		t.Error("revoked session still valid")
	}
	must(s.Logout(ctx, t2))
	if _, err := s.UserIDByToken(ctx, t2); err == nil {
		t.Error("logged-out session still valid")
	}

	// --- password change revokes other sessions --------------------------
	a, _ := s.LoginSession(ctx, "ann@example.com", "password-1", SessionMeta{Remember: true})
	b, _ := s.LoginSession(ctx, "ann@example.com", "password-1", SessionMeta{Remember: true})
	if err := s.ChangePassword(ctx, uid, "wrong", "password-3", token(a)); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong current password: %v", err)
	}
	must(s.ChangePassword(ctx, uid, "password-1", "password-3", token(a)))
	if _, err := s.UserIDByToken(ctx, token(b)); err == nil {
		t.Error("other session survived a password change")
	}
	if _, err := s.UserIDByToken(ctx, token(a)); err != nil {
		t.Error("current session revoked by a password change")
	}
	if _, err := s.LoginSession(ctx, "ann@example.com", "password-1", SessionMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("old password still works")
	}

	// --- password reset -----------------------------------------------------
	must(s.RequestPasswordReset(ctx, "nobody@example.com"))
	for i := 0; i < PasswordResetPerHour+2; i++ {
		must(s.RequestPasswordReset(ctx, "ANN@example.com"))
	}
	var queued int
	var payload string
	must(s.QueryRow(ctx, `SELECT count(*), max(payload::text) FROM notification_outbox WHERE user_id=$1 AND payload->>'kind'='password_reset'`, uid).Scan(&queued, &payload))
	if queued != PasswordResetPerHour || strings.Contains(payload, "token") {
		t.Errorf("reset outbox rows = %d payload %s", queued, payload)
	}
	resetTok, _, err := s.IssuePasswordReset(ctx, uid)
	must(err)
	var stored string
	must(s.QueryRow(ctx, `SELECT token_hash FROM password_resets WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, uid).Scan(&stored))
	if stored == resetTok || stored != hashToken(resetTok) {
		t.Error("reset token stored in plaintext")
	}
	must(s.ResetPassword(ctx, resetTok, "password-4"))
	if err := s.ResetPassword(ctx, resetTok, "password-5"); !errors.Is(err, ErrInvalidResetToken) {
		t.Errorf("reused reset token: %v", err)
	}
	if _, err := s.UserIDByToken(ctx, token(a)); err == nil {
		t.Error("sessions survived a password reset")
	}
	expired, _, _ := s.IssuePasswordReset(ctx, uid)
	_, err = s.Exec(ctx, `UPDATE password_resets SET expires_at=now()-interval '1 minute' WHERE token_hash=$1`, hashToken(expired))
	must(err)
	if err := s.ResetPassword(ctx, expired, "password-6"); !errors.Is(err, ErrInvalidResetToken) {
		t.Errorf("expired reset token: %v", err)
	}

	// --- terms, profile, interests -----------------------------------------
	cur, _ := s.LoginSession(ctx, "ann@example.com", "password-4", SessionMeta{Remember: true})
	must(s.AcceptTerms(ctx, uid, "2026-11-01", "prompt"))
	me, _ = s.UserByToken(ctx, token(cur))
	if me["terms_version"] != "2026-11-01" {
		t.Errorf("terms version %v", me["terms_version"])
	}
	if _, err := s.UpdateProfile(ctx, uid, strings.Repeat("名", 41)); err == nil {
		t.Error("41-rune nickname accepted")
	}
	tags, err := s.SetInterests(ctx, uid, []string{"drama", "drama", "comedy"}, true)
	must(err)
	if len(tags) != 2 {
		t.Errorf("interests %v", tags)
	}
	if _, err := s.SetInterests(ctx, uid, []string{"<script>"}, false); err == nil {
		t.Error("bad tag accepted")
	}

	// --- export ------------------------------------------------------------
	// Comments, 赞 / 踩, recent searches and cookie choices are the user's
	// data too (the settings row promises 评论).
	var exportRel, commentID int64
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('acct-export','导出作品','published') RETURNING id`).Scan(&exportRel))
	must(s.QueryRow(ctx, `INSERT INTO comments(release_id,user_id,body) VALUES($1,$2,'导出评论') RETURNING id`, exportRel, uid).Scan(&commentID))
	_, err = s.Exec(ctx, `INSERT INTO comment_likes(user_id,comment_id) VALUES($1,$2)`, uid, commentID)
	must(err)
	_, err = s.Exec(ctx, `INSERT INTO release_reactions(release_id,voter_key,user_id,value) VALUES($1,$2,$3,1)`, exportRel, fmt.Sprintf("u:%d", uid), uid)
	must(err)
	must(s.RecordSearch(ctx, uid, "导出搜索", ""))
	_, err = s.Exec(ctx, `INSERT INTO consent_records(user_id,categories,source) VALUES($1,ARRAY['necessary'],'banner')`, uid)
	must(err)
	var buf bytes.Buffer
	must(s.WriteDataExport(ctx, uid, &buf))
	var export map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &export); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, buf.String())
	}
	for _, sec := range []string{"account", "settings", "sessions", "terms_acceptances", "interests", "watch_history", "playlists", "reviews",
		"comments", "comment_likes", "reactions", "search_history", "consent_records"} {
		if _, ok := export[sec]; !ok {
			t.Errorf("export misses %s", sec)
		}
	}
	for sec, want := range map[string]string{"comments": "导出评论", "reactions": "acct-export", "search_history": "导出搜索", "consent_records": "banner", "comment_likes": "comment_id"} {
		if !strings.Contains(string(export[sec]), want) {
			t.Errorf("export %s = %s, want %q", sec, export[sec], want)
		}
	}
	for _, secret := range []string{"password_hash", "token_hash", "$argon2id", hashToken(token(cur))} {
		if strings.Contains(buf.String(), secret) {
			t.Errorf("export leaks %q", secret)
		}
	}
	if strings.Contains(buf.String(), "bob@example.com") {
		t.Error("export contains another user's data")
	}
	for i := 0; i < DataExportsPerDay; i++ {
		must(s.ReserveDataExport(ctx, uid))
	}
	if err := s.ReserveDataExport(ctx, uid); !errors.Is(err, ErrRateLimited) {
		t.Errorf("export limit: %v", err)
	}

	// --- deletion: grace, cancel by login, purge -----------------------------
	if _, err := s.RequestAccountDeletion(ctx, uid, "nope"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("deletion with wrong password: %v", err)
	}
	after, err := s.RequestAccountDeletion(ctx, uid, "password-4")
	must(err)
	if d := time.Until(after); d < AccountDeletionGrace-time.Hour || d > AccountDeletionGrace+time.Hour {
		t.Errorf("purge_after in %v", d)
	}
	if _, err := s.UserIDByToken(ctx, token(cur)); err == nil {
		t.Error("session survived a deletion request")
	}
	again, err := s.LoginSession(ctx, "ann@example.com", "password-4", SessionMeta{})
	must(err)
	if again["deletion_cancelled"] != true {
		t.Errorf("login did not cancel deletion: %v", again)
	}
	if n, err := s.PurgeDeletedAccounts(ctx, 0, 10); err != nil || n != 0 {
		t.Errorf("purge after cancel = %d %v", n, err)
	}
	_, err = s.RequestAccountDeletion(ctx, bob, "password-2")
	must(err)
	if n, err := s.PurgeDeletedAccounts(ctx, AccountDeletionGrace, 10); err != nil || n != 0 {
		t.Errorf("purge inside grace = %d %v", n, err)
	}
	_, err = s.Exec(ctx, `UPDATE users SET deletion_requested_at=now()-interval '31 days' WHERE id=$1`, bob)
	must(err)
	if n, err := s.PurgeDeletedAccounts(ctx, AccountDeletionGrace, 10); err != nil || n != 1 {
		t.Errorf("purge = %d %v", n, err)
	}
	var left int
	must(s.QueryRow(ctx, `SELECT count(*) FROM users WHERE id=$1`, bob).Scan(&left))
	if left != 0 {
		t.Error("purged user still exists")
	}

	// --- OAuth ---------------------------------------------------------------
	if _, _, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-ann", Email: "ann@example.com", EmailVerified: true}, Adult{Confirmed: true}); !errors.Is(err, ErrIdentityEmailTaken) {
		t.Errorf("auto-link to password account: %v", err)
	}
	if _, _, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-x", Email: "x@example.com"}, Adult{Confirmed: true}); !errors.Is(err, ErrNoEmail) {
		t.Errorf("unverified email: %v", err)
	}
	gid, created, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-carol", Email: "Carol@example.com", EmailVerified: true, Name: "Carol"}, Adult{Confirmed: true})
	must(err)
	if !created {
		t.Error("first OAuth sign-in did not create a user")
	}
	if again, created, _ := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-carol"}, Adult{}); again != gid || created {
		t.Errorf("second sign-in = %d %v", again, created)
	}
	if _, err := s.LoginSession(ctx, "carol@example.com", "", SessionMeta{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("OAuth-only account accepts an empty password")
	}
	if err := s.UnlinkIdentity(ctx, gid, "google"); !errors.Is(err, ErrLastSignInMethod) {
		t.Errorf("unlink last method: %v", err)
	}
	must(s.LinkIdentity(ctx, uid, OAuthProfile{Provider: "apple", Subject: "a-ann", Email: "ann@privaterelay.appleid.com"}))
	if err := s.LinkIdentity(ctx, gid, OAuthProfile{Provider: "apple", Subject: "a-ann"}); !errors.Is(err, ErrIdentityTaken) {
		t.Errorf("link taken identity: %v", err)
	}
	must(s.UnlinkIdentity(ctx, uid, "apple")) // Ann still has a password
	code, err := s.CreateOAuthLoginCode(ctx, gid, "google", false, true)
	must(err)
	sess, err := s.ExchangeOAuthLoginCode(ctx, code, "UA")
	must(err)
	if sess["created"] != true || sess["remember"] != false {
		t.Errorf("exchange = %v", sess)
	}
	if _, err := s.ExchangeOAuthLoginCode(ctx, code, "UA"); !errors.Is(err, ErrOAuthState) {
		t.Errorf("code reuse: %v", err)
	}
	must(s.SaveOAuthState(ctx, OAuthState{StateHash: "h1", Provider: "google", CodeVerifier: "v", Nonce: "n", BindHash: "b", NextPath: "/", Remember: true}))
	if _, err := s.TakeOAuthState(ctx, "apple", "h1"); !errors.Is(err, ErrOAuthState) {
		t.Errorf("state for another provider: %v", err)
	}
	if _, err := s.TakeOAuthState(ctx, "google", "h1"); !errors.Is(err, ErrOAuthState) {
		t.Errorf("state survived a wrong-provider take: %v", err)
	}

	// --- notification timing ---------------------------------------------
	var perf, rid, upcoming int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES('acct-perf','演员') RETURNING id`).Scan(&perf))
	_, err = s.Exec(ctx, `UPDATE releases SET notified_at=now() WHERE notified_at IS NULL`)
	must(err)
	_, err = s.PatchSettings(ctx, uid, []byte(`{"notify_prefs":{"new_work":{"email":true,"push":false},"reservation":{"email":true,"push":false}},"quiet_hours":null,"notify_daily_cap":2,"reservation_timing":"day_before"}`))
	must(err)
	if _, err := s.PatchSettings(ctx, uid, []byte(`{"notify_daily_cap":4}`)); !errors.Is(err, ErrInvalidSettings) {
		t.Errorf("cap 4 accepted: %v", err)
	}
	_, err = s.Exec(ctx, `INSERT INTO follows(user_id,performer_id) VALUES($1,$2)`, uid, perf)
	must(err)
	publish := func(slug string) {
		t.Helper()
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$1,'published',current_date) RETURNING id`, slug).Scan(&rid))
		_, err := s.Exec(ctx, `INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,0,true)`, rid, perf)
		must(err)
		_, err = s.NotifyFanout(ctx, 10)
		must(err)
	}
	emailRows := func() int {
		var n int
		must(s.QueryRow(ctx, `SELECT count(*) FROM notification_outbox WHERE user_id=$1 AND channel='email' AND payload->>'kind'='new_work' AND status<>'cancelled'`, uid).Scan(&n))
		return n
	}
	publish("acct-a")
	publish("acct-b") // merged into the pending slot-0 row
	if n := emailRows(); n != 1 {
		t.Fatalf("email rows after two releases = %d, want 1", n)
	}
	_, err = s.Exec(ctx, `UPDATE notification_outbox SET status='sent' WHERE user_id=$1 AND channel='email'`, uid)
	must(err)
	publish("acct-c") // slot 1 opens
	publish("acct-d") // merged into slot 1
	_, err = s.Exec(ctx, `UPDATE notification_outbox SET status='sent' WHERE user_id=$1 AND channel='email'`, uid)
	must(err)
	publish("acct-e") // cap 2 reached: dropped
	if n := emailRows(); n != 2 {
		t.Errorf("email rows with cap 2 = %d, want 2", n)
	}

	// Day-before reminder, then the go-live notice is skipped.
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,scheduled_at) VALUES('acct-up','预约作品','upcoming',now()+interval '20 hours') RETURNING id`).Scan(&upcoming))
	must(s.Reserve(ctx, uid, "acct-up"))
	if n, err := s.RemindReservations(ctx, time.Now(), 10); err != nil || n != 1 {
		t.Fatalf("reminders = %d %v", n, err)
	}
	if n, _ := s.RemindReservations(ctx, time.Now(), 10); n != 0 {
		t.Errorf("reminder sent twice")
	}
	var reminderMail int
	must(s.QueryRow(ctx, `SELECT count(*) FROM notification_outbox WHERE user_id=$1 AND payload->>'kind'='reservation_reminder'`, uid).Scan(&reminderMail))
	if reminderMail != 1 {
		t.Errorf("reminder outbox rows = %d", reminderMail)
	}
	_, err = s.Exec(ctx, `UPDATE releases SET scheduled_at=now()-interval '1 minute' WHERE id=$1`, upcoming)
	must(err)
	_, err = s.NotifyFanout(ctx, 10)
	must(err)
	var liveNotices int
	must(s.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND kind='new_work' AND payload::text LIKE '%acct-up%'`, uid).Scan(&liveNotices))
	if liveNotices != 0 {
		t.Errorf("day_before still got the go-live notice")
	}

	// Pending deletion: no new notices.
	_, err = s.Exec(ctx, `UPDATE users SET deletion_requested_at=now() WHERE id=$1`, uid)
	must(err)
	var before, after2 int
	must(s.QueryRow(ctx, `SELECT count(*) FROM notification_receipts WHERE user_id=$1`, uid).Scan(&before))
	publish("acct-f")
	must(s.QueryRow(ctx, `SELECT count(*) FROM notification_receipts WHERE user_id=$1`, uid).Scan(&after2))
	if after2 != before {
		t.Error("user pending deletion was notified")
	}
	if n, err := s.PruneExpiredAuth(ctx); err != nil || n < 0 {
		t.Errorf("prune = %d %v", n, err)
	}
}
