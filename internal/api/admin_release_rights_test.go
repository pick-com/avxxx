package api

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeRights struct {
	added   []store.RightsProofInput
	removed []string
}

func (f *fakeRights) ReleaseRightsProofs(context.Context, int64, bool) ([]store.RightsProof, error) {
	return []store.RightsProof{}, nil
}

func (f *fakeRights) AddReleaseRightsProof(_ context.Context, id int64, in store.RightsProofInput, actor string) (store.RightsProof, error) {
	f.added = append(f.added, in)
	return store.RightsProof{ID: 1, ReleaseID: id, CreatedBy: actor}, nil
}

func (f *fakeRights) RemoveReleaseRightsProof(_ context.Context, _, _ int64, reason, actor string) (store.RightsProof, error) {
	f.removed = append(f.removed, actor+":"+reason)
	return store.RightsProof{}, nil
}

func TestReleaseRightsRoutes(t *testing.T) {
	f := newFakeAdmins()
	rights := &fakeRights{}
	srv := &Server{adminAuth: f, rights: rights}
	mux := http.NewServeMux()
	for _, rt := range srv.releaseRightsRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(method, path, body, role string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest(method, path, strings.NewReader(body)), "u-"+role, role))
		return rec
	}
	if rec := do("GET", "/api/admin/releases/5/rights-proofs", "", "moderator"); rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	// Reads of proofs are in the op log.
	if a := f.audits(); len(a) != 1 || a[0].Route != "GET /api/admin/releases/{id}/rights-proofs" {
		t.Errorf("read not audited: %+v", a)
	}
	if rec := do("GET", "/api/admin/releases/5/rights-proofs", "", "translator"); rec.Code != 403 {
		t.Errorf("translator list: %d", rec.Code)
	}
	if rec := do("POST", "/api/admin/releases/5/rights-proofs", `{"reference":"C-1","url":"javascript:alert(1)"}`, "content_editor"); rec.Code != 400 {
		t.Errorf("javascript url: %d", rec.Code)
	}
	if rec := do("POST", "/api/admin/releases/5/rights-proofs", `{"kind":"contract","reference":"C-1","url":"https://docs.example/c1","note":"签约"}`, "content_editor"); rec.Code != 201 {
		t.Errorf("add: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/api/admin/releases/5/rights-proofs", `{"reference":"C-2"}`, "moderator"); rec.Code != 403 {
		t.Errorf("moderator add: %d", rec.Code)
	}
	if rec := do("DELETE", "/api/admin/releases/5/rights-proofs/1?reason=expired", "", "content_editor"); rec.Code != 200 {
		t.Errorf("remove: %d", rec.Code)
	}
	if len(rights.added) != 1 || rights.added[0].Kind != "contract" || len(rights.removed) != 1 || rights.removed[0] != "u-content_editor:expired" {
		t.Errorf("calls %+v %v", rights.added, rights.removed)
	}
	for v, want := range map[string]bool{"": true, "https://a.example/x": true, "http://a.example": true, "ftp://a.example": false, "/relative": false, "https://": false} {
		if validProofURL(v) != want {
			t.Errorf("validProofURL(%q) != %v", v, want)
		}
	}
}
