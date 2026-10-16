package api

import (
	"net/http"
	"strconv"
)

func (srv *Server) catalogRoutes() []route {
	return []route{
		{"GET /api/items", srv.items},
		{"GET /api/performers", srv.performers},
		{"GET /api/performers/{slug}", srv.performerDetail},
		{"GET /api/performers/{slug}/releases", srv.performerReleases},
		{"GET /api/releases", srv.releases},
		{"GET /api/releases/{slug}", srv.releaseBySlug},
		{"GET /api/releases/{slug}/performers", srv.releasePerformers},
		{"GET /api/tags", srv.tags},
		{"GET /api/makers", srv.makers},
		{"GET /api/catalog/search", srv.catalogSearch},
	}
}

func (srv *Server) items(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Items(r.Context(), r.URL.Query().Get("q"))
	reply(w, v, e)
}

func (srv *Server) performers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.Performers(r.Context(), r.URL.Query().Get("q"), limit)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) performerDetail(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.PerformerDetail(r.Context(), r.PathValue("slug"))
	reply(w, v, e)
}

func (srv *Server) performerReleases(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.PerformerReleases(r.Context(), r.PathValue("slug"), limit)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) releases(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.Releases(r.Context(), r.URL.Query().Get("q"), limit)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) releaseBySlug(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.ReleaseBySlug(r.Context(), r.PathValue("slug"))
	reply(w, v, e)
}

func (srv *Server) releasePerformers(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.ReleasePerformers(r.Context(), r.PathValue("slug"))
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) tags(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.Tags(r.Context(), limit)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) makers(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.Makers(r.Context(), limit)
	reply(w, map[string]any{"items": v}, e)
}

func (srv *Server) catalogSearch(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.CatalogSearch(r.Context(), r.URL.Query().Get("q"), limit)
	reply(w, map[string]any{"items": v}, e)
}
