package api

// User-side social routes: follows and the following feed, reservations,
// watch-later, settings and watch-history management. Every route requires a
// valid user token (401 otherwise).

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// socialStore is the subset of *store.Store used by the social and
// notification handlers, as an interface so they can be tested without a database.
type socialStore interface {
	Follow(ctx context.Context, userID int64, performerSlug, mode string) (map[string]any, error)
	Unfollow(ctx context.Context, userID int64, performerSlug string) error
	Follows(ctx context.Context, userID int64) ([]map[string]any, error)
	FollowingFeed(ctx context.Context, userID int64, c *store.Cursor, limit int) ([]map[string]any, string, error)

	Reserve(ctx context.Context, userID int64, releaseSlug string) error
	Unreserve(ctx context.Context, userID int64, releaseSlug string) error
	Reservations(ctx context.Context, userID int64) ([]map[string]any, error)

	WatchLater(ctx context.Context, userID int64) ([]map[string]any, error)
	AddWatchLater(ctx context.Context, userID int64, releaseSlug string) error
	RemoveWatchLater(ctx context.Context, userID int64, releaseSlug string) error
	ReorderWatchLater(ctx context.Context, userID int64, ordered []string) error

	Settings(ctx context.Context, userID int64) (store.UserSettings, error)
	PatchSettings(ctx context.Context, userID int64, body []byte) (store.UserSettings, error)
	HistoryPaused(ctx context.Context, userID int64) (bool, error)

	DeleteWatch(ctx context.Context, userID int64, releaseSlug string) error
	ClearWatchHistory(ctx context.Context, userID int64) (int64, error)

	Notifications(ctx context.Context, userID int64, c *store.Cursor, limit int) ([]map[string]any, string, error)
	UnreadNotifications(ctx context.Context, userID int64) (int64, error)
	MarkNotificationsRead(ctx context.Context, userID int64, ids []int64, all bool) (int64, error)
}

// maxBodyBytes bounds JSON bodies on user routes.
const maxBodyBytes = 64 << 10

func (srv *Server) socialRoutes() []route {
	return []route{
		{"GET /api/me/follows", srv.user(srv.listFollows)},
		{"PUT /api/me/follows/{performer_slug}", srv.user(srv.follow)},
		{"DELETE /api/me/follows/{performer_slug}", srv.user(srv.unfollow)},
		{"GET /api/me/following/feed", srv.user(srv.followingFeed)},

		{"GET /api/me/reservations", srv.user(srv.listReservations)},
		{"PUT /api/me/reservations/{release_slug}", srv.user(srv.reserve)},
		{"DELETE /api/me/reservations/{release_slug}", srv.user(srv.unreserve)},

		{"GET /api/me/watch-later", srv.user(srv.listWatchLater)},
		{"PUT /api/me/watch-later/{release_slug}", srv.user(srv.addWatchLater)},
		{"DELETE /api/me/watch-later/{release_slug}", srv.user(srv.removeWatchLater)},
		{"POST /api/me/watch-later/reorder", srv.user(srv.reorderWatchLater)},

		{"GET /api/me/settings", srv.user(srv.getSettings)},
		{"PATCH /api/me/settings", srv.user(srv.patchSettings)},

		{"DELETE /api/me/watch-history/{release_slug}", srv.user(srv.deleteWatch)},
		{"DELETE /api/me/watch-history", srv.user(srv.clearWatchHistory)},
	}
}

// userHandler is a handler that runs with an authenticated user id.
type userHandler func(w http.ResponseWriter, r *http.Request, userID int64)

// user resolves the bearer token to a user id (401 on failure) before h.
func (srv *Server) user(h userHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.accounts == nil {
			reply(w, nil, errUnauthorized)
			return
		}
		id, err := srv.userID(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		h(w, r, id)
	}
}

// readJSON decodes a bounded JSON body into v; an empty body leaves v unchanged
// when allowEmpty is set.
func readJSON(r *http.Request, v any, allowEmpty bool) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil {
		return badRequest("unreadable body", err)
	}
	if len(body) > maxBodyBytes {
		return badRequest("body too large", nil)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		if allowEmpty {
			return nil
		}
		return badRequest("JSON body required", nil)
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid JSON body", err)
	}
	return nil
}

func conflict(msg string, cause error) error {
	return &httpError{status: http.StatusConflict, msg: msg, err: cause}
}

// --- follows ---------------------------------------------------------------

func (srv *Server) listFollows(w http.ResponseWriter, r *http.Request, uid int64) {
	v, e := srv.social.Follows(r.Context(), uid)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) follow(w http.ResponseWriter, r *http.Request, uid int64) {
	in := struct {
		NotifyMode string `json:"notify_mode"`
	}{NotifyMode: store.NotifyAll}
	if err := readJSON(r, &in, true); err != nil {
		reply(w, nil, err)
		return
	}
	if !store.ValidNotifyMode(in.NotifyMode) {
		reply(w, nil, badRequest("notify_mode must be all, lead_only or off", nil))
		return
	}
	v, e := srv.social.Follow(r.Context(), uid, r.PathValue("performer_slug"), in.NotifyMode)
	reply(w, v, e)
}

func (srv *Server) unfollow(w http.ResponseWriter, r *http.Request, uid int64) {
	e := srv.social.Unfollow(r.Context(), uid, r.PathValue("performer_slug"))
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func (srv *Server) followingFeed(w http.ResponseWriter, r *http.Request, uid int64) {
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := cursorParam(q, store.FeedSort)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, next, e := srv.social.FollowingFeed(r.Context(), uid, c, limit)
	reply(w, page(v, next), e)
}

// --- reservations ----------------------------------------------------------

func (srv *Server) listReservations(w http.ResponseWriter, r *http.Request, uid int64) {
	v, e := srv.social.Reservations(r.Context(), uid)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) reserve(w http.ResponseWriter, r *http.Request, uid int64) {
	e := reservationErr(srv.social.Reserve(r.Context(), uid, r.PathValue("release_slug")))
	reply(w, map[string]bool{"reserved": e == nil}, e)
}

func (srv *Server) unreserve(w http.ResponseWriter, r *http.Request, uid int64) {
	e := reservationErr(srv.social.Unreserve(r.Context(), uid, r.PathValue("release_slug")))
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func reservationErr(e error) error {
	if errors.Is(e, store.ErrReleaseNotUpcoming) {
		return conflict(store.ErrReleaseNotUpcoming.Error(), e)
	}
	return e
}

// --- watch later -----------------------------------------------------------

func (srv *Server) listWatchLater(w http.ResponseWriter, r *http.Request, uid int64) {
	v, e := srv.social.WatchLater(r.Context(), uid)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) addWatchLater(w http.ResponseWriter, r *http.Request, uid int64) {
	e := srv.social.AddWatchLater(r.Context(), uid, r.PathValue("release_slug"))
	reply(w, map[string]bool{"saved": e == nil}, e)
}

func (srv *Server) removeWatchLater(w http.ResponseWriter, r *http.Request, uid int64) {
	e := srv.social.RemoveWatchLater(r.Context(), uid, r.PathValue("release_slug"))
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func (srv *Server) reorderWatchLater(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Slugs []string `json:"slugs"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if len(in.Slugs) == 0 {
		reply(w, nil, badRequest("slugs must be a non-empty array", nil))
		return
	}
	e := srv.social.ReorderWatchLater(r.Context(), uid, in.Slugs)
	if errors.Is(e, store.ErrInvalidOrder) {
		e = badRequest(e.Error(), nil)
	}
	reply(w, map[string]bool{"saved": e == nil}, e)
}

// --- settings --------------------------------------------------------------

func (srv *Server) getSettings(w http.ResponseWriter, r *http.Request, uid int64) {
	v, e := srv.social.Settings(r.Context(), uid)
	reply(w, v, e)
}

func (srv *Server) patchSettings(w http.ResponseWriter, r *http.Request, uid int64) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		reply(w, nil, badRequest("invalid body", err))
		return
	}
	// Validate up front so bad patches never reach the store.
	if _, err := store.ApplySettingsPatch(store.DefaultSettings(), body); err != nil {
		reply(w, nil, badRequest(err.Error(), nil))
		return
	}
	v, e := srv.social.PatchSettings(r.Context(), uid, body)
	if errors.Is(e, store.ErrInvalidSettings) {
		e = badRequest(e.Error(), nil)
	}
	reply(w, v, e)
}

// --- watch history ---------------------------------------------------------

func (srv *Server) deleteWatch(w http.ResponseWriter, r *http.Request, uid int64) {
	e := srv.social.DeleteWatch(r.Context(), uid, r.PathValue("release_slug"))
	reply(w, map[string]bool{"deleted": e == nil}, e)
}

func (srv *Server) clearWatchHistory(w http.ResponseWriter, r *http.Request, uid int64) {
	n, e := srv.social.ClearWatchHistory(r.Context(), uid)
	reply(w, map[string]int64{"deleted": n}, e)
}
