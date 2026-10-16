package store

// Google / Apple sign-in state (internal/oidc runs the protocol; migration
// 037): in-flight authorization requests, provider identities and the
// one-time codes that hand a finished sign-in to the web app.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrOAuthState: unknown, expired or already used authorization request.
	ErrOAuthState = errors.New("invalid or expired sign-in request")
	// ErrIdentityEmailTaken: a password account already uses the provider's
	// email. It is never linked automatically (pre-registered accounts could
	// otherwise be hijacked); the user signs in and links from Settings.
	ErrIdentityEmailTaken = errors.New("an account with this email already exists")
	// ErrIdentityTaken: the provider account is linked to another user.
	ErrIdentityTaken = errors.New("this sign-in is linked to another account")
	// ErrLastSignInMethod: unlinking would leave the account without a way to sign in.
	ErrLastSignInMethod = errors.New("cannot remove the only sign-in method")
	// ErrNoEmail: the provider did not return a verified email for a new account.
	ErrNoEmail = errors.New("the provider did not share a verified email")
)

const (
	OAuthStateTTL = 10 * time.Minute
	OAuthCodeTTL  = 2 * time.Minute
)

// OAuthState is one oauth_states row. StateHash is sha256(state); the state
// itself only travels through the browser.
type OAuthState struct {
	StateHash    string
	Provider     string
	CodeVerifier string
	Nonce        string
	BindHash     string
	NextPath     string
	Remember     bool
	LinkUserID   int64 // 0 = sign in
}

// HashOpaque is the stored form of an opaque one-time value (state, bind
// cookie, login code): hex sha256, like session tokens.
func HashOpaque(v string) string { return hashToken(v) }

// SaveOAuthState stores an authorization request and drops expired ones.
func (s *Store) SaveOAuthState(ctx context.Context, st OAuthState) error {
	if _, err := s.Exec(ctx, `DELETE FROM oauth_states WHERE expires_at < now()`); err != nil {
		return err
	}
	var link any
	if st.LinkUserID > 0 {
		link = st.LinkUserID
	}
	_, err := s.Exec(ctx, `INSERT INTO oauth_states(state_hash,provider,code_verifier,nonce,bind_hash,next_path,remember,link_user_id,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, st.StateHash, st.Provider, st.CodeVerifier, st.Nonce, st.BindHash, st.NextPath, st.Remember, link,
		time.Now().Add(OAuthStateTTL))
	return err
}

// TakeOAuthState deletes and returns an unexpired authorization request for
// provider (single use).
func (s *Store) TakeOAuthState(ctx context.Context, provider, stateHash string) (OAuthState, error) {
	var st OAuthState
	var link *int64
	var live bool
	// Deleted whatever the outcome: a state presented once is spent, even
	// on the wrong provider's callback.
	err := s.QueryRow(ctx, `DELETE FROM oauth_states WHERE state_hash=$1 RETURNING state_hash,provider,code_verifier,nonce,bind_hash,next_path,remember,link_user_id,expires_at>now()`,
		stateHash).Scan(&st.StateHash, &st.Provider, &st.CodeVerifier, &st.Nonce, &st.BindHash, &st.NextPath, &st.Remember, &link, &live)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (!live || st.Provider != provider)) {
		return OAuthState{}, ErrOAuthState
	}
	if err != nil {
		return st, err
	}
	if link != nil {
		st.LinkUserID = *link
	}
	return st, nil
}

// OAuthProfile is what a verified ID token says about the user.
type OAuthProfile struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// OAuthSignIn finds or creates the user for a provider identity: an existing
// identity signs in (and cancels a pending deletion); otherwise a new
// OAuth-only account is created from the verified email. created reports a
// new account (the web app then shows onboarding and the terms prompt).
func (s *Store) OAuthSignIn(ctx context.Context, p OAuthProfile) (userID int64, created bool, err error) {
	err = s.QueryRow(ctx, `UPDATE user_identities SET last_login_at=now() WHERE provider=$1 AND subject=$2 RETURNING user_id`, p.Provider, p.Subject).Scan(&userID)
	if err == nil {
		_, err = s.cancelDeletion(ctx, userID)
		return userID, false, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	email := strings.ToLower(strings.TrimSpace(p.Email))
	if email == "" || !p.EmailVerified {
		return 0, false, ErrNoEmail
	}
	name, _ := CleanDisplayName(p.Name)
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,'',$2) ON CONFLICT (email) DO NOTHING RETURNING id`, email, name).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, ErrIdentityEmailTaken
	}
	if err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO user_identities(user_id,provider,subject,email,last_login_at) VALUES($1,$2,$3,$4,now())`,
		userID, p.Provider, p.Subject, email); err != nil {
		if isUniqueViolation(err) { // raced with a concurrent first sign-in
			return 0, false, ErrIdentityTaken
		}
		return 0, false, err
	}
	return userID, true, tx.Commit(ctx)
}

// LinkIdentity links a provider identity to a signed-in user.
func (s *Store) LinkIdentity(ctx context.Context, userID int64, p OAuthProfile) error {
	var owner int64
	err := s.QueryRow(ctx, `SELECT user_id FROM user_identities WHERE provider=$1 AND subject=$2`, p.Provider, p.Subject).Scan(&owner)
	switch {
	case err == nil && owner == userID:
		return nil
	case err == nil:
		return ErrIdentityTaken
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO user_identities(user_id,provider,subject,email) VALUES($1,$2,$3,$4)
		ON CONFLICT (user_id,provider) DO UPDATE SET subject=EXCLUDED.subject, email=EXCLUDED.email, created_at=now()`,
		userID, p.Provider, p.Subject, strings.ToLower(strings.TrimSpace(p.Email)))
	if isUniqueViolation(err) {
		return ErrIdentityTaken
	}
	return err
}

// Identities lists the user's linked providers.
func (s *Store) Identities(ctx context.Context, userID int64) ([]map[string]any, error) {
	out, err := collectRows(ctx, s, `SELECT provider,email,created_at,last_login_at FROM user_identities WHERE user_id=$1 ORDER BY provider`, []any{userID},
		func(r pgx.Rows) (map[string]any, error) {
			var provider, email string
			var created time.Time
			var last *time.Time
			err := r.Scan(&provider, &email, &created, &last)
			return map[string]any{"provider": provider, "email": email, "linked_at": created, "last_login_at": last}, err
		})
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

// UnlinkIdentity removes a provider from the user, unless it is the only way
// left to sign in (no password and no other provider).
func (s *Store) UnlinkIdentity(ctx context.Context, userID int64, provider string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hasPassword bool
	var others int
	if err = tx.QueryRow(ctx, `SELECT password_hash<>'', (SELECT count(*) FROM user_identities WHERE user_id=$1 AND provider<>$2) FROM users WHERE id=$1 FOR UPDATE`,
		userID, provider).Scan(&hasPassword, &others); err != nil {
		return err
	}
	if !hasPassword && others == 0 {
		return ErrLastSignInMethod
	}
	tag, err := tx.Exec(ctx, `DELETE FROM user_identities WHERE user_id=$1 AND provider=$2`, userID, provider)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return tx.Commit(ctx)
}

// CreateOAuthLoginCode stores a one-time code for a finished sign-in and
// returns the code (only its hash is stored).
func (s *Store) CreateOAuthLoginCode(ctx context.Context, userID int64, provider string, remember, created bool) (string, error) {
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err = s.Exec(ctx, `DELETE FROM oauth_login_codes WHERE expires_at < now()`); err != nil {
		return "", err
	}
	_, err = s.Exec(ctx, `INSERT INTO oauth_login_codes(code_hash,user_id,remember,provider,created_user,expires_at) VALUES($1,$2,$3,$4,$5,$6)`,
		hashToken(code), userID, remember, provider, created, time.Now().Add(OAuthCodeTTL))
	return code, err
}

// ExchangeOAuthLoginCode consumes a login code and opens a session.
func (s *Store) ExchangeOAuthLoginCode(ctx context.Context, code, userAgent string) (map[string]any, error) {
	var uid int64
	var remember, created, live bool
	var provider, email string
	err := s.QueryRow(ctx, `WITH c AS (DELETE FROM oauth_login_codes WHERE code_hash=$1 RETURNING user_id,remember,provider,created_user,expires_at>now() AS live)
		SELECT c.user_id,c.remember,c.provider,c.created_user,c.live,u.email FROM c JOIN users u ON u.id=c.user_id`, hashToken(code)).
		Scan(&uid, &remember, &provider, &created, &live, &email)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !live) {
		return nil, ErrOAuthState
	}
	if err != nil {
		return nil, err
	}
	out, err := s.createSession(ctx, uid, email, SessionMeta{Remember: remember, UserAgent: userAgent, Method: provider})
	if err == nil {
		out["created"] = created
	}
	return out, err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
