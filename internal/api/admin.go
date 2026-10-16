package api

import (
	"avxxx/internal/model"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func (srv *Server) adminRoutes() []route {
	return []route{
		{"POST /api/admin/releases/{id}/media-link", srv.admin(srv.linkMediaToRelease)},
		{"POST /api/admin/media-links/by-identifier", srv.admin(srv.linkMediaByIdentifier)},
		{"GET /api/admin/sources", srv.admin(srv.sources)},
		{"GET /api/admin/jobs", srv.admin(srv.jobs)},
		{"GET /api/admin/crawl-policies", srv.admin(srv.crawlPolicies)},
		{"POST /api/admin/media-assets", srv.admin(srv.saveMediaAsset)},
		{"GET /api/admin/identity-candidates", srv.admin(srv.identityCandidates)},
		{"POST /api/admin/identity-candidates/generate", srv.admin(srv.generateIdentityCandidates)},
		{"POST /api/admin/identity-candidates/{id}/merge", srv.admin(srv.mergeCandidate)},
		{"POST /api/admin/identity-candidates/{id}/not-duplicate", srv.admin(srv.notDuplicateCandidate)},
		{"POST /api/admin/merges/{id}/undo", srv.admin(srv.undoMerge)},
		{"PUT /api/admin/sources/{id}/crawl-policy", srv.admin(srv.upsertCrawlPolicy)},
		{"GET /api/admin/conflicts", srv.admin(srv.conflicts)},
		{"POST /api/admin/conflicts/{id}/resolve", srv.admin(srv.resolveConflict)},
		{"POST /api/admin/sources", srv.admin(srv.addSource)},
	}
}

func (srv *Server) linkMediaToRelease(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid id", err))
		return
	}
	var input struct {
		Source   string `json:"source"`
		MediaID  string `json:"media_id"`
		MediaURL string `json:"media_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	if input.Source == "" || input.MediaID == "" {
		reply(w, nil, badRequest("source and media_id are required", nil))
		return
	}
	err = srv.store.LinkMediaToRelease(r.Context(), id, input.Source, input.MediaID, input.MediaURL)
	reply(w, map[string]any{"linked": err == nil}, err)
}

func (srv *Server) linkMediaByIdentifier(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Source     string `json:"source"`
		MediaID    string `json:"media_id"`
		MediaURL   string `json:"media_url"`
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	if input.Source == "" || input.MediaID == "" || input.Identifier == "" {
		reply(w, nil, badRequest("source, media_id and identifier are required", nil))
		return
	}
	linked, err := srv.store.LinkMediaByIdentifier(r.Context(), input.Source, input.MediaID, input.MediaURL, input.Identifier)
	reply(w, map[string]any{"linked": linked}, err)
}

func (srv *Server) sources(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Sources(r.Context())
	reply(w, v, e)
}

func (srv *Server) jobs(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Jobs(r.Context())
	reply(w, v, e)
}

func (srv *Server) crawlPolicies(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.CrawlPolicies(r.Context())
	reply(w, v, e)
}

func (srv *Server) saveMediaAsset(w http.ResponseWriter, r *http.Request) {
	var asset map[string]any
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&asset) != nil || asset["entity_type"] == nil || asset["entity_id"] == nil || asset["asset_type"] == nil || asset["object_key"] == nil || asset["content_sha256"] == nil || asset["mime_type"] == nil {
		http.Error(w, "required asset fields are missing", 400)
		return
	}
	if asset["storage_provider"] == nil {
		asset["storage_provider"] = "s3"
	}
	if asset["is_original"] == nil {
		asset["is_original"] = true
	}
	reply(w, map[string]bool{"ok": true}, srv.store.SaveMediaAsset(r.Context(), asset))
}

func (srv *Server) identityCandidates(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.IdentityCandidates(r.Context(), r.URL.Query().Get("entity_type"), limit)
	reply(w, v, e)
}

// identityStore is the subset of *store.Store used by the identity review
// handlers and the merge redirects, as an interface for database-free tests.
type identityStore interface {
	GenerateIdentityCandidates(ctx context.Context, entityType string) (store.GenerateResult, error)
	MergeCandidate(ctx context.Context, candidateID, targetID int64, actor, reason string) (store.MergeResult, error)
	MarkNotDuplicate(ctx context.Context, candidateID int64, actor, reason string) (store.DecisionResult, error)
	UndoMerge(ctx context.Context, eventID int64, actor, reason string) (store.UndoResult, error)
	ResolveMergedSlug(ctx context.Context, entityType, slug string) (string, bool, error)
}

// reviewInput is the optional JSON body of identity decisions. Admin auth is
// a shared token, so actor is a self-declared label recorded in
// review_events.actor ("admin" when omitted).
type reviewInput struct {
	TargetID   int64  `json:"target_id"`
	EntityType string `json:"entity_type"`
	Actor      string `json:"actor"`
	Reason     string `json:"reason"`
}

// decodeReview reads an optional reviewInput; an empty body is allowed.
func decodeReview(w http.ResponseWriter, r *http.Request) (reviewInput, error) {
	var in reviewInput
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		return in, badRequest("invalid JSON body", err)
	}
	in.Actor = strings.TrimSpace(in.Actor)
	if in.Actor == "" {
		in.Actor = "admin"
	}
	if len(in.Actor) > 100 || len(in.Reason) > 2000 {
		return in, badRequest("actor or reason too long", nil)
	}
	return in, nil
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid id", err)
	}
	return id, nil
}

func (srv *Server) generateIdentityCandidates(w http.ResponseWriter, r *http.Request) {
	in, err := decodeReview(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if in.EntityType == "" {
		in.EntityType = r.URL.Query().Get("entity_type")
	}
	if in.EntityType != "" && in.EntityType != "performer" && in.EntityType != "release" {
		reply(w, nil, badRequest("entity_type must be performer or release", nil))
		return
	}
	v, err := srv.identity.GenerateIdentityCandidates(r.Context(), in.EntityType)
	reply(w, v, err)
}

func (srv *Server) mergeCandidate(w http.ResponseWriter, r *http.Request) {
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
	v, err := srv.identity.MergeCandidate(r.Context(), id, in.TargetID, in.Actor, in.Reason)
	reply(w, v, err)
}

func (srv *Server) notDuplicateCandidate(w http.ResponseWriter, r *http.Request) {
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
	v, err := srv.identity.MarkNotDuplicate(r.Context(), id, in.Actor, in.Reason)
	reply(w, v, err)
}

func (srv *Server) undoMerge(w http.ResponseWriter, r *http.Request) {
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
	v, err := srv.identity.UndoMerge(r.Context(), id, in.Actor, in.Reason)
	reply(w, v, err)
}

func (srv *Server) upsertCrawlPolicy(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	var req store.CrawlPolicy
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.MinRequestInterval == "" {
		http.Error(w, "min_request_interval is required", 400)
		return
	}
	req.SourceID = id
	reply(w, map[string]bool{"ok": true}, srv.store.UpsertCrawlPolicy(r.Context(), req))
}

func (srv *Server) conflicts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.PendingConflicts(r.Context(), limit)
	reply(w, v, e)
}

// resolveConflict settles a pending field conflict. Body:
// {"value": <json>, "source_record_id": n, "note": "..."}; value may be
// omitted when source_record_id is given (adopt that source's value). The
// actor is X-Admin-User. 400 for an unreviewable field or a bad value, 404
// for an unknown conflict, 409 when it is no longer pending.
func (srv *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
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
	var req struct {
		SourceRecordID int64           `json:"source_record_id"`
		Value          json.RawMessage `json:"value"`
		Note           string          `json:"note"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	if len(req.Value) == 0 && req.SourceRecordID <= 0 {
		reply(w, nil, badRequest("value or source_record_id is required", nil))
		return
	}
	if len(req.Note) > store.MaxReasonLen {
		reply(w, nil, badRequest("note too long", nil))
		return
	}
	res, err := srv.editor.ResolveConflict(r.Context(), id, req.SourceRecordID, req.Value, req.Note, actor)
	reply(w, res, err)
}

func (srv *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var x model.Source
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&x) != nil || strings.TrimSpace(x.Name) == "" || !scraper.ValidURL(x.URL) {
		http.Error(w, "name and HTTP(S) feed URL required", 400)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.store.AddSource(r.Context(), x))
}
