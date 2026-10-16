package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCheckpoints is an in-memory CheckpointStore.
type fakeCheckpoints struct {
	mu       sync.Mutex
	rows     map[CheckpointKey]Checkpoint
	advances []string
	fails    []string
}

func newFakeCheckpoints() *fakeCheckpoints {
	return &fakeCheckpoints{rows: map[CheckpointKey]Checkpoint{}}
}

func (f *fakeCheckpoints) LoadCheckpoint(_ context.Context, k CheckpointKey) (Checkpoint, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp, ok := f.rows[k]
	return cp, ok, nil
}

func (f *fakeCheckpoints) AdvanceCheckpoint(_ context.Context, k CheckpointKey, cursor string, wm *time.Time, pages int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := f.rows[k]
	cp.Cursor, cp.Watermark, cp.LastErrorCode = cursor, wm, ""
	cp.CompletedPages += pages
	f.rows[k] = cp
	f.advances = append(f.advances, fmt.Sprintf("%d:%s", k.SourceID, cursor))
	return nil
}

func (f *fakeCheckpoints) FailCheckpoint(_ context.Context, k CheckpointKey, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := f.rows[k]
	cp.LastErrorCode = code
	f.rows[k] = cp
	f.fails = append(f.fails, fmt.Sprintf("%d:%s:%s", k.SourceID, k.TaskType, code))
	return nil
}

func (f *fakeCheckpoints) discovery(sourceID int64) Checkpoint {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[CheckpointKey{SourceID: sourceID, TaskType: TaskDiscovery, ParserVersion: "javdatabase"}]
}

// fakeSink records saved records; failOn makes saves for that key fail.
type fakeSink struct {
	mu     sync.Mutex
	saved  []string
	failOn string
}

func (s *fakeSink) SaveCatalogRecord(_ context.Context, sourceID int64, _ string, rec Record) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.SourceKey == s.failOn {
		return 0, errors.New("db down")
	}
	s.saved = append(s.saved, fmt.Sprintf("%d:%s", sourceID, rec.SourceKey))
	return int64(len(s.saved)), nil
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// site serves the recorded JAV Database fixtures. override can replace a
// path's response; hits records every request path.
type site struct {
	*httptest.Server
	mu       sync.Mutex
	hits     []string
	override map[string]http.HandlerFunc
}

func newJAVDatabaseSite(t *testing.T) *site {
	t.Helper()
	pages := map[string]string{
		"/movies/":        fixture(t, "javdatabase_movies_page1.html"),
		"/movies/page/2/": fixture(t, "javdatabase_movies_page2.html"),
		"/movies/page/3/": fixture(t, "javdatabase_movies_page3.html"),
	}
	s := &site{override: map[string]http.HandlerFunc{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits = append(s.hits, r.URL.Path)
		h := s.override[r.URL.Path]
		s.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		if body, ok := pages[r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/movies/"):
			fmt.Fprint(w, `<html><body><h1>DETAIL</h1><p>DVD ID: X-1</p></body></html>`)
		case strings.HasPrefix(r.URL.Path, "/idols/"):
			fmt.Fprint(w, `<html><body><h1>Jane Doe - JAV Profile</h1><p>Cup: C</p></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *site) setOverride(path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.override[path] = h
}

func (s *site) requested(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, h := range s.hits {
		if h == path {
			return true
		}
	}
	return false
}

func (s *site) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hits)
}

func newCrawl(sourceID int64, s *site, cps *fakeCheckpoints, sink *fakeSink, policy FetchPolicy) *Crawl {
	return &Crawl{
		SourceID:    sourceID,
		StartURL:    s.URL + "/movies/",
		Adapter:     JAVDatabaseAdapter{},
		Fetcher:     NewFetcher(s.Client(), policy),
		Checkpoints: cps,
		Sink:        sink,
	}
}

func TestCrawlFullPassAdvancesPerPage(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	res, err := newCrawl(1, s, cps, sink, FetchPolicy{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopComplete || res.Pages != 3 || res.Records != 4 {
		t.Fatalf("result %+v", res)
	}
	want := []string{"1:" + s.URL + "/movies/page/2/", "1:" + s.URL + "/movies/page/3/", "1:"}
	if strings.Join(cps.advances, ",") != strings.Join(want, ",") {
		t.Fatalf("advances %v want %v", cps.advances, want)
	}
	cp := cps.discovery(1)
	if cp.Cursor != "" || cp.Watermark == nil || cp.CompletedPages != 3 {
		t.Fatalf("final checkpoint %+v", cp)
	}
}

func TestCrawlResumesFromCursor(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	key := CheckpointKey{SourceID: 1, TaskType: TaskDiscovery, ParserVersion: "javdatabase"}
	cps.rows[key] = Checkpoint{Cursor: s.URL + "/movies/page/3/", CompletedPages: 2}
	res, err := newCrawl(1, s, cps, sink, FetchPolicy{}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.requested("/movies/") || s.requested("/movies/page/2/") {
		t.Fatalf("earlier pages refetched: %v", s.hits)
	}
	if !s.requested("/movies/page/3/") || res.Pages != 1 || strings.Join(sink.saved, ",") != "1:abc-003" {
		t.Fatalf("res %+v saved %v hits %v", res, sink.saved, s.hits)
	}
	if cp := cps.discovery(1); cp.CompletedPages != 3 || cp.Cursor != "" {
		t.Fatalf("checkpoint %+v", cp)
	}
}

func TestCrawlIgnoresForeignCursor(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	cps.rows[CheckpointKey{SourceID: 1, TaskType: TaskDiscovery, ParserVersion: "javdatabase"}] = Checkpoint{Cursor: "https://elsewhere.invalid/movies/page/9/"}
	if _, err := newCrawl(1, s, cps, sink, FetchPolicy{MaxPages: 1}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.requested("/movies/") {
		t.Fatalf("expected restart from start URL, hits %v", s.hits)
	}
}

func TestCrawlFailedBatchDoesNotAdvance(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		setup      func(*site, *fakeSink)
	}{
		{"detail fetch", "detail_fetch_failed", func(s *site, _ *fakeSink) {
			s.setOverride("/movies/abc-002/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
		}},
		{"record save", "record_save_failed", func(_ *site, k *fakeSink) { k.failOn = "abc-002" }},
		{"index fetch", "index_fetch_failed", func(s *site, _ *fakeSink) {
			s.setOverride("/movies/page/2/", func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "boom", 500) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newJAVDatabaseSite(t)
			cps, sink := newFakeCheckpoints(), &fakeSink{}
			tc.setup(s, sink)
			_, err := newCrawl(1, s, cps, sink, FetchPolicy{}).Run(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			// Page 1 completed and advanced; page 2 failed and must not.
			if len(cps.advances) != 1 || cps.advances[0] != "1:"+s.URL+"/movies/page/2/" {
				t.Fatalf("advances %v", cps.advances)
			}
			cp := cps.discovery(1)
			if cp.Cursor != s.URL+"/movies/page/2/" || cp.LastErrorCode != tc.code {
				t.Fatalf("checkpoint %+v", cp)
			}
			// Next run resumes on the failed page.
			s.setOverride("/movies/abc-002/", nil)
			s.setOverride("/movies/page/2/", nil)
			sink.failOn = ""
			if _, err := newCrawl(1, s, cps, sink, FetchPolicy{}).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			if cp := cps.discovery(1); cp.Cursor != "" || cp.LastErrorCode != "" || cp.CompletedPages != 3 {
				t.Fatalf("after retry %+v", cp)
			}
		})
	}
}

func TestCrawlPageBudgetStopsRun(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	// Page 1 costs 3 requests (index + 2 details); the 4th request is page 2's index.
	res, err := newCrawl(1, s, cps, sink, FetchPolicy{MaxPages: 4}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopBudget || res.Pages != 1 {
		t.Fatalf("result %+v", res)
	}
	if s.count() != 4 {
		t.Fatalf("requests %d (%v), want 4", s.count(), s.hits)
	}
	// Page 2's details were never fetched, so its batch must not advance.
	if cp := cps.discovery(1); cp.Cursor != s.URL+"/movies/page/2/" || cp.CompletedPages != 1 {
		t.Fatalf("checkpoint %+v", cp)
	}
	if len(cps.fails) != 0 {
		t.Fatalf("budget stop is not a failure: %v", cps.fails)
	}
}

func TestCrawlByteBudgetStopsRun(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	res, err := newCrawl(1, s, cps, sink, FetchPolicy{MaxBytes: 1}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != StopBudget || s.count() != 1 || len(cps.advances) != 0 {
		t.Fatalf("result %+v hits %v advances %v", res, s.hits, cps.advances)
	}
}

func TestCrawlMaxPagesPerRun(t *testing.T) {
	s := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}
	c := newCrawl(1, s, cps, sink, FetchPolicy{})
	c.MaxPages = 2
	res, err := c.Run(context.Background())
	if err != nil || res.Stop != StopPageLimit || res.Pages != 2 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if cp := cps.discovery(1); cp.Cursor != s.URL+"/movies/page/3/" {
		t.Fatalf("checkpoint %+v", cp)
	}
}

func TestCrawlBlockedSourceIsolated(t *testing.T) {
	blocked := newJAVDatabaseSite(t)
	blocked.setOverride("/movies/abc-001/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	healthy := newJAVDatabaseSite(t)
	cps, sink := newFakeCheckpoints(), &fakeSink{}

	_, errA := newCrawl(1, blocked, cps, sink, FetchPolicy{}).Run(context.Background())
	b, ok := IsBlocked(errA)
	if !ok || b.Status != http.StatusTooManyRequests || b.RetryAfter != 120*time.Second {
		t.Fatalf("source 1 error %v", errA)
	}
	if blocked.requested("/idols/jane-doe/") || blocked.requested("/movies/page/2/") {
		t.Fatalf("blocked source kept fetching: %v", blocked.hits)
	}
	if cp := cps.discovery(1); cp.LastErrorCode != "source_blocked" || cp.CompletedPages != 0 {
		t.Fatalf("source 1 checkpoint %+v", cp)
	}

	res, errB := newCrawl(2, healthy, cps, sink, FetchPolicy{}).Run(context.Background())
	if errB != nil || res.Stop != StopComplete || res.Records != 4 {
		t.Fatalf("source 2 res %+v err %v", res, errB)
	}
	if cp := cps.discovery(2); cp.CompletedPages != 3 || cp.LastErrorCode != "" {
		t.Fatalf("source 2 checkpoint %+v", cp)
	}
}

func fakeClock(f *Fetcher) *[]time.Duration {
	var slept []time.Duration
	clock := time.Unix(1_700_000_000, 0)
	f.now = func() time.Time { return clock }
	f.sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		clock = clock.Add(d)
		return nil
	}
	return &slept
}

func Test429RetryAfterInlineWait(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	f := NewFetcher(srv.Client(), FetchPolicy{MaxRetryWait: 30 * time.Second, MinInterval: time.Second})
	slept := fakeClock(f)
	doc, err := f.Fetch(context.Background(), srv.URL)
	if err != nil || string(doc.Body) != "ok" {
		t.Fatalf("doc %q err %v", doc.Body, err)
	}
	if len(*slept) != 1 || (*slept)[0] != 7*time.Second {
		t.Fatalf("slept %v, want exactly the Retry-After", *slept)
	}
	if pages, _ := f.Usage(); pages != 2 {
		t.Fatalf("retry must count against the budget, pages=%d", pages)
	}
}

func Test429LongRetryAfterBlocks(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", time.Unix(1_700_000_000, 0).Add(2*time.Hour).UTC().Format(http.TimeFormat))
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	f := NewFetcher(srv.Client(), FetchPolicy{MaxRetryWait: 30 * time.Second})
	slept := fakeClock(f)
	_, err := f.Fetch(context.Background(), srv.URL)
	b, ok := IsBlocked(err)
	if !ok || b.RetryAfter != 2*time.Hour || b.Reason != "rate_limited" {
		t.Fatalf("err %v", err)
	}
	if len(*slept) != 0 {
		t.Fatalf("must not sleep through a long Retry-After: %v", *slept)
	}
	// Further fetches on a blocked fetcher are refused without a request.
	if _, err := f.Fetch(context.Background(), srv.URL+"/other"); err == nil || calls != 1 {
		t.Fatalf("calls %d err %v", calls, err)
	}
}

func TestMinIntervalSpacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") }))
	defer srv.Close()
	f := NewFetcher(srv.Client(), FetchPolicy{MinInterval: 5 * time.Second})
	slept := fakeClock(f)
	for i := 0; i < 3; i++ {
		if _, err := f.Fetch(context.Background(), srv.URL); err != nil {
			t.Fatal(err)
		}
	}
	if len(*slept) != 2 || (*slept)[0] != 5*time.Second || (*slept)[1] != 5*time.Second {
		t.Fatalf("slept %v", *slept)
	}
}

func TestBlockDetection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		blocked bool
		reason  string
		retry   time.Duration
	}{
		{"403", 403, nil, "Forbidden", true, "forbidden", DefaultBlockBackoff},
		{"429 seconds", 429, map[string]string{"Retry-After": "90"}, "", true, "rate_limited", 90 * time.Second},
		{"429 huge", 429, map[string]string{"Retry-After": "99999999"}, "", true, "rate_limited", MaxBlockBackoff},
		{"cloudflare 200 challenge", 200, nil, `<html><head><title>Just a moment...</title></head><body><script src="/cdn-cgi/challenge-platform/h/b/orchestrate/chl_page/v1"></script></body></html>`, true, "challenge", DefaultBlockBackoff},
		{"cf-mitigated header", 403, map[string]string{"cf-mitigated": "challenge"}, "", true, "forbidden", DefaultBlockBackoff},
		{"503 challenge", 503, nil, `<form id="challenge-form"></form>`, true, "challenge", DefaultBlockBackoff},
		{"503 retry-after", 503, map[string]string{"Retry-After": "60"}, "maintenance", true, "unavailable", time.Minute},
		{"bare 503 is an outage", 503, nil, "maintenance", false, "", 0},
		{"captcha comment form is not a block", 200, nil, fixture(t, "javguru_page1.html"), false, "", 0},
		{"404", 404, nil, "", false, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			_, err := NewFetcher(srv.Client(), FetchPolicy{}).Fetch(context.Background(), srv.URL)
			b, ok := IsBlocked(err)
			if ok != tc.blocked {
				t.Fatalf("blocked=%v err=%v", ok, err)
			}
			if ok && (b.Reason != tc.reason || b.RetryAfter != tc.retry) {
				t.Fatalf("got %+v", b)
			}
			if !ok && tc.status >= 400 && err == nil {
				t.Fatal("non-2xx must error")
			}
		})
	}
}

func TestAdaptersNextPage(t *testing.T) {
	for _, tc := range []struct {
		name    string
		adapter Paginator
		url     string
		file    string
		want    string
	}{
		{"javdatabase page 1", JAVDatabaseAdapter{}, "https://www.javdatabase.com/movies/", "javdatabase_movies_page1.html", "https://www.javdatabase.com/movies/page/2/"},
		{"javdatabase page 2", JAVDatabaseAdapter{}, "https://www.javdatabase.com/movies/page/2/", "javdatabase_movies_page2.html", "https://www.javdatabase.com/movies/page/3/"},
		{"javdatabase last page", JAVDatabaseAdapter{}, "https://www.javdatabase.com/movies/page/3/", "javdatabase_movies_page3.html", ""},
		{"xcity", XCITYAdapter{}, "https://xxx.xcity.jp/idol/", "xcity_idol_list.html", "https://xxx.xcity.jp/idol/?page=2"},
		{"javguru", JavGuruAdapter{}, "https://jav.guru/", "javguru_page1.html", "https://jav.guru/page/2/"},
		{"cross-host next rejected", JavGuruAdapter{}, "https://mirror.example/", "javguru_page1.html", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.adapter.NextPage(Document{FinalURL: tc.url, Body: []byte(fixture(t, tc.file))})
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestAllAdaptersPaginate(t *testing.T) {
	for _, a := range []Adapter{NewJAVDatabaseAdapter(), NewXCITYAdapter(), NewJavGuruAdapter()} {
		if _, ok := a.(Paginator); !ok {
			t.Errorf("%s does not implement Paginator", a.Name())
		}
	}
}
