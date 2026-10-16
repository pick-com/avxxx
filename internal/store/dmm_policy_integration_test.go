package store

import (
	"avxxx/internal/catalog"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestDMMFieldPolicyIntegration checks how DMM (official_catalog, tier 10)
// combines with the field priority policy, #61 admin overrides and #70's
// hidden birthdays and page_title evidence, and that the seeded DMM sources
// stay behind the license gate until an admin records a terms review.
// Skipped unless AVXXX_TEST_DATABASE_URL is set.
func TestDMMFieldPolicyIntegration(t *testing.T) {
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	text := func(q string, args ...any) string {
		t.Helper()
		var v *string
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	sfx := fmt.Sprint(time.Now().UnixNano())

	// ---- Seeded DMM sources: official_api, terms URL, no review, disabled.
	var dmmID int64
	var termsURL string
	var reviewed *time.Time
	var enabled bool
	must(s.QueryRow(ctx, `SELECT id,COALESCE(terms_url,''),terms_reviewed_at,enabled FROM sources WHERE url=$1`, catalog.DMMItemListURL).Scan(&dmmID, &termsURL, &reviewed, &enabled))
	if termsURL == "" || reviewed != nil || enabled {
		t.Fatalf("dmm seed: terms_url=%q reviewed=%v enabled=%v", termsURL, reviewed, enabled)
	}
	for _, phase := range []catalog.Phase{catalog.PhaseIncremental, catalog.PhaseBaseline} {
		d := catalog.Gate(catalog.SourceGovernance{Adapter: "dmm", LicenseStatus: catalog.LicenseOfficialAPI, TermsURL: termsURL}, phase, 0)
		if d.Allowed || d.Code != catalog.CodeLicenseGate {
			t.Fatalf("unreviewed dmm source passes the %s gate: %+v", phase, d)
		}
	}
	now := time.Now()
	if d := catalog.Gate(catalog.SourceGovernance{Adapter: "dmm", LicenseStatus: catalog.LicenseOfficialAPI, TermsURL: termsURL, TermsReviewedAt: &now}, catalog.PhaseIncremental, 0); !d.Allowed {
		t.Fatalf("reviewed official_api source refused: %+v", d)
	}

	// ---- Release identity fields: DMM (tier 10) beats a structured
	// database (tier 30) whichever saves first; an admin override stands.
	var dbID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('dmm-policy-db','https://dmm-policy.test/'||$1,'javdatabase','structured_database') RETURNING id`, sfx).Scan(&dbID))
	code := "DMMP-" + sfx[len(sfx)-6:]
	dbRec := catalog.Record{EntityType: catalog.Release, SourceKey: "db-" + sfx, URL: "https://dmm-policy.test/m/" + sfx,
		Fields: map[string]any{"title": code + " Fan Title", "dvd_code": code, "release_date": "2024-05-01", "maker": "Maker DB"}}
	dmmRec := catalog.Record{EntityType: catalog.Release, SourceKey: "dmmp" + sfx, URL: "https://video.dmm.co.jp/av/content/?id=dmmp" + sfx,
		Fields: map[string]any{"title": "Official Title " + sfx, "dvd_code": code, "release_date": "2024-04-23", "maker": "Maker Official", "content_id": "dmmp" + sfx}}
	if _, err := s.SaveCatalogRecord(ctx, dbID, "javdatabase", dbRec); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveCatalogRecord(ctx, dmmID, "dmm", dmmRec); err != nil {
		t.Fatal(err)
	}
	// The structured database saving again must not take the fields back.
	if _, err := s.SaveCatalogRecord(ctx, dbID, "javdatabase", dbRec); err != nil {
		t.Fatal(err)
	}
	slug := text(`SELECT r.slug FROM releases r JOIN source_records sr ON sr.release_id=r.id WHERE sr.source_id=$1 AND sr.source_record_key=$2`, dmmID, dmmRec.SourceKey)
	if got := text(`SELECT canonical_title||'|'||release_date::text||'|'||maker_name FROM releases WHERE slug=$1`, slug); got != "Official Title "+sfx+"|2024-04-23|Maker Official" {
		t.Fatalf("release canonical fields = %q, want DMM's", got)
	}
	if got := text(`SELECT current_value#>>'{}' FROM field_conflicts WHERE entity_type='release' AND entity_id=(SELECT id FROM releases WHERE slug=$1) AND field_name='title' AND status='pending'`, slug); got != "Official Title "+sfx {
		t.Fatalf("title conflict current_value = %q, want DMM's", got)
	}
	_, err = s.Exec(ctx, `INSERT INTO release_overrides(release_id,field_name,value,reason,actor) SELECT id,'canonical_title','"Admin Title"','test','admin' FROM releases WHERE slug=$1`, slug)
	must(err)
	_, err = s.Exec(ctx, `UPDATE releases SET canonical_title='Admin Title' WHERE slug=$1`, slug)
	must(err)
	if _, err := s.SaveCatalogRecord(ctx, dmmID, "dmm", dmmRec); err != nil {
		t.Fatal(err)
	}
	if got := text(`SELECT canonical_title FROM releases WHERE slug=$1`, slug); got != "Admin Title" {
		t.Fatalf("admin override lost to DMM: %q", got)
	}

	// ---- Performers: DMM's birthday wins the column, but a hidden birthday
	// stays hidden on public reads; page_title never opens a conflict.
	name := "Policy Idol " + sfx
	if _, err := s.SaveCatalogRecord(ctx, dbID, "javdatabase", catalog.Record{EntityType: catalog.Performer, SourceKey: "p-" + sfx, URL: "https://dmm-policy.test/i/" + sfx,
		Fields: map[string]any{"name": name, "birth_date": "1999-01-01", catalog.FieldPageTitle: name + " - Fan DB"}}); err != nil {
		t.Fatal(err)
	}
	pslug := text(`SELECT p.slug FROM performers p JOIN source_records sr ON sr.performer_id=p.id WHERE sr.source_id=$1 AND sr.source_record_key=$2`, dbID, "p-"+sfx)
	_, err = s.Exec(ctx, `UPDATE performers SET birth_date_hidden=true WHERE slug=$1`, pslug)
	must(err)
	if _, err := s.SaveCatalogRecord(ctx, dmmID, "dmm", catalog.Record{EntityType: catalog.Performer, SourceKey: "dmm-actress-" + sfx, URL: "https://actress.dmm.co.jp/-/detail/=/actress_id=" + sfx,
		Fields: map[string]any{"name": name, "birth_date": "1998-02-02", catalog.FieldPageTitle: name + " - FANZA"}}); err != nil {
		t.Fatal(err)
	}
	if got := text(`SELECT birth_date::text FROM performers WHERE slug=$1`, pslug); got != "1998-02-02" {
		t.Fatalf("performer birth_date = %q, want DMM's", got)
	}
	p, err := s.PerformerBySlug(ctx, pslug)
	must(err)
	if p.BirthDate != nil {
		t.Fatalf("hidden birthday public: %v", p.BirthDate)
	}
	detail, err := s.PerformerDetail(ctx, pslug)
	must(err)
	if detail["birth_date"] != nil || detail["birth_date_hidden"] != true {
		t.Fatalf("hidden birthday in detail: %v / %v", detail["birth_date"], detail["birth_date_hidden"])
	}
	if got := text(`SELECT count(*)::text FROM field_conflicts WHERE entity_type='performer' AND entity_id=(SELECT id FROM performers WHERE slug=$1) AND field_name=$2`, pslug, catalog.FieldPageTitle); got != "0" {
		t.Fatalf("page_title conflicts = %s", got)
	}
	if got := text(`SELECT string_agg(sf.raw_value#>>'{}', '|' ORDER BY sf.raw_value#>>'{}') FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id
		JOIN performers p ON p.id=sr.performer_id WHERE p.slug=$1 AND sf.field_name=$2 AND sf.is_current`, pslug, catalog.FieldPageTitle); !strings.Contains(got, "FANZA") || !strings.Contains(got, "Fan DB") {
		t.Fatalf("page_title evidence = %q", got)
	}
}
