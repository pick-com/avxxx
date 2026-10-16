package api

// Home curation and site announcements (PRD 4.1 主推横幅 / 高分短片, PRD 11
// AdminOps; migrations/038_home_ops.sql, internal/homeops):
//
//	GET    /api/home/slots?lang=&region=            live curated slots (hero + shelf), public targets only
//	GET    /api/home/top-rated-short?lang=&limit=&max_minutes=   visitor 高分短片 shelf
//	GET    /api/announcements?lang=&region=         live announcements
//	GET    /api/me/announcement-dismissals          ids the signed-in user dismissed
//	PUT    /api/me/announcement-dismissals/{id}     dismiss one (signed in)
//
// AdminOps (srv.admin; the authenticated admin is the review_events actor):
//
//	GET    /api/admin/home-slots?from=&to=&slot_type=
//	POST   /api/admin/home-slots
//	PUT    /api/admin/home-slots/{id}
//	DELETE /api/admin/home-slots/{id}
//	POST   /api/admin/home-slots/reorder            {"slot_type":"hero","ids":[3,1,2]}
//	GET    /api/admin/home-slots/preview?at=&lang=&region=   what the public routes serve at `at`
//	GET    /api/admin/announcements
//	POST   /api/admin/announcements
//	PUT    /api/admin/announcements/{id}
//	DELETE /api/admin/announcements/{id}
//
// The public reads are cached in process for a short time (homeCacheTTL;
// 高分短片 shortCacheTTL) and sent with a public Cache-Control; any AdminOps
// write on this instance drops the cache. region is an ISO 3166-1 alpha-2
// code from ?region= or, behind a CDN, the CF-IPCountry header; without one,
// region-targeted entries are left out.

import (
	"avxxx/internal/homeops"
	"avxxx/internal/i18n"
	"avxxx/internal/images"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// homeStore is the subset of *store.Store the home and AdminOps routes use.
type homeStore interface {
	LiveHomeSlots(ctx context.Context, at time.Time, lang, region string) ([]store.HomeSlot, error)
	HomeSlots(ctx context.Context, f store.HomeSlotFilter) ([]store.HomeSlot, error)
	CreateHomeSlot(ctx context.Context, in store.HomeSlotInput, actor string) (store.HomeSlot, error)
	UpdateHomeSlot(ctx context.Context, id int64, in store.HomeSlotInput, actor string) (store.HomeSlot, error)
	DeleteHomeSlot(ctx context.Context, id int64, actor string) error
	ReorderHomeSlots(ctx context.Context, slotType string, ids []int64, actor string) (int, error)
	LiveAnnouncements(ctx context.Context, at time.Time, lang, region string) ([]store.Announcement, error)
	Announcements(ctx context.Context) ([]store.Announcement, error)
	CreateAnnouncement(ctx context.Context, in store.AnnouncementInput, actor string) (store.Announcement, error)
	UpdateAnnouncement(ctx context.Context, id int64, in store.AnnouncementInput, actor string) (store.Announcement, error)
	DeleteAnnouncement(ctx context.Context, id int64, actor string) error
	DismissAnnouncement(ctx context.Context, userID, id int64) error
	DismissedAnnouncements(ctx context.Context, userID int64) ([]int64, error)
	TopRatedShort(ctx context.Context, maxMinutes, limit int) ([]store.RatedRelease, string, error)
}

const (
	homeCacheTTL  = 30 * time.Second
	shortCacheTTL = 5 * time.Minute
	homeMaxAge    = "public, max-age=60"
	shortMaxAge   = "public, max-age=300"
)

func (srv *Server) homeRoutes() []route {
	return []route{
		{"GET /api/home/slots", srv.withHome(srv.publicHomeSlots)},
		{"GET /api/home/top-rated-short", srv.withHome(srv.topRatedShort)},
		{"GET /api/announcements", srv.withHome(srv.publicAnnouncements)},
		{"GET /api/me/announcement-dismissals", srv.withHome(srv.announcementDismissals)},
		{"PUT /api/me/announcement-dismissals/{id}", srv.withHome(srv.dismissAnnouncement)},
	}
}

func (srv *Server) adminOpsRoutes() []route {
	return []route{
		{"GET /api/admin/home-slots", srv.admin(srv.withHome(srv.adminHomeSlots))},
		{"POST /api/admin/home-slots", srv.admin(srv.withHome(srv.createHomeSlot))},
		{"PUT /api/admin/home-slots/{id}", srv.admin(srv.withHome(srv.updateHomeSlot))},
		{"DELETE /api/admin/home-slots/{id}", srv.admin(srv.withHome(srv.deleteHomeSlot))},
		{"POST /api/admin/home-slots/reorder", srv.admin(srv.withHome(srv.reorderHomeSlots))},
		{"GET /api/admin/home-slots/preview", srv.admin(srv.withHome(srv.previewHome))},
		{"GET /api/admin/announcements", srv.admin(srv.withHome(srv.adminAnnouncements))},
		{"POST /api/admin/announcements", srv.admin(srv.withHome(srv.createAnnouncement))},
		{"PUT /api/admin/announcements/{id}", srv.admin(srv.withHome(srv.updateAnnouncement))},
		{"DELETE /api/admin/announcements/{id}", srv.admin(srv.withHome(srv.deleteAnnouncement))},
	}
}

func (srv *Server) withHome(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.home == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// ---- short-lived response cache ----

// respCache keeps encoded JSON bodies for a short time. A nil *respCache
// caches nothing. clear() drops every entry (AdminOps writes).
type respCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	body []byte
	at   time.Time
}

func newRespCache() *respCache { return &respCache{entries: map[string]cacheEntry{}} }

func (c *respCache) get(key string, ttl time.Duration) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Since(e.at) > ttl {
		return nil, false
	}
	return e.body, true
}

func (c *respCache) put(key string, body []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) > 512 { // bounded: lang x region keys
		c.entries = map[string]cacheEntry{}
	}
	c.entries[key] = cacheEntry{body: body, at: time.Now()}
}

func (c *respCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]cacheEntry{}
	c.mu.Unlock()
}

// cachedJSON serves key from the cache or builds, caches and serves it.
// Errors are not cached.
func (srv *Server) cachedJSON(w http.ResponseWriter, key string, ttl time.Duration, cacheControl string, build func() (any, error)) {
	if body, ok := srv.homeCache.get(key, ttl); ok {
		writeCachedJSON(w, body, cacheControl)
		return
	}
	v, err := build()
	if err != nil {
		reply(w, nil, err)
		return
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(v); err != nil {
		reply(w, nil, err)
		return
	}
	srv.homeCache.put(key, buf.Bytes())
	writeCachedJSON(w, buf.Bytes(), cacheControl)
}

func writeCachedJSON(w http.ResponseWriter, body []byte, cacheControl string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", cacheControl)
	w.Write(body)
}

// ---- request parameters ----

// requestRegion is ?region=, else the visitor's country from CF-IPCountry
// or GEOIP_HEADER ("" when unknown; region.go).
func requestRegion(r *http.Request) (string, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("region")); v != "" {
		reg, ok := homeops.NormalizeRegion(v)
		if !ok {
			return "", badRequest("invalid region", nil)
		}
		return reg, nil
	}
	return headerRegion(r), nil
}

// parseTimeParam accepts RFC 3339 or a YYYY-MM-DD date (UTC midnight).
func parseTimeParam(v, name string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return &t, nil
	}
	return nil, badRequest("invalid "+name+" (RFC 3339 or YYYY-MM-DD)", nil)
}

// ---- public shapes ----

// homeRelease is a slot or shelf release as the home page shows it.
type homeRelease struct {
	ID               int64        `json:"id"`
	Slug             string       `json:"slug"`
	CanonicalTitle   string       `json:"canonical_title"`
	DisplayTitle     string       `json:"display_title,omitempty"`
	DisplayTitleLang string       `json:"display_title_lang,omitempty"`
	ReleaseDate      *time.Time   `json:"release_date,omitempty"`
	RuntimeMinutes   int          `json:"runtime_minutes,omitempty"`
	MakerName        string       `json:"maker_name,omitempty"`
	Cover            *model.Image `json:"cover,omitempty"`
}

// homeSlotView is one live slot: final title / subtitle for the language
// (operator override, else the target's localized title) and its link.
type homeSlotView struct {
	ID         int64           `json:"id"`
	SlotType   string          `json:"slot_type"`
	TargetType string          `json:"target_type"`
	Title      string          `json:"title"`
	TitleLang  string          `json:"title_lang,omitempty"`
	Subtitle   string          `json:"subtitle,omitempty"`
	Href       string          `json:"href"`
	External   bool            `json:"external,omitempty"`
	Release    *homeRelease    `json:"release,omitempty"`
	Series     *homeSeriesView `json:"series,omitempty"`
	Cover      *model.Image    `json:"cover,omitempty"`
	StartsAt   time.Time       `json:"starts_at"`
	EndsAt     *time.Time      `json:"ends_at"`
	Priority   int             `json:"priority"`
}

type homeSeriesView struct {
	store.HomeSeries
	DisplayName string `json:"display_name,omitempty"`
}

// homeSlotViews resolves live slots for lang ("" = zh-CN copy, canonical
// target titles). Covers come from stored images (internal/images).
func (srv *Server) homeSlotViews(ctx context.Context, slots []store.HomeSlot, lang string) ([]homeSlotView, error) {
	var relIDs []int64
	for _, h := range slots {
		if h.Release != nil {
			relIDs = append(relIDs, h.Release.ID)
		}
	}
	covers := srv.publicImages(ctx, images.EntityRelease, relIDs)
	l, err := srv.localizer(ctx, lang, func() []i18n.Ref {
		var refs []i18n.Ref
		for _, h := range slots {
			if h.Release != nil {
				refs = append(refs, ref(i18n.EntityRelease, idText(h.Release.ID), "title"))
			}
			if h.Series != nil {
				refs = append(refs, ref(i18n.EntitySeries, idText(h.Series.ID), "name"))
			}
		}
		return refs
	})
	if err != nil {
		return nil, err
	}
	out := make([]homeSlotView, 0, len(slots))
	for _, h := range slots {
		v := homeSlotView{ID: h.ID, SlotType: h.SlotType, TargetType: h.TargetType, StartsAt: h.StartsAt, EndsAt: h.EndsAt, Priority: h.Priority}
		var fallback string
		if h.Release != nil {
			rel := &homeRelease{ID: h.Release.ID, Slug: h.Release.Slug, CanonicalTitle: h.Release.CanonicalTitle, ReleaseDate: h.Release.ReleaseDate,
				RuntimeMinutes: h.Release.RuntimeMinutes, MakerName: h.Release.MakerName, Cover: covers[h.Release.ID]}
			fallback = rel.CanonicalTitle
			if l != nil {
				rel.DisplayTitle, rel.DisplayTitleLang = l.get(i18n.EntityRelease, idText(rel.ID), "title", rel.CanonicalTitle)
				fallback = rel.DisplayTitle
			}
			v.Release, v.Cover = rel, rel.Cover
		}
		switch h.TargetType {
		case homeops.TargetRelease:
			if v.Release != nil {
				v.Href = "/releases/" + v.Release.Slug
			}
		case homeops.TargetSeries:
			if h.Series != nil {
				s := &homeSeriesView{HomeSeries: *h.Series}
				fallback = s.Name
				if l != nil {
					s.DisplayName, _ = l.get(i18n.EntitySeries, idText(s.ID), "name", s.Name)
					fallback = s.DisplayName
				}
				v.Series = s
				v.Href = "/series/" + s.Slug
			}
		case homeops.TargetLink:
			v.Href = h.LinkURL
			v.External = strings.HasPrefix(h.LinkURL, "https://")
		}
		v.Title, v.TitleLang = homeops.Pick(h.Texts, lang, homeops.Title)
		if v.Title == "" {
			v.Title = fallback
		}
		// The subtitle follows the title's language so one slot never mixes
		// languages; without a title override it is picked on its own.
		if v.TitleLang != "" {
			v.Subtitle = h.Texts[v.TitleLang].Subtitle
		} else {
			v.Subtitle, _ = homeops.Pick(h.Texts, lang, homeops.Subtitle)
		}
		out = append(out, v)
	}
	return out, nil
}

// announcementView is one live announcement in the request language.
type announcementView struct {
	ID          int64      `json:"id"`
	Severity    string     `json:"severity"`
	Body        string     `json:"body"`
	BodyLang    string     `json:"body_lang,omitempty"`
	LinkURL     string     `json:"link_url,omitempty"`
	LinkLabel   string     `json:"link_label,omitempty"`
	External    bool       `json:"external,omitempty"`
	Dismissible bool       `json:"dismissible"`
	StartsAt    time.Time  `json:"starts_at"`
	EndsAt      *time.Time `json:"ends_at"`
	// Version changes whenever the announcement is edited, so a visitor's
	// stored dismissal (id:version) does not hide a materially new text.
	Version int64 `json:"version"`
}

func announcementViews(as []store.Announcement, lang string) []announcementView {
	out := make([]announcementView, 0, len(as))
	for _, a := range as {
		v := announcementView{ID: a.ID, Severity: a.Severity, LinkURL: a.LinkURL, External: strings.HasPrefix(a.LinkURL, "https://"),
			Dismissible: a.Dismissible, StartsAt: a.StartsAt, EndsAt: a.EndsAt, Version: a.UpdatedAt.Unix()}
		v.Body, v.BodyLang = homeops.Pick(a.Texts, lang, homeops.Body)
		if v.LinkURL != "" && v.BodyLang != "" {
			// Same language as the body; empty means the UI's default label.
			v.LinkLabel = a.Texts[v.BodyLang].LinkLabel
		}
		if v.Body != "" {
			out = append(out, v)
		}
	}
	return out
}

// ---- public handlers ----

func (srv *Server) publicHomeSlots(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	varyRegion(w)
	region, err := requestRegion(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.cachedJSON(w, "slots|"+lang+"|"+region, homeCacheTTL, homeMaxAge, func() (any, error) {
		return srv.homeSlotsBody(r.Context(), time.Now(), lang, region)
	})
}

func (srv *Server) homeSlotsBody(ctx context.Context, at time.Time, lang, region string) (map[string]any, error) {
	slots, err := srv.home.LiveHomeSlots(ctx, at, lang, region)
	if err != nil {
		return nil, err
	}
	views, err := srv.homeSlotViews(ctx, slots, lang)
	if err != nil {
		return nil, err
	}
	hero, shelf := []homeSlotView{}, []homeSlotView{}
	for _, v := range views {
		if v.SlotType == homeops.SlotHero {
			hero = append(hero, v)
		} else {
			shelf = append(shelf, v)
		}
	}
	body := map[string]any{"hero": hero, "shelf": shelf, "region": region}
	if lang != "" {
		body["lang"] = lang
	}
	return body, nil
}

func (srv *Server) topRatedShort(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	q := r.URL.Query()
	limit, err := intParam(q, "limit", 50)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if limit == 0 {
		limit = 10
	}
	maxMin, err := intParam(q, "max_minutes", 180)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if maxMin == 0 {
		maxMin = store.ShortMaxMinutes
	}
	key := "short|" + lang + "|" + strconv.Itoa(limit) + "|" + strconv.Itoa(maxMin)
	srv.cachedJSON(w, key, shortCacheTTL, shortMaxAge, func() (any, error) {
		items, basis, err := srv.home.TopRatedShort(r.Context(), maxMin, limit)
		if err != nil {
			return nil, err
		}
		rs := make([]model.Release, len(items))
		for i, x := range items {
			rs[i] = x.Release
		}
		srv.withCovers(r.Context(), rs)
		l, err := srv.localizer(r.Context(), lang, releaseRefs(rs))
		if err != nil {
			return nil, err
		}
		type ratedView struct {
			homeRelease
			Score *float64 `json:"score"`
			Votes int      `json:"votes"`
		}
		out := make([]ratedView, len(items))
		for i, x := range items {
			v := ratedView{homeRelease: homeRelease{ID: x.ID, Slug: x.Slug, CanonicalTitle: x.CanonicalTitle, ReleaseDate: x.ReleaseDate,
				RuntimeMinutes: x.RuntimeMinutes, MakerName: x.MakerName, Cover: rs[i].Cover}, Score: x.Score, Votes: x.Votes}
			if l != nil {
				v.DisplayTitle, v.DisplayTitleLang = l.get(i18n.EntityRelease, idText(x.ID), "title", x.CanonicalTitle)
			}
			out[i] = v
		}
		return withLang(map[string]any{"items": out, "basis": basis, "max_minutes": maxMin}, l), nil
	})
}

func (srv *Server) publicAnnouncements(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	varyRegion(w)
	region, err := requestRegion(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.cachedJSON(w, "ann|"+lang+"|"+region, homeCacheTTL, homeMaxAge, func() (any, error) {
		as, err := srv.home.LiveAnnouncements(r.Context(), time.Now(), lang, region)
		if err != nil {
			return nil, err
		}
		return map[string]any{"items": announcementViews(as, lang)}, nil
	})
}

func (srv *Server) announcementDismissals(w http.ResponseWriter, r *http.Request) {
	uid, err := srv.userID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ids, err := srv.home.DismissedAnnouncements(r.Context(), uid)
	w.Header().Set("Cache-Control", "private, no-store")
	reply(w, map[string]any{"ids": ids}, err)
}

func (srv *Server) dismissAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	uid, err := srv.userID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.home.DismissAnnouncement(r.Context(), uid, id)
	reply(w, map[string]any{"id": id, "dismissed": err == nil}, err)
}

// ---- AdminOps handlers ----

func (srv *Server) adminHomeSlots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := parseTimeParam(q.Get("from"), "from")
	if err != nil {
		reply(w, nil, err)
		return
	}
	to, err := parseTimeParam(q.Get("to"), "to")
	if err != nil {
		reply(w, nil, err)
		return
	}
	typ := strings.TrimSpace(q.Get("slot_type"))
	if typ != "" && !homeops.ValidSlotType(typ) {
		reply(w, nil, badRequest("slot_type must be hero or shelf", nil))
		return
	}
	items, err := srv.home.HomeSlots(r.Context(), store.HomeSlotFilter{From: from, To: to, SlotType: typ})
	if err == nil {
		srv.withSlotCovers(r.Context(), items)
	}
	reply(w, map[string]any{"items": items, "now": time.Now().UTC()}, err)
}

// withSlotCovers sets the stored cover on each slot's target release.
func (srv *Server) withSlotCovers(ctx context.Context, items []store.HomeSlot) {
	var ids []int64
	for _, h := range items {
		if h.Release != nil {
			ids = append(ids, h.Release.ID)
		}
	}
	covers := srv.publicImages(ctx, images.EntityRelease, ids)
	for i := range items {
		if items[i].Release != nil {
			items[i].Release.Cover = covers[items[i].Release.ID]
		}
	}
}

// adminOpsWrite resolves the actor, decodes the body into v and runs fn,
// dropping the public cache after a successful write.
func (srv *Server) adminOpsWrite(w http.ResponseWriter, r *http.Request, v any, fn func(actor string) (any, error)) {
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if v != nil {
		if err := decodeAdminJSON(w, r, v); err != nil {
			reply(w, nil, err)
			return
		}
	}
	out, err := fn(actor)
	if err == nil {
		srv.homeCache.clear()
	}
	reply(w, out, err)
}

func (srv *Server) createHomeSlot(w http.ResponseWriter, r *http.Request) {
	var in store.HomeSlotInput
	srv.adminOpsWrite(w, r, &in, func(actor string) (any, error) {
		return srv.home.CreateHomeSlot(r.Context(), in, actor)
	})
}

func (srv *Server) updateHomeSlot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in store.HomeSlotInput
	srv.adminOpsWrite(w, r, &in, func(actor string) (any, error) {
		return srv.home.UpdateHomeSlot(r.Context(), id, in, actor)
	})
}

func (srv *Server) deleteHomeSlot(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.adminOpsWrite(w, r, nil, func(actor string) (any, error) {
		err := srv.home.DeleteHomeSlot(r.Context(), id, actor)
		return map[string]any{"id": id, "deleted": err == nil}, err
	})
}

func (srv *Server) reorderHomeSlots(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SlotType string  `json:"slot_type"`
		IDs      []int64 `json:"ids"`
	}
	srv.adminOpsWrite(w, r, &in, func(actor string) (any, error) {
		n, err := srv.home.ReorderHomeSlots(r.Context(), in.SlotType, in.IDs, actor)
		return map[string]any{"slot_type": in.SlotType, "changed": n}, err
	})
}

// previewHome answers what GET /api/home/slots and /api/announcements serve
// at `at` (default now) for lang / region, uncached.
func (srv *Server) previewHome(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	at, err := parseTimeParam(q.Get("at"), "at")
	if err != nil {
		reply(w, nil, err)
		return
	}
	when := time.Now()
	if at != nil {
		when = *at
	}
	lang := ""
	if v := strings.TrimSpace(q.Get("lang")); v != "" {
		if lang = i18n.Normalize(v); lang == "" {
			reply(w, nil, badRequest("unsupported lang", nil))
			return
		}
	}
	region, ok := homeops.NormalizeRegion(q.Get("region"))
	if !ok {
		reply(w, nil, badRequest("invalid region", nil))
		return
	}
	body, err := srv.homeSlotsBody(r.Context(), when, lang, region)
	if err != nil {
		reply(w, nil, err)
		return
	}
	as, err := srv.home.LiveAnnouncements(r.Context(), when, lang, region)
	if err != nil {
		reply(w, nil, err)
		return
	}
	body["announcements"] = announcementViews(as, lang)
	body["at"] = when.UTC()
	reply(w, body, nil)
}

func (srv *Server) adminAnnouncements(w http.ResponseWriter, r *http.Request) {
	items, err := srv.home.Announcements(r.Context())
	reply(w, map[string]any{"items": items, "now": time.Now().UTC()}, err)
}

func (srv *Server) createAnnouncement(w http.ResponseWriter, r *http.Request) {
	var in store.AnnouncementInput
	srv.adminOpsWrite(w, r, &in, func(actor string) (any, error) {
		return srv.home.CreateAnnouncement(r.Context(), in, actor)
	})
}

func (srv *Server) updateAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in store.AnnouncementInput
	srv.adminOpsWrite(w, r, &in, func(actor string) (any, error) {
		return srv.home.UpdateAnnouncement(r.Context(), id, in, actor)
	})
}

func (srv *Server) deleteAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.adminOpsWrite(w, r, nil, func(actor string) (any, error) {
		err := srv.home.DeleteAnnouncement(r.Context(), id, actor)
		return map[string]any{"id": id, "deleted": err == nil}, err
	})
}
