package audit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSourceHealthScore(t *testing.T) {
	tests := []struct {
		attempts, playable, ffOK int64
		want                     float64
		ok                       bool
	}{
		{0, 0, 0, 0, false},
		{100, 100, 100, 100, true},
		{100, 100, 0, 70, true},
		{100, 0, 0, 0, true},
		{100, 90, 45, 78, true},     // 0.7×0.9 + 0.3×0.5 = 0.78
		{100, 95, 95, 96.5, true},   // 0.665 + 0.3
		{3, 2, 1, 61.67, true},      // 0.7×2/3 + 0.3×1/2 = 0.61667 → rounded to 0.01
		{10, 20, 30, 100, true},     // counts clamped: playable ≤ attempts, ff_ok ≤ playable
		{40, 40, 38, 98.5, true},    // 0.7 + 0.3×0.95
		{200, 190, 171, 93.5, true}, // 0.665 + 0.3×0.9
	}
	for _, tt := range tests {
		got, ok := SourceHealthScore(tt.attempts, tt.playable, tt.ffOK)
		if ok != tt.ok || got != tt.want {
			t.Errorf("SourceHealthScore(%d,%d,%d) = %v,%v want %v,%v", tt.attempts, tt.playable, tt.ffOK, got, ok, tt.want, tt.ok)
		}
	}
}

func TestRates(t *testing.T) {
	r := Rates(50, 40, 30, 8)
	if r.Playable != 0.8 || r.FirstFrameOK != 0.75 || r.Rebuffer != 0.2 {
		t.Errorf("rates %+v", r)
	}
	if r := Rates(0, 0, 0, 3); r != (HealthRates{}) {
		t.Errorf("zero rates %+v", r)
	}
}

func TestSourceBandsAndPoints(t *testing.T) {
	for score, want := range map[float64]string{0: BandSevere, 59.99: BandSevere, 60: BandDegraded, 79.9: BandDegraded,
		80: BandHealthy, 94.99: BandHealthy, 95: BandExcellent, 100: BandExcellent} {
		if got := SourceBand(score); got != want {
			t.Errorf("SourceBand(%v) = %s, want %s", score, got, want)
		}
	}
	// health_score is a real column: 0.95 comes back as 0.949999988.
	if got := ScorePoints(float64(float32(0.95))); got != 95 {
		t.Errorf("ScorePoints(float32 0.95) = %d, want 95", got)
	}
	if got := ScorePoints(0.9499); got != 94 {
		t.Errorf("ScorePoints(0.9499) = %d, want 94", got)
	}
}

func TestNextSourceStatus(t *testing.T) {
	cfg := HealthConfig{} // defaults: disable < 50, degrade < 80, recover ≥ 85
	tests := []struct {
		current string
		score   float64
		want    string
	}{
		{SourceActive, 100, SourceActive},
		{SourceActive, 80, SourceActive},
		{SourceActive, 79.99, SourceDegraded},
		{SourceActive, 50, SourceDegraded},
		{SourceActive, 49.99, SourceDisabled},
		{SourceDegraded, 82, SourceDegraded}, // hysteresis: needs 85 to recover
		{SourceDegraded, 84.99, SourceDegraded},
		{SourceDegraded, 85, SourceActive},
		{SourceDegraded, 30, SourceDisabled},
		{SourceDisabled, 100, SourceDisabled}, // never re-enabled automatically
		{SourceDisabled, 10, SourceDisabled},
	}
	for _, tt := range tests {
		if got := NextSourceStatus(tt.current, tt.score, cfg); got != tt.want {
			t.Errorf("NextSourceStatus(%s, %v) = %s, want %s", tt.current, tt.score, got, tt.want)
		}
	}
	// Custom thresholds; RecoverAt below DegradeBelow is lifted to it.
	c := HealthConfig{DegradeBelow: 90, DisableBelow: 60, RecoverAt: 70}
	if got := NextSourceStatus(SourceDegraded, 90, c); got != SourceActive {
		t.Errorf("custom recover = %s", got)
	}
	if got := NextSourceStatus(SourceActive, 59, c); got != SourceDisabled {
		t.Errorf("custom disable = %s", got)
	}
}

func TestEvaluateSourceMinimumSample(t *testing.T) {
	start, end := time.Unix(0, 0), time.Unix(86400, 0)
	few := SourceSample{SourceID: 1, Status: SourceActive, HealthScore: 0.97, Attempts: 29, Playable: 0}
	o := EvaluateSource(few, start, end, HealthConfig{})
	if !o.Insufficient || o.Band != BandInsufficient || o.StatusAfter != SourceActive || o.StatusChanged() {
		t.Errorf("29 failing attempts: %+v", o)
	}
	if o.Score == nil || *o.Score != 0 {
		t.Errorf("raw score still recorded: %v", o.Score)
	}
	none := EvaluateSource(SourceSample{SourceID: 2, Status: SourceDegraded, Rebuffers: 3}, start, end, HealthConfig{})
	if !none.Insufficient || none.Score != nil || none.StatusAfter != SourceDegraded {
		t.Errorf("no attempts: %+v", none)
	}
	enough := SourceSample{SourceID: 3, Status: SourceActive, Attempts: 30, Playable: 15, FirstFrameOK: 15}
	o = EvaluateSource(enough, start, end, HealthConfig{})
	if o.Insufficient || *o.Score != 65 || o.Band != BandDegraded || o.StatusAfter != SourceDegraded || !o.StatusChanged() {
		t.Errorf("30 attempts: %+v score %v", o, *o.Score)
	}
	if o := EvaluateSource(few, start, end, HealthConfig{MinAttempts: 10}); o.Insufficient || o.StatusAfter != SourceDisabled {
		t.Errorf("MinAttempts 10: %+v", o)
	}
}

func TestSourceIssue(t *testing.T) {
	score := 42.5
	is := SourceIssue(SourceHealthFacts{SourceID: 7, ReleaseID: 3, Status: SourceDisabled, Score: &score, Band: BandSevere, Attempts: 120}, time.Unix(5, 0))
	if is.Rule != RuleSourceHealth || is.EntityType != "playback_source" || is.EntityID != 7 || is.Severity != SeverityHigh ||
		is.Priority != 120 || is.Detail["score"] != 42.5 {
		t.Errorf("disabled issue %+v", is)
	}
	deg := SourceIssue(SourceHealthFacts{SourceID: 7, Status: SourceDegraded}, time.Unix(5, 0))
	if deg.Severity != SeverityMedium || deg.Fingerprint == is.Fingerprint {
		t.Errorf("degraded issue %+v", deg)
	}
}

// fakeHealth is an in-memory SourceHealthStore.
type fakeHealth struct {
	samples   []SourceSample
	status    map[int64]string
	health    map[int64]float64
	runs      []SourceOutcome
	events    []reviewEvent
	opened    []Issue
	resolved  []int64
	pruned    time.Time
	failApply int64
}

func (f *fakeHealth) SourceHealthSamples(_ context.Context, start, end time.Time, ms int64) ([]SourceSample, error) {
	if end.Sub(start) != 24*time.Hour || ms != 1500 {
		return nil, errors.New("unexpected window or threshold")
	}
	out := make([]SourceSample, len(f.samples))
	for i, s := range f.samples {
		s.Status, s.HealthScore = f.status[s.SourceID], f.health[s.SourceID]
		out[i] = s
	}
	return out, nil
}

func (f *fakeHealth) ApplySourceHealth(_ context.Context, o SourceOutcome) (bool, error) {
	if o.SourceID == f.failApply {
		return false, errors.New("apply failed")
	}
	f.runs = append(f.runs, o)
	if o.Insufficient {
		return false, nil
	}
	f.health[o.SourceID] = *o.Score / 100
	if o.StatusChanged() && f.status[o.SourceID] == o.Status {
		f.status[o.SourceID] = o.StatusAfter
		f.events = append(f.events, reviewEvent{"playback_source", o.SourceID, "status_change", HealthActor})
		return true, nil
	}
	return false, nil
}

func (f *fakeHealth) OpenIssue(_ context.Context, is Issue) (OpenResult, error) {
	f.opened = append(f.opened, is)
	return IssueCreated, nil
}

func (f *fakeHealth) ResolveSourceIssue(_ context.Context, id int64, _ string) error {
	f.resolved = append(f.resolved, id)
	return nil
}

func (f *fakeHealth) PruneSourceHealthRuns(_ context.Context, before time.Time) (int64, error) {
	f.pruned = before
	return 0, nil
}

func TestHealthRunnerTransitions(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	f := &fakeHealth{
		samples: []SourceSample{
			{SourceID: 1, Attempts: 100, Playable: 99, FirstFrameOK: 95},   // 98.09 → stays active
			{SourceID: 2, Attempts: 100, Playable: 80, FirstFrameOK: 40},   // 71 → degraded
			{SourceID: 3, Attempts: 50, Playable: 10, FirstFrameOK: 0},     // 14 → disabled
			{SourceID: 4, Attempts: 100, Playable: 100, FirstFrameOK: 90},  // 97 → degraded recovers
			{SourceID: 5, Attempts: 100, Playable: 100, FirstFrameOK: 100}, // disabled stays disabled
			{SourceID: 6, Attempts: 5, Playable: 0},                        // insufficient: untouched
			{SourceID: 7},                                                  // no activity: no run row
		},
		status: map[int64]string{1: SourceActive, 2: SourceActive, 3: SourceActive, 4: SourceDegraded, 5: SourceDisabled, 6: SourceActive, 7: SourceActive},
		health: map[int64]float64{1: 1, 2: 1, 3: 1, 4: 0.7, 5: 0.2, 6: 0.9, 7: 1},
	}
	rep, err := (&HealthRunner{Store: f, Now: func() time.Time { return now }, JobID: 4}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[int64]string{1: SourceActive, 2: SourceDegraded, 3: SourceDisabled, 4: SourceActive, 5: SourceDisabled, 6: SourceActive, 7: SourceActive}
	for id, st := range want {
		if f.status[id] != st {
			t.Errorf("source %d status %s, want %s", id, f.status[id], st)
		}
	}
	if f.health[6] != 0.9 || f.health[5] != 1 || f.health[2] != 0.71 {
		t.Errorf("health %v", f.health)
	}
	if rep.Sources != 7 || rep.Recorded != 6 || rep.Scored != 5 || rep.Insufficient != 1 || len(rep.Changes) != 3 {
		t.Errorf("report %+v", rep)
	}
	if len(f.events) != 3 {
		t.Errorf("review events %+v", f.events)
	}
	if len(f.opened) != 2 || f.opened[0].EntityID != 2 || f.opened[0].Severity != SeverityMedium || f.opened[1].EntityID != 3 || f.opened[1].Severity != SeverityHigh {
		t.Errorf("opened issues %+v", f.opened)
	}
	if len(f.resolved) != 1 || f.resolved[0] != 4 {
		t.Errorf("resolved %v", f.resolved)
	}
	for _, o := range f.runs {
		if o.JobID != 4 || !o.WindowEnd.Equal(now) || !o.WindowStart.Equal(now.Add(-24*time.Hour)) {
			t.Errorf("run %+v", o)
		}
	}
	if !f.pruned.Equal(now.Add(-30 * 24 * time.Hour)) {
		t.Errorf("pruned before %v", f.pruned)
	}

	// Second run with the same data: no further changes.
	f.events, f.opened, f.resolved = nil, nil, nil
	if rep, err = (&HealthRunner{Store: f, Now: func() time.Time { return now }}).Run(context.Background()); err != nil || len(rep.Changes) != 0 || len(f.events) != 0 {
		t.Errorf("rerun changes %+v events %v err %v", rep.Changes, f.events, err)
	}

	// A failing source does not stop the others; the error is returned.
	f.failApply = 2
	f.runs = nil
	if _, err = (&HealthRunner{Store: f, Now: func() time.Time { return now }}).Run(context.Background()); err == nil || len(f.runs) != 5 {
		t.Errorf("partial failure: err %v runs %d", err, len(f.runs))
	}
}

func TestRuleSource(t *testing.T) {
	f := newFake()
	r := &Runner{Store: f, Now: clock()}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run := findRun(f.runs, RuleSourceHealth); run.Result != ResultNotApplicable {
		t.Errorf("without runs: %+v", run)
	}
	score := 70.0
	f.sourceHasRuns = true
	f.sourceFacts = []SourceHealthFacts{{SourceID: 1, Status: SourceDegraded, Score: &score, Band: BandDegraded, Attempts: 40}, {SourceID: 2, Status: SourceDisabled, Attempts: 90}}
	f.runs = nil
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run := findRun(f.runs, RuleSourceHealth); run.Result != ResultIssues || run.Issues != 2 || run.Checked != 2 {
		t.Errorf("with facts: %+v", run)
	}
	if open := f.open(RuleSourceHealth); len(open) != 2 || open[1].Severity != SeverityHigh {
		t.Errorf("open issues %+v", open)
	}
	// Source 1 recovered: the next run resolves its issue.
	f.sourceFacts = f.sourceFacts[1:]
	f.runs = nil
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run := findRun(f.runs, RuleSourceHealth); run.Fixed != 1 || len(f.open(RuleSourceHealth)) != 1 {
		t.Errorf("after recovery: %+v open %d", run, len(f.open(RuleSourceHealth)))
	}
}

func findRun(runs []Run, rule Rule) Run {
	for _, r := range runs {
		if r.Job == "audit."+string(rule) {
			return r
		}
	}
	return Run{}
}

func TestSourceHealthSummaryPooled(t *testing.T) {
	var s SourceHealthSummary
	s.SetPooled(0, 0, 0)
	if s.Score != nil || s.Band != "" {
		t.Errorf("empty %+v", s)
	}
	s.SetPooled(1000, 990, 960) // 0.693 + 0.3×0.9697 = 98.39
	if s.Score == nil || *s.Score != 98 || s.Band != BandExcellent || s.Attempts != 1000 {
		t.Errorf("pooled %+v", s)
	}
	s.SetPooled(1000, 950, 855) // 66.5 + 27 = 93.5 → 93, healthy (below the 95 gate)
	if *s.Score != 93 || s.Band != BandHealthy {
		t.Errorf("pooled %+v", s)
	}
	d := NewDashboard(time.Unix(0, 0))
	if len(d.SourceHealth.ByBand) != 5 || len(d.SourceHealth.ByStatus) != 3 {
		t.Errorf("dashboard maps %+v", d.SourceHealth)
	}
}
