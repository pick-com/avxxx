package api

// 片段 (clips; migration 054, store/clips.go): a signed-in viewer shares a
// 5–60 s segment of a published release. POST /api/me/clips is rate limited
// with the community posting limits (kind "clip"; muted users are 403) and
// validates the range against the release duration when it is known.
// GET /api/clips/{id} is public and answers only for published releases;
// DELETE /api/me/clips/{id} removes the caller's own clip and GET
// /api/me/clips lists them (我的片段, /me/clips). Clips join the shared
// report inbox (POST /api/clips/{id}/report) and the moderator actions
// (POST /api/admin/moderation/clips/{id}/action, moderation.go).

import (
	"avxxx/internal/community"
	"avxxx/internal/images"
	"avxxx/internal/store"
	"context"
	"net/http"
)

// clipStore is the subset of *store.Store used by the clip handlers.
type clipStore interface {
	CreateClip(ctx context.Context, userID int64, in store.ClipInput, status string) (store.Clip, error)
	ClipByPublicID(ctx context.Context, publicID string, viewerID int64) (store.Clip, error)
	DeleteClip(ctx context.Context, userID int64, publicID string) error
	MyClips(ctx context.Context, userID int64, c *store.Cursor, limit int) ([]store.MyClip, string, error)
}

func (srv *Server) clipRoutes() []route {
	return []route{
		{"GET /api/me/clips", srv.withClips(srv.user(srv.myClips))},
		{"POST /api/me/clips", srv.withClips(srv.user(srv.createClip))},
		{"DELETE /api/me/clips/{id}", srv.withClips(srv.user(srv.deleteClip))},
		{"GET /api/clips/{id}", srv.withClips(srv.getClip)},
		{"POST /api/clips/{id}/report", srv.withClips(srv.user(srv.reportClip))},
	}
}

// withClips answers 404 when no clip store is wired (tests without a database).
func (srv *Server) withClips(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.clips == nil {
			reply(w, nil, &httpError{status: http.StatusNotFound, msg: statusMessage(http.StatusNotFound)})
			return
		}
		h(w, r)
	}
}

// createClip: POST /api/me/clips {"release": slug, "start_ms", "end_ms", "title"?}.
// A title with a moderation keyword is shadowed: the clip is shared as
// usual, but only its author sees the title until a moderator approves or
// removes it (待审片段); "title_pending": true says so.
func (srv *Server) createClip(w http.ResponseWriter, r *http.Request, uid int64) {
	var in store.ClipInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Release == "" {
		reply(w, nil, badRequest("release is required", nil))
		return
	}
	if err := store.ValidateClipRange(in.StartMs, in.EndMs, 0); err != nil {
		reply(w, nil, err)
		return
	}
	title, err := store.CleanClipTitle(in.Title)
	if err != nil {
		reply(w, nil, err)
		return
	}
	titleStatus := store.ClipTitleVisible
	if srv.community != nil {
		if _, err := srv.allowPost(r.Context(), "clip", uid); err != nil {
			reply(w, nil, err)
			return
		}
		if title != "" {
			status, err := srv.moderationStatus(r.Context(), title)
			if err != nil {
				reply(w, nil, err)
				return
			}
			if status != community.StatusVisible {
				titleStatus = store.ClipTitleShadow
			}
		}
	}
	in.Title = title
	c, err := srv.clips.CreateClip(r.Context(), uid, in, titleStatus)
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, map[string]any{"clip": c, "title_pending": c.TitleStatus == store.ClipTitleShadow})
}

// myClips: GET /api/me/clips?cursor=&limit=, the caller's clips newest
// first, each with its release's cover.
func (srv *Server) myClips(w http.ResponseWriter, r *http.Request, uid int64) {
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := cursorParam(q, store.ClipSort)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, next, err := srv.clips.MyClips(r.Context(), uid, c, limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ids := make([]int64, len(items))
	for i, x := range items {
		ids[i] = x.ReleaseID
	}
	covers := srv.publicImages(r.Context(), images.EntityRelease, ids)
	for i := range items {
		items[i].ReleaseCover = covers[items[i].ReleaseID]
	}
	reply(w, page(items, next), nil)
}

// getClip: GET /api/clips/{id} (public id). 404 for unknown, deleted or
// moderated-away clips and for releases that are not published. The
// release's cover comes along, so the clip page renders (og:image included)
// from this one request.
func (srv *Server) getClip(w http.ResponseWriter, r *http.Request) {
	c, err := srv.clips.ClipByPublicID(r.Context(), r.PathValue("id"), srv.viewerID(r))
	if err == nil {
		c.ReleaseCover = srv.publicImages(r.Context(), images.EntityRelease, []int64{c.ReleaseID})[c.ReleaseID]
	}
	reply(w, c, err)
}

// deleteClip: DELETE /api/me/clips/{id}; another user's clip is 404.
func (srv *Server) deleteClip(w http.ResponseWriter, r *http.Request, uid int64) {
	err := srv.clips.DeleteClip(r.Context(), uid, r.PathValue("id"))
	reply(w, map[string]bool{"deleted": err == nil}, err)
}

// reportClip: POST /api/clips/{id}/report {"reason", "note"?} into the shared inbox.
func (srv *Server) reportClip(w http.ResponseWriter, r *http.Request, uid int64) {
	c, err := srv.clips.ClipByPublicID(r.Context(), r.PathValue("id"), uid)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in reportInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	in.TargetType, in.TargetID = "clip", c.RowID
	srv.fileReport(w, r, uid, in)
}
