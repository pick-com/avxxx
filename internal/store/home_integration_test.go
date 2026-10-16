package store

import (
	"avxxx/internal/homeops"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestHomeOpsIntegration covers migration 038 against a disposable database
// (make test-integration): slot validation and slug resolution, live-slot
// selection (window, enabled, langs, regions, public targets only), drag
// reorder, review_events for every write, announcements and dismissals,
// and the 高分短片 tiers.
func TestHomeOpsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "home_ops")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // migration 038 is idempotent
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
	rel := func(slug, status string, runtime int, series string) int64 {
		t.Helper()
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes,release_date,series_name,maker_name)
			VALUES($1,$1,$2,$3,DATE '2026-01-01',NULLIF($4,''),'Maker') RETURNING id`, slug, status, runtime, series).Scan(&id))
		return id
	}
	pub := rel("home-pub", "published", 120, "Home Series")
	draft := rel("home-draft", "draft", 120, "")
	rel("home-series-2", "published", 90, "Home Series")
	var seriesID int64
	must(s.QueryRow(ctx, `SELECT id FROM series WHERE name='Home Series'`).Scan(&seriesID))

	now := time.Now().UTC().Truncate(time.Second)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	later := now.Add(48 * time.Hour)

	// Validation.
	bad := []HomeSlotInput{
		{SlotType: "banner", TargetType: "release", ReleaseID: pub, StartsAt: past},
		{SlotType: "hero", TargetType: "release", ReleaseSlug: "nope", StartsAt: past},
		{SlotType: "hero", TargetType: "link", LinkURL: "//evil.example", Texts: homeops.Texts{"en": {Title: "x"}}, StartsAt: past},
		{SlotType: "hero", TargetType: "link", LinkURL: "/discover", StartsAt: past}, // a link needs a title
		{SlotType: "hero", TargetType: "release", ReleaseID: pub, StartsAt: future, EndsAt: &past},
		{SlotType: "hero", TargetType: "release", ReleaseID: pub, StartsAt: past, Regions: []string{"USA"}},
	}
	for i, in := range bad {
		var ve *ValidationError
		if _, err := s.CreateHomeSlot(ctx, in, "ops"); !errors.As(err, &ve) {
			t.Errorf("bad input %d: err %v, want ValidationError", i, err)
		}
	}

	create := func(in HomeSlotInput) HomeSlot {
		t.Helper()
		h, err := s.CreateHomeSlot(ctx, in, "ops")
		must(err)
		return h
	}
	hero := create(HomeSlotInput{SlotType: "hero", TargetType: "release", ReleaseSlug: "home-pub", StartsAt: past, Priority: 1,
		Texts: homeops.Texts{"en": {Title: "Hero EN"}}})
	if hero.Release == nil || hero.Release.ID != pub || !hero.TargetPublic || hero.CreatedBy != "ops" {
		t.Fatalf("hero %+v", hero)
	}
	seriesSlot := create(HomeSlotInput{SlotType: "hero", TargetType: "series", SeriesID: seriesID, StartsAt: past, Priority: 5})
	if seriesSlot.Series == nil || seriesSlot.Series.ReleaseCount != 2 || seriesSlot.Release == nil {
		t.Fatalf("series slot %+v", seriesSlot)
	}
	draftSlot := create(HomeSlotInput{SlotType: "hero", TargetType: "release", ReleaseID: draft, StartsAt: past})
	if draftSlot.TargetPublic {
		t.Error("draft release slot reported public")
	}
	scheduled := create(HomeSlotInput{SlotType: "hero", TargetType: "release", ReleaseID: pub, StartsAt: future, EndsAt: &later})
	jpOnly := create(HomeSlotInput{SlotType: "shelf", TargetType: "link", LinkURL: "/discover?tag=x", StartsAt: past,
		Texts: homeops.Texts{"zh-CN": {Title: "日本限定"}}, Regions: []string{"jp"}})
	enOnly := create(HomeSlotInput{SlotType: "shelf", TargetType: "link", LinkURL: "https://example.com/e", StartsAt: past,
		Texts: homeops.Texts{"en": {Title: "English only"}}, Langs: []string{"en"}})
	off := false
	create(HomeSlotInput{SlotType: "shelf", TargetType: "link", LinkURL: "/x", StartsAt: past, Texts: homeops.Texts{"en": {Title: "off"}}, Enabled: &off})

	ids := func(hs []HomeSlot) string {
		out := ""
		for _, h := range hs {
			out += fmt.Sprintf("%d,", h.ID)
		}
		return out
	}
	live, err := s.LiveHomeSlots(ctx, now, "", "")
	must(err)
	if got, want := ids(live), fmt.Sprintf("%d,%d,", seriesSlot.ID, hero.ID); got != want {
		t.Errorf("live zh-CN no region = %s, want %s (priority order, no draft / scheduled / targeted / disabled)", got, want)
	}
	live, err = s.LiveHomeSlots(ctx, now, "en", "JP")
	must(err)
	if got, want := ids(live), fmt.Sprintf("%d,%d,%d,%d,", seriesSlot.ID, hero.ID, jpOnly.ID, enOnly.ID); got != want {
		t.Errorf("live en JP = %s, want %s", got, want)
	}
	live, err = s.LiveHomeSlots(ctx, future.Add(time.Minute), "", "")
	must(err)
	if got, want := ids(live), fmt.Sprintf("%d,%d,%d,", seriesSlot.ID, hero.ID, scheduled.ID); got != want {
		t.Errorf("preview at +1h = %s, want %s", got, want)
	}

	// Publishing the draft makes its slot public; merging hides it again.
	must2 := func(_ any, err error) { must(err) }
	must2(s.Exec(ctx, `UPDATE releases SET status='published' WHERE id=$1`, draft))
	live, _ = s.LiveHomeSlots(ctx, now, "", "")
	if len(live) != 3 {
		t.Errorf("after publish: %d live slots, want 3", len(live))
	}
	must2(s.Exec(ctx, `UPDATE releases SET merged_into_id=$2 WHERE id=$1`, draft, pub))
	live, _ = s.LiveHomeSlots(ctx, now, "", "")
	if len(live) != 2 {
		t.Errorf("after merge: %d live slots, want 2", len(live))
	}

	// Update, reorder, delete.
	upd, err := s.UpdateHomeSlot(ctx, hero.ID, HomeSlotInput{SlotType: "hero", TargetType: "release", ReleaseID: pub, StartsAt: past, Priority: 9,
		Texts: homeops.Texts{"en": {Title: "Hero EN 2"}}}, "ops2")
	must(err)
	if upd.Priority != 9 || upd.UpdatedBy != "ops2" || upd.Texts["en"].Title != "Hero EN 2" {
		t.Errorf("update %+v", upd)
	}
	if _, err := s.UpdateHomeSlot(ctx, 999999, HomeSlotInput{SlotType: "hero", TargetType: "release", ReleaseID: pub, StartsAt: past}, "ops"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("update missing: %v", err)
	}
	if _, err := s.ReorderHomeSlots(ctx, "hero", []int64{hero.ID, jpOnly.ID}, "ops"); err == nil {
		t.Error("reorder across slot types accepted")
	}
	n, err := s.ReorderHomeSlots(ctx, "hero", []int64{seriesSlot.ID, scheduled.ID, hero.ID, draftSlot.ID}, "ops3")
	must(err)
	if n == 0 {
		t.Error("reorder changed nothing")
	}
	live, _ = s.LiveHomeSlots(ctx, now, "", "")
	if got, want := ids(live), fmt.Sprintf("%d,%d,", seriesSlot.ID, hero.ID); got != want {
		t.Errorf("after reorder = %s, want %s", got, want)
	}
	must(s.DeleteHomeSlot(ctx, enOnly.ID, "ops"))
	if err := s.DeleteHomeSlot(ctx, enOnly.ID, "ops"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("delete twice: %v", err)
	}
	all, err := s.HomeSlots(ctx, HomeSlotFilter{SlotType: "shelf"})
	must(err)
	if len(all) != 2 {
		t.Errorf("admin shelf list: %d, want 2 (incl. disabled)", len(all))
	}
	window, err := s.HomeSlots(ctx, HomeSlotFilter{From: &later, To: ptrTime(later.Add(time.Hour))})
	must(err)
	inWindow := map[int64]bool{}
	for _, w := range window {
		inWindow[w.ID] = true
	}
	if inWindow[scheduled.ID] || !inWindow[hero.ID] || !inWindow[jpOnly.ID] {
		t.Errorf("window [later, later+1h) = %s: open-ended slots in, the slot ending at later out", ids(window))
	}

	var events map[string]int = map[string]int{}
	rows, err := s.Query(ctx, `SELECT action,count(*) FROM review_events WHERE entity_type='home_slot' AND actor<>'' GROUP BY action`)
	must(err)
	for rows.Next() {
		var a string
		var c int
		must(rows.Scan(&a, &c))
		events[a] = c
	}
	rows.Close()
	if events["create"] != 7 || events["update"] != 1 || events["delete"] != 1 || events["reorder"] != n {
		t.Errorf("review events %v (reorder changed %d)", events, n)
	}

	// Announcements.
	if _, err := s.CreateAnnouncement(ctx, AnnouncementInput{StartsAt: past}, "ops"); err == nil {
		t.Error("announcement without body accepted")
	}
	if _, err := s.CreateAnnouncement(ctx, AnnouncementInput{Severity: "panic", Texts: homeops.Texts{"en": {Body: "x"}}, StartsAt: past}, "ops"); err == nil {
		t.Error("unknown severity accepted")
	}
	info, err := s.CreateAnnouncement(ctx, AnnouncementInput{Texts: homeops.Texts{"zh-CN": {Body: "通知"}}, StartsAt: past}, "ops")
	must(err)
	if info.Severity != "info" || !info.Dismissible || !info.Enabled {
		t.Errorf("defaults %+v", info)
	}
	no := false
	crit, err := s.CreateAnnouncement(ctx, AnnouncementInput{Severity: "critical", Texts: homeops.Texts{"en": {Body: "Outage"}}, StartsAt: past, Dismissible: &no}, "ops")
	must(err)
	_, err = s.CreateAnnouncement(ctx, AnnouncementInput{Texts: homeops.Texts{"en": {Body: "JP"}}, StartsAt: past, Regions: []string{"JP"}}, "ops")
	must(err)
	anns, err := s.LiveAnnouncements(ctx, now, "en", "")
	must(err)
	if len(anns) != 2 || anns[0].ID != crit.ID {
		t.Errorf("live announcements %+v (critical first, JP-only left out)", anns)
	}
	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('home@example.test','x') RETURNING id`).Scan(&uid))
	must(s.DismissAnnouncement(ctx, uid, info.ID))
	must(s.DismissAnnouncement(ctx, uid, info.ID)) // idempotent
	if err := s.DismissAnnouncement(ctx, uid, crit.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("dismissing a non-dismissible announcement: %v", err)
	}
	got, err := s.DismissedAnnouncements(ctx, uid)
	must(err)
	if len(got) != 1 || got[0] != info.ID {
		t.Errorf("dismissed %v", got)
	}
	_, err = s.UpdateAnnouncement(ctx, info.ID, AnnouncementInput{Texts: homeops.Texts{"zh-CN": {Body: "改"}}, StartsAt: past, EndsAt: &future}, "ops")
	must(err)
	must(s.DeleteAnnouncement(ctx, crit.ID, "ops"))
	var annEvents int
	must(s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='announcement'`).Scan(&annEvents))
	if annEvents != 5 {
		t.Errorf("announcement review events %d, want 5", annEvents)
	}

	// 高分短片: one release with 50+ votes, one with a few, then newest short ones.
	shortRated := rel("short-rated", "published", 40, "")
	shortFew := rel("short-few", "published", 30, "")
	rel("short-new", "published", 20, "")
	rel("long-rated", "published", 150, "")
	rel("short-draft", "draft", 20, "")
	vote := func(release int64, n, score int) {
		for i := 0; i < n; i++ {
			must2(s.Exec(ctx, `INSERT INTO ratings(release_id,voter_key,device_id,score,weight) VALUES($1,$2,'d',$3,1)`,
				release, fmt.Sprintf("d:%032x", int64(i)+release*1000), score))
		}
	}
	vote(shortRated, 55, 5)
	vote(shortFew, 6, 4)
	items, basis, err := s.TopRatedShort(ctx, 60, 1)
	must(err)
	if len(items) != 1 || items[0].ID != shortRated || items[0].Score == nil || basis != ShortBasisRated {
		t.Errorf("top 1: %+v %s", items, basis)
	}
	items, basis, err = s.TopRatedShort(ctx, 60, 2)
	must(err)
	if len(items) != 2 || items[1].ID != shortFew || items[1].Score != nil || items[1].Votes != 6 || basis != ShortBasisFewVotes {
		t.Errorf("top 2: %+v %s", items, basis)
	}
	items, basis, err = s.TopRatedShort(ctx, 60, 10)
	must(err)
	if basis != ShortBasisFallback {
		t.Errorf("basis %s, want fallback", basis)
	}
	for _, x := range items {
		if x.RuntimeMinutes > 60 || x.Slug == "short-draft" {
			t.Errorf("unexpected item %s (%d min)", x.Slug, x.RuntimeMinutes)
		}
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
