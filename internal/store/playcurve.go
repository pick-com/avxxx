package store

// PostgreSQL side of the playback replay peak / retention curves (PRD 14.3,
// internal/playcurve, migrations/021_release_curves.sql).

import (
	"avxxx/internal/community"
	"avxxx/internal/playcurve"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ playcurve.Store = (*Store)(nil)

// JobKindReleaseCurves is jobs.kind for the hourly curve job.
const JobKindReleaseCurves = playcurve.JobKind

// CurveWatermark returns the end of the last successful curve run.
func (s *Store) CurveWatermark(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.QueryRow(ctx, `SELECT watermark FROM release_curve_state WHERE id`).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return t, err
}

// SetCurveWatermark records the end of a successful curve run.
func (s *Store) SetCurveWatermark(ctx context.Context, t time.Time) error {
	_, err := s.Exec(ctx, `INSERT INTO release_curve_state(id,watermark) VALUES(true,$1)
		ON CONFLICT (id) DO UPDATE SET watermark=GREATEST(release_curve_state.watermark,EXCLUDED.watermark)`, t)
	return err
}

// curvePlayEvents are the events the curve job reads for a play.
var curvePlayEvents = []string{"play_request", "heartbeat", "play_end", "seek", "pause", "resume",
	"rate_change", "quality_change", "source_switch", "peak_click"}

// ReleasesWithNewPlays implements playcurve.Store. heartbeat, play_end and
// seek carry no release_id, so they are attributed through the play's
// play_request.
func (s *Store) ReleasesWithNewPlays(ctx context.Context, since, until, windowStart time.Time) ([]int64, error) {
	rows, err := s.Query(ctx, `SELECT DISTINCT r.release_id FROM events e
		JOIN events r ON r.play_id=e.play_id AND r.event='play_request' AND r.release_id IS NOT NULL AND r.ts_server>=$3
		WHERE e.ts_server>=$1 AND e.ts_server<$2 AND e.play_id IS NOT NULL
		  AND e.event IN ('play_request','heartbeat','play_end','seek')
		  AND EXISTS (SELECT 1 FROM releases rel WHERE rel.id=r.release_id)
		ORDER BY 1`, since, until, windowStart)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[int64])
}

// curveVersions resolves the release's sources to timelines: version keys
// by source id and the duration of each timeline in ms.
func (s *Store) curveVersions(ctx context.Context, releaseID int64) (map[int64]string, map[string]int, error) {
	var runtime int
	if err := s.QueryRow(ctx, `SELECT COALESCE(runtime_minutes,0) FROM releases WHERE id=$1`, releaseID).Scan(&runtime); err != nil {
		return nil, nil, err
	}
	rows, err := s.Query(ctx, `SELECT id,version_kind,duration_seconds,priority,status FROM playback_sources WHERE release_id=$1`, releaseID)
	if err != nil {
		return nil, nil, err
	}
	srcs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PlaybackSource, error) {
		var x PlaybackSource
		return x, r.Scan(&x.ID, &x.VersionKind, &x.DurationSeconds, &x.Priority, &x.Status)
	})
	if err != nil {
		return nil, nil, err
	}
	keys, durations := CurveTimelines(srcs, runtime)
	return keys, durations, nil
}

// CurveTimelines is the database-free part of curveVersions: main, high
// quality and dub sources share "main" (duration = the reference duration);
// an alternate version (stored as alt, or > 2% off the reference) has
// "alt:<id>" with its own duration.
func CurveTimelines(srcs []PlaybackSource, runtimeMinutes int) (map[int64]string, map[string]int) {
	keys := map[int64]string{}
	durations := map[string]int{}
	ref := ReferenceDuration(srcs, runtimeMinutes)
	if ref != nil {
		durations[community.DefaultVersion] = *ref * 1000
	}
	for _, x := range srcs {
		ApplyAltVersionRule(&x, ref)
		if !x.IsAltVersion {
			keys[x.ID] = community.DefaultVersion
			continue
		}
		k := community.AltVersionKey(x.ID)
		keys[x.ID] = k
		if x.DurationSeconds != nil && *x.DurationSeconds > 0 {
			durations[k] = *x.DurationSeconds * 1000
		}
	}
	return keys, durations
}

// ReleasePlays implements playcurve.Store. Sources that no longer exist
// count as the main timeline.
func (s *Store) ReleasePlays(ctx context.Context, releaseID int64, windowStart time.Time) ([]playcurve.Play, map[string]int, error) {
	keys, durations, err := s.curveVersions(ctx, releaseID)
	if err != nil {
		return nil, nil, err
	}
	b := playcurve.Builder{Version: func(sourceID int64) string {
		if k, ok := keys[sourceID]; ok {
			return k
		}
		return community.DefaultVersion
	}}
	rows, err := s.Query(ctx, `WITH p AS (
		  SELECT DISTINCT play_id FROM events
		  WHERE event='play_request' AND release_id=$1 AND ts_server>=$2 AND play_id IS NOT NULL
		)
		SELECT e.play_id, e.event, e.ts_client, e.ts_server, COALESCE(e.user_id,0), COALESCE(e.device_id,''), e.session_id,
		  COALESCE(e.source_id,0), COALESCE(e.props->>'buckets',''),
		  CASE WHEN e.props->>'from_ms' ~ '^[0-9]{1,15}$' THEN (e.props->>'from_ms')::bigint ELSE 0 END,
		  CASE WHEN e.props->>'to_ms' ~ '^[0-9]{1,15}$' THEN (e.props->>'to_ms')::bigint ELSE 0 END,
		  COALESCE(e.props->>'mode',''),
		  CASE WHEN e.props->>'watched_seconds' ~ '^[0-9]{1,12}$' THEN (e.props->>'watched_seconds')::bigint ELSE 0 END
		FROM events e JOIN p USING (play_id)
		WHERE e.ts_server>=$2 AND e.event=ANY($3) AND (e.event<>'play_request' OR e.release_id=$1)`,
		releaseID, windowStart, curvePlayEvents)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r playcurve.Row
		if err := rows.Scan(&r.PlayID, &r.Event, &r.TSClient, &r.TSServer, &r.UserID, &r.DeviceID, &r.SessionID,
			&r.SourceID, &r.Buckets, &r.FromMS, &r.ToMS, &r.Mode, &r.WatchedSeconds); err != nil {
			return nil, nil, err
		}
		b.Add(r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return b.Plays(), durations, nil
}

// SaveReleaseCurves implements playcurve.Store: the release's rows are
// replaced in one transaction.
func (s *Store) SaveReleaseCurves(ctx context.Context, releaseID int64, results []playcurve.Result) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM release_curves WHERE release_id=$1`, releaseID); err != nil {
		return err
	}
	for _, r := range results {
		var dur *int
		if r.DurationMS > 0 {
			dur = &r.DurationMS
		}
		rows := map[string][]int32{
			playcurve.KindViewers:   ints32(r.Viewers[:]),
			playcurve.KindReplays:   ints32(r.Replays[:]),
			playcurve.KindRetention: ints32(r.Retention[:]),
		}
		rows[playcurve.KindReplayPeak] = nil
		if r.ReplayPeak != nil {
			rows[playcurve.KindReplayPeak] = ints16to32(r.ReplayPeak)
		}
		if r.Skips != nil {
			rows[playcurve.KindSkips] = ints32(r.Skips[:])
		}
		for kind, data := range rows {
			if _, err := tx.Exec(ctx, `INSERT INTO release_curves(release_id,version_key,kind,data,valid_plays,duration_ms,computed_at)
				VALUES($1,$2,$3,$4,$5,$6,now())`, releaseID, r.VersionKey, kind, data, r.ValidPlays, dur); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func ints32(v []int) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

func ints16to32(v []int16) []int32 {
	out := make([]int32, len(v))
	for i, x := range v {
		out[i] = int32(x)
	}
	return out
}

// ReplayCurves returns the replay peaks (0–255) of a release by version
// key; timelines without one are absent.
func (s *Store) ReplayCurves(ctx context.Context, releaseID int64) (map[string][]int16, error) {
	rows, err := s.Query(ctx, `SELECT version_key,data FROM release_curves
		WHERE release_id=$1 AND kind='replay_peak' AND data IS NOT NULL`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int16{}
	for rows.Next() {
		var k string
		var data []int32
		if err := rows.Scan(&k, &data); err != nil {
			return nil, err
		}
		c := make([]int16, len(data))
		for i, x := range data {
			c[i] = int16(min(max(x, 0), playcurve.CurveMax))
		}
		out[k] = c
	}
	return out, rows.Err()
}

// ReleaseCurveSet is every stored curve of one timeline.
type ReleaseCurveSet struct {
	VersionKey string
	ValidPlays int
	DurationMS *int
	ComputedAt time.Time
	// Data by kind; a kind with NULL data (replay_peak below the threshold)
	// is present with a nil slice, a missing kind is absent.
	Data map[string][]int
}

// ReleaseCurves returns the stored curves of a release, main timeline first;
// pgx.ErrNoRows for an unknown release.
func (s *Store) ReleaseCurves(ctx context.Context, releaseID int64) ([]ReleaseCurveSet, error) {
	var exists bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM releases WHERE id=$1)`, releaseID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, pgx.ErrNoRows
	}
	rows, err := s.Query(ctx, `SELECT version_key,kind,data,valid_plays,duration_ms,computed_at FROM release_curves
		WHERE release_id=$1 ORDER BY version_key<>'main', version_key, kind`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReleaseCurveSet
	for rows.Next() {
		var k, kind string
		var data []int32
		var set ReleaseCurveSet
		if err := rows.Scan(&k, &kind, &data, &set.ValidPlays, &set.DurationMS, &set.ComputedAt); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].VersionKey != k {
			set.VersionKey = k
			set.Data = map[string][]int{}
			out = append(out, set)
		}
		cur := &out[len(out)-1]
		var v []int
		if data != nil {
			v = make([]int, len(data))
			for i, x := range data {
				v[i] = int(x)
			}
		}
		cur.Data[kind] = v
	}
	return out, rows.Err()
}
