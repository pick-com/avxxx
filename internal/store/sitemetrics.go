package store

// AdminDashboard site KPIs: the 'site_metrics' job's reads and writes of
// site_daily_metrics (migration 047, internal/sitemetrics) and the rows
// behind GET /api/admin/site-metrics.

import (
	"avxxx/internal/sitemetrics"
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobKindSiteMetrics is jobs.kind of the site metrics job.
const JobKindSiteMetrics = sitemetrics.JobKind

var _ sitemetrics.Store = (*Store)(nil)

// SiteMetricsDays implements sitemetrics.Store.
func (s *Store) SiteMetricsDays(ctx context.Context) (int, error) {
	var n int
	err := s.QueryRow(ctx, `SELECT count(*) FROM site_daily_metrics`).Scan(&n)
	return n, err
}

// rebuildSiteDaysSQL recomputes the rows of the UTC days in $1 from events
// in [$2, $3) (the span of those days). Follow-up events of a play are
// looked for up to a day after its request (bounded for partition pruning),
// like the watch statistics job.
const rebuildSiteDaysSQL = `
WITH d AS (SELECT DISTINCT unnest($1::date[]) AS day),
req AS (
  SELECT DISTINCT ON (q.play_id) q.play_id, q.ts_server
  FROM events q
  WHERE q.event = 'play_request' AND q.play_id IS NOT NULL AND q.ts_server >= $2 AND q.ts_server < $3
  ORDER BY q.play_id, q.ts_server
),
plays AS (
  SELECT (r.ts_server AT TIME ZONE 'UTC')::date AS day,
    LEAST(COALESCE(e.watched, hb.secs, 0), $4::bigint) AS secs,
    COALESCE(e.completed, false) OR m.done IS NOT NULL AS done
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
  ) m ON true
),
pagg AS (
  SELECT day, count(*) AS plays, sum(secs)::bigint AS secs, count(*) FILTER (WHERE done) AS completed
  FROM plays GROUP BY day
),
active AS (
  SELECT (ts_server AT TIME ZONE 'UTC')::date AS day,
    count(DISTINCT COALESCE('u:' || user_id::text, 'd:' || device_id, 's:' || session_id)) AS dau,
    count(DISTINCT user_id) AS signed_in
  FROM events
  WHERE ts_server >= $2 AND ts_server < $3
  GROUP BY 1
)
INSERT INTO site_daily_metrics AS t (day, plays, dau, signed_in, watch_seconds, completed_plays, computed_at)
SELECT d.day, COALESCE(p.plays, 0), COALESCE(a.dau, 0), COALESCE(a.signed_in, 0), COALESCE(p.secs, 0), COALESCE(p.completed, 0), now()
FROM d LEFT JOIN pagg p ON p.day = d.day LEFT JOIN active a ON a.day = d.day
ON CONFLICT (day) DO UPDATE SET plays = EXCLUDED.plays, dau = EXCLUDED.dau, signed_in = EXCLUDED.signed_in,
  watch_seconds = EXCLUDED.watch_seconds, completed_plays = EXCLUDED.completed_plays, computed_at = EXCLUDED.computed_at
RETURNING t.day, t.plays, t.dau, t.signed_in, t.watch_seconds, t.completed_plays, t.computed_at`

func scanSiteDay(r pgx.Rows) (sitemetrics.Day, error) {
	var d sitemetrics.Day
	err := r.Scan(&d.Day, &d.Plays, &d.DAU, &d.SignedIn, &d.WatchSeconds, &d.CompletedPlays, &d.ComputedAt)
	d.Day = d.Day.UTC()
	return d, err
}

// RebuildSiteDays implements sitemetrics.Store.
func (s *Store) RebuildSiteDays(ctx context.Context, days []time.Time) ([]sitemetrics.Day, error) {
	if len(days) == 0 {
		return nil, nil
	}
	from, to := days[0], days[0]
	for _, d := range days {
		if d.Before(from) {
			from = d
		}
		if d.After(to) {
			to = d
		}
	}
	return collectRows(ctx, s, rebuildSiteDaysSQL, []any{days, from, to.AddDate(0, 0, 1), sitemetrics.MaxPlaySeconds}, scanSiteDay)
}

// SiteDailyMetrics returns the stored rows of the days in [from, to], oldest first.
func (s *Store) SiteDailyMetrics(ctx context.Context, from, to time.Time) ([]sitemetrics.Day, error) {
	return collectRows(ctx, s, `SELECT day, plays, dau, signed_in, watch_seconds, completed_plays, computed_at
		FROM site_daily_metrics WHERE day >= $1::date AND day <= $2::date ORDER BY day`, []any{from, to}, scanSiteDay)
}
