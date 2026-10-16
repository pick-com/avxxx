package sitemetrics

import (
	"avxxx/internal/audit"
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestDays(t *testing.T) {
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.FixedZone("x", 9*3600)) // 2026-10-15 18:00 UTC
	if got := HourlyDays(now); len(got) != 2 || !got[0].Equal(date("2026-10-14")) || !got[1].Equal(date("2026-10-15")) {
		t.Errorf("hourly %v", got)
	}
	if f := Floor(now); !f.Equal(date("2026-07-18")) {
		t.Errorf("floor %v", f)
	}
	all := BackfillDays(now, 500)
	if len(all) != RetentionDays || !all[0].Equal(Floor(now)) || !all[len(all)-1].Equal(date("2026-10-15")) {
		t.Errorf("backfill %d %v..%v", len(all), all[0], all[len(all)-1])
	}
	if got := BackfillDays(now, 0); len(got) != 1 {
		t.Errorf("backfill 0: %v", got)
	}
	got := Clamp([]time.Time{date("2026-10-15"), date("2026-01-01"), date("2026-10-16"), date("2026-10-14"), date("2026-10-15")}, now)
	if len(got) != 2 || !got[0].Equal(date("2026-10-14")) {
		t.Errorf("clamp %v", got)
	}
}

func TestSummarize(t *testing.T) {
	latest := date("2026-10-15")
	rows := []Day{
		{Day: latest, Plays: 120, DAU: 50, WatchSeconds: 3600, CompletedPlays: 30},
		{Day: latest.AddDate(0, 0, -1), Plays: 100, DAU: 40, WatchSeconds: 0, CompletedPlays: 20},
		{Day: latest.AddDate(0, 0, -3), Plays: 80, DAU: 60, CompletedPlays: 8},
		{Day: latest.AddDate(0, 0, -8), Plays: 9999}, // outside the 7-day base
	}
	k := Summarize(rows, latest)
	eq := func(name string, p *float64, want float64) {
		t.Helper()
		if p == nil || math.Abs(*p-want) > 1e-9 {
			t.Errorf("%s = %v, want %v", name, p, want)
		}
	}
	eq("plays", k.Plays.Value, 120)
	eq("plays prev", k.Plays.PrevDay, 100)
	eq("plays dod", k.Plays.DoD, 0.2)
	eq("plays 7d", k.Plays.Prev7DAvg, 90)
	eq("plays vs7d", k.Plays.Vs7D, 0.3333)
	eq("dau dod", k.DAU.DoD, 0.25)
	eq("completion", k.CompletionRate.Value, 0.25)
	eq("completion 7d", k.CompletionRate.Prev7DAvg, 0.15) // (0.2 + 0.1) / 2
	if k.WatchSeconds.DoD != nil {
		t.Errorf("watch dod with a zero base: %v", *k.WatchSeconds.DoD)
	}
	none := Summarize(nil, latest)
	if none.Plays.Value != nil || none.Plays.DoD != nil || none.CompletionRate.Prev7DAvg != nil {
		t.Errorf("empty: %+v", none)
	}
	// A day without plays has no completion rate.
	k = Summarize([]Day{{Day: latest}}, latest)
	if k.CompletionRate.Value != nil || k.Plays.Value == nil {
		t.Errorf("zero plays: %+v", k)
	}
}

type fakeStore struct {
	stored  int
	rebuilt []time.Time
	runs    []audit.Run
	fail    error
}

func (f *fakeStore) SiteMetricsDays(context.Context) (int, error) { return f.stored, nil }
func (f *fakeStore) RebuildSiteDays(_ context.Context, days []time.Time) ([]Day, error) {
	f.rebuilt = days
	if f.fail != nil {
		return nil, f.fail
	}
	out := make([]Day, len(days))
	for i, d := range days {
		out[i] = Day{Day: d, Plays: 2}
	}
	return out, nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func TestRunner(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	st := &fakeStore{stored: 5}
	rep, err := (&Runner{Store: st, Now: func() time.Time { return now }, JobID: 3}).Run(context.Background())
	if err != nil || rep.Backfill || rep.Days != 2 || rep.Plays != 4 || rep.FromDay != "2026-10-15" || rep.ToDay != "2026-10-16" {
		t.Fatalf("hourly: %+v %v", rep, err)
	}
	if len(st.runs) != 1 || st.runs[0].Job != JobKind || st.runs[0].JobID != 3 || st.runs[0].Result != audit.ResultOK {
		t.Errorf("run row %+v", st.runs)
	}
	st = &fakeStore{}
	rep, _ = (&Runner{Store: st, Now: func() time.Time { return now }}).Run(context.Background())
	if !rep.Backfill || len(st.rebuilt) != RetentionDays {
		t.Errorf("backfill: %+v (%d days)", rep, len(st.rebuilt))
	}
	st = &fakeStore{stored: 1, fail: errors.New("boom")}
	if _, err := (&Runner{Store: st, Now: func() time.Time { return now }}).Run(context.Background()); err == nil || st.runs[0].Result != audit.ResultFailed {
		t.Errorf("failure: %v %+v", err, st.runs)
	}
	// Explicit days outside the retention do nothing.
	st = &fakeStore{stored: 1}
	rep, _ = (&Runner{Store: st, Now: func() time.Time { return now }, Days: []time.Time{date("2025-01-01")}}).Run(context.Background())
	if rep.Days != 0 || st.rebuilt != nil {
		t.Errorf("old days: %+v %v", rep, st.rebuilt)
	}
}
