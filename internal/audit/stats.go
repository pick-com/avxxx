package audit

// R-STATS (PRD 11 rule G 统计重建): recompute the denormalized counters and
// aggregates the system stores from their sources of truth, report the
// entities whose stored value drifted, and repair the ones that are purely
// derived. Raw events are only ever read.
//
// Counters checked (StatsCounters):
//
//   - reviews.likes, from review_likes. Fully derived and unbounded in time:
//     drift is fixed in place, one bounded transaction per page of reviews,
//     and only where the stored value is still the one read (a like that
//     lands in between wins).
//   - danmaku_curves.valid_count, from the valid danmaku (visible, unmuted,
//     deduped per user and text per bucket, as the curve job counts them).
//     Drift is fixed by recomputing the release's curves (data and count
//     together), one transaction per release. Releases already queued for
//     the curve job are skipped: they are expected to differ.
//   - search_query_daily, from `search` events. Events are kept 90 days, so
//     only closed days fully inside the retention window (and after the
//     oldest search event still stored) are recomputed; a drifted day is
//     replaced in one transaction. A day whose events are all gone while
//     rows remain is reported, never emptied. Older days cannot be rebuilt
//     and get a sanity check only (clicks <= searches and the like).
//   - trending_snapshots.plays and performer_heat_snapshots.plays, from
//     play events and watch history over the board's own window. The hourly
//     recs_trending job owns these boards (a play count change moves ranks),
//     so drift is reported, not written. The year board's window (365 d)
//     reaches past the event retention: it cannot be rebuilt and only gets a
//     monotonic check against the month board computed in the same run.
//   - release_curves (viewers / replays / retention vs valid_plays). The
//     hourly release_curves job recomputes them from the whole 90-day window;
//     re-running its bot filter and daily caps here would duplicate it, so
//     the rule checks their internal sanity only and reports violations.
//
// Counters PRD screens mention that are not stored, so there is nothing to
// drift, are listed in the run detail (StatsNotStored).

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// DefaultEventRetention is how long raw events are kept (PRD 14.5).
const DefaultEventRetention = 90 * 24 * time.Hour

// Counter names used in the R-STATS run detail.
const (
	CounterReviewLikes   = "reviews.likes"
	CounterDanmakuValid  = "danmaku_curves.valid_count"
	CounterSearchDaily   = "search_query_daily"
	CounterTrendingPlays = "trending_snapshots.plays"
	CounterHeatPlays     = "performer_heat_snapshots.plays"
	CounterReleaseCurves = "release_curves"
)

// Board names for BoardSnapshot / BoardRecount.
const (
	BoardTrending = "trending"       // trending_snapshots (releases)
	BoardHeat     = "performer_heat" // performer_heat_snapshots
)

// Board periods; they must match recs.Periods. The year board is checked
// for monotonic sanity only.
var (
	statsRecountPeriods = []string{"today", "week", "month"}
	statsYearPeriod     = "year"
	statsMonthPeriod    = "month"
)

// StatsNotStored lists aggregates the PRD screens show that are computed at
// query time (or not built yet), so R-STATS has nothing to rebuild.
var StatsNotStored = map[string]string{
	"performer works / plays / followers (performer_stats)": "not stored: performer pages count release_performers, watch_history and follows at query time",
	"release ratings (average, count, distribution)":        "not stored: computed from ratings on read",
	"favorites / follows / reservations counts":             "not stored: counted from favorites, follows and reservations on read",
	"comment (review / danmaku) counts":                     "not stored: counted on read; only reviews.likes and danmaku_curves.valid_count are cached",
	"user_watch_daily":                                      "table does not exist yet (WatchStats is V1.1)",
	"release play / view counts":                            "not stored: trending boards and audit priorities count watch_history and play events per run",
}

// CounterValue is one stored counter next to its recomputed value. EntityID
// is the owning row (review, release, performer); Key names the counter
// within it when there are several (version key, "q|lang|field").
type CounterValue struct {
	EntityID   int64  `json:"entity_id,omitempty"`
	Key        string `json:"key,omitempty"`
	Stored     int64  `json:"stored"`
	Recomputed int64  `json:"recomputed"`
}

// Drifted reports whether the stored value differs from the recomputed one.
func (c CounterValue) Drifted() bool { return c.Stored != c.Recomputed }

// CounterDrift returns the values whose stored value differs from the
// recomputed one, in input order.
func CounterDrift(vals []CounterValue) []CounterValue {
	var out []CounterValue
	for _, v := range vals {
		if v.Drifted() {
			out = append(out, v)
		}
	}
	return out
}

// StatsViolation is a sanity finding on a counter that cannot be rebuilt.
type StatsViolation struct {
	EntityID int64  `json:"entity_id,omitempty"`
	Key      string `json:"key,omitempty"`
	Problem  string `json:"problem"`
}

// BoardSnapshot is the latest stored board of one period.
type BoardSnapshot struct {
	Board      string
	Period     string
	ComputedAt time.Time
	Window     time.Duration   // the period's rolling window
	Plays      map[int64]int64 // entity id → stored plays
}

// StatsStore is what R-STATS needs from the database.
type StatsStore interface {
	// ReviewLikeCounts returns reviews with id > afterID in id order, at most
	// limit: Stored = reviews.likes, Recomputed = review_likes rows.
	ReviewLikeCounts(ctx context.Context, afterID int64, limit int) ([]CounterValue, error)
	// FixReviewLikes sets reviews.likes to Recomputed where it still equals
	// Stored, in one transaction, and returns the rows changed.
	FixReviewLikes(ctx context.Context, vals []CounterValue) (int, error)
	// DanmakuCurveReleases returns release ids > afterID that have danmaku
	// or curve rows and are not queued for the curve job, at most limit.
	DanmakuCurveReleases(ctx context.Context, afterID int64, limit int) ([]int64, error)
	// DanmakuValidCounts returns one value per version key of a release:
	// Stored = danmaku_curves.valid_count (0 without a row), Recomputed =
	// the valid count the curve job would compute now.
	DanmakuValidCounts(ctx context.Context, releaseID int64) ([]CounterValue, error)
	// RebuildDanmakuCurves recomputes and saves a release's curves.
	RebuildDanmakuCurves(ctx context.Context, releaseID int64) error
	// OldestEvent returns the oldest stored ts_server of an event name.
	OldestEvent(ctx context.Context, event string) (time.Time, bool, error)
	// SearchDailyCounts returns, for one UTC day, every (q_norm, lang,
	// field) counter of search_query_daily next to its value recomputed
	// from `search` events (Key "q_norm|lang|field", 0 where a side has no
	// row).
	SearchDailyCounts(ctx context.Context, day time.Time) ([]CounterValue, error)
	// RebuildSearchDay replaces one day of search_query_daily from events,
	// in one transaction.
	RebuildSearchDay(ctx context.Context, day time.Time) error
	// SearchDailySanity checks the search_query_daily rows before day and
	// returns how many it checked and how many break the counters' order
	// (zero_results, clicks, rescued <= searches; first_page_clicks <=
	// clicks), with up to limit examples.
	SearchDailySanity(ctx context.Context, before time.Time, limit int) (checked, violations int, examples []StatsViolation, err error)
	// BoardSnapshot returns the stored board of period; ok=false when empty.
	BoardSnapshot(ctx context.Context, board, period string) (BoardSnapshot, bool, error)
	// BoardRecount recomputes the plays of a board period for the window
	// ending at at, the way the trending job does.
	BoardRecount(ctx context.Context, board, period string, at time.Time) (map[int64]int64, error)
	// ReleaseCurveSanity returns how many release_curves timelines it
	// checked and how many have viewers above valid_plays, replays above
	// viewers or retention outside 0–10000, with up to limit examples.
	ReleaseCurveSanity(ctx context.Context, limit int) (checked, violations int, examples []StatsViolation, err error)
}

// SearchRebuildDays returns the UTC days [from, to] search_query_daily can be
// rebuilt for: closed days the search job no longer re-aggregates (it redoes
// yesterday and today), that lie fully inside the event retention window and
// start after the oldest stored search event's day. ok is false when the
// range is empty.
func SearchRebuildDays(now time.Time, retention time.Duration, oldest time.Time, hasEvents bool) (from, to time.Time, ok bool) {
	if !hasEvents {
		return time.Time{}, time.Time{}, false
	}
	today := now.UTC().Truncate(24 * time.Hour)
	to = today.AddDate(0, 0, -2)
	// First day starting at or after the retention cut-off.
	cut := now.UTC().Add(-retention)
	from = cut.Truncate(24 * time.Hour)
	if from.Before(cut) {
		from = from.AddDate(0, 0, 1)
	}
	// The oldest event's day may be partial (purged up to mid-day).
	if first := oldest.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1); first.After(from) {
		from = first
	}
	return from, to, !from.After(to)
}

// BoardMonotonic compares a longer-window board with a shorter one computed
// in the same run: a release cannot have fewer plays in a year than in the
// month inside it. Only entities on both boards are compared.
func BoardMonotonic(shorter, longer map[int64]int64) []StatsViolation {
	var out []StatsViolation
	for id, s := range shorter {
		if l, ok := longer[id]; ok && l < s {
			out = append(out, StatsViolation{EntityID: id, Problem: fmt.Sprintf("year plays %d < month plays %d", l, s)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out
}

// statsExamples caps example lists in the run detail.
const statsExamples = 10

// counterReport is one counter's entry in the R-STATS run detail.
type counterReport struct {
	Checked  int              `json:"checked"`
	Drifted  int              `json:"drifted"`
	Fixed    int              `json:"fixed"`
	Reported int              `json:"reported"` // findings left for a person or the owning job
	Examples []CounterValue   `json:"examples,omitempty"`
	Sanity   []StatsViolation `json:"sanity,omitempty"`
	Window   string           `json:"window,omitempty"`
	Note     string           `json:"note,omitempty"`
}

func (c *counterReport) example(v CounterValue) {
	if len(c.Examples) < statsExamples {
		c.Examples = append(c.Examples, v)
	}
}

func (r *Runner) retention() time.Duration {
	if r.EventRetention > 0 {
		return r.EventRetention
	}
	return DefaultEventRetention
}

func (r *Runner) ruleStats(ctx context.Context, start time.Time) (ruleOutcome, error) {
	out := ruleOutcome{detail: map[string]any{}}
	counters := map[string]*counterReport{}
	out.detail["counters"] = counters
	out.detail["not_stored"] = StatsNotStored
	out.detail["event_retention_days"] = int(r.retention() / (24 * time.Hour))
	steps := []struct {
		name string
		fn   func(context.Context, time.Time) (*counterReport, error)
	}{
		{CounterReviewLikes, r.statsReviewLikes},
		{CounterDanmakuValid, r.statsDanmaku},
		{CounterSearchDaily, r.statsSearchDaily},
		{CounterTrendingPlays, func(ctx context.Context, now time.Time) (*counterReport, error) {
			return r.statsBoard(ctx, now, BoardTrending)
		}},
		{CounterHeatPlays, func(ctx context.Context, now time.Time) (*counterReport, error) {
			return r.statsBoard(ctx, now, BoardHeat)
		}},
		{CounterReleaseCurves, r.statsReleaseCurves},
	}
	for _, s := range steps {
		rep, err := s.fn(ctx, start)
		if rep != nil {
			counters[s.name] = rep
			out.checked += rep.Checked
			out.fixed += rep.Fixed
			out.issues += rep.Reported
		}
		if err != nil {
			return out, fmt.Errorf("%s: %w", s.name, err)
		}
	}
	return out, nil
}

func (r *Runner) statsReviewLikes(ctx context.Context, _ time.Time) (*counterReport, error) {
	rep := &counterReport{Window: "all time (review_likes is never purged)"}
	var after int64
	for {
		page, err := r.Store.ReviewLikeCounts(ctx, after, r.batch())
		if err != nil {
			return rep, err
		}
		rep.Checked += len(page)
		if drift := CounterDrift(page); len(drift) > 0 {
			rep.Drifted += len(drift)
			for _, v := range drift {
				rep.example(v)
			}
			n, err := r.Store.FixReviewLikes(ctx, drift)
			if err != nil {
				return rep, err
			}
			rep.Fixed += n
		}
		if len(page) < r.batch() {
			return rep, nil
		}
		after = page[len(page)-1].EntityID
	}
}

func (r *Runner) statsDanmaku(ctx context.Context, _ time.Time) (*counterReport, error) {
	rep := &counterReport{Window: "all time (danmaku is never purged)",
		Note: "releases queued for the curve job are skipped; drift is fixed by recomputing the release's curves"}
	var after int64
	for {
		ids, err := r.Store.DanmakuCurveReleases(ctx, after, r.batch())
		if err != nil {
			return rep, err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			vals, err := r.Store.DanmakuValidCounts(ctx, id)
			if err != nil {
				return rep, fmt.Errorf("release %d: %w", id, err)
			}
			rep.Checked += len(vals)
			drift := CounterDrift(vals)
			if len(drift) == 0 {
				continue
			}
			rep.Drifted += len(drift)
			for _, v := range drift {
				rep.example(v)
			}
			if err := r.Store.RebuildDanmakuCurves(ctx, id); err != nil {
				return rep, fmt.Errorf("rebuild release %d: %w", id, err)
			}
			rep.Fixed += len(drift)
		}
		if len(ids) < r.batch() {
			return rep, nil
		}
		after = ids[len(ids)-1]
	}
}

func (r *Runner) statsSearchDaily(ctx context.Context, now time.Time) (*counterReport, error) {
	rep := &counterReport{}
	oldest, has, err := r.Store.OldestEvent(ctx, "search")
	if err != nil {
		return rep, err
	}
	from, to, ok := SearchRebuildDays(now, r.retention(), oldest, has)
	sanityBefore := from
	if !ok {
		rep.Note = "no closed day with complete search events inside the retention window; older days get a sanity check only"
		sanityBefore = now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	} else {
		rep.Window = from.Format(time.DateOnly) + ".." + to.Format(time.DateOnly)
		rep.Note = "days before the window cannot be rebuilt (raw events purged after the retention period): sanity check only"
		unsafeDays := 0
		for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			vals, err := r.Store.SearchDailyCounts(ctx, day)
			if err != nil {
				return rep, fmt.Errorf("day %s: %w", day.Format(time.DateOnly), err)
			}
			rep.Checked += len(vals)
			drift := CounterDrift(vals)
			if len(drift) == 0 {
				continue
			}
			rep.Drifted += len(drift)
			for _, v := range drift {
				v.Key = day.Format(time.DateOnly) + "|" + v.Key
				rep.example(v)
			}
			// Stored rows but no events at all for the day: the events were
			// lost, not the aggregate. Never empty the day.
			var recomputed int64
			for _, v := range vals {
				recomputed += v.Recomputed
			}
			if recomputed == 0 {
				unsafeDays++
				rep.Reported += len(drift)
				continue
			}
			if err := r.Store.RebuildSearchDay(ctx, day); err != nil {
				return rep, fmt.Errorf("rebuild day %s: %w", day.Format(time.DateOnly), err)
			}
			rep.Fixed += len(drift)
		}
		if unsafeDays > 0 {
			rep.Note += fmt.Sprintf("; %d day(s) have rows but no search events and were left as they are", unsafeDays)
		}
	}
	c, n, ex, err := r.Store.SearchDailySanity(ctx, sanityBefore, statsExamples)
	if err != nil {
		return rep, err
	}
	rep.Checked += c
	rep.Reported += n
	rep.Sanity = ex
	return rep, nil
}

func (r *Runner) statsBoard(ctx context.Context, now time.Time, board string) (*counterReport, error) {
	rep := &counterReport{Note: "owned by the hourly recs_trending job: drift is reported, not written (plays drive the ranks); " +
		"the year board reaches past the event retention and is only checked against the month board (year plays >= month plays)"}
	cut := now.Add(-r.retention())
	var skipped []string
	for _, period := range statsRecountPeriods {
		snap, ok, err := r.Store.BoardSnapshot(ctx, board, period)
		if err != nil {
			return rep, err
		}
		if !ok {
			continue
		}
		if snap.ComputedAt.Add(-snap.Window).Before(cut) {
			skipped = append(skipped, period)
			continue
		}
		fresh, err := r.Store.BoardRecount(ctx, board, period, snap.ComputedAt)
		if err != nil {
			return rep, fmt.Errorf("%s: %w", period, err)
		}
		ids := make([]int64, 0, len(snap.Plays))
		for id := range snap.Plays {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			n, ok := fresh[id]
			if !ok {
				continue // left the board's scope since (unpublished, merged): the next run drops it
			}
			rep.Checked++
			v := CounterValue{EntityID: id, Key: period, Stored: snap.Plays[id], Recomputed: n}
			if v.Drifted() {
				rep.Drifted++
				rep.Reported++
				rep.example(v)
			}
		}
	}
	if len(skipped) > 0 {
		rep.Note += fmt.Sprintf("; skipped %v: window older than the event retention", skipped)
	}
	month, okM, err := r.Store.BoardSnapshot(ctx, board, statsMonthPeriod)
	if err != nil {
		return rep, err
	}
	year, okY, err := r.Store.BoardSnapshot(ctx, board, statsYearPeriod)
	if err != nil {
		return rep, err
	}
	if okM && okY && month.ComputedAt.Equal(year.ComputedAt) {
		v := BoardMonotonic(month.Plays, year.Plays)
		rep.Checked += len(year.Plays)
		rep.Reported += len(v)
		if len(v) > statsExamples {
			v = v[:statsExamples]
		}
		rep.Sanity = v
	}
	return rep, nil
}

func (r *Runner) statsReleaseCurves(ctx context.Context, _ time.Time) (*counterReport, error) {
	rep := &counterReport{Window: "last 90 days of play events (recomputed hourly by release_curves)",
		Note: "the curve job owns the rebuild (bot filter, daily caps); this rule checks viewers <= valid_plays, replays <= viewers and retention 0–10000"}
	c, n, ex, err := r.Store.ReleaseCurveSanity(ctx, statsExamples)
	if err != nil {
		return rep, err
	}
	rep.Checked = c
	rep.Reported = n
	rep.Sanity = ex
	return rep, nil
}
