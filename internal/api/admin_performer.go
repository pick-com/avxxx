package api

// Admin performer editor (AdminActors / A9, PRD 6, 11, 15): performer list
// with completeness filters and counts, detail, field edits (manual
// overrides), aliases, hide / unhide, personal-information requests, and
// the similarity weights preview. Every admin route sits behind srv.admin;
// the authenticated admin is recorded as review_events.actor. Validation
// errors are 400, unknown ids 404, stale versions and decided requests 409.
//
// POST /api/performer-requests is the public intake of the correction /
// copyright page: no login, minimal fields, rate limited per client address
// and globally. Its response never echoes evidence or contact details.

import (
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// performerAdmin is the subset of *store.Store behind the performer routes,
// as an interface for database-free tests.
type performerAdmin interface {
	AdminPerformers(ctx context.Context, f store.AdminPerformerFilter) ([]store.AdminPerformerSummary, string, error)
	AdminPerformerCounts(ctx context.Context, q, status string) (map[string]int64, error)
	AdminPerformer(ctx context.Context, id int64) (store.AdminPerformer, error)
	PatchPerformer(ctx context.Context, id int64, p store.PerformerPatch, actor string) (store.PerformerPatchResult, error)
	ClearPerformerOverride(ctx context.Context, id int64, field, reason, actor string) error
	AddPerformerAlias(ctx context.Context, id int64, in store.AliasInput, actor string) (store.PerformerAlias, error)
	RemovePerformerAlias(ctx context.Context, id, aliasID int64, reason, actor string) error
	SetPerformerStatus(ctx context.Context, id int64, c store.PerformerStatusChange, actor string) (store.PerformerStatusResult, error)
	PerformerRequests(ctx context.Context, f store.PerformerRequestFilter) ([]store.PerformerRequest, string, error)
	DecidePerformerRequest(ctx context.Context, id int64, to string, d store.RequestDecision, actor string) (store.RequestDecisionResult, error)
	CreatePerformerRequest(ctx context.Context, in store.PerformerRequestInput, now time.Time) (store.PerformerRequestReceipt, error)
	SimilarityWeightsRow(ctx context.Context) (store.SimilarityWeightsRow, error)
	SimilaritySeeds(ctx context.Context, ids []int64, n int) ([]store.SimilaritySeed, error)
}

// Public intake limits (POST /api/performer-requests).
const (
	performerRequestPerIP     = 5
	performerRequestGlobal    = 300
	performerRequestWindow    = time.Hour
	similarityPreviewSample   = 20
	similarityPreviewMax      = 50
	similarityPreviewTopK     = 5
	similarityPreviewMinScore = 0.05 // recs.PerformerConfig default
)

func (srv *Server) adminPerformerRoutes() []route {
	p := srv.withPerformers
	return []route{
		{"GET /api/admin/performers", srv.admin(p(srv.adminPerformerList))},
		{"GET /api/admin/performers/{id}", srv.admin(p(srv.adminPerformerDetail))},
		{"PATCH /api/admin/performers/{id}", srv.admin(p(srv.adminPerformerPatch))},
		{"DELETE /api/admin/performers/{id}/overrides/{field}", srv.admin(p(srv.adminPerformerClearOverride))},
		{"POST /api/admin/performers/{id}/aliases", srv.admin(p(srv.adminPerformerAliasAdd))},
		{"DELETE /api/admin/performers/{id}/aliases/{alias_id}", srv.admin(p(srv.adminPerformerAliasRemove))},
		{"POST /api/admin/performers/{id}/status", srv.admin(p(srv.adminPerformerStatus))},
		{"GET /api/admin/performer-requests", srv.admin(p(srv.adminPerformerRequests))},
		{"POST /api/admin/performer-requests/{id}/approve", srv.admin(p(srv.adminPerformerRequestDecide(store.RequestApproved)))},
		{"POST /api/admin/performer-requests/{id}/reject", srv.admin(p(srv.adminPerformerRequestDecide(store.RequestRejected)))},
		{"POST /api/admin/similarity-weights/preview", srv.admin(p(srv.adminSimilarityPreview))},
		{"POST /api/performer-requests", p(srv.createPerformerRequest)},
	}
}

func (srv *Server) withPerformers(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.performerEd == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// adminPerformerList: ?q=&status=active|hidden&filter=a,b (repeatable;
// ANDed; see store.PerformerFilters)&cursor=&limit=. Returns items,
// next_cursor and counts per filter (for q and status, ignoring filter).
func (srv *Server) adminPerformerList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.AdminPerformerFilter{Q: strings.TrimSpace(q.Get("q")), Status: strings.TrimSpace(q.Get("status")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	if f.Status != "" && f.Status != store.PerformerActive && f.Status != store.PerformerHidden {
		reply(w, nil, badRequest("status must be active or hidden", nil))
		return
	}
	if len(f.Q) > 200 {
		reply(w, nil, badRequest("q too long", nil))
		return
	}
	for _, raw := range q["filter"] {
		for _, name := range strings.Split(raw, ",") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			if !store.ValidPerformerFilter(name) {
				reply(w, nil, badRequest("filter must be among "+strings.Join(store.PerformerFilters, ", "), nil))
				return
			}
			f.Filters = append(f.Filters, name)
		}
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f.Limit = limit
	items, next, err := srv.performerEd.AdminPerformers(r.Context(), f)
	if err != nil {
		reply(w, nil, err)
		return
	}
	counts, err := srv.performerEd.AdminPerformerCounts(r.Context(), f.Q, f.Status)
	out := page(items, next)
	out["counts"] = counts
	reply(w, out, err)
}

func (srv *Server) adminPerformerDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.performerEd.AdminPerformer(r.Context(), id)
	reply(w, v, err)
}

// adminPerformerPatch edits performer fields (store.ParsePerformerPatch);
// "version" is the optimistic lock (409 when stale). A birth_date change
// re-runs R-AGE on every release of the performer; age_recheck reports how
// many changed.
func (srv *Server) adminPerformerPatch(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeStrict(w, r, &body, false); err != nil {
		reply(w, nil, err)
		return
	}
	p, err := store.ParsePerformerPatch(body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.performerEd.PatchPerformer(r.Context(), id, p, actor)
	reply(w, v, err)
}

// adminPerformerClearOverride removes one manual override. Body
// (optional): {"reason": "..."}.
func (srv *Server) adminPerformerClearOverride(w http.ResponseWriter, r *http.Request) {
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
	err = srv.performerEd.ClearPerformerOverride(r.Context(), id, field, reason, actor)
	reply(w, map[string]bool{"ok": true}, err)
}

// adminPerformerAliasAdd: {"name", "language"?, "alias_type"? (stage |
// birth | romanized | former | other), "reason"?}. 201; 409 when the alias
// exists.
func (srv *Server) adminPerformerAliasAdd(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in store.AliasInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.performerEd.AddPerformerAlias(r.Context(), id, in, actor)
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

func (srv *Server) adminPerformerAliasRemove(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	aliasID, err := pathInt(r, "alias_id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.performerEd.RemovePerformerAlias(r.Context(), id, aliasID, reason, actor)
	reply(w, map[string]bool{"ok": true}, err)
}

// adminPerformerStatus hides or unhides: {"status": "hidden" | "active",
// "reason": "..." (required to hide), "version": n}. Never deletes.
func (srv *Server) adminPerformerStatus(w http.ResponseWriter, r *http.Request) {
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var c store.PerformerStatusChange
	if err := decodeStrict(w, r, &c, false); err != nil {
		reply(w, nil, err)
		return
	}
	if c.To != store.PerformerActive && c.To != store.PerformerHidden {
		reply(w, nil, badRequest("status must be active or hidden", nil))
		return
	}
	v, err := srv.performerEd.SetPerformerStatus(r.Context(), id, c, actor)
	reply(w, v, err)
}

// adminPerformerRequests: ?status=open|approved|rejected&kind=&performer_id=
// &cursor=&limit=, earliest due first.
func (srv *Server) adminPerformerRequests(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.PerformerRequestFilter{Status: strings.TrimSpace(q.Get("status")), Kind: strings.TrimSpace(q.Get("kind")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	if f.Status != "" && !slices.Contains(store.RequestStatuses, f.Status) {
		reply(w, nil, badRequest("status must be one of "+strings.Join(store.RequestStatuses, ", "), nil))
		return
	}
	if f.Kind != "" && !slices.Contains(store.RequestKinds, f.Kind) {
		reply(w, nil, badRequest("kind must be one of "+strings.Join(store.RequestKinds, ", "), nil))
		return
	}
	if raw := strings.TrimSpace(q.Get("performer_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			reply(w, nil, badRequest("invalid performer_id", err))
			return
		}
		f.PerformerID = id
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f.Limit = limit
	items, next, err := srv.performerEd.PerformerRequests(r.Context(), f)
	reply(w, page(items, next), err)
}

// adminPerformerRequestDecide approves or rejects an open request. Body:
// {"note": "..." (required to reject), "value": ... (approve correct_field
// only: replaces the proposed value)}. A decided request is 409.
func (srv *Server) adminPerformerRequestDecide(to string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, actor, ok := srv.editorCall(w, r)
		if !ok {
			return
		}
		var d store.RequestDecision
		if err := decodeStrict(w, r, &d, true); err != nil {
			reply(w, nil, err)
			return
		}
		if to == store.RequestRejected && strings.TrimSpace(d.Note) == "" {
			reply(w, nil, badRequest("note is required to reject a request", nil))
			return
		}
		v, err := srv.performerEd.DecidePerformerRequest(r.Context(), id, to, d, actor)
		reply(w, v, err)
	}
}

// createPerformerRequest is the public intake: {"performer": slug, "kind":
// correct_field | hide_birth_date | remove_photo | other, "contact",
// "field"/"value" (correct_field), "role" (self | agency | other),
// "message", "links"}. 201 with {id, kind, status, due_at}; 429 when rate
// limited. Evidence stays with the request for admins only.
func (srv *Server) createPerformerRequest(w http.ResponseWriter, r *http.Request) {
	var in store.PerformerRequestInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowPerformerRequest(r); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.performerEd.CreatePerformerRequest(r.Context(), in, time.Now())
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// allowPerformerRequest applies the per-address and global intake limits.
// The address is the TCP peer, or with TRUSTED_PROXIES the client address
// the trusted proxy forwarded (clientip); without it, behind a proxy every
// request shares the proxy's address and the global limit is what protects
// the queue.
func (srv *Server) allowPerformerRequest(r *http.Request) error {
	if srv.limiter == nil {
		return nil
	}
	ip := srv.clientIP.IP(r)
	for _, l := range []struct {
		key   string
		limit int
	}{{"performer_request:ip:" + ip, performerRequestPerIP}, {"performer_request:all", performerRequestGlobal}} {
		ok, err := srv.limiter.Allow(r.Context(), l.key, l.limit, performerRequestWindow)
		if err != nil {
			return err
		}
		if !ok {
			return tooManyRequests("too many requests, try again later")
		}
	}
	return nil
}

// ---------------------------------------------------------------- similarity preview

// previewPerformer is a performer in the preview payload.
type previewPerformer struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// previewMatch is a ranked neighbour with its slug and name (id, score,
// match_percent, rank, stored_rank and reason come from recs.Ranked).
type previewMatch struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	recs.Ranked
}

type previewSample struct {
	Performer previewPerformer `json:"performer"`
	Current   []previewMatch   `json:"current"`
	Proposed  []previewMatch   `json:"proposed"`
	// Changed: the proposed top list differs from the current one (ids or
	// order); Overlap counts performers in both.
	Changed bool `json:"changed"`
	Overlap int  `json:"overlap"`
}

// adminSimilarityPreview shows the top similar performers of sample
// performers under proposed weights without saving them (AdminActors:
// 预览后次日生效). Body: {"metadata", "co_star", "co_watch", "visual"
// (omitted = current value), "performer_ids": [...] (optional, ≤ 50),
// "sample": n (default 20), "top": k (default 5)}. Scores are recomputed
// from the parts stored in performer_similar.evidence, so only stored
// neighbours can be re-ranked.
func (srv *Server) adminSimilarityPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Metadata     *float64 `json:"metadata"`
		CoStar       *float64 `json:"co_star"`
		CoWatch      *float64 `json:"co_watch"`
		Visual       *float64 `json:"visual"`
		PerformerIDs []int64  `json:"performer_ids"`
		Sample       int      `json:"sample"`
		Top          int      `json:"top"`
	}
	if err := decodeStrict(w, r, &body, true); err != nil {
		reply(w, nil, err)
		return
	}
	if len(body.PerformerIDs) > similarityPreviewMax || body.Sample < 0 || body.Sample > similarityPreviewMax || body.Top < 0 || body.Top > 50 {
		reply(w, nil, badRequest("performer_ids and sample are at most 50, top at most 50", nil))
		return
	}
	if body.Sample == 0 {
		body.Sample = similarityPreviewSample
	}
	if body.Top == 0 {
		body.Top = similarityPreviewTopK
	}
	cur, err := srv.performerEd.SimilarityWeightsRow(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	wt := cur.Weights
	for _, x := range []struct{ src, dst *float64 }{{body.Metadata, &wt.Metadata}, {body.CoStar, &wt.CoStar}, {body.CoWatch, &wt.CoWatch}, {body.Visual, &wt.Visual}} {
		if x.src != nil {
			*x.dst = *x.src
		}
	}
	if msg := wt.Validate(); msg != "" {
		reply(w, nil, badRequest(msg, nil))
		return
	}
	seeds, err := srv.performerEd.SimilaritySeeds(r.Context(), body.PerformerIDs, body.Sample)
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, map[string]any{
		"weights":         wt,
		"current_weights": cur.Weights,
		"saved":           false,
		"effective_from":  nextNightly,
		"samples":         similarityPreview(seeds, cur.Weights, wt, body.Top),
		"note":            "scores are recomputed from the parts stored by the last nightly run; save with PUT /api/admin/similarity-weights",
	}, nil)
}

// similarityPreview ranks each seed's stored neighbours under the current
// and the proposed weights.
func similarityPreview(seeds []store.SimilaritySeed, current, proposed recs.Weights, top int) []previewSample {
	out := make([]previewSample, 0, len(seeds))
	for _, sd := range seeds {
		names := map[int64]previewPerformer{}
		cands := make([]recs.StoredNeighbor, 0, len(sd.Neighbors))
		for _, n := range sd.Neighbors {
			names[n.ID] = previewPerformer{ID: n.ID, Slug: n.Slug, Name: n.Name}
			cands = append(cands, recs.StoredNeighbor{ID: n.ID, Stored: n.Score, Evidence: n.Evidence})
		}
		wrap := func(rs []recs.Ranked) []previewMatch {
			m := make([]previewMatch, len(rs))
			for i, x := range rs {
				m[i] = previewMatch{Slug: names[x.ID].Slug, Name: names[x.ID].Name, Ranked: x}
			}
			return m
		}
		cur := recs.Rerank(cands, current, top, similarityPreviewMinScore)
		next := recs.Rerank(cands, proposed, top, similarityPreviewMinScore)
		s := previewSample{Performer: previewPerformer{ID: sd.ID, Slug: sd.Slug, Name: sd.Name}, Current: wrap(cur), Proposed: wrap(next)}
		in := map[int64]bool{}
		for _, x := range cur {
			in[x.ID] = true
		}
		s.Changed = len(cur) != len(next)
		for i, x := range next {
			if in[x.ID] {
				s.Overlap++
			}
			if i < len(cur) && cur[i].ID != x.ID {
				s.Changed = true
			}
		}
		out = append(out, s)
	}
	return out
}
