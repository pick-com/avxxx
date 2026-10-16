package api

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// auditAdmin is the subset of *store.Store used by the admin audit and
// dashboard handlers, as an interface so they can be tested without a database.
type auditAdmin interface {
	AuditIssues(ctx context.Context, f audit.IssueFilter) ([]audit.Issue, string, error)
	DismissAuditIssue(ctx context.Context, id int64, note string) error
	EnqueueAudit(ctx context.Context) (jobID int64, enqueued bool, err error)
	AdminDashboard(ctx context.Context) (audit.Dashboard, error)
	SourceHealthList(ctx context.Context, f audit.SourceHealthFilter) ([]audit.SourceHealthItem, string, error)
}

func (srv *Server) adminAuditRoutes() []route {
	return []route{
		{"GET /api/admin/audit/issues", srv.admin(srv.auditIssues)},
		{"POST /api/admin/audit/issues/{id}/dismiss", srv.admin(srv.dismissAuditIssue)},
		{"POST /api/admin/audit/run", srv.admin(srv.runAudit)},
		{"GET /api/admin/dashboard", srv.admin(srv.dashboard)},
		{"GET /api/admin/playback-sources/health", srv.admin(srv.sourceHealth)},
	}
}

// auditIssues lists audit issues: ?rule=R-AGE&status=open|fixed|dismissed&limit=&cursor=.
// Ordered by popularity (priority) then recency.
func (srv *Server) auditIssues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := audit.IssueFilter{Rule: audit.Rule(strings.TrimSpace(q.Get("rule"))), Status: strings.TrimSpace(q.Get("status")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	if f.Rule != "" && !audit.KnownRule(f.Rule) {
		reply(w, nil, badRequest("unknown rule", nil))
		return
	}
	switch f.Status {
	case "", audit.StatusOpen, audit.StatusFixed, audit.StatusDismissed:
	default:
		reply(w, nil, badRequest("status must be open, fixed or dismissed", nil))
		return
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f.Limit = limit
	items, next, err := srv.audit.AuditIssues(r.Context(), f)
	reply(w, page(items, next), err)
}

// dismissAuditIssue closes an open issue as not a problem. Body (optional):
// {"note": "..."}. It never unholds a release.
func (srv *Server) dismissAuditIssue(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid id", err))
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	err = srv.audit.DismissAuditIssue(r.Context(), id, strings.TrimSpace(body.Note))
	if errors.Is(err, audit.ErrIssueNotOpen) {
		err = &httpError{status: http.StatusConflict, msg: audit.ErrIssueNotOpen.Error(), err: err}
	}
	reply(w, map[string]bool{"ok": true}, err)
}

// runAudit enqueues a full audit run for the audit worker (cmd/audit).
// 202 either way; enqueued=false means a run was already pending or running.
func (srv *Server) runAudit(w http.ResponseWriter, r *http.Request) {
	id, enqueued, err := srv.audit.EnqueueAudit(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{"job_id": id, "enqueued": enqueued})
}

// dashboard is the AdminDashboard (A1) overview.
func (srv *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	v, err := srv.audit.AdminDashboard(r.Context())
	reply(w, v, err)
}

// sourceHealth lists playback sources by health, worst first (never-scored
// sources last): ?band=severe|degraded|healthy|excellent|insufficient_data
// &trend=N (runs per source, default 12, max 144)&limit=&cursor=.
func (srv *Server) sourceHealth(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := audit.SourceHealthFilter{Band: strings.TrimSpace(q.Get("band")), Cursor: strings.TrimSpace(q.Get("cursor"))}
	if f.Band != "" && !audit.ValidSourceBand(f.Band) {
		reply(w, nil, badRequest("band must be severe, degraded, healthy, excellent or insufficient_data", nil))
		return
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	trend, err := intParam(q, "trend", store.MaxHealthTrend)
	if err != nil {
		reply(w, nil, err)
		return
	}
	f.Limit, f.Trend = limit, trend
	items, next, err := srv.audit.SourceHealthList(r.Context(), f)
	reply(w, page(items, next), err)
}
