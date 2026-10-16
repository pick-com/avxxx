package api

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeStubs struct{ got []store.ReleaseStubInput }

func (f *fakeStubs) CreateReleaseFromTerm(_ context.Context, in store.ReleaseStubInput, _ string) (store.ReleaseStub, error) {
	f.got = append(f.got, in)
	return store.ReleaseStub{Slug: "x"}, nil
}

func TestReleaseFromTermRoute(t *testing.T) {
	stubs := &fakeStubs{}
	srv := &Server{adminAuth: newFakeAdmins(), stubs: stubs}
	mux := http.NewServeMux()
	for _, rt := range srv.releaseStubRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(body, role string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest("POST", "/api/admin/search/terms/release", strings.NewReader(body)), "u-"+role, role))
		return rec.Code
	}
	if c := do(`{"term":"ABC-123"}`, "content_editor"); c != 201 {
		t.Fatalf("draft: %d", c)
	}
	if c := do(`{"term":"新作","title":"新作 2027","code":"XYZ-001","scheduled_at":"2030-01-02T03:04:05Z"}`, "content_editor"); c != 201 {
		t.Fatalf("upcoming: %d", c)
	}
	if c := do(`{"term":"x","scheduled_at":"tomorrow"}`, "content_editor"); c != 400 {
		t.Errorf("bad date: %d", c)
	}
	if c := do(`{"term":""}`, "content_editor"); c != 400 {
		t.Errorf("empty term: %d", c)
	}
	if c := do(`{"term":"x"}`, "operations"); c != 403 {
		t.Errorf("operations (search write, catalog read): %d", c)
	}
	if len(stubs.got) != 2 || stubs.got[0].Title != "ABC-123" || stubs.got[0].ScheduledAt != nil || stubs.got[1].Code != "XYZ-001" || stubs.got[1].ScheduledAt == nil {
		t.Errorf("calls %+v", stubs.got)
	}
}
