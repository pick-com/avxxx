package medialink

import (
	"context"
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/media"
)

var t0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func TestNextTry(t *testing.T) {
	cases := []struct {
		status string
		streak int
		want   time.Duration // -1 = nil
	}{
		{StatusFound, 1, -1},
		{StatusAmbiguous, 1, -1},
		{StatusNotFound, 1, 7 * 24 * time.Hour},
		{StatusNotFound, 2, 7 * 24 * time.Hour},
		{StatusNotFound, 3, 30 * 24 * time.Hour},
		{StatusNotFound, 9, 30 * 24 * time.Hour},
		{StatusError, 0, 10 * time.Minute},
		{StatusError, 1, 10 * time.Minute},
		{StatusError, 2, 20 * time.Minute},
		{StatusError, 3, 40 * time.Minute},
		{StatusError, 6, 320 * time.Minute},
		{StatusError, 7, 6 * time.Hour},
		{StatusError, 100, 6 * time.Hour},
	}
	for _, c := range cases {
		got := NextTry(c.status, c.streak, t0)
		if c.want < 0 {
			if got != nil {
				t.Errorf("%s/%d: got %v, want nil", c.status, c.streak, got)
			}
			continue
		}
		if got == nil || got.Sub(t0) != c.want {
			t.Errorf("%s/%d: got %v, want +%s", c.status, c.streak, got, c.want)
		}
	}
}

func TestDueAndStreak(t *testing.T) {
	past, future := t0.Add(-time.Second), t0.Add(time.Second)
	if !Due(nil, t0) || !Due(&Attempt{NextTryAt: &past}, t0) || !Due(&Attempt{NextTryAt: &t0}, t0) {
		t.Error("missing or past-due attempts are due")
	}
	if Due(&Attempt{NextTryAt: &future}, t0) || Due(&Attempt{Status: StatusAmbiguous}, t0) {
		t.Error("future or never attempts are not due")
	}
	if Streak(nil, StatusError) != 1 || Streak(&Attempt{Status: StatusError, Attempts: 2}, StatusError) != 3 ||
		Streak(&Attempt{Status: StatusNotFound, Attempts: 5}, StatusError) != 1 {
		t.Error("streak counts consecutive attempts with the same status")
	}
}

func TestBudget(t *testing.T) {
	for _, c := range []struct{ per, daily, used, want int }{
		{200, 2000, 0, 200}, {200, 2000, 1900, 100}, {200, 2000, 2000, 0}, {200, 2000, 2500, 0}, {5, 2000, 0, 5},
	} {
		if got := Budget(c.per, c.daily, c.used); got != c.want {
			t.Errorf("Budget(%d,%d,%d) = %d, want %d", c.per, c.daily, c.used, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name   string
		in     []CodeResult
		status string
		id     string
		cands  []string
	}{
		{"none", []CodeResult{{Code: "a-1"}}, StatusNotFound, "", nil},
		{"found", []CodeResult{{Code: "a-1", IDs: []string{"v/1/a-1"}}}, StatusFound, "v/1/a-1", []string{"v/1/a-1"}},
		{"second code found", []CodeResult{{Code: "a-1"}, {Code: "b-1", IDs: []string{"v/2/b-1"}}}, StatusFound, "v/2/b-1", []string{"v/2/b-1"}},
		{"found beats ambiguous", []CodeResult{{Code: "a-1", IDs: []string{"v/1/a-1", "v/9/a-1"}}, {Code: "b-1", IDs: []string{"v/2/b-1"}}}, StatusFound, "v/2/b-1", []string{"v/2/b-1"}},
		{"ambiguous", []CodeResult{{Code: "a-1", IDs: []string{"v/1/a-1", "v/9/a-1"}}}, StatusAmbiguous, "", []string{"v/1/a-1", "v/9/a-1"}},
		{"ambiguous beats error", []CodeResult{{Code: "a-1", Err: boom}, {Code: "b-1", IDs: []string{"v/1/b-1", "v/2/b-1"}}}, StatusAmbiguous, "", []string{"v/1/b-1", "v/2/b-1"}},
		{"error", []CodeResult{{Code: "a-1"}, {Code: "b-1", Err: boom}}, StatusError, "", nil},
	}
	for _, c := range cases {
		status, id, cands, err := Decide(c.in)
		if status != c.status || id != c.id || !slices.Equal(cands, c.cands) {
			t.Errorf("%s: got %s %q %v", c.name, status, id, cands)
		}
		if (status == StatusError) != (err != nil) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c, warn := ConfigFromEnv(get)
	if c != DefaultConfig() || len(warn) != 0 {
		t.Fatalf("defaults: %+v %v", c, warn)
	}
	env = map[string]string{"MEDIA_LINK_MIN_INTERVAL": "5s", "MEDIA_LINK_MAX_PER_RUN": "10", "MEDIA_LINK_DAILY_CAP": "100"}
	c, warn = ConfigFromEnv(get)
	if c.MinInterval != 5*time.Second || c.MaxPerRun != 10 || c.DailyCap != 100 || len(warn) != 0 {
		t.Fatalf("set: %+v %v", c, warn)
	}
	env = map[string]string{"MEDIA_LINK_MIN_INTERVAL": "500ms", "MEDIA_LINK_MAX_PER_RUN": "-1", "MEDIA_LINK_DAILY_CAP": "x"}
	c, warn = ConfigFromEnv(get)
	if c != DefaultConfig() || len(warn) != 3 {
		t.Fatalf("invalid values keep defaults: %+v %v", c, warn)
	}
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "FALSE": false, "0": false, "off": false, "no": false} {
		if OnDemandFromEnv(v) != want {
			t.Errorf("OnDemandFromEnv(%q) != %v", v, want)
		}
	}
}

func TestPacer(t *testing.T) {
	now := t0
	var slept []time.Duration
	p := &Pacer{Interval: 2 * time.Second, Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); now = now.Add(d); return nil }}
	ctx := context.Background()
	for range 3 {
		if err := p.Wait(ctx, 0); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(slept, []time.Duration{0, 2 * time.Second, 2 * time.Second}) {
		t.Fatalf("slept %v", slept)
	}
	// Without sleeping, the next slot is 2 s away: a 1 s bound refuses it.
	p.Sleep = func(context.Context, time.Duration) error { return nil }
	p.Wait(ctx, 0)
	if err := p.Wait(ctx, time.Second); !errors.Is(err, ErrBusy) {
		t.Fatalf("Wait with short bound = %v, want ErrBusy", err)
	}
}

// ---- fakes

type fakeRelease struct {
	id    int64
	date  int // larger = newer
	codes []string
}

type fakeStore struct {
	releases []fakeRelease
	links    map[int64]string // release -> media id
	attempts map[int64]Attempt
	runs     []audit.Run
	backoff  time.Time
	used     int
}

func newFakeStore(rs ...fakeRelease) *fakeStore {
	return &fakeStore{releases: rs, links: map[int64]string{}, attempts: map[int64]Attempt{}}
}

func (f *fakeStore) ClaimMediaLink(_ context.Context, releaseID int64, _, mediaID, _ string) (int64, error) {
	for rid, mid := range f.links {
		if mid == mediaID {
			return rid, nil
		}
	}
	f.links[releaseID] = mediaID
	return releaseID, nil
}
func (f *fakeStore) RecordMediaLinkAttempt(_ context.Context, a Attempt) error {
	f.attempts[a.ReleaseID] = a
	return nil
}
func (f *fakeStore) DueMediaLinks(_ context.Context, _ string, now time.Time, limit int, exclude []int64) ([]Candidate, error) {
	rs := slices.Clone(f.releases)
	sort.Slice(rs, func(i, j int) bool { return rs[i].date > rs[j].date })
	var out []Candidate
	for _, r := range rs {
		if _, linked := f.links[r.id]; linked || len(r.codes) == 0 || slices.Contains(exclude, r.id) {
			continue
		}
		var prev *Attempt
		if a, ok := f.attempts[r.id]; ok {
			prev = &a
		}
		if !Due(prev, now) {
			continue
		}
		out = append(out, Candidate{ReleaseID: r.id, Codes: r.codes, Prev: prev})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
func (f *fakeStore) MediaLinkTriedSince(context.Context, string, time.Time) (int, error) {
	return f.used, nil
}
func (f *fakeStore) MediaLinkProviderBackoff(context.Context, string) (time.Time, error) {
	return f.backoff, nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

// fakeSearch answers per query; queries lists every request made.
type fakeSearch struct {
	results map[string][]media.SearchResult
	errs    map[string]error
	queries []string
}

func (s *fakeSearch) Search(_ context.Context, source, q string, _ int) ([]media.SearchResult, bool, error) {
	s.queries = append(s.queries, q)
	if err := s.errs[q]; err != nil {
		return nil, false, err
	}
	return s.results[q], false, nil
}

func ids(ss ...string) []media.SearchResult {
	out := make([]media.SearchResult, len(ss))
	for i, s := range ss {
		out[i] = media.SearchResult{ID: s, Source: "javtiful"}
	}
	return out
}

func newRunner(st *fakeStore, se *fakeSearch, cfg Config) (*Runner, *[]time.Duration) {
	now := t0
	var slept []time.Duration
	return &Runner{Store: st, Search: se, Config: cfg, Providers: []string{"javtiful"},
		Now: func() time.Time { return now },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			now = now.Add(d)
			return nil
		}}, &slept
}

func TestRunnerOutcomes(t *testing.T) {
	st := newFakeStore(
		fakeRelease{1, 5, []string{"abc-001"}},            // found
		fakeRelease{2, 4, []string{"abc-002"}},            // not_found (only a variant)
		fakeRelease{3, 3, []string{"abc-003"}},            // ambiguous
		fakeRelease{4, 2, []string{"abc-004"}},            // error
		fakeRelease{5, 1, []string{"zzz-009", "abc-005"}}, // found by second code
		fakeRelease{6, 9, nil},                            // no code: never selected
	)
	se := &fakeSearch{
		results: map[string][]media.SearchResult{
			"abc-001": ids("video/1/abc-001", "video/7/abc-0011"),
			"abc-002": ids("video/2/abc-002-reducing-mosaic"),
			"abc-003": ids("video/3/abc-003", "video/33/abc-003"),
			"abc-005": ids("video/5/abc-005"),
		},
		errs: map[string]error{"abc-004": errors.New("dial tcp: timeout")},
	}
	r, slept := newRunner(st, se, Config{MinInterval: 2 * time.Second, MaxPerRun: 200, DailyCap: 2000})
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pr := rep.Providers[0]
	if pr.Checked != 5 || pr.Found != 2 || pr.NotFound != 1 || pr.Ambiguous != 1 || pr.Errors != 1 || pr.Requests != 6 || pr.Stopped != "" {
		t.Fatalf("report %+v", pr)
	}
	// newest first
	if !slices.Equal(se.queries, []string{"abc-001", "abc-002", "abc-003", "abc-004", "zzz-009", "abc-005"}) {
		t.Fatalf("queries %v", se.queries)
	}
	for _, d := range (*slept)[1:] {
		if d < 2*time.Second {
			t.Fatalf("requests closer than the min interval: %v", *slept)
		}
	}
	if st.links[1] != "video/1/abc-001" || st.links[5] != "video/5/abc-005" || len(st.links) != 2 {
		t.Fatalf("links %v", st.links)
	}
	a3 := st.attempts[3]
	if a3.Status != StatusAmbiguous || a3.NextTryAt != nil || !slices.Equal(a3.Candidates, []string{"video/3/abc-003", "video/33/abc-003"}) {
		t.Fatalf("ambiguous attempt %+v", a3)
	}
	if a := st.attempts[2]; a.Status != StatusNotFound || a.NextTryAt == nil || a.NextTryAt.Sub(a.TriedAt) != NotFoundRetry || len(a.Candidates) != 0 {
		t.Fatalf("not_found attempt %+v", a)
	}
	if a := st.attempts[4]; a.Status != StatusError || a.NextTryAt.Sub(a.TriedAt) != ErrorRetryBase || a.LastError == "" {
		t.Fatalf("error attempt %+v", a)
	}
	if len(st.runs) != 1 || st.runs[0].Job != "media_link.javtiful" || st.runs[0].Fixed != 2 || st.runs[0].Result != audit.ResultIssues {
		t.Fatalf("runs %+v", st.runs)
	}

	// A second run right away searches nothing: everything is linked or backing off.
	se.queries = nil
	if rep, _ = r.Run(context.Background()); rep.Providers[0].Checked != 0 || len(se.queries) != 0 {
		t.Fatalf("second run %+v %v", rep.Providers[0], se.queries)
	}
}

func TestRunnerStopsOn429(t *testing.T) {
	st := newFakeStore(fakeRelease{1, 3, []string{"a-1"}}, fakeRelease{2, 2, []string{"a-2"}}, fakeRelease{3, 1, []string{"a-3"}})
	se := &fakeSearch{errs: map[string]error{"a-2": &media.StatusError{Source: "javtiful", Code: 429}}}
	r, _ := newRunner(st, se, DefaultConfig())
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pr := rep.Providers[0]
	if pr.Stopped != "rate_limited" || pr.Checked != 2 || pr.BackoffUntil == nil || !slices.Equal(se.queries, []string{"a-1", "a-2"}) {
		t.Fatalf("report %+v queries %v", pr, se.queries)
	}
	if st.runs[0].Detail["backoff_until"] == nil || st.runs[0].Detail["stopped"] != "rate_limited" {
		t.Fatalf("run detail %+v", st.runs[0].Detail)
	}
	if st.attempts[2].Status != StatusError {
		t.Fatalf("429 release recorded as %+v", st.attempts[2])
	}

	// While the provider backs off, a run searches nothing and carries it on.
	st.backoff = *pr.BackoffUntil
	se.queries = nil
	rep, _ = r.Run(context.Background())
	if rep.Providers[0].Stopped != "backoff" || len(se.queries) != 0 || st.runs[1].Detail["backoff_until"] == nil {
		t.Fatalf("backoff run %+v", rep.Providers[0])
	}
}

func TestRunnerStopsOnErrorStreak(t *testing.T) {
	var rs []fakeRelease
	errs := map[string]error{}
	for i := range 6 {
		code := "e-" + string(rune('a'+i))
		rs = append(rs, fakeRelease{int64(i + 1), 10 - i, []string{code}})
		errs[code] = errors.New("HTTP 500")
	}
	st := newFakeStore(rs...)
	se := &fakeSearch{errs: errs}
	r, _ := newRunner(st, se, DefaultConfig())
	rep, _ := r.Run(context.Background())
	if pr := rep.Providers[0]; pr.Stopped != "error_streak" || pr.Checked != MaxErrorStreak || pr.BackoffUntil == nil {
		t.Fatalf("report %+v", pr)
	}
}

func TestRunnerCaps(t *testing.T) {
	var rs []fakeRelease
	for i := range 10 {
		rs = append(rs, fakeRelease{int64(i + 1), i, []string{"c-" + string(rune('a'+i))}})
	}
	st := newFakeStore(rs...)
	se := &fakeSearch{}
	r, _ := newRunner(st, se, Config{MaxPerRun: 4, DailyCap: 2000, BatchSize: 3})
	rep, _ := r.Run(context.Background())
	if pr := rep.Providers[0]; pr.Checked != 4 || pr.Budget != 4 || len(se.queries) != 4 {
		t.Fatalf("per-run cap: %+v", pr)
	}
	st.used = 1998 // 2 left of the daily cap
	se.queries = nil
	rep, _ = r.Run(context.Background())
	if pr := rep.Providers[0]; pr.Checked != 2 || pr.Budget != 2 {
		t.Fatalf("daily cap: %+v", pr)
	}
	st.used = 2000
	rep, _ = r.Run(context.Background())
	if pr := rep.Providers[0]; pr.Checked != 0 || pr.Stopped != "daily_cap" {
		t.Fatalf("exhausted daily cap: %+v", pr)
	}
}

func TestLinkerConflictIsAmbiguous(t *testing.T) {
	st := newFakeStore()
	st.links[99] = "video/1/dup-1" // another release already holds the page
	se := &fakeSearch{results: map[string][]media.SearchResult{"dup-1": ids("video/1/dup-1")}}
	l := &Linker{Store: st, Search: se, Now: func() time.Time { return t0 }}
	out, err := l.Link(context.Background(), "javtiful", Candidate{ReleaseID: 1, Codes: []string{"dup-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempt.Status != StatusAmbiguous || out.Attempt.NextTryAt != nil || out.Attempt.LastError == "" || st.links[1] != "" {
		t.Fatalf("outcome %+v links %v", out.Attempt, st.links)
	}
	if MediaURL("javtiful", "video/1/dup-1") != "https://javtiful.com/video/1/dup-1" || MediaURL("nope", "x") != "" {
		t.Error("MediaURL")
	}
}
