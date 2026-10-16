package watchstats

import (
	"sort"
	"time"
	_ "time/tzdata" // settings.timezone in images without zoneinfo
)

// Ranges are the GET /api/me/watch-stats ranges in days.
var Ranges = map[string]int{"7d": 7, "30d": 30, "365d": 365}

// StreakDays is how far back the longest streak is looked for.
const StreakDays = 365

// SummaryTop bounds the top performers and genres of a summary.
const SummaryTop = 8

// Totals are the stat cards of a range.
type Totals struct {
	Seconds  int `json:"seconds"`
	Plays    int `json:"plays"`
	Titles   int `json:"titles"`
	Finished int `json:"finished"`
	// CompletionRate is finished / titles (0 without titles).
	CompletionRate float64 `json:"completion_rate"`
	ActiveDays     int     `json:"active_days"`
}

// Streak is the run of consecutive days with watch time ending today (or
// yesterday, when nothing was watched yet today) and the longest run in
// the last StreakDays days.
type Streak struct {
	Current int `json:"current"`
	Longest int `json:"longest"`
}

// DayPoint is one bar of the daily series.
type DayPoint struct {
	Day      string `json:"day"` // YYYY-MM-DD
	Seconds  int    `json:"seconds"`
	Finished int    `json:"finished"`
}

// Summary is the range summary before names and images are attached.
type Summary struct {
	Range  string `json:"range"`
	From   string `json:"from"`
	To     string `json:"to"`
	TZ     string `json:"tz"`
	Totals Totals `json:"totals"`
	// Heatmap is watch seconds by local weekday (0 = Monday … 6 = Sunday)
	// and hour.
	Heatmap    [7][24]int         `json:"heatmap"`
	Streak     Streak             `json:"streak"`
	Daily      []DayPoint         `json:"daily"`
	Performers []PerformerSeconds `json:"-"`
	Tags       []TagSeconds       `json:"-"`
	// HasData is false when the user has no row at all (empty state).
	HasData bool `json:"has_data"`
}

// Weekday returns 0 for Monday … 6 for Sunday.
func Weekday(d time.Time) int { return (int(d.Weekday()) + 6) % 7 }

// Heatmap sums each day's by-hour seconds into weekday × hour cells.
func Heatmap(days []Day) [7][24]int {
	var out [7][24]int
	for _, d := range days {
		w := Weekday(d.Day)
		for h, s := range d.ByHour {
			out[w][h] += s
		}
	}
	return out
}

// Streaks computes the current and longest streaks of days with watch time
// up to today (a DateOf value). Rows after today are ignored.
func Streaks(days []Day, today time.Time) Streak {
	active := map[time.Time]bool{}
	var dates []time.Time
	for _, d := range days {
		if d.Seconds <= 0 || d.Day.After(today) || active[d.Day] {
			continue
		}
		active[d.Day] = true
		dates = append(dates, d.Day)
	}
	sort.Slice(dates, func(i, j int) bool { return dates[i].Before(dates[j]) })
	var st Streak
	run := 0
	var prev time.Time
	for i, d := range dates {
		if i > 0 && d.Equal(prev.AddDate(0, 0, 1)) {
			run++
		} else {
			run = 1
		}
		if run > st.Longest {
			st.Longest = run
		}
		prev = d
	}
	end := today
	if !active[end] {
		end = today.AddDate(0, 0, -1)
	}
	for d := end; active[d]; d = d.AddDate(0, 0, -1) {
		st.Current++
	}
	return st
}

// Summarize builds the summary of the rangeDays days ending today from
// rows (which may reach back StreakDays for the streak). hasData reports
// whether the user has any row at all.
func Summarize(rangeName string, rows []Day, today time.Time, tz string, hasData bool) Summary {
	n := Ranges[rangeName]
	if n == 0 {
		rangeName, n = "7d", 7
	}
	from := today.AddDate(0, 0, -(n - 1))
	s := Summary{Range: rangeName, From: from.Format(time.DateOnly), To: today.Format(time.DateOnly), TZ: tz, HasData: hasData}
	s.Streak = Streaks(rows, today)
	byDay := map[time.Time]Day{}
	var in []Day
	perf, tags := map[int64]int{}, map[string]int{}
	for _, d := range rows {
		if d.Day.Before(from) || d.Day.After(today) {
			continue
		}
		in = append(in, d)
		byDay[d.Day] = d
		s.Totals.Seconds += d.Seconds
		s.Totals.Plays += d.Plays
		s.Totals.Titles += d.Titles
		s.Totals.Finished += d.Finished
		if d.Seconds > 0 {
			s.Totals.ActiveDays++
		}
		for _, p := range d.Performers {
			perf[p.ID] += p.Seconds
		}
		for _, t := range d.Tags {
			tags[t.Tag] += t.Seconds
		}
	}
	if s.Totals.Titles > 0 {
		s.Totals.CompletionRate = float64(s.Totals.Finished) / float64(s.Totals.Titles)
		if s.Totals.CompletionRate > 1 {
			s.Totals.CompletionRate = 1
		}
	}
	s.Heatmap = Heatmap(in)
	s.Daily = make([]DayPoint, 0, n)
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		row := byDay[d]
		s.Daily = append(s.Daily, DayPoint{Day: d.Format(time.DateOnly), Seconds: row.Seconds, Finished: row.Finished})
	}
	s.Performers = topPerformers(perf, SummaryTop)
	s.Tags = topTags(tags, SummaryTop)
	return s
}
