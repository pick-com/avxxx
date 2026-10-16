package store

import (
	"avxxx/internal/community"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestInteractionsIntegration exercises migration 040 against a disposable
// database: 赞 / 踩 per voter with undo, comments with one level of replies,
// visibility (shadow, deleted placeholders), likes, reports, moderation, the
// reply notification, recs exclusion and the release merge.
func TestInteractionsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "interactions")
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
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		return v
	}
	user := func(name string) int64 {
		return id(`INSERT INTO users(email,password_hash,display_name,created_at) VALUES($1,'x',$2,now()-interval '30 days') RETURNING id`,
			name+"@secret.example", name)
	}
	release := func(slug string) int64 {
		return id(`INSERT INTO releases(slug,canonical_title,status) VALUES($1,$1,'published') RETURNING id`, slug)
	}
	ann, bob, cat := user("ann"), user("bob"), user("cat")
	rel, dup := release("ix-1"), release("ix-2")

	// --- 赞 / 踩 ---------------------------------------------------------------
	annV, bobV := community.Voter{UserID: ann}, community.Voter{UserID: bob}
	dev := community.Voter{DeviceID: "device-0001"}
	must(s.SetReaction(ctx, rel, annV, 1))
	must(s.SetReaction(ctx, rel, bobV, -1))
	must(s.SetReaction(ctx, rel, dev, 1))
	must(s.SetReaction(ctx, rel, dev, 1)) // idempotent: still one vote
	sum, err := s.ReactionSummary(ctx, rel, community.VoterKey(bobV))
	if err != nil || sum.Likes != 2 || sum.MyVote != -1 {
		t.Fatalf("summary = %+v %v", sum, err)
	}
	must(s.SetReaction(ctx, rel, bobV, 1)) // switch 踩 -> 赞
	must(s.SetReaction(ctx, rel, dev, 0))  // undo
	if sum, _ = s.ReactionSummary(ctx, rel, ""); sum.Likes != 2 || sum.MyVote != 0 {
		t.Fatalf("after switch + undo = %+v", sum)
	}
	if err := s.SetReaction(ctx, rel, annV, 2); err == nil {
		t.Fatal("value 2 accepted")
	}

	// 踩 removes a release from the user's recommendations.
	must(s.SetReaction(ctx, dup, community.Voter{UserID: cat}, -1))
	rec, err := s.Recall(ctx, RecQuery{Slot: "home", UserID: cat})
	if err != nil || !rec.Watched[dup] {
		t.Fatalf("disliked release must be filtered: %v %v", rec.Watched, err)
	}

	// --- comments ------------------------------------------------------------
	top, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, UserID: ann, Body: "first", Status: community.StatusVisible})
	must(err)
	if !top.Mine || top.ParentID != nil || top.Author.DisplayName != "ann" {
		t.Fatalf("created = %+v", top)
	}
	reply, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, ParentID: top.ID, UserID: bob, Body: "reply", Status: community.StatusVisible})
	must(err)
	// A reply to a reply joins the thread's top-level comment.
	nested, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, ParentID: reply.ID, UserID: cat, Body: "nested", Spoiler: true, Status: community.StatusVisible})
	must(err)
	if nested.ParentID == nil || *nested.ParentID != top.ID || !nested.Spoiler {
		t.Fatalf("nested reply = %+v", nested)
	}
	shadow, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, UserID: cat, Body: "spam link", Status: community.StatusShadow})
	must(err)
	if !shadow.Pending {
		t.Fatalf("shadow = %+v", shadow)
	}
	if _, err := s.CreateComment(ctx, NewComment{ReleaseID: dup, ParentID: top.ID, UserID: bob, Body: "x", Status: community.StatusVisible}); !errors.Is(err, ErrCommentsParent) {
		t.Fatalf("parent on another release = %v", err)
	}
	if _, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, ParentID: shadow.ID, UserID: bob, Body: "x", Status: community.StatusVisible}); !errors.Is(err, ErrCommentsParent) {
		t.Fatalf("reply to someone else's shadow comment = %v", err)
	}

	list := func(viewer int64, sort string) []Comment {
		t.Helper()
		items, _, err := s.Comments(ctx, CommentFilter{ReleaseID: rel, Sort: sort, ViewerID: viewer})
		must(err)
		return items
	}
	if got := list(0, community.CommentSortNewest); len(got) != 1 || got[0].ID != top.ID || got[0].ReplyCount != 2 || len(got[0].Replies) != 2 {
		t.Fatalf("anonymous list = %+v", got)
	}
	if got := list(cat, community.CommentSortNewest); len(got) != 2 || got[0].ID != shadow.ID || !got[0].Mine {
		t.Fatalf("author sees their shadow comment first = %+v", got)
	}

	// Likes: once per user, undoable; "top" follows likes.
	second, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, UserID: bob, Body: "second", Status: community.StatusVisible})
	must(err)
	for _, u := range []int64{ann, cat, cat} {
		if _, err := s.SetCommentLike(ctx, second.ID, u, true); err != nil {
			t.Fatal(err)
		}
	}
	if n, _ := s.SetCommentLike(ctx, second.ID, ann, false); n != 1 {
		t.Fatalf("likes after unlike = %d", n)
	}
	if _, err := s.SetCommentLike(ctx, shadow.ID, ann, true); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("like a shadow comment = %v", err)
	}
	if got := list(cat, community.CommentSortTop); got[0].ID != second.ID || !got[0].LikedByMe || got[0].Likes != 1 {
		t.Fatalf("top = %+v", got[0])
	}
	if got := list(0, community.CommentSortNewest); got[0].ID != second.ID {
		t.Fatalf("newest = %+v", got[0])
	}
	// Paging by keyset (newest) and offset (top).
	for _, sort := range []string{community.CommentSortNewest, community.CommentSortTop} {
		p1, next, err := s.Comments(ctx, CommentFilter{ReleaseID: rel, Sort: sort, Limit: 1})
		must(err)
		cur, err := DecodeCursor(next)
		must(err)
		p2, next2, err := s.Comments(ctx, CommentFilter{ReleaseID: rel, Sort: sort, Limit: 1, Cursor: &cur})
		must(err)
		if len(p1) != 1 || len(p2) != 1 || p1[0].ID == p2[0].ID || next2 != "" {
			t.Fatalf("%s pages = %+v / %+v (next %q)", sort, p1, p2, next2)
		}
	}
	replies, next, err := s.CommentReplies(ctx, top.ID, 0, nil, 1)
	must(err)
	cur, err := DecodeCursor(next)
	must(err)
	more, _, err := s.CommentReplies(ctx, top.ID, 0, &cur, 10)
	must(err)
	if len(replies) != 1 || replies[0].ID != reply.ID || len(more) != 1 || more[0].ID != nested.ID {
		t.Fatalf("replies = %+v / %+v", replies, more)
	}
	if _, _, err := s.CommentReplies(ctx, shadow.ID, ann, nil, 10); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("replies of someone else's shadow comment = %v", err)
	}

	// Author soft delete: owner only; a deleted parent with live replies stays
	// as a placeholder without body or author.
	if err := s.DeleteComment(ctx, top.ID, bob); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("delete someone else's = %v", err)
	}
	must(s.DeleteComment(ctx, top.ID, ann))
	if err := s.DeleteComment(ctx, top.ID, ann); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("delete twice = %v", err)
	}
	var placeholder *Comment
	for _, c := range list(0, community.CommentSortNewest) {
		if c.ID == top.ID {
			placeholder = &c
		}
	}
	if placeholder == nil || !placeholder.Deleted || placeholder.Body != "" || placeholder.Author.ID != 0 || placeholder.Author.DisplayName != "" || len(placeholder.Replies) != 2 {
		t.Fatalf("placeholder = %+v", placeholder)
	}

	// The reply notified ann once per thread (bob and cat, merged; her own
	// thread, so no notice for her own comments).
	var count int
	must(s.QueryRow(ctx, `SELECT (payload->>'count')::int FROM notifications WHERE user_id=$1 AND kind='comment_reply'`, ann).Scan(&count))
	if count != 2 {
		t.Fatalf("reply notice count = %d", count)
	}

	// --- reports and moderation ----------------------------------------------
	r, created, err := s.CreateReport(ctx, "comment", second.ID, ann, "spam", "")
	if err != nil || !created || r.TargetType != "comment" {
		t.Fatalf("report = %+v %v %v", r, created, err)
	}
	if _, _, err := s.CreateReport(ctx, "comment", shadow.ID, ann, "spam", ""); !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("report a hidden comment = %v", err)
	}
	counts, err := s.ModerationCounts(ctx)
	if err != nil || counts.Comments != 1 || counts.Reports != 1 {
		t.Fatalf("counts = %+v %v", counts, err)
	}
	q, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueComments})
	if items := q.([]QueuedComment); err != nil || len(items) != 1 || items[0].ID != shadow.ID {
		t.Fatalf("comment queue = %+v %v", q, err)
	}
	rq, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueReports})
	if items := rq.([]Report); err != nil || len(items) != 1 || !strings.Contains(string(items[0].Target.(json.RawMessage)), `"second"`) {
		t.Fatalf("report queue preview = %+v %v", rq, err)
	}
	res, err := s.Moderate(ctx, ModerationAction{TargetType: "comment", ID: second.ID, Action: community.ActionFold, Actor: "mod"})
	if err != nil || res.ReportsResolved != 1 || res.After != community.StatusFolded {
		t.Fatalf("fold = %+v %v", res, err)
	}
	if got := list(0, community.CommentSortTop); got[0].ID != second.ID || !got[0].Spoiler {
		t.Fatalf("folded comment = %+v", got[0])
	}
	if _, err := s.Moderate(ctx, ModerationAction{TargetType: "comment", ID: shadow.ID, Action: community.ActionDeleteMute, MuteDays: 3, Actor: "mod"}); err != nil {
		t.Fatal(err)
	}
	if n := id(`SELECT count(*) FROM review_events WHERE entity_type='comment' AND actor='mod'`); n != 2 {
		t.Fatalf("review_events for comments = %d", n)
	}
	if got := list(cat, community.CommentSortNewest); len(got) != 2 {
		t.Fatalf("deleted shadow comment still listed for its author: %+v", got)
	}

	// --- release merge moves votes and comments --------------------------------
	must(s.SetReaction(ctx, dup, annV, 1)) // ann voted on both: the target's vote wins
	dupComment, err := s.CreateComment(ctx, NewComment{ReleaseID: dup, UserID: bob, Body: "on the duplicate", Status: community.StatusVisible})
	must(err)
	cand := id(`INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence) VALUES('release',$1,$2,0.9,'{}') RETURNING id`, rel, dup)
	mr, err := s.MergeCandidate(ctx, cand, rel, "mod", "dup")
	must(err)
	if mr.Moved["comments"] != 1 || mr.Moved["release_reactions"] != 1 || mr.Deduplicated["release_reactions"] != 1 {
		t.Fatalf("merge = moved %v dedup %v", mr.Moved, mr.Deduplicated)
	}
	if n := id(`SELECT count(*) FROM comments WHERE id=$1 AND release_id=$2`, dupComment.ID, rel); n != 1 {
		t.Fatal("comment did not move")
	}
	if sum, _ = s.ReactionSummary(ctx, rel, community.VoterKey(community.Voter{UserID: cat})); sum.Likes != 2 || sum.MyVote != -1 {
		t.Fatalf("merged votes = %+v", sum)
	}
	if _, err := s.UndoMerge(ctx, mr.ReviewEventID, "mod", "oops"); err != nil {
		t.Fatalf("undo merge: %v", err)
	}
	if n := id(`SELECT count(*) FROM release_reactions WHERE release_id=$1`, dup); n != 2 {
		t.Fatalf("undo restored %d votes on the duplicate", n)
	}
}
