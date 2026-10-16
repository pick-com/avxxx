package store

import (
	"avxxx/internal/audit"
	"avxxx/internal/obs"
	"context"
	"time"
)

// MetricsSnapshot implements obs.DBSource: the queue, crawl, audit and
// playback-health state behind the Prometheus gauges (read-only; served by
// the scheduler only, cached by the collector).
func (s *Store) MetricsSnapshot(ctx context.Context) (obs.DBSnapshot, error) {
	snap := obs.DBSnapshot{
		ExpiredLeases:    map[string]int64{},
		OldestDueAge:     map[string]float64{},
		FailedLastHour:   map[string]int64{},
		CheckpointLastOK: map[int64]time.Time{},
		// R-AGE is always present so increase() sees the first hold.
		Holds: map[string]int64{string(audit.RuleAge): 0},
	}
	rows, err := s.Query(ctx, `SELECT kind,
			CASE WHEN status='running' THEN 'running' WHEN available_at<=now() THEN 'due' ELSE 'scheduled' END,
			count(*),
			count(*) FILTER (WHERE status='running' AND lease_until<now()),
			COALESCE(extract(epoch FROM now()-min(available_at) FILTER (WHERE status='pending' AND available_at<=now())),0)::float8
		FROM jobs WHERE status IN ('pending','running') GROUP BY 1,2`)
	if err != nil {
		return snap, err
	}
	for rows.Next() {
		var j obs.JobCount
		var expired int64
		var age float64
		if err = rows.Scan(&j.Kind, &j.State, &j.N, &expired, &age); err != nil {
			rows.Close()
			return snap, err
		}
		snap.Jobs = append(snap.Jobs, j)
		snap.ExpiredLeases[j.Kind] += expired
		if j.State == "due" {
			snap.OldestDueAge[j.Kind] = age
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return snap, err
	}
	if err = eachRow(ctx, s, `SELECT kind,count(*) FROM jobs WHERE status='failed' AND finished_at>now()-interval '1 hour' GROUP BY 1`,
		func(scan func(...any) error) error {
			var k string
			var n int64
			err := scan(&k, &n)
			snap.FailedLastHour[k] = n
			return err
		}); err != nil {
		return snap, err
	}
	if err = eachRow(ctx, s, `SELECT c.source_id,max(c.last_success_at) FROM crawl_checkpoints c JOIN sources s ON s.id=c.source_id
			WHERE s.enabled AND c.last_success_at IS NOT NULL GROUP BY 1`,
		func(scan func(...any) error) error {
			var id int64
			var t time.Time
			err := scan(&id, &t)
			snap.CheckpointLastOK[id] = t
			return err
		}); err != nil {
		return snap, err
	}
	if err = s.QueryRow(ctx, `SELECT count(*) FROM sources WHERE enabled AND blocked_until>now()`).Scan(&snap.BlockedSources); err != nil {
		return snap, err
	}
	if err = eachRow(ctx, s, `SELECT rule,severity,count(*) FROM audit_issues WHERE status='open' GROUP BY 1,2`,
		func(scan func(...any) error) error {
			var is obs.IssueCount
			err := scan(&is.Rule, &is.Severity, &is.N)
			snap.OpenIssues = append(snap.OpenIssues, is)
			return err
		}); err != nil {
		return snap, err
	}
	// HoldRelease writes reason "R-AGE: ..."; the rule prefix is the label.
	if err = eachRow(ctx, s, `SELECT COALESCE(substring(reason FROM '^(R-[A-Z0-9-]*[A-Z0-9])'),'other'),count(*)
			FROM review_events WHERE entity_type='release' AND action='hold' GROUP BY 1`,
		func(scan func(...any) error) error {
			var r string
			var n int64
			err := scan(&r, &n)
			snap.Holds[r] += n
			return err
		}); err != nil {
		return snap, err
	}
	if err = eachRow(ctx, s, `SELECT `+sourceBandSQL+`,ps.status,count(*) FROM playback_sources ps GROUP BY 1,2`,
		func(scan func(...any) error) error {
			var b obs.BandCount
			err := scan(&b.Band, &b.Status, &b.N)
			snap.PlaybackSources = append(snap.PlaybackSources, b)
			return err
		}); err != nil {
		return snap, err
	}
	// Same pool as the AdminDashboard 片源健康分 (sourceHealthSummary).
	var ffOK int64
	h := &snap.Health
	if err = s.QueryRow(ctx, `SELECT COALESCE(sum(attempts),0),COALESCE(sum(playable),0),COALESCE(sum(ff_ok),0),max(window_end)
		FROM (SELECT DISTINCT ON (source_id) attempts,playable,ff_ok,window_end FROM source_health_runs
			WHERE window_end>now()-interval '24 hours' ORDER BY source_id,window_end DESC,id DESC) x`).
		Scan(&h.Attempts, &h.Playable, &ffOK, &h.LastRunAt); err != nil {
		return snap, err
	}
	if v, ok := audit.SourceHealthScore(h.Attempts, h.Playable, ffOK); ok {
		h.Score = &v
	}
	return snap, nil
}

// PlayPairing implements obs.DBSource: of the play_ids with a play_request
// in [from, to), how many also have a play_end (PRD 14.5 配对率).
func (s *Store) PlayPairing(ctx context.Context, from, to time.Time) (obs.Pairing, error) {
	var p obs.Pairing
	err := s.QueryRow(ctx, `WITH req AS (
			SELECT DISTINCT play_id FROM events
			WHERE event='play_request' AND play_id IS NOT NULL AND ts_server>=$1 AND ts_server<$2)
		SELECT count(*), count(*) FILTER (WHERE EXISTS (
			SELECT 1 FROM events e WHERE e.event='play_end' AND e.play_id=req.play_id AND e.ts_server>=$1))
		FROM req`, from, to).Scan(&p.Requests, &p.Paired)
	return p, err
}

// eachRow runs q and calls row for every result row.
func eachRow(ctx context.Context, s *Store, q string, row func(scan func(...any) error) error) error {
	rows, err := s.Query(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := row(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}
