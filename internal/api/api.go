// Package api wires the HTTP routes of the avxxx API, organized by domain.
package api

import (
	"avxxx/internal/clientip"
	"avxxx/internal/community"
	"avxxx/internal/media"
	"avxxx/internal/medialink"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Server holds the dependencies shared by all route handlers.
type Server struct {
	store *store.Store
	media *media.Handler
	// accounts is the subset of the store used by the account and health
	// handlers, as an interface so they can be tested without a database.
	accounts accountStore
	// identity is the subset of the store used by identity review and merge
	// redirects; nil disables redirects (tests without a database).
	identity identityStore
	social   socialStore // follows, reservations, watch-later, settings, notifications
	// playlists backs 收藏夹 and the watch-later auto-remove rule (playlists.go).
	playlists libraryStore
	// audit backs the admin audit and dashboard routes (admin_audit.go).
	audit auditAdmin
	// i18n backs content translations, glossary and UI strings (i18n.go,
	// admin_i18n.go); nil serves catalog routes untranslated.
	i18n i18nStore
	// community backs danmaku, ratings, reviews and reports (community.go);
	// moderation backs the AdminOps moderation routes (moderation.go).
	community  communityStore
	moderation moderationStore
	// limiter enforces the danmaku / review posting limits (PRD 5.5).
	limiter community.Limiter
	// clientIP derives the client address for per-address limits: the TCP
	// peer, or X-Forwarded-For behind a TRUSTED_PROXIES proxy.
	clientIP clientip.Resolver
	// push stores Web Push subscriptions (push.go); pushCfg is read from env.
	push    pushStore
	pushCfg pushConfig
	// editor backs the admin video editor and conflict resolution
	// (admin_release.go, admin.go).
	editor releaseEditor
	// recs backs /api/recs and /api/trending, recsAdmin the similarity
	// weights and rebuild routes (recs.go).
	recs      recsStore
	recsAdmin recsAdmin
	// search backs /api/catalog/search, /api/search/hot|suggest and the
	// AdminSearch routes (search.go).
	search searchStore
	// performers backs the admin performer editor, performer requests and
	// the similarity weights preview (admin_performer.go).
	performerEd performerAdmin
	// governance backs source license review, phased jobs, checkpoints and
	// field priority policies (admin_governance.go).
	governance governanceStore
	// playback backs GET /api/releases/{slug}/playback; linker searches a
	// provider for a missing mapping on demand (MEDIA_LINK_ON_DEMAND,
	// default true) and linkAdmin backs /api/admin/media-links (media.go,
	// admin_media_links.go).
	playback     playbackLinks
	linker       *medialink.Linker
	linkOnDemand bool
	linkAdmin    mediaLinkAdmin
	// seriesSt backs /api/series, evidence the public source evidence
	// routes (series.go); reports the admin completeness report (reports.go).
	seriesSt seriesStore
	evidence evidenceStore
	reports  reportStore
	// splitter backs the entity split routes (admin_split.go).
	splitter splitStore
	// sitemap backs /api/sitemap/* and the SEO check report (sitemap.go).
	sitemap sitemapStore
	// img backs catalog images: cover / portrait fields, /api/images/ and
	// the admin media routes (images.go). Without S3_* only the admin
	// routes exist and responses carry no image fields.
	img imageServing
	// home backs the home slots, 高分短片, announcements and the AdminOps
	// routes (home.go); homeCache holds their short-lived public responses.
	home      homeStore
	homeCache *respCache
	// adminToken is ADMIN_TOKEN, read once at construction. Empty disables
	// every admin route (they all return 401).
	adminToken string
}

type route struct {
	pattern string
	handler http.HandlerFunc
}

// New builds the API handler with every route registered on a fresh mux.
func New(s *store.Store, mediaHandler *media.Handler) http.Handler {
	mux := http.NewServeMux()
	Register(mux, s, mediaHandler)
	return mux
}

// Register adds every API route to mux.
func Register(mux *http.ServeMux, s *store.Store, mediaHandler *media.Handler) {
	srv := &Server{store: s, media: mediaHandler, adminToken: os.Getenv("ADMIN_TOKEN"), limiter: community.NewMemoryLimiter(), pushCfg: pushConfigFromEnv(os.Getenv)}
	// cmd/api refuses to start on an invalid TRUSTED_PROXIES; elsewhere an
	// invalid value falls back to the TCP peer (trust no proxy).
	if ips, err := clientip.FromEnv(os.Getenv); err != nil {
		slog.Error("client ip config, trusting no proxy", "err", err)
	} else {
		srv.clientIP = ips
	}
	if s != nil {
		srv.push = s
		srv.accounts = s
		srv.identity = s
		srv.social = s
		srv.playlists = s
		srv.audit = s
		srv.i18n = s
		srv.community = s
		srv.moderation = s
		srv.editor = s
		srv.recs = s
		srv.recsAdmin = s
		srv.search = s
		srv.performerEd = s
		srv.governance = s
		srv.playback = s
		srv.linkAdmin = s
		srv.sitemap = s
		if svc := mediaHandler.Service(); svc != nil {
			cfg, warn := medialink.ConfigFromEnv(os.Getenv)
			for _, w := range warn {
				slog.Warn("media link config", "warning", w)
			}
			srv.linker = &medialink.Linker{Store: s, Search: svc, Pacer: medialink.NewPacer(cfg.MinInterval), MaxWait: onDemandMaxWait}
		}
		srv.seriesSt = s
		srv.evidence = s
		srv.reports = s
		srv.splitter = s
		srv.setupImages(s)
		srv.home = s
		srv.homeCache = newRespCache()
	}
	srv.linkOnDemand = medialink.OnDemandFromEnv(os.Getenv("MEDIA_LINK_ON_DEMAND"))
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
}

// routes returns the full route table, grouped by domain.
func (srv *Server) routes() []route {
	var rs []route
	rs = append(rs, srv.healthRoutes()...)
	rs = append(rs, srv.accountRoutes()...)
	rs = append(rs, srv.socialRoutes()...)
	rs = append(rs, srv.notificationRoutes()...)
	rs = append(rs, srv.pushRoutes()...)
	rs = append(rs, srv.catalogRoutes()...)
	rs = append(rs, srv.mediaRoutes()...)
	rs = append(rs, srv.playbackRoutes()...)
	rs = append(rs, srv.adminRoutes()...)
	rs = append(rs, srv.adminGovernanceRoutes()...)
	rs = append(rs, srv.adminReleaseRoutes()...)
	rs = append(rs, srv.eventRoutes()...)
	rs = append(rs, srv.adminAuditRoutes()...)
	rs = append(rs, srv.adminI18nRoutes()...)
	rs = append(rs, srv.communityRoutes()...)
	rs = append(rs, srv.moderationRoutes()...)
	rs = append(rs, srv.adminCurveRoutes()...)
	rs = append(rs, srv.recsRoutes()...)
	rs = append(rs, srv.searchRoutes()...)
	rs = append(rs, srv.adminPerformerRoutes()...)
	rs = append(rs, srv.adminMediaLinkRoutes()...)
	rs = append(rs, srv.seriesRoutes()...)
	rs = append(rs, srv.adminReportRoutes()...)
	rs = append(rs, srv.adminSplitRoutes()...)
	rs = append(rs, srv.sitemapRoutes()...)
	rs = append(rs, srv.attributionRoutes()...)
	rs = append(rs, srv.imageRoutes()...)
	rs = append(rs, srv.playlistRoutes()...)
	rs = append(rs, srv.homeRoutes()...)
	rs = append(rs, srv.adminOpsRoutes()...)
	return srv.followMerges(rs)
}

// reply writes v as JSON, or maps e to a {"error": "..."} body with a status
// code chosen by errorStatus. 5xx bodies never carry internal error text.
func reply(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		// Logged once, on the request's access-log line (obs.Middleware).
		if !obs.RecordError(w, e) {
			slog.Error("request failed", "err", e)
		}
		code, msg := errorStatus(e)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{"error": msg})
		return
	}
	json.NewEncoder(w).Encode(v)
}

// admin guards h with the ADMIN_TOKEN bearer token, compared in constant time.
func (srv *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := bearerToken(r)
		if srv.adminToken == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(srv.adminToken)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// bearerToken returns the token from an "Authorization: Bearer <token>"
// header, or "" when the header is missing or uses another scheme.
func bearerToken(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

// userID resolves the bearer token on r to a user id. A missing, unknown or
// expired token is a 401; other store failures pass through (500).
func (srv *Server) userID(r *http.Request) (int64, error) {
	token := bearerToken(r)
	if token == "" {
		return 0, errUnauthorized
	}
	id, err := srv.accounts.UserIDByToken(r.Context(), token)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, unauthorized(err)
	}
	return id, err
}
