package store

import (
	"avxxx/internal/completeness"
	"avxxx/internal/series"
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

// TestSeriesEvidenceCompletenessIntegration covers migration 027 against a
// real, disposable PostgreSQL (make test-integration): the series backfill
// and trigger (slugs, Japanese-only fallback, collisions, re-runs), the
// public series queries, the release-detail series, source evidence
// (overrides without reasons, hidden birthdays, no raw_data) and the
// completeness facts, report and snapshots. Skipped unless
// AVXXX_TEST_DATABASE_URL is set; assertions are scoped to its own rows.
func TestSeriesEvidenceCompletenessIntegration(t *testing.T) {
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
	defer func() { s.Close() }()
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
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		return v
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
	en := "Summer Diary " + sfx         // ASCII series name
	ja := "夏の日記シリーズ" + sfx[len(sfx)-6:] // Japanese name with digits: slug base is the digits
	jaOnly := "純愛シリーズ"                  // Japanese-only: fallback slug
	makerA, makerB := "Maker A "+sfx, "Maker B "+sfx

	// A release written before migration 027 (backfill path): inserted with
	// the trigger disabled, so no series row exists until the migrations re-run.
	exec(`ALTER TABLE releases DISABLE TRIGGER releases_series_sync`)
	pre := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status,release_date) VALUES($1,'Pre Vol.2',$2,$3,'published','2024-02-01') RETURNING id`, "pre-"+sfx, en, makerA)
	exec(`ALTER TABLE releases ENABLE TRIGGER releases_series_sync`)
	if n := id(`SELECT count(*) FROM series WHERE name=$1`, en); n != 0 {
		t.Fatalf("series created with the trigger disabled: %d", n)
	}
	s.Close()
	for range 2 { // 027 must be re-runnable and must backfill
		if s, err = Open(ctx); err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if s, err = Open(ctx); err != nil {
		t.Fatal(err)
	}
	seriesEN := id(`SELECT series_id FROM releases WHERE id=$1`, pre)
	if got, want := text(`SELECT slug FROM series WHERE id=$1`, seriesEN), series.SlugBase(en); got != want {
		t.Errorf("backfilled slug %q, want Go SlugBase %q", got, want)
	}

	// Trigger path: inserts and updates assign series_id.
	var rels []int64
	for i, title := range []string{"Diary Vol.1", "Diary Vol.3", "Diary Special"} {
		rels = append(rels, id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status,release_date) VALUES($1,$2,$3,$4,'published',$5) RETURNING id`,
			fmt.Sprintf("sd%d-%s", i, sfx), title, "  "+en+" ", makerA, fmt.Sprintf("2024-0%d-15", i+3)))
	}
	draft := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status) VALUES($1,'Draft Vol.9',$2,$3,'draft') RETURNING id`, "draft-"+sfx, en, makerA)
	for _, r := range append(rels, draft) {
		if got := id(`SELECT series_id FROM releases WHERE id=$1`, r); got != seriesEN {
			t.Errorf("release %d series_id %d, want %d", r, got, seriesEN)
		}
	}
	// Same name, other maker: a distinct series whose slug gets the --<id> suffix.
	other := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status) VALUES($1,'Other',$2,$3,'published') RETURNING id`, "other-"+sfx, en, makerB)
	otherSeries := id(`SELECT series_id FROM releases WHERE id=$1`, other)
	if otherSeries == seriesEN || text(`SELECT slug FROM series WHERE id=$1`, otherSeries) != fmt.Sprintf("%s--%d", series.SlugBase(en), otherSeries) {
		t.Errorf("other maker series %d slug %q", otherSeries, text(`SELECT slug FROM series WHERE id=$1`, otherSeries))
	}
	// Japanese-only names: series-<id> (or series-<id>--<id> if an earlier run took it).
	jr := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status) VALUES($1,'純愛 第2話',$2,$3,'published') RETURNING id`, "ja-"+sfx, jaOnly, makerA+"x")
	js := id(`SELECT series_id FROM releases WHERE id=$1`, jr)
	if slug := text(`SELECT slug FROM series WHERE id=$1`, js); !strings.HasPrefix(slug, fmt.Sprintf("series-%d", js)) {
		t.Errorf("japanese-only slug %q", slug)
	}
	if nameJA := text(`SELECT name_ja FROM series WHERE id=$1`, js); nameJA != jaOnly {
		t.Errorf("name_ja %q", nameJA)
	}
	jm := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status) VALUES($1,'x',$2,$3,'published') RETURNING id`, "jm-"+sfx, ja, makerA)
	if got, want := text(`SELECT s.slug FROM series s JOIN releases r ON r.series_id=s.id WHERE r.id=$1`, jm), series.SlugBase(ja); got != want {
		t.Errorf("mixed slug %q, want %q", got, want)
	}
	// Clearing series_name clears series_id; renaming moves it.
	exec(`UPDATE releases SET series_name=NULL WHERE id=$1`, other)
	if text(`SELECT series_id::text FROM releases WHERE id=$1`, other) != "<nil>" {
		t.Error("series_id kept after clearing series_name")
	}
	if n := id(`SELECT count(*) FROM series WHERE name=$1`, en); n != 2 {
		t.Errorf("series rows for %q: %d", en, n)
	}

	// ---- public series queries
	slugEN := text(`SELECT slug FROM series WHERE id=$1`, seriesEN)
	page, _, err := s.SeriesPage(ctx, SeriesFilter{Q: sfx, Sort: "works"})
	must(err)
	var found *SeriesListItem
	for i := range page {
		if page[i].ID == seriesEN {
			found = &page[i]
		}
		if page[i].ID == otherSeries {
			t.Error("series without published releases listed")
		}
	}
	if found == nil || found.ReleaseCount != 4 || found.FirstReleaseDate == nil || found.FirstReleaseDate.Format(time.DateOnly) != "2024-02-01" {
		t.Fatalf("series page: %+v", page)
	}
	p1, next, err := s.SeriesPage(ctx, SeriesFilter{Sort: "name", Limit: 1})
	must(err)
	if len(p1) != 1 || next == "" {
		t.Errorf("series paging: %+v %q", p1, next)
	}
	c, _ := DecodeCursor(next)
	p2, _, err := s.SeriesPage(ctx, SeriesFilter{Sort: "name", Limit: 1, Cursor: &c})
	must(err)
	if len(p2) != 1 || p2[0].ID == p1[0].ID {
		t.Errorf("series page 2: %+v", p2)
	}
	perf := id(`INSERT INTO performers(slug,canonical_name) VALUES($1,'Series Star') RETURNING id`, "star-"+sfx)
	for _, r := range rels[:2] {
		exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, r, perf)
	}
	exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, draft, perf)
	d, err := s.SeriesDetailBySlug(ctx, slugEN, 5)
	must(err)
	if d.ReleaseCount != 4 || len(d.Makers) != 1 || d.Makers[0].Name != makerA || len(d.TopPerformers) != 1 || d.TopPerformers[0].ReleaseCount != 2 {
		t.Errorf("series detail: %+v", d)
	}
	if _, err = s.SeriesDetailBySlug(ctx, text(`SELECT slug FROM series WHERE id=$1`, otherSeries), 5); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("empty series detail: %v", err)
	}
	srs, err := s.SeriesReleases(ctx, slugEN)
	must(err)
	if len(srs) != 4 {
		t.Fatalf("series releases: %d", len(srs))
	}
	items := make([]series.Item, len(srs))
	for i, x := range srs {
		items[i] = series.Item{ID: x.ID, Title: x.CanonicalTitle, Code: x.Slug, ReleaseDate: x.ReleaseDate}
	}
	ordered, basis := series.Order(items, series.SortEpisode)
	if basis != series.BasisTitle || ordered[0].ID != rels[0] || ordered[1].ID != pre || ordered[2].ID != rels[1] || ordered[3].ID != rels[2] {
		t.Errorf("episode order (%s): %+v", basis, ordered)
	}
	rd, err := s.ReleaseDetailBySlug(ctx, "sd0-"+sfx)
	must(err)
	if rd.Series == nil || rd.Series.Slug != slugEN || rd.Series.ID != seriesEN {
		t.Errorf("release detail series: %+v", rd.Series)
	}

	// ---- evidence
	official := id(`INSERT INTO sources(name,url,source_kind,priority) VALUES('ev-official','https://ev-o.test/'||$1,'official_catalog',10) RETURNING id`, sfx)
	agg := id(`INSERT INTO sources(name,url,source_kind) VALUES('ev-agg','https://ev-a.test/'||$1,'aggregator') RETURNING id`, sfx)
	canary := "RAWCANARY-" + sfx
	rec := func(src int64, entity string, entityID int64, key string) int64 {
		col := "release_id"
		if entity == "performer" {
			col = "performer_id"
		}
		return id(`INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,`+col+`,raw_data,parser_version,resolution_status)
			VALUES($1,$2,$3,$4,$5,$6::jsonb,'t','resolved') RETURNING id`, src, entity, key+sfx, "https://src.test/"+key+sfx, entityID,
			fmt.Sprintf(`{"secret":%q,"title":"x"}`, canary))
	}
	val := func(rec int64, field, value string) {
		exec(`INSERT INTO source_field_values(source_record_id,field_name,raw_value,normalized_value,captured_at) VALUES($1,$2,$3::jsonb,$3::jsonb,now())`, rec, field, value)
	}
	r0 := rels[0]
	ro, ra := rec(official, "release", r0, "ro"), rec(agg, "release", r0, "ra")
	val(ro, "title", `"Diary Vol.1"`)
	val(ra, "title", `"Diary Vol 1"`)
	val(ra, "maker", fmt.Sprintf("%q", makerA))
	val(ra, "secret", fmt.Sprintf("%q", canary))
	val(ro, "director", `{"nested":"object"}`)
	exec(`INSERT INTO release_overrides(release_id,field_name,value,reason,actor) VALUES($1,'director_name','"Dir"','PRIVATE-REASON-'||$2,'PRIVATE-ACTOR')`, r0, sfx)
	ev, err := s.ReleaseEvidence(ctx, "sd0-"+sfx)
	must(err)
	b, _ := json.Marshal(ev)
	for _, leak := range []string{canary, "raw_data", "PRIVATE-REASON", "PRIVATE-ACTOR", "nested"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("release evidence leaks %q: %s", leak, b)
		}
	}
	fieldOf := func(name string) map[string]any {
		var m struct {
			Fields []map[string]any `json:"fields"`
		}
		json.Unmarshal(b, &m)
		for _, f := range m.Fields {
			if f["field"] == name {
				return f
			}
		}
		return nil
	}
	title := fieldOf("canonical_title")
	if pb, _ := title["provided_by"].(map[string]any); pb == nil || pb["source"] != "ev-official" || len(title["sources"].([]any)) != 2 {
		t.Errorf("title evidence: %v", title)
	}
	if dir := fieldOf("director_name"); dir["overridden"] != true || dir["provided_by"].(map[string]any)["source"] != "manual_override" {
		t.Errorf("override evidence: %v", dir)
	}
	if len(ev.Sources) != 2 || ev.Sources[0].Name != "ev-official" {
		t.Errorf("evidence sources: %+v", ev.Sources)
	}
	if _, err = s.ReleaseEvidence(ctx, "draft-"+sfx); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("draft evidence: %v", err)
	}
	exec(`UPDATE performers SET birth_date='1999-09-09',birth_date_hidden=true,height_cm=160 WHERE id=$1`, perf)
	pr := rec(agg, "performer", perf, "pp")
	val(pr, "birth_date", `"1999-09-09"`)
	val(pr, "height_cm", `"160"`)
	pev, err := s.PerformerEvidence(ctx, "star-"+sfx)
	must(err)
	pb, _ := json.Marshal(pev)
	if strings.Contains(string(pb), "1999-09-09") || strings.Contains(string(pb), `"birth_date"`) || strings.Contains(string(pb), canary) {
		t.Errorf("performer evidence leaks the hidden birthday or payload: %s", pb)
	}
	if !strings.Contains(string(pb), `"height_cm"`) {
		t.Errorf("performer evidence lacks height: %s", pb)
	}

	// ---- completeness (scoped to this test's source)
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value,identifier_scope) VALUES($1,'dvd_code',$2,$3,'global')`, r0, "EVX-"+sfx[len(sfx)-6:], "evx-"+sfx[len(sfx)-6:])
	rep, err := s.CompletenessReport(ctx, completeness.Options{Entity: completeness.EntityRelease, Source: "ev-official", GroupBy: completeness.GroupSource})
	must(err)
	if rep.Total != 1 || len(rep.Groups) == 0 {
		t.Fatalf("report: %+v", rep)
	}
	stat := func(fs []completeness.FieldStat, name string) completeness.FieldStat {
		for _, f := range fs {
			if f.Field == name {
				return f
			}
		}
		return completeness.FieldStat{}
	}
	if stat(rep.Fields, "product_code").Present != 1 || stat(rep.Fields, "performers").Present != 1 || stat(rep.Fields, "cover").Present != 0 || stat(rep.Fields, "series").Present != 1 {
		t.Errorf("report fields: %+v", rep.Fields)
	}
	var g completeness.Group
	for _, x := range rep.Groups {
		if x.SourceID == official {
			g = x
		}
	}
	if sp := stat(g.Fields, "canonical_title").SourceProvided; sp == nil || *sp != 1 {
		t.Errorf("source provided: %+v", g)
	}
	prep, err := s.CompletenessReport(ctx, completeness.Options{Entity: completeness.EntityPerformer, Source: fmt.Sprint(agg)})
	must(err)
	if prep.Total != 1 || stat(prep.Fields, "birth_date").Present != 1 || stat(prep.Fields, "works").Present != 1 {
		t.Errorf("performer report: %+v", prep)
	}
	day := time.Date(2001, 1, 2, 0, 0, 0, 0, time.UTC) // far in the past: never collides with real runs
	must(s.SaveCompletenessSnapshot(ctx, completeness.Snapshot{Date: day, Entity: completeness.EntityRelease, Total: 4, RequiredComplete: 1, Fields: map[string]int64{"cover": 2}, Sources: map[string]int64{}}))
	must(s.SaveCompletenessSnapshot(ctx, completeness.Snapshot{Date: day, Entity: completeness.EntityRelease, Total: 5, RequiredComplete: 2, Fields: map[string]int64{"cover": 3}, Sources: map[string]int64{}}))
	if n := id(`SELECT total FROM completeness_snapshots WHERE snapshot_date=$1 AND entity_type='release'`, day); n != 5 {
		t.Errorf("snapshot upsert: total %d", n)
	}
	snaps, err := s.CompletenessSnapshots(ctx, completeness.EntityRelease, MaxSeriesReleases)
	must(err)
	if len(snaps) == 0 || snaps[len(snaps)-1].Date.Format(time.DateOnly) != "2001-01-02" || snaps[len(snaps)-1].Fields["cover"] != 3 {
		t.Errorf("snapshots: %+v", snaps)
	}
	exec(`DELETE FROM completeness_snapshots WHERE snapshot_date=$1`, day)
	run, err := (&completeness.Runner{Store: s}).Run(ctx)
	must(err)
	if run.Entities[completeness.EntityRelease] < 5 {
		t.Errorf("runner: %+v", run)
	}
}
