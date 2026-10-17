package watchstats

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"time"
)

// Job kinds (sourceless jobs run by cmd/audit, enqueued by cmd/scheduler).
// JobKind rebuilds the days that are still open somewhere on Earth (hourly);
// JobKindNightly re-does the few days before, catching late events and
// missed hourly runs.
const (
	JobKind        = "watch_stats"
	JobKindNightly = "watch_stats_nightly"
)

// RetentionDays bounds the days a run may rebuild: raw events are kept 90
// days, so an older day would lose its event-based watch time.
const RetentionDays = 90

// BatchSize is the number of users rebuilt per transaction.
const BatchSize = 200

// CandidatePage is the number of candidate users read per WatchStatsUsers
// call. The candidate query scans the whole window (every play_request and
// watch_history update in it) whatever the page size, so reading it once
// per BatchSize users made a run quadratic: 590 scans and 564 s for 118k
// active users at production volume.
const CandidatePage = 20000

// Local days of any user differ from UTC days by at most these offsets
// (UTC-12 … UTC+14).
const (
	maxEast = 14 * time.Hour
	maxWest = 12 * time.Hour
)

// User is one candidate user of a run.
type User struct {
	ID int64
	TZ string // user_settings.timezone ('' without a settings row)
}

// Inputs are the sources of a batch of users.
type Inputs struct {
	Plays   []Play
	History []HistoryRow
	Meta    ReleaseMeta
}

// Store is what the job needs from the database (internal/store/watchstats.go).
type Store interface {
	// WatchStatsUsers returns, by id after afterID, up to limit users with a
	// play_request or a watch_history update in [from, to) or a stored row
	// on one of days, leaving out accounts pending deletion and users with
	// history_paused.
	WatchStatsUsers(ctx context.Context, from, to time.Time, days []time.Time, afterID int64, limit int) ([]User, error)
	// WatchStatsInputs loads the plays requested in [from, to) whose release
	// is still in the user's watch history, the watch_history rows updated
	// in [from, to), and the performers and tags of their releases.
	WatchStatsInputs(ctx context.Context, userIDs []int64, from, to time.Time) (Inputs, error)
	// ReplaceWatchDays replaces the rows of users on days with rows, in one
	// transaction.
	ReplaceWatchDays(ctx context.Context, userIDs []int64, days []time.Time, rows []Day) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// Runner executes one watch_stats or watch_stats_nightly run.
type Runner struct {
	Store Store
	Now   func() time.Time
	JobID int64
	// Kind selects the days (JobKind or JobKindNightly); Days overrides them
	// (cmd/audit -watch-stats -watch-stats-days N).
	Kind string
	Days []time.Time
}

// Report summarizes one run.
type Report struct {
	Kind       string `json:"kind"`
	FromDay    string `json:"from_day"`
	ToDay      string `json:"to_day"`
	Users      int    `json:"users"`
	Batches    int    `json:"batches"`
	Rows       int    `json:"rows"`
	Plays      int    `json:"plays"`
	DurationMS int64  `json:"duration_ms"`
}

// UTCDate is t's UTC calendar date at midnight.
func UTCDate(t time.Time) time.Time {
	return DateOf(t, time.UTC)
}

// HourlyDays are the local dates that are "today" for some time zone at
// now: the UTC date and the dates either side.
func HourlyDays(now time.Time) []time.Time {
	d := UTCDate(now)
	return []time.Time{d.AddDate(0, 0, -1), d, d.AddDate(0, 0, 1)}
}

// NightlyDays are the three closed dates before HourlyDays.
func NightlyDays(now time.Time) []time.Time {
	d := UTCDate(now)
	return []time.Time{d.AddDate(0, 0, -4), d.AddDate(0, 0, -3), d.AddDate(0, 0, -2)}
}

// BackfillDays are the n dates before the UTC date of now plus one (the
// eastmost "today"), and that date, never older than the event retention
// (at most RetentionDays+1 dates).
func BackfillDays(now time.Time, n int) []time.Time {
	if n > RetentionDays {
		n = RetentionDays
	}
	if n < 1 {
		n = 1
	}
	end := UTCDate(now).AddDate(0, 0, 1)
	out := make([]time.Time, 0, n+1)
	for d := end.AddDate(0, 0, -n); !d.After(end); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return clampDays(out, now)
}

// clampDays drops days older than the event retention.
func clampDays(days []time.Time, now time.Time) []time.Time {
	floor := UTCDate(now).AddDate(0, 0, -(RetentionDays - 1))
	out := days[:0:0]
	for _, d := range days {
		if !d.Before(floor) {
			out = append(out, d)
		}
	}
	return out
}

// Window is the UTC range whose plays and history rows can touch the local
// days: from the earliest start of the first day (UTC+14), less one capped
// play, to the latest end of the last day (UTC-12).
func Window(days []time.Time) (from, to time.Time) {
	first, last := days[0], days[0]
	for _, d := range days {
		if d.Before(first) {
			first = d
		}
		if d.After(last) {
			last = d
		}
	}
	from = first.Add(-maxEast).Add(-MaxPlaySeconds * time.Second)
	to = last.AddDate(0, 0, 1).Add(maxWest)
	return from, to
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run rebuilds the run's days for every candidate user, BatchSize users per
// transaction, recording a nightly_runs row named after the kind.
func (r *Runner) Run(ctx context.Context) (rep Report, err error) {
	start := r.now()
	kind := r.Kind
	if kind == "" {
		kind = JobKind
	}
	rep.Kind = kind
	days := r.Days
	if len(days) == 0 {
		if kind == JobKindNightly {
			days = NightlyDays(start)
		} else {
			days = HourlyDays(start)
		}
	}
	days = clampDays(days, start)
	defer func() {
		rep.DurationMS = r.now().Sub(start).Milliseconds()
		run := audit.Run{Job: kind, JobID: r.JobID, StartedAt: start, DurationMS: rep.DurationMS,
			Checked: rep.Users, Fixed: rep.Rows, Result: audit.ResultOK,
			Detail: map[string]any{"from_day": rep.FromDay, "to_day": rep.ToDay, "plays": rep.Plays}}
		if err != nil {
			run.Result = audit.ResultFailed
			run.Detail["error"] = err.Error()
		}
		_ = r.Store.RecordRun(ctx, run)
	}()
	if len(days) == 0 {
		return rep, nil
	}
	from, to := Window(days)
	rep.FromDay, rep.ToDay = days[0].Format(time.DateOnly), days[len(days)-1].Format(time.DateOnly)
	var after int64
	for {
		if err = ctx.Err(); err != nil {
			return rep, err
		}
		page, err := r.Store.WatchStatsUsers(ctx, from, to, days, after, CandidatePage)
		if err != nil {
			return rep, fmt.Errorf("users: %w", err)
		}
		for rest := page; len(rest) > 0; {
			if err = ctx.Err(); err != nil {
				return rep, err
			}
			users := rest[:min(len(rest), BatchSize)]
			rest = rest[len(users):]
			if err = r.runBatch(ctx, users, from, to, days, &rep); err != nil {
				return rep, err
			}
		}
		if len(page) < CandidatePage {
			return rep, nil
		}
		after = page[len(page)-1].ID
	}
}

// runBatch rebuilds days for one batch of users in one transaction.
func (r *Runner) runBatch(ctx context.Context, users []User, from, to time.Time, days []time.Time, rep *Report) error {
	ids := make([]int64, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	in, err := r.Store.WatchStatsInputs(ctx, ids, from, to)
	if err != nil {
		return fmt.Errorf("inputs: %w", err)
	}
	rows := BuildBatch(users, in, days)
	if err = r.Store.ReplaceWatchDays(ctx, ids, days, rows); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	rep.Users += len(users)
	rep.Batches++
	rep.Rows += len(rows)
	rep.Plays += len(in.Plays)
	return nil
}

// BuildBatch builds the rows of every user in a batch, each in their own
// time zone (UTC when unset or unknown).
func BuildBatch(users []User, in Inputs, days []time.Time) []Day {
	plays := map[int64][]Play{}
	for _, p := range in.Plays {
		plays[p.UserID] = append(plays[p.UserID], p)
	}
	hist := map[int64][]HistoryRow{}
	for _, h := range in.History {
		hist[h.UserID] = append(hist[h.UserID], h)
	}
	var out []Day
	for _, u := range users {
		loc, name := Location(u.TZ)
		out = append(out, BuildUserDays(u.ID, loc, name, plays[u.ID], hist[u.ID], in.Meta, days)...)
	}
	return out
}
