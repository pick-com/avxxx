package catalog

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixtureDoc(t *testing.T, dir, file, url string) Document {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return Document{URL: url, FinalURL: url, Status: 200, ContentType: "text/html", Body: b}
}

func TestXSListParseMicrodata(t *testing.T) {
	a := NewXSListAdapter()
	rec, err := a.Parse(context.Background(), fixtureDoc(t, "xslist", "performers/model_90001.html", "https://xslist.org/en/model/90001.html"), Performer)
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Fields
	want := map[string]any{"name": "Hanako Gousei", "name_ja": "合成花子", "birth_date": "2000-04-01", "birth_place": "Tokyo",
		"height_cm": "158", "measurements": "B84(D) / W58 / H86", "bust_cm": "84", "waist_cm": "58", "hip_cm": "86", "cup_size": "D",
		"blood_type": "A", "activity": "2019-05 ~ 2024-12"}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %#v, want %#v", k, f[k], v)
		}
	}
	if rec.SourceKey != "90001" || rec.EntityType != Performer {
		t.Fatalf("key %q type %s", rec.SourceKey, rec.EntityType)
	}
	if got := f["aliases"]; !reflect.DeepEqual(got, []string{"Hana Gousei", "合成はな"}) {
		t.Errorf("aliases %#v", got)
	}
	// Filmography is evidence only: codes, de-duplicated, never releases.
	if got := f["release_refs"]; !reflect.DeepEqual(got, []string{"ZZZ-001", "ZZZ-002"}) {
		t.Errorf("release_refs %#v", got)
	}
	if _, ok := f["performers"]; ok {
		t.Error("a profile must not carry release credits")
	}
}

func TestXSListParseDefinitionList(t *testing.T) {
	rec, err := NewXSListAdapter().Parse(context.Background(), fixtureDoc(t, "xslist", "performers/model_90002.html", "https://xslist.org/en/model/90002.html"), Performer)
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Fields
	want := map[string]any{"name": "Mika Kakuu", "name_ja": "架空美香", "birth_date": "1998-03-03", "height_cm": "162",
		"bust_cm": "86", "waist_cm": "59", "hip_cm": "88", "cup_size": "E", "debut_date": "2018-07-15"}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %#v, want %#v", k, f[k], v)
		}
	}
	if got := f["aliases"]; !reflect.DeepEqual(got, []string{"Mika K", "架空みか"}) {
		t.Errorf("aliases %#v", got)
	}
}

func TestXSListDiscoverAndPaging(t *testing.T) {
	a := NewXSListAdapter()
	doc := fixtureDoc(t, "xslist", "discovery/ranking_page1.html", "https://xslist.org/en/ranking")
	recs, err := a.Discover(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, r := range recs {
		keys = append(keys, r.SourceKey+"="+r.Fields["name"].(string))
	}
	// Nav-menu links and off-site links are not records.
	if !reflect.DeepEqual(keys, []string{"90001=Hanako Gousei", "90002=Mika Kakuu"}) {
		t.Fatalf("records %v", keys)
	}
	if next := a.(Paginator).NextPage(doc); next != "https://xslist.org/en/ranking?page=2" {
		t.Fatalf("next %q", next)
	}
	last := fixtureDoc(t, "xslist", "discovery/ranking_page2.html", "https://xslist.org/en/ranking?page=2")
	if next := a.(Paginator).NextPage(last); next != "" {
		t.Fatalf("last page next %q", next)
	}
	if _, err := a.Parse(context.Background(), doc, Release); err == nil {
		t.Fatal("xslist parses performer profiles only")
	}
	if got := XSListLookupURL(" Hanako Gousei "); got != "https://xslist.org/search?lg=en&query=Hanako+Gousei" {
		t.Fatalf("lookup %q", got)
	}
}

func TestJAVLibraryParse(t *testing.T) {
	a := NewJAVLibraryAdapter()
	rec, err := a.Parse(context.Background(), fixtureDoc(t, "javlibrary", "releases/javlizzzz1.html", "https://www.javlibrary.com/en/?v=javlizzzz1"), Release)
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Fields
	want := map[string]any{"title": "Synthetic Title One", "dvd_code": "ZZZ-001", "release_date": "2024-06-01", "runtime_minutes": "120",
		"director": "Synthetic Director", "maker": "Synthetic Maker", "label": "Synthetic Label", "javlibrary_user_rating": "8.30"}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %#v, want %#v", k, f[k], v)
		}
	}
	if rec.SourceKey != "javlizzzz1" {
		t.Fatalf("key %q", rec.SourceKey)
	}
	if !reflect.DeepEqual(f["genres"], []string{"Genre One", "Genre Two"}) || !reflect.DeepEqual(f["cast"], []string{"Gousei Hanako", "Kakuu Mika"}) ||
		!reflect.DeepEqual(f["cast_ids"], []string{"zzaa", "zzbb"}) {
		t.Fatalf("lists %#v %#v %#v", f["genres"], f["cast"], f["cast_ids"])
	}
	// Community evidence only: no credits (family-name-first English
	// names would create swapped duplicates) and no public tags.
	for _, k := range []string{"performers", "performer_refs", "tags", "cover_url", "user_rating"} {
		if _, ok := f[k]; ok {
			t.Errorf("javlibrary must not emit %s", k)
		}
	}

	sparse, err := a.Parse(context.Background(), fixtureDoc(t, "javlibrary", "releases/javlizzzz2.html", "https://www.javlibrary.com/en/?v=javlizzzz2"), Release)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"label", "director", "javlibrary_user_rating"} {
		if _, ok := sparse.Fields[k]; ok {
			t.Errorf("sparse page invented %s = %#v", k, sparse.Fields[k])
		}
	}
	if _, err := a.Parse(context.Background(), fixtureDoc(t, "javlibrary", "releases/javlizzzz2.html", "https://www.javlibrary.com/en/vl_star.php?s=zzaa"), Release); err == nil {
		t.Fatal("a listing URL is not a release page")
	}
}

func TestJAVLibraryDiscoverAndPaging(t *testing.T) {
	a := NewJAVLibraryAdapter()
	doc := fixtureDoc(t, "javlibrary", "discovery/newrelease_page1.html", "https://www.javlibrary.com/en/vl_newrelease.php")
	recs, err := a.Discover(context.Background(), doc)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, r := range recs {
		got = append(got, fmt.Sprintf("%s|%v|%v|%s", r.SourceKey, r.Fields["dvd_code"], r.Fields["title"], r.URL))
	}
	want := []string{
		"javlizzzz1|ZZZ-001|Synthetic Title One|https://www.javlibrary.com/en/?v=javlizzzz1",
		"javlizzzz2|ZZZ-002|Synthetic Title Two|https://www.javlibrary.com/en/?v=javlizzzz2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records\n%v\nwant\n%v", got, want)
	}
	if next := a.(Paginator).NextPage(doc); next != "https://www.javlibrary.com/en/vl_newrelease.php?&mode=&page=2" {
		t.Fatalf("next %q", next)
	}
	last := fixtureDoc(t, "javlibrary", "discovery/newrelease_page2.html", "https://www.javlibrary.com/en/vl_newrelease.php?&mode=&page=2")
	if next := a.(Paginator).NextPage(last); next != "" {
		t.Fatalf("last page next %q", next)
	}
	for raw, want := range map[string]string{
		"https://www.javlibrary.com/en/?v=javli4abcd":           "javli4abcd",
		"https://www.javlibrary.com/ja/index.php?v=javli4abcd":  "javli4abcd",
		"https://www.javlibrary.com/en/vl_star.php?s=zzaa&v=x1": "",
		"https://elsewhere.example/en/?v=javli4abcd":            "",
	} {
		if _, key := javlibraryClassify(raw); key != want {
			t.Errorf("%s: key %q, want %q", raw, key, want)
		}
	}
}

// challengeSite serves synthetic catalog pages and, once tripped, a
// Cloudflare-style managed challenge for every path (robots.txt included).
type challengeSite struct {
	*httptest.Server
	mu        sync.Mutex
	pages     map[string]string
	challenge bool // every path, robots.txt included
	pagesOnly bool // pages challenged, robots.txt still served
	hits      []string
}

func newChallengeSite(t *testing.T, pages map[string]string) *challengeSite {
	t.Helper()
	s := &challengeSite{pages: pages}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.hits = append(s.hits, r.URL.RequestURI())
		if s.challenge || (s.pagesOnly && r.URL.Path != "/robots.txt") {
			w.Header().Set("cf-mitigated", "challenge")
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body><form id="challenge-form"></form></body></html>`)
			return
		}
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		body, ok := s.pages[r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *challengeSite) trip() { s.mu.Lock(); s.challenge = true; s.mu.Unlock() }

func (s *challengeSite) tripPages() { s.mu.Lock(); s.pagesOnly = true; s.mu.Unlock() }

func (s *challengeSite) pageHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		if h != "/robots.txt" {
			n++
		}
	}
	return n
}

func readFixture(t *testing.T, dir, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", dir, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestChallengeBlocksAndKeepsData: a source that turns to a bot challenge
// mid-crawl is reported blocked; the records saved before stay, nothing is
// saved from the challenge page, and the baseline cursor stays on the last
// fully persisted page, so the next run resumes there.
func TestChallengeBlocksAndKeepsData(t *testing.T) {
	for _, tc := range []struct {
		adapter Adapter
		pages   map[string]string
		start   string
		next    string
	}{
		{NewXSListAdapter(), map[string]string{
			"/en/ranking":          readFixture(t, "xslist", "discovery/ranking_page1.html"),
			"/en/model/90001.html": readFixture(t, "xslist", "performers/model_90001.html"),
			"/en/model/90002.html": readFixture(t, "xslist", "performers/model_90002.html"),
		}, "/en/ranking", "/en/ranking?page=2"},
		{NewJAVLibraryAdapter(), map[string]string{
			"/en/vl_newrelease.php": readFixture(t, "javlibrary", "discovery/newrelease_page1.html"),
			"/en/?v=javlizzzz1":     readFixture(t, "javlibrary", "releases/javlizzzz1.html"),
			"/en/?v=javlizzzz2":     readFixture(t, "javlibrary", "releases/javlizzzz2.html"),
		}, "/en/vl_newrelease.php", "/en/vl_newrelease.php?&mode=&page=2"},
	} {
		t.Run(tc.adapter.Name(), func(t *testing.T) {
			site := newChallengeSite(t, tc.pages)
			// Site links are absolute to the real host in places: serve them
			// from the test server by rewriting the fixture hosts.
			for k, v := range site.pages {
				v = strings.ReplaceAll(v, "https://xslist.org", site.URL)
				site.pages[k] = strings.ReplaceAll(v, "https://www.javlibrary.com", site.URL)
			}
			ctx := context.Background()
			ua := UserAgent("ops@example.com")
			sink := &fakeSink{}
			cps := newFakeCheckpoints()
			run := func() (CrawlResult, error) {
				f := NewFetcher(site.Client(), FetchPolicy{})
				f.UserAgent, f.Robots, f.Automatic = ua, NewRobots(site.Client(), ua), true
				c := &Crawl{SourceID: 7, StartURL: site.URL + tc.start, Phase: PhaseBaseline, Adapter: tc.adapter, Fetcher: f, Checkpoints: cps, Sink: sink, MaxPages: 1}
				return c.Run(ctx)
			}
			res, err := run()
			if err != nil || res.Records != 2 {
				t.Fatalf("first run: %+v %v", res, err)
			}
			saved := len(sink.saved)
			cursor := cps.rows[CheckpointKey{SourceID: 7, TaskType: TaskDiscovery, Partition: string(PhaseBaseline), ParserVersion: tc.adapter.Name()}].Cursor
			if cursor != site.URL+tc.next {
				t.Fatalf("cursor %q", cursor)
			}

			key := CheckpointKey{SourceID: 7, TaskType: TaskDiscovery, Partition: string(PhaseBaseline), ParserVersion: tc.adapter.Name()}
			// Pages turn into challenges (robots.txt still readable): the
			// automatic run stops at the first challenged page, blocked.
			site.tripPages()
			_, err = run()
			if b, ok := IsBlocked(err); !ok || b.Reason != "challenge" || b.Status != http.StatusForbidden {
				t.Fatalf("automatic run behind a challenge: %v", err)
			}
			if cps.rows[key].LastErrorCode != "source_blocked" {
				t.Fatalf("checkpoint error %q", cps.rows[key].LastErrorCode)
			}
			// robots.txt itself challenged: a verify run is blocked before it
			// fetches any page.
			site.trip()
			before := site.pageHits()
			f := NewFetcher(site.Client(), FetchPolicy{})
			f.UserAgent, f.Robots = ua, NewRobots(site.Client(), ua)
			report := NewParseReport(tc.adapter.Name(), 5)
			_, err = (&Crawl{SourceID: 7, StartURL: site.URL + tc.start, Phase: PhaseVerify, Adapter: tc.adapter, Fetcher: f, Sink: report, MaxPages: 1}).Run(ctx)
			b, ok := IsBlocked(err)
			if !ok || b.Reason != "challenge" || b.Status != http.StatusForbidden || !strings.HasSuffix(b.URL, "/robots.txt") {
				t.Fatalf("verify behind a challenge: %v", err)
			}
			if site.pageHits() != before {
				t.Fatal("a page was fetched although robots.txt was challenged")
			}
			if len(sink.saved) != saved || len(report.Failures) > 0 {
				t.Fatalf("challenge changed saved data: %d -> %d", saved, len(sink.saved))
			}
			if got := cps.rows[key].Cursor; got != cursor {
				t.Fatalf("cursor moved behind a challenge: %q", got)
			}
		})
	}
}

// TestRobotsChallengeIsUnavailable: a challenge served instead of
// robots.txt is "rules unknown", not RFC 9309's "no robots.txt".
func TestRobotsChallengeIsUnavailable(t *testing.T) {
	site := newChallengeSite(t, nil)
	site.trip()
	r := NewRobots(site.Client(), "ua")
	r.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	v := r.Check(context.Background(), site.URL+"/x")
	if !v.Unavailable || v.Allowed || !v.Challenge || v.ChallengeStatus != http.StatusForbidden || !strings.Contains(v.Reason, "challenge") {
		t.Fatalf("verdict %+v", v)
	}
}
