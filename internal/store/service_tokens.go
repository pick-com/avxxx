package store

// Service tokens (migration 044): scoped machine credentials for scripts
// such as the daily SEO check. Only the SHA-256 of the token is stored; the
// API validates scopes against the route table (internal/api/service_tokens.go)
// and authenticates "Authorization: Bearer svc_…" in srv.admin.

import (
	"avxxx/internal/adminauth"
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ServiceTokenPrefix starts every service token, so srv.admin can tell it
// from the break-glass ADMIN_TOKEN without a database lookup.
const ServiceTokenPrefix = "svc_"

// ServiceToken is one service_tokens row (never the token itself).
type ServiceToken struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	TokenPrefix string     `json:"token_prefix"`
	Scopes      []string   `json:"scopes"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	RevokedAt   *time.Time `json:"revoked_at"`
	RevokedBy   *string    `json:"revoked_by"`
	// Status is active, expired or revoked.
	Status string `json:"status"`
}

// ServiceTokenInput creates a token. Scopes are already validated by the API.
type ServiceTokenInput struct {
	Name        string
	Description string
	Scopes      []string
	ExpiresAt   time.Time
}

var serviceTokenNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,47}$`)

const serviceTokenCols = `id,name,description,token_prefix,scopes,created_by,created_at,last_used_at,expires_at,revoked_at,revoked_by,
	CASE WHEN revoked_at IS NOT NULL THEN 'revoked' WHEN expires_at<=now() THEN 'expired' ELSE 'active' END`

func scanServiceToken(row pgx.Row) (ServiceToken, error) {
	var t ServiceToken
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.TokenPrefix, &t.Scopes, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt,
		&t.ExpiresAt, &t.RevokedAt, &t.RevokedBy, &t.Status)
	return t, err
}

// CreateServiceToken stores a new token and returns it with the token value,
// which is never stored or shown again.
func (s *Store) CreateServiceToken(ctx context.Context, in ServiceTokenInput, actor string, actorID int64) (ServiceToken, string, error) {
	in.Name = strings.ToLower(strings.TrimSpace(in.Name))
	in.Description = strings.TrimSpace(in.Description)
	if !serviceTokenNameRE.MatchString(in.Name) {
		return ServiceToken{}, "", invalid("name must be 2-48 characters: a-z, 0-9, . _ -")
	}
	if len([]rune(in.Description)) > 200 {
		return ServiceToken{}, "", invalid("description is at most 200 characters")
	}
	if len(in.Scopes) == 0 {
		return ServiceToken{}, "", invalid("at least one scope is required")
	}
	if !in.ExpiresAt.After(time.Now()) {
		return ServiceToken{}, "", invalid("expires_at must be in the future")
	}
	token, err := adminauth.NewToken(ServiceTokenPrefix)
	if err != nil {
		return ServiceToken{}, "", err
	}
	var by *int64
	if actorID > 0 {
		by = &actorID
	}
	t, err := scanServiceToken(s.QueryRow(ctx, `INSERT INTO service_tokens(name,description,token_hash,token_prefix,scopes,created_by,created_by_id,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING `+serviceTokenCols,
		in.Name, in.Description, adminauth.HashToken(token), token[:len(ServiceTokenPrefix)+6], in.Scopes, actor, by, in.ExpiresAt))
	if isUniqueViolation(err) {
		return ServiceToken{}, "", conflict("a live service token named %q already exists; revoke it first", in.Name)
	}
	if err != nil {
		return ServiceToken{}, "", err
	}
	return t, token, nil
}

// ServiceTokens lists every token, live ones first.
func (s *Store) ServiceTokens(ctx context.Context) ([]ServiceToken, error) {
	rows, err := s.Query(ctx, `SELECT `+serviceTokenCols+` FROM service_tokens
		ORDER BY (revoked_at IS NULL AND expires_at>now()) DESC, created_at DESC, id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServiceToken{}
	for rows.Next() {
		t, err := scanServiceToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeServiceToken revokes a token at once. Unknown: pgx.ErrNoRows;
// already revoked: *ConflictError.
func (s *Store) RevokeServiceToken(ctx context.Context, id int64, actor string) (ServiceToken, error) {
	t, err := scanServiceToken(s.QueryRow(ctx, `UPDATE service_tokens SET revoked_at=now(),revoked_by=$2
		WHERE id=$1 AND revoked_at IS NULL RETURNING `+serviceTokenCols, id, actor))
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if e := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_tokens WHERE id=$1)`, id).Scan(&exists); e != nil {
			return t, e
		}
		if exists {
			return t, conflict("service token %d is already revoked", id)
		}
	}
	return t, err
}

// ServiceTokenByToken resolves a presented token: live (not revoked, not
// expired) tokens only, else pgx.ErrNoRows. last_used_at is refreshed at most
// once a minute.
func (s *Store) ServiceTokenByToken(ctx context.Context, token, ipHash string) (ServiceToken, error) {
	t, err := scanServiceToken(s.QueryRow(ctx, `SELECT `+serviceTokenCols+` FROM service_tokens
		WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>now()`, adminauth.HashToken(token)))
	if err != nil {
		return t, err
	}
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > time.Minute {
		if _, err := s.Exec(ctx, `UPDATE service_tokens SET last_used_at=now(),last_used_ip_hash=$2 WHERE id=$1`, t.ID, ipHash); err != nil {
			return t, err
		}
	}
	return t, nil
}
