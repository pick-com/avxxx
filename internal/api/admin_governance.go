package api

// Crawl governance admin routes (FINAL_DATA_DESIGN 2.4, 7, 10.3): license /
// terms review per source, phased job requests (baseline only on request,
// page-capped verify), crawl checkpoints, and field priority policies. The
// actor is X-Admin-User; every change is written to review_events.

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
)

// governanceStore is the subset of *store.Store behind these routes.
type governanceStore interface {
	Enqueue(ctx context.Context, id int64, req store.EnqueueRequest) (int64, error)
	SetSourceLicense(ctx context.Context, id int64, in store.LicenseReview, actor string) (model.Source, error)
	SourceCheckpoints(ctx context.Context, sourceID int64) ([]store.CrawlCheckpoint, error)
	FieldPolicies(ctx context.Context) ([]store.FieldPolicy, error)
	SetFieldPolicy(ctx context.Context, p store.FieldPolicy, reason, actor string) (store.FieldPolicy, error)
	DeleteFieldPolicy(ctx context.Context, entityType, fieldName string, sourceID int64, reason, actor string) error
	EffectiveFieldOrder(ctx context.Context, entityType, fieldName string) ([]store.EffectiveSource, error)
}

func (srv *Server) adminGovernanceRoutes() []route {
	return []route{
		{"POST /api/admin/sources/{id}/jobs", srv.admin(srv.enqueueJob)},
		{"PUT /api/admin/sources/{id}/license", srv.admin(srv.setSourceLicense)},
		{"GET /api/admin/sources/{id}/checkpoints", srv.admin(srv.sourceCheckpoints)},
		{"GET /api/admin/field-policies", srv.admin(srv.fieldPolicies)},
		{"PUT /api/admin/field-policies", srv.admin(srv.setFieldPolicy)},
		{"DELETE /api/admin/field-policies/{entity_type}/{field}/{source_id}", srv.admin(srv.deleteFieldPolicy)},
	}
}

// enqueueJob requests a job. Body (optional): {"phase": "incremental" |
// "baseline" | "verify", "max_pages": n}. An empty body is an incremental job.
// 409 when the license gate refuses the phase or one is already active.
func (srv *Server) enqueueJob(w http.ResponseWriter, r *http.Request) {
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
		Phase    string `json:"phase"`
		MaxPages int    `json:"max_pages"`
	}
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	phase, err := catalog.ParsePhase(in.Phase)
	if err != nil {
		reply(w, nil, badRequest(err.Error(), nil))
		return
	}
	if in.MaxPages < 0 || (in.MaxPages > 0 && phase != catalog.PhaseVerify) {
		reply(w, nil, badRequest("max_pages applies to verify jobs only", nil))
		return
	}
	jobID, err := srv.governance.Enqueue(r.Context(), id, store.EnqueueRequest{Phase: phase, MaxPages: in.MaxPages, Actor: actor})
	reply(w, map[string]any{"ok": err == nil, "job_id": jobID, "phase": phase}, err)
}

// setSourceLicense records a license / terms review. Body:
// {"license_status": "...", "terms_url": "https://...", "reviewed": true, "note": "..."}.
func (srv *Server) setSourceLicense(w http.ResponseWriter, r *http.Request) {
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
	var in store.LicenseReview
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.governance.SetSourceLicense(r.Context(), id, in, actor)
	reply(w, v, err)
}

func (srv *Server) sourceCheckpoints(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.governance.SourceCheckpoints(r.Context(), id)
	reply(w, v, err)
}

// fieldPolicies lists policy rows and the tier defaults. With
// ?entity_type=&field_name= it also returns the effective source order.
func (srv *Server) fieldPolicies(w http.ResponseWriter, r *http.Request) {
	rows, err := srv.governance.FieldPolicies(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	out := map[string]any{"policies": rows, "tier_defaults": store.DefaultTierPriority, "lower_wins": true}
	if et, f := r.URL.Query().Get("entity_type"), r.URL.Query().Get("field_name"); et != "" || f != "" {
		order, err := srv.governance.EffectiveFieldOrder(r.Context(), et, f)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out["effective"] = order
	}
	reply(w, out, nil)
}

func (srv *Server) setFieldPolicy(w http.ResponseWriter, r *http.Request) {
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		EntityType string `json:"entity_type"`
		FieldName  string `json:"field_name"`
		SourceID   int64  `json:"source_id"`
		Priority   *int   `json:"priority"`
		Reason     string `json:"reason"`
	}
	if err = decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.SourceID <= 0 || in.Priority == nil {
		reply(w, nil, badRequest("source_id and priority are required", nil))
		return
	}
	v, err := srv.governance.SetFieldPolicy(r.Context(), store.FieldPolicy{EntityType: in.EntityType, FieldName: in.FieldName, SourceID: in.SourceID, Priority: *in.Priority}, in.Reason, actor)
	reply(w, v, err)
}

func (srv *Server) deleteFieldPolicy(w http.ResponseWriter, r *http.Request) {
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	sourceID, err := strconv.ParseInt(r.PathValue("source_id"), 10, 64)
	if err != nil || sourceID <= 0 {
		reply(w, nil, badRequest("invalid source_id", err))
		return
	}
	err = srv.governance.DeleteFieldPolicy(r.Context(), r.PathValue("entity_type"), r.PathValue("field"), sourceID, r.URL.Query().Get("reason"), actor)
	reply(w, map[string]bool{"ok": err == nil}, err)
}
