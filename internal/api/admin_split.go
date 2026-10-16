package api

// Entity split (FINAL_DATA_DESIGN 5.5): separate a performer or release that
// ingest built from two different real entities. All routes sit behind
// srv.admin; the actor is the authenticated admin.
//
//	GET  /api/admin/{performers|releases}/{id}/split-preview?source_records=1,2&media_links=javtiful
//	POST /api/admin/{performers|releases}/{id}/split
//	     {"source_records":[1,2],"version":3,"reason":"...","media_links":["javtiful"],
//	      "move_user_data":{"follows":[7]}}
//	POST /api/admin/splits/{event_id}/undo   {"reason":"..."}
//
// 400 for a bad selection, 404 for an unknown entity or event, 409 for a
// stale version, a merged / held / hidden entity, or an undone split.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
	"strings"
)

// splitStore is the subset of *store.Store behind the split routes.
type splitStore interface {
	SplitPreview(ctx context.Context, req store.SplitRequest) (store.SplitPreview, error)
	SplitEntity(ctx context.Context, req store.SplitRequest) (store.SplitResult, error)
	UndoSplit(ctx context.Context, eventID int64, actor, reason string) (store.UndoSplitResult, error)
}

func (srv *Server) adminSplitRoutes() []route {
	return []route{
		{"GET /api/admin/performers/{id}/split-preview", srv.admin(srv.splitPreview("performer"))},
		{"POST /api/admin/performers/{id}/split", srv.admin(srv.splitEntity("performer"))},
		{"GET /api/admin/releases/{id}/split-preview", srv.admin(srv.splitPreview("release"))},
		{"POST /api/admin/releases/{id}/split", srv.admin(srv.splitEntity("release"))},
		{"POST /api/admin/splits/{event_id}/undo", srv.admin(srv.undoSplit)},
	}
}

var errSplitUnavailable = &httpError{status: http.StatusServiceUnavailable, msg: "split unavailable"}

// idList parses "1,2,3" (empty -> nil).
func idList(s string) ([]int64, error) {
	var out []int64
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil || id <= 0 {
			return nil, badRequest("source_records must be a comma-separated list of ids", err)
		}
		out = append(out, id)
	}
	return out, nil
}

func nameList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (srv *Server) splitPreview(entityType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.splitter == nil {
			reply(w, nil, errSplitUnavailable)
			return
		}
		id, err := pathID(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		q := r.URL.Query()
		ids, err := idList(q.Get("source_records"))
		if err != nil {
			reply(w, nil, err)
			return
		}
		v, err := srv.splitter.SplitPreview(r.Context(), store.SplitRequest{EntityType: entityType, EntityID: id,
			SourceRecordIDs: ids, MediaLinks: nameList(q.Get("media_links"))})
		reply(w, v, err)
	}
}

func (srv *Server) splitEntity(entityType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.splitter == nil {
			reply(w, nil, errSplitUnavailable)
			return
		}
		id, err := pathID(r)
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
			SourceRecords []int64            `json:"source_records"`
			Version       int64              `json:"version"`
			Reason        string             `json:"reason"`
			MediaLinks    []string           `json:"media_links"`
			MoveUserData  map[string][]int64 `json:"move_user_data"`
		}
		if err = decodeAdminJSON(w, r, &in); err != nil {
			reply(w, nil, err)
			return
		}
		if len(in.SourceRecords) == 0 {
			reply(w, nil, badRequest("source_records is required", nil))
			return
		}
		if in.Version <= 0 {
			reply(w, nil, badRequest("version is required", nil))
			return
		}
		if len(in.Reason) > store.MaxReasonLen {
			reply(w, nil, badRequest("reason too long", nil))
			return
		}
		v, err := srv.splitter.SplitEntity(r.Context(), store.SplitRequest{EntityType: entityType, EntityID: id, SourceRecordIDs: in.SourceRecords,
			Version: in.Version, Reason: in.Reason, Actor: actor, MediaLinks: in.MediaLinks, MoveUserData: in.MoveUserData})
		reply(w, v, err)
	}
}

func (srv *Server) undoSplit(w http.ResponseWriter, r *http.Request) {
	if srv.splitter == nil {
		reply(w, nil, errSplitUnavailable)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("event_id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid event id", err))
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if len(in.Reason) > store.MaxReasonLen {
		reply(w, nil, badRequest("reason too long", nil))
		return
	}
	v, err := srv.splitter.UndoSplit(r.Context(), id, actor, in.Reason)
	reply(w, v, err)
}
