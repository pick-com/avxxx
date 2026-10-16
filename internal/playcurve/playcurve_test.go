package playcurve

import (
	"avxxx/internal/events"
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)

// table builds a replay table with count c in buckets [from, to).
func table(from, to, c int) [Buckets]int {
	var t [Buckets]int
	for b := from; b < to; b++ {
		t[b] = c
	}
	return t
}

func enc(t [Buckets]int) string {
	var r events.ReplayTable
	for i, v := range t {
		r[i] = uint8(v)
	}
	return events.EncodeBuckets(r)
}

func TestBuilderAssemblesPlays(t *testing.T) {
	b := Builder{Version: func(src int64) string {
		if src == 9 {
			return "alt:9"
		}
		return "main"
	}}
	first, rest := table(0, 50, 1), table(40, 100, 1)
	b.Add(Row{PlayID: "p1", Event: "heartbeat", TSClient: t0.Add(5 * time.Minute), Buckets: enc(first)})
	b.Add(Row{PlayID: "p1", Event: "play_request", TSServer: t0.Add(time.Minute), UserID: 7, DeviceID: "d", SessionID: "s", SourceID: 3})
	b.Add(Row{PlayID: "p1", Event: "play_request", TSServer: t0, UserID: 7, DeviceID: "d", SessionID: "s", SourceID: 3}) // earlier resend wins
	b.Add(Row{PlayID: "p1", Event: "play_end", Buckets: enc(rest), WatchedSeconds: 600})
	b.Add(Row{PlayID: "p1", Event: "quality_change", Mode: "auto"})
	b.Add(Row{PlayID: "p2", Event: "play_request", TSServer: t0, SourceID: 9})
	b.Add(Row{PlayID: "p2", Event: "heartbeat", TSClient: t0})
	b.Add(Row{PlayID: "p2", Event: "heartbeat", TSClient: t0.Add(3 * time.Hour)})
	b.Add(Row{PlayID: "p2", Event: "seek", FromMS: 1, ToMS: 5})
	b.Add(Row{PlayID: "p2", Event: "play_end", Buckets: "eJzLSM3JyQcABiwCFQ=="}) // legacy, ignored
	b.Add(Row{PlayID: "p3", Event: "play_end", Buckets: enc(first)})             // no play_request
	b.Add(Row{PlayID: "p4", Event: "play_request", TSServer: t0})
	b.Add(Row{PlayID: "p4", Event: "source_switch", Mode: "manual"})

	plays := b.Plays()
	if len(plays) != 3 {
		t.Fatalf("plays = %d, want 3", len(plays))
	}
	p1, p2, p4 := plays[0], plays[1], plays[2]
	if !p1.StartedAt.Equal(t0) || p1.UserID != 7 || p1.VersionKey != "main" || !p1.Reported || p1.Interacted {
		t.Errorf("p1 = %+v", p1)
	}
	if p1.Table[0] != 1 || p1.Table[45] != 2 || p1.Table[99] != 1 || p1.Table[100] != 0 {
		t.Errorf("p1 table not summed: %v", p1.Table[:101])
	}
	if p1.Continuous != 10*time.Minute {
		t.Errorf("p1 continuous = %v", p1.Continuous)
	}
	if p2.VersionKey != "alt:9" || !p2.Interacted || p2.Continuous != 3*time.Hour || len(p2.Seeks) != 1 || !p2.Reported {
		t.Errorf("p2 = %+v", p2)
	}
	if b.BadTables != 1 {
		t.Errorf("bad tables = %d", b.BadTables)
	}
	if p4.Reported || !p4.Interacted {
		t.Errorf("p4 = %+v", p4)
	}
}

func TestBuilderCapsBucketSum(t *testing.T) {
	var b Builder
	b.Add(Row{PlayID: "p", Event: "play_request"})
	for range 3 {
		b.Add(Row{PlayID: "p", Event: "heartbeat", Buckets: enc(table(0, 200, 200))})
	}
	if got := b.Plays()[0].Table[0]; got != CountCap {
		t.Fatalf("count = %d, want %d", got, CountCap)
	}
}

func play(id string, user int64, device string, at time.Time, tab [Buckets]int) Play {
	return Play{PlayID: id, VersionKey: "main", UserID: user, DeviceID: device, SessionID: "s-" + id, StartedAt: at, Reported: true, Table: tab, Interacted: true}
}

func TestFilterDedupeAndCaps(t *testing.T) {
	tab := table(0, 200, 1)
	var plays []Play
	// User 1: 5 plays on day 1 on 5 devices, 1 play on day 2.
	for i := range 5 {
		plays = append(plays, play(fmt.Sprintf("u1-%d", i), 1, fmt.Sprintf("dev-u1-%d", i), t0.Add(time.Duration(i)*time.Minute), tab))
	}
	plays = append(plays, play("u1-day2", 1, "dev-u1-0", t0.Add(24*time.Hour), tab))
	// Device X: 4 plays by 4 different users on day 1.
	for i := range 4 {
		plays = append(plays, play(fmt.Sprintf("dx-%d", i), int64(100+i), "dev-x", t0.Add(time.Duration(i)*time.Minute), tab))
	}
	// Anonymous: 4 plays in one session, 2 in separate sessions.
	for i := range 4 {
		p := play(fmt.Sprintf("anon-%d", i), 0, "", t0, tab)
		p.SessionID = "same"
		plays = append(plays, p)
	}
	plays = append(plays, play("anon-a", 0, "", t0, tab), play("anon-b", 0, "", t0, tab))
	// Duplicate play_id and an unreported play.
	plays = append(plays, play("u1-0", 1, "dev-u1-0", t0, tab))
	unrep := play("lost", 50, "dev-lost", t0, tab)
	unrep.Reported = false
	plays = append(plays, unrep)

	out, st := Filter(plays)
	want := Stats{Plays: 17, Unreported: 1, Capped: 2 + 1 + 1, Valid: 3 + 1 + 3 + 3 + 2}
	if st != want {
		t.Fatalf("stats = %+v, want %+v", st, want)
	}
	if len(out) != st.Valid {
		t.Fatalf("out = %d", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i].StartedAt.Before(out[i-1].StartedAt) {
			t.Fatal("output not in play_request order")
		}
	}
	// The cap keeps the earliest plays.
	kept := map[string]bool{}
	for _, p := range out {
		kept[p.PlayID] = true
	}
	for _, id := range []string{"u1-0", "u1-1", "u1-2", "u1-day2", "dx-0", "dx-1", "dx-2"} {
		if !kept[id] {
			t.Errorf("%s dropped", id)
		}
	}
	for _, id := range []string{"u1-3", "u1-4", "dx-3", "lost"} {
		if kept[id] {
			t.Errorf("%s kept", id)
		}
	}
}

func TestFilterBots(t *testing.T) {
	tab := table(0, 200, 1)
	bot := play("bot", 0, "dev-bot", t0, tab)
	bot.Interacted = false
	bot.Continuous = 2*time.Hour + time.Second
	sameDevice := play("bot-other", 9, "dev-bot", t0.Add(time.Hour), tab) // interacted, but the device is a bot
	longButActive := play("active", 0, "dev-a", t0, tab)
	longButActive.Continuous = 5 * time.Hour
	exactly2h := play("2h", 0, "dev-b", t0, tab)
	exactly2h.Interacted = false
	exactly2h.Continuous = 2 * time.Hour
	anonBot := play("anon-bot", 0, "", t0, tab)
	anonBot.Interacted = false
	anonBot.Continuous = 3 * time.Hour
	anonOK := play("anon-ok", 0, "", t0, tab)

	out, st := Filter([]Play{bot, sameDevice, longButActive, exactly2h, anonBot, anonOK})
	if st.Bot != 3 || st.Valid != 3 || len(out) != 3 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestAggregateCounts(t *testing.T) {
	var plays []Play
	// 3 plays: watch 0-100 once; watch 0-200 with 50-60 twice; seek 100 -> 180 (skips 101-179 except what was watched).
	plays = append(plays, play("a", 1, "da", t0, table(0, 100, 1)))
	b := table(0, 200, 1)
	for i := 50; i < 60; i++ {
		b[i] = 2
	}
	plays = append(plays, play("b", 2, "db", t0, b))
	c := table(0, 100, 1)
	for i := 180; i < 200; i++ {
		c[i] = 1
	}
	c[150] = 1 // came back to 150
	pc := play("c", 3, "dc", t0, c)
	// duration 200 s → bucket = 1 s. from 100.5 s (bucket 100) to 180.2 s (bucket 180).
	pc.Seeks = []Seek{{FromMS: 100_500, ToMS: 180_200}, {FromMS: 190_000, ToMS: 20_000}}
	plays = append(plays, pc)
	alt := play("d", 4, "dd", t0, table(0, 200, 3))
	alt.VersionKey = "alt:9"
	plays = append(plays, alt)

	res, st := Aggregate(plays, map[string]int{"main": 200_000})
	if st.Valid != 4 || len(res) != 2 {
		t.Fatalf("stats %+v, %d results", st, len(res))
	}
	if res[0].VersionKey != "alt:9" || res[1].VersionKey != "main" {
		t.Fatalf("order: %s, %s", res[0].VersionKey, res[1].VersionKey)
	}
	a, m := res[0], res[1]
	if a.ValidPlays != 1 || a.Viewers[0] != 1 || a.Replays[0] != 1 || a.Skips != nil || a.DurationMS != 0 {
		t.Errorf("alt = %+v", a)
	}
	if m.ValidPlays != 3 || m.Viewers[0] != 3 || m.Viewers[150] != 2 || m.Viewers[120] != 1 || m.Replays[55] != 1 || m.Replays[0] != 0 {
		t.Errorf("main viewers/replays wrong: v0=%d v150=%d v120=%d r55=%d", m.Viewers[0], m.Viewers[150], m.Viewers[120], m.Replays[55])
	}
	if m.Skips == nil {
		t.Fatal("main skips nil")
	}
	for bk := range Buckets {
		want := 0
		if bk > 100 && bk < 180 && bk != 150 {
			want = 1
		}
		if m.Skips[bk] != want {
			t.Fatalf("skips[%d] = %d, want %d", bk, m.Skips[bk], want)
		}
	}
	if m.Retention[0] != RetentionScale || m.Retention[150] != 6667 || m.Retention[120] != 3333 {
		t.Errorf("retention = %d %d %d", m.Retention[0], m.Retention[150], m.Retention[120])
	}
	if m.ReplayPeak != nil {
		t.Error("replay peak below the threshold")
	}
}

func TestAggregateThreshold(t *testing.T) {
	mk := func(n int) []Play {
		plays := make([]Play, n)
		for i := range plays {
			tab := table(0, 200, 1)
			if i%4 == 0 {
				tab[100] = 2
			}
			plays[i] = play(fmt.Sprint(i), int64(i+1), fmt.Sprint("d", i), t0, tab)
		}
		return plays
	}
	res, _ := Aggregate(mk(MinValidPlays-1), nil)
	if res[0].ReplayPeak != nil {
		t.Fatal("curve with 999 plays")
	}
	res, _ = Aggregate(mk(MinValidPlays), nil)
	p := res[0].ReplayPeak
	if len(p) != Buckets || p[100] != CurveMax || p[99] != CurveMax || p[101] != CurveMax || p[98] != 0 || p[0] != 0 {
		t.Fatalf("peak = %v", p)
	}
	// Plenty of plays but no replay at all: no curve.
	plays := mk(MinValidPlays)
	for i := range plays {
		plays[i].Table = table(0, 200, 1)
	}
	if res, _ = Aggregate(plays, nil); res[0].ReplayPeak != nil {
		t.Fatal("flat curve returned")
	}
}

func TestReplayPeakTrimSmoothNormalize(t *testing.T) {
	var viewers, replays [Buckets]int
	for b := range viewers {
		viewers[b] = 100
	}
	// Huge replay rates in the trimmed head and tail must not matter.
	for b := range TrimHead {
		replays[b] = 100
	}
	for b := Buckets - TrimTail; b < Buckets; b++ {
		replays[b] = 100
	}
	replays[TrimHead] = 30 // first kept bucket
	replays[100] = 60
	replays[101] = 30
	// A bucket with too few viewers is ignored.
	viewers[150], replays[150] = MinBucketViewers-1, MinBucketViewers-1

	rates := ReplayRates(viewers, replays)
	if rates[150] != 0 || rates[100] != 0.6 {
		t.Fatalf("rates: %v %v", rates[150], rates[100])
	}
	tr := Trim(rates)
	for b := range Buckets {
		if (b < TrimHead || b >= Buckets-TrimTail) && tr[b] != 0 {
			t.Fatalf("bucket %d not trimmed", b)
		}
	}
	sm := SmoothKept(tr)
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-9 }
	// Range edge averages only kept neighbours: (0.3 + 0) / 2.
	if !near(sm[TrimHead], 0.15) || !near(sm[TrimHead-1], 0) {
		t.Errorf("edge smoothing: %v %v", sm[TrimHead], sm[TrimHead-1])
	}
	if !near(sm[100], 0.3) || !near(sm[101], 0.3) || !near(sm[99], 0.2) || !near(sm[102], 0.1) {
		t.Errorf("smoothing: %v %v %v %v", sm[99], sm[100], sm[101], sm[102])
	}
	p := ReplayPeak(viewers, replays)
	if p[100] != 255 || p[101] != 255 || p[99] != 170 || p[102] != 85 || p[TrimHead] != 128 || p[0] != 0 || p[Buckets-1] != 0 {
		t.Errorf("peak: %v", p)
	}
}

func TestPeakCurve(t *testing.T) {
	d := make([]int16, Buckets)
	r := make([]int16, Buckets)
	d[10], r[10] = 255, 0
	r[20] = 255
	got := PeakCurve(d, r)
	// 0.7*255 = 178.5 at 10, 0.3*255 = 76.5 at 20; renormalized → 255, 109.
	if got[10] != 255 || got[20] != 109 || got[0] != 0 {
		t.Fatalf("mix = %d %d", got[10], got[20])
	}
	if c := PeakCurve(d, nil); c[10] != 255 || &c[0] == &d[0] {
		t.Fatal("danmaku only")
	}
	if c := PeakCurve(nil, r); c[20] != 255 {
		t.Fatal("replay only")
	}
	if PeakCurve(nil, nil) != nil || PeakCurve(d[:5], nil) != nil {
		t.Fatal("none")
	}
}

func TestIntroSkipSuggested(t *testing.T) {
	skips := make([]int, Buckets)
	for b := 2; b < 15; b++ {
		skips[b] = 300
	}
	if !IntroSkipSuggested(skips, 1000) {
		t.Error("concentrated intro skips not flagged")
	}
	if IntroSkipSuggested(skips, 2000) { // only 15% of plays
		t.Error("small skip rate flagged")
	}
	if IntroSkipSuggested(skips, 50) {
		t.Error("too few plays flagged")
	}
	spread := make([]int, Buckets)
	copy(spread, skips)
	for b := 50; b < 150; b++ {
		spread[b] = 100
	}
	if IntroSkipSuggested(spread, 1000) {
		t.Error("spread skips flagged")
	}
	if IntroSkipSuggested(make([]int, Buckets), 1000) || IntroSkipSuggested(nil, 1000) {
		t.Error("no skips flagged")
	}
}

type fakeStore struct {
	wm       time.Time
	since    time.Time
	ids      []int64
	plays    map[int64][]Play
	failPlay int64
	saved    map[int64][]Result
	setWM    []time.Time
}

func (f *fakeStore) CurveWatermark(context.Context) (time.Time, error) { return f.wm, nil }
func (f *fakeStore) SetCurveWatermark(_ context.Context, t time.Time) error {
	f.setWM = append(f.setWM, t)
	return nil
}
func (f *fakeStore) ReleasesWithNewPlays(_ context.Context, since, _, _ time.Time) ([]int64, error) {
	f.since = since
	return f.ids, nil
}
func (f *fakeStore) ReleasePlays(_ context.Context, id int64, _ time.Time) ([]Play, map[string]int, error) {
	if id == f.failPlay {
		return nil, nil, errors.New("boom")
	}
	return f.plays[id], map[string]int{"main": 100_000}, nil
}
func (f *fakeStore) SaveReleaseCurves(_ context.Context, id int64, rs []Result) error {
	if f.saved == nil {
		f.saved = map[int64][]Result{}
	}
	f.saved[id] = rs
	return nil
}

func TestRunner(t *testing.T) {
	now := t0.Add(48 * time.Hour)
	f := &fakeStore{ids: []int64{1, 2}, plays: map[int64][]Play{1: {play("x", 1, "d", t0, table(0, 10, 1))}}}
	rep, err := (&Runner{Store: f, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !f.since.Equal(now.Add(-DefaultWindow)) {
		t.Errorf("first run since = %v", f.since)
	}
	if rep.Releases != 2 || rep.Versions != 1 || rep.ValidPlays != 1 || len(f.saved[2]) != 0 || len(f.setWM) != 1 || !f.setWM[0].Equal(now) {
		t.Errorf("rep = %+v, saved = %v, wm = %v", rep, f.saved, f.setWM)
	}

	f.wm = now
	f.failPlay = 2
	later := now.Add(time.Hour)
	_, err = (&Runner{Store: f, Now: func() time.Time { return later }}).Run(context.Background())
	if err == nil {
		t.Fatal("error swallowed")
	}
	if !f.since.Equal(now.Add(-Slack)) {
		t.Errorf("since = %v", f.since)
	}
	if len(f.setWM) != 1 {
		t.Error("watermark advanced after a failure")
	}
}
