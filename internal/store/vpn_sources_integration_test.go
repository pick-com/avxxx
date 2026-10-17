package store

import (
	"avxxx/internal/catalog"
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestVPNSourceSeedIntegration checks the XSList / JAVLibrary seed: disabled,
// aggregator, review_required with no terms review (verify only), egress
// policy vpn, a conservative crawl policy, and field policies; idempotent on
// re-open.
func TestVPNSourceSeedIntegration(t *testing.T) {
	openIsolatedTestDB(t, "vpn_seed")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // the second Open re-applies every migration
		s, err := Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := map[string]string{catalog.XSListAdapterName: catalog.XSListStartURL, catalog.JAVLibraryAdapterName: catalog.JAVLibraryStartURL}
	for adapter, startURL := range want {
		var n int
		var u, kind, license, egress, terms string
		var enabled bool
		var reviewed *time.Time
		var minSec, budget, baseBudget, perRun int
		var proxy *string
		if err := s.QueryRow(ctx, `SELECT count(*) OVER (),s.url,s.enabled,s.source_kind,s.license_status,s.egress_policy,COALESCE(s.terms_url,''),s.terms_reviewed_at,
			EXTRACT(EPOCH FROM p.min_request_interval)::int,p.daily_page_budget,p.baseline_daily_page_budget,p.baseline_pages_per_run,p.proxy_url
			FROM sources s JOIN source_crawl_policies p ON p.source_id=s.id WHERE s.adapter=$1`, adapter).
			Scan(&n, &u, &enabled, &kind, &license, &egress, &terms, &reviewed, &minSec, &budget, &baseBudget, &perRun, &proxy); err != nil {
			t.Fatalf("%s: %v", adapter, err)
		}
		if n != 1 || u != startURL || enabled || kind != "aggregator" || license != catalog.LicenseReviewRequired || egress != "vpn" || terms != "" || reviewed != nil {
			t.Errorf("%s: n=%d url=%s enabled=%v kind=%s license=%s egress=%s terms=%q reviewed=%v", adapter, n, u, enabled, kind, license, egress, terms, reviewed)
		}
		if minSec < 5 || budget <= 0 || budget > 200 || baseBudget <= 0 || perRun <= 0 || proxy != nil {
			t.Errorf("%s: min=%ds budget=%d baseline=%d/%d proxy=%v", adapter, minSec, budget, baseBudget, perRun, proxy)
		}
		gov := catalog.SourceGovernance{Adapter: adapter, LicenseStatus: license, TermsURL: terms, TermsReviewedAt: reviewed}
		if catalog.Gate(gov, catalog.PhaseBaseline, 0).Allowed || catalog.Gate(gov, catalog.PhaseIncremental, 0).Allowed || !catalog.Gate(gov, catalog.PhaseVerify, 0).Allowed {
			t.Errorf("%s: the gate lets an unreviewed source crawl automatically", adapter)
		}
	}
	var lowest int
	if err := s.QueryRow(ctx, `SELECT min(fp.priority) FROM source_field_policies fp JOIN sources s ON s.id=fp.source_id WHERE s.adapter='javlibrary'`).Scan(&lowest); err != nil {
		t.Fatal(err)
	}
	if lowest <= TierPriority("third_party_api") {
		t.Fatalf("javlibrary field priority %d must sit below every other tier", lowest)
	}
}

// TestCrawlPolicyProxyIntegration: proxy_url round-trips through the crawl
// policy API and FetchPolicy, older clients (no proxy_url) keep it,
// credentials are refused by the API and by the database CHECK.
func TestCrawlPolicyProxyIntegration(t *testing.T) {
	openIsolatedTestDB(t, "vpn_proxy")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var id int64
	if err := s.QueryRow(ctx, `SELECT id FROM sources WHERE adapter='xslist'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	str := func(v string) *string { return &v }
	base := CrawlPolicy{SourceID: id, DiscoveryInterval: "1 day", MinRequestInterval: "10 seconds", DailyPageBudget: 100}

	p := base
	p.ProxyURL = str(" env:CATALOG_PROXY_VPN ")
	if err := s.UpsertCrawlPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	rp, err := s.FetchPolicy(ctx, id, catalog.PhaseIncremental)
	if err != nil || rp.ProxyURL != "env:CATALOG_PROXY_VPN" {
		t.Fatalf("FetchPolicy proxy %q %v", rp.ProxyURL, err)
	}
	// An older client (the admin form before proxy_url) keeps the value.
	p = base
	p.DailyPageBudget = 80
	if err := s.UpsertCrawlPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	pols, err := s.CrawlPolicies(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range pols {
		if x.SourceID == id && (x.ProxyURL == nil || *x.ProxyURL != "env:CATALOG_PROXY_VPN" || x.DailyPageBudget != 80) {
			t.Fatalf("policy %+v proxy %v", x, x.ProxyURL)
		}
	}
	// Credentials are refused before they reach the database...
	p.ProxyURL = str("socks5://vpnuser:secret@10.8.0.1:1080")
	var inv *ValidationError
	if err := s.UpsertCrawlPolicy(ctx, p); err == nil || !errors.As(err, &inv) {
		t.Fatalf("credentials accepted: %v", err)
	}
	// ...and by the CHECK for anything that bypasses the API.
	if _, err := s.Exec(ctx, `UPDATE source_crawl_policies SET proxy_url='http://u:p@10.8.0.1:3128' WHERE source_id=$1`, id); err == nil {
		t.Fatal("database stored proxy credentials")
	}
	// "" clears it (inherit the global egress).
	p.ProxyURL = str("")
	if err := s.UpsertCrawlPolicy(ctx, p); err != nil {
		t.Fatal(err)
	}
	if rp, _ = s.FetchPolicy(ctx, id, catalog.PhaseIncremental); rp.ProxyURL != "" {
		t.Fatalf("proxy not cleared: %q", rp.ProxyURL)
	}
}

// TestVPNSourceFieldPolicyIntegration: XSList profile data and JAVLibrary
// release data land as evidence; JAVLibrary never overrides a structured
// source's title or date, its cast creates no performers, and its rating
// opens no conflict.
func TestVPNSourceFieldPolicyIntegration(t *testing.T) {
	openIsolatedTestDB(t, "vpn_policy")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var xsID, jlID, jdbID int64
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE adapter='xslist'`).Scan(&xsID))
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE adapter='javlibrary'`).Scan(&jlID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('vp-jdb','https://vp-jdb.test/','javdatabase','structured_database') RETURNING id`).Scan(&jdbID))

	parse := func(a catalog.Adapter, file, u string, entity catalog.EntityType) catalog.Record {
		t.Helper()
		body, err := os.ReadFile("../catalog/testdata/" + file)
		must(err)
		rec, err := a.Parse(ctx, catalog.Document{URL: u, FinalURL: u, Status: 200, Body: body}, entity)
		must(err)
		return rec
	}
	xs := parse(catalog.NewXSListAdapter(), "xslist/performers/model_90001.html", "https://xslist.org/en/model/90001.html", catalog.Performer)
	jl := parse(catalog.NewJAVLibraryAdapter(), "javlibrary/releases/javlizzzz1.html", "https://www.javlibrary.com/en/?v=javlizzzz1", catalog.Release)
	jdb := catalog.Record{EntityType: catalog.Release, SourceKey: "zzz-001", URL: "https://vp-jdb.test/movies/zzz-001/",
		Fields: map[string]any{"title": "Database Title", "dvd_code": "ZZZ-001", "release_date": "2024-05-31", "maker": "Synthetic Maker"}}

	var performersBefore int64
	must(s.QueryRow(ctx, `SELECT count(*) FROM performers`).Scan(&performersBefore))
	for _, step := range []struct {
		src int64
		rec catalog.Record
	}{{jlID, jl}, {jdbID, jdb}, {jlID, jl}, {xsID, xs}} {
		if _, err := s.SaveCatalogRecord(ctx, step.src, "test", step.rec); err != nil {
			t.Fatal(err)
		}
	}
	var title, date string
	must(s.QueryRow(ctx, `SELECT canonical_title,release_date::text FROM releases WHERE slug='zzz-001'`).Scan(&title, &date))
	if title != "Database Title" || date != "2024-05-31" {
		t.Fatalf("javlibrary overrode the structured source: title=%q date=%s", title, date)
	}
	var credits, ratingConflicts int64
	must(s.QueryRow(ctx, `SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE r.slug='zzz-001'`).Scan(&credits))
	must(s.QueryRow(ctx, `SELECT count(*) FROM field_conflicts WHERE field_name LIKE '%rating%'`).Scan(&ratingConflicts))
	if credits != 0 || ratingConflicts != 0 {
		t.Fatalf("javlibrary cast/rating leaked: credits=%d rating conflicts=%d", credits, ratingConflicts)
	}
	var rating string
	must(s.QueryRow(ctx, `SELECT v.normalized_value#>>'{}' FROM source_field_values v JOIN source_records r ON r.id=v.source_record_id
		WHERE r.source_id=$1 AND v.field_name='javlibrary_user_rating' AND v.is_current`, jlID).Scan(&rating))
	if rating != "8.30" {
		t.Fatalf("rating evidence %q", rating)
	}
	var name, birth string
	var height float64
	must(s.QueryRow(ctx, `SELECT p.canonical_name,p.birth_date::text,p.height_cm FROM performers p JOIN source_records r ON r.performer_id=p.id WHERE r.source_id=$1`, xsID).Scan(&name, &birth, &height))
	if name != "Hanako Gousei" || birth != "2000-04-01" || height != 158 {
		t.Fatalf("xslist performer %s %s %v", name, birth, height)
	}
	var performersAfter int64
	must(s.QueryRow(ctx, `SELECT count(*) FROM performers`).Scan(&performersAfter))
	if performersAfter != performersBefore+1 {
		t.Fatalf("performers %d -> %d: only the XSList profile may create one", performersBefore, performersAfter)
	}
}
