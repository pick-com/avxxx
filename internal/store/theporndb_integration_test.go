package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/crosscheck"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestThePornDBCrossCheckIntegration checks migration 032 (disabled
// third_party_api source behind the license gate, conservative policy,
// idempotent), the fallback rules of SaveCatalogRecord for a
// third_party_api source (attach only, fill missing fields, never override a
// higher tier, never open a conflict alone, credits only for uncredited
// releases and existing performers, ThePornDB performer ids) and one
// cross-check run end to end against a fake API. It runs in its own database
// (openIsolatedTestDB): it asserts on the migration-seeded ThePornDB source,
// enables it, and records ThePornDB performer ids and run logs, none of which
// may leak into, or be polluted by, other integration tests. Skipped unless
// AVXXX_TEST_DATABASE_URL is set.
func TestThePornDBCrossCheckIntegration(t *testing.T) {
	openIsolatedTestDB(t, "tpdb")
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
	// The code number must not start with a zero: ThePornDBCode canonicalizes
	// "TPB-01234" to "TPB-1234", whose slug is not the release's, and the
	// runner then reports the match as ambiguous by design. (The old
	// sfx[len(sfx)-5:] was one of "00000".."99000" on macOS, whose clock has
	// microsecond resolution, so one run in ten failed with Ambiguous:2.)
	const num = "51234"

	// ---- Seed: one disabled third_party_api source behind the license gate.
	var tpdbID int64
	var enabled bool
	var kind, license, terms, credit string
	var reviewed *time.Time
	var minSec, budget int
	must(s.QueryRow(ctx, `SELECT s.id,s.enabled,s.source_kind,s.license_status,COALESCE(s.terms_url,''),s.terms_reviewed_at,COALESCE(s.attribution_html,''),
		EXTRACT(EPOCH FROM p.min_request_interval)::int,p.daily_page_budget
		FROM sources s JOIN source_crawl_policies p ON p.source_id=s.id WHERE s.adapter='theporndb'`).
		Scan(&tpdbID, &enabled, &kind, &license, &terms, &reviewed, &credit, &minSec, &budget))
	if enabled || kind != "third_party_api" || license != catalog.LicenseOfficialAPI || terms != catalog.ThePornDBDocsURL || reviewed != nil ||
		credit != catalog.ThePornDBCreditHTML || minSec != 2 || budget != 1000 {
		t.Fatalf("seed: enabled=%v kind=%s license=%s terms=%s reviewed=%v credit=%q min=%d budget=%d", enabled, kind, license, terms, reviewed, credit, minSec, budget)
	}
	if text(`SELECT count(*)::text FROM sources WHERE adapter='theporndb'`) != "1" || text(`SELECT url FROM sources WHERE id=$1`, tpdbID) != catalog.ThePornDBJAVListURL {
		t.Fatal("seed is not one row with the adapter's start URL")
	}
	src, found, err := s.CrossCheckSource(ctx, catalog.ThePornDBAdapterName)
	must(err)
	if !found || src.ID != tpdbID || src.Enabled {
		t.Fatalf("CrossCheckSource = %+v %v", src, found)
	}
	if d := catalog.Gate(src.Governance, catalog.PhaseIncremental, 0); d.Allowed || d.Code != catalog.CodeLicenseGate {
		t.Fatalf("unreviewed source passes the gate: %+v", d)
	}

	// ---- Higher tiers: a DMM-like official record and a structured database.
	var offID, dbID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('tpdb-off','https://tpdb-off.test/'||$1,'dmm','official_catalog') RETURNING id`, sfx).Scan(&offID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('tpdb-db','https://tpdb-db.test/'||$1,'javdatabase','structured_database') RETURNING id`, sfx).Scan(&dbID))
	codeA, codeB, codeC := "TPA-"+num, "TPB-"+num, "TPC-"+num
	actress := "Tpdb Actress " + sfx
	save := func(src int64, parser string, rec catalog.Record) int64 {
		t.Helper()
		id, err := s.SaveCatalogRecord(ctx, src, parser, rec)
		must(err)
		return id
	}
	// A: official record with everything; B: database record missing date,
	// maker and performers; C: database record credited already.
	save(offID, "dmm", catalog.Record{EntityType: catalog.Release, SourceKey: "a" + sfx, URL: "https://tpdb-off.test/a" + sfx,
		Fields: map[string]any{"title": "Official A", "dvd_code": codeA, "release_date": "2024-04-23", "maker": "Official Maker", "cover_url": "https://img.test/a.jpg",
			"performers": []string{"Official Credit " + sfx}}})
	save(dbID, "javdatabase", catalog.Record{EntityType: catalog.Release, SourceKey: "b" + sfx, URL: "https://tpdb-db.test/b" + sfx,
		Fields: map[string]any{"title": "Database B", "dvd_code": codeB}})
	save(dbID, "javdatabase", catalog.Record{EntityType: catalog.Performer, SourceKey: "p" + sfx, URL: "https://tpdb-db.test/p" + sfx,
		Fields: map[string]any{"name": actress}})
	save(dbID, "javdatabase", catalog.Record{EntityType: catalog.Release, SourceKey: "c" + sfx, URL: "https://tpdb-db.test/c" + sfx,
		Fields: map[string]any{"title": "Database C", "dvd_code": codeC, "performers": []string{"Other Credit " + sfx}}})
	releaseID := func(code string) int64 {
		var id int64
		must(s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, catalog.ReleaseSlug(code, "", "", "")).Scan(&id))
		return id
	}
	idA, idB, idC := releaseID(codeA), releaseID(codeB), releaseID(codeC)

	// Due for a cross-check: B (missing date/maker/performers/cover), C
	// (missing date/maker/cover); A has everything and no conflict.
	due, err := s.DueCrossChecks(ctx, tpdbID, time.Now(), 100000, nil)
	must(err)
	reasons := map[int64][]string{}
	for _, c := range due {
		reasons[c.ReleaseID] = c.Reasons
		if c.ReleaseID == idB && (len(c.Codes) != 1 || c.Codes[0] != codeB || c.Slug != catalog.ReleaseSlug(codeB, "", "", "")) {
			t.Fatalf("candidate B = %+v", c)
		}
	}
	if _, ok := reasons[idA]; ok {
		t.Fatalf("complete release A selected: %v", reasons[idA])
	}
	if r := strings.Join(reasons[idB], ","); r != "missing_release_date,missing_maker,missing_performers,missing_cover" {
		t.Fatalf("B reasons = %q", r)
	}
	if r := strings.Join(reasons[idC], ","); r != "missing_release_date,missing_maker,missing_cover" {
		t.Fatalf("C reasons = %q", r)
	}

	// ---- One cross-check run against a fake ThePornDB API.
	const token = "integTPDBtoken55"
	actressID := "7e8d5654-2ca4-46de-ac1e-8683f9e7f778"
	scene := func(id, code, maker, date string, performers string) string {
		return fmt.Sprintf(`{"id":%q,"title":"%s English Title","sku":%q,"date":%q,"image":"https://cdn.theporndb.net/%s.jpg","site":{"name":%q,"id":900010},"performers":[%s]}`,
			id, code, code, date, code, maker, performers)
	}
	perf := func(id, name string) string {
		return fmt.Sprintf(`{"id":%q,"name":%q,"extras":{"gender":"Female"}}`, id, name)
	}
	bodies := map[string]string{
		codeA: scene("11111111-1111-4111-8111-1111111111"+num[:2], codeA, "Third Party Maker", "2024-04-20", ""),
		codeB: scene("22222222-2222-4222-8222-2222222222"+num[:2], codeB, "S1 NO.1 STYLE", "2024-05-01",
			perf(actressID, actress)+","+perf("0f6b8c3e-1d2a-4b5c-9e7f-a1b2c3d4e5f6", "Unknown Actress "+sfx)),
		codeC: scene("33333333-3333-4333-8333-3333333333"+num[:2], codeC, "Maker C", "2024-06-01", perf(actressID, actress)),
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		body, ok := bodies[r.URL.Query().Get("q")]
		if !ok {
			body = ""
		}
		fmt.Fprintf(w, `{"data":[%s],"meta":{"total":1,"per_page":10,"current_page":1,"last":1}}`, body)
	}))
	defer api.Close()
	u, _ := url.Parse(api.URL)
	runner := &crosscheck.Runner{Store: s,
		Adapters:   []catalog.LookupAdapter{catalog.NewThePornDBAdapterWith(token, u.Host)},
		HTTPClient: api.Client(), UserAgent: "avxxx-integration (+ops@example.test)"}
	// Still disabled and unreviewed: nothing happens.
	rep, err := runner.Run(ctx)
	must(err)
	if rep.Adapters[0].Stopped != "disabled" || rep.Adapters[0].Checked != 0 {
		t.Fatalf("disabled run = %+v", rep.Adapters[0])
	}
	_, err = s.Exec(ctx, `UPDATE sources SET enabled=true WHERE id=$1`, tpdbID)
	must(err)
	if rep, err = runner.Run(ctx); err != nil || rep.Adapters[0].Stopped != "license_gate" {
		t.Fatalf("unreviewed run = %+v %v", rep.Adapters[0], err)
	}
	_, err = s.Exec(ctx, `UPDATE sources SET terms_reviewed_at=now(),terms_reviewed_by='test' WHERE id=$1`, tpdbID)
	must(err)
	_, err = s.Exec(ctx, `UPDATE source_crawl_policies SET min_request_interval=interval '0' WHERE source_id=$1`, tpdbID)
	must(err)
	rep, err = runner.Run(ctx)
	must(err)
	if ar := rep.Adapters[0]; ar.Checked != 2 || ar.Found != 2 || ar.Requests != 2 || ar.Stopped != "" {
		t.Fatalf("run = %+v", ar)
	}
	for _, id := range []int64{idB, idC} {
		a, err := s.CrossCheckAttempt(ctx, id, tpdbID)
		must(err)
		if a == nil || a.Status != crosscheck.StatusFound || a.NextTryAt != nil || !strings.HasPrefix(a.MatchedKey, "tpdb-jav-") || len(a.Reasons) == 0 {
			t.Fatalf("attempt %d = %+v", id, a)
		}
	}
	if n := text(`SELECT count(*)::text FROM crosscheck_attempts WHERE source_id=$1 AND release_id=$2`, tpdbID, idA); n != "0" {
		t.Fatalf("complete release A looked up (%s attempts)", n)
	}

	// B: missing fields filled by the fallback; the database title stands
	// and no conflict opens for the disagreeing English title.
	if got := text(`SELECT canonical_title||'|'||COALESCE(release_date::text,'')||'|'||COALESCE(maker_name,'') FROM releases WHERE id=$1`, idB); got != "Database B|2024-05-01|S1 NO.1 STYLE" {
		t.Fatalf("B canonical = %q", got)
	}
	if n := text(`SELECT count(*)::text FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND status='pending'`, idB); n != "0" {
		t.Fatalf("B conflicts = %s", n)
	}
	// Uncredited B: credited with the existing performer only, whose
	// ThePornDB id is now recorded; the unknown performer is not created.
	if got := text(`SELECT string_agg(p.canonical_name, ',') FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE rp.release_id=$1`, idB); got != actress {
		t.Fatalf("B credits = %q", got)
	}
	if n := text(`SELECT count(*)::text FROM performers WHERE canonical_name=$1`, "Unknown Actress "+sfx); n != "0" {
		t.Fatal("fallback source created a performer")
	}
	if got := text(`SELECT p.canonical_name FROM performer_external_ids e JOIN performers p ON p.id=e.performer_id WHERE e.namespace='theporndb_performer' AND e.external_id=$1`, actressID); got != actress {
		t.Fatalf("theporndb id -> %q", got)
	}
	// C was credited by another source: no ThePornDB credits added.
	if got := text(`SELECT string_agg(p.canonical_name, ',') FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE rp.release_id=$1`, idC); got != "Other Credit "+sfx {
		t.Fatalf("C credits = %q", got)
	}
	// Second run: B and C have records now, nothing is due.
	rep, err = runner.Run(ctx)
	must(err)
	if rep.Adapters[0].Checked != 0 {
		t.Fatalf("second run checked %d", rep.Adapters[0].Checked)
	}

	// ---- A directly: the fallback never overrides the official tier and
	// opens no conflict on its own.
	recs, err := catalog.NewThePornDBAdapterWith(token, "").Discover(ctx, catalog.Document{URL: "https://api.theporndb.net/jav", Body: []byte(`{"data":[` + bodies[codeA] + `]}`)})
	must(err)
	save(tpdbID, "theporndb", recs[0])
	if got := text(`SELECT canonical_title||'|'||release_date::text||'|'||maker_name FROM releases WHERE id=$1`, idA); got != "Official A|2024-04-23|Official Maker" {
		t.Fatalf("A canonical = %q, want the official values", got)
	}
	if n := text(`SELECT count(*)::text FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND status='pending'`, idA); n != "0" {
		t.Fatalf("A conflicts = %s", n)
	}
	// ...but joins a conflict the database opens, as a ranked candidate.
	save(dbID, "javdatabase", catalog.Record{EntityType: catalog.Release, SourceKey: "a2" + sfx, URL: "https://tpdb-db.test/a2" + sfx,
		Fields: map[string]any{"title": "Fan A", "dvd_code": codeA, "maker": "Fan Maker"}})
	cands := text(`SELECT candidates::text FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name='maker' AND status='pending'`, idA)
	if !strings.Contains(cands, "third_party_api") || !strings.Contains(cands, "Fan Maker") {
		t.Fatalf("maker conflict candidates = %s", cands)
	}

	// ---- No match: evidence only, no release created.
	orphan := strings.Replace(bodies[codeA], codeA, "TPZ-"+num, -1)
	orphan = strings.Replace(orphan, "11111111-1111-4111-8111-1111111111", "44444444-4444-4444-8444-4444444444", 1)
	recs, err = catalog.NewThePornDBAdapterWith(token, "").Discover(ctx, catalog.Document{URL: "https://api.theporndb.net/jav", Body: []byte(`{"data":[` + orphan + `]}`)})
	must(err)
	recID := save(tpdbID, "theporndb", recs[0])
	if got := text(`SELECT resolution_status||'|'||COALESCE(release_id::text,'-') FROM source_records WHERE id=$1`, recID); got != "unresolved|-" {
		t.Fatalf("orphan record = %q", got)
	}
	if n := text(`SELECT count(*)::text FROM releases WHERE slug=$1`, catalog.ReleaseSlug("TPZ-"+num, "", "", "")); n != "0" {
		t.Fatal("fallback source created a release")
	}
	if n := text(`SELECT count(*)::text FROM source_field_values WHERE source_record_id=$1 AND is_current`, recID); n == "0" {
		t.Fatal("orphan evidence not stored")
	}
	// A performer record from the fallback attaches to the existing
	// performer (by ThePornDB id) and never creates one.
	save(tpdbID, "theporndb", catalog.Record{EntityType: catalog.Performer, SourceKey: "tpdb-performer-" + actressID, URL: "https://api.theporndb.net/performers/" + actressID,
		Fields: map[string]any{"name": "Different Display " + sfx, "theporndb_performer_id": actressID, "birth_date": "1999-02-03"}})
	if got := text(`SELECT COALESCE(birth_date::text,'') FROM performers WHERE canonical_name=$1`, actress); got != "1999-02-03" {
		t.Fatalf("fallback birth_date on uncontested performer = %q", got)
	}
	if n := text(`SELECT count(*)::text FROM performers WHERE canonical_name=$1`, "Different Display "+sfx); n != "0" {
		t.Fatal("fallback performer record created a performer")
	}
	// No token anywhere in what was stored.
	stored := text(`SELECT string_agg(sr.raw_data::text, ' ') FROM source_records sr WHERE sr.source_id=$1`, tpdbID) +
		text(`SELECT COALESCE(string_agg(COALESCE(last_error,'')||candidates::text, ' '),'') FROM crosscheck_attempts WHERE source_id=$1`, tpdbID) +
		text(`SELECT COALESCE(string_agg(detail::text, ' '),'') FROM nightly_runs WHERE job='crosscheck.theporndb'`)
	if strings.Contains(stored, token) {
		t.Fatal("token stored")
	}
	if !slices.Contains([]string{"ok", "issues"}, text(`SELECT result FROM nightly_runs WHERE job='crosscheck.theporndb' ORDER BY id DESC LIMIT 1`)) {
		t.Fatal("run not recorded")
	}
}
