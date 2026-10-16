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
	"PUT /api/admin/sources/{id}/license",
	"GET /api/admin/sources/{id}/checkpoints",
	"GET /api/admin/field-policies",
	"PUT /api/admin/field-policies",
	"DELETE /api/admin/field-policies/{entity_type}/{field}/{source_id}",
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
	"GET /api/admin/site-metrics",
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
	"GET /api/series",
	"GET /api/series/{slug}",
	"GET /api/series/{slug}/releases",
	"GET /api/releases/{slug}/evidence",
	"GET /api/performers/{slug}/evidence",
	"GET /api/admin/reports/completeness",
	"GET /api/admin/performers/{id}/split-preview",
	"POST /api/admin/performers/{id}/split",
	"GET /api/admin/releases/{id}/split-preview",
	"POST /api/admin/releases/{id}/split",
	"POST /api/admin/splits/{event_id}/undo",
	// Sitemap sources and the SEO check report (sitemap.go).
	"GET /api/sitemap/releases",
	"GET /api/sitemap/performers",
	"GET /api/sitemap/tags",
	"GET /api/sitemap/counts",
	"POST /api/admin/seo/report",
	"GET /api/admin/seo/report",
	// Source credits (attributions.go).
	"GET /api/attributions",
	// Catalog images (images.go).
	"GET /api/images/{key...}",
	"GET /api/admin/media",
	"GET /api/admin/media/sources",
	"POST /api/admin/media/jobs",
	"POST /api/admin/media/{id}/suppress",
	"POST /api/admin/media/{id}/restore",
	"PUT /api/admin/sources/{id}/images",
	"GET /api/me/playlists",
	"POST /api/me/playlists",
	"GET /api/me/playlists/{id}",
	"PATCH /api/me/playlists/{id}",
	"DELETE /api/me/playlists/{id}",
	"POST /api/me/playlists/{id}/share-token",
	"PUT /api/me/playlists/{id}/items/{release_slug}",
	"DELETE /api/me/playlists/{id}/items/{release_slug}",
	"POST /api/me/watch-later/{release_slug}/move",
	"GET /api/playlists/{token}",
	// Home curation, 高分短片 and announcements (home.go).
	"GET /api/home/slots",
	"GET /api/home/top-rated-short",
	"GET /api/announcements",
	"GET /api/me/announcement-dismissals",
	"PUT /api/me/announcement-dismissals/{id}",
	"GET /api/admin/home-slots",
	"POST /api/admin/home-slots",
	"PUT /api/admin/home-slots/{id}",
	"DELETE /api/admin/home-slots/{id}",
	"POST /api/admin/home-slots/reorder",
	"GET /api/admin/home-slots/preview",
	"GET /api/admin/announcements",
	"POST /api/admin/announcements",
	"PUT /api/admin/announcements/{id}",
	"DELETE /api/admin/announcements/{id}",
	// Discover, search and performers (catalog.go, search.go).
	"GET /api/upcoming",
	"GET /api/catalog/facets",
	"GET /api/me/search-history",
	"POST /api/me/search-history",
	"DELETE /api/me/search-history",
	// Account and auth (auth.go).
	"POST /api/auth/logout",
	"POST /api/auth/password/forgot",
	"POST /api/auth/password/reset",
	"GET /api/auth/providers",
	"POST /api/auth/oauth/{provider}/start",
	"GET /api/auth/oauth/{provider}/callback",
	"POST /api/auth/oauth/{provider}/callback",
	"POST /api/auth/oauth/exchange",
	"PATCH /api/me/profile",
	"POST /api/me/password",
	"GET /api/me/sessions",
	"DELETE /api/me/sessions/{id}",
	"POST /api/me/sessions/revoke-others",
	"POST /api/me/terms",
	"GET /api/me/identities",
	"DELETE /api/me/identities/{provider}",
	"GET /api/me/export",
	"POST /api/me/deletion",
	"GET /api/me/interests",
	"PUT /api/me/interests",
	"POST /api/me/adult-confirm",
	// Consent records (consent.go).
	"POST /api/consent",
	"GET /api/me/consent",
	// Legal documents (legal.go).
	"GET /api/legal/terms/current-version",
	"GET /api/legal/{kind}",
	"GET /api/legal/{kind}/versions",
	"GET /api/admin/legal",
	"POST /api/admin/legal",
	"GET /api/admin/legal/{id}",
	"PUT /api/admin/legal/{id}",
	"POST /api/admin/legal/{id}/publish",
	"GET /api/admin/legal/{id}/diff",
	// Feedback (feedback.go).
	"POST /api/feedback",
	"GET /api/admin/feedback",
	"POST /api/admin/feedback/{id}/status",
	// Copyright takedowns (takedowns.go).
	"POST /api/takedowns",
	"GET /api/takedowns/{ticket}",
	"POST /api/takedowns/{ticket}/counter",
	"GET /api/admin/takedowns",
	"GET /api/admin/takedowns/{id}",
	"POST /api/admin/takedowns/{id}/status",
	"POST /api/admin/takedowns/{id}/actions",
	"POST /api/admin/takedowns/{id}/restore",
	// 赞 / 踩 and comments (interactions.go, moderation.go).
	"POST /api/admin/moderation/comments/{id}/action",
	"GET /api/releases/{slug}/reaction",
	"PUT /api/releases/{slug}/reaction",
	"DELETE /api/releases/{slug}/reaction",
	"GET /api/releases/{slug}/comments",
	"POST /api/releases/{slug}/comments",
	"GET /api/comments/{id}/replies",
	"DELETE /api/comments/{id}",
	"PUT /api/comments/{id}/like",
	"DELETE /api/comments/{id}/like",
	"POST /api/comments/{id}/report",
	// 观看统计 (social.go, watchstats.go).
	"GET /api/me/watch-stats",
	// Admin accounts (admin_auth.go, admin_users.go).
	"POST /api/admin-auth/login",
	"POST /api/admin-auth/verify",
	"POST /api/admin-auth/enroll/info",
	"POST /api/admin-auth/enroll",
	"GET /api/admin/session",
	"POST /api/admin/session/logout",
	"POST /api/admin/session/recovery-codes",
	"GET /api/admin/roles",
	"GET /api/admin/admin-users",
	"POST /api/admin/admin-users",
	"POST /api/admin/admin-users/{id}/invite",
	"POST /api/admin/admin-users/{id}/disable",
	"POST /api/admin/admin-users/{id}/enable",
	"PUT /api/admin/admin-users/{id}/roles",
	"POST /api/admin/admin-users/{id}/reset-2fa",
	"GET /api/admin/approvals",
	"POST /api/admin/approvals/{id}/approve",
	"POST /api/admin/approvals/{id}/reject",
	"GET /api/admin/audit-log",
	"GET /api/admin/site-users",
	"GET /api/admin/site-users/{id}",
	"POST /api/admin/site-users/{id}/ban",
	"POST /api/admin/site-users/{id}/unban",
	"GET /api/admin/service-tokens",
	"POST /api/admin/service-tokens",
	"POST /api/admin/service-tokens/{id}/revoke",
	"GET /api/admin/jobs/queue",
	"GET /api/admin/jobs/runs",
	"POST /api/admin/jobs/{id}/retry",
	"POST /api/admin/jobs/run",
	"GET /api/admin/releases/{id}/rights-proofs",
	"POST /api/admin/releases/{id}/rights-proofs",
	"DELETE /api/admin/releases/{id}/rights-proofs/{proof_id}",
	"POST /api/admin/search/terms/release",
	// Site-entry 18+ gate (agegate.go).
	"POST /api/age-gate",
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
