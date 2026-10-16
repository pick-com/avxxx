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
	"time"
)

type stubSitemap struct {
	gotCursor string
	gotLimit  int
	err       error
	recorded  []audit.Run
}

func (s *stubSitemap) list(_ context.Context, cursor string, limit int) ([]store.SitemapEntry, string, error) {
	s.gotCursor, s.gotLimit = cursor, limit
	if s.err != nil {
		return nil, "", s.err
	}
	return []store.SitemapEntry{{Slug: "abc-001", UpdatedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}}, "next", nil
}
func (s *stubSitemap) SitemapReleases(ctx context.Context, c string, l int) ([]store.SitemapEntry, string, error) {
	return s.list(ctx, c, l)
}
func (s *stubSitemap) SitemapPerformers(ctx context.Context, c string, l int) ([]store.SitemapEntry, string, error) {
	return s.list(ctx, c, l)
}
func (s *stubSitemap) SitemapTags(ctx context.Context, c string, l int) ([]store.SitemapEntry, string, error) {
	return s.list(ctx, c, l)
}
func (s *stubSitemap) SitemapCounts(context.Context) (store.SitemapCounts, error) {
	return store.SitemapCounts{Releases: 3, PublishedReleases: 5, Performers: 7, Tags: 9}, s.err
}
func (s *stubSitemap) RecordRun(_ context.Context, r audit.Run) error {
	s.recorded = append(s.recorded, r)
	return s.err
}
func (s *stubSitemap) LatestRun(context.Context, string) (audit.Run, bool, error) {
	if len(s.recorded) == 0 {
		return audit.Run{}, false, nil
	}
	return s.recorded[len(s.recorded)-1], true, nil
}

func sitemapMux(st *stubSitemap) *http.ServeMux {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins(), sitemap: st}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestSitemapList(t *testing.T) {
	st := &stubSitemap{}
	mux := sitemapMux(st)
	for _, path := range []string{"/api/sitemap/releases", "/api/sitemap/performers", "/api/sitemap/tags"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?cursor=abc&limit=10", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		var body struct {
			Items      []store.SitemapEntry `json:"items"`
			NextCursor *string              `json:"next_cursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 1 || body.Items[0].Slug != "abc-001" || body.NextCursor == nil || *body.NextCursor != "next" {
			t.Fatalf("%s: body %s", path, rec.Body.String())
		}
		if st.gotCursor != "abc" || st.gotLimit != 10 {
			t.Fatalf("%s: cursor %q limit %d", path, st.gotCursor, st.gotLimit)
		}
		if rec.Header().Get("Cache-Control") == "" {
			t.Fatalf("%s: no Cache-Control", path)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sitemap/releases?limit=-1", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative limit: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sitemap/counts", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"performers":7`) {
		t.Fatalf("counts: %d %s", rec.Code, rec.Body.String())
	}
}

func TestSEOScore(t *testing.T) {
	cases := []struct {
		ssr, cov float64
		want     int
	}{{1, 1, 100}, {0, 0, 0}, {0.9, 1, 94}, {1, 0.5, 80}, {2, -1, 60}}
	for _, c := range cases {
		if got := SEOScore(c.ssr, c.cov); got != c.want {
			t.Errorf("SEOScore(%v, %v) = %d, want %d", c.ssr, c.cov, got, c.want)
		}
	}
}

func TestSEOReport(t *testing.T) {
	st := &stubSitemap{}
	mux := sitemapMux(st)
	do := func(method, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/admin/seo/report", strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := do(http.MethodGet, "", "secret"); rec.Code != http.StatusNotFound {
		t.Fatalf("latest before any run: %d", rec.Code)
	}
	if rec := do(http.MethodPost, `{"ssr_pass_rate":1,"sitemap_coverage":1}`, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
	for _, bad := range []string{`{`, `{"ssr_pass_rate":1}`, `{"ssr_pass_rate":1.5,"sitemap_coverage":1}`, `{"ssr_pass_rate":1,"sitemap_coverage":1,"checked":1,"failed":2}`} {
		if rec := do(http.MethodPost, bad, "secret"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, rec.Code)
		}
	}
	rec := do(http.MethodPost, `{"base_url":"http://web:3000","ssr_pass_rate":0.9,"sitemap_coverage":1,"checked":10,"failed":1,"by_type":{"release":{"checked":5}}}`, "secret")
	if rec.Code != http.StatusCreated {
		t.Fatalf("record: %d %s", rec.Code, rec.Body.String())
	}
	if len(st.recorded) != 1 {
		t.Fatalf("recorded %d runs", len(st.recorded))
	}
	run := st.recorded[0]
	if run.Job != SEOCheckJob || run.Checked != 10 || run.Issues != 1 || run.Result != "issues" || run.Detail["score"] != 94 || run.Detail["band"] != audit.BandHealthy || run.Detail["by_type"] == nil {
		t.Fatalf("run %+v", run)
	}
	rec = do(http.MethodGet, "", "secret")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"score":94`) {
		t.Fatalf("latest: %d %s", rec.Code, rec.Body.String())
	}
}
