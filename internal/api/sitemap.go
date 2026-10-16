package api

// Sitemap sources and the 收录健康分 report (PRD 13 SEO; AdminDashboard).
//
// Public, read-only, for the SSR web server (web/app/lib/sitemap.server.ts):
//
//	GET /api/sitemap/releases?cursor=&limit=    published, playable releases
//	GET /api/sitemap/performers?cursor=&limit=  active performers with a published release
//	GET /api/sitemap/tags?cursor=&limit=        tags of published releases
//	GET /api/sitemap/playlists?cursor=&limit=   public playlists (slug = share_token; never link-only)
//	GET /api/sitemap/counts                     list sizes, for coverage checks
//
// Each list answers {"items":[{"slug","updated_at"}],"next_cursor"} with up
// to limit (default 5000, max 10000) rows in a stable key order.
//
// Admin (ADMIN_TOKEN), written by `npm run seo:check` (web/scripts/seo-check.mjs):
//
//	POST /api/admin/seo/report   record one check run
//	GET  /api/admin/seo/report   the latest run (404 before the first)
//
// A run is stored as a nightly_runs row with job "seo.check", so it shows in
// the dashboard's nightly run list; detail carries the whole report plus
// score = round(100 × (ssr_pass_rate × 0.6 + sitemap_coverage × 0.4)) and
// its PRD 11 band, which the dashboard shows as 收录健康分.

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// SEOCheckJob is the nightly_runs.job of a recorded SEO check.
const SEOCheckJob = audit.SEOCheckJob

type sitemapStore interface {
	SitemapReleases(ctx context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error)
	SitemapPerformers(ctx context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error)
	SitemapTags(ctx context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error)
	SitemapPlaylists(ctx context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error)
	SitemapCounts(ctx context.Context) (store.SitemapCounts, error)
	RecordRun(ctx context.Context, r audit.Run) error
	LatestRun(ctx context.Context, job string) (audit.Run, bool, error)
}

func (srv *Server) sitemapRoutes() []route {
	return []route{
		{"GET /api/sitemap/releases", srv.sitemapList(func(s sitemapStore) sitemapLister { return s.SitemapReleases })},
		{"GET /api/sitemap/performers", srv.sitemapList(func(s sitemapStore) sitemapLister { return s.SitemapPerformers })},
		{"GET /api/sitemap/tags", srv.sitemapList(func(s sitemapStore) sitemapLister { return s.SitemapTags })},
		{"GET /api/sitemap/playlists", srv.sitemapList(func(s sitemapStore) sitemapLister { return s.SitemapPlaylists })},
		{"GET /api/sitemap/counts", srv.sitemapCounts},
		{"POST /api/admin/seo/report", srv.admin(srv.recordSEOReport)},
		{"GET /api/admin/seo/report", srv.admin(srv.latestSEOReport)},
	}
}

type sitemapLister func(ctx context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error)

var errSitemapUnavailable = errors.New("sitemap store not configured")

func (srv *Server) sitemapList(pick func(sitemapStore) sitemapLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.sitemap == nil {
			reply(w, nil, errSitemapUnavailable)
			return
		}
		q := r.URL.Query()
		limit, err := limitParam(q)
		if err != nil {
			reply(w, nil, err)
			return
		}
		items, next, err := pick(srv.sitemap)(r.Context(), strings.TrimSpace(q.Get("cursor")), limit)
		if err == nil {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		reply(w, page(items, next), err)
	}
}

func (srv *Server) sitemapCounts(w http.ResponseWriter, r *http.Request) {
	if srv.sitemap == nil {
		reply(w, nil, errSitemapUnavailable)
		return
	}
	c, err := srv.sitemap.SitemapCounts(r.Context())
	reply(w, c, err)
}

// SEOReport is the body of POST /api/admin/seo/report. Unknown fields are
// kept in the stored detail.
type SEOReport struct {
	// Base URL of the web server that was checked.
	BaseURL string `json:"base_url"`
	// SSRPassRate: share of sampled pages that passed (200, non-empty h1,
	// canonical, hreflang alternates), 0–1.
	SSRPassRate *float64 `json:"ssr_pass_rate"`
	// SitemapCoverage: sitemap URLs found / entities that should be listed, 0–1.
	SitemapCoverage *float64 `json:"sitemap_coverage"`
	// Checked / Failed: sampled pages.
	Checked int `json:"checked"`
	Failed  int `json:"failed"`
	// DurationMS of the check run.
	DurationMS int64 `json:"duration_ms"`
}

// SEOScore is the 收录健康分: SSR pass rate × 0.6 + sitemap coverage × 0.4, 0–100.
func SEOScore(ssrPassRate, sitemapCoverage float64) int {
	return int(math.Round(100 * (clamp01(ssrPassRate)*0.6 + clamp01(sitemapCoverage)*0.4)))
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

const maxSEOReportBytes = 256 << 10

func (srv *Server) recordSEOReport(w http.ResponseWriter, r *http.Request) {
	if srv.sitemap == nil {
		reply(w, nil, errSitemapUnavailable)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxSEOReportBytes+1))
	if err != nil {
		reply(w, nil, badRequest("could not read body", err))
		return
	}
	if len(raw) > maxSEOReportBytes {
		reply(w, nil, badRequest("report too large", nil))
		return
	}
	var rep SEOReport
	var detail map[string]any
	if err = json.Unmarshal(raw, &rep); err == nil {
		err = json.Unmarshal(raw, &detail)
	}
	if err != nil {
		reply(w, nil, badRequest("invalid JSON", err))
		return
	}
	if rep.SSRPassRate == nil || rep.SitemapCoverage == nil {
		reply(w, nil, badRequest("ssr_pass_rate and sitemap_coverage are required", nil))
		return
	}
	for _, v := range []float64{*rep.SSRPassRate, *rep.SitemapCoverage} {
		if math.IsNaN(v) || v < 0 || v > 1 {
			reply(w, nil, badRequest("rates must be between 0 and 1", nil))
			return
		}
	}
	if rep.Checked < 0 || rep.Failed < 0 || rep.Failed > rep.Checked {
		reply(w, nil, badRequest("invalid checked / failed counts", nil))
		return
	}
	score := SEOScore(*rep.SSRPassRate, *rep.SitemapCoverage)
	detail["score"] = score
	detail["band"] = audit.HealthBand(score)
	result := "ok"
	if rep.Failed > 0 || *rep.SitemapCoverage < 1 {
		result = "issues"
	}
	run := audit.Run{
		Job:        SEOCheckJob,
		StartedAt:  time.Now().UTC(),
		DurationMS: max(rep.DurationMS, 0),
		Checked:    rep.Checked,
		Issues:     rep.Failed,
		Result:     result,
		Detail:     detail,
	}
	if err = srv.sitemap.RecordRun(r.Context(), run); err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(run)
}

func (srv *Server) latestSEOReport(w http.ResponseWriter, r *http.Request) {
	if srv.sitemap == nil {
		reply(w, nil, errSitemapUnavailable)
		return
	}
	run, ok, err := srv.sitemap.LatestRun(r.Context(), SEOCheckJob)
	if err == nil && !ok {
		reply(w, nil, &httpError{status: http.StatusNotFound, msg: "no seo check recorded yet"})
		return
	}
	reply(w, run, err)
}
