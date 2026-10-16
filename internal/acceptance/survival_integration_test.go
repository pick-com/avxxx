package acceptance

import (
	"avxxx/internal/catalog"
	"avxxx/internal/store"
	"encoding/json"
	"fmt"
	"testing"
)

// TestAdminDecisionsSurviveRecrawlIntegration applies every kind of admin
// decision to a crawled catalog — field overrides (title, tags, birth date,
// height), a hidden birthday, held and unpublished releases, an identity
// merge and an entity split — then re-crawls every source (baseline,
// incremental, noisy re-ingest, plain again) and requires the catalog to be
// exactly what the admin left (FINAL_DATA_DESIGN 6, 11: 本站人工字段不会被后续
// 采集覆盖; 合并、拆分 … 都有审计记录).
func TestAdminDecisionsSurviveRecrawlIntegration(t *testing.T) {
	e := newEnv(t)
	c := e.newCatalog(t)
	e.crawlAll(c, catalog.PhaseBaseline)
	e.crawlAll(c, catalog.PhaseIncremental)
	ctx := e.ctx
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := func(m map[string]any) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		for k, v := range m {
			b, err := json.Marshal(v)
			must(err)
			out[k] = b
		}
		return out
	}
	releaseID := func(slug string) int64 { return e.int64(`SELECT id FROM releases WHERE slug=$1`, slug) }
	performerID := func(slug string) int64 { return e.int64(`SELECT id FROM performers WHERE slug=$1`, slug) }
	recordID := func(source, key string) int64 {
		return e.int64(`SELECT sr.id FROM source_records sr JOIN sources s ON s.id=sr.source_id WHERE s.name=$1 AND sr.source_record_key=$2`, source, key)
	}

	// Overrides through the admin API's store paths.
	rp, err := store.ParseReleasePatch(raw(map[string]any{"title": "Admin Title", "tags": []string{"Admin Tag"}, "reason": overrideNote}))
	must(err)
	_, err = e.s.PatchRelease(ctx, releaseID("juq-743"), rp, overrideActor)
	must(err)
	pp, err := store.ParsePerformerPatch(raw(map[string]any{"birth_date": "1991-02-03", "height_cm": 161, "reason": overrideNote}))
	must(err)
	_, err = e.s.PatchPerformer(ctx, performerID("yurine-tsukino"), pp, overrideActor)
	must(err)
	hp, err := store.ParsePerformerPatch(raw(map[string]any{"birth_date_hidden": true, "reason": overrideNote}))
	must(err)
	_, err = e.s.PatchPerformer(ctx, performerID("jane-doe"), hp, overrideActor)
	must(err)
	// Release lifecycle decisions.
	e.exec(`UPDATE releases SET status='held_review' WHERE slug='abc-002'`)
	e.exec(`UPDATE releases SET status='unpublished' WHERE slug='abc-003'`)
	e.exec(`UPDATE releases SET status='published' WHERE slug IN ('juq-743','abc-001')`)

	// Merge: DMM's ジェーン・ドウ is Jane Doe.
	jane, jeen := performerID("jane-doe"), performerID("jeendou")
	cand := e.int64(`INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence,status) VALUES('performer',$1,$2,0.9,'{}','pending') RETURNING id`, jane, jeen)
	_, err = e.s.MergeCandidate(ctx, cand, jane, overrideActor, overrideNote)
	must(err)

	// Split: XCITY's 山田花子 (record 1001 and the credit on title 5001) is
	// not javdatabase's Hanako Yamada.
	hanako := performerID("hanako-yamada")
	split, err := e.s.SplitEntity(ctx, store.SplitRequest{EntityType: "performer", EntityID: hanako,
		SourceRecordIDs: []int64{recordID("xcity-fixture", "1001"), recordID("xcity-fixture", "5001")},
		Version:         e.int64(`SELECT version FROM performers WHERE id=$1`, hanako), Actor: overrideActor, Reason: overrideNote})
	must(err)

	expect := func(when string) {
		t.Helper()
		for _, q := range []struct{ what, sql, want string }{
			{"juq-743 title (override)", `SELECT canonical_title FROM releases WHERE slug='juq-743'`, "Admin Title"},
			{"juq-743 tags (override)", `SELECT string_agg(normalized_tag,',' ORDER BY normalized_tag) FROM release_tags t JOIN releases r ON r.id=t.release_id WHERE r.slug='juq-743'`, "admin-tag"},
			{"yurine birth_date (override)", `SELECT birth_date::text FROM performers WHERE slug='yurine-tsukino'`, "1991-02-03"},
			{"yurine height (override)", `SELECT height_cm::int::text FROM performers WHERE slug='yurine-tsukino'`, "161"},
			{"jane-doe birthday hidden", `SELECT birth_date_hidden::text FROM performers WHERE slug='jane-doe'`, "true"},
			{"abc-002 held", `SELECT status FROM releases WHERE slug='abc-002'`, "held_review"},
			{"abc-003 unpublished", `SELECT status FROM releases WHERE slug='abc-003'`, "unpublished"},
			{"abc-001 published", `SELECT status FROM releases WHERE slug='abc-001'`, "published"},
			{"jeendou merged into jane-doe", `SELECT m.slug FROM performers p JOIN performers m ON m.id=p.merged_into_id WHERE p.slug='jeendou'`, "jane-doe"},
			{"DMM actress record on jane-doe", `SELECT p.slug FROM source_records sr JOIN performers p ON p.id=sr.performer_id WHERE sr.source_record_key='dmm-actress-2001'`, "jane-doe"},
			{"DMM credit of ジェーン・ドウ on jane-doe", `SELECT string_agg(p.slug,',') FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id WHERE r.slug='abc-001' AND p.merged_into_id IS NULL AND p.slug<>'hanako-yamada'`, "jane-doe"},
			{"XCITY 1001 on the split-off performer", `SELECT p.slug FROM source_records sr JOIN sources s ON s.id=sr.source_id JOIN performers p ON p.id=sr.performer_id WHERE s.name='xcity-fixture' AND sr.source_record_key='1001'`, split.NewSlug},
			{"javdatabase Hanako on the original", `SELECT p.slug FROM source_records sr JOIN sources s ON s.id=sr.source_id JOIN performers p ON p.id=sr.performer_id WHERE s.name='javdatabase-fixture' AND sr.source_record_key='hanako-yamada'`, "hanako-yamada"},
			{"XCITY title credits the split-off performer", `SELECT string_agg(p.slug,',') FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id WHERE r.slug='summer-story-xc-source-5001'`, split.NewSlug},
			{"abc-001 still credits Hanako Yamada", `SELECT count(*)::text FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE r.slug='abc-001' AND rp.performer_id=` + fmt.Sprint(hanako), "1"},
		} {
			if got := e.str(q.sql); got != q.want {
				t.Errorf("%s: %s = %q, want %q", when, q.what, got, q.want)
			}
		}
	}
	expect("after the admin decisions")
	before := e.snap()

	// The first re-crawl may still settle canonical fields of the merged
	// performer (the field policy now sees both sources' evidence) but must
	// not add or drop rows; from then on nothing may change at all.
	e.crawlAll(c, catalog.PhaseBaseline)
	expect("after the first re-crawl")
	if d := diffCounts(before, e.snap(), identityTables); d != "" {
		t.Errorf("first re-crawl changed row counts:%s", d)
	}
	if d := diffRows(before, e.snap(), []string{"releases", "credits", "release_identifiers", "release_tags", "release_overrides", "performer_overrides"}); d != "" {
		t.Errorf("first re-crawl changed release values:%s", d)
	}
	admin := e.snap()
	t.Logf("settled after admin decisions: %s", admin.counts(identityTables))

	for _, st := range []struct {
		name string
		v    variant
		run  func()
	}{
		{"incremental", variant{}, func() { e.crawlAll(c, catalog.PhaseIncremental) }},
		{"noisy re-ingest", variant{noisy: true}, func() { e.crawlAll(c, catalog.PhaseBaseline) }},
		{"plain again", variant{}, func() { e.crawlAll(c, catalog.PhaseBaseline) }},
	} {
		c.load(st.v)
		st.run()
		expect("after " + st.name)
		if d := diffCounts(admin, e.snap(), identityTables); d != "" {
			t.Errorf("%s changed row counts:%s", st.name, d)
		}
	}
	if d := diffRows(admin, e.snap(), entityTables); d != "" {
		t.Errorf("re-crawls changed what the admin left:%s", d)
	}
	if n := e.int64(`SELECT count(*) FROM review_events WHERE action IN ('merge','split','edit')`); n < 5 {
		t.Errorf("review_events has %d merge/split/edit rows, want >= 5 (audit trail)", n)
	}
}
