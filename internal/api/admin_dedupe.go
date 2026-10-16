package api

// AdminDedupe compare panel and the 其他版本 decision (migration 047):
//
//	GET    /api/admin/identity-candidates/{id}/compare       both sides' summaries: codes, dates, maker, performers, sources, playback links, cover
//	POST   /api/admin/identity-candidates/{id}/versions      {"reason"} video candidate → 其他版本 (link, no merge)
//	DELETE /api/admin/releases/{id}/versions/{other_id}      remove a direct 其他版本 link ({"reason"} optional)
//
// 新片源 is POST .../merge and 不是重复 POST .../not-duplicate (admin.go).
// Decisions write review_events; reads need catalog:read, writes
// catalog:write (admin_perms.go).

import (
	"avxxx/internal/images"
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
)

// dedupeStore backs these routes; *store.Store implements it.
type dedupeStore interface {
	CandidateCompare(ctx context.Context, candidateID int64) (store.CandidateCompare, error)
	LinkCandidateVersions(ctx context.Context, candidateID int64, actor, reason string) (store.DecisionResult, error)
	UnlinkReleaseVersion(ctx context.Context, releaseID, otherID int64, actor, reason string) (int64, error)
}

func (srv *Server) adminDedupeRoutes() []route {
	return []route{
		{"GET /api/admin/identity-candidates/{id}/compare", srv.admin(srv.withDedupe(srv.candidateCompare))},
		{"POST /api/admin/identity-candidates/{id}/versions", srv.admin(srv.withDedupe(srv.linkCandidateVersions))},
		{"DELETE /api/admin/releases/{id}/versions/{other_id}", srv.admin(srv.withDedupe(srv.unlinkReleaseVersion))},
	}
}

func (srv *Server) withDedupe(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.dedupe == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

func (srv *Server) candidateCompare(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.dedupe.CandidateCompare(r.Context(), id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	entity := images.EntityRelease
	if v.EntityType == "performer" {
		entity = images.EntityPerformer
	}
	if imgs := srv.publicImages(r.Context(), entity, []int64{v.Left.ID, v.Right.ID}); imgs != nil {
		v.Left.Cover, v.Right.Cover = imgs[v.Left.ID], imgs[v.Right.ID]
	}
	reply(w, v, nil)
}

func (srv *Server) linkCandidateVersions(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	in, err := decodeReview(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.dedupe.LinkCandidateVersions(r.Context(), id, in.Actor, in.Reason)
	reply(w, v, err)
}

func (srv *Server) unlinkReleaseVersion(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	other, err := strconv.ParseInt(r.PathValue("other_id"), 10, 64)
	if err != nil || other <= 0 {
		reply(w, nil, badRequest("invalid other_id", err))
		return
	}
	in, err := decodeReview(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ev, err := srv.dedupe.UnlinkReleaseVersion(r.Context(), id, other, in.Actor, in.Reason)
	reply(w, map[string]any{"review_event_id": ev, "release_id": id, "other_id": other}, err)
}
