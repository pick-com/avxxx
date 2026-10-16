package watchstats

import (
	"avxxx/internal/audit"
	"context"
	"reflect"
	"testing"
	"time"
)

func date(s string) time.Time {
	d, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return d
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestDateOfAcrossTimezones(t *testing.T) {
	instant := at("2026-10-15T23:30:00Z")
	for _, tc := range []struct {
		tz   string
		want string
	}{
		{"UTC", "2026-10-15"},
		{"Asia/Tokyo", "2026-10-16"},          // 08:30 next day
		{"America/Los_Angeles", "2026-10-15"}, // 16:30
		{"Pacific/Kiritimati", "2026-10-16"},  // UTC+14: 13:30
		{"Pacific/Pago_Pago", "2026-10-15"},   // UTC-11: 12:30
		{"Asia/Kolkata", "2026-10-16"},        // UTC+5:30: 05:00
	} {
		if got := DateOf(instant, mustLoc(t, tc.tz)).Format(time.DateOnly); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.tz, got, tc.want)
		}
	}
}

func TestSpreadAcrossMidnightAndHalfHourZone(t *testing.T) {
	// 23:30 → 01:15 local in Tokyo: 30 min on day 1 hour 23, 60 min on
	// day 2 hour 0, 15 min on day 2 hour 1.
	tokyo := mustLoc(t, "Asia/Tokyo")
	type slice struct {
		day        string
		hour, secs int
	}
	var got []slice
	Spread(at("2026-10-15T14:30:00Z"), 105*60, tokyo, func(d time.Time, h, n int) {
		got = append(got, slice{d.Format(time.DateOnly), h, n})
	})
	want := []slice{{"2026-10-15", 23, 1800}, {"2026-10-16", 0, 3600}, {"2026-10-16", 1, 900}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tokyo %v, want %v", got, want)
	}

	// Kolkata (UTC+5:30): hour boundaries are local, not UTC-aligned.
	got = nil
	Spread(at("2026-10-15T00:00:00Z"), 3600, mustLoc(t, "Asia/Kolkata"), func(d time.Time, h, n int) {
		got = append(got, slice{d.Format(time.DateOnly), h, n})
	})
	want = []slice{{"2026-10-15", 5, 1800}, {"2026-10-15", 6, 1800}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("kolkata %v, want %v", got, want)
	}
}

func TestBuildUserDaysBucketsByUserTimezone(t *testing.T) {
	// One play at 2026-10-15 23:00 UTC for 2 h, finished.
	plays := []Play{{UserID: 1, ReleaseID: 10, Start: at("2026-10-15T23:00:00Z"), Seconds: 7200, Finished: true}}
	meta := ReleaseMeta{Performers: map[int64][]int64{10: {7, 8}}, Tags: map[int64][]string{10: {"drama"}}}
	days := []time.Time{date("2026-10-15"), date("2026-10-16")}

	utc := BuildUserDays(1, time.UTC, "UTC", plays, nil, meta, days)
	if len(utc) != 2 {
		t.Fatalf("utc rows %+v", utc)
	}
	if d := utc[0]; d.Day != date("2026-10-15") || d.Seconds != 3600 || d.Plays != 1 || d.Finished != 1 || d.Titles != 1 || d.ByHour[23] != 3600 {
		t.Errorf("utc day 1 %+v", d)
	}
	if d := utc[1]; d.Day != date("2026-10-16") || d.Seconds != 3600 || d.Plays != 0 || d.Finished != 0 || d.ByHour[0] != 3600 {
		t.Errorf("utc day 2 %+v", d)
	}
	if want := []PerformerSeconds{{7, 3600}, {8, 3600}}; !reflect.DeepEqual(utc[0].Performers, want) {
		t.Errorf("performers %+v", utc[0].Performers)
	}
	if want := []TagSeconds{{"drama", 3600}}; !reflect.DeepEqual(utc[1].Tags, want) {
		t.Errorf("tags %+v", utc[1].Tags)
	}

	// In Tokyo the whole play is on the 16th (08:00–10:00).
	tokyo := BuildUserDays(1, mustLoc(t, "Asia/Tokyo"), "Asia/Tokyo", plays, nil, meta, days)
	if len(tokyo) != 1 || tokyo[0].Day != date("2026-10-16") || tokyo[0].Seconds != 7200 || tokyo[0].ByHour[8] != 3600 || tokyo[0].ByHour[9] != 3600 || tokyo[0].TZ != "Asia/Tokyo" {
		t.Errorf("tokyo %+v", tokyo)
	}

	// Only requested days are produced: the 16th alone in UTC.
	only := BuildUserDays(1, time.UTC, "UTC", plays, nil, meta, days[1:])
	if len(only) != 1 || only[0].Day != date("2026-10-16") || only[0].Seconds != 3600 {
		t.Errorf("one day %+v", only)
	}
}

func TestBuildUserDaysHistoryFallbackAndFinish(t *testing.T) {
	days := []time.Time{date("2026-10-15")}
	hist := []HistoryRow{
		// No play event: counts as an estimated play of its progress.
		{UserID: 1, ReleaseID: 20, WatchedAt: at("2026-10-15T12:00:00Z"), Progress: 1800, Duration: 2000},
		// Covered by a play: no extra time, but finished by the 90 % rule.
		{UserID: 1, ReleaseID: 21, WatchedAt: at("2026-10-15T15:00:00Z"), Progress: 950, Duration: 1000},
		// Progress past the duration is capped.
		{UserID: 1, ReleaseID: 22, WatchedAt: at("2026-10-15T20:00:00Z"), Progress: 5000, Duration: 600},
		// Another user's row is ignored.
		{UserID: 2, ReleaseID: 20, WatchedAt: at("2026-10-15T12:00:00Z"), Progress: 100},
	}
	plays := []Play{{UserID: 1, ReleaseID: 21, Start: at("2026-10-15T14:00:00Z"), Seconds: 600}}
	got := BuildUserDays(1, time.UTC, "UTC", plays, hist, ReleaseMeta{}, days)
	if len(got) != 1 {
		t.Fatalf("rows %+v", got)
	}
	d := got[0]
	if d.Seconds != 1800+600+600 || d.Plays != 3 || d.Titles != 3 || d.Finished != 3 {
		t.Errorf("day %+v", d)
	}
	if d.ByHour[11] != 1800 || d.ByHour[14] != 600 || d.ByHour[19] != 600 {
		t.Errorf("by hour %v", d.ByHour)
	}
	if (HistoryRow{Progress: 899, Duration: 1000}).Finished() || !(HistoryRow{Progress: 900, Duration: 1000}).Finished() || (HistoryRow{Progress: 9, Duration: 0}).Finished() {
		t.Error("90 % rule")
	}
}

func TestBuildUserDaysCapsLongPlays(t *testing.T) {
	plays := []Play{{UserID: 1, ReleaseID: 1, Start: at("2026-10-15T00:00:00Z"), Seconds: 100000}}
	got := BuildUserDays(1, time.UTC, "UTC", plays, nil, ReleaseMeta{}, []time.Time{date("2026-10-15")})
	if len(got) != 1 || got[0].Seconds != MaxPlaySeconds {
		t.Errorf("capped %+v", got)
	}
}

func dayRow(s string, secs int) Day { return Day{Day: date(s), Seconds: secs} }

func TestStreaks(t *testing.T) {
	today := date("2026-10-16")
	for _, tc := range []struct {
		name string
		rows []Day
		want Streak
	}{
		{"none", nil, Streak{}},
		{"today only", []Day{dayRow("2026-10-16", 60)}, Streak{1, 1}},
		{"through yesterday counts while today is empty",
			[]Day{dayRow("2026-10-13", 1), dayRow("2026-10-14", 1), dayRow("2026-10-15", 1)}, Streak{3, 3}},
		{"broken two days ago",
			[]Day{dayRow("2026-10-10", 1), dayRow("2026-10-11", 1), dayRow("2026-10-12", 1), dayRow("2026-10-14", 1)}, Streak{0, 3}},
		{"zero-second rows do not count",
			[]Day{dayRow("2026-10-14", 1), dayRow("2026-10-15", 0), dayRow("2026-10-16", 5)}, Streak{1, 1}},
		{"unsorted and duplicate input",
			[]Day{dayRow("2026-10-16", 1), dayRow("2026-10-14", 1), dayRow("2026-10-15", 1), dayRow("2026-10-15", 1)}, Streak{3, 3}},
		{"future rows ignored", []Day{dayRow("2026-10-17", 1), dayRow("2026-10-16", 1)}, Streak{1, 1}},
		{"across a month end",
			[]Day{dayRow("2026-09-29", 1), dayRow("2026-09-30", 1), dayRow("2026-10-01", 1), dayRow("2026-10-02", 1)}, Streak{0, 4}},
	} {
		if got := Streaks(tc.rows, today); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestHeatmap(t *testing.T) {
	mon := Day{Day: date("2026-10-12")} // Monday
	mon.ByHour[21] = 100
	sun := Day{Day: date("2026-10-18")} // Sunday
	sun.ByHour[0] = 7
	nextMon := Day{Day: date("2026-10-19")}
	nextMon.ByHour[21] = 50
	h := Heatmap([]Day{mon, sun, nextMon})
	if h[0][21] != 150 || h[6][0] != 7 {
		t.Errorf("heatmap mon21=%d sun0=%d", h[0][21], h[6][0])
	}
	sum := 0
	for _, row := range h {
		for _, v := range row {
			sum += v
		}
	}
	if sum != 157 {
		t.Errorf("sum %d", sum)
	}
	if Weekday(date("2026-10-12")) != 0 || Weekday(date("2026-10-18")) != 6 {
		t.Error("weekday mapping")
	}
}

func TestSummarize(t *testing.T) {
	today := date("2026-10-16")
	a := Day{Day: date("2026-10-16"), Seconds: 600, Plays: 2, Titles: 2, Finished: 1,
		Performers: []PerformerSeconds{{1, 400}, {2, 200}}, Tags: []TagSeconds{{"x", 600}}}
	b := Day{Day: date("2026-10-10"), Seconds: 300, Plays: 1, Titles: 1, Finished: 1,
		Performers: []PerformerSeconds{{2, 300}}}
	old := Day{Day: date("2026-09-01"), Seconds: 999, Plays: 9, Titles: 9, Finished: 9}
	s := Summarize("7d", []Day{old, b, a}, today, "UTC", true)
	if s.From != "2026-10-10" || s.To != "2026-10-16" || len(s.Daily) != 7 {
		t.Fatalf("range %s..%s %d", s.From, s.To, len(s.Daily))
	}
	if s.Totals.Seconds != 900 || s.Totals.Plays != 3 || s.Totals.Finished != 2 || s.Totals.Titles != 3 || s.Totals.ActiveDays != 2 {
		t.Errorf("totals %+v", s.Totals)
	}
	if r := s.Totals.CompletionRate; r < 0.66 || r > 0.67 {
		t.Errorf("rate %v", r)
	}
	if want := []PerformerSeconds{{2, 500}, {1, 400}}; !reflect.DeepEqual(s.Performers, want) {
		t.Errorf("performers %+v", s.Performers)
	}
	if s.Daily[0].Seconds != 300 || s.Daily[6].Seconds != 600 || s.Daily[3].Seconds != 0 {
		t.Errorf("daily %+v", s.Daily)
	}
	if s.Streak != (Streak{1, 1}) {
		t.Errorf("streak %+v", s.Streak)
	}
	if bad := Summarize("bogus", nil, today, "UTC", false); bad.Range != "7d" || bad.HasData || len(bad.Daily) != 7 {
		t.Errorf("default range %+v", bad)
	}
	if y := Summarize("365d", nil, today, "UTC", false); len(y.Daily) != 365 {
		t.Errorf("365d daily %d", len(y.Daily))
	}
}

func TestDaySelection(t *testing.T) {
	now := at("2026-10-16T03:00:00Z")
	fmtDays := func(ds []time.Time) []string {
		var out []string
		for _, d := range ds {
			out = append(out, d.Format(time.DateOnly))
		}
		return out
	}
	if got := fmtDays(HourlyDays(now)); !reflect.DeepEqual(got, []string{"2026-10-15", "2026-10-16", "2026-10-17"}) {
		t.Errorf("hourly %v", got)
	}
	if got := fmtDays(NightlyDays(now)); !reflect.DeepEqual(got, []string{"2026-10-12", "2026-10-13", "2026-10-14"}) {
		t.Errorf("nightly %v", got)
	}
	if got := BackfillDays(now, 1000); len(got) != RetentionDays+1 || got[len(got)-1] != date("2026-10-17") {
		t.Errorf("backfill clamp %d %v", len(got), got[len(got)-1])
	}
	from, to := Window([]time.Time{date("2026-10-16"), date("2026-10-15")})
	if !from.Equal(at("2026-10-14T04:00:00Z")) || !to.Equal(at("2026-10-17T12:00:00Z")) {
		t.Errorf("window %v %v", from, to)
	}
}

type fakeStore struct {
	users   []User
	inputs  Inputs
	written map[int64][]Day
	calls   []int64
	runs    []audit.Run
}

func (f *fakeStore) WatchStatsUsers(_ context.Context, _, _ time.Time, _ []time.Time, after int64, limit int) ([]User, error) {
	f.calls = append(f.calls, after)
	var out []User
	for _, u := range f.users {
		if u.ID > after && len(out) < limit {
			out = append(out, u)
		}
	}
	return out, nil
}

func (f *fakeStore) WatchStatsInputs(_ context.Context, ids []int64, _, _ time.Time) (Inputs, error) {
	return f.inputs, nil
}

func (f *fakeStore) ReplaceWatchDays(_ context.Context, ids []int64, _ []time.Time, rows []Day) error {
	for _, id := range ids {
		f.written[id] = nil
	}
	for _, r := range rows {
		f.written[r.UserID] = append(f.written[r.UserID], r)
	}
	return nil
}

func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func TestRunnerBatchesAndIsIdempotent(t *testing.T) {
	f := &fakeStore{written: map[int64][]Day{}}
	for i := int64(1); i <= BatchSize+5; i++ {
		f.users = append(f.users, User{ID: i, TZ: map[bool]string{true: "Asia/Tokyo", false: ""}[i == 1]})
	}
	f.inputs = Inputs{Plays: []Play{
		{UserID: 1, ReleaseID: 1, Start: at("2026-10-15T23:00:00Z"), Seconds: 600},
		{UserID: 2, ReleaseID: 1, Start: at("2026-10-15T23:00:00Z"), Seconds: 600},
	}}
	rn := &Runner{Store: f, Now: func() time.Time { return at("2026-10-16T03:00:00Z") }}
	for i := 0; i < 2; i++ {
		rep, err := rn.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if rep.Users != BatchSize+5 || rep.Batches != 2 || rep.Kind != JobKind {
			t.Errorf("run %d report %+v", i, rep)
		}
	}
	if len(f.written[1]) != 1 || f.written[1][0].Day != date("2026-10-16") || f.written[1][0].TZ != "Asia/Tokyo" {
		t.Errorf("tokyo user %+v", f.written[1])
	}
	if len(f.written[2]) != 1 || f.written[2][0].Day != date("2026-10-15") || f.written[2][0].TZ != "UTC" {
		t.Errorf("utc user %+v", f.written[2])
	}
	if len(f.runs) != 2 || f.runs[0].Job != JobKind || f.runs[0].Result != audit.ResultOK {
		t.Errorf("runs %+v", f.runs)
	}
	if !reflect.DeepEqual(f.calls[:2], []int64{0, BatchSize}) {
		t.Errorf("keyset calls %v", f.calls)
	}
}
