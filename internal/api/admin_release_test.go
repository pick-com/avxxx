package api

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubEditor records calls and returns err for every store call.
type stubEditor struct {
	err   error
	calls []string
	actor string
	patch store.ReleasePatch
	trans store.Transition
	src   store.SourceInput
	perf  store.ReleasePerformerInput
	ids   []int64
	value json.RawMessage
}

func (s *stubEditor) hit(name, actor string) { s.calls = append(s.calls, name); s.actor = actor }

func (s *stubEditor) AdminReleases(_ context.Context, f store.AdminReleaseFilter) ([]store.AdminReleaseSummary, string, error) {
	s.hit("list", "")
	return []store.AdminReleaseSummary{{ID: 1, Status: f.Status}}, "", s.err
}
func (s *stubEditor) AdminRelease(context.Context, int64) (store.AdminRelease, error) {
	s.hit("detail", "")
	return store.AdminRelease{ID: 1}, s.err
}
func (s *stubEditor) PatchRelease(_ context.Context, _ int64, p store.ReleasePatch, actor string) (store.PatchResult, error) {
	s.hit("patch", actor)
	s.patch = p
	return store.PatchResult{ReleaseID: 1}, s.err
}
func (s *stubEditor) TransitionRelease(_ context.Context, _ int64, t store.Transition, actor string) (store.TransitionResult, error) {
	s.hit("status", actor)
	s.trans = t
	return store.TransitionResult{}, s.err
}
func (s *stubEditor) ClearReleaseOverride(_ context.Context, _ int64, _, _, actor string) error {
	s.hit("clear", actor)
	return s.err
}
func (s *stubEditor) AdminPlaybackSources(context.Context, int64) ([]store.AdminPlaybackSource, error) {
	s.hit("sources", "")
	return []store.AdminPlaybackSource{}, s.err
}
func (s *stubEditor) CreatePlaybackSource(_ context.Context, _ int64, in store.SourceInput, actor string) (store.AdminPlaybackSource, error) {
	s.hit("create", actor)
	s.src = in
	return store.AdminPlaybackSource{ID: 9}, s.err
}
func (s *stubEditor) UpdatePlaybackSource(_ context.Context, _, _ int64, _ json.RawMessage, actor string) (store.AdminPlaybackSource, error) {
	s.hit("update", actor)
	return store.AdminPlaybackSource{}, s.err
}
func (s *stubEditor) ReorderPlaybackSources(_ context.Context, _ int64, ids []int64, actor string) ([]store.AdminPlaybackSource, error) {
	s.hit("reorder", actor)
	s.ids = ids
	return nil, s.err
}
func (s *stubEditor) SetPlaybackSourceEnabled(_ context.Context, _, _ int64, enable bool, _, actor string) (store.AdminPlaybackSource, error) {
	if enable {
		s.hit("enable", actor)
	} else {
		s.hit("disable", actor)
	}
	return store.AdminPlaybackSource{}, s.err
}
func (s *stubEditor) AdminPromoteMediaSource(_ context.Context, _ store.PromoteMediaSourceInput, actor string) (store.AdminPlaybackSource, error) {
	s.hit("promote", actor)
	return store.AdminPlaybackSource{}, s.err
}
func (s *stubEditor) SetReleasePerformer(_ context.Context, _ int64, in store.ReleasePerformerInput, actor string) (store.PatchResult, error) {
	s.hit("set_performer", actor)
	s.perf = in
	return store.PatchResult{}, s.err
}
func (s *stubEditor) RemoveReleasePerformer(_ context.Context, _, _ int64, _, actor string) (store.PatchResult, error) {
	s.hit("remove_performer", actor)
	return store.PatchResult{}, s.err
}
func (s *stubEditor) ResolveConflict(_ context.Context, _, _ int64, value json.RawMessage, _, actor string) (store.ConflictResolution, error) {
	s.hit("resolve", actor)
	s.value = value
	return store.ConflictResolution{}, s.err
}

func editorMux(st *stubEditor) *http.ServeMux {
	srv := &Server{adminToken: "secret", editor: st}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestAdminReleaseStatusCodes(t *testing.T) {
	ageErr := &store.AgeGateError{Findings: []audit.AgeFinding{{Name: "X", Age: 17, Reason: audit.ReasonUnder18}}}
	tests := []struct {
		name, method, path, body string
		err                      error
		want                     int
		call                     string // expected store call ("" = none)
	}{
		{"list", "GET", "/api/admin/releases?q=abc&status=held_review", "", nil, 200, "list"},
		{"list bad status", "GET", "/api/admin/releases?status=gone", "", nil, 400, ""},
		{"list bad cursor", "GET", "/api/admin/releases?cursor=x", "", store.ErrInvalidQuery, 400, "list"},
		{"detail", "GET", "/api/admin/releases/1", "", nil, 200, "detail"},
		{"detail bad id", "GET", "/api/admin/releases/abc", "", nil, 400, ""},
		{"detail missing", "GET", "/api/admin/releases/7", "", pgx.ErrNoRows, 404, "detail"},
		{"patch", "PATCH", "/api/admin/releases/1", `{"title":"T","shoot_year":2020,"version":2}`, nil, 200, "patch"},
		{"patch unknown field", "PATCH", "/api/admin/releases/1", `{"status":"published"}`, nil, 400, ""},
		{"patch empty", "PATCH", "/api/admin/releases/1", `{}`, nil, 400, ""},
		{"patch not json", "PATCH", "/api/admin/releases/1", `nope`, nil, 400, ""},
		{"patch stale version", "PATCH", "/api/admin/releases/1", `{"title":"T","version":1}`, &store.ConflictError{Msg: "stale"}, 409, "patch"},
		{"status publish", "POST", "/api/admin/releases/1/status", `{"status":"published"}`, nil, 200, "status"},
		{"status unknown", "POST", "/api/admin/releases/1/status", `{"status":"gone"}`, nil, 400, ""},
		{"status extra field", "POST", "/api/admin/releases/1/status", `{"status":"published","force":true}`, nil, 400, ""},
		{"status R-AGE fails", "POST", "/api/admin/releases/1/status", `{"status":"published","override_reason":"x"}`, ageErr, 409, "status"},
		{"status rule violation", "POST", "/api/admin/releases/1/status", `{"status":"published"}`, &store.ValidationError{Msg: "override_reason is required"}, 400, "status"},
		{"clear override", "DELETE", "/api/admin/releases/1/overrides/canonical_title", ``, nil, 200, "clear"},
		{"clear missing override", "DELETE", "/api/admin/releases/1/overrides/x", `{"reason":"r"}`, pgx.ErrNoRows, 404, "clear"},
		{"sources", "GET", "/api/admin/releases/1/sources", "", nil, 200, "sources"},
		{"create source", "POST", "/api/admin/releases/1/sources", `{"kind":"official_embed","provider":"p","label":"Main","url":"https://p.test/e","license_ref":"L1"}`, nil, 201, "create"},
		{"create source no license", "POST", "/api/admin/releases/1/sources", `{"kind":"official_embed","provider":"p","label":"Main","url":"https://p.test/e"}`, nil, 400, ""},
		{"create preview no license", "POST", "/api/admin/releases/1/sources", `{"kind":"official_preview","provider":"p","label":"PV","url":"https://p.test/pv"}`, nil, 201, "create"},
		{"create source unknown field", "POST", "/api/admin/releases/1/sources", `{"kind":"external_link","provider":"p","label":"L","url":"https://p.test","status":"active"}`, nil, 400, ""},
		{"update source", "PATCH", "/api/admin/releases/1/sources/2", `{"label":"4K"}`, nil, 200, "update"},
		{"update source not object", "PATCH", "/api/admin/releases/1/sources/2", `[1]`, nil, 400, ""},
		{"update source bad id", "PATCH", "/api/admin/releases/1/sources/x", `{"label":"4K"}`, nil, 400, ""},
		{"update source invalid", "PATCH", "/api/admin/releases/1/sources/2", `{"status":"active"}`, &store.ValidationError{Msg: "bad"}, 400, "update"},
		{"reorder", "POST", "/api/admin/releases/1/sources/reorder", `{"ids":[3,2]}`, nil, 200, "reorder"},
		{"reorder empty", "POST", "/api/admin/releases/1/sources/reorder", `{"ids":[]}`, nil, 400, ""},
		{"promote", "POST", "/api/admin/releases/1/sources/promote", `{"source":"tube","kind":"external_link","label":"Tube"}`, nil, 200, "promote"},
		{"promote missing", "POST", "/api/admin/releases/1/sources/promote", `{"kind":"external_link"}`, nil, 400, ""},
		{"promote unknown media", "POST", "/api/admin/releases/1/sources/promote", `{"source":"x","kind":"external_link","label":"L"}`, pgx.ErrNoRows, 404, "promote"},
		{"disable", "POST", "/api/admin/releases/1/sources/2/disable", ``, nil, 200, "disable"},
		{"enable needs reason", "POST", "/api/admin/releases/1/sources/2/enable", ``, &store.ValidationError{Msg: "reason is required"}, 400, "enable"},
		{"enable already active", "POST", "/api/admin/releases/1/sources/2/enable", `{"reason":"ok"}`, &store.ConflictError{Msg: "already active"}, 409, "enable"},
		{"set performer", "PUT", "/api/admin/releases/1/performers/5", `{"role":"lead"}`, nil, 200, "set_performer"},
		{"set performer bad role", "PUT", "/api/admin/releases/1/performers/5", `{"role":"star"}`, nil, 400, ""},
		{"set performer bad id", "PUT", "/api/admin/releases/1/performers/0", `{"role":"lead"}`, nil, 400, ""},
		{"remove performer", "DELETE", "/api/admin/releases/1/performers/5", ``, nil, 200, "remove_performer"},
		{"remove performer not credited", "DELETE", "/api/admin/releases/1/performers/5", ``, pgx.ErrNoRows, 404, "remove_performer"},
		{"resolve conflict", "POST", "/api/admin/conflicts/3/resolve", `{"value":"Title","note":"n"}`, nil, 200, "resolve"},
		{"resolve adopt source", "POST", "/api/admin/conflicts/3/resolve", `{"source_record_id":4}`, nil, 200, "resolve"},
		{"resolve nothing", "POST", "/api/admin/conflicts/3/resolve", `{"note":"n"}`, nil, 400, ""},
		{"resolve unreviewable", "POST", "/api/admin/conflicts/3/resolve", `{"value":"X"}`, &store.ValidationError{Msg: "field is not reviewable: dvd_code"}, 400, "resolve"},
		{"resolve done", "POST", "/api/admin/conflicts/3/resolve", `{"value":"X"}`, &store.ConflictError{Msg: "conflict is already resolved"}, 409, "resolve"},
		{"resolve missing", "POST", "/api/admin/conflicts/3/resolve", `{"value":"X"}`, pgx.ErrNoRows, 404, "resolve"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubEditor{err: tc.err}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("X-Admin-User", "alice")
			rec := httptest.NewRecorder()
			editorMux(st).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			got := ""
			if len(st.calls) > 0 {
				got = st.calls[0]
			}
			if got != tc.call {
				t.Errorf("store call %q, want %q", got, tc.call)
			}
			if tc.call != "" && tc.method != "GET" && st.actor != "alice" {
				t.Errorf("actor %q, want alice (X-Admin-User)", st.actor)
			}
		})
	}
}

func TestAdminReleaseHandlersPassInput(t *testing.T) {
	st := &stubEditor{}
	mux := editorMux(st)
	do := func(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	do("PATCH", "/api/admin/releases/1", `{"title":"T","tags":["a","A"],"version":4,"reason":"r"}`, nil)
	if st.patch.Version != 4 || st.patch.Reason != "r" || len(st.patch.Columns) != 1 || st.patch.Tags == nil || len(*st.patch.Tags) != 1 {
		t.Errorf("patch = %+v", st.patch)
	}
	if st.actor != "admin" {
		t.Errorf("default actor = %q, want admin", st.actor)
	}
	do("POST", "/api/admin/releases/1/status", `{"status":"upcoming","scheduled_at":"2030-01-02T03:04:05Z","version":2}`, nil)
	if st.trans.To != "upcoming" || st.trans.ScheduledAt == nil || st.trans.ScheduledAt.Year() != 2030 || st.trans.Version != 2 {
		t.Errorf("transition = %+v", st.trans)
	}
	do("POST", "/api/admin/releases/1/sources", `{"kind":"external_link","provider":"p","label":"L","url":"https://p.test","region_allow":["jp"]}`, nil)
	if st.src.VersionKind != "main" || len(st.src.RegionAllow) != 1 || st.src.RegionAllow[0] != "JP" {
		t.Errorf("source = %+v", st.src)
	}
	do("PUT", "/api/admin/releases/1/performers/8", `{"role":"Guest","billing_order":2}`, nil)
	if st.perf.PerformerID != 8 || st.perf.Role != "guest" || *st.perf.BillingOrder != 2 {
		t.Errorf("performer = %+v", st.perf)
	}
	do("POST", "/api/admin/releases/1/sources/reorder", `{"ids":[3,1,2]}`, nil)
	if len(st.ids) != 3 || st.ids[0] != 3 {
		t.Errorf("ids = %v", st.ids)
	}
	do("POST", "/api/admin/conflicts/2/resolve", `{"value":"2021-01-01"}`, nil)
	if string(st.value) != `"2021-01-01"` {
		t.Errorf("value = %s", st.value)
	}
	if rec := do("PATCH", "/api/admin/releases/1", `{"title":"T"}`, map[string]string{"X-Admin-User": strings.Repeat("x", 200)}); rec.Code != 400 {
		t.Errorf("long X-Admin-User: %d", rec.Code)
	}
	// 5xx never leaks store error text.
	st.err = context.DeadlineExceeded
	if rec := do("GET", "/api/admin/releases/1", "", nil); rec.Code != 500 || strings.Contains(rec.Body.String(), "deadline") {
		t.Errorf("500 body = %d %s", rec.Code, rec.Body.String())
	}
}
