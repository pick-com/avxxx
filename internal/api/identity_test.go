package api

import (
	"avxxx/internal/identity"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubIdentity is an in-memory identityStore.
type stubIdentity struct {
	err       error
	redirects map[string]string // "type/slug" -> target slug
	lookupErr error
	calls     []string
	gotActor  string
	gotTarget int64
	gotType   string
}

func (s *stubIdentity) GenerateIdentityCandidates(_ context.Context, typ string) (store.GenerateResult, error) {
	s.calls = append(s.calls, "Generate")
	s.gotType = typ
	return store.GenerateResult{"performer": identity.Stats{Inserted: 2}}, s.err
}

func (s *stubIdentity) MergeCandidate(_ context.Context, id, target int64, actor, _ string) (store.MergeResult, error) {
	s.calls = append(s.calls, "Merge")
	s.gotActor, s.gotTarget = actor, target
	return store.MergeResult{ReviewEventID: 9, TargetID: target}, s.err
}

func (s *stubIdentity) MarkNotDuplicate(_ context.Context, id int64, actor, _ string) (store.DecisionResult, error) {
	s.calls = append(s.calls, "NotDuplicate")
	s.gotActor = actor
	return store.DecisionResult{ReviewEventID: 9, CandidateID: id, Status: "not_duplicate"}, s.err
}

func (s *stubIdentity) UndoMerge(_ context.Context, id int64, actor, _ string) (store.UndoResult, error) {
	s.calls = append(s.calls, "Undo")
	s.gotActor = actor
	return store.UndoResult{ReviewEventID: 10, RevertedEventID: id}, s.err
}

func (s *stubIdentity) ResolveMergedSlug(_ context.Context, typ, slug string) (string, bool, error) {
	s.calls = append(s.calls, "Resolve")
	if s.lookupErr != nil {
		return "", false, s.lookupErr
	}
	t, ok := s.redirects[typ+"/"+slug]
	return t, ok, nil
}

func identityServer(st *stubIdentity) http.Handler {
	srv := &Server{identity: st, adminToken: "secret"}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestIdentityHandlers(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   string
		err    error
		want   int
		call   string
		actor  string
		target int64
	}{
		{"generate all", "/api/admin/identity-candidates/generate", "", nil, 200, "Generate", "", 0},
		{"generate performer", "/api/admin/identity-candidates/generate", `{"entity_type":"performer"}`, nil, 200, "Generate", "", 0},
		{"generate bad type", "/api/admin/identity-candidates/generate", `{"entity_type":"studio"}`, nil, 400, "", "", 0},
		{"merge default", "/api/admin/identity-candidates/5/merge", "", nil, 200, "Merge", "admin", 0},
		{"merge target", "/api/admin/identity-candidates/5/merge", `{"target_id":3,"actor":"kim","reason":"same code"}`, nil, 200, "Merge", "kim", 3},
		{"merge bad id", "/api/admin/identity-candidates/x/merge", "", nil, 400, "", "", 0},
		{"merge bad json", "/api/admin/identity-candidates/5/merge", `{`, nil, 400, "", "", 0},
		{"merge missing", "/api/admin/identity-candidates/5/merge", "", pgx.ErrNoRows, 404, "Merge", "admin", 0},
		{"merge decided", "/api/admin/identity-candidates/5/merge", "", &store.ConflictError{Msg: "candidate 5 is already merged"}, 409, "Merge", "admin", 0},
		{"merge wrong target", "/api/admin/identity-candidates/5/merge", `{"target_id":99}`, store.ErrInvalidMergeTarget, 400, "Merge", "admin", 99},
		{"merge db failure", "/api/admin/identity-candidates/5/merge", "", errors.New("boom"), 500, "Merge", "admin", 0},
		{"not duplicate", "/api/admin/identity-candidates/5/not-duplicate", `{"actor":" ana "}`, nil, 200, "NotDuplicate", "ana", 0},
		{"not duplicate decided", "/api/admin/identity-candidates/5/not-duplicate", "", &store.ConflictError{Msg: "x"}, 409, "NotDuplicate", "admin", 0},
		{"undo", "/api/admin/merges/9/undo", "", nil, 200, "Undo", "admin", 0},
		{"undo twice", "/api/admin/merges/9/undo", "", &store.ConflictError{Msg: "merge 9 was already undone"}, 409, "Undo", "admin", 0},
		{"undo unknown", "/api/admin/merges/9/undo", "", pgx.ErrNoRows, 404, "Undo", "admin", 0},
		{"undo bad id", "/api/admin/merges/0/undo", "", nil, 400, "", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubIdentity{err: tt.err}
			req := httptest.NewRequest("POST", tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			identityServer(st).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}
			if tt.call == "" && len(st.calls) != 0 || tt.call != "" && (len(st.calls) != 1 || st.calls[0] != tt.call) {
				t.Errorf("calls %v, want %q", st.calls, tt.call)
			}
			if st.gotActor != tt.actor || st.gotTarget != tt.target {
				t.Errorf("actor %q target %d, want %q %d", st.gotActor, st.gotTarget, tt.actor, tt.target)
			}
			if tt.want == 409 {
				var body map[string]string
				json.NewDecoder(rec.Body).Decode(&body)
				if body["error"] != tt.err.Error() {
					t.Errorf("409 body %v", body)
				}
			}
		})
	}
}

func TestIdentityHandlersRequireToken(t *testing.T) {
	st := &stubIdentity{}
	rec := httptest.NewRecorder()
	identityServer(st).ServeHTTP(rec, httptest.NewRequest("POST", "/api/admin/merges/1/undo", nil))
	if rec.Code != 401 || len(st.calls) != 0 {
		t.Fatalf("status %d calls %v", rec.Code, st.calls)
	}
}

func TestMergeRedirects(t *testing.T) {
	st := &stubIdentity{redirects: map[string]string{"performer/old-name": "new-name", "release/juq-743-dup": "juq-743"}}
	h := identityServer(st)
	tests := []struct{ path, loc string }{
		{"/api/performers/old-name", "/api/performers/new-name"},
		{"/api/performers/old-name/releases?limit=5", "/api/performers/new-name/releases?limit=5"},
		{"/api/releases/juq-743-dup", "/api/releases/juq-743"},
		{"/api/releases/juq-743-dup/performers", "/api/releases/juq-743/performers"},
	}
	for _, tt := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != tt.loc {
			t.Errorf("%s: %d Location %q, want 301 %q", tt.path, rec.Code, rec.Header().Get("Location"), tt.loc)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: redirect is cacheable", tt.path)
		}
		var body map[string]string
		if json.NewDecoder(rec.Body).Decode(&body) != nil || body["redirect"] != tt.loc {
			t.Errorf("%s: body %v", tt.path, body)
		}
	}
}

func TestFollowMergesWrapsOnlySlugRoutes(t *testing.T) {
	st := &stubIdentity{redirects: map[string]string{"performer/old-name": "new-name"}}
	srv := &Server{identity: st}
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }
	rs := srv.followMerges([]route{
		{"GET /api/performers", ok},
		{"GET /api/performers/{slug}", ok},
		{"GET /api/releases/{slug}", ok},
		{"GET /api/releases/{slug}/sources", ok},
		{"GET /api/tags", ok},
	})
	mux := http.NewServeMux()
	for _, rt := range rs {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	tests := []struct {
		path    string
		code    int
		lookups int
	}{
		{"/api/performers", http.StatusTeapot, 0},
		{"/api/tags", http.StatusTeapot, 0},
		{"/api/performers/live-name", http.StatusTeapot, 1},
		{"/api/performers/old-name", http.StatusMovedPermanently, 1},
		{"/api/releases/old-name", http.StatusTeapot, 1}, // performer slug is not a release redirect
		{"/api/releases/old-name/sources", http.StatusTeapot, 1},
	}
	for _, tt := range tests {
		st.calls = nil
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", tt.path, nil))
		if rec.Code != tt.code || len(st.calls) != tt.lookups {
			t.Errorf("%s: status %d lookups %d, want %d %d", tt.path, rec.Code, len(st.calls), tt.code, tt.lookups)
		}
	}
}

func TestMergeRedirectFailsOpen(t *testing.T) {
	st := &stubIdentity{lookupErr: errors.New("db down")}
	srv := &Server{identity: st}
	called := false
	h := srv.mergeRedirect("performer", func(w http.ResponseWriter, r *http.Request) { called = true })
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/performers/{slug}", h)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/performers/a", nil))
	if !called || rec.Code == http.StatusMovedPermanently {
		t.Fatalf("lookup error should fall through to the handler (called=%v, code=%d)", called, rec.Code)
	}
}

func TestConflictErrorStatus(t *testing.T) {
	code, msg := errorStatus(&store.ConflictError{Msg: "merge 3 was already undone"})
	if code != 409 || msg != "merge 3 was already undone" {
		t.Errorf("ConflictError -> %d %q", code, msg)
	}
	code, _ = errorStatus(store.ErrInvalidMergeTarget)
	if code != 400 {
		t.Errorf("ErrInvalidMergeTarget -> %d", code)
	}
}
