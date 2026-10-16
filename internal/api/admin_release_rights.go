package api

// 版权凭证 on the AdminVideo 发布 tab (migration 044):
//
//	GET    /api/admin/releases/{id}/rights-proofs?removed=1        proofs (op-logged read)
//	POST   /api/admin/releases/{id}/rights-proofs                  {"kind","reference","url","note","valid_until"}
//	DELETE /api/admin/releases/{id}/rights-proofs/{proof_id}?reason=  marks it removed
//
// Proofs are private: only these catalog-group admin routes read them, every
// read is in the op log, and responses are Cache-Control: no-store. A URL
// must be http(s); the document itself stays wherever the reference points
// (private storage, the contract archive), never in a public bucket.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type rightsStore interface {
	ReleaseRightsProofs(ctx context.Context, releaseID int64, withRemoved bool) ([]store.RightsProof, error)
	AddReleaseRightsProof(ctx context.Context, releaseID int64, in store.RightsProofInput, actor string) (store.RightsProof, error)
	RemoveReleaseRightsProof(ctx context.Context, releaseID, proofID int64, reason, actor string) (store.RightsProof, error)
}

func (srv *Server) releaseRightsRoutes() []route {
	return []route{
		{"GET /api/admin/releases/{id}/rights-proofs", srv.admin(srv.withRights(srv.listRightsProofs))},
		{"POST /api/admin/releases/{id}/rights-proofs", srv.admin(srv.withRights(srv.addRightsProof))},
		{"DELETE /api/admin/releases/{id}/rights-proofs/{proof_id}", srv.admin(srv.withRights(srv.removeRightsProof))},
	}
}

func (srv *Server) withRights(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.rights == nil {
			reply(w, nil, errUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r)
	}
}

// validProofURL accepts an empty value or an absolute http(s) URL.
func validProofURL(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return true
	}
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (srv *Server) listRightsProofs(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, err := srv.rights.ReleaseRightsProofs(r.Context(), id, r.URL.Query().Get("removed") == "1")
	reply(w, map[string]any{"items": items, "kinds": store.RightsProofKinds}, err)
}

func (srv *Server) addRightsProof(w http.ResponseWriter, r *http.Request) {
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
	var in store.RightsProofInput
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if !validProofURL(in.URL) {
		reply(w, nil, badRequest("url must be an absolute http(s) URL", nil))
		return
	}
	v, err := srv.rights.AddReleaseRightsProof(r.Context(), id, in, actor)
	replyCreated(w, v, err)
}

func (srv *Server) removeRightsProof(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	proofID, err := strconv.ParseInt(r.PathValue("proof_id"), 10, 64)
	if err != nil || proofID <= 0 {
		reply(w, nil, badRequest("invalid proof_id", err))
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reason := strings.TrimSpace(r.URL.Query().Get("reason"))
	if len(reason) > store.MaxReasonLen {
		reply(w, nil, badRequest("reason too long", nil))
		return
	}
	v, err := srv.rights.RemoveReleaseRightsProof(r.Context(), id, proofID, reason, actor)
	reply(w, v, err)
}
