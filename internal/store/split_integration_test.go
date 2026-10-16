package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/identity"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// TestEntitySplitIntegration splits a performer and a release that ingest
// built from two different real entities, checks what moved, the canonical
// fields of both sides, that re-ingest keeps attaching the moved records to
// the new entity, and that undo re-merges through the merge path. Skipped
// unless AVXXX_TEST_DATABASE_URL is set (make test-integration).
func TestEntitySplitIntegration(t *testing.T) {
	s := openIntegration(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}
	str := func(q string, args ...any) string {
		t.Helper()
		var v *string
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	save := func(src int64, rec catalog.Record) int64 {
		t.Helper()
		srID, err := s.SaveCatalogRecord(ctx, src, "t", rec)
		if err != nil {
			t.Fatalf("save %s: %v", rec.SourceKey, err)
		}
		return srID
	}
	wantConflict := func(what string, err error) {
		t.Helper()
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Errorf("%s: err %v, want ConflictError", what, err)
		}
	}
	sfx := fmt.Sprint(time.Now().UnixNano())
	num := sfx[len(sfx)-7:]
	srcA := id(`INSERT INTO sources(name,url,source_kind) VALUES('split-a','https://split-a.test/'||$1,'official_catalog') RETURNING id`, sfx)
	srcB := id(`INSERT INTO sources(name,url,source_kind) VALUES('split-b','https://split-b.test/'||$1,'aggregator') RETURNING id`, sfx)
	user := id(`INSERT INTO users(email,password_hash) VALUES('split-'||$1||'@example.test','x') RETURNING id`, sfx)
	user2 := id(`INSERT INTO users(email,password_hash) VALUES('split2-'||$1||'@example.test','x') RETURNING id`, sfx)

	// ---- Performer: two same-name people from two sources became one.
	name := "Split Person " + sfx
	perfRec := func(src int64, key string, f map[string]any) catalog.Record {
		f["name"] = name
		return catalog.Record{EntityType: catalog.Performer, SourceKey: key, URL: "https://x.test/" + key, Fields: f}
	}
	recPA := perfRec(srcA, "pa-"+sfx, map[string]any{"birth_date": "1990-01-01", "height_cm": "160"})
	recPB := perfRec(srcB, "pb-"+sfx, map[string]any{"birth_date": "1995-05-05", "height_cm": "150", "cup_size": "D", "name_romaji": "Other Person " + sfx})
	srPA, srPB := save(srcA, recPA), save(srcB, recPB)
	relRec := func(key, code, title string, extra map[string]any) catalog.Record {
		f := map[string]any{"title": title, "dvd_code": code, "performers": []string{name}}
		for k, v := range extra {
			f[k] = v
		}
		return catalog.Record{EntityType: catalog.Release, SourceKey: key, URL: "https://x.test/" + key, Fields: f}
	}
	recRA := relRec("ra-"+sfx, "SPA-"+num, "Release A "+sfx, nil)
	recRB := relRec("rb-"+sfx, "SPB-"+num, "Release B "+sfx, nil)
	srRA, srRB := save(srcA, recRA), save(srcB, recRB)
	orig := id(`SELECT performer_id FROM source_records WHERE id=$1`, srPA)
	if id(`SELECT performer_id FROM source_records WHERE id=$1`, srPB) != orig {
		t.Fatal("setup: the two people did not collapse into one performer")
	}
	origSlug := str(`SELECT slug FROM performers WHERE id=$1`, orig)
	relB := id(`SELECT release_id FROM source_records WHERE id=$1`, srRB)
	relA := id(`SELECT release_id FROM source_records WHERE id=$1`, srRA)
	if got := str(`SELECT birth_date::text FROM performers WHERE id=$1`, orig); got != "1990-01-01" {
		t.Fatalf("setup birth_date = %s (official source should win)", got)
	}
	exec(`INSERT INTO performer_overrides(performer_id,field_name,value,actor) VALUES($1,'birth_place','"Tokyo"','admin')`, orig)
	exec(`UPDATE performers SET birth_place='Tokyo' WHERE id=$1`, orig)
	exec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$2),($3,$2)`, user, orig, user2)
	version := int64(id(`SELECT version FROM performers WHERE id=$1`, orig))

	// Preview: what moves and both sides' fields; nothing changes.
	pv, err := s.SplitPreview(ctx, SplitRequest{EntityType: "performer", EntityID: orig})
	must(err)
	if len(pv.Records) != 4 || pv.Plan != nil || pv.Version != version {
		t.Fatalf("preview records = %+v plan=%v", pv.Records, pv.Plan)
	}
	sel := []int64{srPB, srRB}
	pv, err = s.SplitPreview(ctx, SplitRequest{EntityType: "performer", EntityID: orig, SourceRecordIDs: sel})
	must(err)
	if pv.Plan == nil || pv.Plan.NewSlug == "" || pv.Plan.NewSlug == origSlug {
		t.Fatalf("preview plan = %+v", pv.Plan)
	}
	if f, _ := pv.Plan.Field(false, "birth_date"); string(f.Value) != `"1995-05-05"` {
		t.Errorf("preview new birth_date = %s", f.Value)
	}
	if n := count(`SELECT count(*) FROM performers WHERE slug=$1`, pv.Plan.NewSlug); n != 0 {
		t.Error("preview created the new performer")
	}
	_, err = s.SplitEntity(ctx, SplitRequest{EntityType: "performer", EntityID: orig, SourceRecordIDs: sel, Version: version + 7, Actor: "t"})
	wantConflict("stale version", err)
	_, err = s.SplitEntity(ctx, SplitRequest{EntityType: "performer", EntityID: orig, SourceRecordIDs: []int64{srPA, srPB, srRA, srRB}, Version: version, Actor: "t"})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("split of everything: %v, want ValidationError", err)
	}

	res, err := s.SplitEntity(ctx, SplitRequest{EntityType: "performer", EntityID: orig, SourceRecordIDs: sel, Version: version, Actor: "tester", Reason: "two people"})
	must(err)
	newP := res.NewID
	if res.NewSlug == "" || str(`SELECT slug FROM performers WHERE id=$1`, newP) != res.NewSlug {
		t.Errorf("new slug = %q", res.NewSlug)
	}
	if res.NewSlug != catalog.Slug("Other Person "+sfx) {
		t.Errorf("new slug %q, want the romaji reading's slug (the name slug is the original's)", res.NewSlug)
	}
	if got := str(`SELECT slug FROM performers WHERE id=$1`, orig); got != origSlug {
		t.Errorf("original slug changed to %s", got)
	}
	if id(`SELECT performer_id FROM source_records WHERE id=$1`, srPB) != newP || id(`SELECT performer_id FROM source_records WHERE id=$1`, srPA) != orig {
		t.Error("source records not split")
	}
	if id(`SELECT release_id FROM source_records WHERE id=$1`, srRB) != relB {
		t.Error("a credit moved its release record off the release")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relB, newP); n != 1 {
		t.Error("release B credit not moved to the new performer")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relB, orig); n != 0 {
		t.Error("release B still credits the original")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relA, orig); n != 1 {
		t.Error("release A credit lost")
	}
	if n := count(`SELECT count(*) FROM release_performer_sources WHERE source_record_id=$1 AND performer_id=$2`, srRB, newP); n != 1 {
		t.Error("credit evidence not moved")
	}
	for col, want := range map[string]string{"birth_date::text": "1990-01-01", "height_cm::int::text": "160", "cup_size": "<nil>", "birth_place": "Tokyo"} {
		if got := str(fmt.Sprintf(`SELECT %s FROM performers WHERE id=$1`, col), orig); got != want {
			t.Errorf("original %s = %s, want %s", col, got, want)
		}
	}
	for col, want := range map[string]string{"canonical_name": name, "birth_date::text": "1995-05-05", "height_cm::int::text": "150", "cup_size": "D", "birth_place": "<nil>"} {
		if got := str(fmt.Sprintf(`SELECT %s FROM performers WHERE id=$1`, col), newP); got != want {
			t.Errorf("new %s = %s, want %s", col, got, want)
		}
	}
	if n := count(`SELECT count(*) FROM performer_overrides WHERE performer_id=$1`, newP); n != 0 {
		t.Error("the new performer got overrides")
	}
	if n := count(`SELECT count(*) FROM follows WHERE performer_id=$1`, orig); n != 2 {
		t.Errorf("follows on original = %d, want 2 (user data stays)", n)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE id=$1 AND action='split' AND entity_id=$2 AND actor='tester' AND after_value->'plan'->>'new_id'=$3`, res.ReviewEventID, orig, fmt.Sprint(newP)); n != 1 {
		t.Error("split review event missing")
	}
	p := identity.CanonicalPair(orig, newP)
	if n := count(`SELECT count(*) FROM not_duplicate_pairs WHERE entity_type='performer' AND a_id=$1 AND b_id=$2 AND review_event_id=$3`, p.Lo, p.Hi, res.ReviewEventID); n != 1 {
		t.Error("not-duplicate pair missing")
	}
	if _, err = s.SplitEntity(ctx, SplitRequest{EntityType: "performer", EntityID: orig, SourceRecordIDs: []int64{srRA}, Version: version, Actor: "t"}); err == nil {
		t.Error("split at the old version accepted")
	}

	// ---- Re-ingest: moved records keep attaching to the new performer.
	for range 2 {
		save(srcB, recPB)
		save(srcB, recRB)
		save(srcA, recPA)
		save(srcA, recRA)
	}
	if id(`SELECT performer_id FROM source_records WHERE id=$1`, srPB) != newP {
		t.Error("re-ingest moved the performer record back")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relB, orig); n != 0 {
		t.Error("re-ingest credited the original on release B again")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relB, newP); n != 1 {
		t.Error("re-ingest lost the new performer's credit")
	}
	if got := str(`SELECT birth_date::text FROM performers WHERE id=$1`, orig); got != "1990-01-01" {
		t.Errorf("re-ingest changed the original's birth_date to %s", got)
	}
	if n := count(`SELECT count(*) FROM performers WHERE canonical_name=$1 AND merged_into_id IS NULL`, name); n != 2 {
		t.Errorf("%d live performers named %q, want 2", n, name)
	}
	gen, err := s.GenerateIdentityCandidates(ctx, "performer")
	must(err)
	_ = gen
	if n := count(`SELECT count(*) FROM identity_candidates WHERE entity_type='performer' AND left_id=$1 AND right_id=$2`, p.Lo, p.Hi); n != 0 {
		t.Error("candidate generation proposed the split pair")
	}

	// ---- Undo: re-merge through the merge path.
	undo, err := s.UndoSplit(ctx, res.ReviewEventID, "tester", "same person after all")
	must(err)
	if undo.MergedID != newP || undo.OriginalID != orig {
		t.Errorf("undo = %+v", undo)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE id=$1 AND action='undo_split' AND reverts_event_id=$2`, undo.ReviewEventID, res.ReviewEventID); n != 1 {
		t.Error("undo event not linked to the split")
	}
	if id(`SELECT merged_into_id FROM performers WHERE id=$1`, newP) != orig || id(`SELECT performer_id FROM source_records WHERE id=$1`, srPB) != orig {
		t.Error("undo did not merge back")
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=$1 AND performer_id=$2`, relB, orig); n != 1 {
		t.Error("undo did not restore the release B credit")
	}
	if n := count(`SELECT count(*) FROM not_duplicate_pairs WHERE entity_type='performer' AND a_id=$1 AND b_id=$2`, p.Lo, p.Hi); n != 0 {
		t.Error("undo kept the not-duplicate pair")
	}
	if got := str(`SELECT cup_size FROM performers WHERE id=$1`, orig); got != "D" {
		t.Errorf("undo did not recompute cup_size: %s", got)
	}
	if target, ok, err := s.ResolveMergedSlug(ctx, "performer", res.NewSlug); err != nil || !ok || target != origSlug {
		t.Errorf("new slug redirect = %q %v %v", target, ok, err)
	}
	_, err = s.UndoSplit(ctx, res.ReviewEventID, "tester", "")
	wantConflict("second undo", err)
	_, err = s.UndoMerge(ctx, undo.ReviewEventID, "tester", "")
	wantConflict("undo_merge of an undo_split", err)
	_, err = s.SplitPreview(ctx, SplitRequest{EntityType: "performer", EntityID: newP})
	wantConflict("split of a merged performer", err)

	// ---- Release: a wrong code match glued two releases together.
	code := "SPL-" + num
	recR1 := relRec("r1-"+sfx, code, "Right "+sfx, map[string]any{"maker": "Maker A", "performers": []string{}})
	recR2 := relRec("r2-"+sfx, code, "Wrong "+sfx, map[string]any{"maker": "Maker A", "content_id": "zz" + num, "runtime": "95", "tags": []string{"VR"}, "performers": []string{}})
	sr1, sr2 := save(srcA, recR1), save(srcB, recR2)
	rel := id(`SELECT release_id FROM source_records WHERE id=$1`, sr1)
	if id(`SELECT release_id FROM source_records WHERE id=$1`, sr2) != rel {
		t.Fatal("setup: releases did not collide")
	}
	relSlug := str(`SELECT slug FROM releases WHERE id=$1`, rel)
	exec(`UPDATE releases SET status='published' WHERE id=$1`, rel)
	exec(`INSERT INTO favorites(user_id,release_id) VALUES($1,$2),($3,$2)`, user, rel, user2)
	exec(`INSERT INTO release_media_sources(release_id,source,media_id) VALUES($1,'javtiful','m-'||$2)`, rel, sfx)
	relVersion := id(`SELECT version FROM releases WHERE id=$1`, rel)
	exec(`UPDATE releases SET status='held_review' WHERE id=$1`, rel)
	_, err = s.SplitEntity(ctx, SplitRequest{EntityType: "release", EntityID: rel, SourceRecordIDs: []int64{sr2}, Version: relVersion, Actor: "t"})
	wantConflict("held release", err)
	exec(`UPDATE releases SET status='published' WHERE id=$1`, rel)

	rres, err := s.SplitEntity(ctx, SplitRequest{EntityType: "release", EntityID: rel, SourceRecordIDs: []int64{sr2}, Version: relVersion, Actor: "t",
		MediaLinks: []string{"javtiful"}, MoveUserData: map[string][]int64{"favorites": {user2}}})
	must(err)
	newR := rres.NewID
	if want := catalog.ReleaseSlug("", "Wrong "+sfx, "split-b", "r2-"+sfx); rres.NewSlug != want {
		t.Errorf("new release slug = %q, want %q", rres.NewSlug, want)
	}
	if str(`SELECT slug FROM releases WHERE id=$1`, rel) != relSlug || str(`SELECT status FROM releases WHERE id=$1`, newR) != "published" {
		t.Error("original slug changed or new status not copied")
	}
	if got := str(`SELECT canonical_title FROM releases WHERE id=$1`, rel); got != "Right "+sfx {
		t.Errorf("original title = %s", got)
	}
	if got := str(`SELECT canonical_title FROM releases WHERE id=$1`, newR); got != "Wrong "+sfx {
		t.Errorf("new title = %s", got)
	}
	if got := str(`SELECT runtime_minutes::text FROM releases WHERE id=$1`, rel); got != "<nil>" {
		t.Errorf("original runtime = %s, want cleared (only the moved record had it)", got)
	}
	if n := count(`SELECT count(*) FROM release_identifiers WHERE release_id=$1 AND identifier_type='content_id'`, newR); n != 1 {
		t.Error("content_id not moved")
	}
	if n := count(`SELECT count(*) FROM release_identifiers WHERE release_id=$1 AND identifier_type='dvd_code'`, rel); n != 1 {
		t.Error("shared dvd_code left the original")
	}
	if n := count(`SELECT count(*) FROM release_tags WHERE release_id=$1 AND normalized_tag='vr'`, newR); n != 1 {
		t.Error("tag not moved")
	}
	if n := count(`SELECT count(*) FROM release_tags WHERE release_id=$1`, rel); n != 0 {
		t.Error("tag stayed on the original")
	}
	if id(`SELECT user_id FROM favorites WHERE release_id=$1`, newR) != user2 || count(`SELECT count(*) FROM favorites WHERE release_id=$1`, rel) != 1 {
		t.Error("favorites not split as selected")
	}
	if n := count(`SELECT count(*) FROM release_media_sources WHERE release_id=$1`, newR); n != 1 {
		t.Error("media link not moved")
	}
	// Re-ingest and a code-based media link keep to the new release.
	save(srcB, recR2)
	save(srcA, recR1)
	if id(`SELECT release_id FROM source_records WHERE id=$1`, sr2) != newR {
		t.Error("re-ingest moved the release record back to the code's release")
	}
	if got := str(`SELECT canonical_title FROM releases WHERE id=$1`, rel); got != "Right "+sfx {
		t.Errorf("re-ingest overwrote the original title: %s", got)
	}
	ok, err := s.LinkMediaByIdentifier(ctx, "javtiful", "m-"+sfx, "https://m.test/"+sfx, code)
	must(err)
	if !ok || count(`SELECT count(*) FROM release_media_sources WHERE release_id=$1`, rel) != 0 || count(`SELECT count(*) FROM release_media_sources WHERE release_id=$1`, newR) != 1 {
		t.Error("code match re-linked the media to the original")
	}
	rundo, err := s.UndoSplit(ctx, rres.ReviewEventID, "t", "")
	must(err)
	if rundo.MergedID != newR || id(`SELECT release_id FROM source_records WHERE id=$1`, sr2) != rel {
		t.Error("release undo did not merge back")
	}
	if got := str(`SELECT runtime_minutes::text FROM releases WHERE id=$1`, rel); got != "95" {
		t.Errorf("undo did not recompute runtime: %s", got)
	}
	if n := count(`SELECT count(*) FROM favorites WHERE release_id=$1`, rel); n != 2 {
		t.Errorf("favorites after undo = %d", n)
	}
	if _, err = s.UndoSplit(ctx, res.ReviewEventID+1_000_000, "t", ""); err == nil {
		t.Error("undo of an unknown event accepted")
	}
}
