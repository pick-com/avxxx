package api

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// expectedRoutes is the full public route surface of the API.
var expectedRoutes = []string{
	"GET /api/health",
	"GET /api/live",
	"POST /api/auth/register",
	"POST /api/auth/login",
	"GET /api/auth/me",
	"GET /api/me/watch-history",
	"POST /api/me/watch-history",
	"GET /api/me/favorites",
	"POST /api/me/favorites",
	"GET /api/items",
	"GET /api/performers",
	"GET /api/performers/{slug}",
	"GET /api/performers/{slug}/releases",
	"GET /api/releases",
	"GET /api/releases/{slug}",
	"GET /api/releases/{slug}/performers",
	"GET /api/releases/{slug}/sources",
	"GET /api/releases/{slug}/playback",
	"GET /api/releases/{slug}/play-config",
	"POST /api/admin/releases/{id}/media-link",
	"POST /api/admin/media-links/by-identifier",
	"GET /api/tags",
	"GET /api/makers",
	"GET /api/catalog/search",
	"GET /api/search",
	"GET /api/detail/{source}/{id...}",
	"POST /api/resolve/{source}/{id...}",
	"GET /api/admin/sources",
	"GET /api/admin/jobs",
	"GET /api/admin/crawl-policies",
	"POST /api/admin/media-assets",
	"GET /api/admin/identity-candidates",
	"PUT /api/admin/sources/{id}/crawl-policy",
	"GET /api/admin/conflicts",
	"POST /api/admin/conflicts/{id}/resolve",
	"POST /api/admin/sources",
	"POST /api/admin/sources/{id}/jobs",
	"POST /api/events",
}

var (
	restWildcard = regexp.MustCompile(`\{[^}]+\.\.\.\}`)
	wildcard     = regexp.MustCompile(`\{[^}]+\}`)
)

// samplePath turns a pattern path into a concrete request path.
func samplePath(p string) string {
	p = restWildcard.ReplaceAllString(p, "a/b")
	return wildcard.ReplaceAllString(p, "1")
}

func TestRoutesRegistered(t *testing.T) {
	if len(expectedRoutes) != 38 {
		t.Fatalf("expected route list has %d entries, want 38", len(expectedRoutes))
	}
	mux := http.NewServeMux()
	Register(mux, nil, nil)

	srv := &Server{}
	got := map[string]bool{}
	for _, rt := range srv.routes() {
		if got[rt.pattern] {
			t.Errorf("duplicate route %q", rt.pattern)
		}
		got[rt.pattern] = true
	}
	if len(got) != len(expectedRoutes) {
		t.Errorf("route table has %d routes, want %d", len(got), len(expectedRoutes))
	}

	for _, want := range expectedRoutes {
		if !got[want] {
			t.Errorf("route %q missing from route table", want)
		}
		method, path, _ := strings.Cut(want, " ")
		req := httptest.NewRequest(method, samplePath(path), nil)
		_, pattern := mux.Handler(req)
		if pattern != want {
			t.Errorf("%s %s resolved to pattern %q, want %q", method, req.URL.Path, pattern, want)
		}
	}
}

func TestAdminRoutesRequireToken(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "secret")
	h := New(nil, nil)
	for _, want := range expectedRoutes {
		method, path, _ := strings.Cut(want, " ")
		if !strings.HasPrefix(path, "/api/admin/") {
			continue
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, samplePath(path), nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: status %d, want 401", want, rec.Code)
		}
	}
}
