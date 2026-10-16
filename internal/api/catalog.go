package api

import (
	"avxxx/internal/i18n"
	"avxxx/internal/images"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
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
		{"GET /api/upcoming", srv.upcoming},
		{"GET /api/catalog/facets", srv.catalogFacets},
	}
}

func (srv *Server) items(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Items(r.Context(), r.URL.Query().Get("q"))
	reply(w, v, e)
}

// performers lists active performers: ?q=&sort=name|works|newest_work|heat|
// followers|rising&era=<decade>&tag=&audio_lang=&limit=&cursor= (NavActors;
// heat / followers / rising and the era / tag / audio_lang filters read the
// hourly performer_stats).
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
	srv.withPortraits(r.Context(), performerPtrs(v, func(p *store.PerformerListItem) *model.Performer { return &p.Performer }))
	l, err := srv.localizer(r.Context(), lang, performerRefs(v))
	reply(w, withLang(page(localizePerformers(l, v), next), l), err)
}

// performerDetail returns one performer; ?lang= adds display_name and
// display_biography (see i18n.go). It also carries aliases (别名), names
// (reviewed name translations: 多语言名) and stats (作品 / 主演 / 即将上线 /
// 粉丝 live; 总播放 / 30 天热度 from performer_stats).
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
	if pid, perr := strconv.ParseInt(id, 10, 64); perr == nil {
		if img := srv.publicImages(r.Context(), images.EntityPerformer, []int64{pid})[pid]; img != nil {
			v["portrait"] = img
		}
		x, err := srv.store.PerformerExtrasByID(r.Context(), pid)
		if err != nil {
			reply(w, nil, err)
			return
		}
		v["aliases"], v["names"], v["stats"] = x.Aliases, x.Names, x.Stats
	}
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		return []i18n.Ref{ref(i18n.EntityPerformer, id, "name"), ref(i18n.EntityPerformer, id, "biography")}
	})
	localizePerformerDetail(l, v)
	reply(w, v, err)
}

func (srv *Server) performerReleases(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.PerformerReleases(r.Context(), r.PathValue("slug"), limit)
	if e == nil {
		srv.withCovers(r.Context(), v)
	}
	reply(w, map[string]any{"items": v}, e)
}

// releases lists published releases: ?q=&tag=&maker=&performer=&role=lead|guest
// &year=&min_runtime=&max_runtime=&min_height=&subtitle=any|<lang>&audio_lang=
// &sort=newest|oldest|runtime&limit=&cursor=.
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
	srv.withCovers(r.Context(), v)
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
	one := []model.Release{v.Release}
	srv.withCovers(r.Context(), one)
	v.Cover = one[0].Cover
	l, err := srv.localizer(r.Context(), lang, releaseDetailRefs(v))
	reply(w, localizeReleaseDetail(l, v), err)
}

// releasePerformers is the cast of a published or upcoming release with each
// credit's lead flag (主演 / 客串), role and credited name.
func (srv *Server) releasePerformers(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.ReleaseCast(r.Context(), r.PathValue("slug"))
	if e == nil {
		srv.withPortraits(r.Context(), performerPtrs(v, func(p *store.CastMember) *model.Performer { return &p.Performer }))
	}
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

// upcomingItem is one 即将上线 row; Reserved is set for a signed-in caller.
type upcomingItem struct {
	store.UpcomingRelease
	DisplayTitle     string `json:"display_title,omitempty"`
	DisplayTitleLang string `json:"display_title_lang,omitempty"`
	Reserved         *bool  `json:"reserved,omitempty"`
}

// upcoming: GET /api/upcoming?performer=&followed=1&limit=&cursor=&lang=
// (NavNew 即将上线, the Actor page shelf). Soonest first; the client groups
// by date (null = 日期待定). followed=1 (只看关注的演员) needs a bearer
// token; with one, items carry reserved (预约 state).
func (srv *Server) upcoming(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.UpcomingFilter{Performer: strings.TrimSpace(q.Get("performer"))}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		reply(w, nil, err)
		return
	}
	if f.Cursor, err = cursorParam(q, store.UpcomingSort); err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	uid := srv.optionalUserID(r)
	switch strings.TrimSpace(q.Get("followed")) {
	case "", "0", "false":
	case "1", "true":
		if uid == nil {
			reply(w, nil, errUnauthorized)
			return
		}
		f.FollowedBy = *uid
	default:
		reply(w, nil, badRequest("followed must be 1 or 0", nil))
		return
	}
	ctx := r.Context()
	items, next, err := srv.store.UpcomingPage(ctx, f)
	if err != nil {
		reply(w, nil, err)
		return
	}
	rs := make([]model.Release, len(items))
	ids := make([]int64, len(items))
	for i, x := range items {
		rs[i], ids[i] = x.Release, x.ID
	}
	srv.withCovers(ctx, rs)
	var reserved map[int64]bool
	if uid != nil {
		if reserved, err = srv.store.ReservedAmong(ctx, *uid, ids); err != nil {
			reply(w, nil, err)
			return
		}
	}
	l, err := srv.localizer(ctx, lang, releaseRefs(rs))
	out := make([]upcomingItem, len(items))
	for i, x := range items {
		x.Cover = rs[i].Cover
		out[i].UpcomingRelease = x
		if l != nil {
			out[i].DisplayTitle, out[i].DisplayTitleLang = l.get(i18n.EntityRelease, idText(x.ID), "title", x.CanonicalTitle)
		}
		if reserved != nil {
			v := reserved[x.ID]
			out[i].Reserved = &v
		}
	}
	reply(w, withLang(page(out, next), l), err)
}

// facetsTTL bounds how stale GET /api/catalog/facets may be: it aggregates
// every playback source, so it is computed at most once per TTL per process.
const facetsTTL = 10 * time.Minute

var facetsCache struct {
	sync.Mutex
	at time.Time
	v  store.CatalogFacets
}

// catalogFacets: GET /api/catalog/facets. Which playback-source facets have
// data (清晰度 heights, 字幕 subtitle_langs / subtitled, 语言 audio_langs),
// so search and discover only offer filters that can match.
func (srv *Server) catalogFacets(w http.ResponseWriter, r *http.Request) {
	facetsCache.Lock()
	defer facetsCache.Unlock()
	if facetsCache.at.IsZero() || time.Since(facetsCache.at) > facetsTTL {
		v, err := srv.store.CatalogFacets(r.Context())
		if err != nil {
			reply(w, nil, err)
			return
		}
		facetsCache.v, facetsCache.at = v, time.Now()
	}
	w.Header().Set("Cache-Control", "public, max-age=600")
	reply(w, facetsCache.v, nil)
}
