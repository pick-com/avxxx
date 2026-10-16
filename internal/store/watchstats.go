package store

// 观看统计 (WatchStats, PRD 20, tech plan 3.13): the 'watch_stats' /
// 'watch_stats_nightly' jobs' reads and writes of user_watch_daily
// (migration 043, internal/watchstats) and the owner-only summary behind
// GET /api/me/watch-stats.

import (
	"avxxx/internal/model"
	"avxxx/internal/watchstats"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job kinds of the watch statistics jobs.
const (
	JobKindWatchStats        = watchstats.JobKind
	JobKindWatchStatsNightly = watchstats.JobKindNightly
)

var _ watchstats.Store = (*Store)(nil)

// WatchStatsUsers implements watchstats.Store.
func (s *Store) WatchStatsUsers(ctx context.Context, from, to time.Time, days []time.Time, afterID int64, limit int) ([]watchstats.User, error) {
	return collectRows(ctx, s, `WITH c AS (
		  SELECT user_id FROM events
		  WHERE event='play_request' AND ts_server >= $1 AND ts_server < $2 AND user_id > $4
		  UNION
		  SELECT user_id FROM watch_history WHERE watched_at >= $1 AND watched_at < $2 AND user_id > $4
		  UNION
		  SELECT user_id FROM user_watch_daily WHERE day = ANY($3::date[]) AND user_id > $4
		)
		SELECT u.id, COALESCE(st.timezone, '')
		FROM c JOIN users u ON u.id = c.user_id
		LEFT JOIN user_settings st ON st.user_id = u.id
		WHERE u.deletion_requested_at IS NULL AND NOT COALESCE(st.history_paused, false)
		ORDER BY u.id LIMIT $5`, []any{from, to, days, afterID, limit},
		func(r pgx.Rows) (watchstats.User, error) {
			var u watchstats.User
			return u, r.Scan(&u.ID, &u.TZ)
		})
}

// watchPlaysSQL: each play_request in [$2, $3) of the batch users whose
// release is still in their watch history, with play_end.watched_seconds
// (else the time to its last heartbeat) and whether it finished (play_end
// reason=completed or progress milestone 100). Follow-up events are looked
// for up to a day after the request (bounded for partition pruning).
const watchPlaysSQL = `
WITH req AS (
  SELECT DISTINCT ON (q.play_id) q.play_id, q.user_id, q.release_id, q.ts_server
  FROM events q
  WHERE q.event='play_request' AND q.user_id = ANY($1) AND q.ts_server >= $2 AND q.ts_server < $3
    AND q.play_id IS NOT NULL AND q.release_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM watch_history h WHERE h.user_id = q.user_id AND h.release_id = q.release_id)
  ORDER BY q.play_id, q.ts_server
)
SELECT r.user_id, r.release_id, r.ts_server,
  LEAST(COALESCE(e.watched, hb.secs, 0), 2000000000)::int,
  COALESCE(e.completed, false) OR m.done IS NOT NULL
FROM req r
LEFT JOIN LATERAL (
  SELECT max(CASE WHEN x.props->>'watched_seconds' ~ '^[0-9]{1,9}$' THEN (x.props->>'watched_seconds')::bigint END) AS watched,
         bool_or(x.props->>'reason' = 'completed') AS completed
  FROM events x
  WHERE x.play_id = r.play_id AND x.event = 'play_end' AND x.ts_server >= r.ts_server AND x.ts_server < r.ts_server + interval '1 day'
) e ON true
LEFT JOIN LATERAL (
  SELECT GREATEST(0, extract(epoch FROM max(x.ts_server) - r.ts_server))::bigint AS secs
  FROM events x
  WHERE x.play_id = r.play_id AND x.event = 'heartbeat' AND x.ts_server >= r.ts_server AND x.ts_server < r.ts_server + interval '1 day'
) hb ON true
LEFT JOIN LATERAL (
  SELECT 1 AS done FROM events x
  WHERE x.play_id = r.play_id AND x.event = 'progress' AND x.props->>'milestone' = '100'
    AND x.ts_server >= r.ts_server AND x.ts_server < r.ts_server + interval '1 day'
  LIMIT 1
) m ON true`

// WatchStatsInputs implements watchstats.Store.
func (s *Store) WatchStatsInputs(ctx context.Context, userIDs []int64, from, to time.Time) (watchstats.Inputs, error) {
	in := watchstats.Inputs{Meta: watchstats.ReleaseMeta{Performers: map[int64][]int64{}, Tags: map[int64][]string{}}}
	var err error
	in.Plays, err = collectRows(ctx, s, watchPlaysSQL, []any{userIDs, from, to}, func(r pgx.Rows) (watchstats.Play, error) {
		var p watchstats.Play
		return p, r.Scan(&p.UserID, &p.ReleaseID, &p.Start, &p.Seconds, &p.Finished)
	})
	if err != nil {
		return in, err
	}
	in.History, err = collectRows(ctx, s, `SELECT h.user_id, h.release_id, h.watched_at, h.progress_seconds,
		  COALESCE(NULLIF(h.duration_seconds, 0), r.runtime_minutes::int * 60, 0)
		FROM watch_history h JOIN releases r ON r.id = h.release_id
		WHERE h.user_id = ANY($1) AND h.watched_at >= $2 AND h.watched_at < $3`, []any{userIDs, from, to},
		func(r pgx.Rows) (watchstats.HistoryRow, error) {
			var h watchstats.HistoryRow
			return h, r.Scan(&h.UserID, &h.ReleaseID, &h.WatchedAt, &h.Progress, &h.Duration)
		})
	if err != nil {
		return in, err
	}
	seen := map[int64]bool{}
	var rels []int64
	for _, p := range in.Plays {
		if !seen[p.ReleaseID] {
			seen[p.ReleaseID] = true
			rels = append(rels, p.ReleaseID)
		}
	}
	for _, h := range in.History {
		if !seen[h.ReleaseID] {
			seen[h.ReleaseID] = true
			rels = append(rels, h.ReleaseID)
		}
	}
	if len(rels) == 0 {
		return in, nil
	}
	rows, err := s.Query(ctx, `SELECT release_id, performer_id FROM release_performers WHERE release_id = ANY($1)`, rels)
	if err != nil {
		return in, err
	}
	for rows.Next() {
		var rel, p int64
		if err := rows.Scan(&rel, &p); err != nil {
			rows.Close()
			return in, err
		}
		in.Meta.Performers[rel] = append(in.Meta.Performers[rel], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return in, err
	}
	rows, err = s.Query(ctx, `SELECT release_id, normalized_tag FROM release_tags WHERE release_id = ANY($1)`, rels)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		var rel int64
		var tag string
		if err := rows.Scan(&rel, &tag); err != nil {
			return in, err
		}
		in.Meta.Tags[rel] = append(in.Meta.Tags[rel], tag)
	}
	return in, rows.Err()
}

// ReplaceWatchDays implements watchstats.Store.
func (s *Store) ReplaceWatchDays(ctx context.Context, userIDs []int64, days []time.Time, rows []watchstats.Day) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM user_watch_daily WHERE user_id = ANY($1) AND day = ANY($2::date[])`, userIDs, days); err != nil {
		return err
	}
	if len(rows) > 0 {
		b := &pgx.Batch{}
		for _, d := range rows {
			perf, err := json.Marshal(nonNilSlice(d.Performers))
			if err != nil {
				return err
			}
			tags, err := json.Marshal(nonNilSlice(d.Tags))
			if err != nil {
				return err
			}
			hours := make([]int32, 24)
			for i, v := range d.ByHour {
				hours[i] = int32(v)
			}
			b.Queue(`INSERT INTO user_watch_daily(user_id, day, tz, watch_seconds, plays, titles, finished, by_hour, top_performers, top_tags, built_at)
				VALUES($1, $2::date, $3, $4, $5, $6, $7, $8, $9::jsonb, $10::jsonb, now())`,
				d.UserID, d.Day, d.TZ, d.Seconds, d.Plays, d.Titles, d.Finished, hours, string(perf), string(tags))
		}
		if err = tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nonNilSlice[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

// WatchStatsPerformer is one of the most watched performers of a range.
type WatchStatsPerformer struct {
	ID       int64        `json:"-"`
	Slug     string       `json:"slug"`
	Name     string       `json:"name"`
	NameJA   string       `json:"name_ja,omitempty"`
	Seconds  int          `json:"seconds"`
	Portrait *model.Image `json:"portrait,omitempty"`
}

// WatchStatsGenre is one of the most watched tags of a range.
type WatchStatsGenre struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Seconds int    `json:"seconds"`
}

// WatchStats is the GET /api/me/watch-stats body.
type WatchStats struct {
	watchstats.Summary
	Performers []WatchStatsPerformer `json:"performers"`
	Genres     []WatchStatsGenre     `json:"genres"`
	// UpdatedAt is when the newest row was built (null without rows).
	UpdatedAt *time.Time `json:"updated_at"`
}

// WatchStatsFor builds the user's summary of rangeName (watchstats.Ranges)
// ending today in their time zone. Top performers resolve merges and skip
// performers that are no longer public.
func (s *Store) WatchStatsFor(ctx context.Context, userID int64, rangeName string, now time.Time) (WatchStats, error) {
	var tz string
	if err := s.QueryRow(ctx, `SELECT COALESCE((SELECT timezone FROM user_settings WHERE user_id=$1), '')`, userID).Scan(&tz); err != nil {
		return WatchStats{}, err
	}
	loc, tzName := watchstats.Location(tz)
	today := watchstats.DateOf(now, loc)
	first := today.AddDate(0, 0, -(watchstats.StreakDays - 1))
	var updated *time.Time
	rows, err := collectRows(ctx, s, `SELECT day, watch_seconds, plays, titles, finished, by_hour, top_performers, top_tags, built_at
		FROM user_watch_daily WHERE user_id=$1 AND day >= $2::date AND day <= $3::date ORDER BY day`, []any{userID, first, today},
		func(r pgx.Rows) (watchstats.Day, error) {
			var d watchstats.Day
			var hours []int32
			var perf, tags []byte
			var built time.Time
			if err := r.Scan(&d.Day, &d.Seconds, &d.Plays, &d.Titles, &d.Finished, &hours, &perf, &tags, &built); err != nil {
				return d, err
			}
			d.Day = time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, time.UTC)
			for i := 0; i < len(hours) && i < 24; i++ {
				d.ByHour[i] = int(hours[i])
			}
			if err := json.Unmarshal(perf, &d.Performers); err != nil {
				return d, err
			}
			if err := json.Unmarshal(tags, &d.Tags); err != nil {
				return d, err
			}
			if updated == nil || built.After(*updated) {
				b := built
				updated = &b
			}
			return d, nil
		})
	if err != nil {
		return WatchStats{}, err
	}
	sum := watchstats.Summarize(rangeName, rows, today, tzName, len(rows) > 0)
	out := WatchStats{Summary: sum, Performers: []WatchStatsPerformer{}, Genres: []WatchStatsGenre{}, UpdatedAt: updated}

	if len(sum.Performers) > 0 {
		ids := make([]int64, len(sum.Performers))
		secs := map[int64]int{}
		for i, p := range sum.Performers {
			ids[i] = p.ID
			secs[p.ID] = p.Seconds
		}
		type ref struct {
			from int64
			p    WatchStatsPerformer
		}
		refs, err := collectRows(ctx, s, `SELECT p.id, t.id, t.slug, t.canonical_name, COALESCE(t.canonical_name_ja, '')
			FROM performers p JOIN performers t ON t.id = COALESCE(p.merged_into_id, p.id)
			WHERE p.id = ANY($1) AND t.status = 'active' AND t.merged_into_id IS NULL`, []any{ids},
			func(r pgx.Rows) (ref, error) {
				var x ref
				return x, r.Scan(&x.from, &x.p.ID, &x.p.Slug, &x.p.Name, &x.p.NameJA)
			})
		if err != nil {
			return WatchStats{}, err
		}
		byTarget := map[int64]int{}
		for _, x := range refs {
			if i, ok := byTarget[x.p.ID]; ok {
				out.Performers[i].Seconds += secs[x.from]
				continue
			}
			x.p.Seconds = secs[x.from]
			byTarget[x.p.ID] = len(out.Performers)
			out.Performers = append(out.Performers, x.p)
		}
		sortPerformerStats(out.Performers)
	}
	if len(sum.Tags) > 0 {
		slugs := make([]string, len(sum.Tags))
		for i, t := range sum.Tags {
			slugs[i] = t.Tag
		}
		names := map[string]string{}
		rows, err := s.Query(ctx, `SELECT normalized_tag, MIN(tag) FROM release_tags WHERE normalized_tag = ANY($1) GROUP BY normalized_tag`, slugs)
		if err != nil {
			return WatchStats{}, err
		}
		for rows.Next() {
			var slug, name string
			if err := rows.Scan(&slug, &name); err != nil {
				rows.Close()
				return WatchStats{}, err
			}
			names[slug] = name
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return WatchStats{}, err
		}
		for _, t := range sum.Tags {
			name := names[t.Tag]
			if name == "" {
				continue // tag no longer on any release
			}
			out.Genres = append(out.Genres, WatchStatsGenre{Slug: t.Tag, Name: name, Seconds: t.Seconds})
		}
	}
	return out, nil
}

func sortPerformerStats(ps []WatchStatsPerformer) {
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && (ps[j].Seconds > ps[j-1].Seconds || ps[j].Seconds == ps[j-1].Seconds && ps[j].Slug < ps[j-1].Slug); j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
}
