package store

import (
	"avxxx/internal/catalog"
	"context"
	"os"
	"testing"
	"time"
)

// TestMakerSourceSeedIntegration checks migration 034: one disabled
// official_maker source per built-in maker profile, URL = the profile's
// listing_url, not reviewed (the gate allows verify only), a conservative
// crawl policy; idempotent on re-open.
func TestMakerSourceSeedIntegration(t *testing.T) {
	openIsolatedTestDB(t, "maker_seed")
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
	profiles := map[string]catalog.MakerProfile{}
	for _, p := range catalog.BuiltinMakerProfiles() {
		profiles[catalog.MakerAdapterPrefix+p.Slug] = p
	}
	rows, err := s.Query(ctx, `SELECT s.adapter,s.url,s.enabled,s.source_kind,s.license_status,s.terms_reviewed_at,COALESCE(s.terms_url,''),s.priority,
		EXTRACT(EPOCH FROM p.min_request_interval)::int,p.daily_page_budget
		FROM sources s JOIN source_crawl_policies p ON p.source_id=s.id WHERE s.adapter LIKE 'maker:%' ORDER BY s.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var adapter, u, kind, license, terms string
		var enabled bool
		var reviewed *time.Time
		var prio int16
		var minSec, budget int
		if err := rows.Scan(&adapter, &u, &enabled, &kind, &license, &reviewed, &terms, &prio, &minSec, &budget); err != nil {
			t.Fatal(err)
		}
		seen[adapter] = true
		p, ok := profiles[adapter]
		if !ok {
			t.Errorf("seeded source %s has no built-in profile", adapter)
			continue
		}
		if u != p.ListingURL || enabled || kind != catalog.MakerSourceKind || license != catalog.LicenseReviewRequired || reviewed != nil || terms != "" ||
			prio != 20 || minSec < 5 || budget <= 0 {
			t.Errorf("%s: url=%s enabled=%v kind=%s license=%s reviewed=%v terms=%q prio=%d min=%d budget=%d", adapter, u, enabled, kind, license, reviewed, terms, prio, minSec, budget)
		}
		gov := catalog.SourceGovernance{Adapter: adapter, LicenseStatus: license, TermsURL: terms, TermsReviewedAt: reviewed}
		if catalog.Gate(gov, catalog.PhaseBaseline, 0).Allowed || catalog.Gate(gov, catalog.PhaseIncremental, 0).Allowed || !catalog.Gate(gov, catalog.PhaseVerify, 0).Allowed {
			t.Errorf("%s: gate lets an unreviewed maker source crawl automatically", adapter)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != len(profiles) {
		t.Fatalf("seeded maker sources %v, built-in profiles %d", seen, len(profiles))
	}
}

// TestMakerFieldPolicyIntegration: for a title the maker owns, the official
// maker record wins title / release date over JAV Database
// (structured_database) and an aggregator, whatever the save order, on the
// one release the shared DVD code names; the shared maker scope adds no
// second identifier row.
func TestMakerFieldPolicyIntegration(t *testing.T) {
	openIsolatedTestDB(t, "maker_policy")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var makerID, jdbID, aggID int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE adapter='maker:s1'`).Scan(&makerID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('mk-jdb','https://mk-jdb.test/','javdatabase','structured_database') RETURNING id`).Scan(&jdbID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('mk-agg','https://mk-agg.test/','javguru','aggregator') RETURNING id`).Scan(&aggID))

	body, err := os.ReadFile("../catalog/testdata/maker-s1/release_sone00901.html")
	must(err)
	var p catalog.MakerProfile
	for _, b := range catalog.BuiltinMakerProfiles() {
		if b.Slug == "s1" {
			p = b
		}
	}
	a, err := catalog.NewMakerAdapter(p)
	must(err)
	u := "https://s1s1s1.com/works/detail/sone00901"
	official, err := a.Parse(ctx, catalog.Document{URL: u, FinalURL: u, Status: 200, Body: body}, catalog.Release)
	must(err)
	agg := catalog.Record{EntityType: catalog.Release, SourceKey: "sone-901-agg", URL: "https://mk-agg.test/jav/sone-901/",
		Fields: map[string]any{"title": "SONE-901 Aggregator Title", "dvd_code": "sone901", "release_date": "2026-09-25", "maker": "S1 NO.1 STYLE"}}
	jdb := catalog.Record{EntityType: catalog.Release, SourceKey: "sone-901", URL: "https://mk-jdb.test/movies/sone-901/",
		Fields: map[string]any{"title": "SONE-901 Database Title", "dvd_code": "SONE-901", "release_date": "2026-09-18", "maker": "S1 NO.1 STYLE", "runtime_minutes": "119"}}

	save := func(src int64, rec catalog.Record) {
		t.Helper()
		if _, err := s.SaveCatalogRecord(ctx, src, "test", rec); err != nil {
			t.Fatal(err)
		}
	}
	check := func(when string) {
		t.Helper()
		var n int64
		var title, date string
		var runtime *int
		must(s.QueryRow(ctx, `SELECT count(*) FROM releases WHERE slug='sone-901'`).Scan(&n))
		if n != 1 {
			t.Fatalf("%s: %d releases for sone-901", when, n)
		}
		must(s.QueryRow(ctx, `SELECT canonical_title,release_date::text,runtime_minutes FROM releases WHERE slug='sone-901'`).Scan(&title, &date, &runtime))
		if title != official.Fields["title"] || date != "2026-09-19" || runtime == nil || *runtime != 120 {
			t.Fatalf("%s: canonical title=%q date=%s runtime=%v, want the maker's", when, title, date, runtime)
		}
		must(s.QueryRow(ctx, `SELECT count(*) FROM release_identifiers i JOIN releases r ON r.id=i.release_id WHERE r.slug='sone-901' AND i.identifier_type='dvd_code'`).Scan(&n))
		if n != 1 {
			t.Fatalf("%s: %d dvd_code identifiers (maker scopes should coincide)", when, n)
		}
		var scope string
		must(s.QueryRow(ctx, `SELECT i.identifier_scope FROM release_identifiers i JOIN releases r ON r.id=i.release_id WHERE r.slug='sone-901' AND i.identifier_type='dvd_code'`).Scan(&scope))
		if scope != a.MakerScope() {
			t.Fatalf("%s: scope %s, want %s", when, scope, a.MakerScope())
		}
	}
	save(aggID, agg)
	save(jdbID, jdb)
	save(makerID, official)
	check("maker saved last")
	save(jdbID, jdb)
	save(aggID, agg)
	check("aggregators re-saved after the maker")
	var pending int64
	must(s.QueryRow(ctx, `SELECT count(*) FROM field_conflicts c JOIN releases r ON r.id=c.entity_id WHERE c.entity_type='release' AND r.slug='sone-901' AND c.status='pending' AND c.field_name IN ('title','canonical_title')`).Scan(&pending))
	if pending != 1 {
		t.Errorf("title disagreement should stay reviewable (pending conflicts %d)", pending)
	}
}
