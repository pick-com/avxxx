package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/crosscheck"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestThePornDBCodeKeyIntegration checks matching ThePornDB records by code
// key (migration 033, catalog.CodeMatchKey): ThePornDB drops zero padding,
// so a release stored as "TPP-0123" must still get the record that comes
// back as "TPP-123"; two releases whose codes share a key stay ambiguous /
// unresolved; an exact, unique code still attaches as before. Runs in its own
// database (it enables the migration-seeded ThePornDB source). Skipped unless
// AVXXX_TEST_DATABASE_URL is set.
func TestThePornDBCodeKeyIntegration(t *testing.T) {
	openIsolatedTestDB(t, "tpdbkey")
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

	// ---- SQL code_match_key mirrors catalog.CodeMatchKey.
	for _, in := range []string{"HEYZO-0123", "heyzo 123", "TPB-02000", "ABC-001", "ABC-1", "ABC-000", "abc-0123-a", "ABC-0123-DL",
		"300MIUM-0123", "Heyzo", "FC2-PPV-1234567", "ＳＯＮＥ－０１５６", "1234", "-"} {
		if got, want := text(`SELECT code_match_key($1)`, in), catalog.CodeMatchKey(in); got != want {
			t.Errorf("code_match_key(%q) = %q, CodeMatchKey = %q", in, got, want)
		}
	}

	var tpdbID, dbID int64
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE adapter='theporndb'`).Scan(&tpdbID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('key-db','https://key-db.test/','javdatabase','structured_database') RETURNING id`).Scan(&dbID))
	seed := func(key, code string) int64 {
		t.Helper()
		_, err := s.SaveCatalogRecord(ctx, dbID, "javdatabase", catalog.Record{EntityType: catalog.Release, SourceKey: key, URL: "https://key-db.test/" + key,
			Fields: map[string]any{"title": "Title " + code, "dvd_code": code}})
		must(err)
		var id int64
		must(s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, catalog.ReleaseSlug(code, "", "", "")).Scan(&id))
		return id
	}
	// P: zero-padded beyond ThePornDB's form; Q: likewise, 5 digits;
	// X1/X2: different titles whose codes share a key; E: plain exact code.
	idP, idQ := seed("p", "TPP-0123"), seed("q", "TPQ-02000")
	idX1, idX2 := seed("x1", "TPX-001"), seed("x2", "TPX-1")
	idE := seed("e", "TPE-456")
	// W1/W2: share a key, and neither is under the record's code slug.
	idW1, idW2 := seed("w1", "TPW-0099"), seed("w2", "TPW-00099")

	ids, err := s.CrossCheckCodeReleases(ctx, "TPP-123")
	must(err)
	if fmt.Sprint(ids) != fmt.Sprint([]int64{idP}) {
		t.Fatalf("CrossCheckCodeReleases(TPP-123) = %v, want [%d]", ids, idP)
	}
	if ids, err = s.CrossCheckCodeReleases(ctx, "TPX-001"); err != nil || fmt.Sprint(ids) != fmt.Sprint([]int64{idX1, idX2}) {
		t.Fatalf("CrossCheckCodeReleases(TPX-001) = %v %v", ids, err)
	}

	// ---- A cross-check run against a fake ThePornDB API that answers with
	// unpadded codes.
	const token = "integKEYtoken77"
	scene := func(n int, sku string) string {
		return fmt.Sprintf(`{"id":"%08d-1111-4111-8111-111111111111","title":"%s English","sku":%q,"date":"2024-05-01","site":{"name":"Maker %s","id":900010}}`, n, sku, sku, sku)
	}
	bodies := map[string]string{
		"TPP-0123":  scene(1, "TPP-123"),
		"TPQ-02000": scene(2, "TPQ-2000"),
		"TPX-001":   scene(3, "TPX-001"),
		"TPX-1":     scene(4, "TPX-1"),
		"TPE-456":   scene(5, "TPE-456"),
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"data":[%s],"meta":{"total":1,"per_page":10,"current_page":1,"last":1}}`, bodies[r.URL.Query().Get("q")])
	}))
	defer api.Close()
	u, _ := url.Parse(api.URL)
	_, err = s.Exec(ctx, `UPDATE sources SET enabled=true,terms_reviewed_at=now(),terms_reviewed_by='test' WHERE id=$1`, tpdbID)
	must(err)
	_, err = s.Exec(ctx, `UPDATE source_crawl_policies SET min_request_interval=interval '0' WHERE source_id=$1`, tpdbID)
	must(err)
	runner := &crosscheck.Runner{Store: s,
		Adapters:   []catalog.LookupAdapter{catalog.NewThePornDBAdapterWith(token, u.Host)},
		HTTPClient: api.Client(), UserAgent: "avxxx-integration (+ops@example.test)"}
	rep, err := runner.Run(ctx)
	must(err)
	// W1/W2 are due too: no scene for them (not found).
	if ar := rep.Adapters[0]; ar.Checked != 7 || ar.Found != 3 || ar.Ambiguous != 2 || ar.NotFound != 2 {
		t.Fatalf("run = %+v", ar)
	}
	status := func(id int64) string {
		a, err := s.CrossCheckAttempt(ctx, id, tpdbID)
		must(err)
		if a == nil {
			return "<none>"
		}
		return a.Status
	}
	attached := func(id int64) string {
		return text(`SELECT count(*)::text FROM source_records WHERE source_id=$1 AND release_id=$2`, tpdbID, id)
	}
	// Zero-padded releases: found and attached; the unpadded ThePornDB code
	// is not added as a second DVD code; the missing date is filled.
	for _, id := range []int64{idP, idQ} {
		if st, n := status(id), attached(id); st != crosscheck.StatusFound || n != "1" {
			t.Fatalf("padded release %d: status %s, records %s", id, st, n)
		}
		if got := text(`SELECT string_agg(identifier_value, ',') FROM release_identifiers WHERE release_id=$1 AND identifier_type='dvd_code'`, id); strings.Contains(got, ",") {
			t.Fatalf("padded release %d codes = %s", id, got)
		}
		if got := text(`SELECT COALESCE(release_date::text,'') FROM releases WHERE id=$1`, id); got != "2024-05-01" {
			t.Fatalf("padded release %d release_date = %q", id, got)
		}
	}
	// Colliding releases: ambiguous, nothing attached.
	for _, id := range []int64{idX1, idX2} {
		a, err := s.CrossCheckAttempt(ctx, id, tpdbID)
		must(err)
		if a == nil || a.Status != crosscheck.StatusAmbiguous || a.NextTryAt != nil || attached(id) != "0" {
			t.Fatalf("colliding release %d: %+v, records %s", id, a, attached(id))
		}
	}
	// Exact code: unchanged.
	if st, n := status(idE), attached(idE); st != crosscheck.StatusFound || n != "1" {
		t.Fatalf("exact release: status %s, records %s", st, n)
	}

	// ---- SaveCatalogRecord directly (e.g. the discovery crawl).
	saveTPDB := func(n int, sku string) int64 {
		t.Helper()
		recs, err := catalog.NewThePornDBAdapterWith(token, "").Discover(ctx, catalog.Document{URL: "https://api.theporndb.net/jav", Body: []byte(`{"data":[` + scene(n, sku) + `]}`)})
		must(err)
		id, err := s.SaveCatalogRecord(ctx, tpdbID, "theporndb", recs[0])
		must(err)
		return id
	}
	resolved := func(recID int64) string {
		return text(`SELECT resolution_status||'|'||COALESCE(release_id::text,'-') FROM source_records WHERE id=$1`, recID)
	}
	// Shared key, no release under the code slug "tpw-099": unresolved.
	if got := resolved(saveTPDB(6, "TPW-99")); got != "unresolved|-" {
		t.Fatalf("colliding TPW record = %q (W1 %d, W2 %d)", got, idW1, idW2)
	}
	// Shared key, one of them under the code slug: still unresolved.
	if got := resolved(saveTPDB(7, "TPX-1")); got != "unresolved|-" {
		t.Fatalf("colliding TPX record = %q", got)
	}
	// Padded and exact: attached.
	if got := resolved(saveTPDB(8, "TPP-0123")); got != fmt.Sprintf("resolved|%d", idP) {
		t.Fatalf("padded record = %q, want release %d", got, idP)
	}
	if got := resolved(saveTPDB(9, "TPE-456")); got != fmt.Sprintf("resolved|%d", idE) {
		t.Fatalf("exact record = %q, want release %d", got, idE)
	}
	if n := text(`SELECT count(*)::text FROM releases WHERE slug IN ('tpw-099','tpp-123','tpq-2000')`); n != "0" {
		t.Fatal("fallback source created a release")
	}
}
