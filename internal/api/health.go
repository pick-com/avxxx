package api

import "net/http"

func (srv *Server) healthRoutes() []route {
	return []route{
		{"GET /api/health", srv.health},
		{"GET /api/live", srv.live},
	}
}

func (srv *Server) health(w http.ResponseWriter, r *http.Request) {
	if e := srv.accounts.Ping(r.Context()); e != nil {
		reply(w, nil, &httpError{status: http.StatusServiceUnavailable, msg: "database unavailable", err: e})
		return
	}
	reply(w, map[string]bool{"ok": true}, nil)
}

func (srv *Server) live(w http.ResponseWriter, r *http.Request) {
	reply(w, map[string]bool{"ok": true}, nil)
}
