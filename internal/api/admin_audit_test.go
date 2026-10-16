package api

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type stubAudit struct {
	issuesErr  error
	dismissErr error
	enqueueErr error
	enqueued   bool
	dashErr    error
	gotFilter  audit.IssueFilter
	healthErr  error
	gotHealth  audit.SourceHealthFilter
	gotDismiss struct {
		id   int64
		note string
	}
}

func (s *stubAudit) AuditIssues(_ context.Context, f audit.IssueFilter) ([]audit.Issue, string, error) {
	s.gotFilter = f
	if s.issuesErr != nil {
		return nil, "", s.issuesErr
	}
	return []audit.Issue{{ID: 3, Rule: audit.RuleAge, EntityType: "release", EntityID: 2, Severity: audit.SeverityBlocking, Status: audit.StatusOpen}}, "next", nil
}

func (s *stubAudit) DismissAuditIssue(_ context.Context, id int64, note string) error {
	s.gotDismiss.id, s.gotDismiss.note = id, note
	return s.dismissErr
}

func (s *stubAudit) EnqueueAudit(context.Context) (int64, bool, error) {
	return 42, s.enqueued, s.enqueueErr
}

func (s *stubAudit) AdminDashboard(context.Context) (audit.Dashboard, error) {
	d := audit.NewDashboard(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC))
	d.AddIssueCount("R-AGE", "blocking", 2)
	d.SetHealth(100, 2)
	return d, s.dashErr
}

func (s *stubAudit) SourceHealthList(_ context.Context, f audit.SourceHealthFilter) ([]audit.SourceHealthItem, string, error) {
	s.gotHealth = f
	if s.healthErr != nil {
		return nil, "", s.healthErr
	}
	score := 72
	return []audit.SourceHealthItem{{SourceID: 5, ReleaseID: 2, Status: audit.SourceDegraded, Score: &score, Band: audit.BandDegraded,
		Trend: []audit.SourceHealthPoint{}}}, "", nil
}

func auditMux(st *stubAudit) *http.ServeMux {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins(), audit: st}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestAdminAuditStatusCodes(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		st                       stubAudit
		want                     int
		wantBody                 string // substring
	}{
		{"list default", "GET", "/api/admin/audit/issues", "", stubAudit{}, 200, `"next_cursor":"next"`},
		{"list by rule and status", "GET", "/api/admin/audit/issues?rule=R-META&status=dismissed&limit=5", "", stubAudit{}, 200, `"items"`},
		{"list unknown rule", "GET", "/api/admin/audit/issues?rule=R-NOPE", "", stubAudit{}, 400, "unknown rule"},
		{"list bad status", "GET", "/api/admin/audit/issues?status=closed", "", stubAudit{}, 400, "status must be"},
		{"list bad limit", "GET", "/api/admin/audit/issues?limit=-1", "", stubAudit{}, 400, "invalid limit"},
		{"list bad cursor", "GET", "/api/admin/audit/issues?cursor=zz", "", stubAudit{issuesErr: fmt.Errorf("%w: malformed cursor", store.ErrInvalidQuery)}, 400, "malformed cursor"},
		{"list store failure", "GET", "/api/admin/audit/issues", "", stubAudit{issuesErr: errors.New("boom")}, 500, "request failed"},
		{"dismiss ok with note", "POST", "/api/admin/audit/issues/7/dismiss", `{"note":" checked ID "}`, stubAudit{}, 200, `"ok":true`},
		{"dismiss ok without body", "POST", "/api/admin/audit/issues/7/dismiss", "", stubAudit{}, 200, `"ok":true`},
		{"dismiss bad id", "POST", "/api/admin/audit/issues/x/dismiss", "", stubAudit{}, 400, "invalid id"},
		{"dismiss bad json", "POST", "/api/admin/audit/issues/7/dismiss", `{`, stubAudit{}, 400, "invalid JSON body"},
		{"dismiss missing", "POST", "/api/admin/audit/issues/7/dismiss", "", stubAudit{dismissErr: pgx.ErrNoRows}, 404, "not found"},
		{"dismiss not open", "POST", "/api/admin/audit/issues/7/dismiss", "", stubAudit{dismissErr: audit.ErrIssueNotOpen}, 409, "not open"},
		{"run enqueued", "POST", "/api/admin/audit/run", "", stubAudit{enqueued: true}, 202, `"enqueued":true`},
		{"run already queued", "POST", "/api/admin/audit/run", "", stubAudit{}, 202, `"enqueued":false`},
		{"run failure", "POST", "/api/admin/audit/run", "", stubAudit{enqueueErr: errors.New("boom")}, 500, "request failed"},
		{"dashboard", "GET", "/api/admin/dashboard", "", stubAudit{}, 200, `"band":"excellent"`},
		{"dashboard failure", "GET", "/api/admin/dashboard", "", stubAudit{dashErr: errors.New("boom")}, 500, "request failed"},
		{"source health", "GET", "/api/admin/playback-sources/health", "", stubAudit{}, 200, `"band":"degraded"`},
		{"source health band", "GET", "/api/admin/playback-sources/health?band=insufficient_data&trend=6", "", stubAudit{}, 200, `"next_cursor":null`},
		{"source health bad band", "GET", "/api/admin/playback-sources/health?band=ok", "", stubAudit{}, 400, "band must be"},
		{"source health bad trend", "GET", "/api/admin/playback-sources/health?trend=500", "", stubAudit{}, 400, "invalid trend"},
		{"source health bad cursor", "GET", "/api/admin/playback-sources/health?cursor=zz", "", stubAudit{healthErr: fmt.Errorf("%w: malformed cursor", store.ErrInvalidQuery)}, 400, "malformed cursor"},
		{"source health failure", "GET", "/api/admin/playback-sources/health", "", stubAudit{healthErr: errors.New("boom")}, 500, "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := tt.st
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			auditMux(&st).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %s, want it to contain %s", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestAdminAuditPassesFilterAndNote(t *testing.T) {
	st := &stubAudit{}
	mux := auditMux(st)
	req := httptest.NewRequest("GET", "/api/admin/audit/issues?rule=R-AGE&status=fixed&limit=5&cursor=abc", nil)
	req.Header.Set("Authorization", "Bearer secret")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if want := (audit.IssueFilter{Rule: audit.RuleAge, Status: "fixed", Cursor: "abc", Limit: 5}); st.gotFilter != want {
		t.Errorf("filter %+v, want %+v", st.gotFilter, want)
	}
	req = httptest.NewRequest("POST", "/api/admin/audit/issues/9/dismiss", strings.NewReader(`{"note":"  adult, ID on file "}`))
	req.Header.Set("Authorization", "Bearer secret")
	mux.ServeHTTP(httptest.NewRecorder(), req)
	if st.gotDismiss.id != 9 || st.gotDismiss.note != "adult, ID on file" {
		t.Errorf("dismiss got %+v", st.gotDismiss)
	}
}

func TestDashboardShape(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/dashboard", nil)
	req.Header.Set("Authorization", "Bearer secret")
	auditMux(&stubAudit{}).ServeHTTP(rec, req)
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"generated_at", "releases", "performers", "issues", "jobs", "nightly_runs", "identity_candidates_open", "conflicts_open", "catalog_health", "source_health"} {
		if _, ok := got[k]; !ok {
			t.Errorf("dashboard missing %q", k)
		}
	}
	health := got["catalog_health"].(map[string]any)
	if health["score"] != float64(98) || health["band"] != "excellent" {
		t.Errorf("catalog_health %v", health)
	}
}

func TestSourceHealthPassesFilter(t *testing.T) {
	st := &stubAudit{}
	req := httptest.NewRequest("GET", "/api/admin/playback-sources/health?band=severe&trend=24&limit=10&cursor=abc", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	auditMux(st).ServeHTTP(rec, req)
	if want := (audit.SourceHealthFilter{Band: "severe", Trend: 24, Limit: 10, Cursor: "abc"}); st.gotHealth != want {
		t.Errorf("filter %+v, want %+v", st.gotHealth, want)
	}
	req = httptest.NewRequest("GET", "/api/admin/playback-sources/health", nil)
	rec = httptest.NewRecorder()
	auditMux(st).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without token: %d", rec.Code)
	}
}
