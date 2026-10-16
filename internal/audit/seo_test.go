package audit

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func checks(fs []SEOFinding) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.Check)
	}
	return out
}

func TestCheckSEO(t *testing.T) {
	good := SEOFacts{EntityType: "release", ID: 1, Slug: "abc-123", Title: "A title", HasFacts: true, HasImage: true}
	in := seoInputs{coversOn: true,
		dupes:    map[int64]*SEOFinding{7: {Check: SEODuplicateTitle, Severity: SeverityLow, Langs: []string{"canonical"}, Others: []int64{8}}},
		failures: map[string]map[string]SEOFinding{"performer": {"hana": {Check: SEOSSRFailed, Severity: SeverityHigh}}}}
	cases := []struct {
		name string
		f    SEOFacts
		in   seoInputs
		want []string
	}{
		{"clean release", good, in, []string{}},
		{"blank title", SEOFacts{EntityType: "release", ID: 2, Slug: "x", Title: "  ", HasFacts: true, HasImage: true}, in, []string{SEOBlankTitle}},
		{"thin description", SEOFacts{EntityType: "release", ID: 3, Slug: "x", Title: "T", HasImage: true}, in, []string{SEOThinDescription}},
		{"synopsis is enough", SEOFacts{EntityType: "release", ID: 3, Slug: "x", Title: "T", HasSynopsis: true, HasImage: true}, in, []string{}},
		{"no cover", SEOFacts{EntityType: "release", ID: 4, Slug: "x", Title: "T", HasFacts: true}, in, []string{SEONoImage}},
		{"no cover while storage is off", SEOFacts{EntityType: "release", ID: 4, Slug: "x", Title: "T", HasFacts: true}, seoInputs{}, []string{}},
		{"fallback slug", SEOFacts{EntityType: "release", ID: 5, Slug: "release-5", Title: "T", HasFacts: true, HasImage: true}, in, []string{SEOFallbackSlug}},
		{"duplicate title", SEOFacts{EntityType: "release", ID: 7, Slug: "x", Title: "T", HasFacts: true, HasImage: true}, in, []string{SEODuplicateTitle}},
		{"performer ssr failure first", SEOFacts{EntityType: "performer", ID: 9, Slug: "hana", Title: "Hana", HasFacts: true}, in, []string{SEOSSRFailed}},
		{"performer never needs a cover", SEOFacts{EntityType: "performer", ID: 10, Slug: "performer-10", Title: "", HasFacts: true}, in, []string{SEOBlankTitle, SEOFallbackSlug}},
		{"worst first", SEOFacts{EntityType: "release", ID: 7, Slug: "release-7", Title: " "}, in, []string{SEOBlankTitle, SEODuplicateTitle, SEOThinDescription, SEONoImage, SEOFallbackSlug}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := checks(CheckSEO(c.f, c.in)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("checks = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDuplicateFindings(t *testing.T) {
	got := duplicateFindings([]TitleGroup{{Lang: "", Title: "t", IDs: []int64{1, 2, 3}}, {Lang: "en", Title: "t", IDs: []int64{1, 4}}})
	if len(got) != 4 {
		t.Fatalf("findings for %d releases", len(got))
	}
	if d := got[1]; !reflect.DeepEqual(d.Langs, []string{"canonical", "en"}) || !reflect.DeepEqual(d.Others, []int64{2, 3, 4}) {
		t.Fatalf("release 1: %+v", d)
	}
	if d := got[4]; !reflect.DeepEqual(d.Langs, []string{"en"}) || !reflect.DeepEqual(d.Others, []int64{1}) {
		t.Fatalf("release 4: %+v", d)
	}
}

// reportDetail decodes a JSON seo-check report the way nightly_runs.detail
// comes back from the database.
func reportDetail(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

const sampleReport = `{
  "sitemap": {"releases": {"listed": 90, "expected": 100}, "performers": {"listed": 20, "expected": 20}, "tags": {"listed": 5, "expected": null}},
  "sitemap_missing_langs": ["releases:th"],
  "counts_source": "http://api/api/sitemap/counts",
  "score": 88, "ssr_pass_rate": 0.9, "sitemap_coverage": 0.9,
  "failures": [
    {"url": "https://reel.example/en/releases/abc-001", "reasons": ["canonical", "hreflang"]},
    {"url": "https://reel.example/performers/%E6%A9%8B%E6%9C%AC", "reasons": ["status_500"]},
    {"url": "https://reel.example/discover?tag=x", "reasons": ["no_h1"]},
    {"url": "::bad", "reasons": []}
  ]
}`

func TestSEOReportParsing(t *testing.T) {
	d := reportDetail(t, sampleReport)
	fails, other := SEOPageFailures(d)
	if other != 2 {
		t.Errorf("other = %d", other)
	}
	if f, ok := fails["release"]["abc-001"]; !ok || !reflect.DeepEqual(f.Reasons, []string{"canonical", "hreflang"}) || f.Severity != SeverityHigh {
		t.Errorf("release failure: %+v", fails["release"])
	}
	if _, ok := fails["performer"]["橋本"]; !ok {
		t.Errorf("performer failure (unescaped slug): %+v", fails["performer"])
	}
	site := SEOReportFindings(d)
	if len(site) != 2 || site[0].Check != SEOSiteSitemapGap || site[0].Type != "releases" || site[0].Listed != 90 || site[0].Want != 100 ||
		site[1].Check != SEOSiteMissingLangs {
		t.Fatalf("site findings %+v", site)
	}
	if got := SEOReportFindings(map[string]any{"counts_source": "unavailable"}); len(got) != 1 || got[0].Check != SEOSiteCountsUnavailable {
		t.Fatalf("unavailable counts: %+v", got)
	}
}

func seoFake(t *testing.T) *fakeStore {
	f := newFake()
	f.seoSite = SEOSiteFacts{SitemapReleases: 3, SitemapPerformers: 2, ReleasesWithCover: 1}
	f.seo = []SEOFacts{
		{EntityType: "release", ID: 1, Slug: "abc-001", Title: "One", HasFacts: true, HasImage: true, Popularity: 40},
		{EntityType: "release", ID: 2, Slug: "release-2", Title: "Same", HasFacts: true, HasImage: true},
		{EntityType: "release", ID: 3, Slug: "def-003", Title: "same", HasFacts: true, HasImage: true, Popularity: 9},
		{EntityType: "performer", ID: 10, Slug: "hana", Title: "Hana", HasFacts: true},
		{EntityType: "performer", ID: 11, Slug: "橋本", Title: "橋本", HasFacts: true},
	}
	f.seoDupes = []TitleGroup{{Title: "same", IDs: []int64{2, 3}}}
	f.latestRun = map[string]Run{SEOCheckJob: {Job: SEOCheckJob, StartedAt: time.Date(2026, 10, 14, 3, 0, 0, 0, time.UTC), Detail: reportDetail(t, sampleReport)}}
	return f
}

func TestRuleSEOOpensIssuesAndNeverActs(t *testing.T) {
	f := seoFake(t)
	r := &Runner{Store: f, Now: clock(), BatchSize: 2}
	start := r.now()
	out, err := r.ruleSEO(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	// Entities: 1 (ssr), 2 (dup + slug), 3 (dup), 11 (ssr); site: gap + langs.
	if out.checked != 5 || out.issues != 4+2 || out.fixed != 0 {
		t.Fatalf("outcome %+v", out)
	}
	open := f.open(RuleSEO)
	if len(open) != 4 {
		t.Fatalf("issues %+v", open)
	}
	byID := map[int64]*Issue{}
	for _, is := range open {
		byID[is.EntityID] = is
	}
	if is := byID[1]; is == nil || is.Severity != SeverityHigh || is.Priority != 40 || is.Detail["auto_action"] != "none" {
		t.Errorf("release 1: %+v", is)
	}
	if is := byID[2]; is == nil || is.Severity != SeverityLow || len(is.Detail["findings"].([]SEOFinding)) != 2 {
		t.Errorf("release 2: %+v", is)
	}
	if is := byID[11]; is == nil || is.EntityType != "performer" || is.Detail["name"] != "橋本" {
		t.Errorf("performer 11: %+v", is)
	}
	if len(f.events) != 0 {
		t.Fatalf("rule changed data: %+v", f.events)
	}
	sc, _ := out.detail["seo_check"].(map[string]any)
	if sc["found"] != true || sc["score"] != float64(88) || sc["page_failures_other"] != 2 {
		t.Errorf("seo_check detail %+v", sc)
	}

	// Release 3 got its own title (release 2 keeps its fallback slug) and an
	// admin dismissed release 1: the next run resolves 3, refreshes 2 and
	// does not reopen 1 while its findings are unchanged.
	byID[1].Status = StatusDismissed
	f.seoDupes = nil
	f.seo[2].Title = "Three"
	out, err = r.ruleSEO(context.Background(), r.now())
	if err != nil {
		t.Fatal(err)
	}
	if out.fixed != 1 || out.issues != 2+2 {
		t.Fatalf("second run %+v", out)
	}
	if byID[3].Status != StatusFixed || byID[1].Status != StatusDismissed {
		t.Fatalf("statuses 1=%s 3=%s", byID[1].Status, byID[3].Status)
	}
	open = f.open(RuleSEO)
	if len(open) != 2 || open[0].EntityID != 2 || checks(open[0].Detail["findings"].([]SEOFinding))[0] != SEOFallbackSlug {
		t.Fatalf("open after second run: %+v", open)
	}
}

func TestRuleSEOWithoutOrWithStaleReport(t *testing.T) {
	f := seoFake(t)
	delete(f.latestRun, SEOCheckJob)
	r := &Runner{Store: f, Now: clock()}
	out, err := r.ruleSEO(context.Background(), r.now())
	if err != nil {
		t.Fatal(err)
	}
	// No report: page checks only (2 and 3), and no site issue.
	if out.issues != 2 || out.detail["note"] == nil {
		t.Fatalf("no report: %+v", out)
	}

	f = seoFake(t)
	run := f.latestRun[SEOCheckJob]
	run.StartedAt = run.StartedAt.Add(-30 * 24 * time.Hour)
	f.latestRun[SEOCheckJob] = run
	r = &Runner{Store: f, Now: clock()}
	out, err = r.ruleSEO(context.Background(), r.now())
	if err != nil {
		t.Fatal(err)
	}
	// Stale: page failures are not raised; gap + langs + stale are.
	if out.issues != 2+3 {
		t.Fatalf("stale report: %+v", out)
	}
	for _, is := range f.open(RuleSEO) {
		if is.EntityID == 1 || is.EntityID == 11 {
			t.Errorf("stale failure raised: %+v", is)
		}
	}
}

func TestRunnerRulesSelectsRules(t *testing.T) {
	f := seoFake(t)
	rep, err := (&Runner{Store: f, Now: clock(), Rules: []Rule{RuleSEO, RuleAge}}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Runs) != 2 || rep.Runs[0].Job != "audit.R-AGE" || rep.Runs[1].Job != "audit.R-SEO" || rep.Runs[1].Result != ResultIssues {
		t.Fatalf("runs %+v", rep.Runs)
	}
}
