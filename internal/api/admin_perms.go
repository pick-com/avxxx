package api

// Admin route permissions (artboard AdminUsers, tech plan 3.6). Every
// /api/admin route pattern maps to a route group of the role matrix
// (internal/adminauth/roles.go) and the access it needs. srv.admin looks the
// pattern up on every request (r.Pattern), so enforcement is central and a
// handler cannot forget it.
//
// A pattern missing from this table is allowed for super_admin only, and
// TestAdminRoutePermissionsCoverEveryRoute fails listing it so whoever adds
// the route assigns it here.

import (
	"avxxx/internal/adminauth"
	"strings"
)

// routePerm is what a route needs.
type routePerm struct {
	Group  adminauth.Group
	Access adminauth.Access
	// Dual: the request is stored and runs only after a different admin
	// approves it (admin_approvals.go). Break-glass bypasses it (logged).
	Dual bool
	// LogReads: GETs are written to the op log too (personal data, the log itself).
	LogReads bool
}

func rd(g adminauth.Group) routePerm { return routePerm{Group: g, Access: adminauth.Read} }
func wr(g adminauth.Group) routePerm { return routePerm{Group: g, Access: adminauth.Write} }
func dual(g adminauth.Group) routePerm {
	return routePerm{Group: g, Access: adminauth.Write, Dual: true}
}
func pii(p routePerm) routePerm { p.LogReads = true; return p }

const (
	gDashboard  = adminauth.GroupDashboard
	gCatalog    = adminauth.GroupCatalog
	gQuality    = adminauth.GroupQuality
	gIngest     = adminauth.GroupIngest
	gModeration = adminauth.GroupModeration
	gSiteUsers  = adminauth.GroupSiteUsers
	gCompliance = adminauth.GroupCompliance
	gLegal      = adminauth.GroupLegal
	gOps        = adminauth.GroupOps
	gSearch     = adminauth.GroupSearch
	gI18n       = adminauth.GroupI18n
	gAdminUsers = adminauth.GroupAdminUsers
	gAuditLog   = adminauth.GroupAuditLog
	gSelf       = adminauth.GroupSelf
)

// adminRoutePerms maps every registered "METHOD /api/admin/..." pattern.
var adminRoutePerms = map[string]routePerm{
	// Session, role matrix and the approvals queue: every signed-in admin.
	// Approving re-checks the stored route's group (admin_approvals.go).
	"GET /api/admin/session":                     rd(gSelf),
	"POST /api/admin/session/logout":             wr(gSelf),
	"POST /api/admin/session/recovery-codes":     wr(gSelf),
	"GET /api/admin/roles":                       rd(gSelf),
	"GET /api/admin/approvals":                   rd(gSelf),
	"POST /api/admin/approvals/{id}/approve":     wr(gSelf),
	"POST /api/admin/approvals/{id}/reject":      wr(gSelf),
	"GET /api/admin/admin-users":                 rd(gAdminUsers),
	"POST /api/admin/admin-users":                wr(gAdminUsers),
	"POST /api/admin/admin-users/{id}/invite":    wr(gAdminUsers),
	"POST /api/admin/admin-users/{id}/disable":   wr(gAdminUsers),
	"PUT /api/admin/admin-users/{id}/roles":      dual(gAdminUsers),
	"POST /api/admin/admin-users/{id}/enable":    dual(gAdminUsers),
	"POST /api/admin/admin-users/{id}/reset-2fa": dual(gAdminUsers),
	"GET /api/admin/audit-log":                   pii(rd(gAuditLog)),
	// Service tokens (service_tokens.go): super_admin only, like accounts.
	"GET /api/admin/service-tokens":              rd(gAdminUsers),
	"POST /api/admin/service-tokens":             wr(gAdminUsers),
	"POST /api/admin/service-tokens/{id}/revoke": wr(gAdminUsers),
	"GET /api/admin/site-users":                  pii(rd(gSiteUsers)),
	"GET /api/admin/site-users/{id}":             pii(rd(gSiteUsers)),
	"POST /api/admin/site-users/{id}/ban":        wr(gSiteUsers),
	"POST /api/admin/site-users/{id}/unban":      wr(gSiteUsers),

	// 总览与报告
	"GET /api/admin/dashboard":               rd(gDashboard),
	"GET /api/admin/playback-sources/health": rd(gDashboard),
	"GET /api/admin/reports/completeness":    rd(gDashboard),
	"GET /api/admin/seo/report":              rd(gDashboard),

	// 视频与演员、重复与归并、字段冲突、拆分
	"GET /api/admin/releases":                                   rd(gCatalog),
	"GET /api/admin/releases/{id}":                              rd(gCatalog),
	"PATCH /api/admin/releases/{id}":                            wr(gCatalog),
	"POST /api/admin/releases/{id}/status":                      wr(gCatalog),
	"DELETE /api/admin/releases/{id}/overrides/{field}":         wr(gCatalog),
	"GET /api/admin/releases/{id}/sources":                      rd(gCatalog),
	"POST /api/admin/releases/{id}/sources":                     wr(gCatalog),
	"PATCH /api/admin/releases/{id}/sources/{source_id}":        wr(gCatalog),
	"POST /api/admin/releases/{id}/sources/reorder":             wr(gCatalog),
	"POST /api/admin/releases/{id}/sources/promote":             wr(gCatalog),
	"POST /api/admin/releases/{id}/sources/{source_id}/disable": wr(gCatalog),
	"POST /api/admin/releases/{id}/sources/{source_id}/enable":  wr(gCatalog),
	"PUT /api/admin/releases/{id}/performers/{performer_id}":    wr(gCatalog),
	"DELETE /api/admin/releases/{id}/performers/{performer_id}": wr(gCatalog),
	"GET /api/admin/releases/{id}/curves":                       rd(gCatalog),
	"POST /api/admin/releases/{id}/media-link":                  wr(gCatalog),
	"POST /api/admin/media-links/by-identifier":                 wr(gCatalog),
	"POST /api/admin/media-assets":                              wr(gCatalog),
	"GET /api/admin/performers":                                 rd(gCatalog),
	"GET /api/admin/performers/{id}":                            rd(gCatalog),
	"PATCH /api/admin/performers/{id}":                          wr(gCatalog),
	"DELETE /api/admin/performers/{id}/overrides/{field}":       wr(gCatalog),
	"POST /api/admin/performers/{id}/aliases":                   wr(gCatalog),
	"DELETE /api/admin/performers/{id}/aliases/{alias_id}":      wr(gCatalog),
	"POST /api/admin/performers/{id}/status":                    wr(gCatalog),
	"GET /api/admin/performer-requests":                         rd(gCatalog),
	"POST /api/admin/performer-requests/{id}/approve":           wr(gCatalog),
	"POST /api/admin/performer-requests/{id}/reject":            wr(gCatalog),
	"GET /api/admin/similarity-weights":                         rd(gCatalog),
	"PUT /api/admin/similarity-weights":                         wr(gCatalog),
	"POST /api/admin/similarity-weights/preview":                rd(gCatalog),
	"GET /api/admin/identity-candidates":                        rd(gCatalog),
	"POST /api/admin/identity-candidates/generate":              wr(gCatalog),
	"POST /api/admin/identity-candidates/{id}/merge":            wr(gCatalog),
	"POST /api/admin/identity-candidates/{id}/not-duplicate":    wr(gCatalog),
	"POST /api/admin/merges/{id}/undo":                          wr(gCatalog),
	"GET /api/admin/conflicts":                                  rd(gCatalog),
	"POST /api/admin/conflicts/{id}/resolve":                    wr(gCatalog),
	"GET /api/admin/performers/{id}/split-preview":              rd(gCatalog),
	"POST /api/admin/performers/{id}/split":                     wr(gCatalog),
	"GET /api/admin/releases/{id}/split-preview":                rd(gCatalog),
	"POST /api/admin/releases/{id}/split":                       wr(gCatalog),
	"POST /api/admin/splits/{event_id}/undo":                    wr(gCatalog),

	// 数据质量
	"GET /api/admin/audit/issues":               rd(gQuality),
	"POST /api/admin/audit/issues/{id}/dismiss": wr(gQuality),
	"POST /api/admin/audit/run":                 wr(gQuality),

	// 入库与数据治理
	"GET /api/admin/sources":                                             rd(gIngest),
	"POST /api/admin/sources":                                            wr(gIngest),
	"GET /api/admin/jobs":                                                rd(gIngest),
	"GET /api/admin/jobs/queue":                                          rd(gIngest),
	"GET /api/admin/jobs/runs":                                           rd(gIngest),
	"POST /api/admin/jobs/{id}/retry":                                    wr(gIngest),
	"POST /api/admin/jobs/run":                                           wr(gIngest),
	"GET /api/admin/crawl-policies":                                      rd(gIngest),
	"PUT /api/admin/sources/{id}/crawl-policy":                           wr(gIngest),
	"POST /api/admin/sources/{id}/jobs":                                  wr(gIngest),
	"PUT /api/admin/sources/{id}/license":                                wr(gIngest),
	"GET /api/admin/sources/{id}/checkpoints":                            rd(gIngest),
	"PUT /api/admin/sources/{id}/images":                                 wr(gIngest),
	"GET /api/admin/field-policies":                                      rd(gIngest),
	"PUT /api/admin/field-policies":                                      wr(gIngest),
	"DELETE /api/admin/field-policies/{entity_type}/{field}/{source_id}": wr(gIngest),
	"GET /api/admin/media-links":                                         rd(gIngest),
	"POST /api/admin/media-links/run":                                    wr(gIngest),
	"POST /api/admin/media-links/{release_id}/retry":                     wr(gIngest),
	"POST /api/admin/media-links/{release_id}/choose":                    wr(gIngest),
	"GET /api/admin/media":                                               rd(gIngest),
	"GET /api/admin/media/sources":                                       rd(gIngest),
	"POST /api/admin/media/jobs":                                         wr(gIngest),
	"POST /api/admin/media/{id}/suppress":                                wr(gIngest),
	"POST /api/admin/media/{id}/restore":                                 wr(gIngest),
	"POST /api/admin/recs/rebuild":                                       wr(gIngest),
	"POST /api/admin/seo/report":                                         wr(gIngest),

	// 举报与审核 (rating-flood removal deletes ratings in bulk: dual approval)
	"GET /api/admin/moderation/queue":                 rd(gModeration),
	"POST /api/admin/moderation/danmaku/{id}/action":  wr(gModeration),
	"POST /api/admin/moderation/reviews/{id}/action":  wr(gModeration),
	"POST /api/admin/moderation/ratings/{id}/action":  wr(gModeration),
	"POST /api/admin/moderation/comments/{id}/action": wr(gModeration),
	"POST /api/admin/moderation/reports/{id}/resolve": wr(gModeration),
	"POST /api/admin/moderation/rating-floods/remove": dual(gModeration),
	"GET /api/admin/moderation/words":                 rd(gModeration),
	"POST /api/admin/moderation/words":                wr(gModeration),
	"DELETE /api/admin/moderation/words/{word}":       wr(gModeration),
	"DELETE /api/admin/moderation/mutes/{user_id}":    wr(gModeration),

	// 版权投诉与反馈 (PRD 11: 审核 handles 举报与版权); 法律文本
	"GET /api/admin/takedowns":               pii(rd(gCompliance)),
	"GET /api/admin/takedowns/{id}":          pii(rd(gCompliance)),
	"POST /api/admin/takedowns/{id}/status":  wr(gCompliance),
	"POST /api/admin/takedowns/{id}/actions": wr(gCompliance),
	"POST /api/admin/takedowns/{id}/restore": wr(gCompliance),
	"GET /api/admin/feedback":                pii(rd(gCompliance)),
	"POST /api/admin/feedback/{id}/status":   wr(gCompliance),
	"GET /api/admin/legal":                   rd(gLegal),
	"POST /api/admin/legal":                  wr(gLegal),
	"GET /api/admin/legal/{id}":              rd(gLegal),
	"PUT /api/admin/legal/{id}":              wr(gLegal),
	"POST /api/admin/legal/{id}/publish":     wr(gLegal),
	"GET /api/admin/legal/{id}/diff":         rd(gLegal),

	// 首页排期与公告
	"GET /api/admin/home-slots":            rd(gOps),
	"POST /api/admin/home-slots":           wr(gOps),
	"PUT /api/admin/home-slots/{id}":       wr(gOps),
	"DELETE /api/admin/home-slots/{id}":    wr(gOps),
	"POST /api/admin/home-slots/reorder":   wr(gOps),
	"GET /api/admin/home-slots/preview":    rd(gOps),
	"GET /api/admin/announcements":         rd(gOps),
	"POST /api/admin/announcements":        wr(gOps),
	"PUT /api/admin/announcements/{id}":    wr(gOps),
	"DELETE /api/admin/announcements/{id}": wr(gOps),

	// 搜索词情报
	"GET /api/admin/search/zero-results":         rd(gSearch),
	"GET /api/admin/search/stats":                rd(gSearch),
	"GET /api/admin/search/synonyms":             rd(gSearch),
	"POST /api/admin/search/synonyms":            wr(gSearch),
	"PUT /api/admin/search/synonyms/{id}":        wr(gSearch),
	"DELETE /api/admin/search/synonyms/{id}":     wr(gSearch),
	"GET /api/admin/search/redirects":            rd(gSearch),
	"POST /api/admin/search/redirects":           wr(gSearch),
	"PUT /api/admin/search/redirects/{id}":       wr(gSearch),
	"DELETE /api/admin/search/redirects/{id}":    wr(gSearch),
	"GET /api/admin/search/blocklist":            rd(gSearch),
	"POST /api/admin/search/blocklist":           wr(gSearch),
	"DELETE /api/admin/search/blocklist/{id}":    wr(gSearch),
	"POST /api/admin/search/terms/alias":         wr(gSearch),
	"POST /api/admin/search/terms/english-title": wr(gSearch),

	// 翻译管理
	"GET /api/admin/i18n/translations":             rd(gI18n),
	"GET /api/admin/i18n/stats":                    rd(gI18n),
	"POST /api/admin/i18n/translations/mark-stale": wr(gI18n),
	"PUT " + translationPath:                       wr(gI18n),
	"POST " + translationPath + "/submit":          wr(gI18n),
	"POST " + translationPath + "/review":          wr(gI18n),
	"GET /api/admin/i18n/glossary":                 rd(gI18n),
	"POST /api/admin/i18n/glossary":                wr(gI18n),
	"PUT /api/admin/i18n/glossary/{id}":            wr(gI18n),
	"DELETE /api/admin/i18n/glossary/{id}":         wr(gI18n),
	"GET /api/admin/i18n/ui":                       rd(gI18n),
	"PUT /api/admin/i18n/ui/{lang}/{key}":          wr(gI18n),
}

// adminRoutePerm returns the permission of a registered pattern; ok is false
// for a pattern missing from the table (super_admin only).
func adminRoutePerm(pattern string) (routePerm, bool) {
	p, ok := adminRoutePerms[pattern]
	return p, ok
}

// isAdminPattern reports whether a route pattern is under /api/admin/.
func isAdminPattern(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	return strings.HasPrefix(path, "/api/admin/")
}

// routeEntity derives the op-log entity from a pattern: the segment before
// the first wildcard and that wildcard's name ("/api/admin/releases/{id}/status"
// → "releases", "id").
func routeEntity(pattern string) (entityType, wildcard string) {
	_, path, _ := strings.Cut(pattern, " ")
	segs := strings.Split(strings.TrimPrefix(path, "/api/admin/"), "/")
	for i, s := range segs {
		if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
			name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}"), "...")
			if i == 0 {
				return "", name
			}
			return segs[i-1], name
		}
	}
	if len(segs) > 0 {
		return segs[0], ""
	}
	return "", ""
}
