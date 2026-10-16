package catalog

import (
	"context"
	"encoding/json"
	"errors"
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

func builtinMaker(t *testing.T, slug string) MakerProfile {
	t.Helper()
	for _, p := range BuiltinMakerProfiles() {
		if p.Slug == slug {
			return p
		}
	}
	t.Fatalf("no built-in maker profile %q", slug)
	return MakerProfile{}
}

func mustMaker(t *testing.T, p MakerProfile) *MakerAdapter {
	t.Helper()
	a, err := NewMakerAdapter(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// rebase points a profile at another origin (a test server).
func rebase(p MakerProfile, origin string) MakerProfile {
	p.ListingURL = strings.Replace(p.ListingURL, p.BaseURL, origin, 1)
	p.BaseURL = origin
	return p
}

func makerDoc(t *testing.T, file, u string) Document {
	t.Helper()
	return Document{URL: u, FinalURL: u, Status: 200, ContentType: "text/html", Body: []byte(fixture(t, file))}
}

func TestBuiltinMakerProfiles(t *testing.T) {
	ps := BuiltinMakerProfiles()
	if len(ps) < 2 {
		t.Fatalf("built-in profiles = %d", len(ps))
	}
	want := map[string]string{"s1": "maker:s1-no1-style", "ideapocket": "maker:idea-pocket"}
	for _, p := range ps {
		a := mustMaker(t, p)
		if a.Name() != "maker:"+p.Slug || a.SourceKind() != "official_maker" || a.FixtureDir() != "maker-"+p.Slug {
			t.Errorf("%s: name %s kind %s dir %s", p.Slug, a.Name(), a.SourceKind(), a.FixtureDir())
		}
		if w, ok := want[p.Slug]; ok && a.MakerScope() != w {
			t.Errorf("%s: scope %s, want %s", p.Slug, a.MakerScope(), w)
		}
	}
	r := NewRegistry()
	for _, a := range Adapters() {
		if err := r.Register(a); err != nil {
			t.Fatal(err)
		}
	}
	if a, ok := r.Get("MAKER:S1"); !ok || a.Name() != "maker:s1" {
		t.Fatal("maker:s1 is not registered")
	}
}

func TestMakerProfileValidation(t *testing.T) {
	good := builtinMaker(t, "s1")
	for name, mutate := range map[string]func(*MakerProfile){
		"bad slug":              func(p *MakerProfile) { p.Slug = "S1:x" },
		"japanese maker name":   func(p *MakerProfile) { p.Maker = "エスワン" },
		"relative base":         func(p *MakerProfile) { p.BaseURL = "/x" },
		"listing on other host": func(p *MakerProfile) { p.ListingURL = "https://other.example/works/date" },
		"listing not matching":  func(p *MakerProfile) { p.ListingURL = good.BaseURL + "/other" },
		"detail without group":  func(p *MakerProfile) { p.DetailPattern = "^/works/detail/" },
		"no code pattern":       func(p *MakerProfile) { p.CodePattern = "" },
		"bad regexp":            func(p *MakerProfile) { p.CodePattern = "(" },
		"no title selector":     func(p *MakerProfile) { p.Release.Title = "" },
		"bad css":               func(p *MakerProfile) { p.Release.Code = "div[" },
		"performer no name":     func(p *MakerProfile) { p.Performer.Name = "" },
	} {
		p := good
		mutate(&p)
		if _, err := NewMakerAdapter(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ParseMakerProfiles([]byte(`{"profiles":[{"slug":"x","relase":{}}]}`)); err == nil {
		t.Error("unknown (misspelt) key accepted")
	}
}

func TestNormalizeMakerCode(t *testing.T) {
	for in, want := range map[string]string{
		"SONE-156":       "SONE-156",
		"sone00156":      "SONE-156",
		"品番：SONE-156":    "SONE-156",
		"ＳＯＮＥ－１５６":       "SONE-156",
		"118abp00123":    "ABP-123",
		"ipzz001":        "IPZZ-001",
		" ssis 1 ":       "",
		"SSIS-001 (4K版)": "SSIS-001",
		"":               "",
		"no code here":   "",
	} {
		if got := NormalizeMakerCode(in); got != want {
			t.Errorf("NormalizeMakerCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMakerDiscoverAndPaging(t *testing.T) {
	a := mustMaker(t, builtinMaker(t, "s1"))
	ctx := context.Background()
	doc := makerDoc(t, "maker-s1/listing_page1.html", "https://s1s1s1.com/works/date")
	recs, err := a.Discover(ctx, doc)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, fmt.Sprintf("%s:%s:%v", r.EntityType, r.SourceKey, r.Fields["dvd_code"]))
	}
	// Newest first; the foreign ABP title and the nav actress (outside
	// item_selector) are not discovered.
	want := []string{"release:sone00902:SONE-902", "release:sone00901:SONE-901", "performer:40001:<nil>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered %v, want %v", got, want)
	}
	if next := a.NextPage(doc); next != "https://s1s1s1.com/works/date?page=2" {
		t.Fatalf("next = %q", next)
	}
	if next := a.NextPage(makerDoc(t, "maker-s1/listing_page2.html", "https://s1s1s1.com/works/date?page=2")); next != "" {
		t.Fatalf("last page next = %q", next)
	}
	// A next link that is not a listing page, or on another host, is ignored.
	for _, href := range []string{"/works/detail/sone00001", "https://mirror.example/works/date?page=2"} {
		body := `<div class="p-pagination"><a rel="next" href="` + href + `">次へ</a></div>`
		if next := a.NextPage(Document{FinalURL: "https://s1s1s1.com/works/date", Body: []byte(body)}); next != "" {
			t.Errorf("next %s accepted as %q", href, next)
		}
	}
	// A discovery page on another host yields nothing.
	other := doc
	other.FinalURL = "https://mirror.example/works/date"
	if recs, _ := a.Discover(ctx, other); len(recs) != 0 {
		t.Fatalf("cross-host discovery %v", recs)
	}
}

func TestMakerParseRelease(t *testing.T) {
	a := mustMaker(t, builtinMaker(t, "s1"))
	ctx := context.Background()
	rec, err := a.Parse(ctx, makerDoc(t, "maker-s1/release_sone00901.html", "https://s1s1s1.com/works/detail/sone00901"), Release)
	if err != nil {
		t.Fatal(err)
	}
	f := rec.Fields
	want := map[string]any{
		"title": "架空タイトル一 合成フィクスチャ作品", "dvd_code": "SONE-901", "maker": "S1 NO.1 STYLE", "maker_scope": "maker:s1-no1-style",
		"maker_profile": "s1", "release_date": "2026-09-19", "runtime_minutes": "120", "label": "S1 NO.1 STYLE", "series": "架空シリーズ",
		"director": "架空監督", "cover_url": "https://s1s1s1.com/img/sone00901pl.jpg",
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %v, want %v", k, f[k], v)
		}
	}
	if !reflect.DeepEqual(f["performers"], []string{"合成 花子", "合成 二子"}) || !reflect.DeepEqual(f["genres"], []string{"単体作品", "ドラマ"}) {
		t.Errorf("performers %v genres %v", f["performers"], f["genres"])
	}
	if rec.SourceKey != "sone00901" || MakerScope(f["maker"].(string), "") != f["maker_scope"] {
		t.Errorf("key %s scope %v", rec.SourceKey, f["maker_scope"])
	}

	rec, err = a.Parse(ctx, makerDoc(t, "maker-s1/release_sone00900.html", "https://s1s1s1.com/works/detail/sone00900"), Release)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Fields["dvd_code"] != "SONE-900" || rec.Fields["release_date"] != "2026-08-07" || rec.Fields["runtime_minutes"] != "150" ||
		rec.Fields["cover_url"] != "https://s1s1s1.com/img/sone00900pl.jpg" || rec.Fields["series"] != nil {
		t.Errorf("sone00900 fields %v", rec.Fields)
	}

	_, err = a.Parse(ctx, makerDoc(t, "maker-s1/release_abp00123_foreign.html", "https://s1s1s1.com/works/detail/abp00123"), Release)
	if !errors.Is(err, ErrSkipRecord) {
		t.Fatalf("foreign title: %v", err)
	}
	// A redesign that loses the title fails the run instead of skipping.
	_, err = a.Parse(ctx, Document{FinalURL: "https://s1s1s1.com/works/detail/sone00901", Body: []byte("<html><body><p>x</p></body></html>")}, Release)
	if !errors.Is(err, ErrMakerNoTitle) {
		t.Fatalf("no title: %v", err)
	}
	// Pages of the wrong kind or host are refused.
	if _, err = a.Parse(ctx, makerDoc(t, "maker-s1/release_sone00901.html", "https://s1s1s1.com/actress/detail/1"), Release); err == nil {
		t.Fatal("performer URL parsed as release")
	}
}

func TestMakerParsePerformer(t *testing.T) {
	a := mustMaker(t, builtinMaker(t, "ideapocket"))
	rec, err := a.Parse(context.Background(), makerDoc(t, "maker-ideapocket/actress_50001.html", "https://ideapocket.com/actress/detail/50001"), Performer)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "架空 葵", "name_ja": "架空 葵", "name_romaji": "Aoi Kakuu", "birth_date": "2000-04-01", "height_cm": "158",
		"cup_size": "D", "birth_place": "東京都", "maker_scope": "maker:idea-pocket"}
	for k, v := range want {
		if rec.Fields[k] != v {
			t.Errorf("%s = %v, want %v", k, rec.Fields[k], v)
		}
	}
}

// makerSite serves the s1 fixtures; extra pages (path -> body) can be added.
type makerSite struct {
	*httptest.Server
	mu    sync.Mutex
	pages map[string]string
	hits  map[string]int
}

func newMakerSite(t *testing.T) *makerSite {
	t.Helper()
	s := &makerSite{pages: map[string]string{
		"/works/date":             fixture(t, "maker-s1/listing_page1.html"),
		"/works/date?page=2":      fixture(t, "maker-s1/listing_page2.html"),
		"/works/detail/sone00901": fixture(t, "maker-s1/release_sone00901.html"),
		"/works/detail/sone00900": fixture(t, "maker-s1/release_sone00900.html"),
		"/works/detail/sone00902": strings.Replace(fixture(t, "maker-s1/release_sone00901.html"), "SONE-901", "SONE-902", 1),
		"/actress/detail/40001":   fixture(t, "maker-s1/actress_40001.html"),
	}, hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		k := r.URL.RequestURI()
		s.hits[k]++
		body, ok := s.pages[k]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *makerSite) set(k, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages[k] = body
}

func (s *makerSite) hitCount(k string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[k]
}

// TestMakerCrawlWatermark runs the maker adapter through the generic crawl:
// a baseline walks both listing pages; an incremental run after one new
// title reads only the head, fetches only the new detail page and stops at
// the previous head (watermark); verify writes no checkpoint.
func TestMakerCrawlWatermark(t *testing.T) {
	s := newMakerSite(t)
	a := mustMaker(t, rebase(builtinMaker(t, "s1"), s.URL))
	cps := newFakeCheckpoints()
	run := func(phase Phase) (CrawlResult, *fakeSink) {
		t.Helper()
		sink := &fakeSink{}
		f := NewFetcher(s.Client(), FetchPolicy{})
		c := &Crawl{SourceID: 7, StartURL: a.StartURL(), Phase: phase, Adapter: a, Fetcher: f, Checkpoints: cps, Sink: sink, Now: func() time.Time { return time.Unix(1700000000, 0) }}
		res, err := c.Run(context.Background())
		if err != nil {
			t.Fatalf("%s: %v", phase, err)
		}
		return res, sink
	}
	res, sink := run(PhaseBaseline)
	if res.Pages != 2 || res.Stop != StopComplete || len(sink.saved) != 4 {
		t.Fatalf("baseline %+v saved %v", res, sink.saved)
	}
	if s.hitCount("/works/detail/abp00123") != 0 {
		t.Fatal("foreign title fetched")
	}
	res, sink = run(PhaseIncremental)
	if res.Stop != StopComplete || len(sink.saved) != 4 { // first incremental: no mark yet, reads to the end
		t.Fatalf("first incremental %+v saved %v", res, sink.saved)
	}
	// A new title appears at the head of the listing.
	page1 := fixture(t, "maker-s1/listing_page1.html")
	s.set("/works/date", strings.Replace(page1, `<section class="p-worksList">`,
		`<section class="p-worksList"><div class="item"><a href="/works/detail/sone00903">SONE-903</a></div>`, 1))
	s.set("/works/detail/sone00903", strings.Replace(fixture(t, "maker-s1/release_sone00901.html"), "SONE-901", "SONE-903", 1))
	before := s.hitCount("/works/detail/sone00902")
	res, sink = run(PhaseIncremental)
	if res.Stop != StopWatermark || res.Pages != 1 || !reflect.DeepEqual(sink.saved, []string{"7:sone00903"}) {
		t.Fatalf("incremental after a new title %+v saved %v", res, sink.saved)
	}
	if s.hitCount("/works/detail/sone00902") != before || s.hitCount("/works/date?page=2") != 2 {
		t.Fatal("incremental read past the watermark")
	}
	n := len(cps.advances)
	if res, _ = run(PhaseVerify); res.Pages != 2 || len(cps.advances) != n {
		t.Fatalf("verify %+v advanced checkpoints", res)
	}
}

func TestAdaptersFromEnvMakerOverride(t *testing.T) {
	p := builtinMaker(t, "s1")
	p.Release.Title = "h1.override"
	extra := p
	extra.Slug, extra.Maker = "moodyz-test", "MOODYZ"
	dir := t.TempDir()
	path := filepath.Join(dir, "makers.json")
	body := fmt.Sprintf(`{"profiles":[%s,%s]}`, mustJSON(t, p), mustJSON(t, extra))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	as, err := AdaptersFromEnv(func(k string) string {
		if k == MakerProfilesEnv {
			return path
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]Adapter{}
	for _, a := range as {
		if _, dup := names[a.Name()]; dup {
			t.Fatalf("duplicate adapter %s", a.Name())
		}
		names[a.Name()] = a
	}
	if names["maker:s1"].(*MakerAdapter).Profile().Release.Title != "h1.override" || names["maker:moodyz-test"] == nil || names["maker:ideapocket"] == nil || names["dmm"] == nil {
		t.Fatalf("adapters %v", names)
	}
	if _, err := AdaptersFromEnv(func(string) string { return filepath.Join(dir, "missing.json") }); err == nil {
		t.Fatal("missing profile file accepted")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
