package store

import (
	"avxxx/internal/media"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// media_resolve_cache (migration 050): the shared L2 behind the API's
// in-process resolve cache. *Store implements media.ResolveStore.

var _ media.ResolveStore = (*Store)(nil)

// LoadResolve returns key's row when it is still valid at now, else nil.
func (s *Store) LoadResolve(ctx context.Context, key string, now time.Time) (*media.StoredResolve, error) {
	var r media.StoredResolve
	err := s.QueryRow(ctx, `SELECT result, expires_at, max_expires_at, refreshed_at FROM media_resolve_cache WHERE key=$1 AND expires_at > $2`, key, now).
		Scan(&r.Result, &r.ExpiresAt, &r.MaxExpiresAt, &r.RefreshedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SaveResolve upserts key's row. Oversized results are rejected by the
// table's CHECK (the media cache never sends them).
func (s *Store) SaveResolve(ctx context.Context, key string, e media.StoredResolve) error {
	if len(e.Result) > media.MaxStoredResolveBytes {
		return errors.New("resolve result too large to store")
	}
	_, err := s.Exec(ctx, `INSERT INTO media_resolve_cache(key, result, expires_at, max_expires_at, refreshed_at) VALUES($1, $2, $3, $4, $5)
		ON CONFLICT (key) DO UPDATE SET result=EXCLUDED.result, expires_at=EXCLUDED.expires_at, max_expires_at=EXCLUDED.max_expires_at, refreshed_at=EXCLUDED.refreshed_at`,
		key, string(e.Result), e.ExpiresAt, e.MaxExpiresAt, e.RefreshedAt)
	return err
}

// ExtendResolve slides expires_at forward, never past max_expires_at and
// never backwards.
func (s *Store) ExtendResolve(ctx context.Context, key string, expiresAt time.Time) error {
	_, err := s.Exec(ctx, `UPDATE media_resolve_cache SET expires_at = LEAST(GREATEST(expires_at, $2), max_expires_at) WHERE key=$1`, key, expiresAt)
	return err
}

func (s *Store) DeleteResolve(ctx context.Context, key string) error {
	_, err := s.Exec(ctx, `DELETE FROM media_resolve_cache WHERE key=$1`, key)
	return err
}

// PurgeResolves deletes up to 5000 rows expired at now.
func (s *Store) PurgeResolves(ctx context.Context, now time.Time) (int64, error) {
	tag, err := s.Exec(ctx, `DELETE FROM media_resolve_cache WHERE key IN (SELECT key FROM media_resolve_cache WHERE expires_at <= $1 LIMIT 5000)`, now)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
