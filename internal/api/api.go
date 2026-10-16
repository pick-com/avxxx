// Package api wires the HTTP routes of the avxxx API, organized by domain.
package api

import (
	"avxxx/internal/media"
	"avxxx/internal/store"
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
	srv := &Server{store: s, media: mediaHandler}
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
}

// routes returns the full route table, grouped by domain.
func (srv *Server) routes() []route {
	var rs []route
	rs = append(rs, srv.healthRoutes()...)
	rs = append(rs, srv.accountRoutes()...)
	rs = append(rs, srv.catalogRoutes()...)
	rs = append(rs, srv.mediaRoutes()...)
	rs = append(rs, srv.playbackRoutes()...)
	rs = append(rs, srv.adminRoutes()...)
	return rs
}

// reply writes v as JSON, or maps e to a JSON error with a status code.
func reply(w http.ResponseWriter, v any, e error) {
	w.Header().Set("Content-Type", "application/json")
	if e != nil {
		log.Print(e)
		code := http.StatusInternalServerError
		if errors.Is(e, pgx.ErrNoRows) {
			code = http.StatusNotFound
		}
		if strings.Contains(e.Error(), "source disabled") {
			code = http.StatusConflict
		}
		if strings.Contains(e.Error(), "duplicate key") {
			code = http.StatusConflict
		}
		http.Error(w, `{"error":"request failed"}`, code)
		return
	}
	json.NewEncoder(w).Encode(v)
}

// admin guards h with the ADMIN_TOKEN bearer token.
func admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("ADMIN_TOKEN")
		if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		h(w, r)
	}
}

func bearerToken(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}

// userID resolves the bearer token on r to a user id.
func (srv *Server) userID(r *http.Request) (int64, error) {
	return srv.store.UserIDByToken(r.Context(), bearerToken(r))
}
