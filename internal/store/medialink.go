package store

// Offline release -> playback provider linking (internal/medialink,
// migrations/026_media_link.sql).

import (
	"avxxx/internal/medialink"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobKindMediaLink is the hourly offline linking job.
const JobKindMediaLink = medialink.JobKind

// EnqueueKind queues a sourceless job of kind unless one is already pending
// or running, in which case it returns that job's id with enqueued=false.
func (s *Store) EnqueueKind(ctx context.Context, kind string) (jobID int64, enqueued bool, err error) {
	err = s.QueryRow(ctx, `INSERT INTO jobs(kind) VALUES($1) ON CONFLICT DO NOTHING RETURNING id`, kind).Scan(&jobID)
	if err == nil {
		return jobID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = s.QueryRow(ctx, `SELECT id FROM jobs WHERE kind=$1 AND source_id IS NULL AND status IN ('pending','running') ORDER BY id DESC LIMIT 1`, kind).Scan(&jobID)
	return jobID, false, err
}

// DueMediaLinks implements medialink.Store.
func (s *Store) DueMediaLinks(ctx context.Context, provider string, now time.Time, limit int, exclude []int64) ([]medialink.Candidate, error) {
	if exclude == nil {
		exclude = []int64{}
	}
	rows, err := s.Query(ctx, `SELECT r.id, array_agg(DISTINCT ri.normalized_value ORDER BY ri.normalized_value),
			a.status, a.tried_at, a.next_try_at, a.attempts, a.matched_media_id, a.candidates, a.last_error
		FROM releases r
		JOIN release_identifiers ri ON ri.release_id=r.id AND ri.identifier_type='dvd_code'
		LEFT JOIN media_link_attempts a ON a.release_id=r.id AND a.provider=$1
		WHERE r.status='published' AND r.merged_into_id IS NULL
		  AND NOT EXISTS (SELECT 1 FROM release_media_sources m WHERE m.release_id=r.id AND m.source=$1)
		  AND (a.release_id IS NULL OR a.next_try_at<=$2)
		  AND r.id <> ALL($4::bigint[])
		GROUP BY r.id, a.release_id, a.provider
		ORDER BY r.release_date DESC NULLS LAST, r.id DESC
		LIMIT $3`, provider, now, limit, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []medialink.Candidate
	for rows.Next() {
		var c medialink.Candidate
		var status, mediaID, lastErr *string
		var tried *time.Time
		var next *time.Time
		var attempts *int
		var cands []byte
		if err := rows.Scan(&c.ReleaseID, &c.Codes, &status, &tried, &next, &attempts, &mediaID, &cands, &lastErr); err != nil {
			return nil, err
		}
		if status != nil {
			a := medialink.Attempt{ReleaseID: c.ReleaseID, Provider: provider, Status: *status, TriedAt: *tried, NextTryAt: next, Attempts: *attempts}
			if mediaID != nil {
				a.MatchedMediaID = *mediaID
			}
			if lastErr != nil {
				a.LastError = *lastErr
			}
			_ = json.Unmarshal(cands, &a.Candidates)
			c.Prev = &a
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MediaLinkTriedSince implements medialink.Store.
func (s *Store) MediaLinkTriedSince(ctx context.Context, provider string, since time.Time) (n int, err error) {
	err = s.QueryRow(ctx, `SELECT count(*) FROM media_link_attempts WHERE provider=$1 AND tried_at>=$2`, provider, since).Scan(&n)
	return n, err
}

// MediaLinkProviderBackoff implements medialink.Store: the backoff_until of
// the latest 'media_link.<provider>' run, zero when it has none.
func (s *Store) MediaLinkProviderBackoff(ctx context.Context, provider string) (time.Time, error) {
	var raw *string
	err := s.QueryRow(ctx, `SELECT detail->>'backoff_until' FROM nightly_runs WHERE job=$1 ORDER BY started_at DESC, id DESC LIMIT 1`,
		medialink.JobKind+"."+provider).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && raw == nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return time.Time{}, nil // malformed: ignore rather than block linking
	}
	return t, nil
}

// ClaimMediaLink implements medialink.LinkStore: it inserts the link unless
// the release already has one for provider or another release holds
// mediaID, and returns the release holding mediaID afterwards (releaseID
// when the release ended up linked, including by a concurrent writer).
func (s *Store) ClaimMediaLink(ctx context.Context, releaseID int64, provider, mediaID, mediaURL string) (int64, error) {
	tag, err := s.Exec(ctx, `INSERT INTO release_media_sources(release_id,source,media_id,media_url)
		VALUES($1,lower($2),$3,NULLIF($4,'')) ON CONFLICT DO NOTHING`, releaseID, provider, mediaID, mediaURL)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() == 1 {
		return releaseID, nil
	}
	var owner int64
	err = s.QueryRow(ctx, `SELECT release_id FROM release_media_sources WHERE source=lower($1) AND media_id=$2`, provider, mediaID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return releaseID, nil // the release already has another link for provider
	}
	return owner, err
}

// RecordMediaLinkAttempt implements medialink.LinkStore (upsert).
func (s *Store) RecordMediaLinkAttempt(ctx context.Context, a medialink.Attempt) error {
	cands := a.Candidates
	if cands == nil {
		cands = []string{}
	}
	raw, err := json.Marshal(cands)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO media_link_attempts(release_id,provider,status,tried_at,next_try_at,attempts,matched_media_id,candidates,last_error)
		VALUES($1,lower($2),$3,$4,$5,$6,NULLIF($7,''),$8::jsonb,NULLIF($9,''))
		ON CONFLICT (release_id,provider) DO UPDATE SET status=EXCLUDED.status,tried_at=EXCLUDED.tried_at,next_try_at=EXCLUDED.next_try_at,
		attempts=EXCLUDED.attempts,matched_media_id=EXCLUDED.matched_media_id,candidates=EXCLUDED.candidates,last_error=EXCLUDED.last_error`,
		a.ReleaseID, a.Provider, a.Status, a.TriedAt, a.NextTryAt, a.Attempts, a.MatchedMediaID, string(raw), a.LastError)
	return err
}

// MediaLinkAttempt returns the attempt for (release, provider), nil if none.
func (s *Store) MediaLinkAttempt(ctx context.Context, releaseID int64, provider string) (*medialink.Attempt, error) {
	a := medialink.Attempt{ReleaseID: releaseID, Provider: provider}
	var mediaID, lastErr *string
	var cands []byte
	err := s.QueryRow(ctx, `SELECT status,tried_at,next_try_at,attempts,matched_media_id,candidates,last_error
		FROM media_link_attempts WHERE release_id=$1 AND provider=lower($2)`, releaseID, provider).
		Scan(&a.Status, &a.TriedAt, &a.NextTryAt, &a.Attempts, &mediaID, &cands, &lastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mediaID != nil {
		a.MatchedMediaID = *mediaID
	}
	if lastErr != nil {
		a.LastError = *lastErr
	}
	_ = json.Unmarshal(cands, &a.Candidates)
	return &a, nil
}

// ---------------------------------------------------------------- admin

// MediaLinkFilter selects GET /api/admin/media-links rows.
type MediaLinkFilter struct {
	Provider string
	Status   string // "" = any
	Cursor   string
	Limit    int
}

// MediaLinkRow is one attempt with its release.
type MediaLinkRow struct {
	medialink.Attempt
	Slug   string   `json:"slug"`
	Title  string   `json:"title"`
	Codes  []string `json:"codes"`
	Linked string   `json:"linked_media_id,omitempty"` // current release_media_sources row
}

// MediaLinkAttempts lists attempts newest first (tried_at, release id),
// keyset paginated; it returns the next cursor, or "" on the last page.
func (s *Store) MediaLinkAttempts(ctx context.Context, f MediaLinkFilter) ([]MediaLinkRow, string, error) {
	limit := pageLimit(f.Limit)
	hasCursor := false
	var curAt time.Time
	var curID int64
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		if c.Sort != "media_links" {
			return nil, "", invalidQuery("cursor is for another list")
		}
		if curAt, err = time.Parse(time.RFC3339Nano, c.Key); err != nil {
			return nil, "", invalidQuery("malformed cursor")
		}
		hasCursor, curID = true, c.ID
	}
	rows, err := s.Query(ctx, `SELECT a.release_id,a.provider,a.status,a.tried_at,a.next_try_at,a.attempts,
			COALESCE(a.matched_media_id,''),a.candidates,COALESCE(a.last_error,''),r.slug,r.canonical_title,
			COALESCE((SELECT array_agg(DISTINCT ri.normalized_value ORDER BY ri.normalized_value) FROM release_identifiers ri
				WHERE ri.release_id=r.id AND ri.identifier_type='dvd_code'),'{}'),
			COALESCE((SELECT m.media_id FROM release_media_sources m WHERE m.release_id=r.id AND m.source=a.provider),'')
		FROM media_link_attempts a JOIN releases r ON r.id=a.release_id
		WHERE a.provider=lower($1) AND ($2='' OR a.status=$2)
		  AND (NOT $3 OR (a.tried_at,a.release_id) < ($4,$5))
		ORDER BY a.tried_at DESC, a.release_id DESC LIMIT $6`, f.Provider, f.Status, hasCursor, curAt, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []MediaLinkRow{}
	for rows.Next() {
		var m MediaLinkRow
		var cands []byte
		if err := rows.Scan(&m.ReleaseID, &m.Provider, &m.Status, &m.TriedAt, &m.NextTryAt, &m.Attempts, &m.MatchedMediaID,
			&cands, &m.LastError, &m.Slug, &m.Title, &m.Codes, &m.Linked); err != nil {
			return nil, "", err
		}
		m.Candidates = []string{}
		_ = json.Unmarshal(cands, &m.Candidates)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = EncodeCursor(Cursor{Sort: "media_links", Key: last.TriedAt.UTC().Format(time.RFC3339Nano), ID: last.ReleaseID})
	}
	return out, next, nil
}

// RetryMediaLink makes the release due now. pgx.ErrNoRows when it has no
// attempt for provider.
func (s *Store) RetryMediaLink(ctx context.Context, releaseID int64, provider string) error {
	tag, err := s.Exec(ctx, `UPDATE media_link_attempts SET next_try_at=now() WHERE release_id=$1 AND provider=lower($2)`, releaseID, provider)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// ChooseMediaLink links the release to one of its attempt's candidates (an
// admin decision), marks the attempt found and logs a review event with
// actor. pgx.ErrNoRows without an attempt; a ValidationError when mediaID is
// not a candidate; a ConflictError when another release holds mediaID.
func (s *Store) ChooseMediaLink(ctx context.Context, releaseID int64, provider, mediaID, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cands []byte
	var before *string
	if err := tx.QueryRow(ctx, `SELECT candidates,(SELECT media_id FROM release_media_sources WHERE release_id=$1 AND source=lower($2))
		FROM media_link_attempts WHERE release_id=$1 AND provider=lower($2) FOR UPDATE`, releaseID, provider).Scan(&cands, &before); err != nil {
		return err
	}
	var list []string
	_ = json.Unmarshal(cands, &list)
	ok := false
	for _, c := range list {
		ok = ok || c == mediaID
	}
	if !ok {
		return invalid("media_id is not a candidate of this release")
	}
	var owner int64
	err = tx.QueryRow(ctx, `SELECT release_id FROM release_media_sources WHERE source=lower($1) AND media_id=$2`, provider, mediaID).Scan(&owner)
	if err == nil && owner != releaseID {
		return conflict("media id is already linked to release %d", owner)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO release_media_sources(release_id,source,media_id,media_url,confidence)
		VALUES($1,lower($2),$3,NULLIF($4,''),'admin') ON CONFLICT(release_id,source)
		DO UPDATE SET media_id=EXCLUDED.media_id,media_url=EXCLUDED.media_url,confidence='admin',updated_at=now()`,
		releaseID, provider, mediaID, medialink.MediaURL(provider, mediaID)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE media_link_attempts SET status='found',matched_media_id=$3,next_try_at=NULL,attempts=1,last_error=NULL
		WHERE release_id=$1 AND provider=lower($2)`, releaseID, provider, mediaID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES('release',$1,'media_link_choose',CASE WHEN $3::text IS NULL THEN NULL ELSE jsonb_build_object('provider',$2::text,'media_id',$3::text) END,
		jsonb_build_object('provider',$2::text,'media_id',$4::text),'chosen from media link candidates',$5)`,
		releaseID, provider, before, mediaID, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
