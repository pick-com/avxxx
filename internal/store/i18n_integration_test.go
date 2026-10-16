package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/i18n"
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestI18nIntegration exercises migration 015 and the translation, stale,
// glossary and UI-string queries against a real, disposable PostgreSQL
// database. It is skipped unless AVXXX_TEST_DATABASE_URL is set; the
// database is modified (rows are namespaced by a unique suffix).
func TestI18nIntegration(t *testing.T) {
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
	if s, err = Open(ctx); err != nil { // migrations must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	// SQL i18n_hash must equal Go i18n.SourceHash.
	for _, v := range []string{"", "abc", "元のタイトル 🎬"} {
		var h string
		must(s.QueryRow(ctx, `SELECT i18n_hash($1)`, v).Scan(&h))
		if h != i18n.SourceHash(v) {
			t.Errorf("i18n_hash(%q) = %s, Go %s", v, h, i18n.SourceHash(v))
		}
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	var sourceID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('i18n','https://i18n.test/'||$1) RETURNING id`, sfx).Scan(&sourceID))
	code := "I18N-" + sfx[len(sfx)-9:]
	tag := "tag" + sfx
	relRec := func(title string) catalog.Record {
		return catalog.Record{EntityType: catalog.Release, SourceKey: "rel-" + sfx, URL: "https://i18n.test/r/" + sfx,
			Fields: map[string]any{"title": title, "dvd_code": code, "tags": []string{tag}}}
	}
	if _, err = s.SaveCatalogRecord(ctx, sourceID, "t1", relRec("Original Title")); err != nil {
		t.Fatal(err)
	}
	var relID int64
	must(s.QueryRow(ctx, `SELECT release_id FROM release_identifiers WHERE identifier_value=$1`, code).Scan(&relID))
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(s.Exec(ctx, `UPDATE releases SET synopsis='あらすじ',status='published' WHERE id=$1`, relID))
	var slug string
	must(s.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, relID).Scan(&slug))
	if d, err := s.ReleaseDetailBySlug(ctx, slug); err != nil || d.Synopsis != "あらすじ" || len(d.Tags) != 1 {
		t.Errorf("release detail %+v %v", d, err)
	}
	var perfID int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,biography) VALUES($1,'Perf','bio') RETURNING id`, "perf-"+sfx).Scan(&perfID))
	rid, pid := strconv.FormatInt(relID, 10), strconv.FormatInt(perfID, 10)
	title := i18n.Ref{EntityType: "release", EntityID: rid, Field: "title"}
	synopsis := i18n.Ref{EntityType: "release", EntityID: rid, Field: "synopsis"}
	pname := i18n.Ref{EntityType: "performer", EntityID: pid, Field: "name"}
	tagRef := i18n.Ref{EntityType: "tag", EntityID: tag, Field: "name"}

	// i18n_source resolves each kind, NULL for unknown.
	for ref, want := range map[i18n.Ref]string{title: "Original Title", synopsis: "あらすじ", pname: "Perf", tagRef: tag} {
		var got *string
		must(s.QueryRow(ctx, `SELECT i18n_source($1,$2,$3)`, ref.EntityType, ref.EntityID, ref.Field).Scan(&got))
		if got == nil || *got != want {
			t.Errorf("i18n_source(%v) = %v, want %q", ref, got, want)
		}
	}
	var none *string
	must(s.QueryRow(ctx, `SELECT i18n_source('release','x','title')`).Scan(&none))
	if none != nil {
		t.Errorf("non-numeric release id gave %v", *none)
	}

	// Missing tasks list our fields; upsert of a non-existent entity is 404.
	tasks, _, err := s.TranslationTasks(ctx, TaskFilter{Lang: "zh-TW", Missing: true, EntityType: "release", Limit: 100})
	must(err)
	if !hasTask(tasks, title, "missing") {
		// The page may be full of older rows; walk until found.
		if !findTask(t, s, TaskFilter{Lang: "zh-TW", Missing: true, EntityType: "release"}, title, "missing") {
			t.Errorf("title not listed as missing")
		}
	}
	if _, err = s.UpsertTranslation(ctx, i18n.Ref{EntityType: "release", EntityID: "999999999999", Field: "title"}, "en", "x", ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("upsert of missing release: %v", err)
	}

	// Draft is not public.
	tt, err := s.UpsertTranslation(ctx, title, "zh-CN", "简体标题", "ed")
	must(err)
	if tt.Status != "draft" || tt.Submitted || tt.Outdated || tt.UpdatedBy == nil || *tt.UpdatedBy != "ed" || tt.Source == nil || *tt.Source != "Original Title" {
		t.Errorf("upsert task %+v", tt)
	}
	if set := public(t, s, "zh-TW", title); len(set) != 0 {
		t.Errorf("draft served publicly: %v", set)
	}
	// Review before submit is refused; submit then review publishes.
	if _, err = s.TransitionTranslation(ctx, title, "zh-CN", i18n.ActionReview, ""); !errors.Is(err, i18n.ErrTransition) {
		t.Errorf("review unsubmitted: %v", err)
	}
	if _, err = s.TransitionTranslation(ctx, title, "en", i18n.ActionSubmit, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("submit missing row: %v", err)
	}
	tt, err = s.TransitionTranslation(ctx, title, "zh-CN", i18n.ActionSubmit, "")
	must(err)
	if !tt.Submitted || tt.Status != "draft" {
		t.Errorf("submitted %+v", tt)
	}
	tt, err = s.TransitionTranslation(ctx, title, "zh-CN", i18n.ActionReview, "rev")
	must(err)
	if tt.Status != "reviewed" || tt.ReviewedValue == nil || *tt.ReviewedValue != "简体标题" || tt.Submitted || *tt.UpdatedBy != "rev" {
		t.Errorf("reviewed %+v", tt)
	}
	if _, err = s.TransitionTranslation(ctx, title, "zh-CN", i18n.ActionReview, ""); !errors.Is(err, i18n.ErrTransition) {
		t.Errorf("double review: %v", err)
	}

	// Publish more rows: tag in en, performer in zh-TW, synopsis in ja.
	publish := func(ref i18n.Ref, lang, v string) {
		t.Helper()
		_, err := s.UpsertTranslation(ctx, ref, lang, v, "")
		must(err)
		_, err = s.TransitionTranslation(ctx, ref, lang, i18n.ActionSubmit, "")
		must(err)
		_, err = s.TransitionTranslation(ctx, ref, lang, i18n.ActionReview, "")
		must(err)
	}
	publish(tagRef, "en", "Tag EN")
	publish(pname, "zh-TW", "演員")
	publish(synopsis, "ja", "日本語あらすじ")

	// One batched read serves the fallback chain.
	set := public(t, s, "ko", title, synopsis, pname, tagRef)
	chain := i18n.FallbackChain("ko")
	if v, l := set.Resolve(title, chain, "Original Title"); v != "简体标题" || l != "zh-CN" {
		t.Errorf("title -> %q %q", v, l)
	}
	if v, l := set.Resolve(pname, chain, "Perf"); v != "演員" || l != "zh-TW" {
		t.Errorf("performer -> %q %q", v, l)
	}
	if v, l := set.Resolve(tagRef, chain, tag); v != "Tag EN" || l != "en" {
		t.Errorf("tag -> %q %q", v, l)
	}
	if v, l := set.Resolve(synopsis, chain, "あらすじ"); v != "あらすじ" || l != i18n.Original {
		t.Errorf("synopsis (ja only) for ko -> %q %q", v, l)
	}

	// Editing a reviewed translation keeps the reviewed text public.
	_, err = s.UpsertTranslation(ctx, pname, "zh-TW", "新演員名", "")
	must(err)
	if v, _ := public(t, s, "zh-TW", pname).Resolve(pname, []string{"zh-TW"}, ""); v != "演員" {
		t.Errorf("draft edit replaced public text: %q", v)
	}

	// Re-ingest with a changed title marks the reviewed title stale; it stays public.
	if _, err = s.SaveCatalogRecord(ctx, sourceID, "t1", relRec("Original Title")); err != nil {
		t.Fatal(err)
	}
	if st := status(t, s, title, "zh-CN"); st != "reviewed" {
		t.Errorf("unchanged ingest made title %s", st)
	}
	if _, err = s.SaveCatalogRecord(ctx, sourceID, "t1", relRec("Changed Title")); err != nil {
		t.Fatal(err)
	}
	if st := status(t, s, title, "zh-CN"); st != "stale" {
		t.Errorf("changed title: status %s, want stale", st)
	}
	if v, l := public(t, s, "zh-CN", title).Resolve(title, []string{"zh-CN"}, ""); v != "简体标题" || l != "zh-CN" {
		t.Errorf("stale not served: %q %q", v, l)
	}
	tasks, _, err = s.TranslationTasks(ctx, TaskFilter{Lang: "zh-CN", Status: "stale", EntityType: "release", Limit: 100})
	must(err)
	if !findTask(t, s, TaskFilter{Lang: "zh-CN", Status: "stale", EntityType: "release"}, title, "stale") {
		t.Errorf("stale title not in stale task list")
	}
	// Re-confirming a stale translation records the new source hash.
	tt, err = s.TransitionTranslation(ctx, title, "zh-CN", i18n.ActionReview, "")
	must(err)
	if tt.Status != "reviewed" || tt.Outdated {
		t.Errorf("re-review %+v", tt)
	}

	// A submitted draft reviewed after the source moved on lands as stale.
	_, err = s.UpsertTranslation(ctx, synopsis, "en", "Synopsis EN", "")
	must(err)
	_, err = s.TransitionTranslation(ctx, synopsis, "en", i18n.ActionSubmit, "")
	must(err)
	must2(s.Exec(ctx, `UPDATE releases SET synopsis='新しいあらすじ' WHERE id=$1`, relID))
	tt, err = s.TransitionTranslation(ctx, synopsis, "en", i18n.ActionReview, "")
	must(err)
	if tt.Status != "stale" || !tt.Outdated || *tt.ReviewedValue != "Synopsis EN" {
		t.Errorf("late review %+v", tt)
	}

	// Admin edits outside the ingest path: the sweep catches them; ResolveConflict marks inline.
	must2(s.Exec(ctx, `UPDATE release_tags SET tag='Renamed' WHERE normalized_tag=$1`, tag))
	n, err := s.MarkStaleTranslations(ctx)
	must(err)
	if n < 2 || status(t, s, tagRef, "en") != "stale" || status(t, s, synopsis, "ja") != "stale" {
		t.Errorf("sweep marked %d; tag %s synopsis/ja %s", n, status(t, s, tagRef, "en"), status(t, s, synopsis, "ja"))
	}
	publish(pname, "en", "Perf EN")
	var conflictID int64
	must(s.QueryRow(ctx, `INSERT INTO field_conflicts(entity_type,entity_id,field_name,current_value,candidates) VALUES('performer',$1,'canonical_name','"Perf"','[]') RETURNING id`, perfID).Scan(&conflictID))
	must(s.ResolveConflict(ctx, conflictID, 0, []byte(`"Perf Renamed"`), "fix"))
	if st := status(t, s, pname, "en"); st != "stale" {
		t.Errorf("ResolveConflict: performer name/en %s, want stale", st)
	}

	// Pagination over tasks with the tuple cursor.
	var seen int
	f := TaskFilter{EntityType: "release", Limit: 1}
	for range 1000 {
		page, next, err := s.TranslationTasks(ctx, f)
		must(err)
		for _, x := range page {
			if x.EntityID == rid {
				seen++
			}
		}
		if next == "" {
			break
		}
		if f.Cursor, err = DecodeTaskCursor(next); err != nil {
			t.Fatal(err)
		}
	}
	if seen != 3 { // title/zh-CN, synopsis/ja, synopsis/en
		t.Errorf("paged tasks for release: %d, want 3", seen)
	}

	stats, err := s.TranslationStats(ctx)
	must(err)
	byLang := stats["by_lang"].(map[string]map[string]int64)
	if byLang["zh-CN"]["reviewed"] < 1 || byLang["en"]["stale"] < 2 || stats["total_fields"].(int64) < 4 {
		t.Errorf("stats %v", stats)
	}

	// Glossary CRUD.
	g, err := s.UpsertGlossaryTerm(ctx, GlossaryEntry{Term: "熟女" + sfx, Lang: "en", Translation: "mature"}, "ed")
	must(err)
	g2, err := s.UpsertGlossaryTerm(ctx, GlossaryEntry{Term: "熟女" + sfx, Lang: "en", Translation: "MILF", Note: "n"}, "")
	must(err)
	if g2.ID != g.ID || g2.Translation != "MILF" {
		t.Errorf("glossary upsert %+v -> %+v", g, g2)
	}
	other, err := s.UpsertGlossaryTerm(ctx, GlossaryEntry{Term: "熟女" + sfx, Lang: "ko", Translation: "숙녀"}, "")
	must(err)
	if _, err = s.UpdateGlossaryTerm(ctx, other.ID, GlossaryEntry{Term: "熟女" + sfx, Lang: "en", Translation: "x"}, ""); err == nil {
		t.Error("update into an existing (term, lang) must fail")
	}
	list, err := s.GlossaryTerms(ctx, "en", sfx, 10)
	must(err)
	if len(list) != 1 || list[0].ID != g.ID {
		t.Errorf("glossary list %+v", list)
	}
	must(s.DeleteGlossaryTerm(ctx, g.ID))
	if err = s.DeleteGlossaryTerm(ctx, g.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("double delete: %v", err)
	}
	if _, err = s.UpdateGlossaryTerm(ctx, g.ID, GlossaryEntry{Term: "a", Lang: "en", Translation: "b"}, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("update deleted: %v", err)
	}

	// UI strings: only reviewed ones are public.
	k1, k2 := "t"+sfx+".home", "t"+sfx+".draft"
	_, err = s.UpsertUIString(ctx, UIString{Key: k1, Lang: "th", Value: "หน้าแรก", Status: "reviewed"}, "")
	must(err)
	_, err = s.UpsertUIString(ctx, UIString{Key: k2, Lang: "th", Value: "ร่าง", Status: "draft"}, "")
	must(err)
	pub, err := s.PublicUIStrings(ctx, "th")
	must(err)
	if pub[k1] != "หน้าแรก" || pub[k2] != "" {
		t.Errorf("public ui %v", pub)
	}
	us, err := s.UIStrings(ctx, "th", "", "t"+sfx)
	must(err)
	if len(us) != 2 {
		t.Errorf("ui list %+v", us)
	}
	if _, err = s.Exec(ctx, `INSERT INTO ui_strings(key,lang,value) VALUES('x','fr','y')`); err == nil {
		t.Error("unsupported lang accepted by CHECK")
	}
}

func public(t *testing.T, s *Store, lang string, refs ...i18n.Ref) i18n.Set {
	t.Helper()
	set, err := s.PublicTranslations(context.Background(), i18n.FallbackChain(lang), refs)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func status(t *testing.T, s *Store, ref i18n.Ref, lang string) string {
	t.Helper()
	var st string
	if err := s.QueryRow(context.Background(), `SELECT status FROM translations WHERE entity_type=$1 AND entity_id=$2 AND field=$3 AND lang=$4`,
		ref.EntityType, ref.EntityID, ref.Field, lang).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func hasTask(ts []TranslationTask, ref i18n.Ref, status string) bool {
	for _, x := range ts {
		if x.EntityType == ref.EntityType && x.EntityID == ref.EntityID && x.Field == ref.Field && x.Status == status {
			return true
		}
	}
	return false
}

// findTask pages through f until a task for ref with status shows up.
func findTask(t *testing.T, s *Store, f TaskFilter, ref i18n.Ref, status string) bool {
	t.Helper()
	f.Limit = 100
	for range 10000 {
		ts, next, err := s.TranslationTasks(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if hasTask(ts, ref, status) {
			return true
		}
		if next == "" {
			return false
		}
		if f.Cursor, err = DecodeTaskCursor(next); err != nil {
			t.Fatal(err)
		}
	}
	return false
}
