package api

import (
	"avxxx/internal/i18n"
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

// performers lists active performers: ?q=&sort=name|works|newest_work&limit=&cursor=.
func (srv *Server) performers(w http.ResponseWriter, r *http.Request) {
	f, err := parsePerformerFilter(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, next, err := srv.store.PerformerPage(r.Context(), f)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, performerRefs(v))
	reply(w, withLang(page(localizePerformers(l, v), next), l), err)
}

// performerDetail returns one performer; ?lang= adds display_name and
// display_biography (see i18n.go).
func (srv *Server) performerDetail(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.store.PerformerDetail(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	id := mapID(v)
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		return []i18n.Ref{ref(i18n.EntityPerformer, id, "name"), ref(i18n.EntityPerformer, id, "biography")}
	})
	localizePerformerDetail(l, v)
	reply(w, v, err)
}

func (srv *Server) performerReleases(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.PerformerReleases(r.Context(), r.PathValue("slug"), limit)
	reply(w, map[string]any{"items": v}, e)
}

// releases lists published releases: ?q=&tag=&maker=&performer=&year=
// &min_runtime=&max_runtime=&sort=newest|oldest|runtime&limit=&cursor=.
func (srv *Server) releases(w http.ResponseWriter, r *http.Request) {
	f, err := parseReleaseFilter(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, next, err := srv.store.ReleasePage(r.Context(), f)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, releaseRefs(v))
	reply(w, withLang(page(localizeReleases(l, v), next), l), err)
}

// releaseBySlug returns the release page payload; ?lang= adds
// display_title, display_synopsis and tag display_name (see i18n.go).
func (srv *Server) releaseBySlug(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.store.ReleaseDetailBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, releaseDetailRefs(v))
	reply(w, localizeReleaseDetail(l, v), err)
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
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, err := srv.store.SearchCatalog(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, searchRefs(v))
	localizeSearch(l, v)
	reply(w, withLang(map[string]any{"items": v}, l), err)
}
