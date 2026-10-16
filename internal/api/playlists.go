package api

// 收藏夹 (playlists, PRD 7 NavFavorites; tech plan 3.13) and 移到收藏夹 from
// watch-later. Every /api/me route requires a user token and is scoped to the
// caller's own playlists in the store: another user's playlist id answers 404
// exactly like a missing one. GET /api/playlists/{token} is the only public
// read, for public and link-only playlists.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
)

// libraryStore is the subset of *store.Store used by the playlist handlers
// and the watch-later auto-remove rule in saveWatch.
type libraryStore interface {
	Playlists(ctx context.Context, userID int64, releaseSlug string) ([]store.Playlist, error)
	CreatePlaylist(ctx context.Context, userID int64, in store.PlaylistInput) (store.Playlist, error)
	UpdatePlaylist(ctx context.Context, userID, id int64, in store.PlaylistInput) (store.Playlist, error)
	RotatePlaylistToken(ctx context.Context, userID, id int64) (store.Playlist, error)
	DeletePlaylist(ctx context.Context, userID, id int64) error
	PlaylistWithItems(ctx context.Context, userID, id int64) (store.Playlist, []store.PlaylistItem, error)
	PublicPlaylist(ctx context.Context, token string) (store.Playlist, []store.PlaylistItem, error)
	AddPlaylistItem(ctx context.Context, userID, id int64, releaseSlug string) error
	RemovePlaylistItem(ctx context.Context, userID, id int64, releaseSlug string) error
	MoveWatchLaterToPlaylist(ctx context.Context, userID, id int64, releaseSlug string) error
	LaterAutoRemove(ctx context.Context, userID int64, releaseSlug string, progress, duration int) (bool, error)
}

func (srv *Server) playlistRoutes() []route {
	return []route{
		{"GET /api/me/playlists", srv.library(srv.listPlaylists)},
		{"POST /api/me/playlists", srv.library(srv.createPlaylist)},
		{"GET /api/me/playlists/{id}", srv.library(srv.getPlaylist)},
		{"PATCH /api/me/playlists/{id}", srv.library(srv.updatePlaylist)},
		{"DELETE /api/me/playlists/{id}", srv.library(srv.deletePlaylist)},
		{"POST /api/me/playlists/{id}/share-token", srv.library(srv.rotatePlaylistToken)},
		{"PUT /api/me/playlists/{id}/items/{release_slug}", srv.library(srv.addPlaylistItem)},
		{"DELETE /api/me/playlists/{id}/items/{release_slug}", srv.library(srv.removePlaylistItem)},
		{"POST /api/me/watch-later/{release_slug}/move", srv.library(srv.moveWatchLater)},
		{"GET /api/playlists/{token}", srv.publicPlaylist},
	}
}

// library is user() plus a check that the playlist store is wired.
func (srv *Server) library(h userHandler) http.HandlerFunc {
	return srv.user(func(w http.ResponseWriter, r *http.Request, uid int64) {
		if srv.playlists == nil {
			reply(w, nil, &httpError{status: http.StatusNotFound, msg: statusMessage(http.StatusNotFound)})
			return
		}
		h(w, r, uid)
	})
}

func (srv *Server) listPlaylists(w http.ResponseWriter, r *http.Request, uid int64) {
	v, e := srv.playlists.Playlists(r.Context(), uid, r.URL.Query().Get("release"))
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) createPlaylist(w http.ResponseWriter, r *http.Request, uid int64) {
	var in store.PlaylistInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	v, e := srv.playlists.CreatePlaylist(r.Context(), uid, in)
	reply(w, v, e)
}

func (srv *Server) getPlaylist(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	p, items, e := srv.playlists.PlaylistWithItems(r.Context(), uid, id)
	reply(w, map[string]any{"playlist": p, "items": items}, e)
}

func (srv *Server) updatePlaylist(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in store.PlaylistInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	v, e := srv.playlists.UpdatePlaylist(r.Context(), uid, id, in)
	reply(w, v, e)
}

func (srv *Server) deletePlaylist(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	e := srv.playlists.DeletePlaylist(r.Context(), uid, id)
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func (srv *Server) rotatePlaylistToken(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, e := srv.playlists.RotatePlaylistToken(r.Context(), uid, id)
	reply(w, v, e)
}

func (srv *Server) addPlaylistItem(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	e := srv.playlists.AddPlaylistItem(r.Context(), uid, id, r.PathValue("release_slug"))
	reply(w, map[string]bool{"saved": e == nil}, e)
}

func (srv *Server) removePlaylistItem(w http.ResponseWriter, r *http.Request, uid int64) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	e := srv.playlists.RemovePlaylistItem(r.Context(), uid, id, r.PathValue("release_slug"))
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func (srv *Server) moveWatchLater(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		PlaylistID int64 `json:"playlist_id"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.PlaylistID <= 0 {
		reply(w, nil, badRequest("playlist_id is required", nil))
		return
	}
	e := srv.playlists.MoveWatchLaterToPlaylist(r.Context(), uid, in.PlaylistID, r.PathValue("release_slug"))
	reply(w, map[string]bool{"moved": e == nil}, e)
}

// publicPlaylist serves a public or link-only playlist to anyone holding the
// share token. Link-only playlists carry X-Robots-Tag: noindex; responses are
// never cached by shared caches (visibility can change at any time).
func (srv *Server) publicPlaylist(w http.ResponseWriter, r *http.Request) {
	if srv.playlists == nil {
		reply(w, nil, &httpError{status: http.StatusNotFound, msg: statusMessage(http.StatusNotFound)})
		return
	}
	p, items, e := srv.playlists.PublicPlaylist(r.Context(), r.PathValue("token"))
	w.Header().Set("Cache-Control", "private, no-store")
	if e == nil && p.Visibility != store.VisibilityPublic {
		w.Header().Set("X-Robots-Tag", "noindex")
	}
	reply(w, map[string]any{"playlist": p, "items": items}, e)
}
