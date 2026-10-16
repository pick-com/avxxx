package api

// Admin data reports (ROADMAP 作品、演员和字段完整性报告; internal/completeness).
//
//	GET /api/admin/reports/completeness?entity=release|performer
//	    &source=<id or name>&group_by=field|source|maker&runs=14&top=5&format=json|csv

import (
	"avxxx/internal/completeness"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// reportStore is the subset of *store.Store behind the admin reports.
type reportStore interface {
	CompletenessReport(ctx context.Context, opts completeness.Options) (completeness.Report, error)
}

func (srv *Server) adminReportRoutes() []route {
	return []route{
		{"GET /api/admin/reports/completeness", srv.admin(srv.completenessReport)},
	}
}

// completenessReport returns the field completeness of releases or
// performers: per-field percentages (ranked by the popularity of what is
// missing), per-source coverage, optional groups and the nightly trend.
// format=csv exports the field rows.
func (srv *Server) completenessReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	opts := completeness.Options{
		Entity:  strings.TrimSpace(q.Get("entity")),
		GroupBy: strings.TrimSpace(q.Get("group_by")),
		Source:  strings.TrimSpace(q.Get("source")),
	}
	format := strings.TrimSpace(q.Get("format"))
	if format != "" && format != "json" && format != "csv" {
		reply(w, nil, badRequest("format must be json or csv", nil))
		return
	}
	var err error
	if opts.TrendRuns, err = intParam(q, "runs", completeness.MaxTrendRuns); err != nil {
		reply(w, nil, err)
		return
	}
	if opts.TopMissing, err = intParam(q, "top", 50); err != nil {
		reply(w, nil, err)
		return
	}
	if err = opts.Validate(); err != nil {
		reply(w, nil, badRequest(err.Error(), nil))
		return
	}
	if srv.reports == nil {
		reply(w, nil, errUnavailable)
		return
	}
	rep, err := srv.reports.CompletenessReport(r.Context(), opts)
	if errors.Is(err, completeness.ErrUnknownSource) {
		err = badRequest(completeness.ErrUnknownSource.Error(), err)
	}
	if err != nil || format != "csv" {
		reply(w, rep, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="completeness-`+opts.Entity+`-`+opts.GroupBy+`.csv"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := completeness.WriteCSV(w, rep); err != nil {
		slog.Error("completeness csv", "err", err) // headers already sent
	}
}
