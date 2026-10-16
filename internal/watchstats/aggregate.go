// Package watchstats builds 观看统计 (WatchStats, PRD 20, tech plan 3.13):
// the per-user, per-local-day aggregate user_watch_daily and the owner-only
// summary behind GET /api/me/watch-stats.
//
// Inputs are play events (play_request → play_end / heartbeat, joined by
// play_id; events carry a user id only with analytics consent) and
// watch_history (one row per user and release, so a rewatch moves it to the
// newer day). A play counts only while its release is still in the user's
// watch history: deleting an entry or clearing the history keeps it out of
// later rebuilds, and plays during a history pause (which writes no
// watch_history) never count. A history row with no play event for that
// release in the 24 h before it was last updated stands in for a play
// (users without analytics consent): its progress, capped at the duration,
// is the estimated watch time.
package watchstats

import (
	"sort"
	"time"
)

// FinishRatio is the share of a release that counts as watched to the end:
// the watch-later auto-remove rule (store.LaterFinishRatio, tech plan 3.13).
const FinishRatio = 0.9

// MaxPlaySeconds caps one play's watch time (a tab left open, clock skew).
const MaxPlaySeconds = 6 * 3600

// FallbackWindow is how far before a watch_history update a play event of
// the same release must start for the history row not to count on its own.
const FallbackWindow = 24 * time.Hour

// TopN bounds the stored top performers and tags per day.
const TopN = 10

// Play is one play from the event stream: its play_request and the watch
// time from play_end.watched_seconds (else play_request → last heartbeat).
type Play struct {
	UserID    int64
	ReleaseID int64
	Start     time.Time
	Seconds   int
	// Finished: play_end reason=completed or a progress milestone of 100.
	Finished bool
}

// HistoryRow is a watch_history row. Duration is duration_seconds, else the
// catalog runtime (0 = unknown).
type HistoryRow struct {
	UserID    int64
	ReleaseID int64
	WatchedAt time.Time
	Progress  int
	Duration  int
}

// Finished reports whether the row reaches FinishRatio of a known duration.
func (h HistoryRow) Finished() bool {
	return h.Duration > 0 && float64(h.Progress) >= FinishRatio*float64(h.Duration)
}

// ReleaseMeta lists the performers and tags (release_tags.normalized_tag)
// of the releases in a batch.
type ReleaseMeta struct {
	Performers map[int64][]int64
	Tags       map[int64][]string
}

// PerformerSeconds is one stored top performer.
type PerformerSeconds struct {
	ID      int64 `json:"id"`
	Seconds int   `json:"seconds"`
}

// TagSeconds is one stored top tag.
type TagSeconds struct {
	Tag     string `json:"tag"`
	Seconds int    `json:"seconds"`
}

// Day is one user_watch_daily row.
type Day struct {
	UserID     int64
	Day        time.Time // midnight UTC of the local calendar date
	TZ         string
	Seconds    int
	Plays      int
	Titles     int
	Finished   int
	ByHour     [24]int
	Performers []PerformerSeconds
	Tags       []TagSeconds
}

// DateOf returns the calendar date of t in loc as midnight UTC, the form
// days are keyed and stored by.
func DateOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// DayStart returns the instant local day (a DateOf value) starts in loc.
func DayStart(day time.Time, loc *time.Location) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// Spread splits seconds of watching from start into local (day, hour)
// slices in loc and calls fn for each non-empty one, in time order.
func Spread(start time.Time, seconds int, loc *time.Location, fn func(day time.Time, hour, secs int)) {
	t := start.In(loc)
	left := seconds
	for left > 0 {
		next := time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
		if !next.After(t) { // DST fold: step a whole hour
			next = t.Add(time.Hour)
		}
		n := int(next.Sub(t) / time.Second)
		if n <= 0 {
			n = 1
		}
		if n > left {
			n = left
		}
		fn(DateOf(t, loc), t.Hour(), n)
		left -= n
		t = t.Add(time.Duration(n) * time.Second).In(loc)
	}
}

type dayAcc struct {
	seconds  int
	plays    int
	byHour   [24]int
	titles   map[int64]bool
	finished map[int64]bool
	release  map[int64]int // watch seconds per release
}

// BuildUserDays aggregates one user's plays and history rows into rows for
// the requested local days (DateOf values) in loc (tzName is stored with
// them). Days without any activity are omitted. Plays and history rows of
// other users are ignored.
func BuildUserDays(userID int64, loc *time.Location, tzName string, plays []Play, hist []HistoryRow, meta ReleaseMeta, days []time.Time) []Day {
	want := make(map[time.Time]bool, len(days))
	for _, d := range days {
		want[d.UTC()] = true
	}
	acc := map[time.Time]*dayAcc{}
	get := func(d time.Time) *dayAcc {
		a := acc[d]
		if a == nil {
			a = &dayAcc{titles: map[int64]bool{}, finished: map[int64]bool{}, release: map[int64]int{}}
			acc[d] = a
		}
		return a
	}
	add := func(rel int64, start time.Time, secs int, finished bool) {
		if secs < 0 {
			secs = 0
		}
		if secs > MaxPlaySeconds {
			secs = MaxPlaySeconds
		}
		if d := DateOf(start, loc); want[d] {
			a := get(d)
			a.plays++
			a.titles[rel] = true
			if finished {
				a.finished[rel] = true
			}
		}
		Spread(start, secs, loc, func(d time.Time, hour, n int) {
			if !want[d] {
				return
			}
			a := get(d)
			a.seconds += n
			a.byHour[hour] += n
			a.release[rel] += n
		})
	}

	starts := map[int64][]time.Time{}
	for _, p := range plays {
		if p.UserID != userID {
			continue
		}
		starts[p.ReleaseID] = append(starts[p.ReleaseID], p.Start)
		add(p.ReleaseID, p.Start, p.Seconds, p.Finished)
	}
	for _, h := range hist {
		if h.UserID != userID {
			continue
		}
		var cover time.Time
		for _, s := range starts[h.ReleaseID] {
			if !s.After(h.WatchedAt) && h.WatchedAt.Sub(s) <= FallbackWindow && s.After(cover) {
				cover = s
			}
		}
		if !cover.IsZero() {
			// The plays carry the time; the history row can still mark the
			// release finished, on the day its latest covering play started
			// (where it is already a title, so it is never counted twice).
			if d := DateOf(cover, loc); want[d] && h.Finished() {
				get(d).finished[h.ReleaseID] = true
			}
			continue
		}
		secs := h.Progress
		if h.Duration > 0 && secs > h.Duration {
			secs = h.Duration
		}
		if secs > MaxPlaySeconds {
			secs = MaxPlaySeconds
		}
		start := h.WatchedAt.Add(-time.Duration(secs) * time.Second)
		// The estimated play is counted on the day it was last watched.
		if d := DateOf(h.WatchedAt, loc); want[d] {
			a := get(d)
			a.plays++
			a.titles[h.ReleaseID] = true
			if h.Finished() {
				a.finished[h.ReleaseID] = true
			}
		}
		Spread(start, secs, loc, func(d time.Time, hour, n int) {
			if !want[d] {
				return
			}
			a := get(d)
			a.seconds += n
			a.byHour[hour] += n
			a.release[h.ReleaseID] += n
		})
	}

	out := make([]Day, 0, len(acc))
	for d, a := range acc {
		if a.seconds == 0 && a.plays == 0 && len(a.finished) == 0 {
			continue
		}
		row := Day{UserID: userID, Day: d, TZ: tzName, Seconds: a.seconds, Plays: a.plays,
			Titles: len(a.titles), Finished: len(a.finished), ByHour: a.byHour}
		if row.Seconds > 86400 { // overlapping plays (two tabs): cap at a day
			scale := 86400.0 / float64(row.Seconds)
			row.Seconds = 0
			for h := range row.ByHour {
				row.ByHour[h] = int(float64(row.ByHour[h]) * scale)
				row.Seconds += row.ByHour[h]
			}
		}
		perf, tags := map[int64]int{}, map[string]int{}
		for rel, n := range a.release {
			for _, p := range meta.Performers[rel] {
				perf[p] += n
			}
			for _, t := range meta.Tags[rel] {
				tags[t] += n
			}
		}
		row.Performers = topPerformers(perf, TopN)
		row.Tags = topTags(tags, TopN)
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day.Before(out[j].Day) })
	return out
}

func topPerformers(m map[int64]int, n int) []PerformerSeconds {
	out := make([]PerformerSeconds, 0, len(m))
	for id, s := range m {
		if s > 0 {
			out = append(out, PerformerSeconds{ID: id, Seconds: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func topTags(m map[string]int, n int) []TagSeconds {
	out := make([]TagSeconds, 0, len(m))
	for t, s := range m {
		if s > 0 {
			out = append(out, TagSeconds{Tag: t, Seconds: s})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seconds != out[j].Seconds {
			return out[i].Seconds > out[j].Seconds
		}
		return out[i].Tag < out[j].Tag
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// Location resolves a stored IANA zone name; empty or unknown is UTC.
func Location(tz string) (*time.Location, string) {
	if tz == "" {
		return time.UTC, "UTC"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return time.UTC, "UTC"
	}
	return loc, tz
}
