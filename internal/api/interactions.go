package api

// 社交互动 routes (PRD 5.4; tech plan 3.13): 赞 / 踩 on releases and
// comments. Votes work like ratings (a user token, or an anonymous
// X-Device-ID); comments need a user token to post, like or report and run
// through the same mute / rate-limit / keyword checks as danmaku and reviews.
// COMMENTS_ENABLED=false (comments are V1.1 in the PRD) turns every public
// comment route into 404 while moderation keeps working.

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// interactionStore is the subset of *store.Store used by these handlers.
type interactionStore interface {
	ReactionSummary(ctx context.Context, releaseID int64, voterKey string) (store.ReactionSummary, error)
	SetReaction(ctx context.Context, releaseID int64, v community.Voter, value int) error

	Comments(ctx context.Context, f store.CommentFilter) ([]store.Comment, string, error)
	CommentReplies(ctx context.Context, parentID, viewerID int64, c *store.Cursor, limit int) ([]store.Comment, string, error)
	CreateComment(ctx context.Context, n store.NewComment) (store.Comment, error)
	EditComment(ctx context.Context, commentID, userID int64, e store.CommentEdit) (store.Comment, error)
	DeleteComment(ctx context.Context, commentID, userID int64) error
	SetCommentLike(ctx context.Context, commentID, userID int64, like bool) (int, error)
}

func (srv *Server) interactionRoutes() []route {
	return []route{
		{"GET /api/releases/{slug}/reaction", srv.withInteractions(srv.getReaction)},
		{"PUT /api/releases/{slug}/reaction", srv.withInteractions(srv.putReaction)},
		{"DELETE /api/releases/{slug}/reaction", srv.withInteractions(srv.deleteReaction)},

		{"GET /api/releases/{slug}/comments", srv.withComments(srv.listComments)},
		{"POST /api/releases/{slug}/comments", srv.withComments(srv.user(srv.createComment))},
		{"GET /api/comments/{id}/replies", srv.withComments(srv.listReplies)},
		{"PATCH /api/comments/{id}", srv.withComments(srv.user(srv.editComment))},
		{"DELETE /api/comments/{id}", srv.withComments(srv.user(srv.deleteComment))},
		{"PUT /api/comments/{id}/like", srv.withComments(srv.user(srv.likeComment(true)))},
		{"DELETE /api/comments/{id}/like", srv.withComments(srv.user(srv.likeComment(false)))},
		{"POST /api/comments/{id}/report", srv.withComments(srv.user(srv.reportTarget("comment")))},
	}
}

// commentsEnabled reads COMMENTS_ENABLED (default true; "0", "false", "off"
// or "no" turn comments off).
func commentsEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

var errCommentsOff = &httpError{status: http.StatusNotFound, msg: "comments are not enabled"}

// withInteractions guards the vote routes when there is no store.
func (srv *Server) withInteractions(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.interactions == nil || srv.community == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// withComments adds the COMMENTS_ENABLED switch to withInteractions.
func (srv *Server) withComments(h http.HandlerFunc) http.HandlerFunc {
	return srv.withInteractions(func(w http.ResponseWriter, r *http.Request) {
		if srv.commentsOff {
			reply(w, nil, errCommentsOff)
			return
		}
		h(w, r)
	})
}

// --- 赞 / 踩 ----------------------------------------------------------------

func (srv *Server) reactionSummary(w http.ResponseWriter, r *http.Request, releaseID int64) {
	v, err := srv.interactions.ReactionSummary(r.Context(), releaseID, srv.voterKeyFor(r))
	w.Header().Set("Cache-Control", "private, no-store")
	reply(w, v, err)
}

// getReaction: GET /api/releases/{slug}/reaction — the 赞 count and the
// caller's own vote (token or X-Device-ID).
func (srv *Server) getReaction(w http.ResponseWriter, r *http.Request) {
	id, err := srv.community.ReleaseIDBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.reactionSummary(w, r, id)
}

// putReaction: PUT /api/releases/{slug}/reaction {"value": 1 | -1}; 0 undoes.
func (srv *Server) putReaction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Value *int `json:"value"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Value == nil || *in.Value < -1 || *in.Value > 1 {
		reply(w, nil, badRequest("value must be 1 (赞), -1 (踩) or 0 (undo)", nil))
		return
	}
	srv.setReaction(w, r, *in.Value)
}

// deleteReaction: DELETE /api/releases/{slug}/reaction (undo).
func (srv *Server) deleteReaction(w http.ResponseWriter, r *http.Request) {
	srv.setReaction(w, r, 0)
}

func (srv *Server) setReaction(w http.ResponseWriter, r *http.Request, value int) {
	ctx := r.Context()
	v, err := srv.voter(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowAnonVote(r, v); err != nil {
		reply(w, nil, err)
		return
	}
	id, err := srv.community.ReleaseIDBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	if srv.limiter != nil {
		ok, err := srv.limiter.Allow(ctx, "reaction:"+community.VoterKey(v), community.ReactionPerMinute, community.PostingLimitWindow)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if !ok {
			reply(w, nil, tooManyRequests("too many votes, try again in a minute"))
			return
		}
	}
	if err := srv.interactions.SetReaction(ctx, id, v, value); err != nil {
		reply(w, nil, err)
		return
	}
	srv.reactionSummary(w, r, id)
}

// --- comments ----------------------------------------------------------------

// listComments: GET /api/releases/{slug}/comments?sort=top|newest&limit=&cursor=.
func (srv *Server) listComments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.CommentFilter{Sort: strings.TrimSpace(q.Get("sort"))}
	if f.Sort == "" {
		f.Sort = community.CommentSortTop
	}
	if !community.ValidCommentSort(f.Sort) {
		reply(w, nil, badRequest("sort must be top or newest", nil))
		return
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		reply(w, nil, err)
		return
	}
	if f.Cursor, err = cursorParam(q, f.Sort); err != nil {
		reply(w, nil, err)
		return
	}
	if f.ReleaseID, err = srv.community.ReleaseIDBySlug(r.Context(), r.PathValue("slug")); err != nil {
		reply(w, nil, err)
		return
	}
	f.ViewerID = srv.viewerID(r)
	items, next, err := srv.interactions.Comments(r.Context(), f)
	w.Header().Set("Cache-Control", "private, no-store")
	reply(w, page(items, next), err)
}

// listReplies: GET /api/comments/{id}/replies?limit=&cursor= (oldest first).
func (srv *Server) listReplies(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	cur, err := cursorParam(q, "replies")
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, next, err := srv.interactions.CommentReplies(r.Context(), id, srv.viewerID(r), cur, limit)
	w.Header().Set("Cache-Control", "private, no-store")
	reply(w, page(items, next), err)
}

// createComment: POST /api/releases/{slug}/comments {"body", "spoiler"?, "parent_id"?}.
func (srv *Server) createComment(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Body     string `json:"body"`
		Spoiler  bool   `json:"spoiler"`
		ParentID int64  `json:"parent_id"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	body, err := community.CleanBody(in.Body, community.MaxCommentRunes, true)
	if err != nil {
		reply(w, nil, badRequest("body: "+err.Error()+" (1-"+strconv.Itoa(community.MaxCommentRunes)+" characters)", nil))
		return
	}
	if in.ParentID < 0 {
		reply(w, nil, badRequest("invalid parent_id", nil))
		return
	}
	ctx := r.Context()
	releaseID, err := srv.community.ReleaseIDBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	if _, err := srv.allowPost(ctx, "comment", uid); err != nil {
		reply(w, nil, err)
		return
	}
	status, err := srv.moderationStatus(ctx, body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := srv.interactions.CreateComment(ctx, store.NewComment{
		ReleaseID: releaseID, ParentID: in.ParentID, UserID: uid, Body: body, Spoiler: in.Spoiler, Status: status,
	})
	if errors.Is(err, store.ErrCommentsParent) {
		err = &httpError{status: http.StatusNotFound, msg: store.ErrCommentsParent.Error(), err: err}
	}
	if err != nil {
		reply(w, nil, err)
		return
	}
	// A shadowed comment looks posted to its author (it is shown to them).
	replyStatus(w, http.StatusCreated, c)
}

// editComment: PATCH /api/comments/{id} {"body", "spoiler"?}: the author's
// edit within store.CommentEditWindow of posting. The new body goes through
// the same mute / rate-limit / blocked-word checks as a new comment (a hit
// shadows it); the previous version is kept for moderators.
func (srv *Server) editComment(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Body    *string `json:"body"`
		Spoiler *bool   `json:"spoiler"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Body == nil {
		reply(w, nil, badRequest("body is required", nil))
		return
	}
	body, err := community.CleanBody(*in.Body, community.MaxCommentRunes, true)
	if err != nil {
		reply(w, nil, badRequest("body: "+err.Error()+" (1-"+strconv.Itoa(community.MaxCommentRunes)+" characters)", nil))
		return
	}
	ctx := r.Context()
	if _, err := srv.allowPost(ctx, "comment", uid); err != nil {
		reply(w, nil, err)
		return
	}
	status, err := srv.moderationStatus(ctx, body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := srv.interactions.EditComment(ctx, id, uid, store.CommentEdit{Body: body, Spoiler: in.Spoiler, KeywordHit: status == community.StatusShadow})
	switch {
	case errors.Is(err, store.ErrNotOwner):
		err = &httpError{status: http.StatusForbidden, msg: "not your comment", err: err}
	case errors.Is(err, store.ErrEditWindow):
		err = conflict(store.ErrEditWindow.Error(), err)
	}
	reply(w, c, err)
}

// deleteComment: DELETE /api/comments/{id} (own comment, soft delete).
func (srv *Server) deleteComment(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.interactions.DeleteComment(r.Context(), id, uid)
	if errors.Is(err, store.ErrNotOwner) {
		err = &httpError{status: http.StatusForbidden, msg: "not your comment", err: err}
	}
	reply(w, map[string]bool{"deleted": err == nil}, err)
}

// likeComment: PUT / DELETE /api/comments/{id}/like.
func (srv *Server) likeComment(like bool) userHandler {
	return func(w http.ResponseWriter, r *http.Request, uid int64) {
		id, err := pathInt64(r, "id")
		if err != nil {
			reply(w, nil, err)
			return
		}
		n, err := srv.interactions.SetCommentLike(r.Context(), id, uid, like)
		reply(w, map[string]any{"liked": like, "likes": n}, err)
	}
}
