// Package api wires the HTTP routes of the avxxx API, organized by domain.
package api

import (
	"avxxx/internal/community"
	"avxxx/internal/media"
	"avxxx/internal/store"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
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
	// push stores Web Push subscriptions (push.go); pushCfg is read from env.
	push    pushStore
	pushCfg pushConfig
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
	if s != nil {
		srv.push = s
		srv.accounts = s
		srv.identity = s
		srv.social = s
		srv.audit = s
		srv.i18n = s
		srv.community = s
		srv.moderation = s
	}
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
	rs = append(rs, srv.eventRoutes()...)
	rs = append(rs, srv.adminAuditRoutes()...)
	rs = append(rs, srv.adminI18nRoutes()...)
	rs = append(rs, srv.communityRoutes()...)
	rs = append(rs, srv.moderationRoutes()...)
	rs = append(rs, srv.adminCurveRoutes()...)
	return srv.followMerges(rs)
}

// reply writes v as JSON, or maps e to a {"error": "..."} body with a status
// code chosen by errorStatus. 5xx bodies never carry internal error text.
func reply(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		log.Print(e)
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
