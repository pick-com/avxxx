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

// TestAdminReleaseIntegration covers the admin video editor against a real,
// disposable PostgreSQL (make test-integration): conflict resolution with
// typed values and overrides, edits that survive re-ingest, the R-AGE
// publish gate on held releases, playback source CRUD / reorder / enable,
// promotion and performer credits. Skipped unless AVXXX_TEST_DATABASE_URL is
// set; assertions are scoped to the rows this test creates.
func TestAdminReleaseIntegration(t *testing.T) {
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
	if s, err = Open(ctx); err != nil { // migration 020 must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
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
	wantValidation := func(what string, err error) {
		t.Helper()
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: err %v, want ValidationError", what, err)
		}
	}
	wantConflict := func(what string, err error) {
		t.Helper()
		var ce *ConflictError
		if !errors.As(err, &ce) {
			t.Errorf("%s: err %v, want ConflictError", what, err)
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
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	var srcA, srcB int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('adm-a','https://adm-a.test/'||$1) RETURNING id`, sfx).Scan(&srcA))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('adm-b','https://adm-b.test/'||$1) RETURNING id`, sfx).Scan(&srcB))
	code := "ADM-" + sfx[len(sfx)-9:]
	perfX := "Perf X " + sfx
	rec := func(key, title, maker string, tags, performers []string) catalog.Record {
		return catalog.Record{EntityType: catalog.Release, SourceKey: key + sfx, URL: "https://adm.test/" + key + sfx,
			Fields: map[string]any{"title": title, "dvd_code": code, "maker": maker, "tags": tags, "performers": performers}}
	}
	recA := rec("a", "Alpha Title", "Maker A", []string{"Tag A " + sfx}, []string{perfX})
	recB := rec("b", "Beta Title", "Maker B", []string{"Tag A " + sfx}, []string{perfX})
	recordA, err := s.SaveCatalogRecord(ctx, srcA, "t1", recA)
	must(err)
	recordB, err := s.SaveCatalogRecord(ctx, srcB, "t1", recB)
	must(err)
	var relID int64
	must(s.QueryRow(ctx, `SELECT release_id FROM release_identifiers WHERE identifier_value=$1`, code).Scan(&relID))

	// ---- ResolveConflict: ingest conflicts carry parser field names and the
	// field-policy winner as current_value (equal tiers: lower source id wins).
	conflictFor := func(field string) int64 {
		t.Helper()
		var id int64
		must(s.QueryRow(ctx, `SELECT id FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name=$2 AND status='pending'`, relID, field).Scan(&id))
		return id
	}
	titleConflict, makerConflict := conflictFor("title"), conflictFor("maker")
	if n := count(`SELECT count(*) FROM field_conflicts WHERE id=$1 AND current_value #>> '{}' = 'Alpha Title'`, titleConflict); n != 1 {
		t.Fatalf("ingest conflict should carry the policy winner as current_value")
	}
	res, err := s.ResolveConflict(ctx, titleConflict, 0, json.RawMessage(`"Admin Title"`), "verified on maker site", "alice")
	must(err)
	if res.Column != "canonical_title" || string(res.After) != `"Admin Title"` {
		t.Errorf("resolution = %+v", res)
	}
	if got := text(`SELECT canonical_title FROM releases WHERE id=$1`, relID); got != "Admin Title" {
		t.Errorf("canonical_title = %q, want unquoted Admin Title", got)
	}
	if got := text(`SELECT actor FROM release_overrides WHERE release_id=$1 AND field_name='canonical_title'`, relID); got != "alice" {
		t.Errorf("override actor = %s", got)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE conflict_id=$1 AND action='resolve' AND actor='alice'
		AND after_value->>'value'='Admin Title' AND before_value->>'field'='canonical_title'`, titleConflict); n != 1 {
		t.Errorf("resolve review_events rows = %d", n)
	}
	_, err = s.ResolveConflict(ctx, titleConflict, 0, json.RawMessage(`"Again"`), "", "alice")
	wantConflict("resolve twice", err)
	_, err = s.ResolveConflict(ctx, makerConflict, recordA+recordB+999999, nil, "", "alice")
	wantValidation("foreign source record", err)
	res, err = s.ResolveConflict(ctx, makerConflict, recordB, nil, "adopt source B", "bob")
	must(err)
	if got := text(`SELECT maker_name FROM releases WHERE id=$1`, relID); got != "Maker B" {
		t.Errorf("maker adopted from source B = %q", got)
	}
	if _, err = s.ResolveConflict(ctx, 1<<60, 0, json.RawMessage(`"x"`), "", "a"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown conflict: %v", err)
	}
	var idConflict int64
	must(s.QueryRow(ctx, `INSERT INTO field_conflicts(entity_type,entity_id,field_name,candidates) VALUES('release',$1,'dvd_code','[]') RETURNING id`, relID).Scan(&idConflict))
	_, err = s.ResolveConflict(ctx, idConflict, 0, json.RawMessage(`"X-1"`), "", "a")
	wantValidation("identifier conflict", err)
	var dateConflict int64
	must(s.QueryRow(ctx, `INSERT INTO field_conflicts(entity_type,entity_id,field_name,candidates) VALUES('release',$1,'release_date','[]') RETURNING id`, relID).Scan(&dateConflict))
	_, err = s.ResolveConflict(ctx, dateConflict, 0, json.RawMessage(`"not a date"`), "", "a")
	wantValidation("bad date", err)
	_, err = s.ResolveConflict(ctx, dateConflict, 0, json.RawMessage(`"2021-06-01"`), "", "a")
	must(err)
	if got := text(`SELECT release_date::text FROM releases WHERE id=$1`, relID); got != "2021-06-01" {
		t.Errorf("release_date = %s", got)
	}

	// ---- Edits: overrides survive re-ingest.
	p, err := ParseReleasePatch(map[string]json.RawMessage{"tags": json.RawMessage(`["Admin Tag ` + sfx + `"]`),
		"director": json.RawMessage(`"Dir"`), "age_rating": json.RawMessage(`"18"`), "region_allow": json.RawMessage(`["jp"]`),
		"reason": json.RawMessage(`"cleanup"`)})
	must(err)
	pr, err := s.PatchRelease(ctx, relID, p, "carol")
	must(err)
	if pr.Audit == nil || len(pr.Changed) != 4 {
		t.Errorf("patch result = %+v", pr)
	}
	stale := p
	stale.Version = pr.Version - 1
	_, err = s.PatchRelease(ctx, relID, stale, "carol")
	wantConflict("stale version", err)

	recA2 := rec("a", "Alpha Title v2", "Maker A2", []string{"Tag A " + sfx, "Tag New " + sfx}, []string{perfX})
	_, err = s.SaveCatalogRecord(ctx, srcA, "t2", recA2)
	must(err)
	if got := text(`SELECT canonical_title FROM releases WHERE id=$1`, relID); got != "Admin Title" {
		t.Errorf("re-ingest overwrote the overridden title: %q", got)
	}
	if got := text(`SELECT maker_name FROM releases WHERE id=$1`, relID); got != "Maker B" {
		t.Errorf("re-ingest overwrote the overridden maker: %q", got)
	}
	if got := text(`SELECT string_agg(tag,',') FROM release_tags WHERE release_id=$1`, relID); got != "Admin Tag "+sfx {
		t.Errorf("tags after re-ingest = %q", got)
	}
	if n := count(`SELECT count(*) FROM release_tag_sources WHERE release_id=$1 AND source_tag=$2`, relID, "Tag New "+sfx); n != 1 {
		t.Errorf("tag evidence not recorded: %d", n)
	}
	if n := count(`SELECT count(*) FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name IN ('title','maker','tags') AND status='pending'`, relID); n != 0 {
		t.Errorf("overridden fields reopened %d conflicts", n)
	}
	// Clearing an override is explicit; the next crawl then writes the field again.
	if err = s.ClearReleaseOverride(ctx, relID, "canonical_title", "", "carol"); err != nil {
		t.Fatal(err)
	}
	if err = s.ClearReleaseOverride(ctx, relID, "canonical_title", "", "carol"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("clear twice: %v", err)
	}
	_, err = s.SaveCatalogRecord(ctx, srcA, "t3", recA2)
	must(err)
	if got := text(`SELECT canonical_title FROM releases WHERE id=$1`, relID); got != "Alpha Title v2" {
		t.Errorf("after clearing the override the crawl should write the title: %q", got)
	}

	// ---- Performers: credits become an override.
	var perfXID, perfLead int64
	must(s.QueryRow(ctx, `SELECT performer_id FROM release_performers WHERE release_id=$1`, relID).Scan(&perfXID))
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,birth_date) VALUES($1,'Lead',DATE '1995-01-01') RETURNING id`, "lead-"+sfx).Scan(&perfLead))
	_, err = s.SetReleasePerformer(ctx, relID, ReleasePerformerInput{PerformerID: perfLead, Role: "lead"}, "dave")
	must(err)
	_, err = s.SetReleasePerformer(ctx, relID, ReleasePerformerInput{PerformerID: perfLead, Role: "boss"}, "dave")
	wantValidation("bad role", err)
	_, err = s.RemoveReleasePerformer(ctx, relID, perfXID, "not in this video", "dave")
	must(err)
	if _, err = s.RemoveReleasePerformer(ctx, relID, perfXID, "", "dave"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("remove twice: %v", err)
	}
	_, err = s.SaveCatalogRecord(ctx, srcA, "t4", recA2)
	must(err)
	if got := text(`SELECT string_agg(performer_id::text||':'||role||':'||is_primary::text,',') FROM release_performers WHERE release_id=$1`, relID); got != fmt.Sprintf("%d:lead:true", perfLead) {
		t.Errorf("credits after re-ingest = %s", got)
	}

	// ---- Detail and list.
	d, err := s.AdminRelease(ctx, relID)
	must(err)
	if len(d.Overrides) < 4 || len(d.Performers) != 1 || d.Performers[0].AgeAtFilming == nil || d.Performers[0].AgeAtFilming.Age != 26 ||
		!d.Performers[0].AgeAtFilming.Approx || len(d.ReviewEvents) == 0 || d.AgeRating == nil || *d.AgeRating != "18" ||
		strings.Join(d.RegionAllow, ",") != "JP" || len(d.Identifiers) == 0 {
		t.Errorf("detail = %+v", d)
	}
	if d.Translations.Counts["missing"] == 0 {
		t.Errorf("translation summary = %+v", d.Translations)
	}
	list, _, err := s.AdminReleases(ctx, AdminReleaseFilter{Q: strings.ToLower(code)})
	must(err)
	if len(list) != 1 || list[0].ID != relID || list[0].Status != ReleaseDraft {
		t.Errorf("list by identifier = %+v", list)
	}
	if _, _, err = s.AdminReleases(ctx, AdminReleaseFilter{Status: "bogus"}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("bad status filter: %v", err)
	}

	// ---- R-AGE: a held release cannot be published until the rule passes.
	var minor, heldID int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,birth_date) VALUES($1,'Young',DATE '2005-10-01') RETURNING id`, "young-"+sfx).Scan(&minor))
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,release_date,status) VALUES($1,'Held',DATE '2023-12-01','published') RETURNING id`, "held-"+sfx).Scan(&heldID))
	pr, err = s.SetReleasePerformer(ctx, heldID, ReleasePerformerInput{PerformerID: minor, Role: "lead"}, "erin")
	must(err)
	if pr.Status != ReleaseHeld || pr.Audit == nil || !pr.Audit.Held {
		t.Fatalf("adding an under-18 performer must hold the release: %+v", pr)
	}
	_, err = s.TransitionRelease(ctx, heldID, Transition{To: ReleasePublished}, "erin")
	wantValidation("publish held without override_reason", err)
	_, err = s.TransitionRelease(ctx, heldID, Transition{To: ReleaseDraft}, "erin")
	wantConflict("held to draft", err)
	_, err = s.TransitionRelease(ctx, heldID, Transition{To: ReleasePublished, OverrideReason: "looks fine"}, "erin")
	var age *AgeGateError
	if !errors.As(err, &age) || len(age.Findings) != 1 {
		t.Errorf("publish while R-AGE fails: %v", err)
	}
	if got := text(`SELECT status FROM releases WHERE id=$1`, heldID); got != ReleaseHeld {
		t.Errorf("status after refused publish = %s", got)
	}
	// Fix the data: shot in 2025, the performer was 19-20. The audit never unholds.
	p, err = ParseReleasePatch(map[string]json.RawMessage{"shoot_year": json.RawMessage(`2025`)})
	must(err)
	pr, err = s.PatchRelease(ctx, heldID, p, "erin")
	must(err)
	if len(pr.Audit.Age) != 0 || pr.Status != ReleaseHeld {
		t.Errorf("after shoot_year fix: %+v", pr)
	}
	tr, err := s.TransitionRelease(ctx, heldID, Transition{To: ReleasePublished, OverrideReason: "shoot year confirmed by licensor", Version: pr.Version}, "erin")
	must(err)
	if tr.From != ReleaseHeld || tr.To != ReleasePublished {
		t.Errorf("transition = %+v", tr)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action='status_change' AND actor='erin'
		AND reason='shoot year confirmed by licensor' AND after_value->>'override_reason'<>''`, heldID); n != 1 {
		t.Errorf("held→published review_events = %d", n)
	}
	if n := count(`SELECT count(*) FROM audit_issues WHERE rule=$1 AND entity_type='release' AND entity_id=$2 AND status='open'`, string(audit.RuleAge), heldID); n != 0 {
		t.Errorf("R-AGE issue still open after the hold was lifted")
	}
	// A draft with a failing R-AGE cannot be published or scheduled either.
	var draftID int64
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,release_date) VALUES($1,'Draft',DATE '2022-01-01') RETURNING id`, "draft-"+sfx).Scan(&draftID))
	exec(`INSERT INTO release_performers(release_id,performer_id,is_primary) VALUES($1,$2,true)`, draftID, minor)
	future := time.Now().Add(48 * time.Hour)
	_, err = s.TransitionRelease(ctx, draftID, Transition{To: ReleaseUpcoming, ScheduledAt: &future}, "erin")
	if !errors.As(err, &age) {
		t.Errorf("schedule a failing draft: %v", err)
	}
	exec(`DELETE FROM release_performers WHERE release_id=$1`, draftID)
	tr, err = s.TransitionRelease(ctx, draftID, Transition{To: ReleaseUpcoming, ScheduledAt: &future}, "erin")
	must(err)
	if tr.ScheduledAt == nil || tr.To != ReleaseUpcoming {
		t.Errorf("schedule = %+v", tr)
	}
	_, err = s.TransitionRelease(ctx, draftID, Transition{To: ReleaseUnpublished, Reason: "copyright claim"}, "erin")
	must(err)
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action='status_change'`, draftID); n != 2 {
		t.Errorf("status_change events = %d", n)
	}

	// ---- Playback sources.
	u1, u2, lic := "https://partner.test/embed/"+sfx, "https://partner.test/preview/"+sfx, "LIC-"+sfx
	_, err = s.CreatePlaybackSource(ctx, heldID, SourceInput{Kind: KindOfficialEmbed, Provider: "partner", Label: "Main", URL: &u1}, "frank")
	wantValidation("embed without license_ref", err)
	dur := 3600
	main, err := s.CreatePlaybackSource(ctx, heldID, SourceInput{Kind: KindOfficialEmbed, Provider: "partner", Label: "Main", URL: &u1, LicenseRef: &lic, DurationSeconds: &dur}, "frank")
	must(err)
	altDur := 4000
	prev, err := s.CreatePlaybackSource(ctx, heldID, SourceInput{Kind: KindOfficialPreview, Provider: "partner", Label: "Cut", URL: &u2, DurationSeconds: &altDur}, "frank")
	must(err)
	if !prev.IsAltVersion || prev.VersionKind != VersionAlt || main.Status != SourceStatusActive || main.HealthBand != audit.BandInsufficient {
		t.Errorf("created: main %+v preview %+v", main, prev)
	}
	upd, err := s.UpdatePlaybackSource(ctx, heldID, main.ID, json.RawMessage(`{"label":"Main 1080p","max_height":1080,"region_deny":["de"]}`), "frank")
	must(err)
	if upd.Label != "Main 1080p" || *upd.MaxHeight != 1080 || strings.Join(upd.RegionDeny, ",") != "DE" || upd.LicenseRef == nil {
		t.Errorf("updated = %+v", upd)
	}
	_, err = s.UpdatePlaybackSource(ctx, heldID, main.ID, json.RawMessage(`{"status":"disabled"}`), "frank")
	wantValidation("status via patch", err)
	if _, err = s.UpdatePlaybackSource(ctx, relID, main.ID, json.RawMessage(`{"label":"x"}`), "frank"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("source of another release: %v", err)
	}
	_, err = s.ReorderPlaybackSources(ctx, heldID, []int64{prev.ID}, "frank")
	wantValidation("partial reorder", err)
	ordered, err := s.ReorderPlaybackSources(ctx, heldID, []int64{prev.ID, main.ID}, "frank")
	must(err)
	if len(ordered) != 2 || ordered[0].ID != prev.ID || ordered[0].Priority != 10 || ordered[1].Priority != 20 {
		t.Errorf("reordered = %+v", ordered)
	}
	_, err = s.SetPlaybackSourceEnabled(ctx, heldID, main.ID, true, "", "frank")
	wantConflict("enable active", err)
	dis, err := s.SetPlaybackSourceEnabled(ctx, heldID, main.ID, false, "broken embed", "frank")
	must(err)
	if dis.Status != SourceStatusDisabled {
		t.Errorf("disabled = %+v", dis)
	}
	if _, err = s.SetPlaybackSourceEnabled(ctx, heldID, main.ID, true, "", "frank"); err != nil {
		t.Errorf("admin-disabled source re-enables without a reason: %v", err)
	}
	// The health job disabled the preview: re-enabling needs a reason and closes R-SOURCE.
	exec(`UPDATE playback_sources SET status='disabled',health_score=0.3 WHERE id=$1`, prev.ID)
	exec(`INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor) VALUES('playback_source',$1,'status_change','{"status":"active"}','{"status":"disabled"}','health',$2)`, prev.ID, audit.HealthActor)
	exec(`INSERT INTO audit_issues(rule,entity_type,entity_id,severity,status) VALUES($1,'playback_source',$2,'high','open')`, string(audit.RuleSourceHealth), prev.ID)
	_, err = s.SetPlaybackSourceEnabled(ctx, heldID, prev.ID, true, "  ", "frank")
	wantValidation("re-enable health-disabled without reason", err)
	en, err := s.SetPlaybackSourceEnabled(ctx, heldID, prev.ID, true, "provider fixed the CDN", "frank")
	must(err)
	if en.Status != SourceStatusActive || en.StatusChangedBy == nil || *en.StatusChangedBy != "frank" {
		t.Errorf("enabled = %+v", en)
	}
	if n := count(`SELECT count(*) FROM audit_issues WHERE rule=$1 AND entity_id=$2 AND status='open'`, string(audit.RuleSourceHealth), prev.ID); n != 0 {
		t.Errorf("R-SOURCE issue still open")
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='playback_source' AND entity_id=$1 AND actor='frank'`, main.ID); n < 4 {
		t.Errorf("source review_events = %d", n)
	}
	// Promote a crawled media link.
	must(s.LinkMediaToRelease(ctx, heldID, "tube"+sfx, "m1", "https://tube.test/v/"+sfx))
	_, err = s.AdminPromoteMediaSource(ctx, PromoteMediaSourceInput{ReleaseID: heldID, Source: "tube" + sfx, Kind: KindOfficialEmbed, Label: "Tube"}, "frank")
	wantValidation("promote embed without license", err)
	if _, err = s.AdminPromoteMediaSource(ctx, PromoteMediaSourceInput{ReleaseID: heldID, Source: "nope" + sfx, Kind: KindExternalLink, Label: "x"}, "frank"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("promote unknown media source: %v", err)
	}
	promoted, err := s.AdminPromoteMediaSource(ctx, PromoteMediaSourceInput{ReleaseID: heldID, Source: "tube" + sfx, Kind: KindExternalLink, Label: "Tube"}, "frank")
	must(err)
	if promoted.Kind != KindExternalLink || promoted.URL == nil || *promoted.URL != "https://tube.test/v/"+sfx {
		t.Errorf("promoted = %+v", promoted)
	}
	srcs, err := s.AdminPlaybackSources(ctx, heldID)
	must(err)
	if len(srcs) != 3 {
		t.Errorf("sources = %d", len(srcs))
	}
	d, err = s.AdminRelease(ctx, heldID)
	must(err)
	if len(d.MediaSources) != 1 || d.MediaSources[0].PromotedAs == nil || *d.MediaSources[0].PromotedAs != promoted.ID {
		t.Errorf("media sources = %+v", d.MediaSources)
	}

	// Release-level region: the published release plays in JP only.
	p, err = ParseReleasePatch(map[string]json.RawMessage{"region_allow": json.RawMessage(`["JP"]`)})
	must(err)
	_, err = s.PatchRelease(ctx, heldID, p, "frank")
	must(err)
	var slug string
	must(s.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, heldID).Scan(&slug))
	rel, all, err := s.PlaybackForRelease(ctx, slug)
	must(err)
	if n := len(PlanPlayback(rel, all, "US", 0).Sources); n != 0 {
		t.Errorf("US plays %d sources of a JP-only release", n)
	}
	if n := len(PlanPlayback(rel, all, "JP", 0).Sources); n != 3 {
		t.Errorf("JP plays %d sources, want 3", n)
	}
}
