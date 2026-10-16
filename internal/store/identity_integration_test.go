package store

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestIdentityMergeIntegration runs generation, merge, not-duplicate and undo
// against a real, disposable PostgreSQL database. It is skipped unless
// AVXXX_TEST_DATABASE_URL is set; the database is wiped first.
func TestIdentityMergeIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	t.Setenv("DATABASE_URL", dsn)
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for range 2 { // migrations must be re-runnable
		if s, err = Open(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Exec(ctx, "SELECT 1"); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if s, err = Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := s.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	exec(`INSERT INTO sources(id,name,url) VALUES(1,'src','https://example.test/feed')`)
	exec(`INSERT INTO users(id,email,password_hash) VALUES(1,'u@example.test','x')`)
	exec(`INSERT INTO performers(id,slug,canonical_name,birth_date) VALUES(1,'yui-hatano','Yui Hatano','1988-05-24'),
		(2,'yui-hatano--src2','YUI HATANO','1988-05-24'),(3,'mia','Mia',NULL),(4,'mia--2','Mia','1990-01-01')`)
	exec(`UPDATE performers SET canonical_name_ja='波多野結衣' WHERE id=2`)
	exec(`INSERT INTO performer_aliases(performer_id,name,normalized_name) VALUES(2,'Hatano','hatano')`)
	exec(`INSERT INTO releases(id,slug,canonical_title,runtime_minutes,status) VALUES(10,'juq-743','Summer Story',120,'published'),(11,'juq-743--src2','Summer Story',121,'published')`)
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES(10,'dvd_code','JUQ-743','juq-743'),(11,'content_id','juq00743','juq00743')`)
	exec(`INSERT INTO release_performers(release_id,performer_id) VALUES(10,1),(10,3),(11,2),(11,3)`)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES(10,'Drama','drama'),(11,'drama','drama'),(11,'VR','vr')`)
	exec(`INSERT INTO favorites(user_id,release_id) VALUES(1,10),(1,11)`)
	exec(`INSERT INTO source_records(id,source_id,entity_type,source_record_key,source_url,release_id,parser_version) VALUES(500,1,'release','k11','https://example.test/11',11,'v1')`)
	exec(`SELECT setval('performers_id_seq',100); SELECT setval('releases_id_seq',100); SELECT setval('source_records_id_seq',1000)`)
	// Performers 3 and 4 share a name but were already ruled different.
	exec(`INSERT INTO not_duplicate_pairs(entity_type,a_id,b_id) VALUES('performer',3,4)`)

	gen, err := s.GenerateIdentityCandidates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if gen["performer"].Inserted != 1 || gen["release"].Inserted != 1 || gen["performer"].SkippedNotDuplicate != 1 {
		t.Fatalf("generate: %+v", gen)
	}
	if gen, err = s.GenerateIdentityCandidates(ctx, ""); err != nil || gen["performer"].Inserted+gen["release"].Inserted != 0 {
		t.Fatalf("second generate not idempotent: %+v %v", gen, err)
	}
	var perfCand, relCand int64
	if err = s.QueryRow(ctx, `SELECT id FROM identity_candidates WHERE entity_type='performer' AND left_id=1 AND right_id=2`).Scan(&perfCand); err != nil {
		t.Fatal(err)
	}
	if err = s.QueryRow(ctx, `SELECT id FROM identity_candidates WHERE entity_type='release' AND left_id=10 AND right_id=11`).Scan(&relCand); err != nil {
		t.Fatal(err)
	}

	// Merge release 11 into 10.
	if _, err = s.MergeCandidate(ctx, relCand, 99, "t", ""); !errors.Is(err, ErrInvalidMergeTarget) {
		t.Fatalf("bad target: %v", err)
	}
	relMerge, err := s.MergeCandidate(ctx, relCand, 10, "tester", "same product")
	if err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM release_tags WHERE release_id=10`); n != 2 {
		t.Errorf("tags on target = %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM favorites WHERE user_id=1`); n != 1 {
		t.Errorf("favorites = %d, want 1 (deduplicated)", n)
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE release_id=10`); n != 3 {
		t.Errorf("release 10 performers = %d, want 3 (1,2,3)", n)
	}
	if n := count(`SELECT count(*) FROM release_identifiers WHERE release_id=10`); n != 2 {
		t.Errorf("identifiers = %d", n)
	}
	if n := count(`SELECT count(*) FROM source_records WHERE release_id=10`); n != 1 {
		t.Errorf("source records not relinked")
	}
	if slug, ok, err := s.ResolveMergedSlug(ctx, "release", "juq-743--src2"); err != nil || !ok || slug != "juq-743" {
		t.Errorf("release redirect = %q %v %v", slug, ok, err)
	}
	if _, ok, _ := s.ResolveMergedSlug(ctx, "release", "juq-743"); ok {
		t.Error("live slug redirected")
	}
	if _, err = s.MergeCandidate(ctx, relCand, 10, "tester", ""); err == nil {
		t.Error("merging a decided candidate succeeded")
	}

	// Merge performer 2 into 1.
	perfMerge, err := s.MergeCandidate(ctx, perfCand, 0, "tester", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM performer_aliases WHERE performer_id=1 AND normalized_name IN ('hatano','波多野結衣')`); n != 2 {
		t.Errorf("aliases on target = %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM release_performers WHERE performer_id=2`); n != 0 {
		t.Errorf("merged performer still credited")
	}
	if _, err = s.PerformerDetail(ctx, "yui-hatano--src2"); err == nil {
		t.Error("merged performer still readable")
	}
	if slug, ok, _ := s.ResolveMergedSlug(ctx, "performer", "yui-hatano--src2"); !ok || slug != "yui-hatano" {
		t.Errorf("performer redirect = %q", slug)
	}
	if list, err := s.IdentityCandidates(ctx, "", 50); err != nil || len(list) != 0 {
		t.Errorf("pending candidates = %v %v", list, err)
	}

	// Audit rows are append-only.
	if _, err = s.Exec(ctx, `UPDATE review_events SET reason='x' WHERE id=$1`, perfMerge.ReviewEventID); err == nil {
		t.Error("review_events UPDATE succeeded")
	}
	if _, err = s.Exec(ctx, `DELETE FROM review_events WHERE id=$1`, perfMerge.ReviewEventID); err == nil {
		t.Error("review_events DELETE succeeded")
	}

	// Undo in reverse order restores everything.
	if _, err = s.UndoMerge(ctx, perfMerge.ReviewEventID, "tester", "wrong person"); err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	if _, err = s.UndoMerge(ctx, perfMerge.ReviewEventID, "tester", ""); !errors.As(err, &ce) {
		t.Errorf("second undo: %v", err)
	}
	if _, err = s.PerformerDetail(ctx, "yui-hatano--src2"); err != nil {
		t.Errorf("restored performer unreadable: %v", err)
	}
	if n := count(`SELECT count(*) FROM performer_aliases WHERE performer_id=1`); n != 0 {
		t.Errorf("target kept %d aliases after undo", n)
	}
	if n := count(`SELECT count(*) FROM performer_aliases WHERE performer_id=2`); n != 1 {
		t.Errorf("source aliases not restored")
	}
	if _, ok, _ := s.ResolveMergedSlug(ctx, "performer", "yui-hatano--src2"); ok {
		t.Error("redirect survived undo")
	}
	if _, err = s.UndoMerge(ctx, relMerge.ReviewEventID, "tester", ""); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]int{
		`SELECT count(*) FROM release_tags WHERE release_id=11`:                                       2,
		`SELECT count(*) FROM release_tags WHERE release_id=10`:                                       1,
		`SELECT count(*) FROM favorites WHERE user_id=1`:                                              2,
		`SELECT count(*) FROM release_performers WHERE release_id=11`:                                 2,
		`SELECT count(*) FROM release_performers WHERE release_id=10`:                                 2,
		`SELECT count(*) FROM release_identifiers WHERE release_id=11`:                                1,
		`SELECT count(*) FROM source_records WHERE release_id=11`:                                     1,
		`SELECT count(*) FROM releases WHERE id=11 AND merged_into_id IS NULL AND status='published'`: 1,
		`SELECT count(*) FROM review_events WHERE action IN ('merge','undo_merge')`:                   4,
		`SELECT count(*) FROM identity_candidates WHERE status='split'`:                               2,
	} {
		if n := count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	// Split pairs are not suggested again.
	if gen, err = s.GenerateIdentityCandidates(ctx, ""); err != nil || gen["performer"].Inserted+gen["release"].Inserted != 0 {
		t.Errorf("regenerated after split: %+v %v", gen, err)
	}

	// Not-duplicate on a fresh pair.
	exec(`INSERT INTO performers(id,slug,canonical_name) VALUES(20,'ai','Ai Uehara'),(21,'ai--2','ai uehara')`)
	if _, err = s.GenerateIdentityCandidates(ctx, "performer"); err != nil {
		t.Fatal(err)
	}
	var nd int64
	if err = s.QueryRow(ctx, `SELECT id FROM identity_candidates WHERE left_id=20 AND right_id=21`).Scan(&nd); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MarkNotDuplicate(ctx, nd, "tester", "different people"); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM not_duplicate_pairs WHERE entity_type='performer' AND a_id=20 AND b_id=21 AND review_event_id IS NOT NULL`); n != 1 {
		t.Error("not-duplicate pair not stored")
	}

	// A split pair can be merged again from a fresh candidate, and ingest that
	// resolves the merged entity (by slug) lands on the surviving one.
	if _, err = s.MergeCandidate(ctx, freshCandidate(t, s, "release", 11, 10), 10, "tester", ""); err != nil {
		t.Fatal(err)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for id, want := range map[int64]int64{11: 10, 10: 10, 3: 3} {
		table := "releases"
		if id == 3 {
			table = "performers"
		}
		if got, err := survivingID(ctx, tx, table, id); err != nil || got != want {
			t.Errorf("survivingID(%s,%d) = %d %v, want %d", table, id, got, err, want)
		}
	}
}

func freshCandidate(t *testing.T, s *Store, typ string, a, b int64) int64 {
	t.Helper()
	var id int64
	if err := s.QueryRow(context.Background(), `INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence) VALUES($1,$2,$3,0.9,'{}') RETURNING id`, typ, a, b).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
