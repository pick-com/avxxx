package search

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"time"
)

// JobKind is jobs.kind of the hourly search statistics job: cmd/scheduler
// enqueues it once per SEARCH_STATS_EVERY slot (default 1h) and the
// cmd/audit worker runs it.
const JobKind = "search_stats"

// HotPeriod is the only search_trending period: the last 24 hours.
const HotPeriod = "24h"

// HotWindow is the hot-search window.
const HotWindow = 24 * time.Hour

// BackfillDays bounds how far back the first aggregation run reaches
// (raw events are kept 90 days).
const BackfillDays = 90

// Store is everything the job needs from the database
// (internal/store/search.go). None of these methods read user ids.
type Store interface {
	// LastSearchDay is the latest search_query_daily day (ok=false when empty).
	LastSearchDay(ctx context.Context) (day time.Time, ok bool, err error)
	// SearchEventCounts groups `search` events with from <= ts_server < to
	// by UTC day, raw query and ui_lang.
	SearchEventCounts(ctx context.Context, from, to time.Time) ([]RawCount, error)
	// ReplaceSearchDaily replaces every search_query_daily row with
	// fromDay <= day <= toDay by rows, in one transaction.
	ReplaceSearchDaily(ctx context.Context, fromDay, toDay time.Time, rows []DailyRow) error
	// SearchHotInputs groups `search` events with from <= ts_server < to by
	// raw query: distinct sessions, events and zero-result events.
	SearchHotInputs(ctx context.Context, from, to time.Time) ([]HotInput, error)
	// SearchExclusions loads search_blocklist and moderation_words.
	SearchExclusions(ctx context.Context) (Exclusions, error)
	// ReplaceSearchTrending swaps a period's board.
	ReplaceSearchTrending(ctx context.Context, period string, rows []HotRow, at time.Time) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// Runner executes the search statistics job.
type Runner struct {
	Store Store
	Now   func() time.Time
	JobID int64
	Hot   HotConfig
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Report summarizes one run.
type Report struct {
	StartedAt  time.Time `json:"started_at"`
	FromDay    string    `json:"from_day"`
	ToDay      string    `json:"to_day"`
	RawGroups  int       `json:"raw_groups"`
	DailyRows  int       `json:"daily_rows"`
	HotInputs  int       `json:"hot_inputs"`
	HotRows    int       `json:"hot_rows"`
	DurationMS int64     `json:"duration_ms"`
}

// AggregationRange is the day range a run recomputes: from the latest
// aggregated day (re-done because it was partial) or, at least, yesterday
// (late events), back to at most BackfillDays when the table is empty.
func AggregationRange(now time.Time, last time.Time, ok bool) (from, to time.Time) {
	to = now.UTC().Truncate(24 * time.Hour)
	from = to.AddDate(0, 0, -1)
	if !ok {
		return to.AddDate(0, 0, -(BackfillDays - 1)), to
	}
	last = last.UTC().Truncate(24 * time.Hour)
	if last.Before(from) {
		from = last
	}
	if floor := to.AddDate(0, 0, -(BackfillDays - 1)); from.Before(floor) {
		from = floor
	}
	return from, to
}

// Run aggregates `search` events into search_query_daily and recomputes the
// 24 h hot-search board, recording nightly_runs rows 'search.daily' and
// 'search.hot'.
func (r *Runner) Run(ctx context.Context) (rep Report, err error) {
	start := r.now()
	rep.StartedAt = start

	dailyStart := start
	err = func() (err error) {
		defer func() {
			r.record(ctx, "search.daily", dailyStart, rep.RawGroups, rep.DailyRows, map[string]any{"from_day": rep.FromDay, "to_day": rep.ToDay}, err)
		}()
		last, ok, err := r.Store.LastSearchDay(ctx)
		if err != nil {
			return fmt.Errorf("last day: %w", err)
		}
		from, to := AggregationRange(start, last, ok)
		rep.FromDay, rep.ToDay = from.Format(time.DateOnly), to.Format(time.DateOnly)
		raw, err := r.Store.SearchEventCounts(ctx, from, to.AddDate(0, 0, 1))
		if err != nil {
			return fmt.Errorf("event counts: %w", err)
		}
		rep.RawGroups = len(raw)
		rows := AggregateDaily(raw)
		rep.DailyRows = len(rows)
		if err = r.Store.ReplaceSearchDaily(ctx, from, to, rows); err != nil {
			return fmt.Errorf("write daily: %w", err)
		}
		return nil
	}()
	if err != nil {
		return rep, err
	}

	hotStart := r.now()
	err = func() (err error) {
		defer func() {
			r.record(ctx, "search.hot", hotStart, rep.HotInputs, rep.HotRows, nil, err)
		}()
		in, err := r.Store.SearchHotInputs(ctx, start.Add(-HotWindow), start)
		if err != nil {
			return fmt.Errorf("hot inputs: %w", err)
		}
		rep.HotInputs = len(in)
		ex, err := r.Store.SearchExclusions(ctx)
		if err != nil {
			return fmt.Errorf("exclusions: %w", err)
		}
		rows := RankHot(in, ex, r.Hot)
		rep.HotRows = len(rows)
		if err = r.Store.ReplaceSearchTrending(ctx, HotPeriod, rows, start); err != nil {
			return fmt.Errorf("write hot: %w", err)
		}
		return nil
	}()
	rep.DurationMS = r.now().Sub(start).Milliseconds()
	return rep, err
}

func (r *Runner) record(ctx context.Context, job string, start time.Time, checked, written int, detail map[string]any, cause error) {
	run := audit.Run{Job: job, JobID: r.JobID, StartedAt: start, DurationMS: r.now().Sub(start).Milliseconds(),
		Checked: checked, Fixed: written, Result: audit.ResultOK, Detail: detail}
	if cause != nil {
		run.Result = audit.ResultFailed
		if run.Detail == nil {
			run.Detail = map[string]any{}
		}
		run.Detail["error"] = cause.Error()
	}
	_ = r.Store.RecordRun(ctx, run)
}
