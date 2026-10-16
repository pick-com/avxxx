package api

import (
	"avxxx/internal/clientip"
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// stubPerformers records calls and returns err for every store call.
type stubPerformers struct {
	err      error
	calls    []string
	actor    string
	filter   store.AdminPerformerFilter
	patch    store.PerformerPatch
	status   store.PerformerStatusChange
	decision string
	request  store.PerformerRequestInput
	seeds    []store.SimilaritySeed
}

func (s *stubPerformers) hit(name, actor string) { s.calls = append(s.calls, name); s.actor = actor }

func (s *stubPerformers) AdminPerformers(_ context.Context, f store.AdminPerformerFilter) ([]store.AdminPerformerSummary, string, error) {
	s.hit("list", "")
	s.filter = f
	return []store.AdminPerformerSummary{{ID: 1}}, "", s.err
}
func (s *stubPerformers) AdminPerformerCounts(context.Context, string, string) (map[string]int64, error) {
	return map[string]int64{"all": 1}, nil
}
func (s *stubPerformers) AdminPerformer(context.Context, int64) (store.AdminPerformer, error) {
	s.hit("detail", "")
	return store.AdminPerformer{ID: 1}, s.err
}
func (s *stubPerformers) PatchPerformer(_ context.Context, _ int64, p store.PerformerPatch, actor string) (store.PerformerPatchResult, error) {
	s.hit("patch", actor)
	s.patch = p
	return store.PerformerPatchResult{PerformerID: 1}, s.err
}
func (s *stubPerformers) ClearPerformerOverride(_ context.Context, _ int64, _, _, actor string) error {
	s.hit("clear", actor)
	return s.err
}
func (s *stubPerformers) AddPerformerAlias(_ context.Context, _ int64, _ store.AliasInput, actor string) (store.PerformerAlias, error) {
	s.hit("alias_add", actor)
	return store.PerformerAlias{ID: 3}, s.err
}
func (s *stubPerformers) RemovePerformerAlias(_ context.Context, _, _ int64, _, actor string) error {
	s.hit("alias_remove", actor)
	return s.err
}
func (s *stubPerformers) SetPerformerStatus(_ context.Context, _ int64, c store.PerformerStatusChange, actor string) (store.PerformerStatusResult, error) {
	s.hit("status", actor)
	s.status = c
	return store.PerformerStatusResult{}, s.err
}
func (s *stubPerformers) PerformerRequests(context.Context, store.PerformerRequestFilter) ([]store.PerformerRequest, string, error) {
	s.hit("requests", "")
	return []store.PerformerRequest{}, "", s.err
}
func (s *stubPerformers) DecidePerformerRequest(_ context.Context, _ int64, to string, _ store.RequestDecision, actor string) (store.RequestDecisionResult, error) {
	s.hit("decide", actor)
	s.decision = to
	return store.RequestDecisionResult{}, s.err
}
func (s *stubPerformers) CreatePerformerRequest(_ context.Context, in store.PerformerRequestInput, now time.Time) (store.PerformerRequestReceipt, error) {
	s.hit("create_request", "")
	s.request = in
	return store.PerformerRequestReceipt{ID: 5, Kind: in.Kind, Status: store.RequestOpen, DueAt: now.Add(store.RequestSLA(in.Kind))}, s.err
}
func (s *stubPerformers) SimilarityWeightsRow(context.Context) (store.SimilarityWeightsRow, error) {
	return store.SimilarityWeightsRow{Weights: recs.DefaultWeights}, nil
}
func (s *stubPerformers) SimilaritySeeds(context.Context, []int64, int) ([]store.SimilaritySeed, error) {
	s.hit("seeds", "")
	return s.seeds, s.err
}

func performerMux(st *stubPerformers, lim community.Limiter) *http.ServeMux {
	srv := &Server{adminToken: "secret", performerEd: st, limiter: lim}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestAdminPerformerStatusCodes(t *testing.T) {
	tests := []struct {
		name, method, path, body string
		err                      error
		want                     int
		call                     string
	}{
		{"list", "GET", "/api/admin/performers?filter=missing_birth,upcoming&filter=birth_hidden&status=hidden", "", nil, 200, "list"},
		{"list bad filter", "GET", "/api/admin/performers?filter=pretty", "", nil, 400, ""},
		{"list bad status", "GET", "/api/admin/performers?status=deleted", "", nil, 400, ""},
		{"list bad cursor", "GET", "/api/admin/performers?cursor=x", "", store.ErrInvalidQuery, 400, "list"},
		{"detail", "GET", "/api/admin/performers/1", "", nil, 200, "detail"},
		{"detail bad id", "GET", "/api/admin/performers/x", "", nil, 400, ""},
		{"detail missing", "GET", "/api/admin/performers/9", "", pgx.ErrNoRows, 404, "detail"},
		{"patch", "PATCH", "/api/admin/performers/1", `{"birth_date":"1995-02-03","name_ja":"橋本","version":3,"reason":"agency"}`, nil, 200, "patch"},
		{"patch hide birthday", "PATCH", "/api/admin/performers/1", `{"birth_date_hidden":true}`, nil, 200, "patch"},
		{"patch bad date", "PATCH", "/api/admin/performers/1", `{"birth_date":"03/02/1995"}`, nil, 400, ""},
		{"patch unknown field", "PATCH", "/api/admin/performers/1", `{"status":"hidden"}`, nil, 400, ""},
		{"patch empty", "PATCH", "/api/admin/performers/1", `{}`, nil, 400, ""},
		{"patch stale", "PATCH", "/api/admin/performers/1", `{"cup_size":"C","version":1}`, &store.ConflictError{Msg: "stale"}, 409, "patch"},
		{"clear override", "DELETE", "/api/admin/performers/1/overrides/birth_date", ``, nil, 200, "clear"},
		{"clear missing override", "DELETE", "/api/admin/performers/1/overrides/x", ``, pgx.ErrNoRows, 404, "clear"},
		{"alias add", "POST", "/api/admin/performers/1/aliases", `{"name":"Sena H.","language":"en"}`, nil, 201, "alias_add"},
		{"alias add empty", "POST", "/api/admin/performers/1/aliases", `{"name":"  "}`, nil, 400, ""},
		{"alias add bad type", "POST", "/api/admin/performers/1/aliases", `{"name":"X","alias_type":"merged"}`, nil, 400, ""},
		{"alias exists", "POST", "/api/admin/performers/1/aliases", `{"name":"X"}`, &store.ConflictError{Msg: "exists"}, 409, "alias_add"},
		{"alias remove", "DELETE", "/api/admin/performers/1/aliases/3", ``, nil, 200, "alias_remove"},
		{"alias remove missing", "DELETE", "/api/admin/performers/1/aliases/3", ``, pgx.ErrNoRows, 404, "alias_remove"},
		{"hide", "POST", "/api/admin/performers/1/status", `{"status":"hidden","reason":"site title, not a person"}`, nil, 200, "status"},
		{"hide bad status", "POST", "/api/admin/performers/1/status", `{"status":"deleted"}`, nil, 400, ""},
		{"hide no reason", "POST", "/api/admin/performers/1/status", `{"status":"hidden"}`, &store.ValidationError{Msg: "reason is required"}, 400, "status"},
		{"unhide noop", "POST", "/api/admin/performers/1/status", `{"status":"active"}`, &store.ConflictError{Msg: "already active"}, 409, "status"},
		{"requests", "GET", "/api/admin/performer-requests?status=open&kind=hide_birth_date&performer_id=2", "", nil, 200, "requests"},
		{"requests bad status", "GET", "/api/admin/performer-requests?status=done", "", nil, 400, ""},
		{"requests bad performer", "GET", "/api/admin/performer-requests?performer_id=x", "", nil, 400, ""},
		{"approve", "POST", "/api/admin/performer-requests/4/approve", ``, nil, 200, "decide"},
		{"approve decided", "POST", "/api/admin/performer-requests/4/approve", `{"note":"ok"}`, &store.ConflictError{Msg: "request is already approved"}, 409, "decide"},
		{"approve missing", "POST", "/api/admin/performer-requests/4/approve", ``, pgx.ErrNoRows, 404, "decide"},
		{"reject needs note", "POST", "/api/admin/performer-requests/4/reject", `{}`, nil, 400, ""},
		{"reject", "POST", "/api/admin/performer-requests/4/reject", `{"note":"no evidence of agency"}`, nil, 200, "decide"},
		{"preview", "POST", "/api/admin/similarity-weights/preview", `{"co_watch":0.1}`, nil, 200, "seeds"},
		{"preview bad weights", "POST", "/api/admin/similarity-weights/preview", `{"metadata":0,"co_star":0,"co_watch":0}`, nil, 400, ""},
		{"preview too many", "POST", "/api/admin/similarity-weights/preview", `{"sample":500}`, nil, 400, ""},
		{"preview empty body", "POST", "/api/admin/similarity-weights/preview", ``, nil, 200, "seeds"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &stubPerformers{err: tc.err}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer secret")
			req.Header.Set("X-Admin-User", "alice")
			rec := httptest.NewRecorder()
			performerMux(st, nil).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			got := ""
			if len(st.calls) > 0 {
				got = st.calls[0]
			}
			if got != tc.call {
				t.Fatalf("store call %q, want %q", got, tc.call)
			}
			if tc.call != "" && tc.method != "GET" && tc.call != "seeds" && st.actor != "alice" {
				t.Errorf("actor %q, want alice", st.actor)
			}
		})
	}
}

func TestAdminPerformerHandlersPassInput(t *testing.T) {
	st := &stubPerformers{}
	do := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		performerMux(st, nil).ServeHTTP(rec, req)
		return rec
	}
	do("GET", "/api/admin/performers?filter=missing_birth,upcoming&filter=suspected_duplicate&q=sena", "")
	if strings.Join(st.filter.Filters, ",") != "missing_birth,upcoming,suspected_duplicate" || st.filter.Q != "sena" {
		t.Errorf("filter %+v", st.filter)
	}
	do("PATCH", "/api/admin/performers/1", `{"birth_date":"1995-02-03","version":4}`)
	if !st.patch.TouchesBirthDate() || st.patch.Version != 4 {
		t.Errorf("patch %+v", st.patch)
	}
	do("POST", "/api/admin/performer-requests/1/reject", `{"note":"n"}`)
	if st.decision != store.RequestRejected || st.actor != "admin" {
		t.Errorf("decision %q actor %q", st.decision, st.actor)
	}
	rec := do("GET", "/api/admin/performers", "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["counts"] == nil {
		t.Errorf("list body %s", rec.Body)
	}
}

func TestPublicPerformerRequest(t *testing.T) {
	lim := community.NewMemoryLimiter()
	st := &stubPerformers{}
	mux := performerMux(st, lim)
	post := func(body, addr string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/performer-requests", strings.NewReader(body))
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	ok := `{"performer":"sena-hashimoto","kind":"hide_birth_date","contact":"agent@agency.example","role":"agency","message":"please hide","links":["https://agency.example/sena"]}`
	rec := post(ok, "10.0.0.1:1234")
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	// The receipt never echoes evidence or contact.
	if b := rec.Body.String(); strings.Contains(b, "agency.example") || strings.Contains(b, "please hide") {
		t.Fatalf("receipt leaks evidence: %s", b)
	}
	if st.request.Role != "agency" || st.request.Contact != "agent@agency.example" {
		t.Errorf("request %+v", st.request)
	}
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"no contact", `{"performer":"x","kind":"other"}`, 400},
		{"bad kind", `{"performer":"x","kind":"delete_me","contact":"a@b.example"}`, 400},
		{"bad email", `{"performer":"x","kind":"other","contact":"a@"}`, 400},
		{"bad link", `{"performer":"x","kind":"other","contact":"tel 123","links":["javascript:alert(1)"]}`, 400},
		{"correct needs field", `{"performer":"x","kind":"correct_field","contact":"tel 123"}`, 400},
		{"correct bad value", `{"performer":"x","kind":"correct_field","field":"birth_date","value":"tomorrow","contact":"tel 123"}`, 400},
		{"correct unknown field", `{"performer":"x","kind":"correct_field","field":"status","value":"hidden","contact":"tel 123"}`, 400},
		{"value without correct", `{"performer":"x","kind":"other","field":"birth_date","contact":"tel 123"}`, 400},
		{"unknown key", `{"performer":"x","kind":"other","contact":"tel 123","admin":true}`, 400},
		{"correct ok", `{"performer":"x","kind":"correct_field","field":"birth_date","value":"1999-01-02","contact":"tel 123","role":"self"}`, 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if rec := post(tc.body, "10.0.1."+tc.name[:1]+":1"); rec.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
		})
	}
	// Unknown performer is 404.
	st.err = pgx.ErrNoRows
	if rec := post(ok, "10.0.2.1:1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown performer: %d", rec.Code)
	}
	st.err = nil
	// Per-address limit: 5 per hour.
	codes := []int{}
	for range 6 {
		codes = append(codes, post(ok, "10.9.9.9:5555").Code)
	}
	if codes[4] != http.StatusCreated || codes[5] != http.StatusTooManyRequests {
		t.Fatalf("rate limit codes %v", codes)
	}
	// Another address is still allowed.
	if rec := post(ok, "10.9.9.8:5555"); rec.Code != http.StatusCreated {
		t.Fatalf("other address: %d", rec.Code)
	}
}

// TestPerformerRequestLimitBehindProxy: with TRUSTED_PROXIES the per-address
// limit keys on the forwarded client, not the proxy, and a client cannot
// escape it by sending its own X-Forwarded-For entries.
func TestPerformerRequestLimitBehindProxy(t *testing.T) {
	ips, err := clientip.Parse("127.0.0.1/32,::1/128")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{adminToken: "secret", performerEd: &stubPerformers{}, limiter: community.NewMemoryLimiter(), clientIP: ips}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	ok := `{"performer":"sena-hashimoto","kind":"other","contact":"tel 123"}`
	post := func(remote string, xff ...string) int {
		req := httptest.NewRequest("POST", "/api/performer-requests", strings.NewReader(ok))
		req.RemoteAddr = remote
		for _, v := range xff {
			req.Header.Add("X-Forwarded-For", v)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	// Caddy appends the real client; the client spoofs a new left-most
	// address on every request and still hits its limit after 5.
	for i := range performerRequestPerIP {
		if c := post("127.0.0.1:40000", fmt.Sprintf("192.0.2.%d, 198.51.100.7", i)); c != http.StatusCreated {
			t.Fatalf("request %d: %d", i, c)
		}
	}
	if c := post("127.0.0.1:40000", "192.0.2.99, 198.51.100.7"); c != http.StatusTooManyRequests {
		t.Fatalf("spoofed request: %d, want 429", c)
	}
	// Another client behind the same proxy has its own budget.
	if c := post("[::1]:40000", "198.51.100.8"); c != http.StatusCreated {
		t.Fatalf("other client behind proxy: %d", c)
	}
	// An untrusted peer's header is ignored: keyed on the peer itself.
	for i := range performerRequestPerIP {
		if c := post("203.0.113.5:1", fmt.Sprintf("198.51.100.%d", 100+i)); c != http.StatusCreated {
			t.Fatalf("direct request %d: %d", i, c)
		}
	}
	if c := post("203.0.113.5:1", "198.51.100.200"); c != http.StatusTooManyRequests {
		t.Fatalf("direct client escaped its limit with X-Forwarded-For: %d", c)
	}
}

func TestSimilarityPreviewReranks(t *testing.T) {
	ev := func(meta, cs, cw float64) recs.Evidence {
		return recs.Evidence{Metadata: meta, CoStarScore: cs, CoWatch: cw}
	}
	seeds := []store.SimilaritySeed{{ID: 1, Slug: "a", Name: "A", Neighbors: []store.SimilarityNeighbor{
		{ID: 2, Slug: "b", Name: "B", Score: 0.6, Evidence: ev(0.1, 0, 1)},   // audience overlap
		{ID: 3, Slug: "c", Name: "C", Score: 0.4, Evidence: ev(0.9, 0.3, 0)}, // same style, co-star
	}}}
	metaOnly := recs.Weights{Metadata: 1}
	out := similarityPreview(seeds, recs.DefaultWeights, metaOnly, 5)
	if len(out) != 1 {
		t.Fatal(out)
	}
	s := out[0]
	if s.Current[0].ID != 2 || s.Proposed[0].ID != 3 || !s.Changed || s.Overlap != 2 {
		t.Fatalf("preview %+v", s)
	}
	if s.Proposed[0].StoredRank != 2 || s.Proposed[0].Rank != 1 || s.Proposed[0].Name != "C" || s.Proposed[0].Match != 90 {
		t.Errorf("proposed[0] %+v", s.Proposed[0])
	}
	same := similarityPreview(seeds, recs.DefaultWeights, recs.DefaultWeights, 5)
	if same[0].Changed {
		t.Errorf("same weights changed: %+v", same[0])
	}
}
