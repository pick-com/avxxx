package api

// Recommendations V1 (PRD 4.1, 4.2, 6.1, 6.2, 10, 18; tech plan 3.5):
//
//	GET /api/recs?slot=home|related|similar_performers|performers|tonight
//	    &release=<slug>&performer=<slug>&limit=&cursor=&lang=
//	    &exclude=<id,id>&duration=any|30|60|120|series   (tonight)
//	GET /api/trending?period=today|week|month|year&tag=&limit=&cursor=&lang=
//	GET /api/trending/performers?period=&limit=&cursor=&lang=
//	GET/PUT /api/admin/similarity-weights
//	POST /api/admin/recs/rebuild?job=all|similarity|trending
//
// Everything heavy is precomputed by the recs jobs (internal/recs); a
// request is one batched recall round trip, one hydration query and, with
// a language, one translation lookup. The bearer token is optional: with a
// valid one the user's history, follows and watched filter apply; without
// (or with an invalid one) the visitor recall (trending + new) is used.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// recsStore is the subset of *store.Store used by the public recommendation
// routes, as an interface so they can be tested without a database.
type recsStore interface {
	ReleaseIDBySlug(ctx context.Context, slug string) (int64, error)
	PerformerIDBySlug(ctx context.Context, slug string) (int64, error)
	Recall(ctx context.Context, q store.RecQuery) (store.RecallResult, error)
	RecInfos(ctx context.Context, ids []int64, seed int64, lang string) (map[int64]recs.Info, error)
	TrendingBoard(ctx context.Context, period, tag string, offset, limit int) ([]store.BoardRow, error)
	TrendingTags(ctx context.Context, period string, limit int) ([]store.TagCount, error)
	PerformerHeatBoard(ctx context.Context, period string, offset, limit int) ([]store.BoardRow, error)
	SimilarPerformers(ctx context.Context, id int64, offset, limit int) ([]store.PerformerRec, error)
	RecommendedPerformers(ctx context.Context, userID int64, offset, limit int) ([]store.PerformerRec, error)
}

// recsAdmin backs the admin weights and rebuild routes.
type recsAdmin interface {
	SimilarityWeightsRow(ctx context.Context) (store.SimilarityWeightsRow, error)
	SetSimilarityWeights(ctx context.Context, w recs.Weights, by string) (store.SimilarityWeightsRow, error)
	EnqueueSourceless(ctx context.Context, kind string) (int64, bool, error)
}

const (
	recsDefaultLimit = 20
	recsMaxLimit     = 50
	recsMaxExclude   = 50
	recsCursorSort   = "recs"
	heatOnFirstPage  = 10
	tagsOnFirstPage  = 12
)

func (srv *Server) recsRoutes() []route {
	return []route{
		{"GET /api/recs", srv.withRecs(srv.getRecs)},
		{"GET /api/trending", srv.withRecs(srv.getTrending)},
		{"GET /api/trending/performers", srv.withRecs(srv.getPerformerHeat)},
		{"GET /api/admin/similarity-weights", srv.admin(srv.withRecsAdmin(srv.getSimilarityWeights))},
		{"PUT /api/admin/similarity-weights", srv.admin(srv.withRecsAdmin(srv.putSimilarityWeights))},
		{"POST /api/admin/recs/rebuild", srv.admin(srv.withRecsAdmin(srv.rebuildRecs))},
	}
}

func (srv *Server) withRecs(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.recs == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

func (srv *Server) withRecsAdmin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.recsAdmin == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// recsParams is a parsed /api/recs request.
type recsParams struct {
	slot      string
	release   string
	performer string
	limit     int
	offset    int
	period    string
	duration  string
	exclude   []int64
}

// parseRecsParams validates the query string; it never touches the store.
func parseRecsParams(q url.Values) (recsParams, error) {
	p := recsParams{
		slot:      strings.TrimSpace(q.Get("slot")),
		release:   strings.TrimSpace(q.Get("release")),
		performer: strings.TrimSpace(q.Get("performer")),
		period:    strings.TrimSpace(q.Get("period")),
		duration:  strings.TrimSpace(q.Get("duration")),
	}
	if !recs.ValidSlot(p.slot) {
		return p, badRequest("slot must be home, related, similar_performers, performers or tonight", nil)
	}
	if p.slot == recs.SlotRelated && p.release == "" {
		return p, badRequest("slot related needs release", nil)
	}
	if p.slot == recs.SlotSimilarPerformers && p.performer == "" {
		return p, badRequest("slot similar_performers needs performer", nil)
	}
	if p.period == "" {
		p.period = recs.PeriodWeek
	} else if _, ok := recs.PeriodWindow(p.period); !ok {
		return p, badRequest("period must be today, week, month or year", nil)
	}
	if !recs.ValidDuration(p.duration) {
		return p, badRequest("duration must be any, 30, 60, 120 or series", nil)
	}
	var err error
	if p.limit, err = limitParam(q); err != nil {
		return p, err
	}
	if p.limit == 0 {
		p.limit = recsDefaultLimit
	}
	p.limit = min(p.limit, recsMaxLimit)
	if p.offset, err = offsetCursor(q); err != nil {
		return p, err
	}
	if raw := strings.TrimSpace(q.Get("exclude")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || id <= 0 {
				return p, badRequest("exclude must be comma-separated release ids", err)
			}
			p.exclude = append(p.exclude, id)
		}
		if len(p.exclude) > recsMaxExclude {
			return p, badRequest("too many exclude ids", nil)
		}
	}
	return p, nil
}

// offsetCursor decodes the opaque offset cursor of the ranked lists.
func offsetCursor(q url.Values) (int, error) {
	c, err := cursorParam(q, recsCursorSort)
	if err != nil || c == nil {
		return 0, err
	}
	n, err := strconv.Atoi(c.Key)
	if err != nil || n < 0 || n > 10000 {
		return 0, badRequest("invalid cursor", err)
	}
	return n, nil
}

func nextOffsetCursor(offset, n, limit, total int) string {
	if n < limit || offset+n >= total {
		return ""
	}
	return store.EncodeCursor(store.Cursor{Sort: recsCursorSort, Key: strconv.Itoa(offset + n), ID: 1})
}

// recPerformer is the primary performer of a recommended release.
type recPerformer struct {
	ID              int64  `json:"id"`
	Slug            string `json:"slug"`
	Name            string `json:"name"`
	DisplayName     string `json:"display_name,omitempty"`
	DisplayNameLang string `json:"display_name_lang,omitempty"`
}

// recItem is one recommended release in a response.
type recItem struct {
	recs.Info
	DisplayTitle     string        `json:"display_title,omitempty"`
	DisplayTitleLang string        `json:"display_title_lang,omitempty"`
	Performer        *recPerformer `json:"performer"`
	Score            float64       `json:"score"`
	Reason           recs.Reason   `json:"reason"`
	Channels         []string      `json:"channels,omitempty"`
}

func (srv *Server) getRecs(w http.ResponseWriter, r *http.Request) {
	p, err := parseRecsParams(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	uid := srv.viewerID(r)
	if uid != 0 {
		w.Header().Set("Cache-Control", "private, no-store")
	}
	ctx := r.Context()
	switch p.slot {
	case recs.SlotSimilarPerformers, recs.SlotPerformers:
		srv.performerRecs(w, r, p, uid, lang)
		return
	}
	var seed int64
	if p.slot == recs.SlotRelated {
		if seed, err = srv.recs.ReleaseIDBySlug(ctx, p.release); err != nil {
			reply(w, nil, err)
			return
		}
	}
	rec, err := srv.recs.Recall(ctx, store.RecQuery{Slot: p.slot, UserID: uid, SeedID: seed, Period: p.period})
	if err != nil {
		reply(w, nil, err)
		return
	}
	ids := candidateIDs(rec.Candidates, p.exclude)
	infos, err := srv.recs.RecInfos(ctx, ids, seed, lang)
	if err != nil {
		reply(w, nil, err)
		return
	}
	opt := recs.Options{Seed: seed, Watched: rec.Watched, RequirePlayable: rec.RequirePlayable, Exclude: map[int64]bool{}}
	for _, id := range p.exclude {
		opt.Exclude[id] = true
		if info, ok := infos[id]; ok {
			opt.Demote = append(opt.Demote, info)
		}
	}
	if p.slot == recs.SlotTonight {
		opt.Keep = recs.TonightKeep(p.duration)
	} else {
		opt.Demote = nil
	}
	ranked := recs.Rank(rec.Candidates, infos, opt)

	if p.slot == recs.SlotTonight {
		pick, alts := recs.Tonight(ranked)
		shown := alts
		if pick != nil {
			shown = append([]recs.Rec{*pick}, alts...)
		}
		items, l, err := srv.recItems(ctx, lang, shown)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out := map[string]any{"slot": p.slot, "item": nil, "alternates": []recItem{}, "duration": orAny(p.duration), "exclude": nonNil(p.exclude)}
		if pick != nil {
			out["item"], out["alternates"] = items[0], items[1:]
		}
		reply(w, withLang(out, l), nil)
		return
	}
	end := min(len(ranked), p.offset+p.limit)
	pageRecs := ranked[min(p.offset, end):end]
	items, l, err := srv.recItems(ctx, lang, pageRecs)
	if err != nil {
		reply(w, nil, err)
		return
	}
	out := page(items, nextOffsetCursor(p.offset, len(items), p.limit, len(ranked)))
	out["slot"] = p.slot
	reply(w, withLang(out, l), nil)
}

func orAny(d string) string {
	if d == "" {
		return recs.DurationAny
	}
	return d
}

func nonNil(x []int64) []int64 {
	if x == nil {
		return []int64{}
	}
	return x
}

// candidateIDs lists distinct candidate releases plus the excluded ones
// (hydrated so tonight can demote their maker / performer).
func candidateIDs(cands []recs.Candidate, exclude []int64) []int64 {
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, c := range cands {
		add(c.ReleaseID)
	}
	for _, id := range exclude {
		add(id)
	}
	return ids
}

// recItems localizes a page of recs: display titles, performer display
// names, and reasons rebuilt with the localized seed titles and names.
func (srv *Server) recItems(ctx context.Context, lang string, rs []recs.Rec) ([]recItem, *localizer, error) {
	l, err := srv.localizer(ctx, lang, func() []i18n.Ref {
		var refs []i18n.Ref
		for _, x := range rs {
			refs = append(refs, ref(i18n.EntityRelease, idText(x.ReleaseID), "title"))
			if x.Best.SeedID > 0 {
				refs = append(refs, ref(i18n.EntityRelease, idText(x.Best.SeedID), "title"))
			}
			if x.PerformerID > 0 {
				refs = append(refs, ref(i18n.EntityPerformer, idText(x.PerformerID), "name"))
			}
			if x.Best.PerformerID > 0 {
				refs = append(refs, ref(i18n.EntityPerformer, idText(x.Best.PerformerID), "name"))
			}
		}
		return refs
	})
	if err != nil {
		return nil, nil, err
	}
	out := make([]recItem, len(rs))
	for i, x := range rs {
		it := recItem{Info: x.Info, Score: x.Score, Reason: x.Reason, Channels: x.Channels}
		if x.PerformerID > 0 {
			it.Performer = &recPerformer{ID: x.PerformerID, Slug: x.PerformerSlug, Name: x.PerformerName}
		}
		if l != nil {
			it.DisplayTitle, it.DisplayTitleLang = l.get(i18n.EntityRelease, idText(x.ReleaseID), "title", x.Title)
			info, best := x.Info, x.Best
			if it.Performer != nil {
				it.Performer.DisplayName, it.Performer.DisplayNameLang = l.get(i18n.EntityPerformer, idText(x.PerformerID), "name", x.PerformerName)
				info.PerformerName = it.Performer.DisplayName
			}
			if best.SeedID > 0 {
				best.SeedTitle, _ = l.get(i18n.EntityRelease, idText(best.SeedID), "title", best.SeedTitle)
			}
			if best.PerformerID > 0 {
				best.PerformerName, _ = l.get(i18n.EntityPerformer, idText(best.PerformerID), "name", best.PerformerName)
			}
			it.Reason = recs.ReasonFor(best, info)
		}
		out[i] = it
	}
	return out, l, nil
}

// performerItem is one recommended / similar performer in a response.
type performerItem struct {
	store.PerformerRec
	DisplayName     string      `json:"display_name,omitempty"`
	DisplayNameLang string      `json:"display_name_lang,omitempty"`
	Reason          recs.Reason `json:"reason"`
}

func (srv *Server) performerRecs(w http.ResponseWriter, r *http.Request, p recsParams, uid int64, lang string) {
	ctx := r.Context()
	var rows []store.PerformerRec
	var err error
	hot := false
	var heat []store.BoardRow
	switch {
	case p.slot == recs.SlotSimilarPerformers:
		var id int64
		if id, err = srv.recs.PerformerIDBySlug(ctx, p.performer); err != nil {
			reply(w, nil, err)
			return
		}
		rows, err = srv.recs.SimilarPerformers(ctx, id, p.offset, p.limit)
	case uid != 0:
		rows, err = srv.recs.RecommendedPerformers(ctx, uid, p.offset, p.limit)
	}
	if err != nil {
		reply(w, nil, err)
		return
	}
	// Visitors, and users with nothing to go on yet: 热门演员.
	if p.slot == recs.SlotPerformers && len(rows) == 0 && (uid == 0 || p.offset == 0) {
		hot = true
		if heat, err = srv.recs.PerformerHeatBoard(ctx, p.period, p.offset, p.limit); err != nil {
			reply(w, nil, err)
			return
		}
		for _, h := range heat {
			rows = append(rows, store.PerformerRec{ID: h.ID, Slug: h.Slug, Name: h.Name, Score: h.Score})
		}
	}
	l, err := srv.localizer(ctx, lang, func() []i18n.Ref {
		var refs []i18n.Ref
		for _, x := range rows {
			refs = append(refs, ref(i18n.EntityPerformer, idText(x.ID), "name"))
			if x.ViaID > 0 {
				refs = append(refs, ref(i18n.EntityPerformer, idText(x.ViaID), "name"))
			}
		}
		return refs
	})
	if err != nil {
		reply(w, nil, err)
		return
	}
	items := make([]performerItem, len(rows))
	for i, x := range rows {
		it := performerItem{PerformerRec: x}
		via := x.ViaName
		if l != nil {
			it.DisplayName, it.DisplayNameLang = l.get(i18n.EntityPerformer, idText(x.ID), "name", x.Name)
			if x.ViaID > 0 {
				via, _ = l.get(i18n.EntityPerformer, idText(x.ViaID), "name", x.ViaName)
			}
		}
		switch {
		case hot:
			it.Reason = recs.Reason{Code: "hot_performer", Text: recs.PeriodLabel(p.period) + "演员 · 第 " + strconv.Itoa(heat[i].Rank) + " 名"}
		case p.slot == recs.SlotSimilarPerformers:
			it.Reason = recs.Reason{Code: "similar_performer", Text: recs.PerformerReason(derefEvidence(x.Evidence))}
		case x.ViaFollow:
			it.Reason = recs.Reason{Code: "similar_to_followed", Text: "与你关注的 " + via + " 相似"}
		default:
			it.Reason = recs.Reason{Code: "similar_to_watched", Text: "因为你看过 " + via + " 的作品"}
		}
		items[i] = it
	}
	next := ""
	if len(items) == p.limit {
		next = store.EncodeCursor(store.Cursor{Sort: recsCursorSort, Key: strconv.Itoa(p.offset + len(items)), ID: 1})
	}
	out := page(items, next)
	out["slot"] = p.slot
	reply(w, withLang(out, l), nil)
}

func derefEvidence(e *recs.Evidence) recs.Evidence {
	if e == nil {
		return recs.Evidence{}
	}
	return *e
}

// boardMeta parses period, limit and cursor for the trending routes.
func boardMeta(q url.Values) (period string, limit, offset int, err error) {
	period = strings.TrimSpace(q.Get("period"))
	if period == "" {
		period = recs.PeriodWeek
	} else if _, ok := recs.PeriodWindow(period); !ok {
		return "", 0, 0, badRequest("period must be today, week, month or year", nil)
	}
	if limit, err = limitParam(q); err != nil {
		return
	}
	if limit == 0 {
		limit = recsDefaultLimit
	}
	limit = min(limit, recsMaxLimit)
	offset, err = offsetCursor(q)
	return
}

// trendingItem is one board row with its movement (▲ ▼ 新上榜).
type trendingItem struct {
	store.BoardRow
	Movement string   `json:"movement"`
	Delta    int      `json:"delta"`
	Release  *recItem `json:"release,omitempty"`
}

type heatItem struct {
	store.BoardRow
	Movement  string       `json:"movement"`
	Delta     int          `json:"delta"`
	Performer recPerformer `json:"performer"`
}

// getTrending is the NavTrending board (PRD 4.2). ?tag= keeps the board's
// releases with that tag (genre chips; ranks stay board-wide). The first
// page also carries the top performer heat rows and "tags", the most
// frequent tags on the board (the chips themselves).
func (srv *Server) getTrending(w http.ResponseWriter, r *http.Request) {
	period, limit, offset, err := boardMeta(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ctx := r.Context()
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	rows, err := srv.recs.TrendingBoard(ctx, period, tag, offset, limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ids := make([]int64, len(rows))
	for i, b := range rows {
		ids[i] = b.ID
	}
	infos, err := srv.recs.RecInfos(ctx, ids, 0, lang)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var shown []recs.Rec
	var kept []store.BoardRow
	for _, b := range rows {
		info, ok := infos[b.ID]
		if !ok {
			continue // held, merged or unpublished since the run
		}
		best := recs.Candidate{ReleaseID: b.ID, Channel: recs.ChanTrending, Label: period, BoardRank: b.Rank}
		shown = append(shown, recs.Rec{Info: info, Score: b.Score, Reason: recs.ReasonFor(best, info), Best: best})
		kept = append(kept, b)
	}
	recItemsOut, l, err := srv.recItems(ctx, lang, shown)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items := make([]trendingItem, len(kept))
	var computed *time.Time
	for i, b := range kept {
		items[i] = trendingItem{BoardRow: b, Release: &recItemsOut[i]}
		items[i].Movement, items[i].Delta = recs.Movement(b.Rank, b.PrevRank)
		if computed == nil {
			t := b.ComputedAt
			computed = &t
		}
	}
	next := ""
	if len(rows) == limit {
		next = store.EncodeCursor(store.Cursor{Sort: recsCursorSort, Key: strconv.Itoa(offset + len(rows)), ID: 1})
	}
	out := page(items, next)
	out["period"], out["computed_at"] = period, computed
	if offset == 0 {
		heat, l2, err := srv.heatItems(ctx, lang, period, 0, heatOnFirstPage)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out["performers"] = heat
		if l == nil {
			l = l2
		}
		tags, err := srv.recs.TrendingTags(ctx, period, tagsOnFirstPage)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out["tags"] = tags
	}
	reply(w, withLang(out, l), nil)
}

// getPerformerHeat is the full performer heat ranking.
func (srv *Server) getPerformerHeat(w http.ResponseWriter, r *http.Request) {
	period, limit, offset, err := boardMeta(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, l, err := srv.heatItems(r.Context(), lang, period, offset, limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	next := ""
	if len(items) == limit {
		next = store.EncodeCursor(store.Cursor{Sort: recsCursorSort, Key: strconv.Itoa(offset + len(items)), ID: 1})
	}
	out := page(items, next)
	out["period"] = period
	reply(w, withLang(out, l), nil)
}

func (srv *Server) heatItems(ctx context.Context, lang, period string, offset, limit int) ([]heatItem, *localizer, error) {
	rows, err := srv.recs.PerformerHeatBoard(ctx, period, offset, limit)
	if err != nil {
		return nil, nil, err
	}
	l, err := srv.localizer(ctx, lang, func() []i18n.Ref {
		refs := make([]i18n.Ref, len(rows))
		for i, b := range rows {
			refs[i] = ref(i18n.EntityPerformer, idText(b.ID), "name")
		}
		return refs
	})
	if err != nil {
		return nil, nil, err
	}
	items := make([]heatItem, len(rows))
	for i, b := range rows {
		it := heatItem{BoardRow: b, Performer: recPerformer{ID: b.ID, Slug: b.Slug, Name: b.Name}}
		it.Movement, it.Delta = recs.Movement(b.Rank, b.PrevRank)
		if l != nil {
			it.Performer.DisplayName, it.Performer.DisplayNameLang = l.get(i18n.EntityPerformer, idText(b.ID), "name", b.Name)
		}
		items[i] = it
	}
	return items, l, nil
}

// ---------------------------------------------------------------- admin

// weightsView is the admin weights payload.
type weightsView struct {
	store.SimilarityWeightsRow
	EffectiveFrom string `json:"effective_from"`
}

const nextNightly = "next_nightly_run"

func (srv *Server) getSimilarityWeights(w http.ResponseWriter, r *http.Request) {
	v, err := srv.recsAdmin.SimilarityWeightsRow(r.Context())
	reply(w, weightsView{v, nextNightly}, err)
}

// putSimilarityWeights saves performer similarity weights (AdminActors /
// A9). Body: {"metadata":0.35,"co_star":0.25,"co_watch":0.4,"visual":0};
// omitted fields keep their current value. Each weight is 0–1 and at least
// one of metadata, co_star, co_watch must be positive. The nightly job reads
// them, so the change takes effect on its next run (or POST
// /api/admin/recs/rebuild).
func (srv *Server) putSimilarityWeights(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Metadata *float64 `json:"metadata"`
		CoStar   *float64 `json:"co_star"`
		CoWatch  *float64 `json:"co_watch"`
		Visual   *float64 `json:"visual"`
	}
	if err := readJSON(r, &body, false); err != nil {
		reply(w, nil, err)
		return
	}
	cur, err := srv.recsAdmin.SimilarityWeightsRow(r.Context())
	if err != nil {
		reply(w, nil, err)
		return
	}
	wt := cur.Weights
	for _, x := range []struct {
		src *float64
		dst *float64
	}{{body.Metadata, &wt.Metadata}, {body.CoStar, &wt.CoStar}, {body.CoWatch, &wt.CoWatch}, {body.Visual, &wt.Visual}} {
		if x.src != nil {
			*x.dst = *x.src
		}
	}
	if msg := wt.Validate(); msg != "" {
		reply(w, nil, badRequest(msg, nil))
		return
	}
	v, err := srv.recsAdmin.SetSimilarityWeights(r.Context(), wt, "admin")
	reply(w, weightsView{v, nextNightly}, err)
}

// rebuildRecs enqueues the recommendation jobs for the cmd/audit worker:
// ?job=all (default) | similarity | trending. 202 either way; enqueued=false
// means that job was already pending or running.
func (srv *Server) rebuildRecs(w http.ResponseWriter, r *http.Request) {
	var kinds []string
	switch strings.TrimSpace(r.URL.Query().Get("job")) {
	case "", "all":
		kinds = []string{recs.JobKindSimilarity, recs.JobKindTrending}
	case "similarity":
		kinds = []string{recs.JobKindSimilarity}
	case "trending":
		kinds = []string{recs.JobKindTrending}
	default:
		reply(w, nil, badRequest("job must be all, similarity or trending", nil))
		return
	}
	jobs := []map[string]any{}
	for _, k := range kinds {
		id, enqueued, err := srv.recsAdmin.EnqueueSourceless(r.Context(), k)
		if err != nil {
			reply(w, nil, err)
			return
		}
		jobs = append(jobs, map[string]any{"kind": k, "job_id": id, "enqueued": enqueued})
	}
	replyStatus(w, http.StatusAccepted, map[string]any{"jobs": jobs})
}
