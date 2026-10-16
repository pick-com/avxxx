package store

import (
	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestAdminPerformerIntegration covers the AdminActors editor against a
// real, disposable PostgreSQL (make test-integration): junk names from site
// titles and R-PERFORMER-NAME, hidden birthdays in every public read while
// R-AGE keeps the real date, versioned edits with overrides and the R-AGE
// re-check, aliases, hide / unhide, the request state machine and the
// similarity preview seeds. Skipped unless AVXXX_TEST_DATABASE_URL is set;
// assertions are scoped to the rows this test creates, and it never runs
// the whole audit (other packages' integration tests share the database).
func TestAdminPerformerIntegration(t *testing.T) {
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
	s.Close()
	if s, err = Open(ctx); err != nil { // migration 022 must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}
	wantConflict := func(what string, err error) {
		t.Helper()
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Errorf("%s: err %v, want ConflictError", what, err)
		}
	}
	wantValidation := func(what string, err error) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err %v, want ValidationError", what, err)
		}
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	var src int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('perf-a','https://perf-a.test/'||$1) RETURNING id`, sfx).Scan(&src))

	// ---- Ingest: a real performer page and a release whose cast list
	// carries site chrome text.
	name := "Sena Test " + sfx
	junkCast := "JAV Japanese Porn Actress & Idol Database"
	_, err = s.SaveCatalogRecord(ctx, src, "t1", catalog.Record{EntityType: catalog.Performer, SourceKey: "sena-" + sfx, URL: "https://perf-a.test/idols/sena-" + sfx,
		Fields: map[string]any{"name": name, "birth_date": "1995-03-04", catalog.FieldPageTitle: name + " - Perf A Database"}})
	must(err)
	var perfID int64
	must(s.QueryRow(ctx, `SELECT id FROM performers WHERE slug=$1`, catalog.Slug(name)).Scan(&perfID))
	_, err = s.SaveCatalogRecord(ctx, src, "t1", catalog.Record{EntityType: catalog.Release, SourceKey: "rel-" + sfx, URL: "https://perf-a.test/movies/rel-" + sfx,
		Fields: map[string]any{"title": "Perf Release " + sfx, "dvd_code": "PRF-" + sfx[len(sfx)-8:], "release_date": "2020-06-01", "performers": []string{name, junkCast}}})
	must(err)
	var relID int64
	must(s.QueryRow(ctx, `SELECT release_id FROM release_performers WHERE performer_id=$1`, perfID).Scan(&relID))
	if n := count(`SELECT count(*) FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE rp.release_id=$1`, relID); n != 1 {
		t.Errorf("cast = %d performers, want the junk cast name skipped", n)
	}
	if n := count(`SELECT count(*) FROM field_conflicts WHERE entity_type='performer' AND entity_id=$1 AND field_name=$2`, perfID, catalog.FieldPageTitle); n != 0 {
		t.Errorf("page_title opened %d conflicts", n)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`UPDATE releases SET status='published' WHERE id=$1`, relID)

	// ---- R-PERFORMER-NAME: a junk performer from an older parser run.
	var junkID int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$2) RETURNING id`, "jav-db-"+sfx, junkCast).Scan(&junkID))
	exec(`INSERT INTO release_performers(release_id,performer_id,billing_order) VALUES($1,$2,1)`, relID, junkID)
	var facts []audit.PerformerNameFacts
	for after := int64(0); ; {
		page, err := s.PerformerNames(ctx, after, 500)
		must(err)
		for _, p := range page {
			if p.ID == perfID || p.ID == junkID {
				facts = append(facts, p)
			}
			after = p.ID
		}
		if len(page) < 500 {
			break
		}
	}
	if len(facts) != 2 {
		t.Fatalf("PerformerNames facts = %+v", facts)
	}
	for _, f := range facts {
		fs := audit.CheckPerformerName(f)
		switch f.ID {
		case perfID:
			if len(fs) != 0 || len(f.PageTitles) != 1 {
				t.Errorf("real performer flagged: %+v %+v", f, fs)
			}
		case junkID:
			if len(fs) != 1 || f.Releases != 1 {
				t.Fatalf("junk performer: %+v %+v", f, fs)
			}
			_, err = s.OpenIssue(ctx, audit.PerformerNameIssue(f, fs, time.Now()))
			must(err)
		}
	}
	list, _, err := s.AdminPerformers(ctx, AdminPerformerFilter{Q: sfx, Filters: []string{PerformerFilterNameIssue}})
	must(err)
	if len(list) != 1 || list[0].ID != junkID || list[0].OpenIssues != 1 {
		t.Fatalf("name_issue filter: %+v", list)
	}
	// Hide (never delete): public reads drop it, the issue closes.
	_, err = s.SetPerformerStatus(ctx, junkID, PerformerStatusChange{To: PerformerHidden}, "alice")
	wantValidation("hide without reason", err)
	st, err := s.SetPerformerStatus(ctx, junkID, PerformerStatusChange{To: PerformerHidden, Reason: "site title, not a person"}, "alice")
	must(err)
	if st.ResolvedIssues != 1 || st.HiddenReason == nil {
		t.Errorf("hide result %+v", st)
	}
	_, err = s.SetPerformerStatus(ctx, junkID, PerformerStatusChange{To: PerformerHidden, Reason: "again"}, "alice")
	wantConflict("hide twice", err)
	if _, err = s.PerformerBySlug(ctx, "jav-db-"+sfx); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("hidden performer still public: %v", err)
	}
	if n := count(`SELECT count(*) FROM performers WHERE id=$1`, junkID); n != 1 {
		t.Error("hiding deleted the performer")
	}
	page, err := s.PerformerNames(ctx, junkID-1, 1)
	must(err)
	if len(page) == 1 && page[0].ID == junkID {
		t.Error("hidden performer still checked by R-PERFORMER-NAME")
	}

	// ---- Versioned edit + R-AGE re-check: a corrected (younger) birth date
	// holds the published release.
	ap, err := s.AdminPerformer(ctx, perfID)
	must(err)
	if ap.BirthDate == nil || *ap.BirthDate != "1995-03-04" || len(ap.Works) != 1 || ap.Completeness == 100 {
		t.Fatalf("detail %+v", ap)
	}
	patch, err := ParsePerformerPatch(map[string]json.RawMessage{"birth_date": json.RawMessage(`"2004-09-01"`), "version": json.RawMessage(fmt.Sprint(ap.Version)), "reason": json.RawMessage(`"agency profile"`)})
	must(err)
	pr, err := s.PatchPerformer(ctx, perfID, patch, "bob")
	must(err)
	if pr.AgeRecheck == nil || pr.AgeRecheck.Releases != 1 || pr.AgeRecheck.Held != 1 || pr.AgeRecheck.StatusChanged != 1 || pr.AgeRecheck.Flagged != 1 {
		t.Fatalf("age recheck %+v", pr.AgeRecheck)
	}
	if n := count(`SELECT count(*) FROM releases WHERE id=$1 AND status='held_review'`, relID); n != 1 {
		t.Error("release not held")
	}
	_, err = s.PatchPerformer(ctx, perfID, patch, "bob") // same (now stale) version
	wantConflict("stale version", err)
	if n := count(`SELECT count(*) FROM performer_overrides WHERE performer_id=$1 AND field_name='birth_date' AND actor='bob'`, perfID); n != 1 {
		t.Error("birth_date override missing")
	}
	// Re-ingest keeps the override.
	_, err = s.SaveCatalogRecord(ctx, src, "t2", catalog.Record{EntityType: catalog.Performer, SourceKey: "sena-" + sfx, URL: "https://perf-a.test/idols/sena-" + sfx,
		Fields: map[string]any{"name": name, "birth_date": "1995-03-04"}})
	must(err)
	if n := count(`SELECT count(*) FROM performers WHERE id=$1 AND birth_date='2004-09-01'`, perfID); n != 1 {
		t.Error("re-ingest overwrote the overridden birth_date")
	}
	// Back to an adult birth date: R-AGE never lifts a hold by itself.
	patch, err = ParsePerformerPatch(map[string]json.RawMessage{"birth_date": json.RawMessage(`"1995-03-04"`)})
	must(err)
	pr, err = s.PatchPerformer(ctx, perfID, patch, "bob")
	must(err)
	if pr.AgeRecheck.StillHeld != 1 || pr.AgeRecheck.StatusChanged != 0 {
		t.Errorf("recheck after fix %+v", pr.AgeRecheck)
	}
	exec(`UPDATE releases SET status='published' WHERE id=$1`, relID)

	// ---- Requests: hide the birthday (public intake → admin approval).
	in := PerformerRequestInput{Performer: catalog.Slug(name), Kind: RequestHideBirthDate, Contact: "agent@agency.example", Role: "agency", Message: "secret evidence " + sfx}
	must(in.Normalize())
	receipt, err := s.CreatePerformerRequest(ctx, in, time.Now())
	must(err)
	if receipt.Status != RequestOpen || receipt.DueAt.Before(time.Now().Add(71*time.Hour)) {
		t.Errorf("receipt %+v", receipt)
	}
	bad := PerformerRequestInput{Performer: "nobody-" + sfx, Kind: RequestOther, Contact: "x"}
	must(bad.Normalize())
	if _, err = s.CreatePerformerRequest(ctx, bad, time.Now()); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown performer: %v", err)
	}
	reqs, _, err := s.PerformerRequests(ctx, PerformerRequestFilter{Status: RequestOpen, PerformerID: perfID})
	must(err)
	if len(reqs) != 1 || reqs[0].ID != receipt.ID || !strings.Contains(string(reqs[0].Evidence), "secret evidence") {
		t.Fatalf("requests %+v", reqs)
	}
	dec, err := s.DecidePerformerRequest(ctx, receipt.ID, RequestApproved, RequestDecision{Note: "agency verified"}, "carol")
	must(err)
	if dec.Request.Status != RequestApproved || dec.Request.ResolvedBy == nil || *dec.Request.ResolvedBy != "carol" || dec.Applied["birth_date_hidden"] != true {
		t.Fatalf("decision %+v", dec)
	}
	_, err = s.DecidePerformerRequest(ctx, receipt.ID, RequestRejected, RequestDecision{Note: "n"}, "carol")
	wantConflict("decide twice", err)
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='performer' AND entity_id=$1 AND (before_value::text LIKE '%secret evidence%' OR after_value::text LIKE '%secret evidence%' OR reason LIKE '%secret evidence%' OR after_value::text LIKE '%agency.example%')`, perfID); n != 0 {
		t.Error("request evidence leaked into review_events")
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='performer' AND entity_id=$1 AND action IN ('request_received','request_approve')`, perfID); n != 2 {
		t.Errorf("request review_events = %d", n)
	}

	// ---- Hidden birthday in every public read.
	detail, err := s.PerformerDetail(ctx, catalog.Slug(name))
	must(err)
	if detail["birth_date"] != nil || detail["debut_age"] != nil || detail["birth_date_hidden"] != true {
		t.Errorf("public detail shows the hidden birthday: %v", detail)
	}
	bySlug, err := s.PerformerBySlug(ctx, catalog.Slug(name))
	must(err)
	if bySlug.BirthDate != nil {
		t.Error("PerformerBySlug shows the hidden birthday")
	}
	listed, err := s.Performers(ctx, name, 5)
	must(err)
	if len(listed) != 1 || listed[0].BirthDate != nil {
		t.Errorf("Performers: %+v", listed)
	}
	pg, _, err := s.PerformerPage(ctx, PerformerFilter{Q: name})
	must(err)
	if len(pg) != 1 || pg[0].BirthDate != nil {
		t.Errorf("PerformerPage: %+v", pg)
	}
	var relSlug string
	must(s.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, relID).Scan(&relSlug))
	cast, err := s.ReleasePerformers(ctx, relSlug)
	must(err)
	if len(cast) != 1 || cast[0].BirthDate != nil {
		t.Errorf("ReleasePerformers: %+v", cast)
	}
	found, err := s.SearchCatalog(ctx, name, 5)
	must(err)
	if len(found) != 1 || found[0]["birth_date"] != nil {
		t.Errorf("SearchCatalog: %+v", found)
	}
	// The admin and R-AGE still see the real date.
	ap, err = s.AdminPerformer(ctx, perfID)
	must(err)
	if ap.BirthDate == nil || !ap.BirthDateHidden || ap.Works[0].AgeAtFilming == nil {
		t.Errorf("admin detail lost the real birthday: %+v", ap)
	}
	rf, err := s.ReleaseFacts(ctx, relID)
	must(err)
	if len(rf.Performers) == 0 || rf.Performers[0].BirthDate == nil {
		t.Fatal("R-AGE lost the hidden birth date")
	}
	patch, err = ParsePerformerPatch(map[string]json.RawMessage{"birth_date": json.RawMessage(`"2004-09-01"`)})
	must(err)
	pr, err = s.PatchPerformer(ctx, perfID, patch, "bob")
	must(err)
	if pr.AgeRecheck.Held != 1 {
		t.Errorf("R-AGE did not hold with a hidden birthday: %+v", pr.AgeRecheck)
	}
	hidden, _, err := s.AdminPerformers(ctx, AdminPerformerFilter{Q: name, Filters: []string{PerformerFilterBirthHidden}})
	must(err)
	if len(hidden) != 1 || !hidden[0].BirthDateHidden {
		t.Errorf("birth_hidden filter: %+v", hidden)
	}
	counts, err := s.AdminPerformerCounts(ctx, name, "")
	must(err)
	if counts["all"] != 1 || counts[PerformerFilterBirthHidden] != 1 || counts[PerformerFilterMissingPhoto] != 1 || counts[PerformerFilterMissingBirth] != 0 {
		t.Errorf("counts %+v", counts)
	}

	// ---- correct_field request: approval writes an override.
	in = PerformerRequestInput{Performer: catalog.Slug(name), Kind: RequestCorrectField, Field: "height_cm", Value: json.RawMessage(`"158"`), Contact: "tel 0"}
	must(in.Normalize())
	receipt, err = s.CreatePerformerRequest(ctx, in, time.Now())
	must(err)
	dec, err = s.DecidePerformerRequest(ctx, receipt.ID, RequestApproved, RequestDecision{Value: json.RawMessage(`159`)}, "carol")
	must(err)
	if n := count(`SELECT count(*) FROM performers WHERE id=$1 AND height_cm=159`, perfID); n != 1 {
		t.Errorf("correction not applied: %+v", dec)
	}
	if n := count(`SELECT count(*) FROM performer_overrides WHERE performer_id=$1 AND field_name='height_cm'`, perfID); n != 1 {
		t.Error("correction is not an override")
	}
	// remove_photo suppresses, reject records the answer.
	exec(`INSERT INTO media_assets(entity_type,entity_id,asset_type,object_key,content_sha256,mime_type) VALUES('performer',$1,'avatar',$2,$2,'image/jpeg')`, perfID, "perf-"+sfx)
	in = PerformerRequestInput{Performer: catalog.Slug(name), Kind: RequestRemovePhoto, Contact: "tel 0"}
	must(in.Normalize())
	receipt, err = s.CreatePerformerRequest(ctx, in, time.Now())
	must(err)
	dec, err = s.DecidePerformerRequest(ctx, receipt.ID, RequestApproved, RequestDecision{}, "carol")
	must(err)
	if dec.Applied["suppressed_photos"] != int64(1) || count(`SELECT count(*) FROM media_assets WHERE entity_id=$1 AND entity_type='performer' AND suppressed_at IS NOT NULL`, perfID) != 1 {
		t.Errorf("remove_photo: %+v", dec.Applied)
	}
	in = PerformerRequestInput{Performer: catalog.Slug(name), Kind: RequestOther, Contact: "tel 0"}
	must(in.Normalize())
	receipt, err = s.CreatePerformerRequest(ctx, in, time.Now())
	must(err)
	_, err = s.DecidePerformerRequest(ctx, receipt.ID, RequestRejected, RequestDecision{}, "carol")
	wantValidation("reject without note", err)
	dec, err = s.DecidePerformerRequest(ctx, receipt.ID, RequestRejected, RequestDecision{Note: "not the performer"}, "carol")
	must(err)
	if dec.Request.Status != RequestRejected || dec.Request.ResolutionNote == nil {
		t.Errorf("reject %+v", dec.Request)
	}

	// ---- Aliases.
	alias, err := s.AddPerformerAlias(ctx, perfID, AliasInput{Name: "Sena Alias " + sfx, AliasType: "stage"}, "dan")
	must(err)
	_, err = s.AddPerformerAlias(ctx, perfID, AliasInput{Name: "sena  alias " + sfx, AliasType: "stage"}, "dan")
	wantConflict("duplicate alias", err)
	viaAlias, _, err := s.AdminPerformers(ctx, AdminPerformerFilter{Q: "Sena Alias " + sfx})
	must(err)
	if len(viaAlias) != 1 || viaAlias[0].ID != perfID {
		t.Errorf("alias search: %+v", viaAlias)
	}
	must(s.RemovePerformerAlias(ctx, perfID, alias.ID, "typo", "dan"))
	if err = s.RemovePerformerAlias(ctx, perfID, alias.ID, "", "dan"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("remove twice: %v", err)
	}
	var mergedAlias int64
	must(s.QueryRow(ctx, `INSERT INTO performer_aliases(performer_id,name,normalized_name,alias_type) VALUES($1,'M','m-`+sfx+`','merged') RETURNING id`, perfID).Scan(&mergedAlias))
	wantConflict("remove merged alias", s.RemovePerformerAlias(ctx, perfID, mergedAlias, "", "dan"))

	// ---- Similarity preview seeds.
	exec(`INSERT INTO performer_similar(performer_id,similar_id,score,evidence) VALUES($1,$2,0.5,'{"metadata":0.5,"co_star":1,"co_star_score":0.333,"co_watch":0.2}')`, perfID, junkID)
	seeds, err := s.SimilaritySeeds(ctx, []int64{perfID}, 0)
	must(err)
	if len(seeds) != 1 || len(seeds[0].Neighbors) != 0 { // the hidden junk performer is not a public neighbour
		t.Errorf("seeds %+v", seeds)
	}
	ap, err = s.AdminPerformer(ctx, perfID)
	must(err)
	if len(ap.Similar) != 1 || ap.Similar[0].Status != PerformerHidden || len(ap.Requests) != 4 || len(ap.ReviewEvents) == 0 || len(ap.Overrides) < 2 {
		t.Errorf("admin detail: similar %+v requests %d events %d overrides %d", ap.Similar, len(ap.Requests), len(ap.ReviewEvents), len(ap.Overrides))
	}

	// ---- A birthday hidden on a duplicate stays hidden on the survivor.
	var survivor int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,birth_date) VALUES($1,$2,'1990-01-01') RETURNING id`, "survivor-"+sfx, "Survivor "+sfx).Scan(&survivor))
	exec(`INSERT INTO performers(slug,canonical_name,birth_date,birth_date_hidden,status,merged_into_id) VALUES($1,$2,'1990-01-01',true,'merged',$3)`, "dup-"+sfx, "Dup "+sfx, survivor)
	sv, err := s.PerformerBySlug(ctx, "survivor-"+sfx)
	must(err)
	if sv.BirthDate != nil {
		t.Error("survivor of a merge shows the duplicate's hidden birthday")
	}

	// ---- Unhide.
	_, err = s.SetPerformerStatus(ctx, junkID, PerformerStatusChange{To: PerformerActive, Reason: "restored"}, "alice")
	must(err)
	if n := count(`SELECT count(*) FROM performers WHERE id=$1 AND status='active' AND hidden_reason IS NULL`, junkID); n != 1 {
		t.Error("unhide")
	}
}
