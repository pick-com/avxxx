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
	"GET /api/me/follows",
	"PUT /api/me/follows/{performer_slug}",
	"DELETE /api/me/follows/{performer_slug}",
	"GET /api/me/following/feed",
	"GET /api/me/reservations",
	"PUT /api/me/reservations/{release_slug}",
	"DELETE /api/me/reservations/{release_slug}",
	"GET /api/me/watch-later",
	"PUT /api/me/watch-later/{release_slug}",
	"DELETE /api/me/watch-later/{release_slug}",
	"POST /api/me/watch-later/reorder",
	"GET /api/me/settings",
	"PATCH /api/me/settings",
	"DELETE /api/me/watch-history/{release_slug}",
	"DELETE /api/me/watch-history",
	"GET /api/me/notifications",
	"GET /api/me/notifications/unread-count",
	"POST /api/me/notifications/read",
	"GET /api/push/vapid-public-key",
	"POST /api/me/push-subscriptions",
	"DELETE /api/me/push-subscriptions",
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
	"POST /api/admin/identity-candidates/generate",
	"POST /api/admin/identity-candidates/{id}/merge",
	"POST /api/admin/identity-candidates/{id}/not-duplicate",
	"POST /api/admin/merges/{id}/undo",
	"PUT /api/admin/sources/{id}/crawl-policy",
	"GET /api/admin/conflicts",
	"POST /api/admin/conflicts/{id}/resolve",
	"POST /api/admin/sources",
	"POST /api/admin/sources/{id}/jobs",
	"GET /api/admin/releases",
	"GET /api/admin/releases/{id}",
	"PATCH /api/admin/releases/{id}",
	"POST /api/admin/releases/{id}/status",
	"DELETE /api/admin/releases/{id}/overrides/{field}",
	"GET /api/admin/releases/{id}/sources",
	"POST /api/admin/releases/{id}/sources",
	"PATCH /api/admin/releases/{id}/sources/{source_id}",
	"POST /api/admin/releases/{id}/sources/reorder",
	"POST /api/admin/releases/{id}/sources/promote",
	"POST /api/admin/releases/{id}/sources/{source_id}/disable",
	"POST /api/admin/releases/{id}/sources/{source_id}/enable",
	"PUT /api/admin/releases/{id}/performers/{performer_id}",
	"DELETE /api/admin/releases/{id}/performers/{performer_id}",
	"POST /api/events",
	"GET /api/admin/audit/issues",
	"POST /api/admin/audit/issues/{id}/dismiss",
	"POST /api/admin/audit/run",
	"GET /api/admin/dashboard",
	"GET /api/i18n/ui/{lang}",
	"GET /api/admin/i18n/translations",
	"GET /api/admin/i18n/stats",
	"POST /api/admin/i18n/translations/mark-stale",
	"PUT /api/admin/i18n/translations/{entity_type}/{entity_id}/{field}/{lang}",
	"POST /api/admin/i18n/translations/{entity_type}/{entity_id}/{field}/{lang}/submit",
	"POST /api/admin/i18n/translations/{entity_type}/{entity_id}/{field}/{lang}/review",
	"GET /api/admin/i18n/glossary",
	"POST /api/admin/i18n/glossary",
	"PUT /api/admin/i18n/glossary/{id}",
	"DELETE /api/admin/i18n/glossary/{id}",
	"GET /api/admin/i18n/ui",
	"PUT /api/admin/i18n/ui/{lang}/{key}",
	"GET /api/admin/playback-sources/health",
	"GET /api/releases/{slug}/danmaku",
	"POST /api/releases/{slug}/danmaku",
	"POST /api/danmaku/{id}/report",
	"GET /api/releases/{slug}/rating",
	"PUT /api/releases/{slug}/rating",
	"GET /api/releases/{slug}/reviews",
	"POST /api/releases/{slug}/reviews",
	"PATCH /api/reviews/{id}",
	"DELETE /api/reviews/{id}",
	"PUT /api/reviews/{id}/like",
	"DELETE /api/reviews/{id}/like",
	"POST /api/reviews/{id}/report",
	"POST /api/reports",
	"GET /api/admin/moderation/queue",
	"POST /api/admin/moderation/danmaku/{id}/action",
	"POST /api/admin/moderation/reviews/{id}/action",
	"POST /api/admin/moderation/ratings/{id}/action",
	"POST /api/admin/moderation/reports/{id}/resolve",
	"POST /api/admin/moderation/rating-floods/remove",
	"GET /api/admin/moderation/words",
	"POST /api/admin/moderation/words",
	"DELETE /api/admin/moderation/words/{word}",
	"DELETE /api/admin/moderation/mutes/{user_id}",
	"GET /api/admin/releases/{id}/curves",
	"GET /api/recs",
	"GET /api/trending",
	"GET /api/trending/performers",
	"GET /api/admin/similarity-weights",
	"PUT /api/admin/similarity-weights",
	"POST /api/admin/recs/rebuild",
	"GET /api/search/hot",
	"GET /api/search/suggest",
	"GET /api/admin/search/zero-results",
	"GET /api/admin/search/stats",
	"GET /api/admin/search/synonyms",
	"POST /api/admin/search/synonyms",
	"PUT /api/admin/search/synonyms/{id}",
	"DELETE /api/admin/search/synonyms/{id}",
	"GET /api/admin/search/redirects",
	"POST /api/admin/search/redirects",
	"PUT /api/admin/search/redirects/{id}",
	"DELETE /api/admin/search/redirects/{id}",
	"GET /api/admin/search/blocklist",
	"POST /api/admin/search/blocklist",
	"DELETE /api/admin/search/blocklist/{id}",
	"POST /api/admin/search/terms/alias",
	"POST /api/admin/search/terms/english-title",
	"GET /api/admin/performers",
	"GET /api/admin/performers/{id}",
	"PATCH /api/admin/performers/{id}",
	"DELETE /api/admin/performers/{id}/overrides/{field}",
	"POST /api/admin/performers/{id}/aliases",
	"DELETE /api/admin/performers/{id}/aliases/{alias_id}",
	"POST /api/admin/performers/{id}/status",
	"GET /api/admin/performer-requests",
	"POST /api/admin/performer-requests/{id}/approve",
	"POST /api/admin/performer-requests/{id}/reject",
	"POST /api/admin/similarity-weights/preview",
	"POST /api/performer-requests",
	"GET /api/admin/media-links",
	"POST /api/admin/media-links/run",
	"POST /api/admin/media-links/{release_id}/retry",
	"POST /api/admin/media-links/{release_id}/choose",
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
	listed := map[string]bool{}
	for _, rt := range expectedRoutes {
		if listed[rt] {
			t.Errorf("expectedRoutes lists %q twice", rt)
		}
		listed[rt] = true
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
