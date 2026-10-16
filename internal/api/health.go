package api

import "net/http"

func (srv *Server) healthRoutes() []route {
	return []route{
		{"GET /api/health", srv.health},
		{"GET /api/live", srv.live},
	}
}

func (srv *Server) health(w http.ResponseWriter, r *http.Request) {
	e := srv.store.Ping(r.Context())
	if e != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	reply(w, map[string]bool{"ok": e == nil}, e)
}

func (srv *Server) live(w http.ResponseWriter, r *http.Request) {
	reply(w, map[string]bool{"ok": true}, nil)
}
