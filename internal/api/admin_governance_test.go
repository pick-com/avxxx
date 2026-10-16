package api

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type fakeGovernance struct {
	enqueued   []store.EnqueueRequest
	enqueueErr error
	license    store.LicenseReview
	actor      string
	policy     store.FieldPolicy
	deleted    string
}

func (f *fakeGovernance) Enqueue(_ context.Context, _ int64, req store.EnqueueRequest) (int64, error) {
	f.enqueued = append(f.enqueued, req)
	return 42, f.enqueueErr
}
func (f *fakeGovernance) SetSourceLicense(_ context.Context, id int64, in store.LicenseReview, actor string) (model.Source, error) {
	f.license, f.actor = in, actor
	return model.Source{ID: id, LicenseStatus: in.LicenseStatus}, nil
}
func (f *fakeGovernance) SourceCheckpoints(context.Context, int64) ([]store.CrawlCheckpoint, error) {
	return []store.CrawlCheckpoint{{PartitionKey: "incremental"}}, nil
}
func (f *fakeGovernance) FieldPolicies(context.Context) ([]store.FieldPolicy, error) {
	return []store.FieldPolicy{{EntityType: "release", FieldName: "canonical_title", SourceID: 1, Priority: 5}}, nil
}
func (f *fakeGovernance) SetFieldPolicy(_ context.Context, p store.FieldPolicy, _, actor string) (store.FieldPolicy, error) {
	f.policy, f.actor = p, actor
	return p, nil
}
func (f *fakeGovernance) DeleteFieldPolicy(_ context.Context, et, field string, src int64, _, _ string) error {
	f.deleted = et + "/" + field
	if src == 404 {
		return pgx.ErrNoRows
	}
	return nil
}
func (f *fakeGovernance) EffectiveFieldOrder(context.Context, string, string) ([]store.EffectiveSource, error) {
	return []store.EffectiveSource{{SourceID: 1, Priority: 5, FromPolicy: true}}, nil
}

func doGovernance(t *testing.T, g governanceStore, method, target, body string, auth bool) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	srv := &Server{adminToken: "tok", governance: g}
	mux := http.NewServeMux()
	for _, rt := range srv.adminGovernanceRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if auth {
		req.Header.Set("Authorization", "Bearer tok")
	}
	req.Header.Set("X-Admin-User", "alice")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestEnqueueJobPhases(t *testing.T) {
	f := &fakeGovernance{}
	if rec, _ := doGovernance(t, f, "POST", "/api/admin/sources/3/jobs", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if rec, body := doGovernance(t, f, "POST", "/api/admin/sources/3/jobs", "", true); rec.Code != 200 || body["phase"] != "incremental" {
		t.Fatalf("empty body: %d %v", rec.Code, body)
	}
	if rec, _ := doGovernance(t, f, "POST", "/api/admin/sources/3/jobs", `{"phase":"verify","max_pages":2}`, true); rec.Code != 200 {
		t.Fatalf("verify: %d", rec.Code)
	}
	last := f.enqueued[len(f.enqueued)-1]
	if last.Phase != catalog.PhaseVerify || last.MaxPages != 2 || last.Actor != "alice" {
		t.Fatalf("request %+v", last)
	}
	for _, bad := range []string{`{"phase":"full"}`, `{"phase":"baseline","max_pages":3}`, `{"bogus":1}`} {
		if rec, _ := doGovernance(t, f, "POST", "/api/admin/sources/3/jobs", bad, true); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	f.enqueueErr = &store.GateRefusal{Code: catalog.CodeLicenseGate, Reason: "license_status \"review_required\" does not permit automatic crawls"}
	rec, body := doGovernance(t, f, "POST", "/api/admin/sources/3/jobs", `{"phase":"baseline"}`, true)
	if rec.Code != http.StatusConflict || !strings.Contains(body["error"].(string), "license_gate") {
		t.Fatalf("gate refusal: %d %v", rec.Code, body)
	}
}

func TestSetSourceLicenseRoute(t *testing.T) {
	f := &fakeGovernance{}
	rec, body := doGovernance(t, f, "PUT", "/api/admin/sources/3/license", `{"license_status":"terms_confirmed","terms_url":"https://x.test/terms","reviewed":true,"note":"ok"}`, true)
	if rec.Code != 200 || body["license_status"] != "terms_confirmed" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	if !f.license.Reviewed || f.license.TermsURL == nil || *f.license.TermsURL != "https://x.test/terms" || f.actor != "alice" {
		t.Fatalf("review %+v actor %s", f.license, f.actor)
	}
}

func TestFieldPolicyRoutes(t *testing.T) {
	f := &fakeGovernance{}
	rec, body := doGovernance(t, f, "GET", "/api/admin/field-policies?entity_type=release&field_name=title", "", true)
	if rec.Code != 200 || body["effective"] == nil || body["tier_defaults"] == nil {
		t.Fatalf("list: %d %v", rec.Code, body)
	}
	if rec, _ = doGovernance(t, f, "PUT", "/api/admin/field-policies", `{"entity_type":"release","field_name":"title","source_id":2,"priority":0}`, true); rec.Code != 200 {
		t.Fatalf("set: %d", rec.Code)
	}
	if f.policy.SourceID != 2 || f.policy.Priority != 0 || f.actor != "alice" {
		t.Fatalf("policy %+v", f.policy)
	}
	if rec, _ = doGovernance(t, f, "PUT", "/api/admin/field-policies", `{"entity_type":"release","field_name":"title","source_id":2}`, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing priority: %d", rec.Code)
	}
	if rec, _ = doGovernance(t, f, "DELETE", "/api/admin/field-policies/release/title/2", "", true); rec.Code != 200 || f.deleted != "release/title" {
		t.Fatalf("delete: %d %s", rec.Code, f.deleted)
	}
	if rec, _ = doGovernance(t, f, "DELETE", "/api/admin/field-policies/release/title/404", "", true); rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing: %d", rec.Code)
	}
	if rec, _ = doGovernance(t, f, "GET", "/api/admin/sources/1/checkpoints", "", true); rec.Code != 200 {
		t.Fatalf("checkpoints: %d", rec.Code)
	}
}
