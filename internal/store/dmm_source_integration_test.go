package store

import (
	"avxxx/internal/catalog"
	"context"
	"os"
	"strings"
	"testing"
)

// TestDMMSourceSeedIntegration checks migration 025 (disabled DMM sources,
// tier, license status, credit text, conservative policy; idempotent on
// re-open) and that mapped DMM records save through SaveCatalogRecord with
// their identifiers and without credentials. Skipped unless
// AVXXX_TEST_DATABASE_URL is set.
func TestDMMSourceSeedIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
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

	rows, err := s.Query(ctx, `SELECT s.id,s.url,s.enabled,s.source_kind,s.license_status,COALESCE(s.attribution_html,''),
EXTRACT(EPOCH FROM p.min_request_interval)::int,p.daily_page_budget
FROM sources s JOIN source_crawl_policies p ON p.source_id=s.id WHERE s.adapter='dmm' ORDER BY s.id`)
	if err != nil {
		t.Fatal(err)
	}
	var itemListID int64
	n := 0
	for rows.Next() {
		var id int64
		var u, kind, license, credit string
		var enabled bool
		var minSec, budget int
		if err := rows.Scan(&id, &u, &enabled, &kind, &license, &credit, &minSec, &budget); err != nil {
			t.Fatal(err)
		}
		n++
		if enabled || kind != "official_catalog" || license != "official_api" || credit != catalog.DMMCreditHTML || minSec != 2 || budget <= 0 {
			t.Errorf("dmm source %s: enabled=%v kind=%s license=%s credit=%q min=%d budget=%d", u, enabled, kind, license, credit, minSec, budget)
		}
		if strings.Contains(u, "api_id") || strings.Contains(u, "affiliate_id") {
			t.Errorf("credential parameter in seeded URL %s", u)
		}
		if u == catalog.DMMItemListURL {
			itemListID = id
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 || itemListID == 0 {
		t.Fatalf("dmm sources = %d (ItemList id %d), want 2 with the adapter's start URL", n, itemListID)
	}

	// A mapped record as the crawl would save it.
	creds := catalog.DMMCredentials{APIID: "INTEGAPIID77", AffiliateID: "integ-991"}
	a := catalog.NewDMMAdapterWith(creds, catalog.DMMAPIHost)
	body, err := os.ReadFile("../catalog/testdata/dmm/itemlist_page1.json")
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.NewReplacer("{{API_ID}}", creds.APIID, "{{AFFILIATE_ID}}", creds.AffiliateID).Replace(string(body)))
	recs, err := a.Discover(ctx, catalog.Document{URL: catalog.DMMItemListURL, Status: 200, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	sink := catalog.RedactingSink{Sink: s, Redactor: a}
	for _, rec := range recs {
		if _, err := sink.SaveCatalogRecord(ctx, itemListID, "dmm", rec); err != nil {
			t.Fatalf("save %s: %v", rec.SourceKey, err)
		}
	}
	var slug, raw string
	if err := s.QueryRow(ctx, `SELECT r.slug, sr.raw_data::text FROM source_records sr JOIN releases r ON r.id=sr.release_id
WHERE sr.source_id=$1 AND sr.source_record_key='sone00156'`, itemListID).Scan(&slug, &raw); err != nil {
		t.Fatal(err)
	}
	if slug != "sone-156" {
		t.Errorf("slug = %q, want the maker_product code", slug)
	}
	var ids []string
	r2, err := s.Query(ctx, `SELECT identifier_type||'='||normalized_value||'@'||identifier_scope FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id WHERE r.slug='sone-156' AND ri.source_id=$1 ORDER BY 1`, itemListID)
	if err != nil {
		t.Fatal(err)
	}
	for r2.Next() {
		var v string
		if err := r2.Scan(&v); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, v)
	}
	if got := strings.Join(ids, ","); !strings.Contains(got, "content_id=sone00156@global") || !strings.Contains(got, "dvd_code=sone-156@maker:") {
		t.Errorf("identifiers = %s", got)
	}
	var stored string
	if err := s.QueryRow(ctx, `SELECT string_agg(sf.raw_value::text, ' ') FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id WHERE sr.source_id=$1`, itemListID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{creds.APIID, creds.AffiliateID} {
		if strings.Contains(raw+stored, secret) {
			t.Fatalf("credential %q stored", secret)
		}
	}
}
