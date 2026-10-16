package store

import (
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSlugScopesIntegration covers Japanese-only names (FINAL_DATA_DESIGN
// 3.1) and maker scopes: DMM fixture ingest links credited performers,
// romaji / source-scoped / id slugs, external-id and name resolution, the
// better-slug redirect, unchanged ASCII slugs on re-ingest, the performer
// relation override, migration 028's re-scoping of bare "maker:" codes and
// the non-empty slug constraint. Skipped unless AVXXX_TEST_DATABASE_URL is
// set.
func TestSlugScopesIntegration(t *testing.T) {
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
	save := func(src int64, rec catalog.Record) {
		t.Helper()
		if _, err := s.SaveCatalogRecord(ctx, src, "test", rec); err != nil {
			t.Fatalf("save %s: %v", rec.SourceKey, err)
		}
	}
	// credits returns "slug:credited_name:primary" of a release's performers.
	credits := func(releaseSlug string) []string {
		t.Helper()
		rows, err := s.Query(ctx, `SELECT p.slug||':'||rp.credited_name||':'||rp.is_primary FROM release_performers rp
			JOIN performers p ON p.id=rp.performer_id JOIN releases r ON r.id=rp.release_id WHERE r.slug=$1 ORDER BY rp.billing_order`, releaseSlug)
		must(err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			must(rows.Scan(&v))
			out = append(out, v)
		}
		must(rows.Err())
		return out
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}
	sfx := fmt.Sprint(time.Now().UnixNano())
	num := sfx[len(sfx)-9:] // a numeric id unique to this run

	var itemListID, actressID int64
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE url=$1`, catalog.DMMItemListURL).Scan(&itemListID))
	must(s.QueryRow(ctx, `SELECT id FROM sources WHERE url=$1`, catalog.DMMActressSearchURL).Scan(&actressID))
	discover := func(file, start string) []catalog.Record {
		t.Helper()
		creds := catalog.DMMCredentials{APIID: "SLUGAPIID", AffiliateID: "slug-990"}
		body, err := os.ReadFile("../catalog/testdata/dmm/" + file)
		must(err)
		body = []byte(strings.NewReplacer("{{API_ID}}", creds.APIID, "{{AFFILIATE_ID}}", creds.AffiliateID).Replace(string(body)))
		recs, err := catalog.NewDMMAdapterWith(creds, catalog.DMMAPIHost).Discover(ctx, catalog.Document{URL: start, Status: 200, Body: body})
		must(err)
		return recs
	}

	// ---- DMM fixture: Japanese-only credited names are linked, not dropped.
	items := discover("itemlist_page1.json", catalog.DMMItemListURL)
	for range 2 { // re-ingest is idempotent
		for _, rec := range items {
			save(itemListID, rec)
		}
	}
	if got := credits("sone-156"); len(got) != 1 || got[0] != "unpai:うんぱい:true" {
		t.Errorf("sone-156 credits = %q, want the kana name under its romaji slug", got)
	}
	if got := credits("dss-145"); len(got) != 1 || got[0] != "dmm-actress-15187:小澤マリア:true" {
		t.Errorf("dss-145 credits = %q, want the source-scoped slug (kanji name, no reading)", got)
	}
	if n := count(`SELECT count(*) FROM performers WHERE canonical_name IN ('うんぱい','小澤マリア')`); n != 2 {
		t.Errorf("performers for the fixture credits = %d, want 2 after re-ingest", n)
	}
	if n := count(`SELECT count(*) FROM performer_external_ids e JOIN performers p ON p.id=e.performer_id WHERE e.namespace='dmm_actress' AND e.external_id='1074740' AND p.slug='unpai'`); n != 1 {
		t.Errorf("dmm actress id not recorded")
	}
	if n := count(`SELECT count(*) FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE p.slug='unpai' AND a.alias_type='romaji' AND a.normalized_name='unpai'`); n != 1 {
		t.Errorf("romaji alias not recorded")
	}
	scopes := map[string]string{}
	rows, err := s.Query(ctx, `SELECT r.slug,ri.identifier_scope FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id
		WHERE r.slug IN ('sone-156','dss-145') AND ri.identifier_type='dvd_code' AND ri.source_id=$1`, itemListID)
	must(err)
	for rows.Next() {
		var slug, scope string
		must(rows.Scan(&slug, &scope))
		scopes[slug] = scope
	}
	rows.Close()
	if scopes["sone-156"] != "dmm-maker:3152" || scopes["dss-145"] != "dmm-maker:40016" {
		t.Errorf("dvd_code scopes = %v, want dmm-maker:<id>", scopes)
	}
	if n := count(`SELECT count(*) FROM release_identifiers WHERE identifier_scope='maker:'`); n != 0 {
		t.Errorf("%d bare maker: scopes", n)
	}
	// Exact-code matching (media link, identity code keys) ignores the scope.
	if ok, err := s.LinkMediaByIdentifier(ctx, "slugtest"+sfx, "m"+sfx, "", "SONE-156"); err != nil || !ok {
		t.Errorf("LinkMediaByIdentifier(SONE 156) = %v %v", ok, err)
	}

	// ---- ActressSearch record, then a credit by the same DMM id: one
	// performer, under the kana reading's romaji slug.
	for _, rec := range discover("actresssearch.json", catalog.DMMActressSearchURL) {
		save(actressID, rec)
	}
	var yumaID int64
	must(s.QueryRow(ctx, `SELECT id FROM performers WHERE slug='asamiyuma'`).Scan(&yumaID))
	relKey := "slugrel" + sfx
	rel := catalog.Record{EntityType: catalog.Release, SourceKey: relKey, URL: "https://slug.test/" + relKey, Fields: map[string]any{
		"title": "Slug release " + sfx, "dvd_code": "SLG-" + num, "maker": "スラッグ映像", "maker_id": num,
		"performers": []string{"麻美ゆま", "新人" + num},
		"performer_refs": []map[string]string{
			{"name": "麻美ゆま", "dmm_actress_id": "15365", "is_primary": "false"},
			{"name": "新人" + num, "dmm_actress_id": num},
		},
	}}
	save(itemListID, rel)
	relSlug := "slg-" + num
	if got := credits(relSlug); len(got) != 2 || got[0] != "asamiyuma:麻美ゆま:false" || got[1] != "dmm-actress-"+num+":新人"+num+":false" {
		t.Errorf("%s credits = %q (is_primary from the record)", relSlug, got)
	}
	if n := count(`SELECT count(*) FROM performers WHERE canonical_name='麻美ゆま' AND merged_into_id IS NULL`); n != 1 {
		t.Errorf("麻美ゆま performers = %d, want 1", n)
	}

	// ---- The credited actress's own record arrives later with a reading:
	// same performer, published slug kept, romaji slug redirects to it.
	save(actressID, catalog.Record{EntityType: catalog.Performer, SourceKey: "dmm-actress-" + num, URL: "https://slug.test/a/" + num,
		Fields: map[string]any{"name": "新人" + num, "name_ja": "新人" + num, "name_kana": "しんじん" + num, "dmm_actress_id": num}})
	if n := count(`SELECT count(*) FROM performers WHERE canonical_name=$1`, "新人"+num); n != 1 {
		t.Errorf("performer record for a credited actress created a duplicate (%d)", n)
	}
	if target, ok, err := s.ResolveMergedSlug(ctx, "performer", "shinjin"+num); err != nil || !ok || target != "dmm-actress-"+num {
		t.Errorf("better slug redirect = %q %v %v, want dmm-actress-%s", target, ok, err, num)
	}
	if n := count(`SELECT count(*) FROM performers WHERE slug=$1`, "dmm-actress-"+num); n != 1 {
		t.Error("published fallback slug changed")
	}

	// ---- Name match: a DMM credit of a performer another source published
	// under an English slug links to it.
	var dbSrc int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('slug-db','https://slug-db.test/'||$1,'javdatabase','structured_database') RETURNING id`, sfx).Scan(&dbSrc))
	enName := "Slug Star " + sfx
	jaName := "星野" + num
	save(dbSrc, catalog.Record{EntityType: catalog.Performer, SourceKey: "star-" + sfx, URL: "https://slug-db.test/star/" + sfx,
		Fields: map[string]any{"name": enName, "name_ja": jaName}})
	enSlug := catalog.Slug(enName)
	rel2 := catalog.Record{EntityType: catalog.Release, SourceKey: "slugrel2" + sfx, URL: "https://slug.test/2/" + sfx, Fields: map[string]any{
		"title": "Slug release two " + sfx, "dvd_code": "SLH-" + num, "maker": "スラッグ映像", "maker_id": num,
		"performers": []string{jaName}, "performer_refs": []map[string]string{{"name": jaName, "dmm_actress_id": "9" + num}},
	}}
	save(itemListID, rel2)
	if got := credits("slh-" + num); len(got) != 1 || got[0] != enSlug+":"+jaName+":true" {
		t.Errorf("name-matched credit = %q, want %s", got, enSlug)
	}

	// ---- Regression: ASCII slugs are unchanged on re-ingest, even when the
	// source renames the performer.
	asciiKey := "ascii-" + sfx
	save(dbSrc, catalog.Record{EntityType: catalog.Performer, SourceKey: asciiKey, URL: "https://slug-db.test/a/" + sfx, Fields: map[string]any{"name": "Regress Name " + sfx}})
	asciiSlug := "regress-name-" + sfx
	var asciiID int64
	must(s.QueryRow(ctx, `SELECT id FROM performers WHERE slug=$1`, asciiSlug).Scan(&asciiID))
	save(dbSrc, catalog.Record{EntityType: catalog.Performer, SourceKey: asciiKey, URL: "https://slug-db.test/a/" + sfx, Fields: map[string]any{"name": "Regress Renamed " + sfx}})
	save(dbSrc, catalog.Record{EntityType: catalog.Release, SourceKey: "ascii-rel-" + sfx, URL: "https://slug-db.test/r/" + sfx, Fields: map[string]any{
		"title": "ASCII release " + sfx, "dvd_code": "ASC-" + num, "maker": "Ascii Maker", "performers": []string{"Regress Name " + sfx}}})
	var gotSlug string
	must(s.QueryRow(ctx, `SELECT slug FROM performers WHERE id=$1`, asciiID).Scan(&gotSlug))
	if gotSlug != asciiSlug {
		t.Errorf("ASCII slug changed on re-ingest: %q", gotSlug)
	}
	if n := count(`SELECT count(*) FROM performers WHERE slug LIKE 'regress-%-'||$1`, sfx); n != 1 {
		t.Errorf("rename created a second performer (%d)", n)
	}
	if got := credits("asc-" + num); len(got) != 1 || got[0] != asciiSlug+":Regress Name "+sfx+":true" {
		t.Errorf("ASCII credit = %q", got)
	}
	if n := count(`SELECT count(*) FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id WHERE r.slug=$1 AND ri.identifier_scope='maker:ascii-maker'`, "asc-"+num); n != 1 {
		t.Error("ASCII maker scope changed")
	}

	// ---- No id, no reading, kanji only: performer-<id>, found again by name.
	kanji := "漢字名" + num
	plain := catalog.Record{EntityType: catalog.Release, SourceKey: "plain-" + sfx, URL: "https://slug-db.test/p/" + sfx, Fields: map[string]any{
		"title": "Plain release " + sfx, "dvd_code": "PLN-" + num, "maker": "漢字メーカー", "performers": []string{kanji}}}
	save(dbSrc, plain)
	save(dbSrc, plain)
	var fallback string
	var fallbackID int64
	must(s.QueryRow(ctx, `SELECT id,slug FROM performers WHERE canonical_name=$1`, kanji).Scan(&fallbackID, &fallback))
	if fallback != catalog.FallbackPerformerSlug(fallbackID) {
		t.Errorf("fallback slug = %q, want performer-%d", fallback, fallbackID)
	}
	if n := count(`SELECT count(*) FROM performers WHERE canonical_name=$1`, kanji); n != 1 {
		t.Errorf("re-ingest created %d performers", n)
	}
	if scope := catalog.MakerScope("漢字メーカー", ""); count(`SELECT count(*) FROM release_identifiers WHERE identifier_scope=$1 AND normalized_value=$2`, scope, "pln-"+num) != 1 {
		t.Errorf("maker-name scope %s missing", scope)
	}

	// ---- Admin override of the performer relation still wins.
	var ovRelID int64
	ovKey := "ov-" + sfx
	save(itemListID, catalog.Record{EntityType: catalog.Release, SourceKey: ovKey, URL: "https://slug.test/ov/" + sfx, Fields: map[string]any{"title": "Override release " + sfx, "dvd_code": "OVR-" + num}})
	must(s.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_record_key=$1`, ovKey).Scan(&ovRelID))
	must(upsertOverride(ctx, s, "release", ovRelID, OverridePerformers, json.RawMessage(`[]`), "test", "test", 0))
	save(itemListID, catalog.Record{EntityType: catalog.Release, SourceKey: ovKey, URL: "https://slug.test/ov/" + sfx, Fields: map[string]any{
		"title": "Override release " + sfx, "dvd_code": "OVR-" + num, "performers": []string{"うんぱい"},
		"performer_refs": []map[string]string{{"name": "うんぱい", "dmm_actress_id": "1074740"}}}})
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1`, ovRelID); n != 0 {
		t.Errorf("override ignored: %d credits", n)
	}
	if n := count(`SELECT count(*) FROM release_performer_sources WHERE release_id=$1`, ovRelID); n != 1 {
		t.Errorf("source evidence not kept under override: %d", n)
	}

	// ---- Migration 028 re-scopes bare "maker:" codes saved by older code.
	var bareDMM, bareOther int64
	must(s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, relSlug).Scan(&bareDMM))
	must(s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, "pln-"+num).Scan(&bareOther))
	_, err = s.Exec(ctx, `UPDATE release_identifiers SET identifier_scope='maker:' WHERE identifier_type='dvd_code' AND release_id IN ($1,$2)`, bareDMM, bareOther)
	must(err)
	s2, err := Open(ctx) // re-applies every migration
	must(err)
	s2.Close()
	for id, want := range map[int64]string{bareDMM: "dmm-maker:" + num, bareOther: catalog.MakerScope("漢字メーカー", "")} {
		var scope string
		must(s.QueryRow(ctx, `SELECT identifier_scope FROM release_identifiers WHERE identifier_type='dvd_code' AND release_id=$1`, id).Scan(&scope))
		if scope != want {
			t.Errorf("migrated scope of release %d = %q, want %q (catalog.MakerScope)", id, scope, want)
		}
	}

	// ---- Never an empty slug.
	if _, err := s.Exec(ctx, `INSERT INTO performers(slug,canonical_name) VALUES('','x')`); err == nil {
		t.Error("empty performer slug accepted")
	}
	if _, err := s.Exec(ctx, `INSERT INTO releases(slug,canonical_title) VALUES(' ','x')`); err == nil {
		t.Error("blank release slug accepted")
	}
}
