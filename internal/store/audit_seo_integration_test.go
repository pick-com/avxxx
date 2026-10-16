package store

import (
	"avxxx/internal/audit"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestAuditSEOIntegration runs R-SEO (audit -once -rules R-SEO) against a
// real, disposable PostgreSQL database: sitemap-listed entities, duplicate
// canonical and translated titles, stored covers, the latest seo.check report
// and issue resolution. Skipped unless AVXXX_TEST_DATABASE_URL is set; the
// database is modified. Assertions are scoped to the rows this test creates.
func TestAuditSEOIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	release := func(slug, title string, playable bool, extra string) int64 {
		t.Helper()
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,maker_name,synopsis) VALUES($1,$2,'published',$3,NULLIF($4,'')) RETURNING id`,
			slug+"-"+sfx, title, "Maker", extra).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if playable {
			exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url) VALUES($1,'external_link','p','x',$2)`, id, "https://example.test/seo/"+slug+sfx)
		}
		return id
	}
	dupTitle := "SEO Same " + sfx
	a := release("seo-a", dupTitle, true, "")
	b := release("seo-b", "  seo same "+sfx+" ", true, "")         // same title after case / space folding
	c := release("seo-c", "SEO unique C "+sfx, true, "")           // gets a fallback slug below
	d := release("seo-d", "SEO hidden "+sfx, false, "")            // published, not playable: not in the sitemap
	e := release("seo-e", "SEO unique E "+sfx, true, "A synopsis") // translated title clashes with a's
	exec(`UPDATE releases SET slug='release-'||id WHERE id=$1`, c)
	// Covers are in use (e has one), so a, b and c are missing theirs.
	exec(`INSERT INTO media_assets(entity_type,entity_id,asset_type,object_key,content_sha256,mime_type) VALUES('release',$1,'cover',$2,$2,'image/jpeg')`, e, "seo-cover-"+sfx)
	enTitle := "Same English " + sfx
	for _, id := range []int64{a, e} {
		exec(`INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,reviewed_value,reviewed_at)
			VALUES('release',$1,'title','en',$2,'reviewed','h',$2,now())`, fmt.Sprint(id), enTitle)
	}
	var p int64
	if err = s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$2) RETURNING id`, "seo-p-"+sfx, "SEO Performer "+sfx).Scan(&p); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, a, p)

	// The latest seo.check: e's English page and the performer page failed.
	report := map[string]any{
		"score": 70, "band": "degraded", "ssr_pass_rate": 0.5, "sitemap_coverage": 1, "counts_source": "http://api/api/sitemap/counts",
		"sitemap": map[string]any{"releases": map[string]any{"listed": 3, "expected": 3}},
		"failures": []any{
			map[string]any{"url": "https://reel.example/en/releases/seo-e-" + sfx, "reasons": []any{"hreflang"}},
			map[string]any{"url": "https://reel.example/performers/seo-p-" + sfx, "reasons": []any{"no_h1"}},
		},
	}
	raw, _ := json.Marshal(report)
	exec(`INSERT INTO nightly_runs(job,started_at,checked,issues,result,detail) VALUES($1,now(),2,2,'issues',$2::jsonb)`, audit.SEOCheckJob, string(raw))

	// Store inputs.
	site, err := s.SEOSite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if site.ReleasesWithCover < 1 || site.SitemapReleases < 4 || site.PublishedReleases < site.SitemapReleases+1 || site.TitleTranslations["en"] < 2 {
		t.Fatalf("site %+v", site)
	}
	groups, err := s.SEODuplicateTitles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var canon, en []int64
	for _, g := range groups {
		if len(g.IDs) == 2 && g.IDs[0] == a && g.IDs[1] == b && g.Lang == "" {
			canon = g.IDs
		}
		if len(g.IDs) == 2 && g.IDs[0] == a && g.IDs[1] == e && g.Lang == "en" {
			en = g.IDs
		}
	}
	if canon == nil || en == nil {
		t.Fatalf("duplicate groups %+v", groups)
	}

	run := func() audit.Run {
		t.Helper()
		rep, err := (&audit.Runner{Store: s, Rules: []audit.Rule{audit.RuleSEO}}).Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Runs) != 1 || rep.Runs[0].Job != "audit.R-SEO" || rep.Runs[0].Result != audit.ResultIssues {
			t.Fatalf("report %+v", rep)
		}
		return rep.Runs[0]
	}
	run()

	type row struct {
		severity string
		checks   map[string]bool
	}
	issues := func() map[string]row {
		t.Helper()
		rows, err := s.Query(ctx, `SELECT entity_type||':'||entity_id,severity,detail FROM audit_issues
			WHERE rule='R-SEO' AND status='open' AND ((entity_type='release' AND entity_id=ANY($1)) OR (entity_type='performer' AND entity_id=$2))`,
			[]int64{a, b, c, d, e}, p)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]row{}
		for rows.Next() {
			var key, sev string
			var detail struct {
				Findings []audit.SEOFinding `json:"findings"`
			}
			var raw []byte
			if err = rows.Scan(&key, &sev, &raw); err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(raw, &detail); err != nil {
				t.Fatal(err)
			}
			r := row{severity: sev, checks: map[string]bool{}}
			for _, f := range detail.Findings {
				r.checks[f.Check] = true
			}
			out[key] = r
		}
		return out
	}
	key := func(typ string, id int64) string { return fmt.Sprintf("%s:%d", typ, id) }
	got := issues()
	want := map[string][]string{
		key("release", a):   {audit.SEODuplicateTitle, audit.SEONoImage},
		key("release", b):   {audit.SEODuplicateTitle, audit.SEONoImage},
		key("release", c):   {audit.SEONoImage, audit.SEOFallbackSlug},
		key("release", e):   {audit.SEOSSRFailed, audit.SEODuplicateTitle},
		key("performer", p): {audit.SEOSSRFailed},
	}
	if len(got) != len(want) {
		t.Fatalf("issues %+v, want %v", got, want)
	}
	for k, checks := range want {
		r, ok := got[k]
		if !ok || len(r.checks) != len(checks) {
			t.Fatalf("%s: %+v, want %v", k, r, checks)
		}
		for _, c := range checks {
			if !r.checks[c] {
				t.Fatalf("%s: %+v, want %v", k, r, checks)
			}
		}
	}
	if got[key("release", e)].severity != string(audit.SeverityHigh) || got[key("release", a)].severity != string(audit.SeverityLow) {
		t.Errorf("severities %+v", got)
	}
	// The rule never changes catalog data.
	var events int
	if err = s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE actor='audit' AND entity_type='release' AND entity_id=ANY($1)`, []int64{a, b, c, d, e}).Scan(&events); err != nil || events != 0 {
		t.Fatalf("review events %d %v", events, err)
	}

	// Fixes: b gets its own title, c a cover and a real slug, a newer report
	// has no failures. The next run resolves c and p, and narrows b (no
	// image) and e (the English duplicate only).
	exec(`UPDATE releases SET canonical_title=$2 WHERE id=$1`, b, "SEO distinct B "+sfx)
	exec(`UPDATE releases SET slug=$2 WHERE id=$1`, c, "seo-c-"+sfx)
	exec(`INSERT INTO media_assets(entity_type,entity_id,asset_type,object_key,content_sha256,mime_type) VALUES('release',$1,'cover',$2,$2,'image/jpeg')`, c, "seo-cover-c-"+sfx)
	report["failures"] = []any{}
	raw, _ = json.Marshal(report)
	exec(`INSERT INTO nightly_runs(job,started_at,result,detail) VALUES($1,now(),'ok',$2::jsonb)`, audit.SEOCheckJob, string(raw))
	second := run()
	if fixed := second.Fixed; fixed < 2 {
		t.Errorf("second run fixed %d", fixed)
	}
	got = issues()
	if len(got) != 3 || !got[key("release", a)].checks[audit.SEONoImage] || got[key("release", a)].checks[audit.SEODuplicateTitle] != true ||
		!got[key("release", b)].checks[audit.SEONoImage] || got[key("release", b)].checks[audit.SEODuplicateTitle] ||
		len(got[key("release", e)].checks) != 1 || !got[key("release", e)].checks[audit.SEODuplicateTitle] {
		t.Fatalf("after fixes %+v", got)
	}
}
