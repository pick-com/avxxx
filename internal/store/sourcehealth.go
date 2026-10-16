package store

// PostgreSQL side of the playback source health job (internal/audit/
// sourcehealth.go): the 24 h event aggregation, per-run results in
// source_health_runs, automatic status changes, the R-SOURCE audit query,
// the admin list and the dashboard summary.

import (
	"avxxx/internal/audit"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ audit.SourceHealthStore = (*Store)(nil)

// JobKindSourceHealth is jobs.kind for source health runs.
const JobKindSourceHealth = audit.JobKindSourceHealth

// sourceHealthSamplesSQL counts, per playback source, over ts_server in
// [$1, $2):
//   - attempts: distinct play_id with a play_request for the source;
//   - playable: of those, plays with a first_frame for the same source whose
//     first first_frame (by ts_client) is not preceded by a play_end with
//     reason=error in the same play;
//   - ff_ok: playable plays whose first first_frame elapsed_ms ≤ $3;
//   - rebuffers: rebuffer events for the source;
//   - switches_away: automatic source_switch events from the source.
//
// Ordering inside a play uses ts_client: one device's clock is consistent
// within a play, while ts_server depends on batching.
const sourceHealthSamplesSQL = `
WITH req AS (
  SELECT DISTINCT source_id, play_id FROM events
  WHERE event='play_request' AND ts_server>=$1 AND ts_server<$2 AND source_id IS NOT NULL AND play_id IS NOT NULL
), ff AS (
  SELECT DISTINCT ON (source_id, play_id) source_id, play_id, ts_client,
    CASE WHEN props->>'elapsed_ms' ~ '^[0-9]+$' THEN (props->>'elapsed_ms')::bigint END AS elapsed_ms
  FROM events
  WHERE event='first_frame' AND ts_server>=$1 AND ts_server<$2 AND source_id IS NOT NULL AND play_id IS NOT NULL
  ORDER BY source_id, play_id, ts_client, event_id
), fatal AS (
  SELECT play_id, min(ts_client) AS ts_client FROM events
  WHERE event='play_end' AND props->>'reason'='error' AND ts_server>=$1 AND ts_server<$2 AND play_id IS NOT NULL
  GROUP BY play_id
), plays AS (
  SELECT r.source_id,
    (ff.play_id IS NOT NULL AND (fatal.ts_client IS NULL OR fatal.ts_client>=ff.ts_client)) AS playable,
    ff.elapsed_ms
  FROM req r
  LEFT JOIN ff ON ff.source_id=r.source_id AND ff.play_id=r.play_id
  LEFT JOIN fatal ON fatal.play_id=r.play_id
), agg AS (
  SELECT source_id, count(*) AS attempts, count(*) FILTER (WHERE playable) AS playable,
    count(*) FILTER (WHERE playable AND elapsed_ms<=$3) AS ff_ok
  FROM plays GROUP BY source_id
), reb AS (
  SELECT source_id, count(*) AS n FROM events
  WHERE event='rebuffer' AND ts_server>=$1 AND ts_server<$2 AND source_id IS NOT NULL
  GROUP BY source_id
), sw AS (
  SELECT (props->>'from_source_id')::bigint AS source_id, count(*) AS n FROM events
  WHERE event='source_switch' AND ts_server>=$1 AND ts_server<$2 AND props->>'mode'='auto'
    AND props->>'from_source_id' ~ '^[0-9]{1,18}$'
  GROUP BY 1
)
SELECT ps.id, ps.release_id, COALESCE(r.canonical_title,''), ps.provider, ps.label, ps.status, ps.health_score,
  COALESCE(agg.attempts,0), COALESCE(agg.playable,0), COALESCE(agg.ff_ok,0), COALESCE(reb.n,0), COALESCE(sw.n,0)
FROM playback_sources ps
JOIN releases r ON r.id=ps.release_id
LEFT JOIN agg ON agg.source_id=ps.id
LEFT JOIN reb ON reb.source_id=ps.id
LEFT JOIN sw ON sw.source_id=ps.id
ORDER BY ps.id`

// SourceHealthSamples implements audit.SourceHealthStore.
func (s *Store) SourceHealthSamples(ctx context.Context, start, end time.Time, firstFrameOKMS int64) ([]audit.SourceSample, error) {
	rows, err := s.Query(ctx, sourceHealthSamplesSQL, start, end, firstFrameOKMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.SourceSample
	for rows.Next() {
		var x audit.SourceSample
		var health float32
		if err = rows.Scan(&x.SourceID, &x.ReleaseID, &x.ReleaseTitle, &x.Provider, &x.Label, &x.Status, &health,
			&x.Attempts, &x.Playable, &x.FirstFrameOK, &x.Rebuffers, &x.SwitchesAway); err != nil {
			return nil, err
		}
		x.HealthScore = float64(health)
		out = append(out, x)
	}
	return out, rows.Err()
}

// ApplySourceHealth implements audit.SourceHealthStore. It runs in one
// transaction and locks the source row, so a concurrent admin status change
// either wins (the automatic change is skipped) or comes after.
func (s *Store) ApplySourceHealth(ctx context.Context, o audit.SourceOutcome) (bool, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var current string
	if err = tx.QueryRow(ctx, `SELECT status FROM playback_sources WHERE id=$1 FOR UPDATE`, o.SourceID).Scan(&current); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // deleted since sampling
		}
		return false, err
	}
	after := current
	changed := false
	if !o.Insufficient && o.Score != nil {
		if o.StatusChanged() && current == o.Status {
			after, changed = o.StatusAfter, true
		}
		if _, err = tx.Exec(ctx, `UPDATE playback_sources SET health_score=$2,health_checked_at=$3,status=$4,
			updated_at=CASE WHEN $5 THEN now() ELSE updated_at END WHERE id=$1`,
			o.SourceID, float32(*o.Score/100), o.WindowEnd, after, changed); err != nil {
			return false, err
		}
	}
	var score *float32
	if o.Score != nil {
		v := float32(*o.Score)
		score = &v
	}
	if _, err = tx.Exec(ctx, `INSERT INTO source_health_runs(source_id,job_id,window_start,window_end,attempts,playable,ff_ok,
		rebuffers,switches_away,score,band,insufficient_data,status_before,status_after)
		VALUES($1,NULLIF($2,0),$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		o.SourceID, o.JobID, o.WindowStart, o.WindowEnd, o.Attempts, o.Playable, o.FirstFrameOK, o.Rebuffers, o.SwitchesAway,
		score, o.Band, o.Insufficient, current, after); err != nil {
		return false, err
	}
	if changed {
		r := audit.Rates(o.Attempts, o.Playable, o.FirstFrameOK, o.Rebuffers)
		afterJSON, err := json.Marshal(map[string]any{"status": after, "score": *o.Score, "band": o.Band, "attempts": o.Attempts,
			"playable_rate": r.Playable, "ff_ok_rate": r.FirstFrameOK, "window_start": o.WindowStart, "window_end": o.WindowEnd})
		if err != nil {
			return false, err
		}
		reason := "source health " + strconv.FormatFloat(*o.Score, 'f', 2, 64) + ": " + current + " → " + after
		if _, err = tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
			VALUES('playback_source',$1,'status_change',jsonb_build_object('status',$2::text,'health_score',$3::real),$4::jsonb,$5,$6)`,
			o.SourceID, current, float32(o.HealthScore), string(afterJSON), reason, audit.HealthActor); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit(ctx)
}

// ResolveSourceIssue implements audit.SourceHealthStore.
func (s *Store) ResolveSourceIssue(ctx context.Context, sourceID int64, note string) error {
	_, err := s.Exec(ctx, `UPDATE audit_issues SET status='fixed',resolved_at=now(),resolution_note=$3
		WHERE rule=$1 AND entity_type='playback_source' AND entity_id=$2 AND status='open'`, string(audit.RuleSourceHealth), sourceID, note)
	return err
}

// PruneSourceHealthRuns implements audit.SourceHealthStore.
func (s *Store) PruneSourceHealthRuns(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.Exec(ctx, `DELETE FROM source_health_runs WHERE window_end<$1`, before)
	return tag.RowsAffected(), err
}

// SourceHealthIssues implements audit.Store (R-SOURCE).
func (s *Store) SourceHealthIssues(ctx context.Context) ([]audit.SourceHealthFacts, bool, error) {
	var hasRuns bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_health_runs)`).Scan(&hasRuns); err != nil {
		return nil, false, err
	}
	rows, err := s.Query(ctx, `SELECT ps.id,ps.release_id,COALESCE(r.canonical_title,''),ps.provider,ps.label,ps.status,
		lr.score,COALESCE(lr.band,''),COALESCE(lr.attempts,0),lr.window_end
		FROM playback_sources ps
		JOIN releases r ON r.id=ps.release_id
		JOIN LATERAL (SELECT actor FROM review_events WHERE entity_type='playback_source' AND entity_id=ps.id AND action='status_change'
			ORDER BY id DESC LIMIT 1) ev ON ev.actor=$1
		LEFT JOIN LATERAL (SELECT score,band,attempts,window_end FROM source_health_runs
			WHERE source_id=ps.id AND NOT insufficient_data ORDER BY window_end DESC,id DESC LIMIT 1) lr ON true
		WHERE ps.status IN ('degraded','disabled') ORDER BY ps.id`, audit.HealthActor)
	if err != nil {
		return nil, hasRuns, err
	}
	defer rows.Close()
	var out []audit.SourceHealthFacts
	for rows.Next() {
		var f audit.SourceHealthFacts
		var score *float32
		if err = rows.Scan(&f.SourceID, &f.ReleaseID, &f.ReleaseTitle, &f.Provider, &f.Label, &f.Status, &score, &f.Band, &f.Attempts, &f.WindowEnd); err != nil {
			return nil, hasRuns, err
		}
		f.Score = f64ptr(score)
		out = append(out, f)
	}
	return out, hasRuns, rows.Err()
}

func f64ptr(v *float32) *float64 {
	if v == nil {
		return nil
	}
	x := float64(*v)
	return &x
}

// ---------------------------------------------------------------- admin

// sourceBandSQL is the band of a playback_sources row, matching
// audit.ScorePoints + audit.HealthBand.
const sourceBandSQL = `CASE WHEN ps.health_checked_at IS NULL THEN 'insufficient_data'
	WHEN floor(ps.health_score::float8*100+0.001)<60 THEN 'severe'
	WHEN floor(ps.health_score::float8*100+0.001)<80 THEN 'degraded'
	WHEN floor(ps.health_score::float8*100+0.001)<95 THEN 'healthy' ELSE 'excellent' END`

// Trend length bounds for SourceHealthList.
const (
	DefaultHealthTrend = 12
	MaxHealthTrend     = 144 // 24 h of 10-minute runs
)

// SourceHealthList lists playback sources worst first (never-scored sources
// last), keyset paginated, each with its latest run and a trend of its last
// runs (newest first).
func (s *Store) SourceHealthList(ctx context.Context, f audit.SourceHealthFilter) ([]audit.SourceHealthItem, string, error) {
	limit := pageLimit(f.Limit)
	trend := f.Trend
	if trend <= 0 {
		trend = DefaultHealthTrend
	}
	if trend > MaxHealthTrend {
		trend = MaxHealthTrend
	}
	if f.Band != "" && !audit.ValidSourceBand(f.Band) {
		return nil, "", invalidQuery("unknown band")
	}
	var hasCursor bool
	var curGroup int
	var curScore float32
	var curID int64
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		if c.Sort != "source_health" {
			return nil, "", invalidQuery("cursor does not match this list")
		}
		g, sc, ok := strings.Cut(c.Key, ":")
		v, err2 := strconv.ParseFloat(sc, 32)
		if !ok || (g != "0" && g != "1") || err2 != nil {
			return nil, "", invalidQuery("malformed cursor")
		}
		hasCursor, curGroup, curScore, curID = true, int(g[0]-'0'), float32(v), c.ID
	}
	rows, err := s.Query(ctx, `SELECT ps.id,ps.release_id,r.slug,r.canonical_title,ps.kind,ps.provider,ps.label,ps.status,
		ps.health_score,ps.health_checked_at,`+sourceBandSQL+`,(ps.health_checked_at IS NULL)::int AS grp,
		COALESCE((SELECT json_agg(json_build_object('window_start',h.window_start,'window_end',h.window_end,'attempts',h.attempts,
			'playable',h.playable,'ff_ok',h.ff_ok,'rebuffers',h.rebuffers,'switches_away',h.switches_away,'score',h.score,
			'insufficient_data',h.insufficient_data) ORDER BY h.window_end DESC,h.id DESC)
			FROM (SELECT * FROM source_health_runs WHERE source_id=ps.id ORDER BY window_end DESC,id DESC LIMIT $1) h),'[]'::json)
		FROM playback_sources ps JOIN releases r ON r.id=ps.release_id
		WHERE ($2='' OR `+sourceBandSQL+`=$2)
		  AND (NOT $3 OR ((ps.health_checked_at IS NULL)::int,ps.health_score,ps.id)>($4,$5::real,$6))
		ORDER BY (ps.health_checked_at IS NULL)::int,ps.health_score,ps.id LIMIT $7`,
		trend, f.Band, hasCursor, curGroup, curScore, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	type runJSON struct {
		WindowStart  time.Time `json:"window_start"`
		WindowEnd    time.Time `json:"window_end"`
		Attempts     int64     `json:"attempts"`
		Playable     int64     `json:"playable"`
		FFOK         int64     `json:"ff_ok"`
		Rebuffers    int64     `json:"rebuffers"`
		SwitchesAway int64     `json:"switches_away"`
		Score        *float64  `json:"score"`
		Insufficient bool      `json:"insufficient_data"`
	}
	out := []audit.SourceHealthItem{}
	var lastHealth []float32
	var lastGroup []int
	for rows.Next() {
		var x audit.SourceHealthItem
		var health float32
		var grp int
		var raw []byte
		if err = rows.Scan(&x.SourceID, &x.ReleaseID, &x.ReleaseSlug, &x.ReleaseTitle, &x.Kind, &x.Provider, &x.Label, &x.Status,
			&health, &x.CheckedAt, &x.Band, &grp, &raw); err != nil {
			return nil, "", err
		}
		var runs []runJSON
		if err = json.Unmarshal(raw, &runs); err != nil {
			return nil, "", err
		}
		if x.CheckedAt != nil {
			p := audit.ScorePoints(float64(health))
			x.Score = &p
		}
		x.Trend = make([]audit.SourceHealthPoint, 0, len(runs))
		for i, h := range runs {
			if i == 0 {
				x.Latest = &audit.SourceHealthSampleView{WindowStart: h.WindowStart, WindowEnd: h.WindowEnd, Attempts: h.Attempts,
					Playable: h.Playable, FirstFrameOK: h.FFOK, Rebuffers: h.Rebuffers, SwitchesAway: h.SwitchesAway,
					HealthRates: audit.Rates(h.Attempts, h.Playable, h.FFOK, h.Rebuffers), Score: h.Score, Insufficient: h.Insufficient}
			}
			x.Trend = append(x.Trend, audit.SourceHealthPoint{WindowEnd: h.WindowEnd, Score: h.Score, Attempts: h.Attempts, Insufficient: h.Insufficient})
		}
		out = append(out, x)
		lastHealth = append(lastHealth, health)
		lastGroup = append(lastGroup, grp)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		i := limit - 1
		next = EncodeCursor(Cursor{Sort: "source_health",
			Key: strconv.Itoa(lastGroup[i]) + ":" + strconv.FormatFloat(float64(lastHealth[i]), 'g', -1, 32), ID: out[i].SourceID})
	}
	return out, next, nil
}

// sourceHealthSummary fills the dashboard's 片源健康分 block.
func (s *Store) sourceHealthSummary(ctx context.Context, d *audit.Dashboard) error {
	sh := &d.SourceHealth
	rows, err := s.Query(ctx, `SELECT ps.status,`+sourceBandSQL+`,count(*) FROM playback_sources ps GROUP BY 1,2`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var st, band string
		var n int64
		if err = rows.Scan(&st, &band, &n); err != nil {
			rows.Close()
			return err
		}
		sh.Sources += n
		sh.ByStatus[st] += n
		sh.ByBand[band] += n
		if band == audit.BandInsufficient {
			sh.InsufficientData += n
		} else {
			sh.Scored += n
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	// Pool every source's latest run in the last 24 hours.
	var attempts, playable, ffOK int64
	if err = s.QueryRow(ctx, `SELECT COALESCE(sum(attempts),0),COALESCE(sum(playable),0),COALESCE(sum(ff_ok),0),max(window_end)
		FROM (SELECT DISTINCT ON (source_id) attempts,playable,ff_ok,window_end FROM source_health_runs
			WHERE window_end>now()-interval '24 hours' ORDER BY source_id,window_end DESC,id DESC) x`).
		Scan(&attempts, &playable, &ffOK, &sh.LastRunAt); err != nil {
		return err
	}
	sh.SetPooled(attempts, playable, ffOK)
	return nil
}
