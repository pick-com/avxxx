package api

import "net/http"

func (srv *Server) mediaRoutes() []route {
	return []route{
		{"GET /api/releases/{slug}/sources", srv.releaseSources},
		{"GET /api/releases/{slug}/playback", srv.releasePlayback},
		// Source-specific search/detail/resolve routes are ready for Kodi adapters.
		// Adapters are intentionally not registered until their source implementation
		// and authorization requirements are reviewed.
		{"GET /api/search", srv.media.Search},
		{"GET /api/detail/{source}/{id...}", srv.media.Detail},
		{"POST /api/resolve/{source}/{id...}", srv.media.Resolve},
	}
}

func (srv *Server) releaseSources(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.ReleaseSources(r.Context(), r.PathValue("slug"))
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) releasePlayback(w http.ResponseWriter, r *http.Request) {
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "javtiful"
	}
	v, e := srv.store.ReleasePlayback(r.Context(), r.PathValue("slug"), source)
	reply(w, v, e)
}
