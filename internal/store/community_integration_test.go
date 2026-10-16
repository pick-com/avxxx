package store

import (
	"avxxx/internal/community"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestCommunityIntegration exercises migration 017 and the community and
// moderation SQL against a real, disposable PostgreSQL database. It is
// skipped unless AVXXX_TEST_DATABASE_URL is set; the database is modified.
func TestCommunityIntegration(t *testing.T) {
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

	sfx := fmt.Sprint(time.Now().UnixNano())
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	newUser := func(name string, age time.Duration) int64 {
		t.Helper()
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name,created_at) VALUES($1,'x',$2,now()-$3::interval) RETURNING id`,
			name+sfx+"@t", name, fmt.Sprintf("%d seconds", int(age.Seconds()))).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	slug := "cm-" + sfx
	var rel int64
	if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,'Community',
		'published',100) RETURNING id`, slug).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	var mainSrc, altSrc int64
	s.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,duration_seconds) VALUES($1,'official_embed','p','main','https://e/m',6000) RETURNING id`, rel).Scan(&mainSrc)
	s.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,duration_seconds) VALUES($1,'official_embed','p','cut','https://e/a',7000) RETURNING id`, rel).Scan(&altSrc)

	old := newUser("old", 30*24*time.Hour)
	fresh := newUser("fresh", time.Hour)
	spammer := newUser("spam", 30*24*time.Hour)

	// --- danmaku and timelines ----------------------------------------------
	tl, err := s.DanmakuTimelineFor(ctx, slug, 0)
	if err != nil || tl.VersionKey != "main" || tl.DurationMS != 6_000_000 {
		t.Fatalf("main timeline = %+v %v", tl, err)
	}
	altTL, err := s.DanmakuTimelineFor(ctx, slug, altSrc)
	if err != nil || altTL.VersionKey != community.AltVersionKey(altSrc) || altTL.DurationMS != 7_000_000 {
		t.Fatalf("alt timeline = %+v %v", altTL, err)
	}
	if _, err := s.DanmakuTimelineFor(ctx, slug, 999999999); !errors.Is(err, ErrUnknownSource) {
		t.Fatalf("unknown source = %v", err)
	}
	post := func(tl DanmakuTimeline, uid int64, tms int, body, status string) Danmaku {
		t.Helper()
		d, err := s.CreateDanmaku(ctx, NewDanmaku{ReleaseID: tl.ReleaseID, VersionKey: tl.VersionKey, TMs: tms, Body: body,
			Color: "#FFFFFF", Mode: "scroll", UserID: uid, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// 60 distinct users in distinct buckets -> 60 valid danmaku on main.
	var voters []int64
	for i := range 60 {
		u := newUser(fmt.Sprintf("v%d-", i), 30*24*time.Hour)
		voters = append(voters, u)
		post(tl, u, i*30_000, fmt.Sprintf("弹幕%d", i), community.StatusVisible)
	}
	// Spam that must not inflate the curve: same user, same bucket.
	for range 30 {
		post(tl, spammer, 1_000, "666", community.StatusVisible)
	}
	shadow := post(tl, fresh, 2_000, "加微信", community.StatusShadow)
	post(altTL, old, 5_000, "alt", community.StatusVisible)

	got, err := s.DanmakuRange(ctx, tl, 0, 60_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range got {
		if d.ID == shadow.ID {
			t.Error("anonymous reader sees a shadow danmaku")
		}
	}
	mine, _ := s.DanmakuRange(ctx, tl, 0, 60_000, fresh)
	if len(mine) != len(got)+1 {
		t.Errorf("author must see own shadow danmaku: %d vs %d", len(mine), len(got))
	}

	// --- curve ------------------------------------------------------------------
	n, err := s.RecomputeReleaseCurves(ctx, rel)
	if err != nil || n != 1 {
		t.Fatalf("recompute = %d %v", n, err)
	}
	curves, err := s.DanmakuCurves(ctx, rel)
	if err != nil || len(curves["main"]) != 200 || curves[altTL.VersionKey] != nil {
		t.Fatalf("curves = %v %v", len(curves["main"]), err)
	}
	var valid, bucketMS int
	s.QueryRow(ctx, `SELECT valid_count,bucket_ms FROM danmaku_curves WHERE release_id=$1 AND version_key='main'`, rel).Scan(&valid, &bucketMS)
	if valid != 61 || bucketMS != 30_000 { // 60 users + spammer once
		t.Errorf("valid=%d bucket=%d, want 61 / 30000", valid, bucketMS)
	}
	var queued bool
	s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM danmaku_curve_queue WHERE release_id=$1)`, rel).Scan(&queued)
	if queued {
		t.Error("recomputed release still queued")
	}
	// Cadence: a new danmaku re-queues it, but a curve computed seconds ago is not due yet.
	post(tl, old, 3_000_000, "new", community.StatusVisible)
	if _, err := s.RecomputeDanmakuCurves(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM danmaku_curve_queue WHERE release_id=$1)`, rel).Scan(&queued)
	if !queued {
		t.Error("release recomputed before its cadence")
	}
	mustExec(`UPDATE danmaku_curves SET computed_at=now()-interval '2 minutes' WHERE release_id=$1`, rel)
	if _, err := s.RecomputeDanmakuCurves(ctx, 1000); err != nil {
		t.Fatal(err)
	}
	s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM danmaku_curve_queue WHERE release_id=$1)`, rel).Scan(&queued)
	if queued {
		t.Error("new release not recomputed after 1 minute")
	}

	// --- ratings ----------------------------------------------------------------
	if err := s.RateRelease(ctx, rel, community.Voter{UserID: old, AccountAge: 30 * 24 * time.Hour}, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.RateRelease(ctx, rel, community.Voter{UserID: old, AccountAge: 30 * 24 * time.Hour}, 4); err != nil {
		t.Fatal(err) // re-vote updates
	}
	if err := s.RateRelease(ctx, rel, community.Voter{UserID: fresh, AccountAge: time.Hour}, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.RateRelease(ctx, rel, community.Voter{DeviceID: "device-abcdefgh"}, 1); err != nil {
		t.Fatal(err)
	}
	sum, err := s.RatingSummary(ctx, rel, community.VoterKey(community.Voter{UserID: old}))
	if err != nil || sum.Count != 3 || sum.Score != nil || !sum.Insufficient || sum.MyScore == nil || *sum.MyScore != 4 {
		t.Fatalf("summary = %+v %v", sum, err)
	}
	var w float32
	s.QueryRow(ctx, `SELECT weight FROM ratings WHERE release_id=$1 AND user_id=$2`, rel, fresh).Scan(&w)
	if w != 0.5 {
		t.Errorf("fresh weight %v", w)
	}
	var dev string
	s.QueryRow(ctx, `SELECT device_id FROM ratings WHERE release_id=$1 AND user_id IS NULL`, rel).Scan(&dev)
	if dev == "device-abcdefgh" || dev == "" {
		t.Errorf("raw device id stored: %q", dev)
	}
	for _, u := range voters {
		s.RateRelease(ctx, rel, community.Voter{UserID: u, AccountAge: 30 * 24 * time.Hour}, 4)
	}
	sum, _ = s.RatingSummary(ctx, rel, "")
	if sum.Count != 63 || sum.Score == nil || *sum.Score < 2 || *sum.Score > 10 || len(sum.Distribution) != 5 || sum.MyScore != nil {
		t.Fatalf("summary with 63 votes = %+v", sum)
	}

	// Flood: 20 anonymous 5-star votes in one hour on another release.
	slug2 := "cm2-" + sfx
	var rel2 int64
	s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,'Flooded','published') RETURNING id`, slug2).Scan(&rel2)
	for i := range community.FloodMinVotes {
		s.RateRelease(ctx, rel2, community.Voter{DeviceID: fmt.Sprintf("flood-%s-%d", sfx, i)}, 5)
	}
	mustExec(`UPDATE ratings SET created_at=date_trunc('hour', now()) + interval '5 minutes' WHERE release_id=$1`, rel2)
	fl, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueRatingFloods, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	var flood *RatingFlood
	for _, f := range fl.([]RatingFlood) {
		if f.ReleaseID == rel2 {
			flood = &f
		}
	}
	if flood == nil || flood.Votes != community.FloodMinVotes || flood.Anonymous != community.FloodMinVotes || flood.Score != 5 {
		t.Fatalf("flood not detected: %+v", flood)
	}
	removed, err := s.RemoveRatingFlood(ctx, rel2, 5, flood.WindowStart, "confirmed", "mod")
	if err != nil || removed != int64(community.FloodMinVotes) {
		t.Fatalf("remove flood = %d %v", removed, err)
	}
	if sum, _ := s.RatingSummary(ctx, rel2, ""); sum.Count != 0 {
		t.Errorf("removed votes still counted: %+v", sum)
	}
	// A struck vote stays struck on re-vote.
	s.RateRelease(ctx, rel2, community.Voter{DeviceID: "flood-" + sfx + "-0"}, 4)
	if sum, _ := s.RatingSummary(ctx, rel2, ""); sum.Count != 0 {
		t.Errorf("re-vote revived a struck vote: %+v", sum)
	}

	// --- reviews ---------------------------------------------------------------
	mustExec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds) VALUES($1,$2,5900,6000)`, old, rel)
	mustExec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds) VALUES($1,$2,3000,6000)`, fresh, rel)
	r1, err := s.CreateReview(ctx, rel, old, "很好看", false, community.StatusVisible)
	if err != nil || r1.WatchBadge != "finished" || !r1.Mine || r1.Author.DisplayName != "old" {
		t.Fatalf("review = %+v %v", r1, err)
	}
	if _, err := s.CreateReview(ctx, rel, old, "again", false, community.StatusVisible); err == nil {
		t.Fatal("second live review must conflict")
	}
	r2, err := s.CreateReview(ctx, rel, fresh, "一般", true, community.StatusVisible)
	if err != nil || r2.WatchBadge != "partial" || r2.WatchPercent != 50 || !r2.Spoiler {
		t.Fatalf("review 2 = %+v %v", r2, err)
	}
	r3, err := s.CreateReview(ctx, rel, spammer, "加微信", false, community.StatusShadow)
	if err != nil || !r3.Pending || r3.WatchBadge != "none" {
		t.Fatalf("review 3 = %+v %v", r3, err)
	}
	if n, err := s.SetReviewLike(ctx, r2.ID, old, true); err != nil || n != 1 {
		t.Fatalf("like = %d %v", n, err)
	}
	if n, _ := s.SetReviewLike(ctx, r2.ID, old, true); n != 1 {
		t.Errorf("double like counted: %d", n)
	}
	if _, err := s.SetReviewLike(ctx, r3.ID, old, true); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("liking a shadow review: %v", err)
	}
	page, next, err := s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortHelpful, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != r2.ID || next == "" {
		t.Fatalf("helpful page 1 = %+v %q %v", page, next, err)
	}
	c, _ := DecodeCursor(next)
	page, next, _ = s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortHelpful, Limit: 1, Cursor: &c})
	if len(page) != 1 || page[0].ID != r1.ID || next != "" {
		t.Fatalf("helpful page 2 = %+v %q (shadow review must be hidden)", page, next)
	}
	page, _, _ = s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortNewest, Limit: 10, ViewerID: spammer})
	if len(page) != 3 || page[0].ID != r3.ID {
		t.Errorf("author sees own shadow review first in newest: %d", len(page))
	}
	page, next, _ = s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortNewest, Limit: 1})
	c, _ = DecodeCursor(next)
	page2, _, _ := s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortNewest, Limit: 1, Cursor: &c})
	if len(page) != 1 || len(page2) != 1 || page[0].ID != r2.ID || page2[0].ID != r1.ID {
		t.Errorf("newest keyset paging: %+v %+v", page, page2)
	}
	page, _, _ = s.Reviews(ctx, ReviewFilter{ReleaseID: rel, Sort: community.ReviewSortFinished, ViewerID: old})
	if len(page) != 1 || page[0].ID != r1.ID || page[0].LikedByMe {
		t.Errorf("finished filter: %+v", page)
	}
	body := "改了"
	if _, err := s.UpdateReview(ctx, r1.ID, fresh, ReviewEdit{Body: &body}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("edit someone else's review: %v", err)
	}
	up, err := s.UpdateReview(ctx, r1.ID, old, ReviewEdit{Body: &body, KeywordHit: true})
	if err != nil || up.Body != body || !up.Pending {
		t.Errorf("keyword edit must shadow: %+v %v", up, err)
	}

	// --- reports & moderation ---------------------------------------------------
	rep, created, err := s.CreateReport(ctx, "review", r2.ID, old, "spoiler", "")
	if d := rep.DueAt.Sub(rep.CreatedAt); err != nil || !created || d < 47*time.Hour || d > 49*time.Hour {
		t.Fatalf("report = %+v %v %v", rep, created, err)
	}
	if again, created, _ := s.CreateReport(ctx, "review", r2.ID, old, "spoiler", ""); created || again.ID != rep.ID {
		t.Error("duplicate open report")
	}
	if _, _, err := s.CreateReport(ctx, "danmaku", shadow.ID, old, "spam", ""); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("reporting an invisible danmaku: %v", err)
	}
	urgent, _, err := s.CreateReport(ctx, "release", rel, fresh, "underage", "")
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueReports, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	reps := items.([]Report)
	posUrgent, posRep := -1, -1
	for i, r := range reps {
		switch r.ID {
		case urgent.ID:
			posUrgent = i
		case rep.ID:
			posRep = i
			if r.Target == nil {
				t.Error("report without target preview")
			}
		}
	}
	if posUrgent < 0 || posRep < 0 || posUrgent > posRep {
		t.Errorf("reports must be ordered by SLA: urgent at %d, spoiler at %d", posUrgent, posRep)
	}

	res, err := s.Moderate(ctx, ModerationAction{TargetType: "review", ID: r2.ID, Action: community.ActionFold, Reason: "spoiler", Actor: "mod"})
	if err != nil || res.After != "folded" || res.ReportsResolved != 1 || res.EventID == 0 {
		t.Fatalf("fold = %+v %v", res, err)
	}
	var actor, action string
	s.QueryRow(ctx, `SELECT actor,action FROM review_events WHERE id=$1`, res.EventID).Scan(&actor, &action)
	if actor != "mod" || action != "moderation.fold" {
		t.Errorf("event %s %s", actor, action)
	}

	dq, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueDanmaku, Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, d := range dq.([]QueuedDanmaku) {
		found = found || d.ID == shadow.ID
	}
	if !found {
		t.Error("shadow danmaku missing from the queue")
	}
	if _, err := s.Moderate(ctx, ModerationAction{TargetType: "danmaku", ID: shadow.ID, Action: community.ActionApprove, Actor: "mod"}); err != nil {
		t.Fatal(err)
	}
	// delete + mute the spammer through one of their danmaku: mute row, lower
	// vote weight, curve re-queued and their history excluded.
	var spamDanmaku int64
	s.QueryRow(ctx, `SELECT id FROM danmaku WHERE user_id=$1 LIMIT 1`, spammer).Scan(&spamDanmaku)
	s.RateRelease(ctx, rel, community.Voter{UserID: spammer, AccountAge: 30 * 24 * time.Hour}, 1)
	res, err = s.Moderate(ctx, ModerationAction{TargetType: "danmaku", ID: spamDanmaku, Action: community.ActionDeleteMute, MuteDays: 3, Actor: "mod"})
	if err != nil || res.MutedUntil == nil || res.MutedUntil.Before(time.Now().Add(71*time.Hour)) {
		t.Fatalf("delete_mute = %+v %v", res, err)
	}
	p, err := s.PosterInfo(ctx, spammer)
	if err != nil || !p.Muted(time.Now()) || !p.Abnormal() {
		t.Errorf("poster = %+v %v", p, err)
	}
	s.QueryRow(ctx, `SELECT weight FROM ratings WHERE release_id=$1 AND user_id=$2`, rel, spammer).Scan(&w)
	if w > 0.11 {
		t.Errorf("muted user's vote weight %v", w)
	}
	if _, err := s.RecomputeReleaseCurves(ctx, rel); err != nil {
		t.Fatal(err)
	}
	s.QueryRow(ctx, `SELECT valid_count FROM danmaku_curves WHERE release_id=$1 AND version_key='main'`, rel).Scan(&valid)
	if valid != 62 { // 60 voters + old's "new" + the approved shadow one; spammer excluded
		t.Errorf("valid after mute = %d, want 62", valid)
	}
	if _, err := s.Moderate(ctx, ModerationAction{TargetType: "rating", ID: 1, Action: community.ActionFold, Actor: "mod"}); !errors.Is(err, ErrInvalidAction) {
		t.Errorf("fold on rating: %v", err)
	}
	if err := s.Unmute(ctx, spammer, "appeal", "mod"); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PosterInfo(ctx, spammer); p.Muted(time.Now()) {
		t.Error("still muted")
	}

	if _, err := s.ResolveReport(ctx, urgent.ID, "dismissed", "fine", "mod"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveReport(ctx, urgent.ID, "resolved", "", "mod"); !errors.Is(err, ErrReportClosed) {
		t.Errorf("resolve twice: %v", err)
	}
	if _, err := s.ModerationCounts(ctx); err != nil {
		t.Fatal(err)
	}

	// --- keyword list ------------------------------------------------------------
	word := "广告" + sfx[len(sfx)-6:]
	if _, err := s.ModerationMatcher(ctx); err != nil { // warm the cache
		t.Fatal(err)
	}
	k, err := s.AddModerationWord(ctx, " "+word+" ", "ad", "mod")
	if err != nil || k.Word != word {
		t.Fatalf("add word = %+v %v", k, err)
	}
	m, _ := s.ModerationMatcher(ctx)
	if _, hit := m.Match("这是" + word); !hit {
		t.Error("new word not active (cache not invalidated)")
	}
	if err := s.DeleteModerationWord(ctx, word, "mod"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModerationWord(ctx, word, "mod"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("delete missing word: %v", err)
	}
	m, _ = s.ModerationMatcher(ctx)
	if _, hit := m.Match("这是" + word); hit {
		t.Error("deleted word still active")
	}
	var events int
	s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='moderation_word' AND actor='mod' AND created_at > now()-interval '1 minute'`).Scan(&events)
	if events < 2 {
		t.Errorf("word events %d", events)
	}
}
