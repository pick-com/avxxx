package store

// 社交互动 (PRD 5.4; tech plan 3.13; migration 040): 赞 / 踩 on releases and
// comments with one level of replies. Comments share the posting checks,
// keyword list, report inbox and moderation actions of danmaku and reviews
// (community.go, moderation.go).

import (
	"avxxx/internal/community"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------
// 赞 / 踩

// ReactionSummary is the public view of a release's votes. Only the 赞 count
// is public: PRD 5.4 asks for two separate buttons but says nothing about
// showing 踩, so the dislike count stays private (it feeds recommendations
// and the admin side only), as on most video sites.
type ReactionSummary struct {
	Likes  int `json:"likes"`
	MyVote int `json:"my_vote"` // 1 赞, -1 踩, 0 none
}

// ReactionSummary counts a release's 赞 and the caller's own vote (voterKey
// "" for nobody).
func (s *Store) ReactionSummary(ctx context.Context, releaseID int64, voterKey string) (ReactionSummary, error) {
	var out ReactionSummary
	err := s.QueryRow(ctx, `SELECT count(*) FILTER (WHERE value=1),
		COALESCE((SELECT value FROM release_reactions WHERE release_id=$1 AND voter_key=$2), 0)
		FROM release_reactions WHERE release_id=$1`, releaseID, voterKey).Scan(&out.Likes, &out.MyVote)
	return out, err
}

// SetReaction records the voter's 赞 (1) or 踩 (-1), replacing an earlier
// vote; 0 removes it (undo).
func (s *Store) SetReaction(ctx context.Context, releaseID int64, v community.Voter, value int) error {
	key := community.VoterKey(v)
	if value == 0 {
		_, err := s.Exec(ctx, `DELETE FROM release_reactions WHERE release_id=$1 AND voter_key=$2`, releaseID, key)
		return err
	}
	if value != 1 && value != -1 {
		return invalidQuery("value must be 1, -1 or 0")
	}
	var userID *int64
	var device *string
	if v.UserID != 0 {
		userID = &v.UserID
	} else {
		h := community.DeviceHash(v.DeviceID)
		device = &h
	}
	_, err := s.Exec(ctx, `INSERT INTO release_reactions(release_id,voter_key,user_id,device_id,value) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (release_id,voter_key) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`,
		releaseID, key, userID, device, value)
	return err
}

// ---------------------------------------------------------------------------
// Comments

// ErrCommentsParent: a reply names a parent that is missing, hidden or on
// another release.
var ErrCommentsParent = errors.New("parent comment not found")

// Comment is one comment as returned to clients. A deleted comment that
// still has live replies is a placeholder: Deleted, no body, no author.
type Comment struct {
	ID         int64        `json:"id"`
	ParentID   *int64       `json:"parent_id"`
	Body       string       `json:"body"`
	Spoiler    bool         `json:"spoiler"`
	Likes      int          `json:"likes"`
	LikedByMe  bool         `json:"liked_by_me"`
	Mine       bool         `json:"mine"`
	Pending    bool         `json:"pending"` // shadow: only the author sees it
	Deleted    bool         `json:"deleted"`
	Author     ReviewAuthor `json:"author"`
	ReplyCount int          `json:"reply_count"`
	Replies    []Comment    `json:"replies,omitempty"` // top level: the first replies, oldest first
	CreatedAt  time.Time    `json:"created_at"`
	// EditedAt is the last edit (the 「已编辑」 marker); nil when never edited.
	EditedAt *time.Time `json:"edited_at"`
	// EditableUntil is set on the viewer's own comments: the end of the
	// edit window (CommentEditWindow after posting).
	EditableUntil *time.Time `json:"editable_until,omitempty"`
}

// CommentFilter selects a page of top-level comments.
type CommentFilter struct {
	ReleaseID int64
	Sort      string // community.CommentSort*
	Cursor    *Cursor
	Limit     int
	ViewerID  int64 // 0 for anonymous
}

const (
	defaultCommentLimit = 20
	maxCommentLimit     = 50
	maxTopOffset        = 1000
	// replyPreview is how many replies ride along with each top-level comment.
	replyPreview = 3
)

// commentVisible is who may see a comment row c: everyone for visible and
// folded, the author for shadow. $1 is the viewer (0 for anonymous).
const commentVisible = `(c.status IN ('visible','folded') OR (c.status='shadow' AND c.user_id=$1))`

// commentListed adds the placeholders: a deleted top-level comment stays in
// the list while it has a visible reply, so the thread keeps its context.
const commentListed = `(` + commentVisible + ` OR (c.status='deleted' AND EXISTS(
	SELECT 1 FROM comments r WHERE r.parent_id=c.id AND r.status IN ('visible','folded'))))`

// commentColumns never expose the body or author of a deleted comment. $1 is
// the viewer.
const commentColumns = `c.id,c.parent_id,
	CASE WHEN c.status='deleted' THEN '' ELSE c.body END,
	c.status<>'deleted' AND (c.spoiler OR c.status='folded'),
	CASE WHEN c.status='deleted' THEN 0 ELSE c.likes END,
	c.status<>'deleted' AND EXISTS(SELECT 1 FROM comment_likes l WHERE l.comment_id=c.id AND l.user_id=$1),
	c.status<>'deleted' AND c.user_id=$1, c.status='shadow', c.status='deleted',
	CASE WHEN c.status='deleted' THEN 0 ELSE u.id END,
	CASE WHEN c.status='deleted' THEN '' ELSE u.display_name END,
	CASE WHEN c.parent_id IS NOT NULL THEN 0 ELSE
		(SELECT count(*) FROM comments r WHERE r.parent_id=c.id AND (r.status IN ('visible','folded') OR (r.status='shadow' AND r.user_id=$1))) END,
	c.created_at,
	CASE WHEN c.status='deleted' THEN NULL ELSE c.edited_at END,
	CASE WHEN c.status<>'deleted' AND c.user_id=$1 THEN c.created_at + interval '` + commentEditWindowSQL + `' END`

func scanComment(r rowScanner) (Comment, error) {
	var x Comment
	err := r.Scan(&x.ID, &x.ParentID, &x.Body, &x.Spoiler, &x.Likes, &x.LikedByMe, &x.Mine, &x.Pending, &x.Deleted,
		&x.Author.ID, &x.Author.DisplayName, &x.ReplyCount, &x.CreatedAt, &x.EditedAt, &x.EditableUntil)
	return x, err
}

func collectComments(rows pgx.Rows) ([]Comment, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Comment, error) { return scanComment(r) })
}

// commentTopOrder is the 热门 (top) order. Migration 058's comments_top
// index has the same expression and directions, so a page reads the
// release's comments in order instead of sorting all of them.
const commentTopOrder = `CASE WHEN c.status='deleted' THEN 0 ELSE c.likes END DESC, c.created_at DESC, c.id DESC`

// commentListSQL is the page query of Comments; LIMIT and OFFSET are the
// last two of nargs parameters.
func commentListSQL(where, order string, nargs int) string {
	return fmt.Sprintf(`SELECT %s FROM comments c JOIN users u ON u.id=c.user_id WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		commentColumns, where, order, nargs-1, nargs)
}

// Comments lists a page of top-level comments with their first replies.
// "newest" uses a keyset cursor; "top" (likes) changes as people like, so its
// cursor is an offset.
func (s *Store) Comments(ctx context.Context, f CommentFilter) ([]Comment, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultCommentLimit
	}
	limit = min(limit, maxCommentLimit)
	if !community.ValidCommentSort(f.Sort) {
		return nil, "", invalidQuery("invalid sort")
	}
	if f.Cursor != nil && f.Cursor.Sort != f.Sort {
		return nil, "", invalidQuery("cursor does not match sort")
	}
	args := []any{f.ViewerID, f.ReleaseID}
	where := `c.release_id=$2 AND c.parent_id IS NULL AND ` + commentListed
	order := `c.created_at DESC, c.id DESC`
	offset := 0
	switch f.Sort {
	case community.CommentSortTop:
		order = commentTopOrder
		if f.Cursor != nil {
			n, err := strconv.Atoi(f.Cursor.Key)
			if err != nil || n < 0 || n > maxTopOffset {
				return nil, "", invalidQuery("malformed cursor")
			}
			offset = n
		}
	default:
		if f.Cursor != nil {
			t, err := time.Parse(time.RFC3339Nano, f.Cursor.Key)
			if err != nil {
				return nil, "", invalidQuery("malformed cursor")
			}
			args = append(args, t, f.Cursor.ID)
			where += ` AND (c.created_at, c.id) < ($3, $4)`
		}
	}
	args = append(args, limit+1, offset)
	rows, err := s.Query(ctx, commentListSQL(where, order, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	out, err := collectComments(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		c := Cursor{Sort: f.Sort, ID: last.ID}
		if f.Sort == community.CommentSortTop {
			if offset+limit <= maxTopOffset {
				c.Key = strconv.Itoa(offset + limit)
				next = EncodeCursor(c)
			}
		} else {
			c.Key = last.CreatedAt.UTC().Format(time.RFC3339Nano)
			next = EncodeCursor(c)
		}
	}
	if err := s.attachReplies(ctx, out, f.ViewerID); err != nil {
		return nil, "", err
	}
	if out == nil {
		out = []Comment{}
	}
	return out, next, nil
}

// attachReplies fills the first replies of each top-level comment.
func (s *Store) attachReplies(ctx context.Context, top []Comment, viewerID int64) error {
	var ids []int64
	at := map[int64]int{}
	for i, c := range top {
		if c.ReplyCount > 0 {
			ids = append(ids, c.ID)
			at[c.ID] = i
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.Query(ctx, `SELECT `+commentColumns+` FROM (
			SELECT c.*, row_number() OVER (PARTITION BY c.parent_id ORDER BY c.created_at, c.id) AS rn
			FROM comments c WHERE c.parent_id = ANY($2) AND `+commentVisible+`) c
		JOIN users u ON u.id=c.user_id WHERE c.rn <= $3 ORDER BY c.parent_id, c.created_at, c.id`, viewerID, ids, replyPreview)
	if err != nil {
		return err
	}
	replies, err := collectComments(rows)
	if err != nil {
		return err
	}
	for _, r := range replies {
		if r.ParentID != nil {
			i := at[*r.ParentID]
			top[i].Replies = append(top[i].Replies, r)
		}
	}
	return nil
}

// CommentReplies lists the replies of a top-level comment, oldest first,
// after a keyset cursor. The parent must be listed for the viewer
// (pgx.ErrNoRows otherwise).
func (s *Store) CommentReplies(ctx context.Context, parentID, viewerID int64, c *Cursor, limit int) ([]Comment, string, error) {
	if limit <= 0 {
		limit = defaultCommentLimit
	}
	limit = min(limit, maxCommentLimit)
	var ok bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM comments c JOIN releases r ON r.id=c.release_id
		WHERE c.id=$2 AND c.parent_id IS NULL AND r.status='published' AND r.merged_into_id IS NULL AND `+commentListed+`)`,
		viewerID, parentID).Scan(&ok); err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", pgx.ErrNoRows
	}
	args := []any{viewerID, parentID}
	where := `c.parent_id=$2 AND ` + commentVisible
	if c != nil {
		t, err := time.Parse(time.RFC3339Nano, c.Key)
		if err != nil || c.Sort != "replies" {
			return nil, "", invalidQuery("malformed cursor")
		}
		args = append(args, t, c.ID)
		where += ` AND (c.created_at, c.id) > ($3, $4)`
	}
	args = append(args, limit+1)
	rows, err := s.Query(ctx, fmt.Sprintf(`SELECT %s FROM comments c JOIN users u ON u.id=c.user_id WHERE %s ORDER BY c.created_at, c.id LIMIT $%d`,
		commentColumns, where, len(args)), args...)
	if err != nil {
		return nil, "", err
	}
	out, err := collectComments(rows)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = EncodeCursor(Cursor{Sort: "replies", Key: last.CreatedAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
	}
	if out == nil {
		out = []Comment{}
	}
	return out, next, nil
}

// NewComment is a comment to store. ParentID 0 is a top-level comment; a
// reply to a reply is attached to that reply's top-level comment (threads
// are one level deep). Status is visible, or shadow after a keyword hit.
type NewComment struct {
	ReleaseID int64
	ParentID  int64
	UserID    int64
	Body      string
	Spoiler   bool
	Status    string
}

func (s *Store) commentByID(ctx context.Context, q dbtx, id, viewerID int64) (Comment, error) {
	return scanComment(q.QueryRow(ctx, `SELECT `+commentColumns+` FROM comments c JOIN users u ON u.id=c.user_id WHERE c.id=$2`, viewerID, id))
}

// CreateComment stores a comment. A visible reply notifies the parent's
// author in the app (notify kind comment_reply, merged per thread) unless
// they replied to themselves or turned that notification off.
func (s *Store) CreateComment(ctx context.Context, n NewComment) (Comment, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback(ctx)
	var parent *int64
	var parentAuthor int64
	if n.ParentID != 0 {
		var root *int64
		var releaseID int64
		err := tx.QueryRow(ctx, `SELECT c.parent_id,c.release_id,c.user_id FROM comments c WHERE c.id=$2 AND `+commentVisible,
			n.UserID, n.ParentID).Scan(&root, &releaseID, &parentAuthor)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && releaseID != n.ReleaseID {
			return Comment{}, ErrCommentsParent
		}
		if err != nil {
			return Comment{}, err
		}
		id := n.ParentID
		if root != nil {
			// Replying to a reply: the thread's top-level comment is the parent.
			id = *root
			if err := tx.QueryRow(ctx, `SELECT user_id FROM comments WHERE id=$1`, id).Scan(&parentAuthor); err != nil {
				return Comment{}, err
			}
		}
		parent = &id
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO comments(release_id,parent_id,user_id,body,spoiler,status) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		n.ReleaseID, parent, n.UserID, n.Body, n.Spoiler, n.Status).Scan(&id); err != nil {
		return Comment{}, err
	}
	if parent != nil && parentAuthor != n.UserID && n.Status == community.StatusVisible {
		if err = s.notifyReply(ctx, tx, parentAuthor, n.ReleaseID, *parent); err != nil {
			return Comment{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return Comment{}, err
	}
	return s.commentByID(ctx, s, id, n.UserID)
}

// notifyReply adds (or bumps) the in-app comment_reply notification of one
// thread: one unread row per user and top-level comment, counting replies.
func (s *Store) notifyReply(ctx context.Context, tx pgx.Tx, userID, releaseID, threadID int64) error {
	settings, err := s.Settings(ctx, userID)
	if err != nil {
		return err
	}
	if !settings.NotifyPrefs.Enabled("comment_reply", "in_app") {
		return nil
	}
	_, err = tx.Exec(ctx, `INSERT INTO notifications(user_id,kind,payload,merged_key)
		SELECT $1,'comment_reply',jsonb_build_object('kind','comment_reply','comment_id',$3::bigint,'count',1,
			'items',jsonb_build_array(jsonb_build_object('slug',r.slug,'title',r.canonical_title))),
			'comment_reply:'||$3::text
		FROM releases r WHERE r.id=$2
		ON CONFLICT (user_id,merged_key) WHERE merged_key IS NOT NULL DO UPDATE SET
			payload=jsonb_set(notifications.payload,'{count}',to_jsonb(COALESCE((notifications.payload->>'count')::int,0)+1)),
			read_at=NULL,updated_at=now()`, userID, releaseID, threadID)
	return err
}

// CommentEditWindow is how long after posting the author may edit a comment.
const CommentEditWindow = 10 * time.Minute

// commentEditWindowSQL is CommentEditWindow as an interval literal.
const commentEditWindowSQL = "600 seconds"

// MaxCommentEdits caps the stored versions of one comment.
const MaxCommentEdits = 20

// ErrEditWindow: the comment is older than CommentEditWindow.
var ErrEditWindow = errors.New("comments can only be edited within 10 minutes of posting")

// CommentEdit is the author's change. KeywordHit (the new body matched the
// blocked-word list) shadows a visible comment until a moderator looks.
type CommentEdit struct {
	Body       string
	Spoiler    *bool
	KeywordHit bool
}

// EditComment changes the user's own comment within CommentEditWindow of
// posting. The previous version goes to comment_edits (moderators see the
// history) and edited_at is set. A shadowed comment stays shadowed and a
// folded one stays folded; an unchanged edit is a no-op. Errors:
// ErrNotOwner, pgx.ErrNoRows (missing or deleted), ErrEditWindow, and a
// conflict after MaxCommentEdits edits.
func (s *Store) EditComment(ctx context.Context, commentID, userID int64, e CommentEdit) (Comment, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback(ctx)
	var owner int64
	var body, status string
	var spoiler, open bool
	if err = tx.QueryRow(ctx, `SELECT user_id,body,spoiler,status,created_at > now() - interval '`+commentEditWindowSQL+`'
		FROM comments WHERE id=$1 AND status<>'deleted' FOR UPDATE`, commentID).Scan(&owner, &body, &spoiler, &status, &open); err != nil {
		return Comment{}, err
	}
	if owner != userID {
		return Comment{}, ErrNotOwner
	}
	if !open {
		return Comment{}, ErrEditWindow
	}
	newSpoiler := spoiler
	if e.Spoiler != nil {
		newSpoiler = *e.Spoiler
	}
	if e.Body == body && newSpoiler == spoiler {
		return s.commentByID(ctx, tx, commentID, userID)
	}
	var n int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM comment_edits WHERE comment_id=$1`, commentID).Scan(&n); err != nil {
		return Comment{}, err
	}
	if n >= MaxCommentEdits {
		return Comment{}, conflict("a comment can be edited at most %d times", MaxCommentEdits)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO comment_edits(comment_id,body,spoiler,status) VALUES($1,$2,$3,$4)`, commentID, body, spoiler, status); err != nil {
		return Comment{}, err
	}
	if e.KeywordHit && status == community.StatusVisible {
		status = community.StatusShadow
	}
	if _, err = tx.Exec(ctx, `UPDATE comments SET body=$2,spoiler=$3,status=$4,edited_at=now(),updated_at=now() WHERE id=$1`,
		commentID, e.Body, newSpoiler, status); err != nil {
		return Comment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Comment{}, err
	}
	return s.commentByID(ctx, s, commentID, userID)
}

// CommentVersion is one earlier version of a comment (moderation only).
type CommentVersion struct {
	Body       string    `json:"body"`
	Spoiler    bool      `json:"spoiler"`
	Status     string    `json:"status"`
	ReplacedAt time.Time `json:"replaced_at"`
}

// CommentHistory is a comment's current text and its earlier versions,
// oldest first, for moderators (pgx.ErrNoRows when the comment is missing).
type CommentHistory struct {
	ID        int64            `json:"id"`
	UserID    int64            `json:"user_id"`
	Body      string           `json:"body"`
	Spoiler   bool             `json:"spoiler"`
	Status    string           `json:"status"`
	CreatedAt time.Time        `json:"created_at"`
	EditedAt  *time.Time       `json:"edited_at"`
	Versions  []CommentVersion `json:"versions"`
}

// CommentEdits returns a comment with its edit history (moderators see
// deleted comments' text too).
func (s *Store) CommentEdits(ctx context.Context, commentID int64) (CommentHistory, error) {
	var h CommentHistory
	if err := s.QueryRow(ctx, `SELECT id,user_id,body,spoiler,status,created_at,edited_at FROM comments WHERE id=$1`, commentID).
		Scan(&h.ID, &h.UserID, &h.Body, &h.Spoiler, &h.Status, &h.CreatedAt, &h.EditedAt); err != nil {
		return h, err
	}
	rows, err := s.Query(ctx, `SELECT body,spoiler,status,created_at FROM comment_edits WHERE comment_id=$1 ORDER BY id`, commentID)
	if err != nil {
		return h, err
	}
	h.Versions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (CommentVersion, error) {
		var v CommentVersion
		return v, r.Scan(&v.Body, &v.Spoiler, &v.Status, &v.ReplacedAt)
	})
	if h.Versions == nil {
		h.Versions = []CommentVersion{}
	}
	return h, err
}

// DeleteComment soft-deletes the user's own comment (ErrNotOwner for
// someone else's; pgx.ErrNoRows when missing or already deleted).
func (s *Store) DeleteComment(ctx context.Context, commentID, userID int64) error {
	var owner int64
	if err := s.QueryRow(ctx, `SELECT user_id FROM comments WHERE id=$1 AND status<>'deleted'`, commentID).Scan(&owner); err != nil {
		return err
	}
	if owner != userID {
		return ErrNotOwner
	}
	_, err := s.Exec(ctx, `UPDATE comments SET status='deleted',updated_at=now() WHERE id=$1`, commentID)
	return err
}

// SetCommentLike likes or unlikes a public comment (once per user, can be
// undone) and returns the new count. Shadow and deleted comments are 404.
func (s *Store) SetCommentLike(ctx context.Context, commentID, userID int64, like bool) (int, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var likes int
	if err = tx.QueryRow(ctx, `SELECT likes FROM comments WHERE id=$1 AND status IN ('visible','folded') FOR UPDATE`, commentID).Scan(&likes); err != nil {
		return 0, err
	}
	q := `INSERT INTO comment_likes(user_id,comment_id) VALUES($1,$2) ON CONFLICT DO NOTHING`
	delta := 1
	if !like {
		q, delta = `DELETE FROM comment_likes WHERE user_id=$1 AND comment_id=$2`, -1
	}
	ct, err := tx.Exec(ctx, q, userID, commentID)
	if err != nil {
		return 0, err
	}
	if ct.RowsAffected() > 0 {
		if err = tx.QueryRow(ctx, `UPDATE comments SET likes=GREATEST(0,likes+$2) WHERE id=$1 RETURNING likes`, commentID, delta).Scan(&likes); err != nil {
			return 0, err
		}
	}
	return likes, tx.Commit(ctx)
}
