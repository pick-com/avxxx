package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/completeness"
	"avxxx/internal/crosscheck"
	"avxxx/internal/model"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Third-party cross-check job storage (internal/crosscheck, migration 032).

// JobKindCrossCheck is the hourly cross-check job.
const JobKindCrossCheck = crosscheck.JobKind

// CrossCheckSource implements crosscheck.Store: the adapter's source row
// (an enabled one first, then the oldest).
func (s *Store) CrossCheckSource(ctx context.Context, adapter string) (crosscheck.Source, bool, error) {
	var x model.Source
	var blocked *time.Time
	err := scanSource(s.QueryRow(ctx, `SELECT `+sourceColumns+`, s.blocked_until FROM sources s WHERE s.adapter=$1 ORDER BY s.enabled DESC, s.id LIMIT 1`, adapter), &x, &blocked)
	if errors.Is(err, pgx.ErrNoRows) {
		return crosscheck.Source{}, false, nil
	}
	if err != nil {
		return crosscheck.Source{}, false, err
	}
	return crosscheck.Source{ID: x.ID, Name: x.Name, Enabled: x.Enabled, BlockedUntil: blocked, Governance: governance(x)}, true, nil
}

// crossCheckNeeds are the reasons a release is worth a third-party lookup:
// a key field is missing or a field conflict waits for review. The cover test
// is the completeness report's.
func crossCheckNeeds() string {
	cover := completenessSQL[completeness.EntityRelease].have["cover"]
	return `ARRAY_REMOVE(ARRAY[
		CASE WHEN r.release_date IS NULL THEN 'missing_release_date' END,
		CASE WHEN COALESCE(btrim(r.maker_name),'')='' THEN 'missing_maker' END,
		CASE WHEN NOT EXISTS (SELECT 1 FROM release_performers rp WHERE rp.release_id=r.id) THEN 'missing_performers' END,
		CASE WHEN NOT ` + cover + ` THEN 'missing_cover' END,
		CASE WHEN EXISTS (SELECT 1 FROM field_conflicts fc WHERE fc.entity_type='release' AND fc.entity_id=r.id AND fc.status='pending') THEN 'open_conflict' END
	], NULL)`
}

// DueCrossChecks implements crosscheck.Store: unmerged, not removed releases
// with a DVD code that need a lookup (crossCheckNeeds), have no record from
// sourceID yet and no attempt still backing off; releases with an open
// conflict first, then newest.
func (s *Store) DueCrossChecks(ctx context.Context, sourceID int64, now time.Time, limit int, exclude []int64) ([]crosscheck.Candidate, error) {
	if exclude == nil {
		exclude = []int64{}
	}
	rows, err := s.Query(ctx, `SELECT id, slug, codes, needs, status, tried_at, next_try_at, attempts, matched_key, candidates, last_error FROM (
		SELECT r.id, r.slug, r.release_date,
			array_agg(DISTINCT ri.identifier_value ORDER BY ri.identifier_value) AS codes,
			`+crossCheckNeeds()+` AS needs,
			a.status, a.tried_at, a.next_try_at, a.attempts, a.matched_key, a.candidates, a.last_error
		FROM releases r
		JOIN release_identifiers ri ON ri.release_id=r.id AND ri.identifier_type='dvd_code'
		LEFT JOIN crosscheck_attempts a ON a.release_id=r.id AND a.source_id=$1
		WHERE r.merged_into_id IS NULL AND r.status<>'removed'
		  AND NOT EXISTS (SELECT 1 FROM source_records sr WHERE sr.source_id=$1 AND sr.release_id=r.id)
		  AND (a.release_id IS NULL OR a.next_try_at<=$2)
		  AND r.id <> ALL($4::bigint[])
		GROUP BY r.id, a.release_id, a.source_id) x
		WHERE cardinality(needs) > 0
		ORDER BY ('open_conflict' = ANY(needs)) DESC, release_date DESC NULLS LAST, id DESC
		LIMIT $3`, sourceID, now, limit, exclude)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []crosscheck.Candidate
	for rows.Next() {
		var c crosscheck.Candidate
		var status, matched, lastErr *string
		var tried, next *time.Time
		var attempts *int
		var cands []byte
		if err := rows.Scan(&c.ReleaseID, &c.Slug, &c.Codes, &c.Reasons, &status, &tried, &next, &attempts, &matched, &cands, &lastErr); err != nil {
			return nil, err
		}
		if status != nil {
			a := crosscheck.Attempt{ReleaseID: c.ReleaseID, SourceID: sourceID, Status: *status, TriedAt: *tried, NextTryAt: next, Attempts: *attempts}
			if matched != nil {
				a.MatchedKey = *matched
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

// CrossCheckTriedSince implements crosscheck.Store.
func (s *Store) CrossCheckTriedSince(ctx context.Context, sourceID int64, since time.Time) (n int, err error) {
	err = s.QueryRow(ctx, `SELECT count(*) FROM crosscheck_attempts WHERE source_id=$1 AND tried_at>=$2`, sourceID, since).Scan(&n)
	return n, err
}

// RecordCrossCheckAttempt implements crosscheck.Store (one row per release
// and source: the latest attempt).
func (s *Store) RecordCrossCheckAttempt(ctx context.Context, a crosscheck.Attempt) error {
	cands, reasons := a.Candidates, a.Reasons
	if cands == nil {
		cands = []string{}
	}
	if reasons == nil {
		reasons = []string{}
	}
	rawC, err := json.Marshal(cands)
	if err != nil {
		return err
	}
	rawR, err := json.Marshal(reasons)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO crosscheck_attempts(release_id,source_id,status,tried_at,next_try_at,attempts,matched_key,candidates,reasons,last_error)
		VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8::jsonb,$9::jsonb,NULLIF($10,''))
		ON CONFLICT (release_id,source_id) DO UPDATE SET status=EXCLUDED.status,tried_at=EXCLUDED.tried_at,next_try_at=EXCLUDED.next_try_at,
		attempts=EXCLUDED.attempts,matched_key=EXCLUDED.matched_key,candidates=EXCLUDED.candidates,reasons=EXCLUDED.reasons,last_error=EXCLUDED.last_error`,
		a.ReleaseID, a.SourceID, a.Status, a.TriedAt, a.NextTryAt, a.Attempts, a.MatchedKey, string(rawC), string(rawR), a.LastError)
	return err
}

// CrossCheckAttempt returns the attempt for (release, source), nil if none.
func (s *Store) CrossCheckAttempt(ctx context.Context, releaseID, sourceID int64) (*crosscheck.Attempt, error) {
	a := crosscheck.Attempt{ReleaseID: releaseID, SourceID: sourceID}
	var matched, lastErr *string
	var cands, reasons []byte
	err := s.QueryRow(ctx, `SELECT status,tried_at,next_try_at,attempts,matched_key,candidates,reasons,last_error
		FROM crosscheck_attempts WHERE release_id=$1 AND source_id=$2`, releaseID, sourceID).
		Scan(&a.Status, &a.TriedAt, &a.NextTryAt, &a.Attempts, &matched, &cands, &reasons, &lastErr)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if matched != nil {
		a.MatchedKey = *matched
	}
	if lastErr != nil {
		a.LastError = *lastErr
	}
	_ = json.Unmarshal(cands, &a.Candidates)
	_ = json.Unmarshal(reasons, &a.Reasons)
	return &a, nil
}

// CrossCheckFetchPolicy implements crosscheck.Store: the source's
// incremental-phase policy, so lookups and the discovery crawl share one
// daily page budget.
func (s *Store) CrossCheckFetchPolicy(ctx context.Context, sourceID int64) (catalog.FetchPolicy, bool, error) {
	p, err := s.FetchPolicy(ctx, sourceID, catalog.PhaseIncremental)
	return p.Fetch, p.Exhausted, err
}

// RecordCrossCheckUsage implements crosscheck.Store.
func (s *Store) RecordCrossCheckUsage(ctx context.Context, sourceID int64, pages, bytes int64) error {
	return s.RecordCrawlUsage(ctx, sourceID, catalog.PhaseIncremental, pages, bytes)
}
