package audit

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"
)

// fakeStats is the in-memory R-STATS side of fakeStore.
type fakeStats struct {
	reviewLikes []CounterValue // id order; Recomputed is the review_likes truth
	likeFixes   int            // FixReviewLikes calls (one transaction each)

	danmaku       map[int64][]CounterValue // release → versions
	danmakuQueued map[int64]bool
	rebuiltCurves []int64

	searchOldest  time.Time
	searchHas     bool
	searchDays    map[string][]CounterValue // day → counters
	rebuiltDays   []string
	searchSanity  []StatsViolation
	searchChecked int
	sanityBefore  time.Time

	boards  map[string]BoardSnapshot   // board/period
	recount map[string]map[int64]int64 // board/period

	curveChecked int
	curveBad     []StatsViolation
}

func (f *fakeStats) ReviewLikeCounts(_ context.Context, after int64, limit int) ([]CounterValue, error) {
	var out []CounterValue
	for _, v := range f.reviewLikes {
		if v.EntityID > after && len(out) < limit {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeStats) FixReviewLikes(_ context.Context, vals []CounterValue) (int, error) {
	f.likeFixes++
	n := 0
	for _, v := range vals {
		for i := range f.reviewLikes {
			if f.reviewLikes[i].EntityID == v.EntityID && f.reviewLikes[i].Stored == v.Stored {
				f.reviewLikes[i].Stored = v.Recomputed
				n++
			}
		}
	}
	return n, nil
}

func (f *fakeStats) DanmakuCurveReleases(_ context.Context, after int64, limit int) ([]int64, error) {
	var ids []int64
	for id := range f.danmaku {
		if id > after && !f.danmakuQueued[id] {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (f *fakeStats) DanmakuValidCounts(_ context.Context, id int64) ([]CounterValue, error) {
	return append([]CounterValue(nil), f.danmaku[id]...), nil
}

func (f *fakeStats) RebuildDanmakuCurves(_ context.Context, id int64) error {
	f.rebuiltCurves = append(f.rebuiltCurves, id)
	for i := range f.danmaku[id] {
		f.danmaku[id][i].Stored = f.danmaku[id][i].Recomputed
	}
	return nil
}

func (f *fakeStats) OldestEvent(context.Context, string) (time.Time, bool, error) {
	return f.searchOldest, f.searchHas, nil
}

func (f *fakeStats) SearchDailyCounts(_ context.Context, day time.Time) ([]CounterValue, error) {
	return append([]CounterValue(nil), f.searchDays[day.Format(time.DateOnly)]...), nil
}

func (f *fakeStats) RebuildSearchDay(_ context.Context, day time.Time) error {
	k := day.Format(time.DateOnly)
	f.rebuiltDays = append(f.rebuiltDays, k)
	for i := range f.searchDays[k] {
		f.searchDays[k][i].Stored = f.searchDays[k][i].Recomputed
	}
	return nil
}

func (f *fakeStats) SearchDailySanity(_ context.Context, before time.Time, limit int) (int, int, []StatsViolation, error) {
	f.sanityBefore = before
	ex := f.searchSanity
	if len(ex) > limit {
		ex = ex[:limit]
	}
	return f.searchChecked, len(f.searchSanity), ex, nil
}

func (f *fakeStats) BoardSnapshot(_ context.Context, board, period string) (BoardSnapshot, bool, error) {
	b, ok := f.boards[board+"/"+period]
	return b, ok, nil
}

func (f *fakeStats) BoardRecount(_ context.Context, board, period string, _ time.Time) (map[int64]int64, error) {
	return f.recount[board+"/"+period], nil
}

func (f *fakeStats) ReleaseCurveSanity(_ context.Context, limit int) (int, int, []StatsViolation, error) {
	return f.curveChecked, len(f.curveBad), f.curveBad, nil
}

func TestCounterDrift(t *testing.T) {
	vals := []CounterValue{{EntityID: 1, Stored: 3, Recomputed: 3}, {EntityID: 2, Stored: 5, Recomputed: 4},
		{EntityID: 3, Stored: 0, Recomputed: 1}, {EntityID: 4}}
	got := CounterDrift(vals)
	want := []CounterValue{{EntityID: 2, Stored: 5, Recomputed: 4}, {EntityID: 3, Stored: 0, Recomputed: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("drift = %+v", got)
	}
	if CounterDrift(nil) != nil || CounterDrift(vals[:1]) != nil {
		t.Fatal("no drift should be nil")
	}
}

func TestSearchRebuildDays(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	day := func(s string) time.Time { x, _ := time.Parse(time.DateOnly, s); return x }
	cases := []struct {
		name     string
		oldest   time.Time
		has      bool
		from, to string
		ok       bool
	}{
		// Retention cut-off is 2026-07-18 12:00: the 18th is partial.
		{"window bound", day("2026-01-01"), true, "2026-07-19", "2026-10-14", true},
		// The oldest event's day may be partial.
		{"oldest event", time.Date(2026, 8, 1, 5, 0, 0, 0, time.UTC), true, "2026-08-02", "2026-10-14", true},
		{"no events", time.Time{}, false, "", "", false},
		{"only recent events", time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC), true, "", "", false},
	}
	for _, c := range cases {
		from, to, ok := SearchRebuildDays(now, DefaultEventRetention, c.oldest, c.has)
		if ok != c.ok {
			t.Errorf("%s: ok = %v", c.name, ok)
			continue
		}
		if ok && (!from.Equal(day(c.from)) || !to.Equal(day(c.to))) {
			t.Errorf("%s: %s..%s, want %s..%s", c.name, from.Format(time.DateOnly), to.Format(time.DateOnly), c.from, c.to)
		}
	}
	// A cut-off exactly at midnight keeps that day.
	from, _, _ := SearchRebuildDays(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), DefaultEventRetention, time.Time{}, true)
	if !from.Equal(day("2026-07-18")) {
		t.Errorf("midnight cut-off from = %s", from)
	}
}

func TestBoardMonotonic(t *testing.T) {
	month := map[int64]int64{1: 10, 2: 5, 3: 7}
	year := map[int64]int64{1: 12, 2: 4, 4: 1} // 2 lost plays; 3 is not on the year board
	got := BoardMonotonic(month, year)
	if len(got) != 1 || got[0].EntityID != 2 {
		t.Fatalf("violations = %+v", got)
	}
}

func statsFake(now time.Time) *fakeStore {
	f := newFake()
	f.reviewLikes = []CounterValue{
		{EntityID: 1, Stored: 2, Recomputed: 2},
		{EntityID: 2, Stored: 9, Recomputed: 3}, // drift
		{EntityID: 3, Stored: 0, Recomputed: 0},
		{EntityID: 4, Stored: 0, Recomputed: 1}, // drift, second page
		{EntityID: 5, Stored: 1, Recomputed: 1},
	}
	f.danmaku = map[int64][]CounterValue{
		10: {{EntityID: 10, Key: "main", Stored: 60, Recomputed: 60}},
		11: {{EntityID: 11, Key: "main", Stored: 50, Recomputed: 48}, {EntityID: 11, Key: "alt:3", Stored: 2, Recomputed: 2}},
		12: {{EntityID: 12, Key: "main", Stored: 1, Recomputed: 7}}, // queued: the curve job will catch up
	}
	f.danmakuQueued = map[int64]bool{12: true}
	f.searchHas = true
	f.searchOldest = now.AddDate(0, 0, -10)
	day := func(n int) string { return now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -n).Format(time.DateOnly) }
	f.searchDays = map[string][]CounterValue{
		day(5): {{Key: "abc||searches", Stored: 4, Recomputed: 6}, {Key: "abc||clicks", Stored: 1, Recomputed: 1}},
		// Rows but no events for the day: reported, never emptied.
		day(4): {{Key: "lost||searches", Stored: 3, Recomputed: 0}},
		day(3): {{Key: "ok||searches", Stored: 2, Recomputed: 2}},
		// Yesterday is still re-aggregated by the search job: not checked.
		day(1): {{Key: "late||searches", Stored: 1, Recomputed: 2}},
	}
	f.searchChecked = 40
	f.searchSanity = []StatsViolation{{Key: "2026-01-02|x|", Problem: "clicks 3 > searches 2"}}
	at := now.Add(-30 * time.Minute)
	f.boards = map[string]BoardSnapshot{
		"trending/today":      {Board: BoardTrending, Period: "today", ComputedAt: at, Window: 24 * time.Hour, Plays: map[int64]int64{10: 5, 11: 3, 99: 1}},
		"trending/month":      {Board: BoardTrending, Period: "month", ComputedAt: at, Window: 30 * 24 * time.Hour, Plays: map[int64]int64{10: 40, 11: 9}},
		"trending/year":       {Board: BoardTrending, Period: "year", ComputedAt: at, Window: 365 * 24 * time.Hour, Plays: map[int64]int64{10: 100, 11: 8}},
		"performer_heat/week": {Board: BoardHeat, Period: "week", ComputedAt: at, Window: 7 * 24 * time.Hour, Plays: map[int64]int64{7: 2}},
	}
	f.recount = map[string]map[int64]int64{
		"trending/today":      {10: 5, 11: 4}, // 11 drifted; 99 left the scope
		"trending/month":      {10: 40, 11: 9},
		"performer_heat/week": {7: 2},
	}
	f.curveChecked = 6
	f.curveBad = []StatsViolation{{EntityID: 10, Key: "main", Problem: "replays above viewers"}}
	return f
}

func TestRuleStats(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	f := statsFake(now)
	r := &Runner{Store: f, Now: func() time.Time { return now }, BatchSize: 2, Rules: []Rule{RuleStats}}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 1 {
		t.Fatalf("runs = %+v", rep.Runs)
	}
	run := rep.Runs[0]
	if run.Job != "audit.R-STATS" || run.Result != ResultIssues {
		t.Fatalf("run = %+v", run)
	}

	// reviews.likes: both drifts fixed, one transaction per page that drifted.
	for _, v := range f.reviewLikes {
		if v.Drifted() {
			t.Errorf("review %d still drifted: %+v", v.EntityID, v)
		}
	}
	if f.likeFixes != 2 {
		t.Errorf("FixReviewLikes calls = %d, want 2 (pages 1 and 2)", f.likeFixes)
	}
	// danmaku: release 11 rebuilt, queued 12 skipped.
	if !reflect.DeepEqual(f.rebuiltCurves, []int64{11}) {
		t.Errorf("rebuilt curves = %v", f.rebuiltCurves)
	}
	if f.danmaku[12][0].Stored != 1 {
		t.Error("queued release was touched")
	}
	// search: day -5 rebuilt; day -4 (no events) left; yesterday not checked.
	d5 := now.Truncate(24*time.Hour).AddDate(0, 0, -5).Format(time.DateOnly)
	if !reflect.DeepEqual(f.rebuiltDays, []string{d5}) {
		t.Errorf("rebuilt days = %v", f.rebuiltDays)
	}
	if want := now.AddDate(0, 0, -9).Truncate(24 * time.Hour); !f.sanityBefore.Equal(want) {
		t.Errorf("sanity before %s, want %s", f.sanityBefore, want)
	}

	counters := run.Detail["counters"].(map[string]*counterReport)
	check := func(name string, checked, drifted, fixed, reported int) {
		t.Helper()
		c := counters[name]
		if c == nil {
			t.Fatalf("%s missing", name)
		}
		if c.Checked != checked || c.Drifted != drifted || c.Fixed != fixed || c.Reported != reported {
			t.Errorf("%s = checked %d drifted %d fixed %d reported %d, want %d %d %d %d", name,
				c.Checked, c.Drifted, c.Fixed, c.Reported, checked, drifted, fixed, reported)
		}
	}
	check(CounterReviewLikes, 5, 2, 2, 0)
	check(CounterDanmakuValid, 3, 1, 1, 0)
	check(CounterSearchDaily, 4+40, 2, 1, 1+1) // lost day + one sanity violation
	// today: 2 compared (99 left scope), 1 drift; month: 2 compared; year: 2 on the board, 1 below month.
	check(CounterTrendingPlays, 2+2+2, 1, 0, 1+1)
	check(CounterHeatPlays, 1, 0, 0, 0)
	check(CounterReleaseCurves, 6, 0, 0, 1)
	if run.Fixed != 4 || run.Issues != 5 || run.Checked != 5+3+44+6+1+6 {
		t.Errorf("run totals checked %d fixed %d issues %d", run.Checked, run.Fixed, run.Issues)
	}
	if ex := counters[CounterReviewLikes].Examples; len(ex) != 2 || ex[0].EntityID != 2 || ex[0].Stored != 9 || ex[0].Recomputed != 3 {
		t.Errorf("likes examples = %+v", ex)
	}
	if s := counters[CounterTrendingPlays].Sanity; len(s) != 1 || s[0].EntityID != 11 {
		t.Errorf("year sanity = %+v", s)
	}
	if run.Detail["not_stored"] == nil || run.Detail["event_retention_days"] != 90 {
		t.Errorf("detail = %+v", run.Detail)
	}

	// A second run finds nothing left to fix.
	f.searchSanity, f.curveBad = nil, nil
	delete(f.searchDays, now.Truncate(24*time.Hour).AddDate(0, 0, -4).Format(time.DateOnly))
	f.boards["trending/today"].Plays[11] = 4
	f.boards["trending/year"].Plays[11] = 9
	rep, err = r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run := rep.Runs[0]; run.Result != ResultOK || run.Fixed != 0 || run.Issues != 0 {
		t.Errorf("second run = %+v", run)
	}
}

func TestRuleStatsSkipsBoardsPastRetention(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC)
	f := newFake()
	f.boards = map[string]BoardSnapshot{
		"trending/month": {Board: BoardTrending, Period: "month", ComputedAt: now, Window: 30 * 24 * time.Hour, Plays: map[int64]int64{1: 4}},
	}
	f.recount = map[string]map[int64]int64{"trending/month": {1: 9}}
	r := &Runner{Store: f, Now: func() time.Time { return now }, Rules: []Rule{RuleStats}, EventRetention: 7 * 24 * time.Hour}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := rep.Runs[0].Detail["counters"].(map[string]*counterReport)[CounterTrendingPlays]
	if c.Checked != 0 || c.Reported != 0 {
		t.Errorf("month board checked past retention: %+v", c)
	}
	if rep.Runs[0].Result != ResultOK {
		t.Errorf("result = %s", rep.Runs[0].Result)
	}
}
