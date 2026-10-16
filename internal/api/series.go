package api

// Series (migrations/027_series.sql, internal/series; FINAL_DATA_DESIGN 9
// GET /api/series; PRD 5.4 同系列) and public source evidence
// (internal/evidence; ROADMAP 来源证据和更新时间展示). Only published
// releases and active performers are visible; evidence never carries
// raw_data, source payloads, override reasons or actors.
//
//	GET /api/series?q=&sort=newest|name|works&limit=&cursor=&lang=
//	GET /api/series/{slug}?lang=
//	GET /api/series/{slug}/releases?sort=episode|newest|oldest&limit=&cursor=&lang=
//	GET /api/releases/{slug}/evidence
//	GET /api/performers/{slug}/evidence

import (
	"avxxx/internal/evidence"
	"avxxx/internal/i18n"
	"avxxx/internal/series"
	"avxxx/internal/store"
	"context"
	"net/http"
	"strings"
)

// seriesStore is the subset of *store.Store behind the series routes.
type seriesStore interface {
	SeriesPage(ctx context.Context, f store.SeriesFilter) ([]store.SeriesListItem, string, error)
	SeriesDetailBySlug(ctx context.Context, slug string, topPerformers int) (store.SeriesDetail, error)
	SeriesReleases(ctx context.Context, slug string) ([]store.SeriesRelease, error)
}

// evidenceStore is the subset of *store.Store behind the evidence routes.
type evidenceStore interface {
	ReleaseEvidence(ctx context.Context, slug string) (evidence.Evidence, error)
	PerformerEvidence(ctx context.Context, slug string) (evidence.Evidence, error)
}

func (srv *Server) seriesRoutes() []route {
	return []route{
		{"GET /api/series", srv.seriesList},
		{"GET /api/series/{slug}", srv.seriesDetail},
		{"GET /api/series/{slug}/releases", srv.seriesReleases},
		{"GET /api/releases/{slug}/evidence", srv.releaseEvidence},
		{"GET /api/performers/{slug}/evidence", srv.performerEvidence},
	}
}

type localSeries struct {
	store.SeriesListItem
	DisplayName     string `json:"display_name,omitempty"`
	DisplayNameLang string `json:"display_name_lang,omitempty"`
}

func seriesRef(id int64) i18n.Ref { return ref(i18n.EntitySeries, idText(id), "name") }

// seriesList lists series with at least one published release.
func (srv *Server) seriesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SeriesFilter{Q: strings.TrimSpace(q.Get("q")), Sort: strings.TrimSpace(q.Get("sort"))}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	if !store.ValidSeriesSort(f.Sort) {
		reply(w, nil, badRequest("sort must be newest, name or works", nil))
		return
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		reply(w, nil, err)
		return
	}
	if f.Cursor, err = cursorParam(q, f.Sort); err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if srv.seriesSt == nil {
		reply(w, nil, errUnavailable)
		return
	}
	items, next, err := srv.seriesSt.SeriesPage(r.Context(), f)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		out := make([]i18n.Ref, len(items))
		for i, x := range items {
			out[i] = seriesRef(x.ID)
		}
		return out
	})
	if l == nil {
		reply(w, page(items, next), err)
		return
	}
	out := make([]localSeries, len(items))
	for i, x := range items {
		out[i].SeriesListItem = x
		out[i].DisplayName, out[i].DisplayNameLang = l.get(i18n.EntitySeries, idText(x.ID), "name", x.Name)
	}
	reply(w, withLang(page(out, next), l), err)
}

type localSeriesPerformer struct {
	store.SeriesPerformer
	DisplayName     string `json:"display_name,omitempty"`
	DisplayNameLang string `json:"display_name_lang,omitempty"`
}

type localSeriesDetail struct {
	store.SeriesDetail
	Lang            string                 `json:"lang"`
	DisplayName     string                 `json:"display_name,omitempty"`
	DisplayNameLang string                 `json:"display_name_lang,omitempty"`
	TopPerformers   []localSeriesPerformer `json:"top_performers"` // shadows SeriesDetail.TopPerformers
}

// seriesDetail returns one series: counts, date range, makers and the most
// credited performers; ?lang= adds display names.
func (srv *Server) seriesDetail(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if srv.seriesSt == nil {
		reply(w, nil, errUnavailable)
		return
	}
	d, err := srv.seriesSt.SeriesDetailBySlug(r.Context(), r.PathValue("slug"), 10)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		out := []i18n.Ref{seriesRef(d.ID)}
		for _, p := range d.TopPerformers {
			out = append(out, ref(i18n.EntityPerformer, idText(p.ID), "name"))
		}
		return out
	})
	if l == nil {
		reply(w, d, err)
		return
	}
	out := localSeriesDetail{SeriesDetail: d, Lang: l.lang, TopPerformers: make([]localSeriesPerformer, len(d.TopPerformers))}
	out.DisplayName, out.DisplayNameLang = l.get(i18n.EntitySeries, idText(d.ID), "name", d.Name)
	for i, p := range d.TopPerformers {
		out.TopPerformers[i].SeriesPerformer = p
		out.TopPerformers[i].DisplayName, out.TopPerformers[i].DisplayNameLang = l.get(i18n.EntityPerformer, idText(p.ID), "name", p.Name)
	}
	reply(w, out, err)
}

type seriesReleaseItem struct {
	store.SeriesRelease
	Episode          *int   `json:"episode,omitempty"`
	DisplayTitle     string `json:"display_title,omitempty"`
	DisplayTitleLang string `json:"display_title_lang,omitempty"`
}

// seriesPageLimit clamps a page size like the store does (default 20, max 100).
func seriesPageLimit(n int) int {
	switch {
	case n <= 0:
		return 20
	case n > 100:
		return 100
	}
	return n
}

// seriesReleases lists a series' published releases in episode order
// (default), or newest / oldest first. The envelope adds episode_basis
// (title | product_code | release_date) for sort=episode.
func (srv *Server) seriesReleases(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sortName := strings.TrimSpace(q.Get("sort"))
	if sortName == "" {
		sortName = series.SortEpisode
	}
	if !series.ValidSort(sortName) {
		reply(w, nil, badRequest("sort must be episode, newest or oldest", nil))
		return
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	cur, err := cursorParam(q, sortName)
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if srv.seriesSt == nil {
		reply(w, nil, errUnavailable)
		return
	}
	rels, err := srv.seriesSt.SeriesReleases(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	byID := make(map[int64]store.SeriesRelease, len(rels))
	items := make([]series.Item, len(rels))
	for i, x := range rels {
		byID[x.ID] = x
		code := x.ProductCode
		if code == "" {
			code = x.Slug
		}
		items[i] = series.Item{ID: x.ID, Title: x.CanonicalTitle, Code: code, ReleaseDate: x.ReleaseDate}
	}
	ordered, basis := series.Order(items, sortName)
	if cur != nil {
		ordered = series.After(ordered, cur.Key, cur.ID)
	}
	limit = seriesPageLimit(limit)
	next := ""
	if len(ordered) > limit {
		last := ordered[limit-1]
		next = store.EncodeCursor(store.Cursor{Sort: sortName, Key: last.Key, ID: last.ID})
		ordered = ordered[:limit]
	}
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		out := make([]i18n.Ref, len(ordered))
		for i, k := range ordered {
			out[i] = ref(i18n.EntityRelease, idText(k.ID), "title")
		}
		return out
	})
	out := make([]seriesReleaseItem, len(ordered))
	for i, k := range ordered {
		out[i] = seriesReleaseItem{SeriesRelease: byID[k.ID], Episode: k.Episode}
		if l != nil {
			out[i].DisplayTitle, out[i].DisplayTitleLang = l.get(i18n.EntityRelease, idText(k.ID), "title", out[i].CanonicalTitle)
		}
	}
	env := withLang(page(out, next), l)
	env["sort"] = sortName
	if sortName == series.SortEpisode {
		env["episode_basis"] = basis
	}
	reply(w, env, err)
}

func (srv *Server) releaseEvidence(w http.ResponseWriter, r *http.Request) {
	if srv.evidence == nil {
		reply(w, nil, errUnavailable)
		return
	}
	v, err := srv.evidence.ReleaseEvidence(r.Context(), r.PathValue("slug"))
	reply(w, v, err)
}

func (srv *Server) performerEvidence(w http.ResponseWriter, r *http.Request) {
	if srv.evidence == nil {
		reply(w, nil, errUnavailable)
		return
	}
	v, err := srv.evidence.PerformerEvidence(r.Context(), r.PathValue("slug"))
	reply(w, v, err)
}
