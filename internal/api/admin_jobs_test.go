package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeJobs struct {
	enqueued []string
	retried  []int64
	days     int
	job      string
}

func (f *fakeJobs) AdminJobQueue(context.Context) ([]store.QueueKind, []store.QueueJob, error) {
	return []store.QueueKind{{Kind: "audit", Failed7d: 1}}, []store.QueueJob{{ID: 7, Kind: "audit", Status: "failed"}}, nil
}

func (f *fakeJobs) AdminJobRuns(_ context.Context, days int, job string) ([]store.RunSeries, error) {
	f.days, f.job = days, job
	return []store.RunSeries{}, nil
}

func (f *fakeJobs) RetryJob(_ context.Context, id int64) (store.QueueJob, error) {
	f.retried = append(f.retried, id)
	return store.QueueJob{ID: id, Status: "pending"}, nil
}

func (f *fakeJobs) EnqueueKindNow(_ context.Context, kind string) (int64, bool, error) {
	f.enqueued = append(f.enqueued, kind)
	return 42, true, nil
}

func TestAdminJobRoutes(t *testing.T) {
	jobs := &fakeJobs{}
	srv := &Server{adminAuth: newFakeAdmins(), jobsAdm: jobs}
	mux := http.NewServeMux()
	for _, rt := range srv.adminJobRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(method, path, body, role string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest(method, path, strings.NewReader(body)), "u-"+role, role))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := do("GET", "/api/admin/jobs/queue", "", "content_editor")
	if code != 200 || len(out["runnable"].([]any)) != len(runnableKinds) || len(out["items"].([]any)) != 1 {
		t.Fatalf("queue: %d %v", code, out)
	}
	if code, _ := do("GET", "/api/admin/jobs/runs?days=7&job=audit.R-AGE", "", "ingest_ops"); code != 200 || jobs.days != 7 || jobs.job != "audit.R-AGE" {
		t.Errorf("runs: %d %d %q", code, jobs.days, jobs.job)
	}
	if code, _ := do("GET", "/api/admin/jobs/runs?days=365", "", "ingest_ops"); code != 400 {
		t.Errorf("days out of range: %d", code)
	}
	// Writes: ingest group only.
	if code, _ := do("POST", "/api/admin/jobs/run", `{"kind":"audit"}`, "content_editor"); code != 403 {
		t.Errorf("editor run: %d", code)
	}
	if code, out := do("POST", "/api/admin/jobs/run", `{"kind":"watch_stats"}`, "ingest_ops"); code != 200 || out["job_id"] != float64(42) {
		t.Errorf("run: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/admin/jobs/run", `{"kind":"crawl"}`, "ingest_ops"); code != 400 {
		t.Errorf("crawl kind: %d", code)
	}
	if code, _ := do("POST", "/api/admin/jobs/7/retry", "", "moderator"); code != 403 {
		t.Errorf("moderator retry: %d", code)
	}
	if code, _ := do("POST", "/api/admin/jobs/7/retry", "", "ingest_ops"); code != 200 || len(jobs.retried) != 1 || jobs.retried[0] != 7 {
		t.Errorf("retry: %d %v", code, jobs.retried)
	}
	if strings.Join(jobs.enqueued, ",") != "watch_stats" {
		t.Errorf("enqueued %v", jobs.enqueued)
	}
}
