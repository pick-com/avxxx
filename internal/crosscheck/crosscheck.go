// Package crosscheck looks releases already in the catalog up in
// third-party APIs (FINAL_DATA_DESIGN 2.4 tier third_party_api, e.g.
// ThePornDB) and records what they say as source evidence: batch lookup and
// missing-field fallback, never the sole source of truth.
//
// The 'crosscheck' job (cmd/scheduler enqueues it every CROSSCHECK_EVERY,
// default 1h; the cmd/audit worker runs it, or `audit -crosscheck`) selects,
// per lookup adapter (catalog.LookupAdapter), unmerged releases that have a
// DVD code, are missing a key field (release date, maker, performers, cover
// evidence) or have a pending field conflict, have no record from that
// source yet and no attempt still backing off. Each is searched by DVD code
// through the source's governed Fetcher (contact User-Agent, robots.txt,
// min_request_interval and the daily page budget of source_crawl_policies,
// shared with the source's incremental crawl). A unique exact code match
// whose code key (catalog.CodeMatchKey: the API drops zero padding, so
// "HEYZO-123" matches "HEYZO-0123") leads to this release alone is saved
// through SaveCatalogRecord, where the fallback rules apply: it only
// attaches to the existing release, fills fields no better source has and
// never opens a field conflict on its own. Every try is one
// crosscheck_attempts row with a backoff, like internal/medialink.
//
// The source row's governance applies exactly as to a crawl: the job does
// nothing for a disabled or blocked source, and needs the license gate of an
// automatic crawl (official_api + recorded terms review + terms_url).
package crosscheck

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// JobKind is jobs.kind of the cross-check job.
const JobKind = "crosscheck"

// Attempt statuses (crosscheck_attempts.status).
const (
	StatusFound     = "found"
	StatusNotFound  = "not_found"
	StatusAmbiguous = "ambiguous"
	StatusError     = "error"
)

// Backoff schedule.
const (
	NotFoundRetry      = 14 * 24 * time.Hour
	NotFoundSlowRetry  = 60 * 24 * time.Hour
	NotFoundSlowAfter  = 3 // misses before the slow retry
	ErrorRetryBase     = 30 * time.Minute
	ErrorRetryMax      = 24 * time.Hour
	MaxErrorStreak     = 3
	ErrorStreakBackoff = time.Hour
)

// Attempt is one crosscheck_attempts row.
type Attempt struct {
	ReleaseID  int64      `json:"release_id"`
	SourceID   int64      `json:"source_id"`
	Status     string     `json:"status"`
	TriedAt    time.Time  `json:"tried_at"`
	NextTryAt  *time.Time `json:"next_try_at"` // nil = never automatically
	Attempts   int        `json:"attempts"`
	MatchedKey string     `json:"matched_key,omitempty"`
	Candidates []string   `json:"candidates"`
	Reasons    []string   `json:"reasons"`
	LastError  string     `json:"last_error,omitempty"`
}

// Candidate is a release due for a cross-check.
type Candidate struct {
	ReleaseID int64
	Slug      string
	Codes     []string // DVD codes as stored (identifier_value), e.g. "SONE-156"
	// Reasons say why the release is selected: missing_release_date,
	// missing_maker, missing_performers, missing_cover, open_conflict.
	Reasons []string
	Prev    *Attempt
}

// NextTry is when a release whose latest attempt ended in status, for the
// streak-th consecutive time, is due again. nil = never automatically
// (found: evidence recorded; ambiguous: an admin decides).
func NextTry(status string, streak int, now time.Time) *time.Time {
	streak = max(streak, 1)
	var d time.Duration
	switch status {
	case StatusNotFound:
		d = NotFoundRetry
		if streak >= NotFoundSlowAfter {
			d = NotFoundSlowRetry
		}
	case StatusError:
		d = ErrorRetryMax
		if streak <= 16 {
			d = min(ErrorRetryBase<<(streak-1), ErrorRetryMax)
		}
	default:
		return nil
	}
	t := now.Add(d)
	return &t
}

// Streak is the attempts value after a try ending in status.
func Streak(prev *Attempt, status string) int {
	if prev != nil && prev.Status == status {
		return prev.Attempts + 1
	}
	return 1
}

// Config is the job's politeness budget on top of the source's own crawl
// policy (min_request_interval, daily_page_budget).
type Config struct {
	MaxPerRun int // releases per adapter per run (CROSSCHECK_MAX_PER_RUN)
	DailyCap  int // releases tried per source per rolling 24 h (CROSSCHECK_DAILY_CAP)
	BatchSize int // releases selected per query
}

// Defaults.
const (
	DefaultMaxPerRun = 100
	DefaultDailyCap  = 1000
	DefaultBatchSize = 25
)

// DefaultConfig is the configuration with every default.
func DefaultConfig() Config {
	return Config{MaxPerRun: DefaultMaxPerRun, DailyCap: DefaultDailyCap, BatchSize: DefaultBatchSize}
}

// ConfigFromEnv reads CROSSCHECK_MAX_PER_RUN and CROSSCHECK_DAILY_CAP
// (positive integers). Unset or invalid values keep the default; warnings
// describe the invalid ones.
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	c := DefaultConfig()
	var warn []string
	for _, f := range []struct {
		name string
		dst  *int
	}{{"CROSSCHECK_MAX_PER_RUN", &c.MaxPerRun}, {"CROSSCHECK_DAILY_CAP", &c.DailyCap}} {
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

// Budget is how many releases a run may try: the per-run cap, limited by
// what is left of the daily cap after used releases in the last 24 h.
func Budget(maxPerRun, dailyCap, used int) int {
	return max(0, min(maxPerRun, dailyCap-used))
}

// errorText shortens an error for storage (never response bodies).
func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}
