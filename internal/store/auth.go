package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"avxxx/internal/search"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/argon2"
)

// ErrEmailTaken is returned by Register when the email is already registered
// (unique violation on users.email).
var ErrEmailTaken = errors.New("email already registered")

// ErrInvalidCredentials is returned by Login for both unknown email and wrong password.
var ErrInvalidCredentials = errors.New("invalid email or password")

// argon2id parameters (RFC 9106 second recommended option, scaled down for an API server).
const (
	argonMemory  uint32 = 64 * 1024 // KiB
	argonTime    uint32 = 3
	argonThreads uint8  = 2
	argonSaltLen        = 16
	argonKeyLen  uint32 = 32
)

// hashPassword returns a PHC-style argon2id string:
// $argon2id$v=19$m=65536,t=3,p=2$<b64 salt>$<b64 hash>
func hashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// legacySHA256Hash is the pre-argon2id unsalted scheme, kept only to verify and upgrade old rows.
func legacySHA256Hash(password string) string {
	h := sha256.Sum256([]byte(password))
	return hex.EncodeToString(h[:])
}

// isLegacyHash reports whether stored is a legacy 64-hex-char SHA-256 digest.
func isLegacyHash(stored string) bool {
	if len(stored) != 64 {
		return false
	}
	_, err := hex.DecodeString(stored)
	return err == nil
}

// verifyPassword checks password against stored (argon2id PHC string or legacy SHA-256 hex).
// needsRehash is true when the password matched but the stored value should be replaced
// with a fresh argon2id hash (legacy scheme or outdated parameters).
func verifyPassword(stored, password string) (ok, needsRehash bool) {
	if isLegacyHash(stored) {
		want, _ := hex.DecodeString(stored)
		got := sha256.Sum256([]byte(password))
		ok = subtle.ConstantTimeCompare(want, got[:]) == 1
		return ok, ok
	}
	// "$argon2id$v=19$m=65536,t=3,p=2$salt$hash" splits into
	// ["", "argon2id", "v=19", "m=65536,t=3,p=2", salt, hash]
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil || memory == 0 || iterations == 0 || threads == 0 {
		return false, false
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, false
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, false
	}
	got := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return false, false
	}
	needsRehash = memory != argonMemory || iterations != argonTime || threads != argonThreads || uint32(len(want)) != argonKeyLen || len(salt) != argonSaltLen
	return true, needsRehash
}

// hashToken returns the value stored in user_sessions.token_hash for a client token.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// dummyHash is verified against when the email is unknown so that Login takes
// roughly the same time whether or not the account exists.
var dummyHash = func() string {
	h, err := hashPassword("dummy password for timing equalisation")
	if err != nil {
		panic(err)
	}
	return h
}()

func (s *Store) Register(ctx context.Context, email, password, displayName string) (map[string]any, error) {
	return s.RegisterSession(ctx, email, password, displayName, "", Adult{}, SessionMeta{Remember: true})
}

// RegisterSession creates an account and its first session. termsVersion, when
// set, is recorded in terms_acceptances (source "register"); a confirmed
// adult sets users.adult_confirmed_at (and adult_birth_ym when given). The
// API requires the confirmation (internal/api/agegate.go); the store only
// records it.
func (s *Store) RegisterSession(ctx context.Context, email, password, displayName, termsVersion string, adult Adult, meta SessionMeta) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if len(email) < 3 || len(password) < 8 {
		return nil, errors.New("email and password (min 8 characters) are required")
	}
	name, err := CleanDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	if termsVersion != "" && !ValidTermsVersion(termsVersion) {
		return nil, invalid("invalid terms version")
	}
	ym, err := adult.birthYM()
	if err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	var id int64
	err = s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name,adult_confirmed_at,adult_birth_ym)
		VALUES($1,$2,$3,CASE WHEN $4 THEN now() END,$5) RETURNING id`, email, hash, name, adult.Confirmed, ym).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Wrapping the PgError keeps "duplicate key" in the message, which the
			// current API reply helper already maps to 409.
			return nil, fmt.Errorf("%w: %w", ErrEmailTaken, err)
		}
		return nil, fmt.Errorf("register user: %w", err)
	}
	if termsVersion != "" {
		if err := s.AcceptTerms(ctx, id, termsVersion, "register"); err != nil {
			return nil, err
		}
	}
	meta.Method = "password"
	return s.createSession(ctx, id, email, meta)
}

func (s *Store) Login(ctx context.Context, email, password string) (map[string]any, error) {
	return s.LoginSession(ctx, email, password, SessionMeta{Remember: true})
}

// LoginSession checks the password and opens a session. Logging in cancels a
// pending account deletion (the response then has "deletion_cancelled": true).
func (s *Store) LoginSession(ctx context.Context, email, password string, meta SessionMeta) (map[string]any, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var id int64
	var stored string
	err := s.QueryRow(ctx, `SELECT id,password_hash FROM users WHERE email=$1`, email).Scan(&id, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		verifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, fmt.Errorf("login lookup: %w", err)
	}
	if stored == "" { // OAuth-only account: same work and answer as a wrong password
		verifyPassword(dummyHash, password)
		return nil, ErrInvalidCredentials
	}
	ok, needsRehash := verifyPassword(stored, password)
	if !ok {
		return nil, ErrInvalidCredentials
	}
	if needsRehash {
		if hash, err := hashPassword(password); err == nil {
			// Best effort: a failed upgrade must not block a valid login; it is retried next time.
			_, _ = s.Exec(ctx, `UPDATE users SET password_hash=$1 WHERE id=$2 AND password_hash=$3`, hash, id, stored)
		}
	}
	if banned, err := userBanned(ctx, s, id); err != nil {
		return nil, err
	} else if banned {
		return nil, ErrAccountBanned // before cancelDeletion: a ban must not undo a deletion request
	}
	cancelled, err := s.cancelDeletion(ctx, id)
	if err != nil {
		return nil, err
	}
	meta.Method = "password"
	out, err := s.createSession(ctx, id, email, meta)
	if err == nil && cancelled {
		out["deletion_cancelled"] = true
	}
	return out, err
}

// SessionMeta describes a new session: remember-me (30 d, kept by the
// browser) or a browser session (24 h), the device's User-Agent and how the
// user signed in (password | google | apple).
type SessionMeta struct {
	Remember  bool
	UserAgent string
	Method    string
}

// Session lifetimes on the server. The web app keeps a non-remembered token
// only for the browser session; the server bounds it to a day regardless.
const (
	SessionTTLRemember = 30 * 24 * time.Hour
	SessionTTLBrowser  = 24 * time.Hour
)

func (s *Store) createSession(ctx context.Context, id int64, email string, meta SessionMeta) (map[string]any, error) {
	// Every sign-in path (password, OAuth, login code) ends here, so a ban
	// (user_bans, migration 042) blocks them all.
	if banned, err := userBanned(ctx, s, id); err != nil {
		return nil, err
	} else if banned {
		return nil, ErrAccountBanned
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(b)
	ttl := SessionTTLBrowser
	if meta.Remember {
		ttl = SessionTTLRemember
	}
	if meta.Method == "" {
		meta.Method = "password"
	}
	exp := time.Now().Add(ttl)
	if _, err := s.Exec(ctx, `INSERT INTO user_sessions(token_hash,user_id,expires_at,user_agent,remember,auth_method) VALUES($1,$2,$3,$4,$5,$6)`,
		hashToken(token), id, exp, truncate(meta.UserAgent, 400), meta.Remember, meta.Method); err != nil {
		return nil, err
	}
	return map[string]any{"token": token, "expires_at": exp, "remember": meta.Remember, "user": map[string]any{"id": id, "email": email}}, nil
}

// UserByToken returns the signed-in user: id, email, display name, whether a
// password is set, the latest accepted terms version ("" = none), whether
// onboarding was finished, whether the 18+ confirmation is on file
// (adult_confirmed; false = show the one-time prompt) and the linked sign-in
// providers.
func (s *Store) UserByToken(ctx context.Context, token string) (map[string]any, error) {
	var id int64
	var email, name, terms string
	var hasPassword bool
	var created time.Time
	var onboarded *time.Time
	var providers []string
	var adult bool
	err := s.QueryRow(ctx, `SELECT u.id,u.email,u.display_name,u.password_hash<>'',u.created_at,u.onboarded_at,u.adult_confirmed_at IS NOT NULL,
		COALESCE((SELECT version FROM terms_acceptances t WHERE t.user_id=u.id ORDER BY accepted_at DESC, version DESC LIMIT 1),''),
		COALESCE((SELECT array_agg(provider ORDER BY provider) FROM user_identities i WHERE i.user_id=u.id),'{}')
		FROM user_sessions ss JOIN users u ON u.id=ss.user_id WHERE ss.token_hash=$1 AND ss.expires_at>now()`, hashToken(token)).
		Scan(&id, &email, &name, &hasPassword, &created, &onboarded, &adult, &terms, &providers)
	if err != nil {
		return nil, errors.New("unauthorized")
	}
	return map[string]any{"id": id, "email": email, "display_name": name, "has_password": hasPassword,
		"created_at": created, "onboarded": onboarded != nil, "adult_confirmed": adult, "terms_version": terms, "providers": providers}, nil
}

// UserIDByToken resolves a session token. last_seen_at is refreshed at most
// every 5 minutes, so authenticated reads do not write on every request.
func (s *Store) UserIDByToken(ctx context.Context, token string) (int64, error) {
	var id int64
	err := s.QueryRow(ctx, `WITH ss AS (
		  SELECT user_id FROM user_sessions WHERE token_hash=$1 AND expires_at>now()),
		touch AS (
		  UPDATE user_sessions SET last_seen_at=now()
		  WHERE token_hash=$1 AND expires_at>now() AND last_seen_at < now() - interval '5 minutes')
		SELECT user_id FROM ss`, hashToken(token)).Scan(&id)
	return id, err
}

func (s *Store) SaveWatch(ctx context.Context, userID int64, slug string, progress, duration int) error {
	var releaseID int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status='published'`, slug).Scan(&releaseID); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,$3,NULLIF($4,0),now()) ON CONFLICT(user_id,release_id) DO UPDATE SET progress_seconds=EXCLUDED.progress_seconds,duration_seconds=COALESCE(EXCLUDED.duration_seconds,watch_history.duration_seconds),watched_at=now()`, userID, releaseID, progress, duration)
	return err
}

// historySearchScan bounds a 播放记录 search to the user's newest rows.
const historySearchScan = 2000

// WatchHistory lists the user's 100 most recent watch_history rows, newest
// first. A non-empty q (播放记录 search) keeps the rows whose title (or
// Japanese title, or slug) or a performer's name contains it, or whose
// product code starts with it or has the same code_match_key ("abc123"
// finds ABC-123). The search reads only the user's newest historySearchScan
// rows (watch_history_recent) and probes per-release indexes, so it stays
// bounded without a new index.
func (s *Store) WatchHistory(ctx context.Context, userID int64, q string) ([]map[string]any, error) {
	q = search.NormalizeQuery(q)
	var rows pgx.Rows
	var err error
	if q == "" {
		rows, err = s.Query(ctx, `SELECT r.slug,r.canonical_title,w.progress_seconds,COALESCE(w.duration_seconds,0),w.watched_at FROM watch_history w JOIN releases r ON r.id=w.release_id WHERE w.user_id=$1 ORDER BY w.watched_at DESC LIMIT 100`, userID)
	} else {
		// A product code: a prefix pattern ("abc-1" → abc-1%), and, when the
		// query has digits, the code_match_key form ("abc00123" = ABC-123).
		code, key := "", ""
		if c := search.CodePrefix(q); c != "" {
			code = prefixPattern(c)
		}
		if strings.ContainsAny(q, "0123456789") {
			key = q
		}
		rows, err = s.Query(ctx, `
			WITH recent AS (
			  SELECT release_id,progress_seconds,duration_seconds,watched_at FROM watch_history
			  WHERE user_id=$1 ORDER BY watched_at DESC LIMIT $5)
			SELECT r.slug,r.canonical_title,w.progress_seconds,COALESCE(w.duration_seconds,0),w.watched_at
			FROM recent w JOIN releases r ON r.id=w.release_id
			WHERE r.canonical_title ILIKE $2 OR r.title_ja ILIKE $2 OR r.slug ILIKE $2
			  OR EXISTS (SELECT 1 FROM release_performers rp JOIN performers p ON p.id=rp.performer_id
			    WHERE rp.release_id=r.id AND (p.canonical_name ILIKE $2 OR p.canonical_name_ja ILIKE $2))
			  OR EXISTS (SELECT 1 FROM release_identifiers i WHERE i.release_id=r.id
			    AND (($3::text<>'' AND i.normalized_value LIKE $3) OR ($4::text<>'' AND code_match_key(i.normalized_value)=code_match_key($4))))
			ORDER BY w.watched_at DESC LIMIT 100`,
			userID, containsPattern(q), code, key, historySearchScan)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var slug, title string
		var progress, duration int
		var at time.Time
		if err = rows.Scan(&slug, &title, &progress, &duration, &at); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"slug": slug, "title": title, "progress_seconds": progress, "duration_seconds": duration, "watched_at": at})
	}
	return out, rows.Err()
}

// SetFavorite adds a release to, or removes it from, the user's default
// playlist (migration 036 replaced the single favorites list).
func (s *Store) SetFavorite(ctx context.Context, userID int64, slug string, enabled bool) error {
	if enabled {
		return s.AddPlaylistItem(ctx, userID, 0, slug)
	}
	return s.RemovePlaylistItem(ctx, userID, 0, slug)
}

// Favorites lists the user's default playlist, newest first.
func (s *Store) Favorites(ctx context.Context, userID int64) ([]map[string]any, error) {
	id, err := ensureDefaultPlaylist(ctx, s, userID)
	if err != nil {
		return nil, err
	}
	items, err := s.playlistItems(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		out = append(out, map[string]any{"slug": it.Slug, "title": it.Title, "created_at": it.AddedAt})
	}
	return out, nil
}
