package store

import (
	"avxxx/internal/catalog"
	"context"
	"testing"
)

// TestDirectorSpilloverCleanupIntegration seeds directors stored by the
// pre-#182 javdatabase parser (the next label row taken as the value) and
// checks that migration 056, re-run on start, clears exactly those, leaves
// real names and admin overrides alone, is idempotent, and lets the next
// save under the field policy refill the column from a real value.
func TestDirectorSpilloverCleanupIntegration(t *testing.T) {
	openIsolatedTestDB(t, "director_cleanup")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var jdb, agg int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('cleanup-jdb','https://cleanup-jdb.test/','javdatabase','structured_database') RETURNING id`).Scan(&jdb))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('cleanup-agg','https://cleanup-agg.test/','javlibrary','aggregator') RETURNING id`).Scan(&agg))
	directors := map[string]string{
		"SNOS-397": "Genre(s): 4K Exclusive Distribution Kiss Kiss",
		"CLN-002":  "Studio: S1 NO.1 STYLE",
		"CLN-003":  "Label: S1",
		"CLN-004":  "Series: Something",
		"CLN-005":  "Release Date: 2024-06-01",
		"CLN-006":  "Runtime: 120 min",
		"CLN-007":  "Studio Ghibli Fan", // a real name: no label colon
		"CLN-008":  "Kazuhiro Label",    // a real name
		"CLN-009":  "Genre(s): 4K",      // overridden below: kept
	}
	ids := map[string]int64{}
	for code, dir := range directors {
		if _, err := s.SaveCatalogRecord(ctx, jdb, "javdatabase-old", catalog.Record{EntityType: catalog.Release, SourceKey: code, URL: "https://cleanup-jdb.test/" + code,
			Fields: map[string]any{"title": code + " Title", "dvd_code": code, "director": dir}}); err != nil {
			t.Fatal(err)
		}
		var id int64
		must(s.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_id=$1 AND source_record_key=$2`, jdb, code).Scan(&id))
		ids[code] = id
	}
	_, err = s.Exec(ctx, `INSERT INTO release_overrides(release_id,field_name,value,reason,actor) VALUES($1,'director_name','"Genre(s): 4K"','test','admin')`, ids["CLN-009"])
	must(err)

	director := func(code string) string {
		t.Helper()
		var v *string
		must(s.QueryRow(ctx, `SELECT director_name FROM releases WHERE id=$1`, ids[code]).Scan(&v))
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	check := func(round string) {
		t.Helper()
		for code, dir := range directors {
			want := "<nil>"
			switch code {
			case "CLN-007", "CLN-008", "CLN-009":
				want = dir
			}
			if got := director(code); got != want {
				t.Errorf("%s: %s director = %q, want %q", round, code, got, want)
			}
		}
		var current int
		must(s.QueryRow(ctx, `SELECT count(*) FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id
			WHERE sr.source_id=$1 AND sf.field_name='director' AND sf.is_current`, jdb).Scan(&current))
		if current != 2 { // only the two real names stay current evidence
			t.Errorf("%s: %d current director evidence rows, want 2", round, current)
		}
	}
	// The migration runs on every start: reopen twice (cleanup, then no-op).
	for _, round := range []string{"first start", "second start"} {
		s.Close()
		if s, err = Open(ctx); err != nil {
			t.Fatal(err)
		}
		check(round)
	}

	// The next save under the field policy refills from a real value: the
	// retired spill-over (a higher tier) no longer competes.
	if _, err := s.SaveCatalogRecord(ctx, agg, "javlibrary", catalog.Record{EntityType: catalog.Release, SourceKey: "lib-snos-397", URL: "https://cleanup-agg.test/snos-397",
		Fields: map[string]any{"title": "SNOS-397 Title", "dvd_code": "SNOS-397", "director": "Real Director"}}); err != nil {
		t.Fatal(err)
	}
	if got := director("SNOS-397"); got != "Real Director" {
		t.Fatalf("refill after cleanup = %q", got)
	}
	// The fixed parser's re-crawl (empty director) keeps it.
	if _, err := s.SaveCatalogRecord(ctx, jdb, "javdatabase", catalog.Record{EntityType: catalog.Release, SourceKey: "SNOS-397", URL: "https://cleanup-jdb.test/SNOS-397",
		Fields: map[string]any{"title": "SNOS-397 Title", "dvd_code": "SNOS-397", "director": ""}}); err != nil {
		t.Fatal(err)
	}
	if got := director("SNOS-397"); got != "Real Director" {
		t.Fatalf("after fixed re-crawl = %q", got)
	}
}
