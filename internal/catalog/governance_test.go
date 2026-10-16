package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGate(t *testing.T) {
	reviewed := time.Unix(1_700_000_000, 0)
	src := func(status, terms string, at *time.Time) SourceGovernance {
		return SourceGovernance{Adapter: "javdatabase", LicenseStatus: status, TermsURL: terms, TermsReviewedAt: at}
	}
	for _, tc := range []struct {
		name    string
		src     SourceGovernance
		phase   Phase
		pages   int
		allowed bool
		max     int
	}{
		{"review_required incremental", src(LicenseReviewRequired, "", nil), PhaseIncremental, 0, false, 0},
		{"review_required baseline", src(LicenseReviewRequired, "https://t", &reviewed), PhaseBaseline, 0, false, 0},
		{"review_required verify default cap", src(LicenseReviewRequired, "", nil), PhaseVerify, 0, true, VerifyDefaultPages},
		{"verify cap clamped", src(LicenseManualOnly, "", nil), PhaseVerify, 50, true, VerifyMaxPages},
		{"verify explicit cap", src(LicenseManualOnly, "", nil), PhaseVerify, 2, true, 2},
		{"manual_only incremental", src(LicenseManualOnly, "https://t", &reviewed), PhaseIncremental, 0, false, 0},
		{"denied verify", src(LicenseDenied, "https://t", &reviewed), PhaseVerify, 1, false, 0},
		{"licensed without review", src(LicenseLicensed, "https://t", nil), PhaseIncremental, 0, false, 0},
		{"terms_confirmed without url", src(LicenseTermsConfirmed, "", &reviewed), PhaseIncremental, 0, false, 0},
		{"terms_confirmed reviewed", src(LicenseTermsConfirmed, "https://t", &reviewed), PhaseIncremental, 0, true, 0},
		{"official_api baseline", src(LicenseOfficialAPI, "https://t", &reviewed), PhaseBaseline, 0, true, 0},
		{"internal needs no url", src(LicenseInternal, "", &reviewed), PhaseBaseline, 0, true, 0},
		{"unknown status", src("whatever", "https://t", &reviewed), PhaseIncremental, 0, false, 0},
		{"rss feeds are not catalog sources", SourceGovernance{Adapter: "rss", LicenseStatus: LicenseReviewRequired}, PhaseIncremental, 0, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := Gate(tc.src, tc.phase, tc.pages)
			if d.Allowed != tc.allowed || d.MaxPages != tc.max {
				t.Fatalf("got %+v", d)
			}
			if !d.Allowed && (d.Code != CodeLicenseGate || d.Reason == "") {
				t.Fatalf("refusal must carry license_gate and a reason: %+v", d)
			}
		})
	}
}

func TestParsePhase(t *testing.T) {
	if p, err := ParsePhase(""); err != nil || p != PhaseIncremental {
		t.Fatalf("empty: %v %v", p, err)
	}
	if p, err := ParsePhase(" Baseline "); err != nil || p != PhaseBaseline {
		t.Fatalf("baseline: %v %v", p, err)
	}
	if _, err := ParsePhase("full"); err == nil {
		t.Fatal("unknown phase accepted")
	}
	if PhaseVerify.Automatic() || !PhaseBaseline.Automatic() {
		t.Fatal("Automatic")
	}
}

func TestUserAgent(t *testing.T) {
	if got := UserAgent("ops@example.com"); got != "avxxx-catalog/0.2 (+mailto:ops@example.com)" {
		t.Fatalf("email UA %q", got)
	}
	if got := UserAgent("https://example.com/bot"); got != "avxxx-catalog/0.2 (+https://example.com/bot)" {
		t.Fatalf("url UA %q", got)
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, err := UserAgentFromEnv(env(nil)); err == nil {
		t.Fatal("missing contact must be an error")
	}
	if _, err := UserAgentFromEnv(env(map[string]string{"CATALOG_USER_AGENT": "Mozilla/5.0"})); err == nil {
		t.Fatal("override without contact must be an error")
	}
	if ua, err := UserAgentFromEnv(env(map[string]string{"CATALOG_CONTACT": "https://c.example"})); err != nil || !strings.Contains(ua, "https://c.example") {
		t.Fatalf("%q %v", ua, err)
	}
}

const robotsSample = `# comment
User-agent: *
Disallow: /private/
Allow: /private/ok
Disallow: /*.pdf$
Crawl-delay: 2

User-agent: AVXXX-Catalog
User-agent: otherbot
Disallow: /movies/secret
Allow: /movies/secret/public$
Crawl-delay: 12.5

User-agent: badbot
Disallow: /
`

func TestParseRobots(t *testing.T) {
	own := ParseRobots([]byte(robotsSample), RobotsToken)
	if own.CrawlDelay != 12500*time.Millisecond {
		t.Fatalf("crawl delay %v", own.CrawlDelay)
	}
	for path, want := range map[string]bool{
		"/movies/secret":          false,
		"/movies/secret/x":        false,
		"/movies/secret/public":   true,
		"/movies/secret/public/2": false,
		"/private/x":              true, // only the specific group applies to us
		"/robots.txt":             true,
		"/":                       true,
	} {
		if got := own.Allowed(path); got != want {
			t.Errorf("own %s = %v want %v", path, got, want)
		}
	}
	star := ParseRobots([]byte(robotsSample), "somebody")
	for path, want := range map[string]bool{
		"/private/x":     false,
		"/private/ok":    true, // longer Allow wins
		"/doc.pdf":       false,
		"/doc.pdf?x=1":   true, // $ anchors the end
		"/public/a.html": true,
	} {
		if got := star.Allowed(path); got != want {
			t.Errorf("star %s = %v want %v", path, got, want)
		}
	}
	if star.CrawlDelay != 2*time.Second {
		t.Errorf("star delay %v", star.CrawlDelay)
	}
	if !ParseRobots([]byte("Disallow: /\n"), RobotsToken).Allowed("/x") {
		t.Error("rules without a user-agent line must be ignored")
	}
	if ParseRobots([]byte("User-agent: *\nDisallow: /\n"), RobotsToken).Allowed("/x") {
		t.Error("global disallow")
	}
	if !ParseRobots([]byte("User-agent: *\nDisallow:\n"), RobotsToken).Allowed("/x") {
		t.Error("empty Disallow allows all")
	}
	tie := ParseRobots([]byte("User-agent: *\nDisallow: /a\nAllow: /a\n"), RobotsToken)
	if !tie.Allowed("/a") {
		t.Error("Allow wins ties")
	}
}

// robotsSite serves robots.txt with a configurable status and counts fetches.
type robotsSite struct {
	*httptest.Server
	mu       sync.Mutex
	status   int
	body     string
	fetches  int
	lastUA   string
	pageUAs  []string
	pageHits []string
}

func newRobotsSite(t *testing.T, status int, body string) *robotsSite {
	t.Helper()
	s := &robotsSite{status: status, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path == "/robots.txt" {
			s.fetches++
			s.lastUA = r.UserAgent()
			w.WriteHeader(s.status)
			fmt.Fprint(w, s.body)
			return
		}
		s.pageUAs = append(s.pageUAs, r.UserAgent())
		s.pageHits = append(s.pageHits, r.URL.Path)
		fmt.Fprint(w, "<html><body>ok</body></html>")
	}))
	t.Cleanup(s.Close)
	return s
}

func TestRobotsCache(t *testing.T) {
	s := newRobotsSite(t, 200, "User-agent: *\nDisallow: /no\n")
	r := NewRobots(s.Client(), "avxxx-catalog/0.2 (+mailto:ops@example.com)")
	clock := time.Unix(1_700_000_000, 0)
	r.now = func() time.Time { return clock }
	ctx := context.Background()
	if v := r.Check(ctx, s.URL+"/yes"); !v.Allowed || v.Unavailable {
		t.Fatalf("yes: %+v", v)
	}
	if v := r.Check(ctx, s.URL+"/no/x"); v.Allowed || v.Reason == "" {
		t.Fatalf("no: %+v", v)
	}
	if s.fetches != 1 || !strings.Contains(s.lastUA, "mailto:ops@example.com") {
		t.Fatalf("fetches %d ua %q", s.fetches, s.lastUA)
	}
	clock = clock.Add(25 * time.Hour)
	r.Check(ctx, s.URL+"/yes")
	if s.fetches != 2 {
		t.Fatalf("expired entry must refetch, fetches %d", s.fetches)
	}

	// 404 = no robots.txt = no restrictions; 5xx = unreachable.
	missing := newRobotsSite(t, 404, "")
	if v := NewRobots(missing.Client(), "").Check(ctx, missing.URL+"/x"); !v.Allowed || v.Unavailable {
		t.Fatalf("404: %+v", v)
	}
	down := newRobotsSite(t, 503, "")
	rd := NewRobots(down.Client(), "")
	rd.now = func() time.Time { return clock }
	if v := rd.Check(ctx, down.URL+"/x"); !v.Unavailable {
		t.Fatalf("503: %+v", v)
	}
	rd.Check(ctx, down.URL+"/y")
	if down.fetches != 1 {
		t.Fatalf("failures are cached too, fetches %d", down.fetches)
	}
	clock = clock.Add(11 * time.Minute)
	rd.Check(ctx, down.URL+"/y")
	if down.fetches != 2 {
		t.Fatalf("failure cache must expire sooner, fetches %d", down.fetches)
	}
}

func TestFetcherHonorsRobots(t *testing.T) {
	ctx := context.Background()
	s := newRobotsSite(t, 200, "User-agent: avxxx-catalog\nDisallow: /no\nCrawl-delay: 9\n")
	ua := UserAgent("ops@example.com")
	f := NewFetcher(s.Client(), FetchPolicy{MinInterval: time.Second})
	f.UserAgent, f.Robots, f.Automatic = ua, NewRobots(s.Client(), ua), true
	slept := fakeClock(f)
	if _, err := f.Fetch(ctx, s.URL+"/no/1"); err == nil {
		t.Fatal("disallowed URL fetched")
	} else if _, ok := IsRobots(err); !ok {
		t.Fatalf("err %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := f.Fetch(ctx, s.URL+"/yes"); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) != 1 || (*slept)[0] != 9*time.Second {
		t.Fatalf("Crawl-delay must raise the 1s min interval: slept %v", *slept)
	}
	if len(s.pageHits) != 2 || s.pageUAs[0] != ua {
		t.Fatalf("hits %v uas %v", s.pageHits, s.pageUAs)
	}

	// Unreachable robots.txt: automatic crawls stop, manual verify proceeds.
	down := newRobotsSite(t, 500, "")
	auto := NewFetcher(down.Client(), FetchPolicy{})
	auto.Robots, auto.Automatic = NewRobots(down.Client(), ua), true
	if _, err := auto.Fetch(ctx, down.URL+"/x"); err == nil {
		t.Fatal("automatic fetch with unreachable robots.txt")
	}
	manual := NewFetcher(down.Client(), FetchPolicy{})
	manual.Robots = NewRobots(down.Client(), ua)
	if _, err := manual.Fetch(ctx, down.URL+"/x"); err != nil {
		t.Fatalf("verify fetch: %v", err)
	}
	if len(down.pageHits) != 1 {
		t.Fatalf("hits %v", down.pageHits)
	}
}

// ---- phases and watermarks over the recorded JAV Database listing.

func phaseCrawl(sourceID int64, s *site, cps *fakeCheckpoints, sink *fakeSink, phase Phase) *Crawl {
	c := newCrawl(sourceID, s, cps, sink, FetchPolicy{})
	c.Phase = phase
	return c
}

func (f *fakeCheckpoints) get(sourceID int64, phase Phase) (Checkpoint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp, ok := f.rows[CheckpointKey{SourceID: sourceID, TaskType: TaskDiscovery, Partition: string(phase), ParserVersion: "javdatabase"}]
	return cp, ok
}

func TestIncrementalStopsAtWatermark(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	res, err := phaseCrawl(1, s, cps, sink, PhaseIncremental).Run(context.Background())
	if err != nil || res.Stop != StopComplete || res.Pages != 3 || res.Records != 4 {
		t.Fatalf("first run %+v %v", res, err)
	}
	cp, _ := cps.get(1, PhaseIncremental)
	if cp.Cursor != "release:abc-001\nperformer:jane-doe" || cp.Watermark == nil {
		t.Fatalf("incremental mark %+v", cp)
	}
	if _, ok := cps.get(1, PhaseBaseline); ok {
		t.Fatal("incremental run touched the baseline checkpoint")
	}

	// A new release appears at the head of the newest-first listing.
	s.setOverride("/movies/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, strings.Replace(fixture(t, "javdatabase/discovery/movies_page1.html"), `<div class="movie-card"><a href="/movies/abc-001/">`,
			`<div class="movie-card"><a href="/movies/abc-000/">ABC-000</a></div><div class="movie-card"><a href="/movies/abc-001/">`, 1))
	})
	s.mu.Lock()
	s.hits = nil
	s.mu.Unlock()
	sink.saved = nil
	res, err = phaseCrawl(1, s, cps, sink, PhaseIncremental).Run(context.Background())
	if err != nil || res.Stop != StopWatermark || res.Records != 1 || res.Pages != 1 {
		t.Fatalf("second run %+v %v", res, err)
	}
	if strings.Join(sink.saved, ",") != "1:abc-000" || s.requested("/movies/page/2/") || s.requested("/movies/abc-001/") {
		t.Fatalf("saved %v hits %v", sink.saved, s.hits)
	}
	cp, _ = cps.get(1, PhaseIncremental)
	if !strings.HasPrefix(cp.Cursor, "release:abc-000\nrelease:abc-001") {
		t.Fatalf("mark after second run %q", cp.Cursor)
	}
}

func TestIncrementalFailureKeepsWatermark(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	key := CheckpointKey{SourceID: 1, TaskType: TaskDiscovery, Partition: string(PhaseIncremental), ParserVersion: "javdatabase"}
	old := time.Unix(1_600_000_000, 0)
	cps.rows[key] = Checkpoint{Cursor: "release:abc-003", Watermark: &old}
	s.setOverride("/movies/abc-002/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
	if _, err := phaseCrawl(1, s, cps, sink, PhaseIncremental).Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	cp := cps.rows[key]
	if cp.Cursor != "release:abc-003" || !cp.Watermark.Equal(old) || cp.LastErrorCode != "detail_fetch_failed" {
		t.Fatalf("failed run moved the watermark: %+v", cp)
	}
}

func TestIncrementalWindowGap(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	key := CheckpointKey{SourceID: 1, TaskType: TaskDiscovery, Partition: string(PhaseIncremental), ParserVersion: "javdatabase"}
	cps.rows[key] = Checkpoint{Cursor: "release:gone-away"}
	c := phaseCrawl(1, s, cps, sink, PhaseIncremental)
	c.MaxPages = 2
	res, err := c.Run(context.Background())
	if err != nil || res.Stop != StopPageLimit || !res.WatermarkGap || s.requested("/movies/page/3/") {
		t.Fatalf("res %+v err %v hits %v", res, err, s.hits)
	}
	if cp := cps.rows[key]; !strings.HasPrefix(cp.Cursor, "release:abc-001\nperformer:jane-doe") || cp.Watermark == nil {
		t.Fatalf("window end must move the mark to the head: %+v", cp)
	}
}

func TestBaselineNeverMovesIncrementalWatermark(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	inc := CheckpointKey{SourceID: 1, TaskType: TaskDiscovery, Partition: string(PhaseIncremental), ParserVersion: "javdatabase"}
	old := time.Unix(1_600_000_000, 0)
	cps.rows[inc] = Checkpoint{Cursor: "release:abc-001", Watermark: &old}
	res, err := phaseCrawl(1, s, cps, sink, PhaseBaseline).Run(context.Background())
	if err != nil || res.Stop != StopComplete || res.Records != 4 {
		t.Fatalf("baseline %+v %v", res, err)
	}
	if cp := cps.rows[inc]; cp.Cursor != "release:abc-001" || !cp.Watermark.Equal(old) {
		t.Fatalf("baseline moved the incremental watermark: %+v", cp)
	}
	if cp, ok := cps.get(1, PhaseBaseline); !ok || cp.CompletedPages != 3 || cp.Watermark == nil {
		t.Fatalf("baseline checkpoint %+v", cp)
	}
}

func TestVerifyWritesNoCheckpoint(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	c := phaseCrawl(1, s, cps, sink, PhaseVerify)
	c.MaxPages = 1
	res, err := c.Run(context.Background())
	if err != nil || res.Pages != 1 || res.Stop != StopPageLimit || res.Records != 2 {
		t.Fatalf("verify %+v %v", res, err)
	}
	if len(cps.rows) != 0 || len(cps.advances) != 0 {
		t.Fatalf("verify wrote checkpoints: %v", cps.rows)
	}
	c = phaseCrawl(1, s, nil, sink, PhaseVerify)
	c.MaxPages = 99
	if res, err = c.Run(context.Background()); err != nil || res.Pages != VerifyMaxPages {
		t.Fatalf("verify cap %+v %v", res, err)
	}
}

func TestCrawlRobotsDetailSkippedIndexStops(t *testing.T) {
	s := newJAVDatabaseSite(t)
	s.setOverride("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /idols/\nDisallow: /movies/page/3/\n")
	})
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	c := phaseCrawl(1, s, cps, sink, PhaseBaseline)
	c.Fetcher.Robots, c.Fetcher.Automatic = NewRobots(s.Client(), ""), true
	res, err := c.Run(context.Background())
	if _, ok := IsRobots(err); !ok {
		t.Fatalf("index disallow must stop the run: %v", err)
	}
	if res.Skipped != 1 || s.requested("/idols/jane-doe/") || s.requested("/movies/page/3/") {
		t.Fatalf("res %+v hits %v", res, s.hits)
	}
	if cp, _ := cps.get(1, PhaseBaseline); cp.LastErrorCode != CodeRobotsDisallow || cp.Cursor != s.URL+"/movies/page/3/" {
		t.Fatalf("checkpoint %+v", cp)
	}
}
