package api

// Admin video editor (AdminVideo / A3, PRD 11): release list and detail,
// field edits (written as manual overrides), status transitions, playback
// sources and performer credits. Every route sits behind srv.admin; the
// authenticated admin is recorded as review_events.actor. Validation errors are 400, unknown ids 404, stale versions and
// forbidden transitions 409.
//
// Out of scope: screenshots and chapters (PRD 5.3) need the transcoding
// pipeline (24 auto captures per video), which does not exist yet, so there
// are no endpoints for them.

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// releaseEditor is the subset of *store.Store behind the admin video editor
// and conflict resolution, as an interface for database-free tests.
type releaseEditor interface {
	AdminReleases(ctx context.Context, f store.AdminReleaseFilter) ([]store.AdminReleaseSummary, string, error)
	AdminRelease(ctx context.Context, id int64) (store.AdminRelease, error)
	PatchRelease(ctx context.Context, id int64, p store.ReleasePatch, actor string) (store.PatchResult, error)
	TransitionRelease(ctx context.Context, id int64, t store.Transition, actor string) (store.TransitionResult, error)
	ClearReleaseOverride(ctx context.Context, id int64, field, reason, actor string) error
	AdminPlaybackSources(ctx context.Context, releaseID int64) ([]store.AdminPlaybackSource, error)
	CreatePlaybackSource(ctx context.Context, releaseID int64, in store.SourceInput, actor string) (store.AdminPlaybackSource, error)
	UpdatePlaybackSource(ctx context.Context, releaseID, sourceID int64, patch json.RawMessage, actor string) (store.AdminPlaybackSource, error)
	ReorderPlaybackSources(ctx context.Context, releaseID int64, ids []int64, actor string) ([]store.AdminPlaybackSource, error)
	SetPlaybackSourceEnabled(ctx context.Context, releaseID, sourceID int64, enable bool, reason, actor string) (store.AdminPlaybackSource, error)
	AdminPromoteMediaSource(ctx context.Context, in store.PromoteMediaSourceInput, actor string) (store.AdminPlaybackSource, error)
	SetReleasePerformer(ctx context.Context, releaseID int64, in store.ReleasePerformerInput, actor string) (store.PatchResult, error)
	RemoveReleasePerformer(ctx context.Context, releaseID, performerID int64, reason, actor string) (store.PatchResult, error)
	ResolveConflict(ctx context.Context, conflictID, sourceRecordID int64, value json.RawMessage, note, actor string) (store.ConflictResolution, error)
}

func (srv *Server) adminReleaseRoutes() []route {
	return []route{
		{"GET /api/admin/releases", srv.admin(srv.adminReleaseList)},
		{"GET /api/admin/releases/{id}", srv.admin(srv.adminReleaseDetail)},
		{"PATCH /api/admin/releases/{id}", srv.admin(srv.adminReleasePatch)},
		{"POST /api/admin/releases/{id}/status", srv.admin(srv.adminReleaseStatus)},
		{"DELETE /api/admin/releases/{id}/overrides/{field}", srv.admin(srv.adminReleaseClearOverride)},
		{"GET /api/admin/releases/{id}/sources", srv.admin(srv.adminSourceList)},
		{"POST /api/admin/releases/{id}/sources", srv.admin(srv.adminSourceCreate)},
		{"PATCH /api/admin/releases/{id}/sources/{source_id}", srv.admin(srv.adminSourceUpdate)},
		{"POST /api/admin/releases/{id}/sources/reorder", srv.admin(srv.adminSourceReorder)},
		{"POST /api/admin/releases/{id}/sources/promote", srv.admin(srv.adminSourcePromote)},
		{"POST /api/admin/releases/{id}/sources/{source_id}/disable", srv.admin(srv.adminSourceEnabled(false))},
		{"POST /api/admin/releases/{id}/sources/{source_id}/enable", srv.admin(srv.adminSourceEnabled(true))},
		{"PUT /api/admin/releases/{id}/performers/{performer_id}", srv.admin(srv.adminPerformerSet)},
		{"DELETE /api/admin/releases/{id}/performers/{performer_id}", srv.admin(srv.adminPerformerRemove)},
	}
}

// pathInt parses a positive integer path value.
func pathInt(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid "+name, err)
	}
	return id, nil
}

// decodeStrict reads a bounded JSON body into v, rejecting unknown fields.
// An empty body is allowed when optional.
func decodeStrict(w http.ResponseWriter, r *http.Request, v any, optional bool) error {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if optional && errors.Is(err, io.EOF) {
			return nil
		}
		return badRequest("invalid JSON body", err)
	}
	return nil
}

// editorCall resolves the release id and the actor shared by every handler.
func (srv *Server) editorCall(w http.ResponseWriter, r *http.Request) (int64, string, bool) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return 0, "", false
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return 0, "", false
	}
	return id, actor, true
}

// adminReleaseList: ?q=&status=&cursor=&limit=. Every status, drafts and
// held releases included; newest first.
func (srv *Server) adminReleaseList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AdminReleaseFilter{Q: strings.TrimSpace(q.Get("q")), Status: strings.TrimSpace(q.Get("status")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	if f.Status != "" && !store.ValidReleaseStatus(f.Status) {
		reply(w, nil, badRequest("status must be one of "+strings.Join(store.ReleaseStatuses, ", "), nil))
		return
	}
	if len(f.Q) > 200 {
		reply(w, nil, badRequest("q too long", nil))
		return
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f.Limit = limit
	items, next, err := srv.editor.AdminReleases(r.Context(), f)
	reply(w, page(items, next), err)
}

func (srv *Server) adminReleaseDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.AdminRelease(r.Context(), id)
	reply(w, v, err)
}

// adminReleasePatch edits release fields (see store.ParseReleasePatch);
// optional "version" is the optimistic lock (409 when stale).
func (srv *Server) adminReleasePatch(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeStrict(w, r, &body, false); err != nil {
		reply(w, nil, err)
		return
	}
	p, err := store.ParseReleasePatch(body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.PatchRelease(r.Context(), id, p, actor)
	reply(w, v, err)
}

// adminReleaseStatus changes the status. Body: {"status": "published" |
// "upcoming" | "unpublished" | "draft", "scheduled_at": RFC 3339 (upcoming
// only), "override_reason": "..." (required to publish held_review),
// "reason": "...", "version": n}. Publishing requires R-AGE to pass.
func (srv *Server) adminReleaseStatus(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var t store.Transition
	if err := decodeStrict(w, r, &t, false); err != nil {
		reply(w, nil, err)
		return
	}
	if !store.ValidReleaseStatus(t.To) {
		reply(w, nil, badRequest("status must be one of "+strings.Join(store.ReleaseStatuses, ", "), nil))
		return
	}
	v, err := srv.editor.TransitionRelease(r.Context(), id, t, actor)
	reply(w, v, err)
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func decodeReason(w http.ResponseWriter, r *http.Request) (string, error) {
	var b reasonBody
	if err := decodeStrict(w, r, &b, true); err != nil {
		return "", err
	}
	if len(b.Reason) > store.MaxReasonLen {
		return "", badRequest("reason too long", nil)
	}
	return strings.TrimSpace(b.Reason), nil
}

// adminReleaseClearOverride removes one manual override; the next crawl may
// write the field again. Body (optional): {"reason": "..."}.
func (srv *Server) adminReleaseClearOverride(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	field := r.PathValue("field")
	if field == "" || len(field) > 64 {
		reply(w, nil, badRequest("invalid field", nil))
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.editor.ClearReleaseOverride(r.Context(), id, field, reason, actor)
	reply(w, map[string]bool{"ok": true}, err)
}

func (srv *Server) adminSourceList(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.AdminPlaybackSources(r.Context(), id)
	reply(w, map[string]any{"items": v}, err)
}

// adminSourceCreate adds a playback source (store.SourceInput). license_ref
// is required for every kind except official_preview and external_link.
func (srv *Server) adminSourceCreate(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in store.SourceInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.CreatePlaybackSource(r.Context(), id, in, actor)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(v)
}

// adminSourceUpdate patches a source; only the keys present change.
func (srv *Server) adminSourceUpdate(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	sourceID, err := pathInt(r, "source_id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	var patch json.RawMessage
	if err := decodeStrict(w, r, &patch, false); err != nil {
		reply(w, nil, err)
		return
	}
	if len(patch) == 0 || patch[0] != '{' {
		reply(w, nil, badRequest("body must be a JSON object", nil))
		return
	}
	v, err := srv.editor.UpdatePlaybackSource(r.Context(), id, sourceID, patch, actor)
	reply(w, v, err)
}

// adminSourceReorder: {"ids": [...]} lists every source of the release in
// the new priority order.
func (srv *Server) adminSourceReorder(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var b struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeStrict(w, r, &b, false); err != nil {
		reply(w, nil, err)
		return
	}
	if len(b.IDs) == 0 || len(b.IDs) > 200 {
		reply(w, nil, badRequest("ids must list the release's sources", nil))
		return
	}
	v, err := srv.editor.ReorderPlaybackSources(r.Context(), id, b.IDs, actor)
	reply(w, map[string]any{"items": v}, err)
}

// adminSourcePromote copies a crawled release_media_sources row into
// playback_sources (store.PromoteMediaSource). Body: {"source", "kind",
// "label", "version_kind", "priority", "max_height", "duration_seconds",
// "license_ref"}.
func (srv *Server) adminSourcePromote(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var b struct {
		Source          string `json:"source"`
		Kind            string `json:"kind"`
		Label           string `json:"label"`
		VersionKind     string `json:"version_kind"`
		Priority        int    `json:"priority"`
		MaxHeight       *int   `json:"max_height"`
		DurationSeconds *int   `json:"duration_seconds"`
		LicenseRef      string `json:"license_ref"`
	}
	if err := decodeStrict(w, r, &b, false); err != nil {
		reply(w, nil, err)
		return
	}
	if strings.TrimSpace(b.Source) == "" || strings.TrimSpace(b.Kind) == "" || strings.TrimSpace(b.Label) == "" {
		reply(w, nil, badRequest("source, kind and label are required", nil))
		return
	}
	v, err := srv.editor.AdminPromoteMediaSource(r.Context(), store.PromoteMediaSourceInput{ReleaseID: id, Source: strings.TrimSpace(b.Source),
		Kind: b.Kind, Label: b.Label, VersionKind: b.VersionKind, Priority: b.Priority, MaxHeight: b.MaxHeight, DurationSeconds: b.DurationSeconds,
		LicenseRef: b.LicenseRef}, actor)
	reply(w, v, err)
}

// adminSourceEnabled disables or enables a source. Body (optional):
// {"reason": "..."}; re-enabling a source the health job disabled or
// degraded requires a reason.
func (srv *Server) adminSourceEnabled(enable bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, actor, ok := srv.editorCall(w, r)
		if !ok {
			return
		}
		sourceID, err := pathInt(r, "source_id")
		if err != nil {
			reply(w, nil, err)
			return
		}
		reason, err := decodeReason(w, r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		v, err := srv.editor.SetPlaybackSourceEnabled(r.Context(), id, sourceID, enable, reason, actor)
		reply(w, v, err)
	}
}

// adminPerformerSet credits a performer: {"role": "lead"|"guest",
// "billing_order": n, "credited_name": "...", "reason": "..."}.
func (srv *Server) adminPerformerSet(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	performerID, err := pathInt(r, "performer_id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in store.ReleasePerformerInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	in.PerformerID = performerID
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.SetReleasePerformer(r.Context(), id, in, actor)
	reply(w, v, err)
}

func (srv *Server) adminPerformerRemove(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	performerID, err := pathInt(r, "performer_id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.editor.RemoveReleasePerformer(r.Context(), id, performerID, reason, actor)
	reply(w, v, err)
}
