package store

// Read side of the precomputed recommendation tables: trending boards,
// performer heat and performer similarity (migrations/019_recs.sql).

import (
	"avxxx/internal/recs"
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// BoardRow is one trending / heat board row. Releases are hydrated
// separately (RecInfos); performer rows carry slug and name.
type BoardRow struct {
	Rank       int       `json:"rank"`
	PrevRank   *int      `json:"prev_rank"`
	ID         int64     `json:"-"`
	Score      float64   `json:"score"`
	Plays      int       `json:"plays"`
	Completion float64   `json:"completion"`
	NewFollows int       `json:"new_follows"`
	ComputedAt time.Time `json:"-"`
	Slug       string    `json:"-"`
	Name       string    `json:"-"`
}

// TrendingBoard returns rows [offset, offset+limit) of a period's board;
// a tag (release_tags.normalized_tag, the board's genre chips) keeps only
// the board's releases with that tag, with their board-wide rank.
// Releases held or unpublished since the run are dropped by RecInfos.
func (s *Store) TrendingBoard(ctx context.Context, period, tag string, offset, limit int) ([]BoardRow, error) {
	rows, err := s.Query(ctx, `SELECT rank, prev_rank, release_id, score, plays, completion, computed_at
		FROM trending_snapshots ts WHERE period=$1
		AND ($4='' OR EXISTS (SELECT 1 FROM release_tags rt WHERE rt.release_id=ts.release_id AND rt.normalized_tag=$4))
		ORDER BY rank OFFSET $2 LIMIT $3`, period, offset, limit, tag)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (BoardRow, error) {
		var b BoardRow
		var score, comp float32
		err := row.Scan(&b.Rank, &b.PrevRank, &b.ID, &score, &b.Plays, &comp, &b.ComputedAt)
		b.Score, b.Completion = round3(float64(score)), round3(float64(comp))
		return b, err
	})
}

// TrendingTags are the most frequent tags among a period's board releases
// (the genre chips above the board), most releases first.
func (s *Store) TrendingTags(ctx context.Context, period string, limit int) ([]TagCount, error) {
	rows, err := s.Query(ctx, `SELECT rt.normalized_tag, min(rt.tag), count(*)
		FROM trending_snapshots ts JOIN release_tags rt ON rt.release_id=ts.release_id
		WHERE ts.period=$1 GROUP BY 1 ORDER BY 3 DESC, 1 LIMIT $2`, period, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TagCount, error) {
		var t TagCount
		err := row.Scan(&t.Slug, &t.Name, &t.Count)
		return t, err
	})
}

// TagCount is a tag with the number of releases it is on.
type TagCount struct {
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// PerformerHeatBoard returns rows [offset, offset+limit) of a period's
// performer heat ranking, active performers only.
func (s *Store) PerformerHeatBoard(ctx context.Context, period string, offset, limit int) ([]BoardRow, error) {
	rows, err := s.Query(ctx, `SELECT h.rank, h.prev_rank, p.id, h.score, h.plays, h.new_follows, h.computed_at, p.slug, p.canonical_name
		FROM performer_heat_snapshots h JOIN performers p ON p.id=h.performer_id AND `+activeP+`
		WHERE h.period=$1 ORDER BY h.rank OFFSET $2 LIMIT $3`, period, offset, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (BoardRow, error) {
		var b BoardRow
		var score float32
		err := row.Scan(&b.Rank, &b.PrevRank, &b.ID, &score, &b.Plays, &b.NewFollows, &b.ComputedAt, &b.Slug, &b.Name)
		b.Score = round3(float64(score))
		return b, err
	})
}

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

// PerformerRec is one recommended or similar performer.
type PerformerRec struct {
	ID        int64          `json:"id"`
	Slug      string         `json:"slug"`
	Name      string         `json:"name"`
	Score     float64        `json:"score"`
	Match     int            `json:"match_percent"` // PRD 6.1 「92% 相似」
	Evidence  *recs.Evidence `json:"evidence,omitempty"`
	ViaID     int64          `json:"-"`
	ViaName   string         `json:"-"`
	ViaFollow bool           `json:"-"`
}

// PerformerIDBySlug resolves an active, unmerged performer.
func (s *Store) PerformerIDBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := s.QueryRow(ctx, `SELECT id FROM performers p WHERE slug=$1 AND `+activeP, slug).Scan(&id)
	return id, err
}

// SimilarPerformers returns performer_similar neighbours of id, best first.
func (s *Store) SimilarPerformers(ctx context.Context, id int64, offset, limit int) ([]PerformerRec, error) {
	rows, err := s.Query(ctx, `SELECT p.id, p.slug, p.canonical_name, ps.score, ps.evidence
		FROM performer_similar ps JOIN performers p ON p.id=ps.similar_id AND `+activeP+`
		WHERE ps.performer_id=$1 ORDER BY ps.score DESC, p.id OFFSET $2 LIMIT $3`, id, offset, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PerformerRec, error) {
		var x PerformerRec
		var score float32
		var ev recs.Evidence
		err := row.Scan(&x.ID, &x.Slug, &x.Name, &score, &ev)
		x.Score, x.Match, x.Evidence = round3(float64(score)), matchPercent(float64(score)), &ev
		return x, err
	})
}

func matchPercent(score float64) int { return int(score*100 + 0.5) }

// RecommendedPerformers (演员库「为你推荐的演员」, PRD 6.2) aggregates
// performer_similar over the user's followed performers (weight 1) and the
// performers of the last 30 watched releases (0.5), excluding the seeds
// themselves, performers already followed and those marked 不推荐此演员
// (rec_feedback). Via is the seed contributing most; match_percent is the
// best single similarity.
func (s *Store) RecommendedPerformers(ctx context.Context, userID int64, offset, limit int) ([]PerformerRec, error) {
	rows, err := s.Query(ctx, `WITH seeds AS (
		  SELECT performer_id, 1.0::float8 AS w, true AS followed FROM follows WHERE user_id=$1
		  UNION ALL
		  SELECT rp.performer_id, 0.5::float8, false FROM (SELECT release_id FROM watch_history WHERE user_id=$1 ORDER BY watched_at DESC LIMIT 30) h
		  JOIN release_performers rp ON rp.release_id=h.release_id
		), agg AS (
		  SELECT ps.similar_id, sum(ps.score*s.w)::float8 AS score, max(ps.score)::float8 AS best,
		    (array_agg(s.performer_id ORDER BY ps.score*s.w DESC, s.followed DESC, s.performer_id))[1] AS via
		  FROM seeds s JOIN performer_similar ps ON ps.performer_id=s.performer_id
		  GROUP BY 1
		)
		SELECT p.id, p.slug, p.canonical_name, a.score, a.best, vp.id, vp.canonical_name,
		  EXISTS (SELECT 1 FROM follows f WHERE f.user_id=$1 AND f.performer_id=a.via), COALESCE(ps.evidence, '{}'::jsonb)
		FROM agg a JOIN performers p ON p.id=a.similar_id AND `+activeP+`
		JOIN performers vp ON vp.id=a.via
		LEFT JOIN performer_similar ps ON ps.performer_id=a.via AND ps.similar_id=a.similar_id
		WHERE NOT EXISTS (SELECT 1 FROM seeds x WHERE x.performer_id=a.similar_id)
		  AND NOT EXISTS (SELECT 1 FROM rec_feedback rf WHERE rf.owner_key=$4 AND rf.kind='`+recs.FeedbackPerformer+`'
		    AND rf.target_id=a.similar_id AND rf.undone_at IS NULL)
		ORDER BY a.score DESC, p.id OFFSET $2 LIMIT $3`, userID, offset, limit, RecFeedbackOwner{UserID: userID}.Key())
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (PerformerRec, error) {
		var x PerformerRec
		var score, best float64
		var ev recs.Evidence
		err := row.Scan(&x.ID, &x.Slug, &x.Name, &score, &best, &x.ViaID, &x.ViaName, &x.ViaFollow, &ev)
		x.Score, x.Match, x.Evidence = round3(score), matchPercent(best), &ev
		return x, err
	})
}
