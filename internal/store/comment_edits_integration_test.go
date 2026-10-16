package store

import (
	"context"
	"errors"
	"testing"

	"avxxx/internal/community"

	"github.com/jackc/pgx/v5"
)

// TestCommentEditIntegration covers migration 048's comment editing: the
// 10-minute window, owner only, the blocked-word re-check, the 已编辑
// marker and the moderator-only version history.
func TestCommentEditIntegration(t *testing.T) {
	openIsolatedTestDB(t, "comment_edits")
	ctx := context.Background()
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
	var alice, bob, rel int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES('a@t','x','Alice') RETURNING id`).Scan(&alice))
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('b@t','x') RETURNING id`).Scan(&bob))
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('ce-1','T','published') RETURNING id`).Scan(&rel))
	c, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, UserID: alice, Body: "frist", Status: community.StatusVisible})
	must(err)
	if c.EditedAt != nil || c.EditableUntil == nil || c.EditableUntil.Sub(c.CreatedAt) != CommentEditWindow {
		t.Fatalf("new comment = %+v", c)
	}

	spoiler := true
	e, err := s.EditComment(ctx, c.ID, alice, CommentEdit{Body: "first", Spoiler: &spoiler})
	must(err)
	if e.Body != "first" || !e.Spoiler || e.EditedAt == nil || e.Pending {
		t.Errorf("edited = %+v", e)
	}
	// Others see the marker but not the edit window.
	list, _, err := s.Comments(ctx, CommentFilter{ReleaseID: rel, Sort: community.CommentSortNewest, ViewerID: bob})
	must(err)
	if len(list) != 1 || list[0].EditedAt == nil || list[0].EditableUntil != nil || list[0].Body != "first" {
		t.Errorf("bob sees %+v", list)
	}
	if _, err := s.EditComment(ctx, c.ID, bob, CommentEdit{Body: "mine now"}); !errors.Is(err, ErrNotOwner) {
		t.Errorf("bob edits alice's comment: %v", err)
	}
	// Unchanged: no new version.
	_, err = s.EditComment(ctx, c.ID, alice, CommentEdit{Body: "first"})
	must(err)
	// A blocked-word hit shadows it; removing the word does not unshadow it.
	e, err = s.EditComment(ctx, c.ID, alice, CommentEdit{Body: "加微信", KeywordHit: true})
	must(err)
	if !e.Pending {
		t.Errorf("keyword edit not shadowed: %+v", e)
	}
	e, err = s.EditComment(ctx, c.ID, alice, CommentEdit{Body: "clean again"})
	must(err)
	if !e.Pending {
		t.Errorf("an edit must not lift a shadow: %+v", e)
	}

	h, err := s.CommentEdits(ctx, c.ID)
	must(err)
	if h.Body != "clean again" || h.Status != community.StatusShadow || len(h.Versions) != 3 ||
		h.Versions[0].Body != "frist" || h.Versions[0].Spoiler || h.Versions[1].Body != "first" || h.Versions[2].Body != "加微信" ||
		h.Versions[2].Status != community.StatusShadow {
		t.Errorf("history = %+v", h)
	}

	// The window closes 10 minutes after posting.
	_, err = s.Exec(ctx, `UPDATE comments SET created_at=now()-interval '11 minutes' WHERE id=$1`, c.ID)
	must(err)
	if _, err := s.EditComment(ctx, c.ID, alice, CommentEdit{Body: "late"}); !errors.Is(err, ErrEditWindow) {
		t.Errorf("edit after the window: %v", err)
	}
	// The edit cap.
	c2, err := s.CreateComment(ctx, NewComment{ReleaseID: rel, UserID: alice, Body: "v0", Status: community.StatusVisible})
	must(err)
	for i := range MaxCommentEdits {
		_, err := s.EditComment(ctx, c2.ID, alice, CommentEdit{Body: "v" + string(rune('a'+i))})
		must(err)
	}
	var ce *ConflictError
	if _, err := s.EditComment(ctx, c2.ID, alice, CommentEdit{Body: "one more"}); !errors.As(err, &ce) {
		t.Errorf("edit past the cap: %v", err)
	}
	// Deleted comments cannot be edited; the history stays for moderators.
	must(s.DeleteComment(ctx, c2.ID, alice))
	if _, err := s.EditComment(ctx, c2.ID, alice, CommentEdit{Body: "back"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("edit deleted: %v", err)
	}
	if h, err := s.CommentEdits(ctx, c2.ID); err != nil || len(h.Versions) != MaxCommentEdits {
		t.Errorf("deleted comment history: %d versions, %v", len(h.Versions), err)
	}
	if _, err := s.CommentEdits(ctx, 999999); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("missing comment history: %v", err)
	}
	// The moderation queue shows the edit count.
	q, _, err := s.ModerationQueue(ctx, QueueFilter{Kind: QueueComments})
	must(err)
	items := q.([]QueuedComment)
	if len(items) != 1 || items[0].ID != c.ID || items[0].Edits != 3 || items[0].EditedAt == nil {
		t.Errorf("queue = %+v", items)
	}
}
