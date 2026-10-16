package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type stubSearch struct {
	gotQ, gotTerm, gotValue, gotActor string
	gotLimit, gotDays, gotMin         int
	gotID                             int64
	result                            store.SearchResult
	createErr                         error
}

func (s *stubSearch) Search(_ context.Context, q string, limit int) (store.SearchResult, error) {
	s.gotQ, s.gotLimit = q, limit
	return s.result, nil
}
func (s *stubSearch) Suggest(_ context.Context, q string, limit int) ([]map[string]any, error) {
	s.gotQ, s.gotLimit = q, limit
	return []map[string]any{{"type": "release", "id": int64(1), "slug": "a", "name": "夜航的人"}, {"type": "query", "name": "夜航"}}, nil
}
func (s *stubSearch) HotSearches(_ context.Context, period string, limit int) ([]store.HotSearch, time.Time, error) {
	s.gotLimit = limit
	return []store.HotSearch{{Rank: 1, Q: "夜航的人", Searches: 12}}, time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), nil
}
func (s *stubSearch) ZeroResultTerms(_ context.Context, days, minZero, limit int) ([]store.ZeroResultTerm, error) {
	s.gotDays, s.gotMin, s.gotLimit = days, minZero, limit
	return []store.ZeroResultTerm{}, nil
}
func (s *stubSearch) SearchStats(_ context.Context, days int) (store.SearchStats, error) {
	s.gotDays = days
	return store.SearchStats{Days: days}, nil
}
func (s *stubSearch) SearchSynonyms(context.Context, string) ([]store.SearchSynonym, error) {
	return []store.SearchSynonym{}, nil
}
func (s *stubSearch) CreateSearchSynonym(_ context.Context, term, canonical, actor string) (store.SearchSynonym, error) {
	s.gotTerm, s.gotValue, s.gotActor = term, canonical, actor
	return store.SearchSynonym{ID: 1, Term: term, Canonical: canonical}, s.createErr
}
func (s *stubSearch) UpdateSearchSynonym(_ context.Context, id int64, term, canonical string) (store.SearchSynonym, error) {
	if id == 404 {
		return store.SearchSynonym{}, pgx.ErrNoRows
	}
	s.gotID, s.gotTerm, s.gotValue = id, term, canonical
	return store.SearchSynonym{ID: id}, nil
}
func (s *stubSearch) DeleteSearchSynonym(_ context.Context, id int64) error {
	if id == 404 {
		return pgx.ErrNoRows
	}
	s.gotID = id
	return nil
}
func (s *stubSearch) SearchRedirects(context.Context, string) ([]store.SearchRedirectRule, error) {
	return nil, nil
}
func (s *stubSearch) CreateSearchRedirect(_ context.Context, term, target, reason, actor string) (store.SearchRedirectRule, error) {
	s.gotTerm, s.gotValue, s.gotActor = term, target, actor
	return store.SearchRedirectRule{ID: 1}, nil
}
func (s *stubSearch) UpdateSearchRedirect(_ context.Context, id int64, term, target, reason string) (store.SearchRedirectRule, error) {
	return store.SearchRedirectRule{ID: id}, nil
}
func (s *stubSearch) DeleteSearchRedirect(context.Context, int64) error { return nil }
func (s *stubSearch) SearchBlocklist(context.Context) ([]store.SearchBlockedTerm, error) {
	return nil, nil
}
func (s *stubSearch) BlockSearchTerm(_ context.Context, term, reason, actor string) (store.SearchBlockedTerm, error) {
	s.gotTerm = term
	return store.SearchBlockedTerm{ID: 1, Term: term}, nil
}
func (s *stubSearch) UnblockSearchTerm(context.Context, int64) error { return nil }
func (s *stubSearch) AddPerformerAliasFromTerm(_ context.Context, id int64, term, name, reason, actor string) (store.TermFix, error) {
	if id == 404 {
		return store.TermFix{}, pgx.ErrNoRows
	}
	if id == 409 {
		return store.TermFix{}, &store.ConflictError{Msg: "performer already has alias"}
	}
	s.gotID, s.gotTerm, s.gotValue, s.gotActor = id, term, name, actor
	return store.TermFix{EntityType: "performer", EntityID: id, Action: "add_alias", Value: name, Term: term}, nil
}
func (s *stubSearch) AddEnglishTitleFromTerm(_ context.Context, id int64, term, title, reason, actor string) (store.TermFix, error) {
	s.gotID, s.gotTerm, s.gotValue, s.gotActor = id, term, title, actor
	return store.TermFix{EntityType: "release", EntityID: id, Action: "add_english_title", Value: title, Term: term}, nil
}

func searchServer(st *stubSearch) http.Handler {
	srv := &Server{adminToken: "tok"}
	if st != nil {
		srv.search = st
	}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func doSearch(t *testing.T, h http.Handler, method, path, body string, admin bool) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if admin {
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("X-Admin-User", "alice")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestCatalogSearchEnvelope(t *testing.T) {
	st := &stubSearch{result: store.SearchResult{
		Query: "夜航的人全集", EffectiveQuery: "夜航的人", Rescued: true,
		Items:     []map[string]any{{"type": "release", "id": int64(3), "slug": "night", "name": "夜航的人", "match": "title"}},
		Redirect:  &store.SearchRedirect{TargetURL: "/help", Reason: "help"},
		ExactCode: map[string]any{"type": "release", "id": int64(3), "slug": "night"},
	}}
	h := searchServer(st)
	rec, out := doSearch(t, h, "GET", "/api/catalog/search?q=%E5%A4%9C&limit=5", "", false)
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if st.gotLimit != 5 || st.gotQ != "夜" {
		t.Errorf("store got q=%q limit=%d", st.gotQ, st.gotLimit)
	}
	if out["rescued"] != true || out["effective_query"] != "夜航的人" || out["query"] != "夜航的人全集" {
		t.Errorf("envelope %v", out)
	}
	if red, _ := out["redirect"].(map[string]any); red["target_url"] != "/help" {
		t.Errorf("redirect %v", out["redirect"])
	}
	if out["exact_match"] == nil || len(out["items"].([]any)) != 1 {
		t.Errorf("items / exact %v", out)
	}

	st.result = store.SearchResult{Query: "x", Items: []map[string]any{}}
	_, out = doSearch(t, h, "GET", "/api/catalog/search?q=x", "", false)
	for _, k := range []string{"rescued", "effective_query", "redirect", "exact_match"} {
		if _, ok := out[k]; ok {
			t.Errorf("%s present without a reason: %v", k, out)
		}
	}
	for _, p := range []string{"/api/catalog/search?q=x&limit=-1", "/api/catalog/search?q=x&lang=xx"} {
		if rec, _ := doSearch(t, h, "GET", p, "", false); rec.Code != 400 {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec, _ := doSearch(t, searchServer(nil), "GET", "/api/catalog/search?q=x", "", false); rec.Code != 503 {
		t.Errorf("nil store: %d", rec.Code)
	}
}

func TestSearchHotAndSuggest(t *testing.T) {
	st := &stubSearch{}
	h := searchServer(st)
	rec, out := doSearch(t, h, "GET", "/api/search/hot", "", false)
	if rec.Code != 200 || out["period"] != "24h" || len(out["items"].([]any)) != 1 || out["computed_at"] == nil {
		t.Errorf("hot %d %v", rec.Code, out)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Error("hot not cacheable")
	}
	if rec, _ := doSearch(t, h, "GET", "/api/search/hot?limit=x", "", false); rec.Code != 400 {
		t.Errorf("bad limit: %d", rec.Code)
	}
	rec, out = doSearch(t, h, "GET", "/api/search/suggest?q=%20%E5%A4%9C%E8%88%AA&limit=3", "", false)
	if rec.Code != 200 || out["q"] != "夜航" || st.gotLimit != 3 || len(out["items"].([]any)) != 2 {
		t.Errorf("suggest %d %v", rec.Code, out)
	}
	if rec, _ := doSearch(t, h, "GET", "/api/search/suggest?q="+strings.Repeat("a", 201), "", false); rec.Code != 400 {
		t.Errorf("long q: %d", rec.Code)
	}
	if rec, _ := doSearch(t, h, "GET", "/api/search/suggest?q=a&lang=zz", "", false); rec.Code != 400 {
		t.Errorf("bad lang: %d", rec.Code)
	}
	if rec, _ := doSearch(t, searchServer(nil), "GET", "/api/search/hot", "", false); rec.Code != 503 {
		t.Errorf("nil store: %d", rec.Code)
	}
}

func TestAdminSearchStatusCodes(t *testing.T) {
	st := &stubSearch{}
	h := searchServer(st)
	for _, c := range []struct {
		method, path, body string
		admin              bool
		want               int
	}{
		{"GET", "/api/admin/search/zero-results", "", false, 401},
		{"GET", "/api/admin/search/zero-results", "", true, 200},
		{"GET", "/api/admin/search/zero-results?days=0", "", true, 400},
		{"GET", "/api/admin/search/zero-results?days=91", "", true, 400},
		{"GET", "/api/admin/search/zero-results?min=0", "", true, 400},
		{"GET", "/api/admin/search/zero-results?limit=101", "", true, 400},
		{"GET", "/api/admin/search/stats?days=30", "", true, 200},
		{"GET", "/api/admin/search/stats?days=abc", "", true, 400},
		{"GET", "/api/admin/search/synonyms", "", true, 200},
		{"POST", "/api/admin/search/synonyms", `{"term":" 夜航 ","canonical":"夜航的人"}`, true, 201},
		{"POST", "/api/admin/search/synonyms", `{"term":"夜航","canonical":"夜航"}`, true, 400},
		{"POST", "/api/admin/search/synonyms", `{"term":"","canonical":"x"}`, true, 400},
		{"POST", "/api/admin/search/synonyms", `{"term":"a","canonical":"b","extra":1}`, true, 400},
		{"POST", "/api/admin/search/synonyms", `not json`, true, 400},
		{"PUT", "/api/admin/search/synonyms/7", `{"term":"a b","canonical":"c"}`, true, 200},
		{"PUT", "/api/admin/search/synonyms/404", `{"term":"a","canonical":"c"}`, true, 404},
		{"PUT", "/api/admin/search/synonyms/x", `{"term":"a","canonical":"c"}`, true, 400},
		{"DELETE", "/api/admin/search/synonyms/7", "", true, 204},
		{"DELETE", "/api/admin/search/synonyms/404", "", true, 404},
		{"POST", "/api/admin/search/redirects", `{"term":"怎么下载","target_url":"/help/download","reason":"help"}`, true, 201},
		{"POST", "/api/admin/search/redirects", `{"term":"怎么下载","target_url":"javascript:alert(1)"}`, true, 400},
		{"POST", "/api/admin/search/redirects", `{"term":"x","target_url":"//evil.com"}`, true, 400},
		{"PUT", "/api/admin/search/redirects/3", `{"term":"x","target_url":"https://help.example.com/a"}`, true, 200},
		{"DELETE", "/api/admin/search/redirects/3", "", true, 204},
		{"GET", "/api/admin/search/redirects", "", true, 200},
		{"POST", "/api/admin/search/blocklist", `{"term":"Spam Term"}`, true, 201},
		{"DELETE", "/api/admin/search/blocklist/1", "", true, 204},
		{"GET", "/api/admin/search/blocklist", "", true, 200},
		{"POST", "/api/admin/search/terms/alias", `{"term":"Yui H","performer_id":5}`, true, 201},
		{"POST", "/api/admin/search/terms/alias", `{"term":"Yui H"}`, true, 400},
		{"POST", "/api/admin/search/terms/alias", `{"term":"","performer_id":5}`, true, 400},
		{"POST", "/api/admin/search/terms/alias", `{"term":"x","performer_id":404}`, true, 404},
		{"POST", "/api/admin/search/terms/alias", `{"term":"x","performer_id":409}`, true, 409},
		{"POST", "/api/admin/search/terms/english-title", `{"term":"night flight","release_id":9,"title":"Night Flight"}`, true, 201},
		{"POST", "/api/admin/search/terms/english-title", `{"term":"night flight"}`, true, 400},
	} {
		rec, _ := doSearch(t, h, c.method, c.path, c.body, c.admin)
		if rec.Code != c.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", c.method, c.path, c.body, rec.Code, c.want, rec.Body)
		}
	}
}

func TestAdminSearchNormalizesAndRecordsActor(t *testing.T) {
	st := &stubSearch{}
	h := searchServer(st)
	doSearch(t, h, "POST", "/api/admin/search/synonyms", `{"term":"  Night　 Flight ","canonical":"夜航的人"}`, true)
	if st.gotTerm != "night flight" || st.gotValue != "夜航的人" || st.gotActor != "alice" {
		t.Errorf("synonym stored as %q -> %q by %q", st.gotTerm, st.gotValue, st.gotActor)
	}
	doSearch(t, h, "POST", "/api/admin/search/terms/alias", `{"term":"  Yui  Hatano ","performer_id":5}`, true)
	if st.gotID != 5 || st.gotTerm != "yui hatano" || st.gotValue != "Yui Hatano" {
		t.Errorf("alias %d %q %q", st.gotID, st.gotTerm, st.gotValue)
	}
	doSearch(t, h, "POST", "/api/admin/search/terms/english-title", `{"term":"night flight","release_id":9,"title":" Night  Flight "}`, true)
	if st.gotID != 9 || st.gotValue != "Night Flight" {
		t.Errorf("english title %d %q", st.gotID, st.gotValue)
	}
	doSearch(t, h, "GET", "/api/admin/search/zero-results", "", true)
	if st.gotDays != 7 || st.gotMin != 20 || st.gotLimit != 50 {
		t.Errorf("zero-result defaults %d %d %d", st.gotDays, st.gotMin, st.gotLimit)
	}
}
