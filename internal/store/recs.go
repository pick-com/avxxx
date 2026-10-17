package store

// PostgreSQL side of recommendations V1 (internal/recs, migrations/019_recs.sql):
// the offline job inputs and table swaps, the online recall / hydration
// queries behind GET /api/recs, the trending boards and the admin weights.
// Public reads only return published, unmerged releases and active,
// unmerged performers, so held_review releases never surface.

import (
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ recs.Store = (*Store)(nil)

// Job kinds for the recommendation jobs (cmd/audit worker).
const (
	JobKindRecsSimilarity = recs.JobKindSimilarity
	JobKindRecsTrending   = recs.JobKindTrending
)

// EnqueueSourceless queues a sourceless job of kind unless one is already
// pending or running, in which case it returns that job's id with
// enqueued=false (same contract as EnqueueAudit).
func (s *Store) EnqueueSourceless(ctx context.Context, kind string) (jobID int64, enqueued bool, err error) {
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

// ---------------------------------------------------------------- weights

// SimilarityWeightsRow is the admin view of similarity_weights.
type SimilarityWeightsRow struct {
	recs.Weights
	UpdatedBy string    `json:"updated_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SimilarityWeights reads the single weights row (defaults if missing).
func (s *Store) SimilarityWeights(ctx context.Context) (recs.Weights, error) {
	row, err := s.SimilarityWeightsRow(ctx)
	return row.Weights, err
}

// SimilarityWeightsRow reads the weights with their audit fields.
func (s *Store) SimilarityWeightsRow(ctx context.Context) (SimilarityWeightsRow, error) {
	var r SimilarityWeightsRow
	var by *string
	err := s.QueryRow(ctx, `SELECT metadata,co_star,co_watch,visual,updated_by,updated_at FROM similarity_weights WHERE id`).
		Scan(&r.Metadata, &r.CoStar, &r.CoWatch, &r.Visual, &by, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SimilarityWeightsRow{Weights: recs.DefaultWeights}, nil
	}
	if by != nil {
		r.UpdatedBy = *by
	}
	return r, err
}

// SetSimilarityWeights saves the weights; the next nightly run uses them.
func (s *Store) SetSimilarityWeights(ctx context.Context, w recs.Weights, by string) (SimilarityWeightsRow, error) {
	_, err := s.Exec(ctx, `INSERT INTO similarity_weights(id,metadata,co_star,co_watch,visual,updated_by,updated_at) VALUES(true,$1,$2,$3,$4,NULLIF($5,''),now())
		ON CONFLICT(id) DO UPDATE SET metadata=EXCLUDED.metadata,co_star=EXCLUDED.co_star,co_watch=EXCLUDED.co_watch,visual=EXCLUDED.visual,
		updated_by=EXCLUDED.updated_by,updated_at=now()`, w.Metadata, w.CoStar, w.CoWatch, w.Visual, by)
	if err != nil {
		return SimilarityWeightsRow{}, err
	}
	return s.SimilarityWeightsRow(ctx)
}

// ---------------------------------------------------------------- offline inputs

// ReleaseDocs returns every published, unmerged release with its tags,
// maker, series and active performers (billing order).
func (s *Store) ReleaseDocs(ctx context.Context) ([]recs.ReleaseDoc, error) {
	rows, err := s.Query(ctx, `SELECT r.id, COALESCE(r.release_date - DATE '1970-01-01', 0)::bigint,
		COALESCE(r.maker_name,''), COALESCE(r.series_name,''),
		COALESCE((SELECT array_agg(t.normalized_tag ORDER BY t.normalized_tag) FROM release_tags t WHERE t.release_id=r.id), '{}'),
		COALESCE((SELECT array_agg(rp.performer_id ORDER BY rp.is_primary DESC, rp.billing_order NULLS LAST, rp.performer_id)
			FROM release_performers rp JOIN performers p ON p.id=rp.performer_id AND p.status='active' AND p.merged_into_id IS NULL
			WHERE rp.release_id=r.id), '{}')
		FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (recs.ReleaseDoc, error) {
		var d recs.ReleaseDoc
		err := row.Scan(&d.ID, &d.Order, &d.Maker, &d.Series, &d.Tags, &d.Performers)
		return d, err
	})
}

// watchInteractionsSQL: best watch ratio per (user, published release) from
// watch_history (progress / duration, falling back to runtime) since $1, the
// highest progress milestone since $2 and completed plays since $2
// (play_end reason=completed joined to its play_request by play_id; play_end
// carries no release_id). NULL ratio = unknown duration.
const watchInteractionsSQL = `
WITH wh AS (
  SELECT h.user_id, h.release_id,
    LEAST(1.0, h.progress_seconds::float8 / NULLIF(COALESCE(NULLIF(h.duration_seconds,0), r.runtime_minutes::int*60), 0)) AS ratio
  FROM watch_history h JOIN releases r ON r.id=h.release_id
  WHERE h.watched_at >= $1
), ms AS (
  SELECT user_id, release_id, max((props->>'milestone')::int) / 100.0 AS ratio FROM events
  WHERE event='progress' AND ts_server >= $2 AND user_id IS NOT NULL AND release_id IS NOT NULL AND props->>'milestone' ~ '^[0-9]{1,3}$'
  GROUP BY 1, 2
), done AS (
  SELECT DISTINCT q.user_id, q.release_id, 1.0::float8 AS ratio FROM events q
  JOIN events e ON e.play_id=q.play_id AND e.event='play_end' AND e.props->>'reason'='completed' AND e.ts_server >= $2
  WHERE q.event='play_request' AND q.ts_server >= $2 AND q.user_id IS NOT NULL AND q.release_id IS NOT NULL AND q.play_id IS NOT NULL
), x AS (
  SELECT * FROM wh UNION ALL SELECT * FROM ms UNION ALL SELECT * FROM done
)
SELECT x.user_id, x.release_id, COALESCE(max(x.ratio), -1)
FROM x JOIN releases r ON r.id=x.release_id AND r.status='published' AND r.merged_into_id IS NULL
GROUP BY 1, 2`

// WatchInteractions implements recs.Store; Weight carries the raw ratio.
func (s *Store) WatchInteractions(ctx context.Context, historySince, eventSince time.Time) ([]recs.Interaction, error) {
	rows, err := s.Query(ctx, watchInteractionsSQL, historySince, eventSince)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (recs.Interaction, error) {
		var x recs.Interaction
		err := row.Scan(&x.UserID, &x.ItemID, &x.Weight)
		return x, err
	})
}

// ReplaceReleaseSimilar implements recs.Store with SyncReleaseSimilar and
// recs.ReleaseSimilarEpsilon.
func (s *Store) ReplaceReleaseSimilar(ctx context.Context, rows []recs.ReleaseSimilarRow, at time.Time) (recs.ReleaseSimilarWrite, error) {
	return s.SyncReleaseSimilar(ctx, rows, at, recs.ReleaseSimilarEpsilon)
}

// RewriteReleaseSimilar swaps the whole release_similar table in one
// transaction: DELETE and COPY. It is the reference SyncReleaseSimilar is
// tested against. At 300k releases it writes ~17.5M rows and leaves as many
// dead ones, so the job does not use it.
func (s *Store) RewriteReleaseSimilar(ctx context.Context, rows []recs.ReleaseSimilarRow, at time.Time) error {
	return pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM release_similar`); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"release_similar"}, []string{"release_id", "similar_id", "score", "reason", "computed_at"},
			pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
				r := rows[i]
				return []any{r.ReleaseID, r.SimilarID, float32(r.Score), r.Reason, at}, nil
			}))
		return err
	})
}

// SyncReleaseSimilar makes release_similar equivalent to rows (see
// recs.SimilarDiff: same pairs, scores within eps, same order per release;
// identical with eps 0) while writing only the rows that differ. It sorts
// rows by release in place.
//
// In one transaction (readers see the old or the new set, never a mix, and
// are never blocked) it streams the current rows in release order, plans
// each release against its new rows, COPYs the planned changes into a temp
// table and applies them with one DELETE and one INSERT … ON CONFLICT DO
// UPDATE. Rows left alone keep their computed_at, which nothing reads.
func (s *Store) SyncReleaseSimilar(ctx context.Context, rows []recs.ReleaseSimilarRow, at time.Time, eps float64) (recs.ReleaseSimilarWrite, error) {
	slices.SortFunc(rows, func(a, b recs.ReleaseSimilarRow) int {
		switch {
		case a.ReleaseID < b.ReleaseID:
			return -1
		case a.ReleaseID > b.ReleaseID:
			return 1
		}
		return 0
	})
	d := &recs.SimilarDiff{Eps: eps}
	err := pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		d.Upsert, d.UpsertSrc, d.Delete, d.Write = nil, nil, nil, recs.ReleaseSimilarWrite{}
		i := 0
		// next plans the releases with only new rows before id, then id.
		next := func(id int64, stored []recs.StoredSimilar) {
			for i < len(rows) && rows[i].ReleaseID < id {
				j := i + 1
				for j < len(rows) && rows[j].ReleaseID == rows[i].ReleaseID {
					j++
				}
				d.Release(rows[i].ReleaseID, rows[i:j], nil)
				i = j
			}
			j := i
			for j < len(rows) && rows[j].ReleaseID == id {
				j++
			}
			if j > i || len(stored) > 0 {
				d.Release(id, rows[i:j], stored)
			}
			i = j
		}
		q, err := tx.Query(ctx, `SELECT release_id, similar_id, reason, score FROM release_similar ORDER BY release_id`)
		if err != nil {
			return err
		}
		cur := int64(-1)
		var stored []recs.StoredSimilar
		reasons := map[string]string{}
		for q.Next() {
			var id int64
			var x recs.StoredSimilar
			var reason []byte
			if err := q.Scan(&id, &x.SimilarID, &reason, &x.Score); err != nil {
				q.Close()
				return err
			}
			// Four reasons: intern them instead of allocating per row.
			r, ok := reasons[string(reason)]
			if !ok {
				r = string(reason)
				reasons[r] = r
			}
			x.Reason = r
			if id != cur {
				if cur >= 0 {
					next(cur, stored)
				}
				cur, stored = id, stored[:0]
			}
			stored = append(stored, x)
		}
		q.Close()
		if err := q.Err(); err != nil {
			return err
		}
		if cur >= 0 {
			next(cur, stored)
		}
		next(math.MaxInt64, nil)
		if len(d.Upsert)+len(d.Delete) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE release_similar_changes (
			release_id bigint NOT NULL, similar_id bigint NOT NULL, reason text NOT NULL, score real
		) ON COMMIT DROP`); err != nil {
			return err
		}
		// score NULL marks a delete.
		n := len(d.Upsert)
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"release_similar_changes"}, []string{"release_id", "similar_id", "reason", "score"},
			pgx.CopyFromSlice(n+len(d.Delete), func(k int) ([]any, error) {
				if k < n {
					r := d.Upsert[k]
					return []any{r.ReleaseID, r.SimilarID, r.Reason, float32(r.Score)}, nil
				}
				r := d.Delete[k-n]
				return []any{r.ReleaseID, r.SimilarID, r.Reason, nil}, nil
			})); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM release_similar t USING release_similar_changes c
			WHERE c.score IS NULL AND t.release_id=c.release_id AND t.reason=c.reason AND t.similar_id=c.similar_id`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO release_similar (release_id, similar_id, score, reason, computed_at)
			SELECT release_id, similar_id, score, reason, $1 FROM release_similar_changes WHERE score IS NOT NULL
			ON CONFLICT (release_id, reason, similar_id) DO UPDATE SET score=EXCLUDED.score, computed_at=EXCLUDED.computed_at`, at)
		return err
	})
	return d.Write, err
}

// ReplacePerformerSimilar implements recs.Store with SyncPerformerSimilar
// and recs.PerformerSimilarEpsilon.
func (s *Store) ReplacePerformerSimilar(ctx context.Context, rows []recs.PerformerMatch, at time.Time) (recs.ReleaseSimilarWrite, error) {
	return s.SyncPerformerSimilar(ctx, rows, at, recs.PerformerSimilarEpsilon)
}

// RewritePerformerSimilar swaps the whole performer_similar table: DELETE
// and COPY in one transaction. It is the reference SyncPerformerSimilar is
// tested against; the job does not use it (~2.4M rows written and as many
// left dead every night at full scale).
func (s *Store) RewritePerformerSimilar(ctx context.Context, rows []recs.PerformerMatch, at time.Time) error {
	return pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM performer_similar`); err != nil {
			return err
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"performer_similar"}, []string{"performer_id", "similar_id", "score", "evidence", "computed_at"},
			pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
				r := rows[i]
				ev, err := json.Marshal(r.Evidence)
				return []any{r.PerformerID, r.SimilarID, float32(r.Score), ev, at}, err
			}))
		return err
	})
}

// SyncPerformerSimilar makes performer_similar equivalent to rows (see
// recs.PerformerSimilarDiff: same pairs, scores within eps, same order per
// performer, evidence accepted by recs.SameEvidence; identical with eps 0)
// while writing only the rows that differ. It sorts rows by performer in
// place. It works like SyncReleaseSimilar: one transaction, the current rows
// streamed in performer order, the changes applied from a temp table with
// one DELETE and one INSERT … ON CONFLICT DO UPDATE.
func (s *Store) SyncPerformerSimilar(ctx context.Context, rows []recs.PerformerMatch, at time.Time, eps float64) (recs.ReleaseSimilarWrite, error) {
	slices.SortFunc(rows, func(a, b recs.PerformerMatch) int { return cmpInt64(a.PerformerID, b.PerformerID) })
	var d *recs.PerformerSimilarDiff
	err := pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		d = &recs.PerformerSimilarDiff{Eps: eps}
		i := 0
		// next plans the performers with only new rows before id, then id.
		next := func(id int64, stored []recs.StoredPerformerSimilar) {
			for i < len(rows) && rows[i].PerformerID < id {
				j := i + 1
				for j < len(rows) && rows[j].PerformerID == rows[i].PerformerID {
					j++
				}
				d.Performer(rows[i].PerformerID, rows[i:j], nil)
				i = j
			}
			j := i
			for j < len(rows) && rows[j].PerformerID == id {
				j++
			}
			if j > i || len(stored) > 0 {
				d.Performer(id, rows[i:j], stored)
			}
			i = j
		}
		q, err := tx.Query(ctx, `SELECT performer_id, similar_id, score, evidence::text FROM performer_similar ORDER BY performer_id`)
		if err != nil {
			return err
		}
		cur := int64(-1)
		var stored []recs.StoredPerformerSimilar
		for q.Next() {
			var id int64
			var x recs.StoredPerformerSimilar
			var ev []byte
			if err := q.Scan(&id, &x.SimilarID, &x.Score, &ev); err != nil {
				q.Close()
				return err
			}
			x.EvidenceOK = decodeEvidence(ev, &x.Evidence)
			if id != cur {
				if cur >= 0 {
					next(cur, stored)
				}
				cur, stored = id, stored[:0]
			}
			stored = append(stored, x)
		}
		q.Close()
		if err := q.Err(); err != nil {
			return err
		}
		if cur >= 0 {
			next(cur, stored)
		}
		next(math.MaxInt64, nil)
		if len(d.Upsert)+len(d.Delete) == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE performer_similar_changes (
			performer_id bigint NOT NULL, similar_id bigint NOT NULL, score real, evidence jsonb
		) ON COMMIT DROP`); err != nil {
			return err
		}
		// score NULL marks a delete.
		n := len(d.Upsert)
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"performer_similar_changes"}, []string{"performer_id", "similar_id", "score", "evidence"},
			pgx.CopyFromSlice(n+len(d.Delete), func(k int) ([]any, error) {
				if k < n {
					r := d.Upsert[k]
					ev, err := json.Marshal(r.Evidence)
					return []any{r.PerformerID, r.SimilarID, float32(r.Score), ev}, err
				}
				r := d.Delete[k-n]
				return []any{r.PerformerID, r.SimilarID, nil, nil}, nil
			})); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM performer_similar t USING performer_similar_changes c
			WHERE c.score IS NULL AND t.performer_id=c.performer_id AND t.similar_id=c.similar_id`); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO performer_similar (performer_id, similar_id, score, evidence, computed_at)
			SELECT performer_id, similar_id, score, evidence, $1 FROM performer_similar_changes WHERE score IS NOT NULL
			ON CONFLICT (performer_id, similar_id) DO UPDATE SET score=EXCLUDED.score, evidence=EXCLUDED.evidence, computed_at=EXCLUDED.computed_at`, at)
		return err
	})
	if d == nil {
		return recs.ReleaseSimilarWrite{}, err
	}
	return d.Write, err
}

// decodeEvidence decodes stored performer_similar evidence strictly: false
// for bad JSON or keys Evidence does not have, so the row is rewritten.
func decodeEvidence(b []byte, ev *recs.Evidence) bool {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(ev) == nil && !dec.More()
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// trendingInputsSQL counts per published release, window [$2,$3) and
// previous window [$1,$2): event plays (distinct play_id with a
// play_request) and completed plays (play_end completed or progress 100 in
// the same play), and watch_history users (completed = progress ≥ 90%).
// Plays and completions take the larger of the two sources, so the board
// works before events are wired up and does not double count after.
const trendingInputsSQL = `
WITH req AS (
  SELECT q.release_id, q.play_id, q.ts_server >= $2 AS cur,
    EXISTS (SELECT 1 FROM events d WHERE d.play_id=q.play_id AND d.ts_server >= $1 AND d.ts_server < $3
      AND ((d.event='play_end' AND d.props->>'reason'='completed') OR (d.event='progress' AND d.props->>'milestone'='100'))) AS done
  FROM events q
  WHERE q.event='play_request' AND q.ts_server >= $1 AND q.ts_server < $3 AND q.release_id IS NOT NULL AND q.play_id IS NOT NULL
), ev AS (
  SELECT release_id,
    count(DISTINCT play_id) FILTER (WHERE cur) AS plays,
    count(DISTINCT play_id) FILTER (WHERE NOT cur) AS prev,
    count(DISTINCT play_id) FILTER (WHERE cur AND done) AS done
  FROM req GROUP BY 1
), wh AS (
  SELECT h.release_id,
    count(*) FILTER (WHERE h.watched_at >= $2) AS plays,
    count(*) FILTER (WHERE h.watched_at < $2) AS prev,
    count(*) FILTER (WHERE h.watched_at >= $2 AND h.progress_seconds >= 0.9 * COALESCE(NULLIF(h.duration_seconds,0), r.runtime_minutes::int*60, 2147483647)) AS done
  FROM watch_history h JOIN releases r ON r.id=h.release_id
  WHERE h.watched_at >= $1 AND h.watched_at < $3
  GROUP BY 1
), ids AS (SELECT release_id FROM ev UNION SELECT release_id FROM wh)
SELECT r.id,
  GREATEST(COALESCE(ev.plays,0), COALESCE(wh.plays,0))::int,
  GREATEST(COALESCE(ev.prev,0), COALESCE(wh.prev,0))::int,
  GREATEST(COALESCE(ev.done,0), COALESCE(wh.done,0))::int
FROM ids JOIN releases r ON r.id=ids.release_id AND r.status='published' AND r.merged_into_id IS NULL
LEFT JOIN ev ON ev.release_id=r.id
LEFT JOIN wh ON wh.release_id=r.id`

// TrendingInputs implements recs.Store.
func (s *Store) TrendingInputs(ctx context.Context, prevFrom, from, to time.Time) ([]recs.TrendInput, error) {
	rows, err := s.Query(ctx, trendingInputsSQL, prevFrom, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (recs.TrendInput, error) {
		var x recs.TrendInput
		err := row.Scan(&x.ID, &x.Plays, &x.PrevPlays, &x.Completed)
		return x, err
	})
}

// PerformerHeatInputs sums the release inputs over each active performer's
// works and adds follows created in the window.
func (s *Store) PerformerHeatInputs(ctx context.Context, prevFrom, from, to time.Time) ([]recs.HeatInput, error) {
	rows, err := s.Query(ctx, heatSQL, prevFrom, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (recs.HeatInput, error) {
		var x recs.HeatInput
		err := row.Scan(&x.ID, &x.Plays, &x.PrevPlays, &x.NewFollows)
		return x, err
	})
}

// heatSQL wraps trendingInputsSQL (columns id, plays, prev, done).
var heatSQL = `WITH rel(id, plays, prev, done) AS (` + trendingInputsSQL + `
), pl AS (
  SELECT rp.performer_id, sum(rel.plays)::int AS plays, sum(rel.prev)::int AS prev
  FROM rel JOIN release_performers rp ON rp.release_id=rel.id
  GROUP BY 1
), fo AS (
  SELECT performer_id, count(*)::int AS n FROM follows WHERE created_at >= $2 AND created_at < $3 GROUP BY 1
), pids AS (SELECT performer_id FROM pl UNION SELECT performer_id FROM fo)
SELECT p.id, COALESCE(pl.plays,0), COALESCE(pl.prev,0), COALESCE(fo.n,0)
FROM pids JOIN performers p ON p.id=pids.performer_id AND p.status='active' AND p.merged_into_id IS NULL
LEFT JOIN pl ON pl.performer_id=p.id
LEFT JOIN fo ON fo.performer_id=p.id`

// ReplaceTrending swaps one period's board; prev_rank comes from the board
// it replaces.
func (s *Store) ReplaceTrending(ctx context.Context, period string, rows []recs.TrendRow, at time.Time) error {
	return s.replaceBoard(ctx, "trending_snapshots", "release_id", period, rows, at, func(r recs.TrendRow, prev *int) []any {
		return []any{period, r.Rank, r.ID, float32(r.Score), r.Plays, float32(r.Completion), float32(r.Growth), prev, at}
	}, []string{"period", "rank", "release_id", "score", "plays", "completion", "growth", "prev_rank", "computed_at"})
}

// ReplacePerformerHeat swaps one period's performer heat ranking.
func (s *Store) ReplacePerformerHeat(ctx context.Context, period string, rows []recs.TrendRow, at time.Time) error {
	return s.replaceBoard(ctx, "performer_heat_snapshots", "performer_id", period, rows, at, func(r recs.TrendRow, prev *int) []any {
		return []any{period, r.Rank, r.ID, float32(r.Score), r.Plays, r.NewFollows, prev, at}
	}, []string{"period", "rank", "performer_id", "score", "plays", "new_follows", "prev_rank", "computed_at"})
}

func (s *Store) replaceBoard(ctx context.Context, table, idCol, period string, rows []recs.TrendRow, _ time.Time,
	values func(recs.TrendRow, *int) []any, cols []string) error {
	return pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		prev := map[int64]int{}
		r, err := tx.Query(ctx, `SELECT `+idCol+`, rank FROM `+table+` WHERE period=$1`, period)
		if err != nil {
			return err
		}
		for r.Next() {
			var id int64
			var rank int
			if err := r.Scan(&id, &rank); err != nil {
				r.Close()
				return err
			}
			prev[id] = rank
		}
		r.Close()
		if err := r.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE period=$1`, period); err != nil {
			return err
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{table}, cols, pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
			var p *int
			if v, ok := prev[rows[i].ID]; ok {
				p = &v
			}
			return values(rows[i], p), nil
		}))
		return err
	})
}

// ---------------------------------------------------------------- online recall

// RecQuery selects the recall channels of one /api/recs request.
type RecQuery struct {
	Slot   string
	UserID int64 // 0 = visitor
	SeedID int64 // related: the release being watched
	Period string
	// Feedback is whose 推荐反馈 (rec_feedback) applies: the signed-in user
	// even with personalize_recs off (it is an explicit choice, not
	// history), else the visitor's device; zero for none.
	Feedback RecFeedbackOwner
}

// Per-channel recall limits.
const (
	recallSimilar   = 80
	recallPerformer = 20
	recallTrending  = 60
	recallNew       = 40
	recallHistory   = 10
	recallFollowed  = 60
	recallInterests = 60
	watchedLimit    = 5000
	dislikedLimit   = 1000
)

const candidateCols = `release_id, channel, raw, seed_id, seed_title, performer_id, performer_name, label, board_rank`

const publishedR = `r.status='published' AND r.merged_into_id IS NULL`
const activeP = `p.status='active' AND p.merged_into_id IS NULL`

var recallSQL = map[string]string{
	"similar": `SELECT similar_id, reason, score::float8, 0::bigint, ''::text, 0::bigint, ''::text, ''::text, 0
		FROM release_similar WHERE release_id=@seed ORDER BY score DESC LIMIT ` + itoa(recallSimilar),
	"seed_performer": `SELECT w.release_id, 'same_performer', 1.0/w.rn, 0::bigint, ''::text, p.id, p.canonical_name, ''::text, 0
		FROM (SELECT performer_id FROM release_performers WHERE release_id=@seed ORDER BY is_primary DESC, billing_order NULLS LAST, performer_id LIMIT 3) sp
		JOIN performers p ON p.id=sp.performer_id AND ` + activeP + `
		CROSS JOIN LATERAL (SELECT rp.release_id, row_number() OVER (ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC) AS rn
			FROM release_performers rp JOIN releases r ON r.id=rp.release_id AND ` + publishedR + `
			WHERE rp.performer_id=sp.performer_id AND rp.release_id<>@seed
			ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC LIMIT ` + itoa(recallPerformer) + `) w`,
	"history_similar": `SELECT s.similar_id, s.reason, s.score::float8 / h.k, h.release_id, h.title, 0::bigint, ''::text, ''::text, 0
		FROM (SELECT wh.release_id, r.canonical_title AS title, row_number() OVER (ORDER BY wh.watched_at DESC, wh.release_id) AS k
			FROM watch_history wh JOIN releases r ON r.id=wh.release_id AND ` + publishedR + `
			WHERE wh.user_id=@user ORDER BY wh.watched_at DESC, wh.release_id LIMIT ` + itoa(recallHistory) + `) h
		CROSS JOIN LATERAL (SELECT similar_id, reason, score FROM release_similar WHERE release_id=h.release_id ORDER BY score DESC LIMIT 15) s`,
	"history_performer": `SELECT w.release_id, 'same_performer', 1.0/(w.rn * h.k), h.release_id, h.title, p.id, p.canonical_name, ''::text, 0
		FROM (SELECT wh.release_id, r.canonical_title AS title, row_number() OVER (ORDER BY wh.watched_at DESC, wh.release_id) AS k
			FROM watch_history wh JOIN releases r ON r.id=wh.release_id AND ` + publishedR + `
			WHERE wh.user_id=@user ORDER BY wh.watched_at DESC, wh.release_id LIMIT 5) h
		CROSS JOIN LATERAL (SELECT performer_id FROM release_performers WHERE release_id=h.release_id ORDER BY is_primary DESC, billing_order NULLS LAST, performer_id LIMIT 1) sp
		JOIN performers p ON p.id=sp.performer_id AND ` + activeP + `
		CROSS JOIN LATERAL (SELECT rp.release_id, row_number() OVER (ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC) AS rn
			FROM release_performers rp JOIN releases r ON r.id=rp.release_id AND ` + publishedR + `
			WHERE rp.performer_id=sp.performer_id
			ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC LIMIT 10) w`,
	"followed_new": `SELECT w.release_id, 'followed_new', 1.0/w.rn, 0::bigint, ''::text, p.id, p.canonical_name, ''::text, 0
		FROM follows f JOIN performers p ON p.id=f.performer_id AND ` + activeP + `
		CROSS JOIN LATERAL (SELECT rp.release_id, row_number() OVER (ORDER BY r.release_date DESC, r.id DESC) AS rn
			FROM release_performers rp JOIN releases r ON r.id=rp.release_id AND ` + publishedR + `
			WHERE rp.performer_id=f.performer_id AND r.release_date >= current_date - 120 AND r.release_date <= current_date
			ORDER BY r.release_date DESC, r.id DESC LIMIT 5) w
		WHERE f.user_id=@user LIMIT ` + itoa(recallFollowed),
	// Cold start: releases tagged with the user's onboarding interests, only
	// while the user has fewer than recs.ColdStartHistory watched releases.
	// More matching interests first, then newer.
	"interests": `SELECT x.release_id, 'interests', x.m + 1.0/x.rn, 0::bigint, ''::text, 0::bigint, ''::text, x.label, 0
		FROM (SELECT t.release_id, count(*)::float8 AS m, min(t.tag) AS label,
				row_number() OVER (ORDER BY count(*) DESC, max(COALESCE(r.release_date, DATE '0001-01-01')) DESC, t.release_id DESC) AS rn
			FROM user_interests ui
			JOIN release_tags t ON t.normalized_tag=ui.tag_slug
			JOIN releases r ON r.id=t.release_id AND ` + publishedR + ` AND COALESCE(r.release_date, DATE '0001-01-01') <= current_date
			WHERE ui.user_id=@user
			  AND (SELECT count(*) FROM (SELECT 1 FROM watch_history WHERE user_id=@user LIMIT ` + itoa(recs.ColdStartHistory) + `) h) < ` + itoa(recs.ColdStartHistory) + `
			GROUP BY t.release_id ORDER BY rn LIMIT ` + itoa(recallInterests) + `) x`,
	"trending": `SELECT t.release_id, 'trending', t.score::float8, 0::bigint, ''::text, 0::bigint, ''::text, t.period, t.rank
		FROM trending_snapshots t WHERE t.period=@period ORDER BY t.rank LIMIT ` + itoa(recallTrending),
	"new": `SELECT r.id, 'new', 1.0/row_number() OVER (ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC), 0::bigint, ''::text, 0::bigint, ''::text, ''::text, 0
		FROM releases r WHERE ` + publishedR + ` AND COALESCE(r.release_date, DATE '0001-01-01') <= current_date
		ORDER BY COALESCE(r.release_date, DATE '0001-01-01') DESC, r.id DESC LIMIT ` + itoa(recallNew),
}

// RecallChannels lists the recall branches of a query, in a stable order.
func RecallChannels(q RecQuery) []string {
	var out []string
	if q.SeedID > 0 {
		out = append(out, "similar", "seed_performer")
	}
	if q.UserID > 0 && q.Slot != recs.SlotRelated {
		out = append(out, "history_similar", "history_performer", "followed_new", "interests")
	}
	return append(out, "trending", "new")
}

// RecallResult is everything the ranker needs besides release infos.
type RecallResult struct {
	Candidates      []recs.Candidate
	Watched         map[int64]bool
	RequirePlayable bool // some playback source exists
	// ColdStart: a signed-in user with fewer than recs.ColdStartHistory
	// watched releases; Interests are their onboarding tags (normalized).
	ColdStart bool
	Interests []string
	// Feedback is the owner's active 推荐反馈 (zero without an owner).
	Feedback recs.Feedback
}

// Recall runs the recall branches, the watched list and the "any playback
// source" check in one batch round trip.
func (s *Store) Recall(ctx context.Context, q RecQuery) (RecallResult, error) {
	var res RecallResult
	if q.Period == "" {
		q.Period = recs.PeriodWeek
	}
	parts := []string{}
	for _, ch := range RecallChannels(q) {
		parts = append(parts, "("+recallSQL[ch]+")")
	}
	args := pgx.NamedArgs{"seed": q.SeedID, "user": q.UserID, "period": q.Period}
	b := &pgx.Batch{}
	b.Queue(`SELECT `+candidateCols+` FROM (`+strings.Join(parts, " UNION ALL ")+`) c(`+candidateCols+`)`, args)
	b.Queue(`SELECT EXISTS (SELECT 1 FROM playback_sources)`)
	if q.UserID > 0 {
		// Watched, plus releases the user gave 踩 (migration 040): both are
		// filtered out of every slot.
		b.Queue(`(SELECT release_id FROM watch_history WHERE user_id=$1 ORDER BY watched_at DESC LIMIT `+itoa(watchedLimit)+`)
			UNION ALL (SELECT release_id FROM release_reactions WHERE user_id=$1 AND value=-1 ORDER BY updated_at DESC LIMIT `+itoa(dislikedLimit)+`)`, q.UserID)
		b.Queue(`SELECT (SELECT count(*) FROM (SELECT 1 FROM watch_history WHERE user_id=$1 LIMIT $2) h) < $2,
			COALESCE((SELECT array_agg(tag_slug ORDER BY tag_slug) FROM user_interests WHERE user_id=$1), '{}')`, q.UserID, recs.ColdStartHistory)
	}
	fbKey := q.Feedback.Key()
	if fbKey != "" {
		b.Queue(recFeedbackSQL, fbKey)
	}
	br := s.SendBatch(ctx, b)
	defer br.Close()
	rows, err := br.Query()
	if err != nil {
		return res, err
	}
	res.Candidates, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (recs.Candidate, error) {
		var c recs.Candidate
		err := row.Scan(&c.ReleaseID, &c.Channel, &c.Raw, &c.SeedID, &c.SeedTitle, &c.PerformerID, &c.PerformerName, &c.Label, &c.BoardRank)
		return c, err
	})
	if err != nil {
		return res, err
	}
	if err = br.QueryRow().Scan(&res.RequirePlayable); err != nil {
		return res, err
	}
	res.Watched = map[int64]bool{}
	if q.UserID > 0 {
		rows, err := br.Query()
		if err != nil {
			return res, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return res, err
		}
		for _, id := range ids {
			res.Watched[id] = true
		}
		if err = br.QueryRow().Scan(&res.ColdStart, &res.Interests); err != nil {
			return res, err
		}
	}
	if fbKey != "" {
		rows, err := br.Query()
		if err != nil {
			return res, err
		}
		if res.Feedback, err = scanRecFeedback(rows); err != nil {
			return res, err
		}
	}
	return res, br.Close()
}

// releaseRatingAggSQL aggregates the active votes of release id (n, w, ws:
// community.RatingAggregate). Migration 058's ratings_active_release covers
// it, so it is an index-only scan rather than a heap read per vote.
func releaseRatingAggSQL(id string) string {
	return `SELECT count(*)::int AS n, COALESCE(sum(weight),0)::float8 AS w, COALESCE(sum(weight*score),0)::float8 AS ws
			FROM ratings WHERE release_id=` + id + ` AND status='active'`
}

// RecInfos hydrates published, unmerged releases: display fields, primary
// performer, playability (an active or degraded source), whether a source
// has audio or subtitles in lang, the public rating and, with seed, the
// first tag shared with the seed. Missing ids (held, merged, draft) are
// simply absent.
func (s *Store) RecInfos(ctx context.Context, ids []int64, seed int64, lang string) (map[int64]recs.Info, error) {
	out := make(map[int64]recs.Info, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	base, _, _ := strings.Cut(lang, "-")
	prior, err := s.RatingPrior(ctx) // cached
	if err != nil {
		return nil, err
	}
	rows, err := s.Query(ctx, `SELECT r.id, r.slug, r.canonical_title, COALESCE(r.runtime_minutes,0)::int, r.release_date,
		COALESCE(r.maker_name,''), COALESCE(r.series_name,''),
		COALESCE(pp.id,0), COALESCE(pp.slug,''), COALESCE(pp.canonical_name,''),
		EXISTS (SELECT 1 FROM playback_sources ps WHERE ps.release_id=r.id AND ps.status IN ('active','degraded')),
		(@lang <> '' AND EXISTS (SELECT 1 FROM playback_sources ps WHERE ps.release_id=r.id AND ps.status IN ('active','degraded')
			AND (@lang = ANY(ps.audio_langs || ps.subtitle_langs) OR @base = ANY(ps.audio_langs || ps.subtitle_langs)))),
		rt.n, rt.w, rt.ws,
		COALESCE((SELECT min(t.tag) FROM release_tags t WHERE @seed > 0 AND t.release_id=r.id
			AND t.normalized_tag IN (SELECT normalized_tag FROM release_tags WHERE release_id=@seed)), ''),
		COALESCE((SELECT array_agg(t.normalized_tag ORDER BY t.normalized_tag) FROM release_tags t WHERE t.release_id=r.id), '{}')
		FROM releases r
		LEFT JOIN LATERAL (SELECT p.id, p.slug, p.canonical_name FROM release_performers rp
			JOIN performers p ON p.id=rp.performer_id AND `+activeP+`
			WHERE rp.release_id=r.id ORDER BY rp.is_primary DESC, rp.billing_order NULLS LAST, p.id LIMIT 1) pp ON true
		CROSS JOIN LATERAL (`+releaseRatingAggSQL("r.id")+`) rt
		WHERE r.id = ANY(@ids) AND `+publishedR,
		pgx.NamedArgs{"ids": ids, "seed": seed, "lang": lang, "base": base})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var x recs.Info
		var agg community.RatingAggregate
		if err := rows.Scan(&x.ReleaseID, &x.Slug, &x.Title, &x.RuntimeMinutes, &x.ReleaseDate, &x.Maker, &x.Series,
			&x.PerformerID, &x.PerformerSlug, &x.PerformerName, &x.Playable, &x.HasLang,
			&agg.Count, &agg.WeightSum, &agg.ScoreSum, &x.SharedTag, &x.Tags); err != nil {
			return nil, err
		}
		x.Rating = community.DisplayScore(agg, prior)
		out[x.ReleaseID] = x
	}
	return out, rows.Err()
}

func itoa(n int) string { return strconv.Itoa(n) }
