package store

import (
	"avxxx/internal/catalog"
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestDirectorFieldPolicyIntegration checks that director_name (and label,
// series, runtime) follow the field policy on every save, not only when the
// release row is first inserted: a re-crawl with a corrected value replaces a
// parser spill-over, source_field_policies and tiers pick the winner, an empty
// report keeps the value, and an admin override is never replaced.
// Skipped unless AVXXX_TEST_DATABASE_URL is set.
func TestDirectorFieldPolicyIntegration(t *testing.T) {
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
	sfx := fmt.Sprint(time.Now().UnixNano())
	var dbID, aggID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('director-db-'||$1,'https://director-db.test/'||$1,'javdatabase','structured_database') RETURNING id`, sfx).Scan(&dbID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('director-agg-'||$1,'https://director-agg.test/'||$1,'javlibrary','aggregator') RETURNING id`, sfx).Scan(&aggID))
	code := "DIRP-" + sfx[len(sfx)-6:]
	rec := func(key string, fields map[string]any) catalog.Record {
		f := map[string]any{"title": code + " Title", "dvd_code": code, "maker": "Maker " + sfx}
		for k, v := range fields {
			f[k] = v
		}
		return catalog.Record{EntityType: catalog.Release, SourceKey: key + sfx, URL: "https://director.test/" + key + sfx, Fields: f}
	}
	save := func(src int64, r catalog.Record) {
		t.Helper()
		if _, err := s.SaveCatalogRecord(ctx, src, "test", r); err != nil {
			t.Fatal(err)
		}
	}
	var relID int64
	col := func(c string) string {
		t.Helper()
		var v *string
		must(s.QueryRow(ctx, `SELECT `+c+`::text FROM releases WHERE id=$1`, relID).Scan(&v))
		if v == nil {
			return "<nil>"
		}
		return *v
	}

	// The old parser stored the next row as the director.
	spill := "Genre(s): 4K Exclusive Distribution"
	save(dbID, rec("db-", map[string]any{"director": spill, "label": "Label Old", "series": "Series Old", "runtime_minutes": 120}))
	must(s.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_id=$1 AND source_record_key=$2`, dbID, "db-"+sfx).Scan(&relID))
	if got := col("director_name"); got != spill {
		t.Fatalf("director after first save = %q", got)
	}

	// A re-crawl with a corrected value replaces it (not insert-only).
	save(dbID, rec("db-", map[string]any{"director": "Real Director", "label": "Label New", "series": "Series New", "runtime_minutes": 125}))
	if got := col("director_name") + "|" + col("label_name") + "|" + col("series_name") + "|" + col("runtime_minutes"); got != "Real Director|Label New|Series New|125" {
		t.Fatalf("after corrected re-crawl = %q", got)
	}

	// A lower-tier source never takes it; a higher-tier one does.
	save(aggID, rec("agg-", map[string]any{"director": "Aggregator Director"}))
	if got := col("director_name"); got != "Real Director" {
		t.Fatalf("aggregator took director: %q", got)
	}
	_, err = s.Exec(ctx, `INSERT INTO source_field_policies(entity_type,field_name,source_id,priority) VALUES('release','director_name',$1,5)`, aggID)
	must(err)
	save(dbID, rec("db-", map[string]any{"director": "Real Director"}))
	if got := col("director_name"); got != "Aggregator Director" {
		t.Fatalf("policy row ignored: %q", got)
	}
	_, err = s.Exec(ctx, `DELETE FROM source_field_policies WHERE source_id=$1`, aggID)
	must(err)

	save(dbID, rec("db-", map[string]any{"director": "Real Director"}))
	if got := col("director_name"); got != "Real Director" {
		t.Fatalf("director after the policy row was dropped = %q", got)
	}

	// Sources that now report no value (an empty row, or a degraded page)
	// never blank the column: the value stays until a source supplies a new
	// one. Spill-over values already stored are cleared by migration 056.
	save(aggID, rec("agg-", map[string]any{"director": ""}))
	save(dbID, rec("db-", map[string]any{"director": "", "label": ""}))
	if got := col("director_name") + "|" + col("label_name"); got != "Real Director|Label New" {
		t.Fatalf("empty re-crawl changed director/label: %q", got)
	}

	// An admin override is never replaced, by a value or by an empty report.
	// (Drop the earlier aggregator disagreement so only new conflicts count.)
	_, err = s.Exec(ctx, `DELETE FROM field_conflicts WHERE entity_type='release' AND entity_id=$1`, relID)
	must(err)
	_, err = s.Exec(ctx, `INSERT INTO release_overrides(release_id,field_name,value,reason,actor) VALUES($1,'director_name','"Admin Director"','test','admin')`, relID)
	must(err)
	_, err = s.Exec(ctx, `UPDATE releases SET director_name='Admin Director' WHERE id=$1`, relID)
	must(err)
	save(dbID, rec("db-", map[string]any{"director": "Crawled Director"}))
	if got := col("director_name"); got != "Admin Director" {
		t.Fatalf("override replaced by re-crawl: %q", got)
	}
	save(dbID, rec("db-", map[string]any{"director": ""}))
	if got := col("director_name"); got != "Admin Director" {
		t.Fatalf("override cleared by empty re-crawl: %q", got)
	}
	var conflicts int
	must(s.QueryRow(ctx, `SELECT count(*) FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name='director' AND status='pending'`, relID).Scan(&conflicts))
	if conflicts != 0 {
		t.Fatalf("overridden director opened %d conflicts", conflicts)
	}

	// A new release with an empty director stores NULL, not ''.
	code2 := code + "B"
	save(dbID, catalog.Record{EntityType: catalog.Release, SourceKey: "db2-" + sfx, URL: "https://director.test/db2-" + sfx,
		Fields: map[string]any{"title": code2 + " Title", "dvd_code": code2, "director": ""}})
	var dir *string
	must(s.QueryRow(ctx, `SELECT r.director_name FROM releases r JOIN source_records sr ON sr.release_id=r.id WHERE sr.source_id=$1 AND sr.source_record_key=$2`, dbID, "db2-"+sfx).Scan(&dir))
	if dir != nil {
		t.Fatalf("empty director stored as %q", *dir)
	}
}
