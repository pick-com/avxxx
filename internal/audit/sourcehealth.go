package audit

// Playback source health (PRD 1 片源健康分 ≥ 95 Alpha gate, PRD 11 bands,
// PRD 13 首帧 ≤ 1.5 s, tech plan 3.3).
//
// Every 10 minutes (job kind 'source_health', enqueued by cmd/scheduler and
// run by the cmd/audit worker) each playback source is scored over the last
// 24 hours of events:
//
//	attempts  = distinct play_id with a play_request for the source
//	playable  = attempts with a first_frame for the same play_id and source,
//	            and no play_end(reason=error) before that first frame
//	ff_ok     = playable attempts whose first first_frame.elapsed_ms ≤ FirstFrameOKMS
//	health    = 100 × (0.7 × playable/attempts + 0.3 × ff_ok/playable)
//
// 首帧达标率 is measured over plays that showed a first frame (a failed play is
// already counted by 可播放率). Below MinAttempts the previous score is kept
// and the run is marked insufficient_data. Status: < DisableBelow → disabled,
// < DegradeBelow → degraded, else active; a degraded source only returns to
// active at RecoverAt (hysteresis) and a disabled source is never re-enabled
// automatically.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// JobKindSourceHealth is jobs.kind for source health runs.
const JobKindSourceHealth = "source_health"

// HealthActor is review_events.actor for automatic source status changes.
const HealthActor = "health"

// BandInsufficient marks a run (or a never-scored source) without enough samples.
const BandInsufficient = "insufficient_data"

// Playback source statuses (playback_sources.status).
const (
	SourceActive   = "active"
	SourceDegraded = "degraded"
	SourceDisabled = "disabled"
)

// Score weights (PRD 1: 可播放率 × 0.7 + 首帧达标率 × 0.3).
const (
	WeightPlayable   = 0.7
	WeightFirstFrame = 0.3
)

// HealthConfig tunes the source health job. Zero fields take the defaults.
type HealthConfig struct {
	Window         time.Duration // default 24h
	FirstFrameOKMS int64         // default 1500 (PRD 13)
	MinAttempts    int           // default 30
	DegradeBelow   float64       // 0–100, default 80
	DisableBelow   float64       // 0–100, default 50
	RecoverAt      float64       // degraded → active only at or above, default 85
	Retention      time.Duration // source_health_runs kept, default 30 days
}

// DefaultHealthConfig is the configuration used when fields are zero.
var DefaultHealthConfig = HealthConfig{
	Window: 24 * time.Hour, FirstFrameOKMS: 1500, MinAttempts: 30,
	DegradeBelow: 80, DisableBelow: 50, RecoverAt: 85, Retention: 30 * 24 * time.Hour,
}

func (c HealthConfig) withDefaults() HealthConfig {
	d := DefaultHealthConfig
	if c.Window > 0 {
		d.Window = c.Window
	}
	if c.FirstFrameOKMS > 0 {
		d.FirstFrameOKMS = c.FirstFrameOKMS
	}
	if c.MinAttempts > 0 {
		d.MinAttempts = c.MinAttempts
	}
	if c.DegradeBelow > 0 {
		d.DegradeBelow = c.DegradeBelow
	}
	if c.DisableBelow > 0 {
		d.DisableBelow = c.DisableBelow
	}
	if c.RecoverAt > 0 {
		d.RecoverAt = c.RecoverAt
	}
	if d.RecoverAt < d.DegradeBelow {
		d.RecoverAt = d.DegradeBelow
	}
	if c.Retention > 0 {
		d.Retention = c.Retention
	}
	return d
}

// SourceSample is one source's event counts for a window plus its current
// state. Rebuffers and SwitchesAway are reported, not scored.
type SourceSample struct {
	SourceID     int64
	ReleaseID    int64
	ReleaseTitle string
	Provider     string
	Label        string
	Status       string
	HealthScore  float64 // current playback_sources.health_score, 0–1
	Attempts     int64
	Playable     int64
	FirstFrameOK int64
	Rebuffers    int64
	SwitchesAway int64 // automatic source_switch away from this source
}

// Active reports whether the sample has any playback activity worth recording.
func (s SourceSample) Active() bool {
	return s.Attempts > 0 || s.Rebuffers > 0 || s.SwitchesAway > 0
}

// rate is n/d, 0 when d is 0.
func rate(n, d int64) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// HealthRates are the component rates of a score.
type HealthRates struct {
	Playable     float64 `json:"playable_rate"`
	FirstFrameOK float64 `json:"ff_ok_rate"`
	Rebuffer     float64 `json:"rebuffer_rate"` // rebuffers per playable play
}

// Rates computes the component rates from counts.
func Rates(attempts, playable, ffOK, rebuffers int64) HealthRates {
	return HealthRates{Playable: rate(playable, attempts), FirstFrameOK: rate(ffOK, playable), Rebuffer: rate(rebuffers, playable)}
}

// SourceHealthScore is 100 × (0.7 × playable/attempts + 0.3 × ff_ok/playable),
// rounded to 0.01. ok is false without attempts.
func SourceHealthScore(attempts, playable, ffOK int64) (score float64, ok bool) {
	if attempts <= 0 {
		return 0, false
	}
	if playable > attempts {
		playable = attempts
	}
	if ffOK > playable {
		ffOK = playable
	}
	r := Rates(attempts, playable, ffOK, 0)
	return math.Round(100*(WeightPlayable*r.Playable+WeightFirstFrame*r.FirstFrameOK)*100) / 100, true
}

// ScorePoints converts a 0–1 health_score (stored as real) to the integer
// 0–100 score used for bands, rounding down but tolerating float error so
// 0.95 stored as 0.9499999 still counts as 95.
func ScorePoints(health float64) int {
	return int(math.Floor(health*100 + 1e-3))
}

// SourceBand is the PRD 11 band of a 0–100 score.
func SourceBand(score float64) string { return HealthBand(int(math.Floor(score + 1e-6))) }

// NextSourceStatus applies the automatic status rules to a sufficient score:
// disabled stays disabled (only an admin re-enables); below DisableBelow →
// disabled; below DegradeBelow → degraded; a degraded source needs RecoverAt
// to become active again; otherwise active.
func NextSourceStatus(current string, score float64, cfg HealthConfig) string {
	cfg = cfg.withDefaults()
	switch {
	case current == SourceDisabled:
		return SourceDisabled
	case score < cfg.DisableBelow:
		return SourceDisabled
	case score < cfg.DegradeBelow:
		return SourceDegraded
	case current == SourceDegraded && score < cfg.RecoverAt:
		return SourceDegraded
	}
	return SourceActive
}

// SourceOutcome is the decision for one source in one run; it is what gets
// stored as a source_health_runs row.
type SourceOutcome struct {
	SourceSample
	WindowStart, WindowEnd time.Time
	Score                  *float64 // raw score of this window; nil without attempts
	Band                   string   // SourceBand(Score), or BandInsufficient
	Insufficient           bool     // attempts < MinAttempts: health_score / status untouched
	StatusAfter            string
	JobID                  int64
}

// StatusChanged reports whether the run changes the source's status.
func (o SourceOutcome) StatusChanged() bool { return o.StatusAfter != o.Status }

// EvaluateSource decides one source's outcome for a window.
func EvaluateSource(s SourceSample, start, end time.Time, cfg HealthConfig) SourceOutcome {
	cfg = cfg.withDefaults()
	o := SourceOutcome{SourceSample: s, WindowStart: start, WindowEnd: end, StatusAfter: s.Status, Band: BandInsufficient}
	if score, ok := SourceHealthScore(s.Attempts, s.Playable, s.FirstFrameOK); ok {
		o.Score = &score
	}
	if o.Score == nil || s.Attempts < int64(cfg.MinAttempts) {
		o.Insufficient = true
		return o
	}
	o.Band = SourceBand(*o.Score)
	o.StatusAfter = NextSourceStatus(s.Status, *o.Score, cfg)
	return o
}

// SourceHealthFacts describes a source the R-SOURCE rule (and the health
// job, on a status change) raises an issue for.
type SourceHealthFacts struct {
	SourceID     int64      `json:"source_id"`
	ReleaseID    int64      `json:"release_id"`
	ReleaseTitle string     `json:"release_title"`
	Provider     string     `json:"provider"`
	Label        string     `json:"label"`
	Status       string     `json:"status"`
	Score        *float64   `json:"score"` // latest sufficient score, 0–100
	Band         string     `json:"band"`
	Attempts     int64      `json:"attempts"` // of the latest run
	WindowEnd    *time.Time `json:"window_end,omitempty"`
}

// SourceIssue builds the R-SOURCE issue for a degraded or disabled source.
// The fingerprint is the status, so dismissing a degraded issue does not hide
// a later disable.
func SourceIssue(f SourceHealthFacts, seen time.Time) Issue {
	sev := SeverityMedium
	action := "degraded: ranked after healthy sources"
	if f.Status == SourceDisabled {
		sev = SeverityHigh
		action = "disabled: removed from playback; only an admin re-enables it"
	}
	detail := map[string]any{"release_id": f.ReleaseID, "release_title": f.ReleaseTitle, "provider": f.Provider, "label": f.Label,
		"status": f.Status, "band": f.Band, "attempts": f.Attempts, "action": action}
	if f.Score != nil {
		detail["score"] = *f.Score
	}
	if f.WindowEnd != nil {
		detail["window_end"] = f.WindowEnd.UTC().Format(time.RFC3339)
	}
	return Issue{Rule: RuleSourceHealth, EntityType: "playback_source", EntityID: f.SourceID, Severity: sev, Detail: detail,
		Fingerprint: Fingerprint("source", f.Status), Priority: f.Attempts, LastSeenAt: seen}
}

// SourceHealthStore is what the source health job needs from the database.
type SourceHealthStore interface {
	// SourceHealthSamples returns every playback source with its event
	// counts over [start, end).
	SourceHealthSamples(ctx context.Context, start, end time.Time, firstFrameOKMS int64) ([]SourceSample, error)
	// ApplySourceHealth records o as a source_health_runs row and, unless
	// o.Insufficient, sets health_score; when o changes the status it does so
	// only if the status is still o.Status, writing a review_events row with
	// actor HealthActor. It returns whether the status changed.
	ApplySourceHealth(ctx context.Context, o SourceOutcome) (bool, error)
	// OpenIssue upserts an audit issue (see Store).
	OpenIssue(ctx context.Context, is Issue) (OpenResult, error)
	// ResolveSourceIssue marks the open R-SOURCE issue of a source fixed.
	ResolveSourceIssue(ctx context.Context, sourceID int64, note string) error
	// PruneSourceHealthRuns deletes runs that ended before before.
	PruneSourceHealthRuns(ctx context.Context, before time.Time) (int64, error)
}

// HealthRunner executes one source health run.
type HealthRunner struct {
	Store  SourceHealthStore
	Config HealthConfig
	Now    func() time.Time
	JobID  int64
}

// HealthReport summarizes one run.
type HealthReport struct {
	WindowStart  time.Time        `json:"window_start"`
	WindowEnd    time.Time        `json:"window_end"`
	Sources      int              `json:"sources"`
	Recorded     int              `json:"recorded"`
	Scored       int              `json:"scored"`
	Insufficient int              `json:"insufficient_data"`
	Changes      []map[string]any `json:"changes,omitempty"`
	Pruned       int64            `json:"pruned"`
}

// Run scores every source with activity, applies status changes, and keeps
// audit issues in step: a source that becomes degraded or disabled gets an
// open R-SOURCE issue, one that recovers has it resolved.
func (h *HealthRunner) Run(ctx context.Context) (HealthReport, error) {
	cfg := h.Config.withDefaults()
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	end := now.UTC()
	start := end.Add(-cfg.Window)
	rep := HealthReport{WindowStart: start, WindowEnd: end}
	samples, err := h.Store.SourceHealthSamples(ctx, start, end, cfg.FirstFrameOKMS)
	if err != nil {
		return rep, err
	}
	rep.Sources = len(samples)
	var errs []error
	for _, s := range samples {
		if !s.Active() {
			continue
		}
		o := EvaluateSource(s, start, end, cfg)
		o.JobID = h.JobID
		changed, err := h.Store.ApplySourceHealth(ctx, o)
		if err != nil {
			errs = append(errs, fmt.Errorf("source %d: %w", s.SourceID, err))
			continue
		}
		rep.Recorded++
		if o.Insufficient {
			rep.Insufficient++
		} else {
			rep.Scored++
		}
		if !changed {
			continue
		}
		rep.Changes = append(rep.Changes, map[string]any{"source_id": s.SourceID, "from": s.Status, "to": o.StatusAfter, "score": *o.Score})
		if o.StatusAfter == SourceActive {
			err = h.Store.ResolveSourceIssue(ctx, s.SourceID, fmt.Sprintf("recovered: health %.2f", *o.Score))
		} else {
			we := o.WindowEnd
			_, err = h.Store.OpenIssue(ctx, SourceIssue(SourceHealthFacts{SourceID: s.SourceID, ReleaseID: s.ReleaseID, ReleaseTitle: s.ReleaseTitle,
				Provider: s.Provider, Label: s.Label, Status: o.StatusAfter, Score: o.Score, Band: o.Band, Attempts: s.Attempts, WindowEnd: &we}, end))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("source %d issue: %w", s.SourceID, err))
		}
	}
	if n, err := h.Store.PruneSourceHealthRuns(ctx, end.Add(-cfg.Retention)); err != nil {
		errs = append(errs, fmt.Errorf("prune: %w", err))
	} else {
		rep.Pruned = n
	}
	return rep, errors.Join(errs...)
}

// ---------------------------------------------------------------- admin

// SourceHealthFilter selects sources for GET /api/admin/playback-sources/health.
type SourceHealthFilter struct {
	Band   string // "" = all; a PRD 11 band or BandInsufficient
	Cursor string
	Limit  int
	Trend  int // runs per source in trend, default 12
}

// ValidSourceBand reports whether b is a band the filter accepts.
func ValidSourceBand(b string) bool {
	switch b {
	case BandSevere, BandDegraded, BandHealthy, BandExcellent, BandInsufficient:
		return true
	}
	return false
}

// SourceHealthPoint is one run in a source's trend.
type SourceHealthPoint struct {
	WindowEnd    time.Time `json:"window_end"`
	Score        *float64  `json:"score"`
	Attempts     int64     `json:"attempts"`
	Insufficient bool      `json:"insufficient_data"`
}

// SourceHealthSampleView is the latest run's sample for the admin list.
type SourceHealthSampleView struct {
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	Attempts     int64     `json:"attempts"`
	Playable     int64     `json:"playable"`
	FirstFrameOK int64     `json:"ff_ok"`
	Rebuffers    int64     `json:"rebuffers"`
	SwitchesAway int64     `json:"switches_away"`
	HealthRates
	Score        *float64 `json:"score"`
	Insufficient bool     `json:"insufficient_data"`
}

// SourceHealthItem is one row of the admin source health list.
type SourceHealthItem struct {
	SourceID     int64                   `json:"source_id"`
	ReleaseID    int64                   `json:"release_id"`
	ReleaseSlug  string                  `json:"release_slug"`
	ReleaseTitle string                  `json:"release_title"`
	Kind         string                  `json:"kind"`
	Provider     string                  `json:"provider"`
	Label        string                  `json:"label"`
	Status       string                  `json:"status"`
	Score        *int                    `json:"score"` // effective health_score × 100; nil until a sufficient run
	Band         string                  `json:"band"`
	CheckedAt    *time.Time              `json:"checked_at"`
	Latest       *SourceHealthSampleView `json:"latest"` // latest run (sample size)
	Trend        []SourceHealthPoint     `json:"trend"`  // newest first
}

// SourceHealthSummary is the 片源健康分 block of the AdminDashboard: the
// pooled score over every source's latest run in the last 24 hours, so busy
// sources weigh more, plus per-band and per-status source counts.
type SourceHealthSummary struct {
	Score            *int             `json:"score"` // nil without attempts
	Band             string           `json:"band,omitempty"`
	Attempts         int64            `json:"attempts"`
	Playable         int64            `json:"playable"`
	FirstFrameOK     int64            `json:"ff_ok"`
	Sources          int64            `json:"sources"`
	Scored           int64            `json:"scored"`
	InsufficientData int64            `json:"insufficient_data"`
	ByBand           map[string]int64 `json:"by_band"`
	ByStatus         map[string]int64 `json:"by_status"`
	LastRunAt        *time.Time       `json:"last_run_at"`
}

// SetPooled fills Score and Band from pooled counts.
func (s *SourceHealthSummary) SetPooled(attempts, playable, ffOK int64) {
	s.Attempts, s.Playable, s.FirstFrameOK = attempts, playable, ffOK
	s.Score, s.Band = nil, ""
	if v, ok := SourceHealthScore(attempts, playable, ffOK); ok {
		p := int(math.Floor(v + 1e-6))
		s.Score, s.Band = &p, HealthBand(p)
	}
}
