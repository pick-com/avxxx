// Package sitemetrics builds the AdminDashboard site KPIs (PRD 11 A1): plays,
// daily active users, watch time and completion rate per UTC day, stored in
// site_daily_metrics (migration 047) by the sourceless 'site_metrics' job
// (cmd/audit, enqueued hourly by cmd/scheduler).
//
// Raw events are kept 90 days (PRD 14.5); the daily rows are kept long-term,
// so a run only rebuilds days that lie fully inside the event retention and
// never touches older rows. Definitions (all per UTC day):
//
//   - plays: distinct play_id of play_request events requested that day.
//   - dau: distinct visitors with any event that day — the signed-in user,
//     else the device id, else (no analytics consent: neither is sent) the
//     session id, so visitors without consent count once per session.
//   - signed_in: distinct signed-in users with any event that day.
//   - watch_seconds: per play, play_end.watched_seconds, else the time from
//     play_request to its last heartbeat, capped at MaxPlaySeconds; counted on
//     the day the play was requested.
//   - completed_plays: plays with play_end reason=completed or a progress
//     milestone of 100. Completion rate = completed_plays / plays.
package sitemetrics

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// JobKind is jobs.kind of the site metrics job.
const JobKind = "site_metrics"

// RetentionDays is the raw event retention: older days cannot be rebuilt.
const RetentionDays = 90

// MaxPlaySeconds caps one play's watch time (a tab left open, clock skew).
const MaxPlaySeconds = 6 * 3600

// Day is one site_daily_metrics row.
type Day struct {
	Day            time.Time `json:"day"` // midnight UTC
	Plays          int64     `json:"plays"`
	DAU            int64     `json:"dau"`
	SignedIn       int64     `json:"signed_in"`
	WatchSeconds   int64     `json:"watch_seconds"`
	CompletedPlays int64     `json:"completed_plays"`
	ComputedAt     time.Time `json:"computed_at"`
}

// CompletionRate is completed_plays / plays, false without plays.
func (d Day) CompletionRate() (float64, bool) {
	if d.Plays <= 0 {
		return 0, false
	}
	return float64(d.CompletedPlays) / float64(d.Plays), true
}

// Store is what the job needs from the database (internal/store/sitemetrics.go).
type Store interface {
	// SiteMetricsDays returns how many site_daily_metrics rows are stored.
	SiteMetricsDays(ctx context.Context) (int, error)
	// RebuildSiteDays recomputes and upserts the rows of days from events,
	// one row per day (zeros for a day without events), in one statement.
	RebuildSiteDays(ctx context.Context, days []time.Time) ([]Day, error)
	RecordRun(ctx context.Context, r audit.Run) error
}

// UTCDate is t's UTC calendar date at midnight.
func UTCDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// Floor is the oldest day a run at now may rebuild: the first day that starts
// after the retention cut-off (now - RetentionDays), so the day is complete.
func Floor(now time.Time) time.Time {
	return UTCDate(now).AddDate(0, 0, -(RetentionDays - 1))
}

// HourlyDays are the days an ordinary run rebuilds: yesterday (late events
// around midnight) and today (partial).
func HourlyDays(now time.Time) []time.Time {
	d := UTCDate(now)
	return []time.Time{d.AddDate(0, 0, -1), d}
}

// BackfillDays are the last n days up to today, never older than Floor.
func BackfillDays(now time.Time, n int) []time.Time {
	if n < 1 {
		n = 1
	}
	if n > RetentionDays {
		n = RetentionDays
	}
	today := UTCDate(now)
	out := make([]time.Time, 0, n)
	for d := today.AddDate(0, 0, -(n - 1)); !d.After(today); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return Clamp(out, now)
}

// Clamp drops days before Floor (raw events already purged) and after today,
// sorted and without duplicates.
func Clamp(days []time.Time, now time.Time) []time.Time {
	floor, today := Floor(now), UTCDate(now)
	seen := map[time.Time]bool{}
	out := make([]time.Time, 0, len(days))
	for _, d := range days {
		d = UTCDate(d)
		if d.Before(floor) || d.After(today) || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// Runner executes one site_metrics run.
type Runner struct {
	Store Store
	Now   func() time.Time
	JobID int64
	// Days overrides the days (cmd/audit -site-metrics -site-metrics-days N).
	Days []time.Time
}

// Report summarizes one run.
type Report struct {
	FromDay    string `json:"from_day"`
	ToDay      string `json:"to_day"`
	Days       int    `json:"days"`
	Backfill   bool   `json:"backfill"`
	Plays      int64  `json:"plays"`
	DurationMS int64  `json:"duration_ms"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run rebuilds the run's days and records a nightly_runs row 'site_metrics'.
// The first run on an empty table backfills the whole retention window.
func (r *Runner) Run(ctx context.Context) (rep Report, err error) {
	start := r.now()
	defer func() {
		rep.DurationMS = r.now().Sub(start).Milliseconds()
		run := audit.Run{Job: JobKind, JobID: r.JobID, StartedAt: start, DurationMS: rep.DurationMS,
			Checked: rep.Days, Fixed: rep.Days, Result: audit.ResultOK,
			Detail: map[string]any{"from_day": rep.FromDay, "to_day": rep.ToDay, "plays": rep.Plays, "backfill": rep.Backfill}}
		if err != nil {
			run.Result = audit.ResultFailed
			run.Fixed = 0
			run.Detail["error"] = err.Error()
		}
		_ = r.Store.RecordRun(ctx, run)
	}()
	days := r.Days
	if len(days) == 0 {
		n, cerr := r.Store.SiteMetricsDays(ctx)
		if cerr != nil {
			return rep, fmt.Errorf("count: %w", cerr)
		}
		if n == 0 {
			days, rep.Backfill = BackfillDays(start, RetentionDays), true
		} else {
			days = HourlyDays(start)
		}
	}
	days = Clamp(days, start)
	if len(days) == 0 {
		return rep, nil
	}
	rep.FromDay, rep.ToDay = days[0].Format(time.DateOnly), days[len(days)-1].Format(time.DateOnly)
	rows, err := r.Store.RebuildSiteDays(ctx, days)
	if err != nil {
		return rep, fmt.Errorf("rebuild: %w", err)
	}
	rep.Days = len(rows)
	for _, d := range rows {
		rep.Plays += d.Plays
	}
	return rep, nil
}

// KPI is one dashboard number: the latest closed day's value with its
// day-over-day and 7-day comparisons.
type KPI struct {
	Value     *float64 `json:"value"`
	PrevDay   *float64 `json:"prev_day"`
	Prev7DAvg *float64 `json:"prev_7d_avg"`
	// DoD / Vs7D are relative changes (0.12 = +12 %), nil when the base is
	// missing or 0.
	DoD  *float64 `json:"dod"`
	Vs7D *float64 `json:"vs_7d"`
}

// KPIs are the four dashboard numbers.
type KPIs struct {
	Plays          KPI `json:"plays"`
	DAU            KPI `json:"dau"`
	WatchSeconds   KPI `json:"watch_seconds"`
	CompletionRate KPI `json:"completion_rate"`
}

// Summarize computes the KPIs for latest (normally yesterday, the last
// closed day) from rows: the day before is the day-over-day base and the
// mean of the stored days among the 7 before latest the 7-day base.
func Summarize(rows []Day, latest time.Time) KPIs {
	by := map[time.Time]Day{}
	for _, d := range rows {
		by[UTCDate(d.Day)] = d
	}
	latest = UTCDate(latest)
	metric := func(f func(Day) (float64, bool)) KPI {
		var k KPI
		if d, ok := by[latest]; ok {
			if v, ok := f(d); ok {
				k.Value = &v
			}
		}
		if d, ok := by[latest.AddDate(0, 0, -1)]; ok {
			if v, ok := f(d); ok {
				k.PrevDay = &v
			}
		}
		var sum float64
		var n int
		for i := 1; i <= 7; i++ {
			if d, ok := by[latest.AddDate(0, 0, -i)]; ok {
				if v, ok := f(d); ok {
					sum += v
					n++
				}
			}
		}
		if n > 0 {
			avg := sum / float64(n)
			k.Prev7DAvg = &avg
		}
		k.DoD = change(k.Value, k.PrevDay)
		k.Vs7D = change(k.Value, k.Prev7DAvg)
		return k
	}
	count := func(f func(Day) int64) func(Day) (float64, bool) {
		return func(d Day) (float64, bool) { return float64(f(d)), true }
	}
	return KPIs{
		Plays:          metric(count(func(d Day) int64 { return d.Plays })),
		DAU:            metric(count(func(d Day) int64 { return d.DAU })),
		WatchSeconds:   metric(count(func(d Day) int64 { return d.WatchSeconds })),
		CompletionRate: metric(Day.CompletionRate),
	}
}

// change is (v - base) / base rounded to 4 decimals; nil when either is
// missing or base is 0.
func change(v, base *float64) *float64 {
	if v == nil || base == nil || *base == 0 {
		return nil
	}
	c := math.Round((*v-*base) / *base * 10000) / 10000
	return &c
}
