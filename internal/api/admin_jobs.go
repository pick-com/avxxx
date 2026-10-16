package api

// Admin 夜间任务 (/admin/jobs): the jobs queue and the nightly_runs history.
//
//	GET  /api/admin/jobs/queue          per-kind counts, pending / running / failed (7 days) jobs with lease and attempts, runnable kinds
//	GET  /api/admin/jobs/runs?days=&job= nightly_runs history per job (days 1-90, default 30)
//	POST /api/admin/jobs/{id}/retry     a failed job back to pending now (one more attempt)
//	POST /api/admin/jobs/run            {"kind"} queue a sourceless job now (no-op while one is active)
//
// Reads need ingest:read, the two writes ingest:write (admin_perms.go).
// Crawl jobs are queued per source on /admin/pipeline (POST
// /api/admin/sources/{id}/jobs), not here.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
	"strings"
)

// jobsAdmin backs the jobs page; *store.Store implements it.
type jobsAdmin interface {
	AdminJobQueue(ctx context.Context) ([]store.QueueKind, []store.QueueJob, error)
	AdminJobRuns(ctx context.Context, days int, job string) ([]store.RunSeries, error)
	RetryJob(ctx context.Context, id int64) (store.QueueJob, error)
	EnqueueKindNow(ctx context.Context, kind string) (int64, bool, error)
}

// runKind is a sourceless job kind an admin can run now.
type runKind struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	// Runs is the nightly_runs job name (prefix) its runs are recorded under.
	Runs string `json:"runs"`
}

// runnableKinds are the sourceless kinds cmd/audit claims (cmd/scheduler
// enqueues them on their schedules).
var runnableKinds = []runKind{
	{store.JobKindAudit, "数据质量审计", "audit."},
	{store.JobKindSourceHealth, "片源健康检查", "source_health"},
	{store.JobKindRecsSimilarity, "相似推荐重建", "recs.similarity"},
	{store.JobKindRecsTrending, "热门榜", "recs.trending"},
	{store.JobKindReleaseCurves, "播放曲线", "release_curves"},
	{store.JobKindSearchStats, "搜索统计", "search."},
	{store.JobKindMediaLink, "片源映射", "media_link"},
	{store.JobKindCompletenessSnapshot, "完整性快照", "completeness_snapshot"},
	{store.JobKindCrossCheck, "第三方交叉验证", "crosscheck."},
	{store.JobKindMediaFetch, "图片抓取", "media_fetch"},
	{store.JobKindMediaPurge, "图片清理", "media_purge"},
	{store.JobKindWatchStats, "观看统计（今天）", "watch_stats"},
	{store.JobKindWatchStatsNightly, "观看统计（夜间）", "watch_stats_nightly"},
}

func runnableKind(kind string) bool {
	for _, k := range runnableKinds {
		if k.Kind == kind {
			return true
		}
	}
	return false
}

func (srv *Server) adminJobRoutes() []route {
	return []route{
		{"GET /api/admin/jobs/queue", srv.admin(srv.withJobs(srv.jobQueue))},
		{"GET /api/admin/jobs/runs", srv.admin(srv.withJobs(srv.jobRuns))},
		{"POST /api/admin/jobs/{id}/retry", srv.admin(srv.withJobs(srv.retryJob))},
		{"POST /api/admin/jobs/run", srv.admin(srv.withJobs(srv.runJobNow))},
	}
}

func (srv *Server) withJobs(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.jobsAdm == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

func (srv *Server) jobQueue(w http.ResponseWriter, r *http.Request) {
	kinds, items, err := srv.jobsAdm.AdminJobQueue(r.Context())
	reply(w, map[string]any{"kinds": kinds, "items": items, "runnable": runnableKinds}, err)
}

func (srv *Server) jobRuns(w http.ResponseWriter, r *http.Request) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 1 || d > 90 {
			reply(w, nil, badRequest("days must be 1 to 90", nil))
			return
		}
		days = d
	}
	job := strings.TrimSpace(r.URL.Query().Get("job"))
	if len(job) > 100 {
		reply(w, nil, badRequest("job too long", nil))
		return
	}
	v, err := srv.jobsAdm.AdminJobRuns(r.Context(), days, job)
	reply(w, map[string]any{"days": days, "jobs": v}, err)
}

func (srv *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.jobsAdm.RetryJob(r.Context(), id)
	reply(w, v, err)
}

func (srv *Server) runJobNow(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind string `json:"kind"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if !runnableKind(in.Kind) {
		reply(w, nil, badRequest("kind must be one of the sourceless job kinds (crawl jobs: /admin/pipeline)", nil))
		return
	}
	id, enqueued, err := srv.jobsAdm.EnqueueKindNow(r.Context(), in.Kind)
	reply(w, map[string]any{"job_id": id, "enqueued": enqueued, "kind": in.Kind}, err)
}
