package api

// Admin view of the offline release -> provider linking (internal/medialink):
//
//	GET  /api/admin/media-links?provider=javtiful&status=&cursor=&limit=
//	POST /api/admin/media-links/{release_id}/retry?provider=javtiful
//	POST /api/admin/media-links/{release_id}/choose {"provider":"javtiful","media_id":"video/1/abc-123"}
//	POST /api/admin/media-links/run

import (
	"avxxx/internal/medialink"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// mediaLinkAdmin is the subset of *store.Store the admin routes need.
type mediaLinkAdmin interface {
	MediaLinkAttempts(ctx context.Context, f store.MediaLinkFilter) ([]store.MediaLinkRow, string, error)
	RetryMediaLink(ctx context.Context, releaseID int64, provider string) error
	ChooseMediaLink(ctx context.Context, releaseID int64, provider, mediaID, actor string) error
	EnqueueKind(ctx context.Context, kind string) (jobID int64, enqueued bool, err error)
}

func (srv *Server) adminMediaLinkRoutes() []route {
	return []route{
		{"GET /api/admin/media-links", srv.admin(srv.withLinkAdmin(srv.listMediaLinks))},
		{"POST /api/admin/media-links/run", srv.admin(srv.withLinkAdmin(srv.runMediaLinks))},
		{"POST /api/admin/media-links/{release_id}/retry", srv.admin(srv.withLinkAdmin(srv.retryMediaLink))},
		{"POST /api/admin/media-links/{release_id}/choose", srv.admin(srv.withLinkAdmin(srv.chooseMediaLink))},
	}
}

func (srv *Server) withLinkAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.linkAdmin == nil {
			reply(w, nil, errNotFound)
			return
		}
		h(w, r)
	}
}

// linkProvider validates a provider name ("" = javtiful).
func linkProvider(p string) (string, error) {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		p = "javtiful"
	}
	if _, ok := medialink.Providers[p]; !ok {
		return "", badRequest("unknown provider", nil)
	}
	return p, nil
}

func pathReleaseID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("release_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid release_id", err)
	}
	return id, nil
}

func (srv *Server) listMediaLinks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider, err := linkProvider(q.Get("provider"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	status := strings.TrimSpace(q.Get("status"))
	if status != "" && !medialink.KnownStatus(status) {
		reply(w, nil, badRequest("unknown status", nil))
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		if limit, err = strconv.Atoi(v); err != nil || limit <= 0 {
			reply(w, nil, badRequest("invalid limit", err))
			return
		}
	}
	items, next, err := srv.linkAdmin.MediaLinkAttempts(r.Context(), store.MediaLinkFilter{Provider: provider, Status: status, Cursor: strings.TrimSpace(q.Get("cursor")), Limit: limit})
	reply(w, map[string]any{"items": items, "next_cursor": next}, err)
}

func (srv *Server) retryMediaLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathReleaseID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	provider, err := linkProvider(r.URL.Query().Get("provider"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.linkAdmin.RetryMediaLink(r.Context(), id, provider)
	reply(w, map[string]any{"release_id": id, "provider": provider, "due": true}, err)
}

func (srv *Server) chooseMediaLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathReleaseID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Provider string `json:"provider"`
		MediaID  string `json:"media_id"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	provider, err := linkProvider(in.Provider)
	if err != nil {
		reply(w, nil, err)
		return
	}
	in.MediaID = strings.TrimSpace(in.MediaID)
	if in.MediaID == "" {
		reply(w, nil, badRequest("media_id is required", nil))
		return
	}
	err = srv.linkAdmin.ChooseMediaLink(r.Context(), id, provider, in.MediaID, actor)
	reply(w, map[string]any{"release_id": id, "provider": provider, "media_id": in.MediaID, "linked": err == nil}, err)
}

func (srv *Server) runMediaLinks(w http.ResponseWriter, r *http.Request) {
	id, enqueued, err := srv.linkAdmin.EnqueueKind(r.Context(), medialink.JobKind)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"job_id": id, "enqueued": enqueued})
}
