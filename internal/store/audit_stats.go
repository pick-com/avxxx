package store

// R-STATS inputs and repairs (internal/audit/stats.go). Every recount reads
// the same sources, with the same code, as the job that maintains the
// counter: review_likes for reviews.likes, releaseCurveInputs +
// community.ComputeCurve for danmaku_curves, SearchEventCounts +
// search.AggregateDaily for search_query_daily, TrendingInputs /
// PerformerHeatInputs for the trending boards. Raw events are only read.

import (
	"avxxx/internal/audit"
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"avxxx/internal/search"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReviewLikeCounts implements audit.StatsStore.
func (s *Store) ReviewLikeCounts(ctx context.Context, afterID int64, limit int) ([]audit.CounterValue, error) {
	rows, err := s.Query(ctx, `SELECT rv.id, rv.likes, (SELECT count(*) FROM review_likes l WHERE l.review_id=rv.id)
		FROM reviews rv WHERE rv.id>$1 ORDER BY rv.id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (audit.CounterValue, error) {
		var v audit.CounterValue
		return v, r.Scan(&v.EntityID, &v.Stored, &v.Recomputed)
	})
}

// FixReviewLikes implements audit.StatsStore: one statement (one
// transaction) per batch. A row whose likes changed since it was read
// (SetReviewLike locks the review and moves the counter with the like) is
// left alone.
func (s *Store) FixReviewLikes(ctx context.Context, vals []audit.CounterValue) (int, error) {
	if len(vals) == 0 {
		return 0, nil
	}
	ids, olds, news := make([]int64, len(vals)), make([]int64, len(vals)), make([]int64, len(vals))
	for i, v := range vals {
		ids[i], olds[i], news[i] = v.EntityID, v.Stored, v.Recomputed
	}
	tag, err := s.Exec(ctx, `UPDATE reviews rv SET likes=x.n
		FROM unnest($1::bigint[],$2::bigint[],$3::bigint[]) AS x(id,old,n)
		WHERE rv.id=x.id AND rv.likes=x.old AND x.n>=0`, ids, olds, news)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// DanmakuCurveReleases implements audit.StatsStore. It walks releases in id
// order and probes the danmaku / curve indexes, so a full pass is linear in
// the catalog.
func (s *Store) DanmakuCurveReleases(ctx context.Context, afterID int64, limit int) ([]int64, error) {
	rows, err := s.Query(ctx, `SELECT r.id FROM releases r WHERE r.id>$1
		AND (EXISTS (SELECT 1 FROM danmaku d WHERE d.release_id=r.id) OR EXISTS (SELECT 1 FROM danmaku_curves c WHERE c.release_id=r.id))
		AND NOT EXISTS (SELECT 1 FROM danmaku_curve_queue q WHERE q.release_id=r.id)
		ORDER BY r.id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// DanmakuValidCounts implements audit.StatsStore.
func (s *Store) DanmakuValidCounts(ctx context.Context, releaseID int64) ([]audit.CounterValue, error) {
	points, durations, err := s.releaseCurveInputs(ctx, releaseID)
	if err != nil {
		return nil, err
	}
	stored := map[string]int64{}
	rows, err := s.Query(ctx, `SELECT version_key, valid_count FROM danmaku_curves WHERE release_id=$1`, releaseID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			rows.Close()
			return nil, err
		}
		stored[k] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(points))
	for k := range points {
		keys = append(keys, k)
	}
	for k := range stored {
		if _, ok := points[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := make([]audit.CounterValue, 0, len(keys))
	for _, k := range keys {
		var valid int64
		if pts := points[k]; len(pts) > 0 {
			valid = int64(community.ComputeCurve(pts, durations[k]).Valid)
		}
		out = append(out, audit.CounterValue{EntityID: releaseID, Key: k, Stored: stored[k], Recomputed: valid})
	}
	return out, nil
}

// RebuildDanmakuCurves implements audit.StatsStore.
func (s *Store) RebuildDanmakuCurves(ctx context.Context, releaseID int64) error {
	_, err := s.RecomputeReleaseCurves(ctx, releaseID)
	return err
}

// OldestEvent implements audit.StatsStore (events_event_ts covers it).
func (s *Store) OldestEvent(ctx context.Context, event string) (time.Time, bool, error) {
	var t *time.Time
	if err := s.QueryRow(ctx, `SELECT min(ts_server) FROM events WHERE event=$1`, event).Scan(&t); err != nil || t == nil {
		return time.Time{}, false, err
	}
	return *t, true, nil
}

var searchFields = []string{"searches", "zero_results", "clicks", "first_page_clicks", "rescued"}

func searchFieldValues(r search.DailyRow) [5]int64 {
	return [5]int64{int64(r.Searches), int64(r.ZeroResults), int64(r.Clicks), int64(r.FirstPageClicks), int64(r.Rescued)}
}

func (s *Store) searchDayFromEvents(ctx context.Context, day time.Time) ([]search.DailyRow, error) {
	day = day.UTC().Truncate(24 * time.Hour)
	raw, err := s.SearchEventCounts(ctx, day, day.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	return search.AggregateDaily(raw), nil
}

// SearchDailyCounts implements audit.StatsStore.
func (s *Store) SearchDailyCounts(ctx context.Context, day time.Time) ([]audit.CounterValue, error) {
	fresh, err := s.searchDayFromEvents(ctx, day)
	if err != nil {
		return nil, err
	}
	type pair struct{ stored, fresh [5]int64 }
	acc := map[string]*pair{}
	get := func(k string) *pair {
		p := acc[k]
		if p == nil {
			p = &pair{}
			acc[k] = p
		}
		return p
	}
	for _, r := range fresh {
		get(r.QNorm + "|" + r.Lang).fresh = searchFieldValues(r)
	}
	rows, err := s.Query(ctx, `SELECT q_norm,lang,searches,zero_results,clicks,first_page_clicks,rescued
		FROM search_query_daily WHERE day=$1::date`, day.UTC().Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r search.DailyRow
		if err := rows.Scan(&r.QNorm, &r.Lang, &r.Searches, &r.ZeroResults, &r.Clicks, &r.FirstPageClicks, &r.Rescued); err != nil {
			rows.Close()
			return nil, err
		}
		get(r.QNorm + "|" + r.Lang).stored = searchFieldValues(r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]audit.CounterValue, 0, len(keys)*len(searchFields))
	for _, k := range keys {
		p := acc[k]
		for i, f := range searchFields {
			out = append(out, audit.CounterValue{Key: k + "|" + f, Stored: p.stored[i], Recomputed: p.fresh[i]})
		}
	}
	return out, nil
}

// RebuildSearchDay implements audit.StatsStore.
func (s *Store) RebuildSearchDay(ctx context.Context, day time.Time) error {
	rows, err := s.searchDayFromEvents(ctx, day)
	if err != nil {
		return err
	}
	day = day.UTC().Truncate(24 * time.Hour)
	return s.ReplaceSearchDaily(ctx, day, day, rows)
}

const searchSanityBroken = `(zero_results>searches OR clicks>searches OR rescued>searches OR first_page_clicks>clicks)`

// SearchDailySanity implements audit.StatsStore.
func (s *Store) SearchDailySanity(ctx context.Context, before time.Time, limit int) (int, int, []audit.StatsViolation, error) {
	b := before.UTC().Format(time.DateOnly)
	var checked, broken int
	if err := s.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE `+searchSanityBroken+`)
		FROM search_query_daily WHERE day<$1::date`, b).Scan(&checked, &broken); err != nil {
		return 0, 0, nil, err
	}
	if broken == 0 {
		return checked, 0, nil, nil
	}
	rows, err := s.Query(ctx, `SELECT day,q_norm,lang,searches,zero_results,clicks,first_page_clicks,rescued
		FROM search_query_daily WHERE day<$1::date AND `+searchSanityBroken+` ORDER BY day,q_norm,lang LIMIT $2`, b, limit)
	if err != nil {
		return 0, 0, nil, err
	}
	ex, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (audit.StatsViolation, error) {
		var d time.Time
		var x search.DailyRow
		if err := r.Scan(&d, &x.QNorm, &x.Lang, &x.Searches, &x.ZeroResults, &x.Clicks, &x.FirstPageClicks, &x.Rescued); err != nil {
			return audit.StatsViolation{}, err
		}
		return audit.StatsViolation{Key: d.Format(time.DateOnly) + "|" + x.QNorm + "|" + x.Lang,
			Problem: fmt.Sprintf("searches %d, zero_results %d, clicks %d, first_page_clicks %d, rescued %d",
				x.Searches, x.ZeroResults, x.Clicks, x.FirstPageClicks, x.Rescued)}, nil
	})
	return checked, broken, ex, err
}

func boardTable(board string) (table, idCol string, err error) {
	switch board {
	case audit.BoardTrending:
		return "trending_snapshots", "release_id", nil
	case audit.BoardHeat:
		return "performer_heat_snapshots", "performer_id", nil
	}
	return "", "", fmt.Errorf("unknown board %q", board)
}

// BoardSnapshot implements audit.StatsStore.
func (s *Store) BoardSnapshot(ctx context.Context, board, period string) (audit.BoardSnapshot, bool, error) {
	snap := audit.BoardSnapshot{Board: board, Period: period, Plays: map[int64]int64{}}
	table, idCol, err := boardTable(board)
	if err != nil {
		return snap, false, err
	}
	w, ok := recs.PeriodWindow(period)
	if !ok {
		return snap, false, fmt.Errorf("unknown period %q", period)
	}
	snap.Window = w
	rows, err := s.Query(ctx, `SELECT `+idCol+`, plays, computed_at FROM `+table+` WHERE period=$1`, period)
	if err != nil {
		return snap, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, plays int64
		var at time.Time
		if err := rows.Scan(&id, &plays, &at); err != nil {
			return snap, false, err
		}
		snap.Plays[id] = plays
		if at.After(snap.ComputedAt) {
			snap.ComputedAt = at
		}
	}
	return snap, len(snap.Plays) > 0, rows.Err()
}

// BoardRecount implements audit.StatsStore with the trending job's inputs
// (recs.Runner.RunTrending) for the window ending at at.
func (s *Store) BoardRecount(ctx context.Context, board, period string, at time.Time) (map[int64]int64, error) {
	w, ok := recs.PeriodWindow(period)
	if !ok {
		return nil, fmt.Errorf("unknown period %q", period)
	}
	from := at.Add(-w)
	prevFrom := from.Add(-w)
	out := map[int64]int64{}
	switch board {
	case audit.BoardTrending:
		in, err := s.TrendingInputs(ctx, prevFrom, from, at)
		if err != nil {
			return nil, err
		}
		for _, x := range in {
			out[x.ID] = int64(x.Plays)
		}
	case audit.BoardHeat:
		in, err := s.PerformerHeatInputs(ctx, prevFrom, from, at)
		if err != nil {
			return nil, err
		}
		for _, x := range in {
			out[x.ID] = int64(x.Plays)
		}
	default:
		return nil, fmt.Errorf("unknown board %q", board)
	}
	return out, nil
}

// releaseCurveCheckSQL classifies each release_curves timeline (one viewers
// row per release and version key); problem is NULL when it is consistent.
const releaseCurveCheckSQL = `
SELECT vw.release_id, vw.version_key,
  CASE
    WHEN EXISTS (SELECT 1 FROM unnest(vw.data) x WHERE x < 0 OR x > vw.valid_plays) THEN 'viewers outside 0..valid_plays'
    WHEN EXISTS (SELECT 1 FROM unnest(rp.data, vw.data) u(r, v) WHERE r > v) THEN 'replays above viewers'
    WHEN EXISTS (SELECT 1 FROM unnest(rt.data) x WHERE x < 0 OR x > 10000) THEN 'retention outside 0..10000'
  END AS problem
FROM release_curves vw
LEFT JOIN release_curves rp ON rp.release_id=vw.release_id AND rp.version_key=vw.version_key AND rp.kind='replays'
LEFT JOIN release_curves rt ON rt.release_id=vw.release_id AND rt.version_key=vw.version_key AND rt.kind='retention'
WHERE vw.kind='viewers'`

// ReleaseCurveSanity implements audit.StatsStore.
func (s *Store) ReleaseCurveSanity(ctx context.Context, limit int) (int, int, []audit.StatsViolation, error) {
	var checked, broken int
	if err := s.QueryRow(ctx, `SELECT count(*), count(problem) FROM (`+releaseCurveCheckSQL+`) c`).Scan(&checked, &broken); err != nil {
		return 0, 0, nil, err
	}
	if broken == 0 {
		return checked, 0, nil, nil
	}
	rows, err := s.Query(ctx, `SELECT release_id, version_key, problem FROM (`+releaseCurveCheckSQL+`) c
		WHERE problem IS NOT NULL ORDER BY release_id, version_key LIMIT $1`, limit)
	if err != nil {
		return 0, 0, nil, err
	}
	ex, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (audit.StatsViolation, error) {
		var v audit.StatsViolation
		return v, r.Scan(&v.EntityID, &v.Key, &v.Problem)
	})
	return checked, broken, ex, err
}
