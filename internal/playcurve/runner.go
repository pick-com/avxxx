package playcurve

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Store is what the hourly curve job needs from the database.
type Store interface {
	// CurveWatermark returns the end of the last fully successful run (zero
	// when the job never ran).
	CurveWatermark(ctx context.Context) (time.Time, error)
	// SetCurveWatermark records the end of a fully successful run.
	SetCurveWatermark(ctx context.Context, t time.Time) error
	// ReleasesWithNewPlays returns the releases with a play_request,
	// heartbeat, play_end or seek received in [since, until) whose
	// play_request is at or after windowStart.
	ReleasesWithNewPlays(ctx context.Context, since, until, windowStart time.Time) ([]int64, error)
	// ReleasePlays loads every play of a release requested at or after
	// windowStart, with version keys resolved, and the duration of each
	// timeline in ms (missing when unknown).
	ReleasePlays(ctx context.Context, releaseID int64, windowStart time.Time) ([]Play, map[string]int, error)
	// SaveReleaseCurves replaces the stored curves of a release.
	SaveReleaseCurves(ctx context.Context, releaseID int64, results []Result) error
}

// Slack re-reads events received shortly before the watermark, so a batch
// committed while the previous run was reading is not missed.
const Slack = 10 * time.Minute

// Runner executes one run of the curve job.
type Runner struct {
	Store  Store
	Now    func() time.Time
	Window time.Duration // default DefaultWindow
}

// Report summarizes one run.
type Report struct {
	Since      time.Time `json:"since"`
	Until      time.Time `json:"until"`
	Releases   int       `json:"releases"`
	Versions   int       `json:"versions"`
	Curves     int       `json:"curves"` // timelines with a replay peak
	Plays      int       `json:"plays"`
	ValidPlays int       `json:"valid_plays"`
	Bot        int       `json:"bot"`
	Capped     int       `json:"capped"`
}

// Run recomputes every release with new plays since the last run. The
// watermark only advances when every release succeeded.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	window := r.Window
	if window <= 0 {
		window = DefaultWindow
	}
	until := now.UTC()
	windowStart := until.Add(-window)
	rep := Report{Until: until}
	wm, err := r.Store.CurveWatermark(ctx)
	if err != nil {
		return rep, err
	}
	since := windowStart
	if !wm.IsZero() && wm.Add(-Slack).After(since) {
		since = wm.Add(-Slack)
	}
	rep.Since = since
	ids, err := r.Store.ReleasesWithNewPlays(ctx, since, until, windowStart)
	if err != nil {
		return rep, err
	}
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		plays, durations, err := r.Store.ReleasePlays(ctx, id, windowStart)
		if err != nil {
			errs = append(errs, fmt.Errorf("release %d: %w", id, err))
			continue
		}
		results, st := Aggregate(plays, durations)
		if err := r.Store.SaveReleaseCurves(ctx, id, results); err != nil {
			errs = append(errs, fmt.Errorf("release %d: save: %w", id, err))
			continue
		}
		rep.Releases++
		rep.Versions += len(results)
		for _, res := range results {
			if res.ReplayPeak != nil {
				rep.Curves++
			}
		}
		rep.Plays += st.Plays
		rep.ValidPlays += st.Valid
		rep.Bot += st.Bot
		rep.Capped += st.Capped
	}
	if len(errs) == 0 {
		if err := r.Store.SetCurveWatermark(ctx, until); err != nil {
			errs = append(errs, fmt.Errorf("watermark: %w", err))
		}
	}
	return rep, errors.Join(errs...)
}
