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

type fakeDedupe struct {
	linked   []int64
	unlinked [][2]int64
	actor    string
	reason   string
}

func (f *fakeDedupe) CandidateCompare(_ context.Context, id int64) (store.CandidateCompare, error) {
	return store.CandidateCompare{CandidateID: id, EntityType: "release",
		Left:  store.EntitySummary{ID: 1, Title: "A", Codes: []string{"ABC-001"}, Sources: []store.SummarySource{}},
		Right: store.EntitySummary{ID: 2, Title: "A 4K", Codes: []string{"ABC-001"}, Sources: []store.SummarySource{}}}, nil
}

func (f *fakeDedupe) LinkCandidateVersions(_ context.Context, id int64, actor, reason string) (store.DecisionResult, error) {
	f.linked, f.actor, f.reason = append(f.linked, id), actor, reason
	return store.DecisionResult{ReviewEventID: 5, CandidateID: id, Status: "versions"}, nil
}

func (f *fakeDedupe) UnlinkReleaseVersion(_ context.Context, id, other int64, actor, _ string) (int64, error) {
	f.unlinked, f.actor = append(f.unlinked, [2]int64{id, other}), actor
	return 6, nil
}

func TestAdminDedupeRoutes(t *testing.T) {
	fake := &fakeDedupe{}
	srv := &Server{adminAuth: newFakeAdmins(), dedupe: fake}
	mux := http.NewServeMux()
	for _, rt := range srv.adminDedupeRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(method, path, body, role string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest(method, path, strings.NewReader(body)), "u-"+role, role))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := do("GET", "/api/admin/identity-candidates/3/compare", "", "ingest_ops")
	if code != 200 || out["left"].(map[string]any)["title"] != "A" || out["right"].(map[string]any)["codes"] == nil {
		t.Fatalf("compare: %d %v", code, out)
	}
	if code, _ := do("POST", "/api/admin/identity-candidates/3/versions", `{"reason":"4K 版"}`, "ingest_ops"); code != 403 {
		t.Errorf("ingest_ops (catalog read only) link: %d", code)
	}
	code, out = do("POST", "/api/admin/identity-candidates/3/versions", `{"reason":"4K 版"}`, "content_editor")
	if code != 200 || out["status"] != "versions" || len(fake.linked) != 1 || fake.reason != "4K 版" || fake.actor == "" {
		t.Errorf("link: %d %v %+v", code, out, fake)
	}
	if code, _ := do("POST", "/api/admin/identity-candidates/x/versions", ``, "content_editor"); code != 400 {
		t.Errorf("bad id: %d", code)
	}
	code, out = do("DELETE", "/api/admin/releases/4/versions/9", ``, "content_editor")
	if code != 200 || out["review_event_id"] != 6.0 || len(fake.unlinked) != 1 || fake.unlinked[0] != [2]int64{4, 9} {
		t.Errorf("unlink: %d %v", code, out)
	}
	if code, _ := do("DELETE", "/api/admin/releases/4/versions/0", ``, "content_editor"); code != 400 {
		t.Errorf("bad other id: %d", code)
	}
}
