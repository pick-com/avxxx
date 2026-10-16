package crosscheck

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/catalog"
)

// Source is the governance state of the source row a lookup adapter writes
// under (the row seeded for the adapter, e.g. migration 032).
type Source struct {
	ID           int64
	Name         string
	Enabled      bool
	BlockedUntil *time.Time
	Governance   catalog.SourceGovernance
}

// Store is everything the job needs from the database.
type Store interface {
	catalog.RecordSink
	// CrossCheckSource returns the source row of adapter; found=false when
	// the adapter has none.
	CrossCheckSource(ctx context.Context, adapter string) (src Source, found bool, err error)
	// DueCrossChecks selects up to limit releases due for a lookup in
	// sourceID (see the package comment), skipping exclude.
	DueCrossChecks(ctx context.Context, sourceID int64, now time.Time, limit int, exclude []int64) ([]Candidate, error)
	// CrossCheckTriedSince counts attempts for sourceID tried at or after since.
	CrossCheckTriedSince(ctx context.Context, sourceID int64, since time.Time) (int, error)
	RecordCrossCheckAttempt(ctx context.Context, a Attempt) error
	// CrossCheckFetchPolicy is the source's fetch policy for today
	// (incremental phase budget); exhausted when nothing is left.
	CrossCheckFetchPolicy(ctx context.Context, sourceID int64) (p catalog.FetchPolicy, exhausted bool, err error)
	// RecordCrossCheckUsage adds the run's requests to the source's daily usage.
	RecordCrossCheckUsage(ctx context.Context, sourceID int64, pages, bytes int64) error
	BlockSource(ctx context.Context, sourceID int64, retryAfter time.Duration, reason string) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// Runner executes the crosscheck job.
type Runner struct {
	Store    Store
	Adapters []catalog.LookupAdapter
	Config   Config
	// HTTPClient is the base client (the adapter adds its credentials).
	HTTPClient *http.Client
	// UserAgent is the crawler contact User-Agent (catalog.UserAgentFromEnv);
	// without one the job does nothing, like catalog crawls.
	UserAgent string
	Robots    catalog.RobotsChecker
	JobID     int64
	Now       func() time.Time
}

// AdapterReport summarizes one adapter's run (also nightly_runs.detail).
type AdapterReport struct {
	Adapter   string `json:"adapter"`
	SourceID  int64  `json:"source_id,omitempty"`
	Checked   int    `json:"checked"`
	Found     int    `json:"found"`
	NotFound  int    `json:"not_found"`
	Ambiguous int    `json:"ambiguous"`
	Errors    int    `json:"errors"`
	Requests  int64  `json:"requests"`
	Budget    int    `json:"budget"`
	DailyUsed int    `json:"daily_used"`
	// Stopped: no_source | disabled | blocked | license_gate | config |
	// not_configured | budget | daily_cap | rate_limited | robots | error_streak
	Stopped    string `json:"stopped,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// Report summarizes one run.
type Report struct {
	StartedAt time.Time       `json:"started_at"`
	Adapters  []AdapterReport `json:"adapters"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run cross-checks due releases for every adapter, recording one
// nightly_runs row 'crosscheck.<adapter>' each.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	rep := Report{StartedAt: r.now()}
	cfg := r.Config
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = DefaultMaxPerRun
	}
	if cfg.DailyCap <= 0 {
		cfg.DailyCap = DefaultDailyCap
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	for _, a := range r.Adapters {
		ar, err := r.runAdapter(ctx, a, cfg)
		rep.Adapters = append(rep.Adapters, ar)
		if err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// maxInlineRetryWait is the longest 429 Retry-After the job sleeps through
// before parking the source as blocked (same as the crawl worker).
const maxInlineRetryWait = 30 * time.Second

func (r *Runner) runAdapter(ctx context.Context, a catalog.LookupAdapter, cfg Config) (ar AdapterReport, err error) {
	start := r.now()
	ar.Adapter = a.Name()
	redactor, _ := a.(catalog.Redactor)
	var fetcher *catalog.Fetcher
	defer func() {
		if fetcher != nil {
			pages, bytes := fetcher.Usage()
			ar.Requests = pages
			if uerr := r.Store.RecordCrossCheckUsage(context.WithoutCancel(ctx), ar.SourceID, pages, bytes); uerr != nil {
				log.Printf("crosscheck %s: record usage: %v", ar.Adapter, uerr)
			}
		}
		err = catalog.SanitizeError(err, redactor)
		ar.Reason = sanitize(redactor, ar.Reason)
		ar.DurationMS = r.now().Sub(start).Milliseconds()
		r.record(ctx, start, ar, err)
	}()
	src, found, err := r.Store.CrossCheckSource(ctx, a.Name())
	if err != nil {
		return ar, fmt.Errorf("load source: %w", err)
	}
	if !found {
		ar.Stopped = "no_source"
		return ar, nil
	}
	ar.SourceID = src.ID
	switch {
	case !src.Enabled:
		ar.Stopped = "disabled"
		return ar, nil
	case src.BlockedUntil != nil && src.BlockedUntil.After(start):
		ar.Stopped, ar.Reason = "blocked", "source blocked until "+src.BlockedUntil.UTC().Format(time.RFC3339)
		return ar, nil
	}
	if g := catalog.Gate(src.Governance, catalog.PhaseIncremental, 0); !g.Allowed {
		ar.Stopped, ar.Reason = "license_gate", g.Reason
		return ar, nil
	}
	if r.UserAgent == "" {
		ar.Stopped, ar.Reason = "config", "CATALOG_CONTACT / CATALOG_USER_AGENT is not set"
		return ar, nil
	}
	client, cerr := catalog.HTTPClientFor(a, r.HTTPClient)
	if cerr != nil {
		ar.Stopped, ar.Reason = "not_configured", cerr.Error()
		return ar, nil
	}
	policy, exhausted, err := r.Store.CrossCheckFetchPolicy(ctx, src.ID)
	if err != nil {
		return ar, fmt.Errorf("fetch policy: %w", err)
	}
	if exhausted {
		ar.Stopped = "budget"
		return ar, nil
	}
	if ar.DailyUsed, err = r.Store.CrossCheckTriedSince(ctx, src.ID, start.Add(-24*time.Hour)); err != nil {
		return ar, fmt.Errorf("daily usage: %w", err)
	}
	ar.Budget = Budget(cfg.MaxPerRun, cfg.DailyCap, ar.DailyUsed)
	if ar.Budget == 0 {
		ar.Stopped = "daily_cap"
		return ar, nil
	}
	policy.MaxRetryWait = maxInlineRetryWait
	fetcher = catalog.NewFetcher(client, policy)
	fetcher.UserAgent, fetcher.Robots, fetcher.Automatic = r.UserAgent, r.Robots, true
	sink := catalog.RedactingSink{Sink: r.Store, Redactor: redactor}
	var seen []int64
	streak := 0
	for ar.Checked < ar.Budget {
		batch, err := r.Store.DueCrossChecks(ctx, src.ID, r.now(), min(cfg.BatchSize, ar.Budget-ar.Checked), seen)
		if err != nil {
			return ar, fmt.Errorf("select releases: %w", err)
		}
		if len(batch) == 0 {
			return ar, nil
		}
		for _, c := range batch {
			seen = append(seen, c.ReleaseID)
			att, stop, err := r.check(ctx, a, fetcher, sink, src.ID, c)
			if err != nil {
				return ar, err
			}
			if stop == "budget" {
				ar.Stopped = stop // nothing was decided for c: no attempt
				return ar, nil
			}
			att.LastError = sanitize(redactor, att.LastError)
			if err := r.Store.RecordCrossCheckAttempt(ctx, att); err != nil {
				return ar, fmt.Errorf("record attempt: %w", err)
			}
			ar.Checked++
			switch att.Status {
			case StatusFound:
				ar.Found++
			case StatusNotFound:
				ar.NotFound++
			case StatusAmbiguous:
				ar.Ambiguous++
			case StatusError:
				ar.Errors++
			}
			if att.Status == StatusError {
				streak++
			} else {
				streak = 0
			}
			switch {
			case stop != "":
				ar.Stopped, ar.Reason = stop, att.LastError
				return ar, nil
			case streak >= MaxErrorStreak:
				ar.Stopped, ar.Reason = "error_streak", att.LastError
				return ar, nil
			}
			if ar.Checked >= ar.Budget {
				break
			}
		}
	}
	return ar, nil
}

// check looks one release up. stop is non-empty when the run must end after
// recording att ("budget": end without recording). err is a database error
// that fails the run.
func (r *Runner) check(ctx context.Context, a catalog.LookupAdapter, fetcher *catalog.Fetcher, sink catalog.RecordSink, sourceID int64, c Candidate) (att Attempt, stop string, err error) {
	now := r.now()
	att = Attempt{ReleaseID: c.ReleaseID, SourceID: sourceID, TriedAt: now, Candidates: []string{}, Reasons: c.Reasons}
	finish := func(status, lastErr string) {
		att.Status, att.LastError = status, lastErr
		att.Attempts = Streak(c.Prev, status)
		att.NextTryAt = NextTry(status, att.Attempts, now)
	}
	var recs []catalog.Record
	for _, code := range c.Codes {
		for _, u := range a.LookupURLs(code) {
			doc, ferr := fetcher.Fetch(ctx, u)
			if errors.Is(ferr, catalog.ErrBudgetExhausted) {
				return att, "budget", nil
			}
			if b, ok := catalog.IsBlocked(ferr); ok {
				if berr := r.Store.BlockSource(context.WithoutCancel(ctx), sourceID, b.RetryAfter, b.Reason); berr != nil {
					log.Printf("crosscheck %s: block source: %v", a.Name(), berr)
				}
				finish(StatusError, b.Error())
				return att, "rate_limited", nil
			}
			if _, ok := catalog.IsRobots(ferr); ok {
				finish(StatusError, ferr.Error())
				return att, "robots", nil
			}
			if ferr != nil {
				if ctx.Err() != nil {
					return att, "", ctx.Err()
				}
				finish(StatusError, ferr.Error())
				return att, "", nil
			}
			found, derr := a.Discover(ctx, doc)
			if derr != nil {
				finish(StatusError, derr.Error())
				return att, "", nil
			}
			recs = append(recs, found...)
		}
	}
	for _, rec := range recs {
		if rec.EntityType == catalog.Release && len(att.Candidates) < 20 {
			att.Candidates = append(att.Candidates, rec.SourceKey)
		}
	}
	matches := catalog.ExactCodeMatches(recs, c.Codes)
	switch len(matches) {
	case 0:
		finish(StatusNotFound, "")
		return att, "", nil
	case 1:
	default:
		att.Candidates = att.Candidates[:0]
		for _, m := range matches {
			att.Candidates = append(att.Candidates, m.SourceKey)
		}
		finish(StatusAmbiguous, "several exact code matches")
		return att, "", nil
	}
	m := matches[0]
	code, _ := m.Fields["dvd_code"].(string)
	if want := catalog.ReleaseSlug(code, "", "", ""); want != c.Slug {
		// SaveCatalogRecord attaches by the code slug; a release published
		// under another slug (split, manual edit) is left to an admin.
		att.Candidates = []string{m.SourceKey}
		finish(StatusAmbiguous, fmt.Sprintf("code slug %q is not the release slug %q", want, c.Slug))
		return att, "", nil
	}
	if _, serr := sink.SaveCatalogRecord(ctx, sourceID, a.Name(), m); serr != nil {
		return att, "", fmt.Errorf("save %s: %w", m.SourceKey, serr)
	}
	att.MatchedKey, att.Candidates = m.SourceKey, []string{m.SourceKey}
	finish(StatusFound, "")
	return att, "", nil
}

func sanitize(r catalog.Redactor, s string) string {
	if r == nil {
		return s
	}
	return r.Redact(s)
}

func (r *Runner) record(ctx context.Context, start time.Time, ar AdapterReport, cause error) {
	detail := map[string]any{"source_id": ar.SourceID, "found": ar.Found, "not_found": ar.NotFound, "ambiguous": ar.Ambiguous,
		"errors": ar.Errors, "requests": ar.Requests, "budget": ar.Budget, "daily_used": ar.DailyUsed}
	if ar.Stopped != "" {
		detail["stopped"] = ar.Stopped
	}
	if ar.Reason != "" {
		detail["reason"] = ar.Reason
	}
	run := audit.Run{Job: JobKind + "." + ar.Adapter, JobID: r.JobID, StartedAt: start, DurationMS: ar.DurationMS,
		Checked: ar.Checked, Fixed: ar.Found, Issues: ar.Ambiguous + ar.Errors, Result: audit.ResultOK, Detail: detail}
	switch {
	case cause != nil:
		run.Result = audit.ResultFailed
		detail["error"] = errorText(cause)
	case ar.Stopped == "license_gate" || ar.Stopped == "config" || ar.Stopped == "not_configured" || ar.Stopped == "rate_limited" ||
		ar.Stopped == "robots" || ar.Stopped == "error_streak" || ar.Ambiguous+ar.Errors > 0:
		run.Result = audit.ResultIssues
	}
	if err := r.Store.RecordRun(context.WithoutCancel(ctx), run); err != nil {
		log.Printf("crosscheck %s: record run: %v", ar.Adapter, err)
	}
}
