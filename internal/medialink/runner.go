package medialink

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"avxxx/internal/audit"
)

// Config is the job's politeness budget.
type Config struct {
	MinInterval time.Duration // between provider requests (MEDIA_LINK_MIN_INTERVAL, >= 2s)
	MaxPerRun   int           // releases per run (MEDIA_LINK_MAX_PER_RUN)
	DailyCap    int           // releases tried per provider per rolling 24 h (MEDIA_LINK_DAILY_CAP)
	BatchSize   int           // releases selected per query
}

// Defaults.
const (
	DefaultMinInterval = 2 * time.Second
	DefaultMaxPerRun   = 200
	DefaultDailyCap    = 2000
	DefaultBatchSize   = 50
)

// DefaultConfig is the configuration with every default.
func DefaultConfig() Config {
	return Config{MinInterval: DefaultMinInterval, MaxPerRun: DefaultMaxPerRun, DailyCap: DefaultDailyCap, BatchSize: DefaultBatchSize}
}

// ConfigFromEnv reads MEDIA_LINK_MIN_INTERVAL (Go duration, never below
// 2s), MEDIA_LINK_MAX_PER_RUN and MEDIA_LINK_DAILY_CAP (positive integers).
// Unset or invalid values keep the default; warnings describe the invalid ones.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	c := DefaultConfig()
	var warn []string
	if v := strings.TrimSpace(getenv("MEDIA_LINK_MIN_INTERVAL")); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			warn = append(warn, fmt.Sprintf("invalid MEDIA_LINK_MIN_INTERVAL %q, using %s", v, c.MinInterval))
		case d < DefaultMinInterval:
			warn = append(warn, fmt.Sprintf("MEDIA_LINK_MIN_INTERVAL %q is below %s, using %s", v, DefaultMinInterval, DefaultMinInterval))
		default:
			c.MinInterval = d
		}
	}
	for _, f := range []struct {
		name string
		dst  *int
	}{{"MEDIA_LINK_MAX_PER_RUN", &c.MaxPerRun}, {"MEDIA_LINK_DAILY_CAP", &c.DailyCap}} {
		if v := strings.TrimSpace(getenv(f.name)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				warn = append(warn, fmt.Sprintf("invalid %s %q, using %d", f.name, v, *f.dst))
				continue
			}
			*f.dst = n
		}
	}
	return c, warn
}

// OnDemandFromEnv parses MEDIA_LINK_ON_DEMAND: default true; "false", "0",
// "no" or "off" make playback rely on the offline job only.
func OnDemandFromEnv(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// Budget is how many releases a run may try: the per-run cap, limited by
// what is left of the daily cap after used releases in the last 24 h.
func Budget(maxPerRun, dailyCap, used int) int {
	return max(0, min(maxPerRun, dailyCap-used))
}

// Store is everything the job needs from the database.
type Store interface {
	LinkStore
	// DueMediaLinks selects up to limit published, unmerged releases with a
	// DVD code and no release_media_sources row for provider whose attempt
	// is missing or due at now, newest releases first, skipping exclude.
	DueMediaLinks(ctx context.Context, provider string, now time.Time, limit int, exclude []int64) ([]Candidate, error)
	// MediaLinkTriedSince counts attempts for provider tried at or after since.
	MediaLinkTriedSince(ctx context.Context, provider string, since time.Time) (int, error)
	// MediaLinkProviderBackoff is the provider-wide backoff recorded by the
	// latest run (zero when none).
	MediaLinkProviderBackoff(ctx context.Context, provider string) (time.Time, error)
	RecordRun(ctx context.Context, r audit.Run) error
}

// Runner executes the media_link job.
type Runner struct {
	Store     Store
	Search    Searcher
	Config    Config
	Providers []string // default: every key of Providers
	JobID     int64
	Now       func() time.Time
	// Sleep replaces the pacer's sleep (tests).
	Sleep func(ctx context.Context, d time.Duration) error
}

// ProviderReport summarizes one provider's run (also nightly_runs.detail).
type ProviderReport struct {
	Provider     string     `json:"provider"`
	Checked      int        `json:"checked"`
	Found        int        `json:"found"`
	NotFound     int        `json:"not_found"`
	Ambiguous    int        `json:"ambiguous"`
	Errors       int        `json:"errors"`
	Requests     int        `json:"requests"`
	Budget       int        `json:"budget"`
	DailyUsed    int        `json:"daily_used"`
	Stopped      string     `json:"stopped,omitempty"` // rate_limited | error_streak | backoff | daily_cap
	BackoffUntil *time.Time `json:"backoff_until,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
}

// Report summarizes one run.
type Report struct {
	StartedAt time.Time        `json:"started_at"`
	Providers []ProviderReport `json:"providers"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) providers() []string {
	if len(r.Providers) > 0 {
		return r.Providers
	}
	out := make([]string, 0, len(Providers))
	for p := range Providers {
		out = append(out, p)
	}
	return out
}

// Run links due releases for every provider, recording one nightly_runs
// row 'media_link.<provider>' each.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	rep := Report{StartedAt: r.now()}
	cfg := r.Config
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = DefaultMinInterval
	}
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = DefaultMaxPerRun
	}
	if cfg.DailyCap <= 0 {
		cfg.DailyCap = DefaultDailyCap
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	for _, p := range r.providers() {
		pr, err := r.runProvider(ctx, p, cfg)
		rep.Providers = append(rep.Providers, pr)
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *Runner) runProvider(ctx context.Context, provider string, cfg Config) (pr ProviderReport, err error) {
	start := r.now()
	pr.Provider = provider
	defer func() {
		pr.DurationMS = r.now().Sub(start).Milliseconds()
		r.record(ctx, start, pr, err)
	}()
	until, err := r.Store.MediaLinkProviderBackoff(ctx, provider)
	if err != nil {
		return pr, fmt.Errorf("provider backoff: %w", err)
	}
	if start.Before(until) {
		pr.Stopped, pr.BackoffUntil = "backoff", &until // carried forward
		return pr, nil
	}
	if pr.DailyUsed, err = r.Store.MediaLinkTriedSince(ctx, provider, start.Add(-24*time.Hour)); err != nil {
		return pr, fmt.Errorf("daily usage: %w", err)
	}
	pr.Budget = Budget(cfg.MaxPerRun, cfg.DailyCap, pr.DailyUsed)
	if pr.Budget == 0 {
		pr.Stopped = "daily_cap"
		return pr, nil
	}
	pacer := &Pacer{Interval: cfg.MinInterval, Now: r.Now, Sleep: r.Sleep}
	linker := &Linker{Store: r.Store, Search: r.Search, Pacer: pacer, Now: r.Now}
	var seen []int64
	streak := 0
	for pr.Checked < pr.Budget {
		batch, err := r.Store.DueMediaLinks(ctx, provider, r.now(), min(cfg.BatchSize, pr.Budget-pr.Checked), seen)
		if err != nil {
			return pr, fmt.Errorf("select releases: %w", err)
		}
		if len(batch) == 0 {
			return pr, nil
		}
		for _, c := range batch {
			seen = append(seen, c.ReleaseID)
			out, err := linker.Link(ctx, provider, c)
			pr.Requests += out.Requests
			if err != nil {
				return pr, err
			}
			pr.Checked++
			switch out.Attempt.Status {
			case StatusFound:
				pr.Found++
			case StatusNotFound:
				pr.NotFound++
			case StatusAmbiguous:
				pr.Ambiguous++
			case StatusError:
				pr.Errors++
			}
			if out.Attempt.Status == StatusError {
				streak++
			} else {
				streak = 0
			}
			switch {
			case out.RateLimited():
				pr.Stopped = "rate_limited"
				t := r.now().Add(RateLimitBackoff)
				pr.BackoffUntil = &t
				log.Printf("media_link %s: %v; backing off until %s", provider, out.Err, t.Format(time.RFC3339))
				return pr, nil
			case streak >= MaxErrorStreak:
				pr.Stopped = "error_streak"
				t := r.now().Add(ErrorStreakBackoff)
				pr.BackoffUntil = &t
				log.Printf("media_link %s: %d errors in a row (last: %v); backing off until %s", provider, streak, out.Err, t.Format(time.RFC3339))
				return pr, nil
			}
			if pr.Checked >= pr.Budget {
				break
			}
		}
	}
	return pr, nil
}

func (r *Runner) record(ctx context.Context, start time.Time, pr ProviderReport, cause error) {
	detail := map[string]any{"found": pr.Found, "not_found": pr.NotFound, "ambiguous": pr.Ambiguous,
		"errors": pr.Errors, "requests": pr.Requests, "budget": pr.Budget, "daily_used": pr.DailyUsed}
	if pr.Stopped != "" {
		detail["stopped"] = pr.Stopped
	}
	if pr.BackoffUntil != nil {
		detail["backoff_until"] = pr.BackoffUntil.UTC().Format(time.RFC3339)
	}
	run := audit.Run{Job: JobKind + "." + pr.Provider, JobID: r.JobID, StartedAt: start, DurationMS: pr.DurationMS,
		Checked: pr.Checked, Fixed: pr.Found, Issues: pr.Ambiguous + pr.Errors, Result: audit.ResultOK, Detail: detail}
	switch {
	case cause != nil:
		run.Result = audit.ResultFailed
		detail["error"] = errorText(cause)
	case pr.Stopped == "rate_limited" || pr.Stopped == "error_streak" || pr.Ambiguous+pr.Errors > 0:
		run.Result = audit.ResultIssues
	}
	if err := r.Store.RecordRun(context.WithoutCancel(ctx), run); err != nil {
		log.Printf("media_link %s: record run: %v", pr.Provider, err)
	}
}
