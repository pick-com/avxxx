package store

import (
	"avxxx/internal/community"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestClipsIntegration covers migration 054 (idempotent re-runs, the
// reports check), the duration check, public visibility, owner-only
// deletion, reports, moderation and the account export.
func TestClipsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "clips")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // re-running every migration must not fail
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var alice, bob int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES('a@t','x','Alice') RETURNING id`).Scan(&alice))
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('b@t','x') RETURNING id`).Scan(&bob))
	release := func(slug, status string, runtime int) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,$1,$2,NULLIF($3,0)) RETURNING id`, slug, status, runtime).Scan(&id))
		return id
	}
	withSource := release("src", "published", 120)
	must(func() error {
		_, err := s.Exec(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,duration_seconds) VALUES($1,'official_embed','p','main','https://e/m',100)`, withSource)
		return err
	}())
	release("runtime", "published", 2)
	release("unknown", "published", 0)
	hidden := release("draft", "draft", 10)

	// Duration: the main source (100 s) wins over the runtime (120 min).
	if _, err := s.CreateClip(ctx, alice, ClipInput{Release: "src", StartMs: 90_000, EndMs: 101_000}, "visible"); err == nil {
		t.Error("clip past the source duration accepted")
	}
	c, err := s.CreateClip(ctx, alice, ClipInput{Release: "src", StartMs: 85_000, EndMs: 100_000, Title: " 名场面 "}, "visible")
	must(err)
	if !ValidClipID(c.ID) || c.Title != "名场面" || c.ReleaseSlug != "src" || !c.Mine || c.RowID == 0 {
		t.Fatalf("created %+v", c)
	}
	// Runtime only: 2 min (rounded, so up to 2:59.999).
	if _, err := s.CreateClip(ctx, alice, ClipInput{Release: "runtime", StartMs: 150_000, EndMs: 170_000}, "visible"); err != nil {
		t.Errorf("clip within the runtime minute: %v", err)
	}
	if _, err := s.CreateClip(ctx, alice, ClipInput{Release: "runtime", StartMs: 170_000, EndMs: 185_000}, "visible"); err == nil {
		t.Error("clip past the runtime accepted")
	}
	if _, err := s.CreateClip(ctx, alice, ClipInput{Release: "unknown", StartMs: 9_000_000, EndMs: 9_030_000}, "visible"); err != nil {
		t.Errorf("unknown duration: %v", err)
	}
	if _, err := s.CreateClip(ctx, alice, ClipInput{Release: "draft", StartMs: 0, EndMs: 9000}, "visible"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unpublished release: %v, want ErrNoRows", err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO clips(public_id,release_id,user_id,start_ms,end_ms) VALUES('AAAAAAAAAA',$1,$2,0,61000)`, withSource, alice); err == nil {
		t.Error("table accepted a 61 s clip")
	}

	// Public read: anyone; Mine only for the author.
	got, err := s.ClipByPublicID(ctx, c.ID, 0)
	must(err)
	if got.Mine || got.AuthorName != "Alice" || got.StartMs != 85_000 || got.EndMs != 100_000 {
		t.Errorf("public read %+v", got)
	}
	if got, _ = s.ClipByPublicID(ctx, c.ID, alice); !got.Mine {
		t.Error("author read not Mine")
	}
	// An unpublished release hides its clips.
	var draftClip string
	must(s.QueryRow(ctx, `INSERT INTO clips(public_id,release_id,user_id,start_ms,end_ms) VALUES('BBBBBBBBBB',$1,$2,0,9000) RETURNING public_id`, hidden, alice).Scan(&draftClip))
	if _, err := s.ClipByPublicID(ctx, draftClip, alice); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("clip of a draft release: %v", err)
	}
	// A shadowed title (keyword hit, migration 055): the clip is public, the
	// title reaches its author only; TitleStatus is never told to others.
	sh, err := s.CreateClip(ctx, bob, ClipInput{Release: "src", StartMs: 0, EndMs: 9000, Title: "加微信 SHADOW-TITLE"}, ClipTitleShadow)
	must(err)
	if sh.Title == "" || sh.TitleStatus != ClipTitleShadow || sh.Status != "visible" {
		t.Errorf("created %+v", sh)
	}
	for _, viewer := range []int64{0, alice} {
		got, err := s.ClipByPublicID(ctx, sh.ID, viewer)
		if err != nil || got.Title != "" || got.TitleStatus != "" {
			t.Errorf("viewer %d sees %+v, %v", viewer, got, err)
		}
	}
	if got, err := s.ClipByPublicID(ctx, sh.ID, bob); err != nil || got.Title != "加微信 SHADOW-TITLE" || got.TitleStatus != ClipTitleShadow {
		t.Errorf("author sees %+v, %v", got, err)
	}
	if _, err := s.CreateClip(ctx, bob, ClipInput{Release: "src", StartMs: 0, EndMs: 9000, Title: "x"}, "removed"); err == nil {
		t.Error("created with title status removed")
	}
	if got, err := s.CreateClip(ctx, bob, ClipInput{Release: "src", StartMs: 0, EndMs: 9000}, ClipTitleShadow); err != nil || got.TitleStatus != ClipTitleVisible {
		t.Errorf("untitled clip with shadow status: %+v, %v", got, err)
	} else {
		must(s.DeleteClip(ctx, bob, got.ID))
	}

	// A shadowed clip (status shadow): author only.
	_, err = s.Exec(ctx, `UPDATE clips SET status='shadow' WHERE id=$1`, sh.RowID)
	must(err)
	if _, err := s.ClipByPublicID(ctx, sh.ID, alice); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("shadow clip visible to others: %v", err)
	}
	if _, err := s.ClipByPublicID(ctx, sh.ID, bob); err != nil {
		t.Errorf("shadow clip hidden from its author: %v", err)
	}

	// Reports and moderation (shared inbox).
	rep, created, err := s.CreateReport(ctx, "clip", c.RowID, bob, "spam", "")
	must(err)
	if !created || rep.TargetType != "clip" {
		t.Fatalf("report %+v created=%v", rep, created)
	}
	if _, _, err := s.CreateReport(ctx, "clip", sh.RowID, alice, "spam", ""); !errors.Is(err, ErrTargetNotFound) {
		t.Errorf("report on a shadow clip: %v", err)
	}
	if prev := s.reportTarget(ctx, "clip", c.RowID); prev == nil {
		t.Error("no queue preview for a clip")
	}
	res, err := s.Moderate(ctx, ModerationAction{TargetType: "clip", ID: c.RowID, Action: community.ActionDelete, Reason: "spam", Actor: "mod"})
	must(err)
	if res.Before != "visible" || res.After != "deleted" || res.ReportsResolved != 1 {
		t.Errorf("moderate %+v", res)
	}
	if _, err := s.ClipByPublicID(ctx, c.ID, alice); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("moderated clip still served: %v", err)
	}

	// 待审片段: clips with a shadowed title, oldest first; approve publishes
	// the title, remove_title clears it (review_events keeps the old text).
	ok1, err := s.CreateClip(ctx, alice, ClipInput{Release: "runtime", StartMs: 0, EndMs: 9000, Title: "审核通过的标题"}, ClipTitleShadow)
	must(err)
	rm, err := s.CreateClip(ctx, alice, ClipInput{Release: "runtime", StartMs: 10_000, EndMs: 19_000, Title: "要移除的标题"}, ClipTitleShadow)
	must(err)
	counts, err := s.ModerationCounts(ctx)
	must(err)
	if counts.Clips != 3 { // sh (a shadowed clip with a shadowed title), ok1, rm
		t.Errorf("clips queue count = %d", counts.Clips)
	}
	queue := func(status string) []QueuedClip {
		t.Helper()
		v, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueClips, Status: status})
		must(err)
		return v.([]QueuedClip)
	}
	if q := queue(""); len(q) != counts.Clips || q[len(q)-1].ID != rm.RowID || q[len(q)-1].Title != "要移除的标题" || q[len(q)-1].PublicID != rm.ID {
		t.Errorf("shadow queue %+v (count %d)", q, counts.Clips)
	}
	if _, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueClips, Status: "folded"}); err == nil {
		t.Error("bad clips filter accepted")
	}
	res, err = s.Moderate(ctx, ModerationAction{TargetType: "clip", ID: ok1.RowID, Action: community.ActionApprove, Actor: "mod"})
	must(err)
	if res.TitleBefore != ClipTitleShadow || res.TitleAfter != ClipTitleVisible || res.After != "visible" {
		t.Errorf("approve %+v", res)
	}
	if got, _ := s.ClipByPublicID(ctx, ok1.ID, bob); got.Title != "审核通过的标题" {
		t.Errorf("approved title not public: %+v", got)
	}
	res, err = s.Moderate(ctx, ModerationAction{TargetType: "clip", ID: rm.RowID, Action: community.ActionRemoveTitle, Reason: "ad", Actor: "mod"})
	must(err)
	if res.TitleAfter != ClipTitleRemoved || res.Before != "visible" || res.After != "visible" || res.EventID == 0 {
		t.Errorf("remove_title %+v", res)
	}
	if got, err := s.ClipByPublicID(ctx, rm.ID, alice); err != nil || got.Title != "" || got.TitleStatus != ClipTitleRemoved {
		t.Errorf("author after remove_title: %+v, %v", got, err)
	}
	if got, err := s.ClipByPublicID(ctx, rm.ID, bob); err != nil || got.Title != "" {
		t.Errorf("others after remove_title: %+v, %v", got, err)
	}
	var evBefore, evAfter map[string]any
	must(s.QueryRow(ctx, `SELECT before_value,after_value FROM review_events WHERE id=$1`, res.EventID).Scan(&evBefore, &evAfter))
	if evBefore["title"] != "要移除的标题" || evBefore["title_status"] != "shadow" || evAfter["title_status"] != "removed" {
		t.Errorf("review event %v -> %v", evBefore, evAfter)
	}
	if q := queue("removed"); len(q) != 1 || q[0].ID != rm.RowID {
		t.Errorf("removed filter %+v", q)
	}
	if q := queue(""); len(q) != counts.Clips-2 {
		t.Errorf("shadow queue after review: %+v", q)
	}

	// Account export lists the author's clips only.
	var buf bytes.Buffer
	must(s.WriteDataExport(ctx, alice, &buf))
	var export map[string]json.RawMessage
	must(json.Unmarshal(buf.Bytes(), &export))
	var clips []map[string]any
	must(json.Unmarshal(export["clips"], &clips))
	if len(clips) != 6 { // src (deleted by a moderator), runtime, unknown, draft, ok1, rm
		t.Errorf("exported clips = %d, want 6: %s", len(clips), export["clips"])
	}

	// 我的片段: newest first, one per page; the moderator-deleted clip and
	// the draft release's clip are left out, other users' clips never show.
	var seen []string
	var cur *Cursor
	for page := 0; ; page++ {
		items, next, err := s.MyClips(ctx, alice, cur, 1)
		must(err)
		for _, x := range items {
			if !x.Mine || x.ReleaseID == 0 || x.ReleaseTitle == "" {
				t.Errorf("my clip %+v", x)
			}
			seen = append(seen, x.ReleaseSlug+"/"+x.TitleStatus)
		}
		if next == "" {
			break
		}
		c, err := DecodeCursor(next)
		must(err)
		cur = &c
		if page > 5 {
			t.Fatal("pagination does not end")
		}
	}
	if want := "runtime/removed runtime/visible unknown/visible runtime/visible"; strings.Join(seen, " ") != want {
		t.Errorf("alice's clips = %v, want %s", seen, want)
	}
	if items, _, err := s.MyClips(ctx, bob, nil, 0); err != nil || len(items) != 1 || items[0].ID != sh.ID {
		t.Errorf("bob's clips = %+v, %v", items, err)
	}
	if _, _, err := s.MyClips(ctx, alice, &Cursor{Sort: "newest", Key: "x", ID: 1}, 0); err == nil {
		t.Error("foreign cursor accepted")
	}

	// Deletion: owner only.
	if err := s.DeleteClip(ctx, alice, sh.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("deleting someone else's clip: %v", err)
	}
	must(s.DeleteClip(ctx, bob, sh.ID))
	if err := s.DeleteClip(ctx, bob, sh.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("second delete: %v", err)
	}
	if err := s.DeleteClip(ctx, bob, "not-an-id"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("malformed id: %v", err)
	}
}
