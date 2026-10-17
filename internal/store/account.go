package store

// Account self-service (PRD 7, 15, 20; tech plan 3.6, 3.13; migration 037):
// sessions and devices, profile and password, password reset, terms
// acceptance, account deletion with a grace period, data export and
// onboarding interests. Every function that takes a userID only touches that
// user's rows (callers pass the id resolved from the session token).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrInvalidResetToken: unknown, used or expired password reset token.
	ErrInvalidResetToken = errors.New("invalid or expired reset token")
	// ErrRateLimited: the per-account limit for this action was reached.
	ErrRateLimited = errors.New("too many requests")
	// ErrWrongPassword: the current password did not match (re-authentication).
	ErrWrongPassword = errors.New("current password is incorrect")
)

// Limits and lifetimes.
const (
	PasswordResetTTL       = time.Hour
	PasswordResetPerHour   = 3 // reset mails per account per hour
	DataExportsPerDay      = 3
	AccountDeletionGrace   = 30 * 24 * time.Hour
	MaxDisplayNameRunes    = 40
	MaxInterests           = 50
	accountEventReset      = "password_reset_requested"
	accountEventExport     = "data_export"
	accountEventPassword   = "password_changed"
	accountEventDeletion   = "deletion_requested"
	accountEventUndelete   = "deletion_cancelled"
	accountEventPasswordRS = "password_reset"
)

// OutboxKindPasswordReset is the notification_outbox payload kind of a reset
// mail. The payload carries no token: the notifier mints one when it sends.
const OutboxKindPasswordReset = "password_reset"

// CleanDisplayName trims a nickname and rejects control characters and names
// longer than MaxDisplayNameRunes.
func CleanDisplayName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxDisplayNameRunes {
		return "", invalid("nickname must be at most %d characters", MaxDisplayNameRunes)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", invalid("nickname contains control characters")
		}
	}
	return name, nil
}

var termsVersionRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)

// ValidTermsVersion reports whether v looks like a terms version string.
func ValidTermsVersion(v string) bool { return termsVersionRe.MatchString(v) }

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ---------------------------------------------------------------------------
// Sessions and devices

// SessionInfo is one row of Settings › 已登录设备.
type SessionInfo struct {
	ID         int64     `json:"id"`
	Device     string    `json:"device"`
	UserAgent  string    `json:"user_agent"`
	Method     string    `json:"method"`
	Remember   bool      `json:"remember"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// Logout deletes the session behind token. Unknown tokens are not an error.
func (s *Store) Logout(ctx context.Context, token string) error {
	_, err := s.Exec(ctx, `DELETE FROM user_sessions WHERE token_hash=$1`, hashToken(token))
	return err
}

// Sessions lists the user's live sessions, current one first, then by last activity.
func (s *Store) Sessions(ctx context.Context, userID int64, currentToken string) ([]SessionInfo, error) {
	cur := hashToken(currentToken)
	out, err := collectRows(ctx, s, `SELECT id,user_agent,auth_method,remember,created_at,last_seen_at,expires_at,token_hash=$2
		FROM user_sessions WHERE user_id=$1 AND expires_at>now()
		ORDER BY token_hash=$2 DESC, last_seen_at DESC, id DESC LIMIT 100`, []any{userID, cur},
		func(r pgx.Rows) (SessionInfo, error) {
			var x SessionInfo
			err := r.Scan(&x.ID, &x.UserAgent, &x.Method, &x.Remember, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt, &x.Current)
			x.Device = DeviceLabel(x.UserAgent)
			return x, err
		})
	if out == nil {
		out = []SessionInfo{}
	}
	return out, err
}

// RevokeSession deletes one of the user's sessions by id. false = no such
// session for this user.
func (s *Store) RevokeSession(ctx context.Context, userID, sessionID int64) (bool, error) {
	tag, err := s.Exec(ctx, `DELETE FROM user_sessions WHERE id=$1 AND user_id=$2`, sessionID, userID)
	return tag.RowsAffected() == 1, err
}

// RevokeOtherSessions deletes every session of the user except currentToken's.
func (s *Store) RevokeOtherSessions(ctx context.Context, userID int64, currentToken string) (int64, error) {
	tag, err := s.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1 AND token_hash<>$2`, userID, hashToken(currentToken))
	return tag.RowsAffected(), err
}

// DeviceLabel is a short "browser · OS" description of a User-Agent.
func DeviceLabel(ua string) string {
	if ua == "" {
		return ""
	}
	l := strings.ToLower(ua)
	browser := ""
	switch {
	case strings.Contains(l, "edg/"):
		browser = "Edge"
	case strings.Contains(l, "opr/") || strings.Contains(l, "opera"):
		browser = "Opera"
	case strings.Contains(l, "samsungbrowser"):
		browser = "Samsung Internet"
	case strings.Contains(l, "firefox/") || strings.Contains(l, "fxios"):
		browser = "Firefox"
	case strings.Contains(l, "crios") || strings.Contains(l, "chrome/"):
		browser = "Chrome"
	case strings.Contains(l, "safari/"):
		browser = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(l, "iphone"):
		os = "iPhone"
	case strings.Contains(l, "ipad"):
		os = "iPad"
	case strings.Contains(l, "android"):
		os = "Android"
	case strings.Contains(l, "windows"):
		os = "Windows"
	case strings.Contains(l, "mac os x") || strings.Contains(l, "macintosh"):
		os = "macOS"
	case strings.Contains(l, "cros"):
		os = "ChromeOS"
	case strings.Contains(l, "linux"):
		os = "Linux"
	}
	switch {
	case browser != "" && os != "":
		return browser + " · " + os
	case browser != "":
		return browser
	case os != "":
		return os
	}
	if r := []rune(ua); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return ua
}

// ---------------------------------------------------------------------------
// Profile and password

// UpdateProfile changes the nickname.
func (s *Store) UpdateProfile(ctx context.Context, userID int64, displayName string) (string, error) {
	name, err := CleanDisplayName(displayName)
	if err != nil {
		return "", err
	}
	_, err = s.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, userID, name)
	return name, err
}

// ChangePassword sets a new password after checking the current one (not
// required for an OAuth-only account that has none yet). Every other session
// and every unused reset token is revoked.
func (s *Store) ChangePassword(ctx context.Context, userID int64, current, next, currentToken string) error {
	if len(next) < 8 {
		return invalid("password must be at least 8 characters")
	}
	if len(next) > 256 {
		return invalid("password is too long")
	}
	var stored string
	if err := s.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, userID).Scan(&stored); err != nil {
		return err
	}
	if stored != "" {
		if ok, _ := verifyPassword(stored, current); !ok {
			return ErrWrongPassword
		}
	}
	hash, err := hashPassword(next)
	if err != nil {
		return err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now() WHERE id=$1`, userID, hash); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1 AND token_hash<>$2`, userID, hashToken(currentToken)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`, userID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_events(user_id,kind) VALUES($1,$2)`, userID, accountEventPassword); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Password reset

// RequestPasswordReset queues a reset mail for email if such an account
// exists and is under PasswordResetPerHour. It never reports whether the
// account exists: unknown emails and rate-limited accounts both return nil.
func (s *Store) RequestPasswordReset(ctx context.Context, email string) error {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var uid int64
	// FOR UPDATE serializes concurrent requests for one account so the hourly
	// count below cannot be raced past.
	err = tx.QueryRow(ctx, `SELECT id FROM users WHERE email=$1 FOR UPDATE`, email).Scan(&uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var recent int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM account_events WHERE user_id=$1 AND kind=$2 AND created_at > now() - interval '1 hour'`,
		uid, accountEventReset).Scan(&recent); err != nil {
		return err
	}
	if recent >= PasswordResetPerHour {
		return nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_events(user_id,kind) VALUES($1,$2)`, uid, accountEventReset); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notification_outbox(user_id,channel,payload) VALUES($1,'email',jsonb_build_object('kind',$2::text))`,
		uid, OutboxKindPasswordReset); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// IssuePasswordReset mints a single-use reset token for the user (called by
// the notifier right before it sends the mail) and stores only its hash.
func (s *Store) IssuePasswordReset(ctx context.Context, userID int64) (string, time.Time, error) {
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	exp := time.Now().Add(PasswordResetTTL)
	_, err = s.Exec(ctx, `INSERT INTO password_resets(user_id,token_hash,expires_at) VALUES($1,$2,$3)`, userID, hashToken(token), exp)
	return token, exp, err
}

// ResetPassword consumes a reset token and sets a new password. Every
// session and every other outstanding token of the account is revoked, and a
// pending deletion is left as it is (logging in cancels it).
func (s *Store) ResetPassword(ctx context.Context, token, password string) error {
	if len(password) < 8 {
		return invalid("password must be at least 8 characters")
	}
	if len(password) > 256 {
		return invalid("password is too long")
	}
	if len(token) != 64 {
		return ErrInvalidResetToken
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, uid int64
	err = tx.QueryRow(ctx, `SELECT id,user_id FROM password_resets WHERE token_hash=$1 AND used_at IS NULL AND expires_at>now() FOR UPDATE`,
		hashToken(token)).Scan(&id, &uid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidResetToken
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2, password_changed_at=now() WHERE id=$1`, uid, hash); err != nil {
		return err
	}
	for _, q := range []string{
		`UPDATE password_resets SET used_at=now() WHERE user_id=$1 AND used_at IS NULL`,
		`DELETE FROM user_sessions WHERE user_id=$1`,
		`INSERT INTO account_events(user_id,kind) VALUES($1,'` + accountEventPasswordRS + `')`,
	} {
		if _, err = tx.Exec(ctx, q, uid); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Terms

// AcceptTerms records that the user accepted a terms version. Accepting the
// same version again only refreshes nothing (first acceptance is kept).
func (s *Store) AcceptTerms(ctx context.Context, userID int64, version, source string) error {
	if !ValidTermsVersion(version) {
		return invalid("invalid terms version")
	}
	switch source {
	case "register", "oauth", "prompt":
	default:
		source = "prompt"
	}
	_, err := s.Exec(ctx, `INSERT INTO terms_acceptances(user_id,version,source) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, userID, version, source)
	return err
}

// ---------------------------------------------------------------------------
// Account deletion

// RequestAccountDeletion marks the account for deletion after re-checking the
// password (when one is set). Every session is revoked and pending push /
// email deliveries are cancelled; logging in again within AccountDeletionGrace
// cancels the deletion, after which PurgeDeletedAccounts removes the data.
func (s *Store) RequestAccountDeletion(ctx context.Context, userID int64, password string) (time.Time, error) {
	var stored string
	var requested *time.Time
	if err := s.QueryRow(ctx, `SELECT password_hash,deletion_requested_at FROM users WHERE id=$1`, userID).Scan(&stored, &requested); err != nil {
		return time.Time{}, err
	}
	if stored != "" {
		if ok, _ := verifyPassword(stored, password); !ok {
			return time.Time{}, ErrWrongPassword
		}
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback(ctx)
	var at time.Time
	if err = tx.QueryRow(ctx, `UPDATE users SET deletion_requested_at=COALESCE(deletion_requested_at, now()) WHERE id=$1 RETURNING deletion_requested_at`,
		userID).Scan(&at); err != nil {
		return time.Time{}, err
	}
	for _, q := range []string{
		`DELETE FROM user_sessions WHERE user_id=$1`,
		`UPDATE notification_outbox SET status='cancelled', last_error='account deletion requested' WHERE user_id=$1 AND status='pending'`,
		`INSERT INTO account_events(user_id,kind) VALUES($1,'` + accountEventDeletion + `')`,
	} {
		if _, err = tx.Exec(ctx, q, userID); err != nil {
			return time.Time{}, err
		}
	}
	return at.Add(AccountDeletionGrace), tx.Commit(ctx)
}

// cancelDeletion clears a pending deletion mark; true when one was cleared.
func (s *Store) cancelDeletion(ctx context.Context, userID int64) (bool, error) {
	tag, err := s.Exec(ctx, `UPDATE users SET deletion_requested_at=NULL WHERE id=$1 AND deletion_requested_at IS NOT NULL`, userID)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	_, err = s.Exec(ctx, `INSERT INTO account_events(user_id,kind) VALUES($1,$2)`, userID, accountEventUndelete)
	return true, err
}

// PurgeDeletedAccounts hard-deletes up to limit accounts whose deletion was
// requested more than grace ago. User rows cascade to every user-owned table;
// analytics events (no foreign key) are detached from the user. Each account
// is purged in its own transaction and re-checked, so a login that cancels the
// deletion concurrently wins.
func (s *Store) PurgeDeletedAccounts(ctx context.Context, grace time.Duration, limit int) (int, error) {
	if limit <= 0 {
		limit = 100
	}
	cutoff := time.Now().Add(-grace)
	ids, err := collectRows(ctx, s, `SELECT id FROM users WHERE deletion_requested_at IS NOT NULL AND deletion_requested_at < $1 ORDER BY deletion_requested_at LIMIT $2`,
		[]any{cutoff, limit}, func(r pgx.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ok, err := s.purgeAccount(ctx, id, cutoff)
		if err != nil {
			return n, fmt.Errorf("purge account: %w", err)
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (s *Store) purgeAccount(ctx context.Context, id int64, cutoff time.Time) (bool, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var still bool
	err = tx.QueryRow(ctx, `SELECT true FROM users WHERE id=$1 AND deletion_requested_at IS NOT NULL AND deletion_requested_at < $2 FOR UPDATE`, id, cutoff).Scan(&still)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `UPDATE events SET user_id=NULL WHERE user_id=$1`, id); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// PruneExpiredAuth deletes expired sessions, OAuth requests and login codes,
// and reset tokens that expired more than a day ago (kept briefly for support).
func (s *Store) PruneExpiredAuth(ctx context.Context) (int64, error) {
	var total int64
	for _, q := range []string{
		`DELETE FROM user_sessions WHERE expires_at < now()`,
		`DELETE FROM oauth_states WHERE expires_at < now()`,
		`DELETE FROM oauth_login_codes WHERE expires_at < now()`,
		`DELETE FROM password_resets WHERE expires_at < now() - interval '1 day'`,
	} {
		tag, err := s.Exec(ctx, q)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// Data export

// ReserveDataExport counts one export against the DataExportsPerDay limit;
// ErrRateLimited when the account already used it up.
func (s *Store) ReserveDataExport(ctx context.Context, userID int64) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
		return err
	}
	var n int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM account_events WHERE user_id=$1 AND kind=$2 AND created_at > now() - interval '24 hours'`,
		userID, accountEventExport).Scan(&n); err != nil {
		return err
	}
	if n >= DataExportsPerDay {
		return ErrRateLimited
	}
	if _, err = tx.Exec(ctx, `INSERT INTO account_events(user_id,kind) VALUES($1,$2)`, userID, accountEventExport); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// exportSections are the parts of a data export, in output order. Each query
// takes the user id as $1 and returns one json value. Secrets (password hash,
// session and reset token hashes, push endpoints and keys) are never included.
var exportSections = []struct{ name, sql string }{
	{"account", `SELECT json_build_object('id',id,'email',email,'display_name',display_name,'created_at',created_at,
		'password_set',password_hash<>'','password_changed_at',password_changed_at,'onboarded_at',onboarded_at,
		'deletion_requested_at',deletion_requested_at,'adult_confirmed_at',adult_confirmed_at,'adult_birth_ym',adult_birth_ym) FROM users WHERE id=$1`},
	{"settings", `SELECT COALESCE((SELECT to_jsonb(s) - 'user_id' FROM user_settings s WHERE user_id=$1),'null'::jsonb)`},
	{"terms_acceptances", `SELECT COALESCE(json_agg(json_build_object('version',version,'source',source,'accepted_at',accepted_at) ORDER BY accepted_at),'[]') FROM terms_acceptances WHERE user_id=$1`},
	{"sign_in_providers", `SELECT COALESCE(json_agg(json_build_object('provider',provider,'email',email,'linked_at',created_at,'last_login_at',last_login_at) ORDER BY provider),'[]') FROM user_identities WHERE user_id=$1`},
	{"sessions", `SELECT COALESCE(json_agg(json_build_object('id',id,'user_agent',user_agent,'method',auth_method,'remember',remember,'created_at',created_at,'last_seen_at',last_seen_at,'expires_at',expires_at) ORDER BY id),'[]') FROM user_sessions WHERE user_id=$1`},
	{"account_events", `SELECT COALESCE(json_agg(json_build_object('kind',kind,'at',created_at) ORDER BY id),'[]') FROM account_events WHERE user_id=$1`},
	{"interests", `SELECT COALESCE(json_agg(tag_slug ORDER BY tag_slug),'[]') FROM user_interests WHERE user_id=$1`},
	{"watch_history", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'title',r.canonical_title,'progress_seconds',w.progress_seconds,'duration_seconds',w.duration_seconds,'watched_at',w.watched_at) ORDER BY w.watched_at DESC),'[]')
		FROM watch_history w JOIN releases r ON r.id=w.release_id WHERE w.user_id=$1`},
	{"favorites", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'title',r.canonical_title,'created_at',f.created_at) ORDER BY f.created_at DESC),'[]')
		FROM favorites f JOIN releases r ON r.id=f.release_id WHERE f.user_id=$1`},
	// Playlists (migration 036); share_token is a capability URL and is left out.
	{"playlists", `SELECT COALESCE(json_agg(json_build_object('name',p.name,'description',p.description,'visibility',p.visibility,'is_default',p.is_default,
		'created_at',p.created_at,'items',(SELECT COALESCE(json_agg(json_build_object('release',r.slug,'title',r.canonical_title,'added_at',i.created_at) ORDER BY i.created_at),'[]')
		  FROM playlist_items i JOIN releases r ON r.id=i.release_id WHERE i.playlist_id=p.id)) ORDER BY p.position, p.id),'[]')
		FROM playlists p WHERE p.user_id=$1`},
	{"watch_later", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'title',r.canonical_title,'position',w.position,'created_at',w.created_at) ORDER BY w.position),'[]')
		FROM watch_later w JOIN releases r ON r.id=w.release_id WHERE w.user_id=$1`},
	{"follows", `SELECT COALESCE(json_agg(json_build_object('performer',p.slug,'name',p.canonical_name,'notify_mode',f.notify_mode,'created_at',f.created_at) ORDER BY f.created_at),'[]')
		FROM follows f JOIN performers p ON p.id=f.performer_id WHERE f.user_id=$1`},
	{"reservations", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'title',r.canonical_title,'created_at',x.created_at) ORDER BY x.created_at),'[]')
		FROM reservations x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	{"notifications", `SELECT COALESCE(json_agg(json_build_object('kind',kind,'payload',payload,'read_at',read_at,'created_at',created_at) ORDER BY id DESC),'[]') FROM notifications WHERE user_id=$1`},
	{"ratings", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'score',x.score,'status',x.status,'created_at',x.created_at,'updated_at',x.updated_at) ORDER BY x.created_at),'[]')
		FROM ratings x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	{"reviews", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'body',x.body,'spoiler',x.spoiler,'status',x.status,'likes',x.likes,'created_at',x.created_at,'updated_at',x.updated_at) ORDER BY x.created_at),'[]')
		FROM reviews x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	{"review_likes", `SELECT COALESCE(json_agg(json_build_object('review_id',review_id,'created_at',created_at) ORDER BY created_at),'[]') FROM review_likes WHERE user_id=$1`},
	{"danmaku", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'t_ms',x.t_ms,'body',x.body,'color',x.color,'mode',x.mode,'status',x.status,'created_at',x.created_at) ORDER BY x.created_at),'[]')
		FROM danmaku x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	// 评论 and 赞 / 踩 (migration 040), recent searches and cookie choices.
	{"comments", `SELECT COALESCE(json_agg(json_build_object('id',x.id,'release',r.slug,'parent_id',x.parent_id,'body',x.body,'spoiler',x.spoiler,'status',x.status,'likes',x.likes,'created_at',x.created_at,'updated_at',x.updated_at) ORDER BY x.created_at),'[]')
		FROM comments x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	{"comment_likes", `SELECT COALESCE(json_agg(json_build_object('comment_id',comment_id,'created_at',created_at) ORDER BY created_at),'[]') FROM comment_likes WHERE user_id=$1`},
	{"reactions", `SELECT COALESCE(json_agg(json_build_object('release',r.slug,'value',x.value,'created_at',x.created_at,'updated_at',x.updated_at) ORDER BY x.created_at),'[]')
		FROM release_reactions x JOIN releases r ON r.id=x.release_id WHERE x.user_id=$1`},
	// 推荐反馈 (migration 051), undone rows included with undone_at.
	{"rec_feedback", `SELECT COALESCE(json_agg(json_build_object('kind',f.kind,'target',COALESCE(r.slug,p.slug,f.target_tag),'name',COALESCE(r.canonical_title,p.canonical_name,f.target_tag),
		'created_at',f.created_at,'undone_at',f.undone_at) ORDER BY f.created_at),'[]')
		FROM rec_feedback f
		LEFT JOIN releases r ON f.kind='not_interested_release' AND r.id=f.target_id
		LEFT JOIN performers p ON f.kind='not_interested_performer' AND p.id=f.target_id
		WHERE f.user_id=$1`},
	// 片段 (migration 054): the shared link and segment of each clip.
	{"clips", `SELECT COALESCE(json_agg(json_build_object('id',c.public_id,'release',r.slug,'start_ms',c.start_ms,'end_ms',c.end_ms,'title',c.title,'title_status',c.title_status,'status',c.status,'created_at',c.created_at) ORDER BY c.created_at),'[]')
		FROM clips c JOIN releases r ON r.id=c.release_id WHERE c.user_id=$1`},
	{"search_history", `SELECT COALESCE(json_agg(json_build_object('query',COALESCE(display,query),'searched_at',searched_at) ORDER BY searched_at DESC),'[]') FROM user_search_history WHERE user_id=$1`},
	{"consent_records", `SELECT COALESCE(json_agg(json_build_object('categories',categories,'policy_version',policy_version,'gpc',gpc,'source',source,'created_at',created_at) ORDER BY id),'[]') FROM consent_records WHERE user_id=$1`},
	{"push_devices", `SELECT COALESCE(json_agg(json_build_object('user_agent',user_agent,'created_at',created_at,'last_success_at',last_success_at) ORDER BY id),'[]') FROM push_subscriptions WHERE user_id=$1`},
	// 观看统计 daily aggregate (migration 043).
	{"user_watch_daily", `SELECT COALESCE(json_agg(json_build_object('day',day,'tz',tz,'watch_seconds',watch_seconds,'plays',plays,'titles',titles,'finished',finished,
		'by_hour',by_hour,'top_performers',top_performers,'top_tags',top_tags,'built_at',built_at) ORDER BY day),'[]') FROM user_watch_daily WHERE user_id=$1`},
}

// WriteDataExport streams the user's data as one JSON object to w, section
// by section, inside a single read-only snapshot.
func (s *Store) WriteDataExport(ctx context.Context, userID int64, w io.Writer) error {
	tx, err := s.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	head, _ := json.Marshal(map[string]any{"format": "reel-account-export", "version": 1, "generated_at": time.Now().UTC()})
	if _, err = io.WriteString(w, `{"export":`+string(head)); err != nil {
		return err
	}
	for _, sec := range exportSections {
		var raw []byte
		if err = tx.QueryRow(ctx, sec.sql, userID).Scan(&raw); err != nil {
			return fmt.Errorf("export %s: %w", sec.name, err)
		}
		if len(raw) == 0 {
			raw = []byte("null")
		}
		if _, err = fmt.Fprintf(w, ",\n%q:%s", sec.name, raw); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "}\n")
	return err
}

// ---------------------------------------------------------------------------
// Onboarding interests

// Tag slugs as GET /api/tags returns them: no whitespace, quotes, angle
// brackets, slashes or control characters; at most 100 characters.
var tagSlugRe = regexp.MustCompile(`^[^\s\p{Cc}/\\<>"'` + "`" + `]{1,100}$`)

// Interests returns the user's onboarding interest tags.
func (s *Store) Interests(ctx context.Context, userID int64) ([]string, error) {
	out, err := collectRows(ctx, s, `SELECT tag_slug FROM user_interests WHERE user_id=$1 ORDER BY created_at, tag_slug`, []any{userID},
		func(r pgx.Rows) (string, error) {
			var v string
			return v, r.Scan(&v)
		})
	if out == nil {
		out = []string{}
	}
	return out, err
}

// SetInterests replaces the user's interest tags and, when onboarded is set,
// records that onboarding was finished.
func (s *Store) SetInterests(ctx context.Context, userID int64, tags []string, onboarded bool) ([]string, error) {
	if len(tags) > MaxInterests {
		return nil, invalid("at most %d interests", MaxInterests)
	}
	seen := map[string]bool{}
	clean := []string{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if !tagSlugRe.MatchString(t) {
			return nil, invalid("invalid tag %q", t)
		}
		if !seen[t] {
			seen[t] = true
			clean = append(clean, t)
		}
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM user_interests WHERE user_id=$1 AND NOT (tag_slug = ANY($2))`, userID, clean); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_interests(user_id,tag_slug) SELECT $1, unnest($2::text[]) ON CONFLICT DO NOTHING`, userID, clean); err != nil {
		return nil, err
	}
	if onboarded {
		if _, err = tx.Exec(ctx, `UPDATE users SET onboarded_at=COALESCE(onboarded_at, now()) WHERE id=$1`, userID); err != nil {
			return nil, err
		}
	}
	return clean, tx.Commit(ctx)
}
