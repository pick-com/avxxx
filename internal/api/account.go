package api

import (
	"encoding/json"
	"errors"
	"net/http"
)

func (srv *Server) accountRoutes() []route {
	return []route{
		{"POST /api/auth/register", srv.register},
		{"POST /api/auth/login", srv.login},
		{"GET /api/auth/me", srv.me},
		{"GET /api/me/watch-history", srv.watchHistory},
		{"POST /api/me/watch-history", srv.saveWatch},
		{"GET /api/me/favorites", srv.favorites},
		{"POST /api/me/favorites", srv.setFavorite},
	}
}

func (srv *Server) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	v, e := srv.store.Register(r.Context(), in.Email, in.Password, in.DisplayName)
	reply(w, v, e)
}

func (srv *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	v, e := srv.store.Login(r.Context(), in.Email, in.Password)
	reply(w, v, e)
}

func (srv *Server) me(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.UserByToken(r.Context(), bearerToken(r))
	reply(w, v, e)
}

func (srv *Server) watchHistory(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, errors.New("unauthorized"))
		return
	}
	v, e := srv.store.WatchHistory(r.Context(), id)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) saveWatch(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, errors.New("unauthorized"))
		return
	}
	var in struct {
		Slug     string `json:"slug"`
		Progress int    `json:"progress_seconds"`
		Duration int    `json:"duration_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	e = srv.store.SaveWatch(r.Context(), id, in.Slug, in.Progress, in.Duration)
	reply(w, map[string]bool{"saved": e == nil}, e)
}

func (srv *Server) favorites(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, errors.New("unauthorized"))
		return
	}
	v, e := srv.store.Favorites(r.Context(), id)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, errors.New("unauthorized"))
		return
	}
	var in struct {
		Slug    string `json:"slug"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	e = srv.store.SetFavorite(r.Context(), id, in.Slug, in.Enabled)
	reply(w, map[string]bool{"saved": e == nil}, e)
}
