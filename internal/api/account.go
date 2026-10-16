package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// accountStore is the subset of *store.Store used by the account and health
// handlers.
type accountStore interface {
	Ping(ctx context.Context) error
	Register(ctx context.Context, email, password, displayName string) (map[string]any, error)
	Login(ctx context.Context, email, password string) (map[string]any, error)
	UserByToken(ctx context.Context, token string) (map[string]any, error)
	UserIDByToken(ctx context.Context, token string) (int64, error)
	WatchHistory(ctx context.Context, userID int64) ([]map[string]any, error)
	SaveWatch(ctx context.Context, userID int64, slug string, progress, duration int) error
	Favorites(ctx context.Context, userID int64) ([]map[string]any, error)
	SetFavorite(ctx context.Context, userID int64, slug string, enabled bool) error
}

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
		Email        string `json:"email"`
		Password     string `json:"password"`
		DisplayName  string `json:"display_name"`
		Remember     *bool  `json:"remember"`
		TermsVersion string `json:"terms_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	// Mirrors the validation in store.Register, which returns an untyped error.
	if len(strings.TrimSpace(in.Email)) < 3 || len(in.Password) < 8 {
		reply(w, nil, badRequest("email and password (min 8 characters) are required", nil))
		return
	}
	if srv.auth != nil {
		if !srv.allow(r, "register:"+srv.ip(r), limitRegisterIP) {
			reply(w, nil, tooManyRequests("too many requests"))
			return
		}
		v, e := srv.auth.RegisterSession(r.Context(), in.Email, in.Password, in.DisplayName, in.TermsVersion,
			store.SessionMeta{Remember: rememberOr(in.Remember), UserAgent: userAgent(r)})
		reply(w, v, e)
		return
	}
	v, e := srv.accounts.Register(r.Context(), in.Email, in.Password, in.DisplayName)
	reply(w, v, e)
}

func (srv *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Remember *bool  `json:"remember"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, nil, badRequest("invalid JSON body", err))
		return
	}
	if srv.auth != nil {
		// Failed-login throttling per address and per account (tech plan 3.6).
		email := strings.ToLower(strings.TrimSpace(in.Email))
		if !srv.allow(r, "login-ip:"+srv.ip(r), limitLoginIP) || !srv.allow(r, "login-email:"+email, limitLoginEmail) {
			reply(w, nil, tooManyRequests("too many login attempts, try again later"))
			return
		}
		v, e := srv.auth.LoginSession(r.Context(), in.Email, in.Password,
			store.SessionMeta{Remember: rememberOr(in.Remember), UserAgent: userAgent(r)})
		reply(w, v, e)
		return
	}
	v, e := srv.accounts.Login(r.Context(), in.Email, in.Password)
	reply(w, v, e)
}

func (srv *Server) me(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		reply(w, nil, errUnauthorized)
		return
	}
	v, e := srv.accounts.UserByToken(r.Context(), token)
	if e != nil {
		// store.UserByToken collapses every failure into errors.New("unauthorized").
		e = unauthorized(e)
	}
	reply(w, v, e)
}

func (srv *Server) watchHistory(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, e)
		return
	}
	v, e := srv.accounts.WatchHistory(r.Context(), id)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) saveWatch(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, e)
		return
	}
	var in struct {
		Slug     string `json:"slug"`
		Progress int    `json:"progress_seconds"`
		Duration int    `json:"duration_seconds"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	// 看完自动移出 (NavLater): applies whether or not history is paused.
	laterRemoved := false
	if srv.playlists != nil {
		var err error
		if laterRemoved, err = srv.playlists.LaterAutoRemove(r.Context(), id, in.Slug, in.Progress, in.Duration); err != nil {
			reply(w, nil, err)
			return
		}
	}
	// Paused history (user_settings.history_paused): accept but do not record.
	if srv.social != nil {
		paused, err := srv.social.HistoryPaused(r.Context(), id)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if paused {
			reply(w, map[string]bool{"saved": false, "paused": true, "later_removed": laterRemoved}, nil)
			return
		}
	}
	e = srv.accounts.SaveWatch(r.Context(), id, in.Slug, in.Progress, in.Duration)
	reply(w, map[string]bool{"saved": e == nil, "later_removed": laterRemoved}, e)
}

func (srv *Server) favorites(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, e)
		return
	}
	v, e := srv.accounts.Favorites(r.Context(), id)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) setFavorite(w http.ResponseWriter, r *http.Request) {
	id, e := srv.userID(r)
	if e != nil {
		reply(w, nil, e)
		return
	}
	var in struct {
		Slug    string `json:"slug"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	e = srv.accounts.SetFavorite(r.Context(), id, in.Slug, in.Enabled)
	reply(w, map[string]bool{"saved": e == nil}, e)
}
