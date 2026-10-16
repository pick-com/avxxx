package store

// Back-office accounts (artboard AdminUsers, tech plan 3.6, migration 042):
// admin_users with argon2id passwords and mandatory TOTP, one-time recovery
// codes, lockout with backoff, short admin sessions, the role assignments,
// invites (enrollment links), the admin op log and dual-approval requests.
// The role → route-group matrix lives in internal/adminauth; the API
// enforces it (internal/api/admin_perms.go).

import (
	"avxxx/internal/adminauth"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminPrincipal is a signed-in admin as the API sees it.
type AdminPrincipal struct {
	ID          int64    `json:"id"`
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
	SessionID   int64    `json:"-"`
}

// AdminUser is one row of the admin accounts list.
type AdminUser struct {
	ID                int64      `json:"id"`
	Username          string     `json:"username"`
	Email             string     `json:"email"`
	DisplayName       string     `json:"display_name"`
	Status            string     `json:"status"`
	Roles             []string   `json:"roles"`
	TOTPEnrolled      bool       `json:"totp_enrolled"`
	RecoveryCodesLeft int        `json:"recovery_codes_left"`
	FailedAttempts    int        `json:"failed_attempts"`
	LockedUntil       *time.Time `json:"locked_until"`
	LastLoginAt       *time.Time `json:"last_login_at"`
	InviteExpiresAt   *time.Time `json:"invite_expires_at"`
	ActiveSessions    int        `json:"active_sessions"`
	CreatedBy         string     `json:"created_by"`
	CreatedAt         time.Time  `json:"created_at"`
}

// AdminSession is a freshly opened admin session; Token is shown once.
type AdminSession struct {
	Token  string `json:"-"`
	Method string `json:"method"` // totp | recovery_code
	// RecoveryCodesLeft is set after a recovery-code sign-in.
	RecoveryCodesLeft *int           `json:"recovery_codes_left,omitempty"`
	ExpiresAt         time.Time      `json:"expires_at"`
	Admin             AdminPrincipal `json:"admin"`
}

// Admin sign-in errors. ErrAdminCredentials covers unknown login, wrong
// password, wrong code and non-active accounts alike.
var (
	ErrAdminCredentials = errors.New("invalid credentials")
	ErrAdminChallenge   = errors.New("sign-in expired; start again")
	ErrAdminInvite      = errors.New("invite link is invalid or expired")
)

// AdminLockedError is returned while an account is locked after repeated failures.
type AdminLockedError struct{ Until time.Time }

func (e *AdminLockedError) Error() string { return "too many failed attempts; try again later" }

// MinAdminPasswordLen is the minimum admin password length.
const MinAdminPasswordLen = 12

var adminUsernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,31}$`)

var (
	adminSealerOnce sync.Once
	adminSealer     adminauth.Sealer
)

// adminSeal is the TOTP sealer from ADMIN_TOTP_KEY (read once per process).
func adminSeal() adminauth.Sealer {
	adminSealerOnce.Do(func() { adminSealer = adminauth.NewSealer(os.Getenv(adminauth.KeyEnv)) })
	return adminSealer
}

// AdminUserInput creates an admin (invite or bootstrap).
type AdminUserInput struct {
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
}

func (in *AdminUserInput) normalize() error {
	in.Username = strings.ToLower(strings.TrimSpace(in.Username))
	in.Email = strings.TrimSpace(in.Email)
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	if !adminUsernameRE.MatchString(in.Username) {
		return invalid("username must be 2-32 characters: a-z, 0-9, . _ -")
	}
	if len(in.Email) < 3 || len(in.Email) > 254 || !strings.Contains(in.Email, "@") {
		return invalid("a valid email is required")
	}
	if len([]rune(in.DisplayName)) > 64 {
		return invalid("display_name is at most 64 characters")
	}
	roles, ok := adminauth.NormalizeRoles(in.Roles)
	if !ok || len(roles) == 0 {
		return invalid("roles must be one or more of the 7 admin roles")
	}
	in.Roles = roles
	return nil
}

func checkAdminPassword(pw string) error {
	if len([]rune(pw)) < MinAdminPasswordLen || len(pw) > 1024 {
		return invalid("password must be at least %d characters", MinAdminPasswordLen)
	}
	return nil
}

func insertAdminRoles(ctx context.Context, q dbtx, id int64, roles []string, by string) error {
	for _, r := range roles {
		if _, err := q.Exec(ctx, `INSERT INTO admin_user_roles(admin_id,role,granted_by) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, r, by); err != nil {
			return err
		}
	}
	return nil
}

func replaceRecoveryCodes(ctx context.Context, q dbtx, id int64) ([]string, error) {
	codes, err := adminauth.NewRecoveryCodes(adminauth.RecoveryCodeCount)
	if err != nil {
		return nil, err
	}
	if _, err := q.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id=$1`, id); err != nil {
		return nil, err
	}
	for _, c := range codes {
		if _, err := q.Exec(ctx, `INSERT INTO admin_recovery_codes(admin_id,code_hash) VALUES($1,$2)`, id, adminauth.HashToken(c)); err != nil {
			return nil, err
		}
	}
	return codes, nil
}

// BootstrapAdmin creates an active admin with a password and a fresh TOTP
// secret (cmd/admin). It returns the base32 secret for the enrollment URI and
// the recovery codes; the first sign-in with a code marks TOTP enrolled.
func (s *Store) BootstrapAdmin(ctx context.Context, in AdminUserInput, password string) (AdminUser, string, []string, error) {
	if err := in.normalize(); err != nil {
		return AdminUser{}, "", nil, err
	}
	if err := checkAdminPassword(password); err != nil {
		return AdminUser{}, "", nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return AdminUser{}, "", nil, err
	}
	secret, err := adminauth.NewSecret()
	if err != nil {
		return AdminUser{}, "", nil, err
	}
	sealed, err := adminSeal().Seal(secret)
	if err != nil {
		return AdminUser{}, "", nil, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminUser{}, "", nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO admin_users(username,email,display_name,password_hash,totp_secret,status,created_by)
		VALUES($1,$2,$3,$4,$5,'active','cli') RETURNING id`, in.Username, in.Email, in.DisplayName, hash, sealed).Scan(&id); err != nil {
		return AdminUser{}, "", nil, fmt.Errorf("create admin: %w", err)
	}
	if err := insertAdminRoles(ctx, tx, id, in.Roles, "cli"); err != nil {
		return AdminUser{}, "", nil, err
	}
	codes, err := replaceRecoveryCodes(ctx, tx, id)
	if err != nil {
		return AdminUser{}, "", nil, err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.bootstrap", nil, map[string]any{"username": in.Username, "roles": in.Roles}, "", "cli"); err != nil {
		return AdminUser{}, "", nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminUser{}, "", nil, err
	}
	u, err := s.AdminUserByID(ctx, id)
	return u, secret, codes, err
}

// CountAdmins is the number of admin accounts (cmd/admin warns when adding
// a non-super first admin).
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.QueryRow(ctx, `SELECT count(*) FROM admin_users`).Scan(&n)
	return n, err
}

// InviteAdmin creates an invited admin and returns the one-time enrollment
// token (shown once; only its hash is stored). The invitee sets a password
// and enrolls TOTP through it.
func (s *Store) InviteAdmin(ctx context.Context, in AdminUserInput, actor string) (AdminUser, string, error) {
	if err := in.normalize(); err != nil {
		return AdminUser{}, "", err
	}
	token, err := adminauth.NewToken("admi_")
	if err != nil {
		return AdminUser{}, "", err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminUser{}, "", err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO admin_users(username,email,display_name,status,invite_token_hash,invite_expires_at,created_by)
		VALUES($1,$2,$3,'invited',$4,now()+$5::int*interval '1 second',$6) RETURNING id`,
		in.Username, in.Email, in.DisplayName, adminauth.HashToken(token), int(adminauth.InviteTTL.Seconds()), actor).Scan(&id)
	if err != nil {
		return AdminUser{}, "", err
	}
	if err := insertAdminRoles(ctx, tx, id, in.Roles, actor); err != nil {
		return AdminUser{}, "", err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.invite", nil, map[string]any{"username": in.Username, "email": in.Email, "roles": in.Roles}, "", actor); err != nil {
		return AdminUser{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminUser{}, "", err
	}
	u, err := s.AdminUserByID(ctx, id)
	return u, token, err
}

// ReissueAdminInvite replaces the enrollment link of an invited admin.
func (s *Store) ReissueAdminInvite(ctx context.Context, id int64, actor string) (string, error) {
	token, err := adminauth.NewToken("admi_")
	if err != nil {
		return "", err
	}
	tag, err := s.Exec(ctx, `UPDATE admin_users SET invite_token_hash=$2, invite_expires_at=now()+$3::int*interval '1 second', updated_at=now()
		WHERE id=$1 AND status='invited'`, id, adminauth.HashToken(token), int(adminauth.InviteTTL.Seconds()))
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.AdminUserByID(ctx, id); err != nil {
			return "", err
		}
		return "", conflict("admin %d is not waiting for enrollment", id)
	}
	_, err = insertEvent(ctx, s, "admin_user", id, "admin.invite_reissue", nil, nil, "", actor)
	return token, err
}

// AdminInvite is what the enrollment page shows: who is enrolling and the
// TOTP secret to scan (generated on first view, sealed at rest).
type AdminInvite struct {
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	OTPAuthURI string    `json:"otpauth_uri"`
	Secret     string    `json:"secret"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// AdminInviteInfo resolves an enrollment token.
func (s *Store) AdminInviteInfo(ctx context.Context, token string) (AdminInvite, error) {
	var id int64
	var inv AdminInvite
	var stored string
	err := s.QueryRow(ctx, `SELECT id,username,email,totp_secret,invite_expires_at FROM admin_users
		WHERE invite_token_hash=$1 AND invite_expires_at>now() AND status='invited'`, adminauth.HashToken(token)).
		Scan(&id, &inv.Username, &inv.Email, &stored, &inv.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return inv, ErrAdminInvite
	}
	if err != nil {
		return inv, err
	}
	secret := ""
	if stored != "" {
		if secret, _, err = adminSeal().Open(stored); err != nil {
			return inv, err
		}
	} else {
		if secret, err = adminauth.NewSecret(); err != nil {
			return inv, err
		}
		sealed, err := adminSeal().Seal(secret)
		if err != nil {
			return inv, err
		}
		// Two concurrent first views: keep whichever secret was stored first.
		if err := s.QueryRow(ctx, `UPDATE admin_users SET totp_secret=CASE WHEN totp_secret='' THEN $2 ELSE totp_secret END
			WHERE id=$1 RETURNING totp_secret`, id, sealed).Scan(&stored); err != nil {
			return inv, err
		}
		if secret, _, err = adminSeal().Open(stored); err != nil {
			return inv, err
		}
	}
	inv.Secret = secret
	inv.OTPAuthURI = adminauth.URI(inv.Email, secret)
	return inv, nil
}

// CompleteAdminEnrollment sets the password, checks a TOTP code against the
// secret shown by AdminInviteInfo, activates the account and returns fresh
// recovery codes (shown once).
func (s *Store) CompleteAdminEnrollment(ctx context.Context, token, password, code string) (string, []string, error) {
	if err := checkAdminPassword(password); err != nil {
		return "", nil, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var username, stored string
	err = tx.QueryRow(ctx, `SELECT id,username,totp_secret FROM admin_users
		WHERE invite_token_hash=$1 AND invite_expires_at>now() AND status='invited' FOR UPDATE`, adminauth.HashToken(token)).Scan(&id, &username, &stored)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && stored == "") {
		return "", nil, ErrAdminInvite
	}
	if err != nil {
		return "", nil, err
	}
	secret, _, err := adminSeal().Open(stored)
	if err != nil {
		return "", nil, err
	}
	step, ok := adminauth.Verify(secret, code, time.Now())
	if !ok {
		return "", nil, invalid("the code does not match; check the authenticator app's clock")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return "", nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET password_hash=$2, status='active', totp_enrolled_at=now(), totp_last_step=$3,
		invite_token_hash=NULL, invite_expires_at=NULL, failed_attempts=0, locked_until=NULL, updated_at=now() WHERE id=$1`,
		id, hash, int64(step)); err != nil {
		return "", nil, err
	}
	codes, err := replaceRecoveryCodes(ctx, tx, id)
	if err != nil {
		return "", nil, err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.enroll", nil, nil, "", username); err != nil {
		return "", nil, err
	}
	return username, codes, tx.Commit(ctx)
}

// adminFailure counts a failed password or second factor and locks the
// account per adminauth.LockoutDuration.
func adminFailure(ctx context.Context, q dbtx, id int64) error {
	var n int
	if err := q.QueryRow(ctx, `UPDATE admin_users SET failed_attempts=failed_attempts+1 WHERE id=$1 RETURNING failed_attempts`, id).Scan(&n); err != nil {
		return err
	}
	if d := adminauth.LockoutDuration(n); d > 0 {
		_, err := q.Exec(ctx, `UPDATE admin_users SET locked_until=now()+$2::int*interval '1 second' WHERE id=$1`, id, int(d.Seconds()))
		return err
	}
	return nil
}

// AdminPasswordLogin is sign-in step 1. On a correct password it returns a
// short-lived challenge token for step 2 (TOTP or recovery code). Unknown
// logins cost the same argon2id work as wrong passwords.
func (s *Store) AdminPasswordLogin(ctx context.Context, login, password string) (string, int64, error) {
	login = strings.ToLower(strings.TrimSpace(login))
	var id int64
	var stored, status string
	var locked *time.Time
	err := s.QueryRow(ctx, `SELECT id,password_hash,status,locked_until FROM admin_users WHERE username=$1 OR lower(email)=$1
		ORDER BY (username=$1) DESC LIMIT 1`, login).Scan(&id, &stored, &status, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		verifyPassword(dummyHash, password)
		return "", 0, ErrAdminCredentials
	}
	if err != nil {
		return "", 0, err
	}
	if locked != nil && locked.After(time.Now()) {
		return "", id, &AdminLockedError{Until: *locked}
	}
	if status != "active" || stored == "" {
		verifyPassword(dummyHash, password)
		return "", id, ErrAdminCredentials
	}
	ok, rehash := verifyPassword(stored, password)
	if !ok {
		if err := adminFailure(ctx, s, id); err != nil {
			return "", id, err
		}
		return "", id, ErrAdminCredentials
	}
	if rehash {
		if h, err := hashPassword(password); err == nil {
			_, _ = s.Exec(ctx, `UPDATE admin_users SET password_hash=$1 WHERE id=$2 AND password_hash=$3`, h, id, stored)
		}
	}
	token, err := adminauth.NewToken("admc_")
	if err != nil {
		return "", id, err
	}
	_, _ = s.Exec(ctx, `DELETE FROM admin_login_challenges WHERE expires_at < now()`)
	_, err = s.Exec(ctx, `INSERT INTO admin_login_challenges(token_hash,admin_id,expires_at) VALUES($1,$2,now()+$3::int*interval '1 second')`,
		adminauth.HashToken(token), id, int(adminauth.ChallengeTTL.Seconds()))
	return token, id, err
}

// maxChallengeAttempts bounds second-factor guesses per password step.
const maxChallengeAttempts = 5

// AdminSessionMeta is recorded on a new admin session.
type AdminSessionMeta struct {
	IPHash    string
	UserAgent string
}

// AdminSecondFactor is sign-in step 2: a TOTP code (never the same step
// twice) or an unused recovery code. Success clears the failure counter and
// opens a session (30 min idle, 12 h absolute).
func (s *Store) AdminSecondFactor(ctx context.Context, challenge, code, recovery string, meta AdminSessionMeta) (AdminSession, int64, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminSession{}, 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var attempts int
	var status, stored string
	var lastStep int64
	var locked *time.Time
	err = tx.QueryRow(ctx, `SELECT c.admin_id,c.attempts,u.status,u.totp_secret,u.totp_last_step,u.locked_until
		FROM admin_login_challenges c JOIN admin_users u ON u.id=c.admin_id
		WHERE c.token_hash=$1 AND c.expires_at>now() FOR UPDATE OF c, u`, adminauth.HashToken(challenge)).
		Scan(&id, &attempts, &status, &stored, &lastStep, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminSession{}, 0, ErrAdminChallenge
	}
	if err != nil {
		return AdminSession{}, 0, err
	}
	if attempts >= maxChallengeAttempts {
		return AdminSession{}, id, ErrAdminChallenge
	}
	if locked != nil && locked.After(time.Now()) {
		return AdminSession{}, id, &AdminLockedError{Until: *locked}
	}
	if status != "active" {
		return AdminSession{}, id, ErrAdminCredentials
	}
	ok := false
	method := "totp"
	reseal := ""
	if strings.TrimSpace(recovery) != "" {
		method = "recovery_code"
		tag, err := tx.Exec(ctx, `UPDATE admin_recovery_codes SET used_at=now() WHERE admin_id=$1 AND code_hash=$2 AND used_at IS NULL`,
			id, adminauth.HashToken(adminauth.NormalizeRecoveryCode(recovery)))
		if err != nil {
			return AdminSession{}, id, err
		}
		ok = tag.RowsAffected() == 1
	} else {
		secret, plain, err := adminSeal().Open(stored)
		if err != nil {
			return AdminSession{}, id, err
		}
		step, match := adminauth.Verify(secret, code, time.Now())
		if match && int64(step) > lastStep {
			ok = true
			if _, err := tx.Exec(ctx, `UPDATE admin_users SET totp_last_step=$2, totp_enrolled_at=COALESCE(totp_enrolled_at,now()) WHERE id=$1`, id, int64(step)); err != nil {
				return AdminSession{}, id, err
			}
			if plain && adminSeal().HasKey() {
				reseal, _ = adminSeal().Seal(secret)
			}
		}
	}
	if !ok {
		if _, err := tx.Exec(ctx, `UPDATE admin_login_challenges SET attempts=attempts+1 WHERE token_hash=$1`, adminauth.HashToken(challenge)); err != nil {
			return AdminSession{}, id, err
		}
		if err := adminFailure(ctx, tx, id); err != nil {
			return AdminSession{}, id, err
		}
		if err := tx.Commit(ctx); err != nil {
			return AdminSession{}, id, err
		}
		return AdminSession{}, id, ErrAdminCredentials
	}
	if reseal != "" {
		if _, err := tx.Exec(ctx, `UPDATE admin_users SET totp_secret=$2 WHERE id=$1`, id, reseal); err != nil {
			return AdminSession{}, id, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_login_challenges WHERE token_hash=$1`, adminauth.HashToken(challenge)); err != nil {
		return AdminSession{}, id, err
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET failed_attempts=0, locked_until=NULL, last_login_at=now() WHERE id=$1`, id); err != nil {
		return AdminSession{}, id, err
	}
	token, err := adminauth.NewToken("adms_")
	if err != nil {
		return AdminSession{}, id, err
	}
	var sess AdminSession
	sess.Token = token
	if err := tx.QueryRow(ctx, `INSERT INTO admin_sessions(admin_id,token_hash,expires_at,ip_hash,user_agent)
		VALUES($1,$2,now()+$3::int*interval '1 second',$4,$5) RETURNING id,expires_at`,
		id, adminauth.HashToken(token), int(adminauth.SessionMax.Seconds()), meta.IPHash, truncate(meta.UserAgent, 400)).
		Scan(&sess.Admin.SessionID, &sess.ExpiresAt); err != nil {
		return AdminSession{}, id, err
	}
	sess.Method = method
	if err := tx.Commit(ctx); err != nil {
		return AdminSession{}, id, err
	}
	p, err := s.adminPrincipal(ctx, id)
	p.SessionID = sess.Admin.SessionID
	sess.Admin = p
	if err == nil && method == "recovery_code" {
		var left int
		_ = s.QueryRow(ctx, `SELECT count(*) FROM admin_recovery_codes WHERE admin_id=$1 AND used_at IS NULL`, id).Scan(&left)
		sess.RecoveryCodesLeft = &left
	}
	return sess, id, err
}

func (s *Store) adminPrincipal(ctx context.Context, id int64) (AdminPrincipal, error) {
	var p AdminPrincipal
	err := s.QueryRow(ctx, `SELECT u.id,u.username,u.email,u.display_name,
		COALESCE((SELECT array_agg(role ORDER BY role) FROM admin_user_roles r WHERE r.admin_id=u.id),'{}')
		FROM admin_users u WHERE u.id=$1`, id).Scan(&p.ID, &p.Username, &p.Email, &p.DisplayName, &p.Roles)
	return p, err
}

// ActiveAdminPrincipal loads an active admin by id (dual-approval replay
// runs as the requester, who must still be active).
func (s *Store) ActiveAdminPrincipal(ctx context.Context, id int64) (AdminPrincipal, error) {
	var status string
	if err := s.QueryRow(ctx, `SELECT status FROM admin_users WHERE id=$1`, id).Scan(&status); err != nil {
		return AdminPrincipal{}, err
	}
	if status != "active" {
		return AdminPrincipal{}, conflict("admin %d is %s", id, status)
	}
	return s.adminPrincipal(ctx, id)
}

// AdminBySession resolves a session token, refreshing last_seen_at. Expired
// (idle or absolute), revoked or non-active sessions are pgx.ErrNoRows.
func (s *Store) AdminBySession(ctx context.Context, token string) (AdminPrincipal, error) {
	var p AdminPrincipal
	err := s.QueryRow(ctx, `UPDATE admin_sessions ss SET last_seen_at=now() FROM admin_users u
		WHERE ss.token_hash=$1 AND ss.revoked_at IS NULL AND ss.expires_at>now()
		  AND ss.last_seen_at > now()-$2::int*interval '1 second' AND u.id=ss.admin_id AND u.status='active'
		RETURNING ss.id,u.id,u.username,u.email,u.display_name,
		  COALESCE((SELECT array_agg(role ORDER BY role) FROM admin_user_roles r WHERE r.admin_id=u.id),'{}')`,
		adminauth.HashToken(token), int(adminauth.SessionIdle.Seconds())).
		Scan(&p.SessionID, &p.ID, &p.Username, &p.Email, &p.DisplayName, &p.Roles)
	return p, err
}

// RevokeAdminSession ends one session (sign out).
func (s *Store) RevokeAdminSession(ctx context.Context, token string) error {
	_, err := s.Exec(ctx, `UPDATE admin_sessions SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, adminauth.HashToken(token))
	return err
}

func revokeAdminSessions(ctx context.Context, q dbtx, id int64) error {
	_, err := q.Exec(ctx, `UPDATE admin_sessions SET revoked_at=now() WHERE admin_id=$1 AND revoked_at IS NULL`, id)
	return err
}

const adminUserCols = `u.id,u.username,u.email,u.display_name,u.status,
	COALESCE((SELECT array_agg(r.role ORDER BY ar.sort) FROM admin_user_roles r JOIN admin_roles ar ON ar.role=r.role WHERE r.admin_id=u.id),'{}'),
	u.totp_enrolled_at IS NOT NULL,
	(SELECT count(*) FROM admin_recovery_codes c WHERE c.admin_id=u.id AND c.used_at IS NULL),
	u.failed_attempts,u.locked_until,u.last_login_at,u.invite_expires_at,
	(SELECT count(*) FROM admin_sessions ss WHERE ss.admin_id=u.id AND ss.revoked_at IS NULL AND ss.expires_at>now()
	   AND ss.last_seen_at > now()-interval '` + "1800" + ` seconds'),
	u.created_by,u.created_at`

func scanAdminUser(row pgx.Row) (AdminUser, error) {
	var u AdminUser
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Status, &u.Roles, &u.TOTPEnrolled, &u.RecoveryCodesLeft,
		&u.FailedAttempts, &u.LockedUntil, &u.LastLoginAt, &u.InviteExpiresAt, &u.ActiveSessions, &u.CreatedBy, &u.CreatedAt)
	return u, err
}

// AdminUserByID returns one admin account.
func (s *Store) AdminUserByID(ctx context.Context, id int64) (AdminUser, error) {
	return scanAdminUser(s.QueryRow(ctx, `SELECT `+adminUserCols+` FROM admin_users u WHERE u.id=$1`, id))
}

// AdminUsers lists every admin account (there are few), active first.
func (s *Store) AdminUsers(ctx context.Context) ([]AdminUser, error) {
	rows, err := s.Query(ctx, `SELECT `+adminUserCols+` FROM admin_users u
		ORDER BY CASE u.status WHEN 'active' THEN 0 WHEN 'invited' THEN 1 ELSE 2 END, u.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminUser{}
	for rows.Next() {
		u, err := scanAdminUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// otherActiveSupers counts active super admins other than id.
func otherActiveSupers(ctx context.Context, q dbtx, id int64) (int, error) {
	var n int
	err := q.QueryRow(ctx, `SELECT count(*) FROM admin_users u JOIN admin_user_roles r ON r.admin_id=u.id AND r.role='super_admin'
		WHERE u.status='active' AND u.id<>$1`, id).Scan(&n)
	return n, err
}

func lockAdmin(ctx context.Context, q dbtx, id int64) (string, []string, error) {
	var status string
	var roles []string
	err := q.QueryRow(ctx, `SELECT status, COALESCE((SELECT array_agg(role ORDER BY role) FROM admin_user_roles r WHERE r.admin_id=u.id),'{}')
		FROM admin_users u WHERE id=$1 FOR UPDATE`, id).Scan(&status, &roles)
	return status, roles, err
}

// SetAdminRoles replaces an admin's roles. Admins cannot change their own
// roles, and the last active super admin keeps super_admin. Runs only
// through dual approval (internal/api/admin_perms.go).
func (s *Store) SetAdminRoles(ctx context.Context, id int64, roles []string, actor string, actorID int64) (AdminUser, error) {
	roles, ok := adminauth.NormalizeRoles(roles)
	if !ok || len(roles) == 0 {
		return AdminUser{}, invalid("roles must be one or more of the 7 admin roles")
	}
	if id == actorID {
		return AdminUser{}, conflict("admins cannot change their own roles")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminUser{}, err
	}
	defer tx.Rollback(ctx)
	status, before, err := lockAdmin(ctx, tx, id)
	if err != nil {
		return AdminUser{}, err
	}
	if adminauth.IsSuper(before) && !adminauth.IsSuper(roles) && status == "active" {
		n, err := otherActiveSupers(ctx, tx, id)
		if err != nil {
			return AdminUser{}, err
		}
		if n == 0 {
			return AdminUser{}, conflict("the last active super admin keeps super_admin")
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_user_roles WHERE admin_id=$1 AND NOT (role = ANY($2))`, id, roles); err != nil {
		return AdminUser{}, err
	}
	if err := insertAdminRoles(ctx, tx, id, roles, actor); err != nil {
		return AdminUser{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET updated_at=now() WHERE id=$1`, id); err != nil {
		return AdminUser{}, err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.roles", map[string]any{"roles": before}, map[string]any{"roles": roles}, "", actor); err != nil {
		return AdminUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminUser{}, err
	}
	return s.AdminUserByID(ctx, id)
}

// SetAdminDisabled disables (immediately, revoking sessions) or re-enables
// an admin. Admins cannot disable themselves or the last active super admin.
// Re-enabling runs through dual approval; an account that never enrolled
// goes back to invited.
func (s *Store) SetAdminDisabled(ctx context.Context, id int64, disabled bool, reason, actor string, actorID int64) (AdminUser, error) {
	if id == actorID {
		return AdminUser{}, conflict("admins cannot disable or enable themselves")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminUser{}, err
	}
	defer tx.Rollback(ctx)
	status, roles, err := lockAdmin(ctx, tx, id)
	if err != nil {
		return AdminUser{}, err
	}
	action := "admin.enable"
	if disabled {
		action = "admin.disable"
		if status == "disabled" {
			return AdminUser{}, conflict("admin %d is already disabled", id)
		}
		if status == "active" && adminauth.IsSuper(roles) {
			n, err := otherActiveSupers(ctx, tx, id)
			if err != nil {
				return AdminUser{}, err
			}
			if n == 0 {
				return AdminUser{}, conflict("cannot disable the last active super admin")
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE admin_users SET status='disabled', invite_token_hash=NULL, invite_expires_at=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
			return AdminUser{}, err
		}
		if err := revokeAdminSessions(ctx, tx, id); err != nil {
			return AdminUser{}, err
		}
	} else {
		if status != "disabled" {
			return AdminUser{}, conflict("admin %d is not disabled", id)
		}
		if _, err := tx.Exec(ctx, `UPDATE admin_users SET status=CASE WHEN password_hash<>'' AND totp_secret<>'' THEN 'active' ELSE 'invited' END,
			failed_attempts=0, locked_until=NULL, updated_at=now() WHERE id=$1`, id); err != nil {
			return AdminUser{}, err
		}
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, action, map[string]any{"status": status}, nil, reason, actor); err != nil {
		return AdminUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AdminUser{}, err
	}
	return s.AdminUserByID(ctx, id)
}

// ResetAdminTOTP removes an admin's password, TOTP secret and recovery
// codes, revokes their sessions and returns a new enrollment token (the
// admin re-enrolls through the invite page). Runs only through dual approval.
func (s *Store) ResetAdminTOTP(ctx context.Context, id int64, reason, actor string, actorID int64) (string, error) {
	if id == actorID {
		return "", conflict("ask another admin to reset your two-step verification")
	}
	token, err := adminauth.NewToken("admi_")
	if err != nil {
		return "", err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	status, _, err := lockAdmin(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if status == "disabled" {
		return "", conflict("enable admin %d first", id)
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET status='invited', password_hash='', totp_secret='', totp_enrolled_at=NULL, totp_last_step=0,
		failed_attempts=0, locked_until=NULL, invite_token_hash=$2, invite_expires_at=now()+$3::int*interval '1 second', updated_at=now() WHERE id=$1`,
		id, adminauth.HashToken(token), int(adminauth.InviteTTL.Seconds())); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id=$1`, id); err != nil {
		return "", err
	}
	if err := revokeAdminSessions(ctx, tx, id); err != nil {
		return "", err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.reset_2fa", nil, nil, reason, actor); err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}

// RegenerateRecoveryCodes replaces the signed-in admin's recovery codes;
// a current TOTP code is required.
func (s *Store) RegenerateRecoveryCodes(ctx context.Context, id int64, code string) ([]string, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var stored, username string
	var last int64
	if err := tx.QueryRow(ctx, `SELECT username,totp_secret,totp_last_step FROM admin_users WHERE id=$1 AND status='active' FOR UPDATE`, id).Scan(&username, &stored, &last); err != nil {
		return nil, err
	}
	secret, _, err := adminSeal().Open(stored)
	if err != nil {
		return nil, err
	}
	step, ok := adminauth.Verify(secret, code, time.Now())
	if !ok || int64(step) <= last {
		return nil, invalid("the code does not match")
	}
	if _, err := tx.Exec(ctx, `UPDATE admin_users SET totp_last_step=$2 WHERE id=$1`, id, int64(step)); err != nil {
		return nil, err
	}
	codes, err := replaceRecoveryCodes(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if _, err := insertEvent(ctx, tx, "admin_user", id, "admin.recovery_codes", nil, nil, "", username); err != nil {
		return nil, err
	}
	return codes, tx.Commit(ctx)
}

// ---- Admin op log ----

// AdminAuditEntry is one admin_audit_log row.
type AdminAuditEntry struct {
	ID         int64          `json:"id"`
	CreatedAt  time.Time      `json:"created_at"`
	AdminID    *int64         `json:"admin_id"`
	Actor      string         `json:"actor"`
	Auth       string         `json:"auth"`
	Action     string         `json:"action"`
	Method     string         `json:"method"`
	Route      string         `json:"route"`
	Path       string         `json:"path"`
	EntityType string         `json:"entity_type"`
	EntityID   string         `json:"entity_id"`
	Status     int            `json:"status"`
	IPHash     string         `json:"ip_hash"`
	ApprovalID *int64         `json:"approval_id"`
	Detail     map[string]any `json:"detail"`
}

// RecordAdminAudit appends to the op log.
func (s *Store) RecordAdminAudit(ctx context.Context, e AdminAuditEntry) error {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	_, err := s.Exec(ctx, `INSERT INTO admin_audit_log(admin_id,actor,auth,action,method,route,path,entity_type,entity_id,status,ip_hash,approval_id,detail)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		e.AdminID, truncate(e.Actor, 100), e.Auth, truncate(e.Action, 100), e.Method, truncate(e.Route, 200), truncate(e.Path, 500),
		truncate(e.EntityType, 64), truncate(e.EntityID, 128), e.Status, e.IPHash, e.ApprovalID, e.Detail)
	return err
}

// AdminAuditFilter narrows the op log. Q matches actor, action, route, path
// and entity id (substring); the other fields match exactly.
type AdminAuditFilter struct {
	Q          string
	Actor      string
	Auth       string
	EntityType string
	EntityID   string
	From, To   *time.Time
	Cursor     string
	Limit      int
}

// AdminAuditLog lists the op log newest first with a keyset cursor (the last id).
func (s *Store) AdminAuditLog(ctx context.Context, f AdminAuditFilter) ([]AdminAuditEntry, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"true"}
	args := []any{}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "$?", "$"+strconv.Itoa(len(args))))
	}
	if q := strings.TrimSpace(f.Q); q != "" {
		add(`(actor ILIKE $? OR action ILIKE $? OR route ILIKE $? OR path ILIKE $? OR entity_id ILIKE $?)`, "%"+likeEscape(q)+"%")
	}
	if f.Actor != "" {
		add(`actor=$?`, f.Actor)
	}
	if f.Auth != "" {
		add(`auth=$?`, f.Auth)
	}
	if f.EntityType != "" {
		add(`entity_type=$?`, f.EntityType)
	}
	if f.EntityID != "" {
		add(`entity_id=$?`, f.EntityID)
	}
	if f.From != nil {
		add(`created_at>=$?`, *f.From)
	}
	if f.To != nil {
		add(`created_at<$?`, *f.To)
	}
	if f.Cursor != "" {
		id, err := strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil || id <= 0 {
			return nil, "", invalid("invalid cursor")
		}
		add(`id<$?`, id)
	}
	args = append(args, limit+1)
	rows, err := s.Query(ctx, `SELECT id,created_at,admin_id,actor,auth,action,method,route,path,entity_type,entity_id,status,ip_hash,approval_id,detail
		FROM admin_audit_log WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []AdminAuditEntry{}
	for rows.Next() {
		var e AdminAuditEntry
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.AdminID, &e.Actor, &e.Auth, &e.Action, &e.Method, &e.Route, &e.Path,
			&e.EntityType, &e.EntityID, &e.Status, &e.IPHash, &e.ApprovalID, &e.Detail); err != nil {
			return nil, "", err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = strconv.FormatInt(out[limit-1].ID, 10)
	}
	return out, next, nil
}

// ---- Dual approval ----

// AdminApproval is a destructive request waiting for (or past) a second admin.
type AdminApproval struct {
	ID              int64      `json:"id"`
	Route           string     `json:"route"`
	Method          string     `json:"method"`
	Path            string     `json:"path"`
	Query           string     `json:"query"`
	Body            []byte     `json:"-"`
	Summary         string     `json:"summary"`
	RequestedBy     int64      `json:"requested_by"`
	RequestedByName string     `json:"requested_by_name"`
	RequestedAt     time.Time  `json:"requested_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	Status          string     `json:"status"`
	DecidedBy       *string    `json:"decided_by"`
	DecidedAt       *time.Time `json:"decided_at"`
	DecisionNote    string     `json:"decision_note"`
	ResultStatus    *int       `json:"result_status"`
}

const approvalCols = `id,route,method,path,query,body,summary,requested_by,requested_by_name,requested_at,expires_at,status,decided_by,decided_at,decision_note,result_status`

func scanApproval(row pgx.Row) (AdminApproval, error) {
	var a AdminApproval
	err := row.Scan(&a.ID, &a.Route, &a.Method, &a.Path, &a.Query, &a.Body, &a.Summary, &a.RequestedBy, &a.RequestedByName,
		&a.RequestedAt, &a.ExpiresAt, &a.Status, &a.DecidedBy, &a.DecidedAt, &a.DecisionNote, &a.ResultStatus)
	return a, err
}

// CreateAdminApproval stores a request for a second admin.
func (s *Store) CreateAdminApproval(ctx context.Context, a AdminApproval) (AdminApproval, error) {
	if a.Body == nil {
		a.Body = []byte{}
	}
	return scanApproval(s.QueryRow(ctx, `INSERT INTO admin_approvals(route,method,path,query,body,summary,requested_by,requested_by_name,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,now()+$9::int*interval '1 second') RETURNING `+approvalCols,
		a.Route, a.Method, a.Path, a.Query, a.Body, truncate(a.Summary, 300), a.RequestedBy, a.RequestedByName, int(adminauth.ApprovalTTL.Seconds())))
}

// AdminApprovals lists requests by status ("" = pending), newest first.
// Pending requests past their expiry are marked expired first.
func (s *Store) AdminApprovals(ctx context.Context, status string, limit int) ([]AdminApproval, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if _, err := s.Exec(ctx, `UPDATE admin_approvals SET status='expired' WHERE status='pending' AND expires_at<=now()`); err != nil {
		return nil, err
	}
	if status == "" {
		status = "pending"
	}
	q := `SELECT ` + approvalCols + ` FROM admin_approvals WHERE status=$1 ORDER BY id DESC LIMIT $2`
	args := []any{status, limit}
	if status == "all" {
		q = `SELECT ` + approvalCols + ` FROM admin_approvals ORDER BY id DESC LIMIT $1`
		args = []any{limit}
	}
	rows, err := s.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminApproval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAdminApproval returns one request.
func (s *Store) GetAdminApproval(ctx context.Context, id int64) (AdminApproval, error) {
	return scanApproval(s.QueryRow(ctx, `SELECT `+approvalCols+` FROM admin_approvals WHERE id=$1`, id))
}

// ClaimAdminApproval atomically moves a pending request to executing for
// approver (approverID 0 = break-glass token). The requester can never
// approve their own request.
func (s *Store) ClaimAdminApproval(ctx context.Context, id, approverID int64, approver string) (AdminApproval, error) {
	a, err := scanApproval(s.QueryRow(ctx, `UPDATE admin_approvals SET status='executing', decided_by=$3, decided_by_id=NULLIF($2::bigint,0), decided_at=now()
		WHERE id=$1 AND status='pending' AND expires_at>now() AND requested_by<>$2 RETURNING `+approvalCols, id, approverID, approver))
	if !errors.Is(err, pgx.ErrNoRows) {
		return a, err
	}
	cur, err := s.GetAdminApproval(ctx, id)
	if err != nil {
		return cur, err
	}
	switch {
	case cur.RequestedBy == approverID:
		return cur, conflict("a request must be approved by a different admin")
	case cur.Status == "pending":
		_, _ = s.Exec(ctx, `UPDATE admin_approvals SET status='expired' WHERE id=$1 AND status='pending'`, id)
		return cur, conflict("approval request %d has expired", id)
	}
	return cur, conflict("approval request %d is already %s", id, cur.Status)
}

// FinishAdminApproval records the outcome of an executed request.
func (s *Store) FinishAdminApproval(ctx context.Context, id int64, ok bool, resultStatus int) error {
	status := "executed"
	if !ok {
		status = "failed"
	}
	_, err := s.Exec(ctx, `UPDATE admin_approvals SET status=$2, result_status=$3 WHERE id=$1 AND status='executing'`, id, status, resultStatus)
	return err
}

// DecideAdminApproval rejects a pending request, or cancels it when the
// decider is the requester.
func (s *Store) DecideAdminApproval(ctx context.Context, id, deciderID int64, decider, note string) (AdminApproval, error) {
	a, err := scanApproval(s.QueryRow(ctx, `UPDATE admin_approvals SET status=CASE WHEN requested_by=$2 THEN 'cancelled' ELSE 'rejected' END,
		decided_by=$3, decided_by_id=NULLIF($2::bigint,0), decided_at=now(), decision_note=$4
		WHERE id=$1 AND status='pending' RETURNING `+approvalCols, id, deciderID, decider, truncate(note, MaxReasonLen)))
	if errors.Is(err, pgx.ErrNoRows) {
		cur, err := s.GetAdminApproval(ctx, id)
		if err != nil {
			return cur, err
		}
		return cur, conflict("approval request %d is already %s", id, cur.Status)
	}
	return a, err
}

// ---- Site users (AdminUsers 用户检索与处罚) ----

// SiteUser is a front-end account as the back office sees it.
type SiteUser struct {
	ID                  int64      `json:"id"`
	Email               string     `json:"email"`
	DisplayName         string     `json:"display_name"`
	CreatedAt           time.Time  `json:"created_at"`
	LastSeenAt          *time.Time `json:"last_seen_at"`
	MutedUntil          *time.Time `json:"muted_until"`
	Banned              bool       `json:"banned"`
	BannedUntil         *time.Time `json:"banned_until"`
	BanReason           string     `json:"ban_reason"`
	DeletionRequestedAt *time.Time `json:"deletion_requested_at"`
	Reports             int        `json:"reports_against"`
}

const siteUserCols = `u.id,u.email,u.display_name,u.created_at,
	(SELECT max(last_seen_at) FROM user_sessions ss WHERE ss.user_id=u.id),
	(SELECT until FROM user_mutes m WHERE m.user_id=u.id AND m.until>now()),
	b.user_id IS NOT NULL, b.until, COALESCE(b.reason,''), u.deletion_requested_at,
	((SELECT count(*) FROM reports r JOIN danmaku d ON d.id=r.target_id WHERE r.target_type='danmaku' AND d.user_id=u.id)
	 + (SELECT count(*) FROM reports r JOIN reviews v ON v.id=r.target_id WHERE r.target_type='review' AND v.user_id=u.id))::int`

func scanSiteUser(row pgx.Row) (SiteUser, error) {
	var u SiteUser
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &u.LastSeenAt, &u.MutedUntil, &u.Banned, &u.BannedUntil, &u.BanReason, &u.DeletionRequestedAt, &u.Reports)
	return u, err
}

const siteUserFrom = ` FROM users u LEFT JOIN user_bans b ON b.user_id=u.id AND (b.until IS NULL OR b.until>now())`

// SearchSiteUsers finds site users by id, email prefix or display name
// substring; q "" lists the newest accounts. status: "" | banned | muted.
func (s *Store) SearchSiteUsers(ctx context.Context, q, status string, limit int) ([]SiteUser, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q = strings.TrimSpace(q)
	where := []string{"true"}
	args := []any{}
	if q != "" {
		if id, err := strconv.ParseInt(strings.TrimPrefix(q, "#"), 10, 64); err == nil && id > 0 {
			args = append(args, id)
			where = append(where, "u.id=$1")
		} else {
			args = append(args, strings.ToLower(likeEscape(q))+"%", "%"+likeEscape(q)+"%")
			where = append(where, "(lower(u.email) LIKE $1 OR u.display_name ILIKE $2)")
		}
	}
	switch status {
	case "":
	case "banned":
		where = append(where, "b.user_id IS NOT NULL")
	case "muted":
		where = append(where, "EXISTS (SELECT 1 FROM user_mutes m WHERE m.user_id=u.id AND m.until>now())")
	default:
		return nil, invalid("status must be banned or muted")
	}
	args = append(args, limit)
	rows, err := s.Query(ctx, `SELECT `+siteUserCols+siteUserFrom+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY u.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SiteUser{}
	for rows.Next() {
		u, err := scanSiteUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SiteUserByID returns one site user.
func (s *Store) SiteUserByID(ctx context.Context, id int64) (SiteUser, error) {
	return scanSiteUser(s.QueryRow(ctx, `SELECT `+siteUserCols+siteUserFrom+` WHERE u.id=$1`, id))
}

// ErrAccountBanned blocks sign-in for a banned site user.
var ErrAccountBanned = errors.New("account suspended")

func userBanned(ctx context.Context, q dbtx, id int64) (bool, error) {
	var b bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_bans WHERE user_id=$1 AND (until IS NULL OR until>now()))`, id).Scan(&b)
	return b, err
}

// BanUser bans a site user until `until` (nil = permanent), revokes their
// sessions and writes review_events (moderation.ban).
func (s *Store) BanUser(ctx context.Context, id int64, until *time.Time, reason, actor string) (SiteUser, error) {
	if until != nil && !until.After(time.Now()) {
		return SiteUser{}, invalid("until must be in the future")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > MaxReasonLen {
		return SiteUser{}, invalid("a reason is required (at most %d bytes)", MaxReasonLen)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return SiteUser{}, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&exists); err != nil {
		return SiteUser{}, err
	}
	var before map[string]any
	var prevUntil *time.Time
	if err := tx.QueryRow(ctx, `SELECT until FROM user_bans WHERE user_id=$1 AND (until IS NULL OR until>now())`, id).Scan(&prevUntil); err == nil {
		before = map[string]any{"banned_until": prevUntil}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return SiteUser{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_bans(user_id,until,reason,created_by) VALUES($1,$2,$3,$4)
		ON CONFLICT (user_id) DO UPDATE SET until=EXCLUDED.until, reason=EXCLUDED.reason, created_by=EXCLUDED.created_by, created_at=now()`,
		id, until, reason, actor); err != nil {
		return SiteUser{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, id); err != nil {
		return SiteUser{}, err
	}
	if _, err := insertEvent(ctx, tx, "user", id, "moderation.ban", before, map[string]any{"banned_until": until}, reason, actor); err != nil {
		return SiteUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SiteUser{}, err
	}
	return s.SiteUserByID(ctx, id)
}

// UnbanUser lifts a ban (review_events moderation.unban).
func (s *Store) UnbanUser(ctx context.Context, id int64, reason, actor string) (SiteUser, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return SiteUser{}, err
	}
	defer tx.Rollback(ctx)
	var until *time.Time
	if err := tx.QueryRow(ctx, `DELETE FROM user_bans WHERE user_id=$1 RETURNING until`, id).Scan(&until); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if _, e := s.SiteUserByID(ctx, id); e != nil {
				return SiteUser{}, e
			}
			return SiteUser{}, conflict("user %d is not banned", id)
		}
		return SiteUser{}, err
	}
	if _, err := insertEvent(ctx, tx, "user", id, "moderation.unban", map[string]any{"banned_until": until}, nil, strings.TrimSpace(reason), actor); err != nil {
		return SiteUser{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return SiteUser{}, err
	}
	return s.SiteUserByID(ctx, id)
}
