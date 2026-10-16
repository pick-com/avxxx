package api

// Search (PRD 4.5 Browse / MobileSearchTyping, PRD 11 AdminSearch; tech
// plan 3.4, 3.13).
//
// Public:
//
//	GET /api/catalog/search?q=&type=&counts=1&limit=&lang=   typed catalog search (catalog.go)
//	GET /api/search/hot?limit=                热搜词 / 大家都在搜 (24 h board)
//	GET /api/search/suggest?q=&limit=&lang=   typed suggestions while typing
//	GET|POST|DELETE /api/me/search-history    signed-in 最近搜索 (bearer token)
//
// Admin (AdminSearch, bearer ADMIN_TOKEN, actor from X-Admin-User):
//
//	GET  /api/admin/search/zero-results?days=7&min=20&limit=50
//	GET  /api/admin/search/stats?days=7
//	GET|POST /api/admin/search/synonyms, PUT|DELETE /api/admin/search/synonyms/{id}
//	GET|POST /api/admin/search/redirects, PUT|DELETE /api/admin/search/redirects/{id}
//	GET|POST /api/admin/search/blocklist, DELETE /api/admin/search/blocklist/{id}
//	POST /api/admin/search/terms/alias          add a performer alias from a term
//	POST /api/admin/search/terms/english-title  add a reviewed English title from a term

import (
	"avxxx/internal/i18n"
	"avxxx/internal/search"
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// searchStore is the subset of *store.Store the search routes use, so the
// handlers can be tested without a database.
type searchStore interface {
	SearchWith(ctx context.Context, q string, limit int, o store.SearchOptions) (store.SearchResult, error)
	Suggest(ctx context.Context, q string, limit int) ([]map[string]any, error)
	HotSearches(ctx context.Context, period string, limit int) ([]store.HotSearch, time.Time, error)

	ZeroResultTerms(ctx context.Context, days, minZero, limit int) ([]store.ZeroResultTerm, error)
	SearchStats(ctx context.Context, days int) (store.SearchStats, error)
	SearchSynonyms(ctx context.Context, q string) ([]store.SearchSynonym, error)
	CreateSearchSynonym(ctx context.Context, term, canonical, actor string) (store.SearchSynonym, error)
	UpdateSearchSynonym(ctx context.Context, id int64, term, canonical string) (store.SearchSynonym, error)
	DeleteSearchSynonym(ctx context.Context, id int64) error
	SearchRedirects(ctx context.Context, q string) ([]store.SearchRedirectRule, error)
	CreateSearchRedirect(ctx context.Context, term, target, reason, actor string) (store.SearchRedirectRule, error)
	UpdateSearchRedirect(ctx context.Context, id int64, term, target, reason string) (store.SearchRedirectRule, error)
	DeleteSearchRedirect(ctx context.Context, id int64) error
	SearchBlocklist(ctx context.Context) ([]store.SearchBlockedTerm, error)
	BlockSearchTerm(ctx context.Context, term, reason, actor string) (store.SearchBlockedTerm, error)
	UnblockSearchTerm(ctx context.Context, id int64) error
	AddPerformerAliasFromTerm(ctx context.Context, performerID int64, term, name, reason, actor string) (store.TermFix, error)
	AddEnglishTitleFromTerm(ctx context.Context, releaseID int64, term, title, reason, actor string) (store.TermFix, error)
}

func (srv *Server) searchRoutes() []route {
	return []route{
		{"GET /api/search/hot", srv.searchHot},
		{"GET /api/search/suggest", srv.searchSuggest},
		{"GET /api/admin/search/zero-results", srv.admin(srv.searchZeroResults)},
		{"GET /api/admin/search/stats", srv.admin(srv.searchStats)},
		{"GET /api/admin/search/synonyms", srv.admin(srv.listSynonyms)},
		{"POST /api/admin/search/synonyms", srv.admin(srv.createSynonym)},
		{"PUT /api/admin/search/synonyms/{id}", srv.admin(srv.updateSynonym)},
		{"DELETE /api/admin/search/synonyms/{id}", srv.admin(srv.deleteSynonym)},
		{"GET /api/admin/search/redirects", srv.admin(srv.listRedirects)},
		{"POST /api/admin/search/redirects", srv.admin(srv.createRedirect)},
		{"PUT /api/admin/search/redirects/{id}", srv.admin(srv.updateRedirect)},
		{"DELETE /api/admin/search/redirects/{id}", srv.admin(srv.deleteRedirect)},
		{"GET /api/admin/search/blocklist", srv.admin(srv.listBlocklist)},
		{"POST /api/admin/search/blocklist", srv.admin(srv.blockTerm)},
		{"DELETE /api/admin/search/blocklist/{id}", srv.admin(srv.unblockTerm)},
		{"POST /api/admin/search/terms/alias", srv.admin(srv.aliasFromTerm)},
		{"POST /api/admin/search/terms/english-title", srv.admin(srv.englishTitleFromTerm)},
		{"GET /api/me/search-history", srv.user(srv.searchHistory)},
		{"POST /api/me/search-history", srv.user(srv.recordSearch)},
		{"DELETE /api/me/search-history", srv.user(srv.clearSearchHistory)},
	}
}

// withSearch guards a missing store (tests without a database).
func (srv *Server) withSearch(w http.ResponseWriter) bool {
	if srv.search == nil {
		reply(w, nil, errUnavailable)
		return false
	}
	return true
}

// catalogSearch: GET /api/catalog/search?q=&limit=&lang=. Items keep
// {type,id,slug,name,...} (performers first, then releases; an exact
// product-code release before both) and add "match" (code | name | alias |
// title | translation | fuzzy). The envelope adds "query" (normalized),
// "effective_query" when synonyms or the modifier-stripped retry changed
// it, "rescued": true when only the retry found results, "redirect"
// ({target_url, reason}) when a search_redirects rule matches, and
// "exact_match" ({type,id,slug}) for a single product-code hit.
// ?type=performer|release returns only that kind (Search results 类型);
// ?counts=1 adds "counts" ({performer, release}, each capped at 1000).
func (srv *Server) catalogSearch(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	limit, err := limitParam(r.URL.Query())
	if err != nil {
		reply(w, nil, err)
		return
	}
	opts := store.SearchOptions{Type: strings.TrimSpace(r.URL.Query().Get("type")), Counts: r.URL.Query().Get("counts") == "1"}
	if opts.Type != "" && opts.Type != store.SearchTypePerformer && opts.Type != store.SearchTypeRelease {
		reply(w, nil, badRequest("type must be performer or release", nil))
		return
	}
	if !srv.withSearch(w) {
		return
	}
	res, err := srv.search.SearchWith(r.Context(), r.URL.Query().Get("q"), limit, opts)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, searchRefs(res.Items))
	localizeSearch(l, res.Items)
	out := map[string]any{"items": res.Items, "query": res.Query}
	if res.EffectiveQuery != "" {
		out["effective_query"] = res.EffectiveQuery
	}
	if res.Rescued {
		out["rescued"] = true
	}
	if res.Redirect != nil {
		out["redirect"] = res.Redirect
	}
	if res.ExactCode != nil {
		out["exact_match"] = res.ExactCode
	}
	if res.Counts != nil {
		out["counts"] = res.Counts
	}
	reply(w, withLang(out, l), err)
}

// searchHot: GET /api/search/hot?limit= (default and max 20).
func (srv *Server) searchHot(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r.URL.Query(), "limit", 1000)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.withSearch(w) {
		return
	}
	items, at, err := srv.search.HotSearches(r.Context(), search.HotPeriod, limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var computed any
	if !at.IsZero() {
		computed = at.UTC()
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	reply(w, map[string]any{"period": search.HotPeriod, "items": items, "computed_at": computed}, nil)
}

// searchSuggest: GET /api/search/suggest?q=&limit=&lang= (limit default and
// max 8). Items: {type: release|performer|upcoming|tag|query, name, id?,
// slug?, release_date? | scheduled_date?}; ?lang= adds display_name.
func (srv *Server) searchSuggest(w http.ResponseWriter, r *http.Request) {
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	q := r.URL.Query()
	limit, err := intParam(q, "limit", 1000)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if utf8.RuneCountInString(q.Get("q")) > search.MaxQueryRunes {
		reply(w, nil, badRequest("q too long", nil))
		return
	}
	if !srv.withSearch(w) {
		return
	}
	items, err := srv.search.Suggest(r.Context(), q.Get("q"), limit)
	if err != nil {
		reply(w, nil, err)
		return
	}
	l, err := srv.localizer(r.Context(), lang, func() []i18n.Ref {
		refs := make([]i18n.Ref, 0, len(items))
		for _, m := range items {
			if et, id, f, ok := suggestRef(m); ok {
				refs = append(refs, ref(et, id, f))
			}
		}
		return refs
	})
	if l != nil {
		for _, m := range items {
			if et, id, f, ok := suggestRef(m); ok {
				v, vl := l.get(et, id, f, mapString(m, "name"))
				setDisplay(m, "name", v, vl)
			}
		}
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	reply(w, withLang(map[string]any{"q": search.NormalizeQuery(q.Get("q")), "items": items}, l), err)
}

// searchHistoryStore backs the signed-in recent searches.
type searchHistoryStore interface {
	SearchHistory(ctx context.Context, userID int64, limit int) ([]string, error)
	RecordSearch(ctx context.Context, userID int64, q string) error
	ClearSearchHistory(ctx context.Context, userID int64) (int64, error)
}

func (srv *Server) withSearchHistory(w http.ResponseWriter) bool {
	if srv.searchHist == nil {
		reply(w, nil, errUnavailable)
		return false
	}
	return true
}

// searchHistory: GET /api/me/search-history — the user's recent searches,
// newest first (MobileSearchTyping 最近搜索).
func (srv *Server) searchHistory(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.withSearchHistory(w) {
		return
	}
	v, err := srv.searchHist.SearchHistory(r.Context(), uid, store.SearchHistoryKeep)
	reply(w, map[string]any{"items": v}, err)
}

// recordSearch: POST /api/me/search-history {q} stores a search (normalized;
// the newest 20 are kept).
func (srv *Server) recordSearch(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		Q string `json:"q"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if utf8.RuneCountInString(in.Q) > search.MaxQueryRunes {
		reply(w, nil, badRequest("q too long", nil))
		return
	}
	q := search.NormalizeQuery(in.Q)
	if q == "" {
		reply(w, nil, badRequest("q is required", nil))
		return
	}
	if !srv.withSearchHistory(w) {
		return
	}
	reply(w, map[string]any{"saved": true, "q": q}, srv.searchHist.RecordSearch(r.Context(), uid, q))
}

// clearSearchHistory: DELETE /api/me/search-history (最近搜索 · 清除).
func (srv *Server) clearSearchHistory(w http.ResponseWriter, r *http.Request, uid int64) {
	if !srv.withSearchHistory(w) {
		return
	}
	n, err := srv.searchHist.ClearSearchHistory(r.Context(), uid)
	reply(w, map[string]any{"deleted": n}, err)
}

// suggestRef is the translatable field of a suggestion.
func suggestRef(m map[string]any) (entityType, id, field string, ok bool) {
	switch mapString(m, "type") {
	case search.SuggestRelease, search.SuggestUpcoming:
		return i18n.EntityRelease, mapID(m), "title", true
	case search.SuggestPerformer:
		return i18n.EntityPerformer, mapID(m), "name", true
	case search.SuggestTag:
		return i18n.EntityTag, mapString(m, "slug"), "name", mapString(m, "slug") != ""
	}
	return "", "", "", false
}

// ---- admin ----

// boundedInt parses an optional integer in [lo, hi]; missing means def.
func boundedInt(r *http.Request, name string, def, lo, hi int) (int, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, badRequest("invalid "+name, err)
	}
	return n, nil
}

// searchZeroResults: ?days=7 (1-90) &min=20 (zero-result searches, >= 1)
// &limit=50 (1-100). Each term carries searches, zero_results, rescued,
// the stripped retry (stripped, would_rescue), resolved (matches now),
// near_matches and the heuristic class with its label and fix actions.
func (srv *Server) searchZeroResults(w http.ResponseWriter, r *http.Request) {
	days, err := boundedInt(r, "days", 7, 1, 90)
	if err != nil {
		reply(w, nil, err)
		return
	}
	minZero, err := boundedInt(r, "min", 20, 1, 1_000_000)
	if err != nil {
		reply(w, nil, err)
		return
	}
	limit, err := boundedInt(r, "limit", 50, 1, 100)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.withSearch(w) {
		return
	}
	items, err := srv.search.ZeroResultTerms(r.Context(), days, minZero, limit)
	reply(w, map[string]any{"days": days, "min": minZero, "items": items}, err)
}

// searchStats: ?days=7 (1-90). Totals and per-day searches, zero-result
// rate against the < 5% target, CTR, first-page CTR and rescue rate.
func (srv *Server) searchStats(w http.ResponseWriter, r *http.Request) {
	days, err := boundedInt(r, "days", 7, 1, 90)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.withSearch(w) {
		return
	}
	st, err := srv.search.SearchStats(r.Context(), days)
	reply(w, st, err)
}

// termParam normalizes a required term (rule key) from a body.
func termParam(name, raw string) (string, error) {
	t := search.NormalizeQuery(raw)
	if t == "" {
		return "", badRequest(name+" is required", nil)
	}
	if utf8.RuneCountInString(strings.TrimSpace(raw)) > search.MaxQueryRunes {
		return "", badRequest(name+" too long", nil)
	}
	return t, nil
}

// adminSearchCall resolves the actor and decodes a strict JSON body.
func (srv *Server) adminSearchCall(w http.ResponseWriter, r *http.Request, body any) (string, bool) {
	actor, err := adminActor(r)
	if err == nil && body != nil {
		err = decodeStrict(w, r, body, false)
	}
	if err != nil {
		reply(w, nil, err)
		return "", false
	}
	return actor, srv.withSearch(w)
}

type synonymBody struct {
	Term      string `json:"term"`
	Canonical string `json:"canonical"`
}

func (b synonymBody) clean() (string, string, error) {
	term, err := termParam("term", b.Term)
	if err != nil {
		return "", "", err
	}
	canonical, err := termParam("canonical", b.Canonical)
	if err != nil {
		return "", "", err
	}
	if term == canonical {
		return "", "", badRequest("term and canonical are the same", nil)
	}
	return term, canonical, nil
}

func (srv *Server) listSynonyms(w http.ResponseWriter, r *http.Request) {
	if !srv.withSearch(w) {
		return
	}
	v, err := srv.search.SearchSynonyms(r.Context(), r.URL.Query().Get("q"))
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) createSynonym(w http.ResponseWriter, r *http.Request) {
	var b synonymBody
	actor, ok := srv.adminSearchCall(w, r, &b)
	if !ok {
		return
	}
	term, canonical, err := b.clean()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.CreateSearchSynonym(r.Context(), term, canonical, actor)
	replyCreated(w, v, err)
}

func (srv *Server) updateSynonym(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var b synonymBody
	if _, ok := srv.adminSearchCall(w, r, &b); !ok {
		return
	}
	term, canonical, err := b.clean()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.UpdateSearchSynonym(r.Context(), id, term, canonical)
	reply(w, v, err)
}

func (srv *Server) deleteSynonym(w http.ResponseWriter, r *http.Request) {
	srv.deleteRule(w, r, func(ctx context.Context, id int64) error { return srv.search.DeleteSearchSynonym(ctx, id) })
}

type redirectBody struct {
	Term      string `json:"term"`
	TargetURL string `json:"target_url"`
	Reason    string `json:"reason"`
}

func (b redirectBody) clean() (string, string, string, error) {
	term, err := termParam("term", b.Term)
	if err != nil {
		return "", "", "", err
	}
	target := strings.TrimSpace(b.TargetURL)
	if !search.ValidRedirectTarget(target) {
		return "", "", "", badRequest("target_url must be a site path (/...) or an https URL", nil)
	}
	reason := strings.TrimSpace(b.Reason)
	if utf8.RuneCountInString(reason) > 500 {
		return "", "", "", badRequest("reason too long", nil)
	}
	return term, target, reason, nil
}

func (srv *Server) listRedirects(w http.ResponseWriter, r *http.Request) {
	if !srv.withSearch(w) {
		return
	}
	v, err := srv.search.SearchRedirects(r.Context(), r.URL.Query().Get("q"))
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) createRedirect(w http.ResponseWriter, r *http.Request) {
	var b redirectBody
	actor, ok := srv.adminSearchCall(w, r, &b)
	if !ok {
		return
	}
	term, target, reason, err := b.clean()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.CreateSearchRedirect(r.Context(), term, target, reason, actor)
	replyCreated(w, v, err)
}

func (srv *Server) updateRedirect(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var b redirectBody
	if _, ok := srv.adminSearchCall(w, r, &b); !ok {
		return
	}
	term, target, reason, err := b.clean()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.UpdateSearchRedirect(r.Context(), id, term, target, reason)
	reply(w, v, err)
}

func (srv *Server) deleteRedirect(w http.ResponseWriter, r *http.Request) {
	srv.deleteRule(w, r, func(ctx context.Context, id int64) error { return srv.search.DeleteSearchRedirect(ctx, id) })
}

type blockBody struct {
	Term   string `json:"term"`
	Reason string `json:"reason"`
}

func (srv *Server) listBlocklist(w http.ResponseWriter, r *http.Request) {
	if !srv.withSearch(w) {
		return
	}
	v, err := srv.search.SearchBlocklist(r.Context())
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) blockTerm(w http.ResponseWriter, r *http.Request) {
	var b blockBody
	actor, ok := srv.adminSearchCall(w, r, &b)
	if !ok {
		return
	}
	term, err := termParam("term", b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.BlockSearchTerm(r.Context(), term, strings.TrimSpace(b.Reason), actor)
	replyCreated(w, v, err)
}

func (srv *Server) unblockTerm(w http.ResponseWriter, r *http.Request) {
	srv.deleteRule(w, r, func(ctx context.Context, id int64) error { return srv.search.UnblockSearchTerm(ctx, id) })
}

func (srv *Server) deleteRule(w http.ResponseWriter, r *http.Request, del func(context.Context, int64) error) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if _, ok := srv.adminSearchCall(w, r, nil); !ok {
		return
	}
	if err = del(r.Context(), id); err != nil {
		reply(w, nil, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// replyCreated answers 201 with v, or the mapped error.
func replyCreated(w http.ResponseWriter, v any, err error) {
	if err == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
	}
	reply(w, v, err)
}

// termFixBody: {term, performer_id | release_id, alias | title (default:
// the term as typed), reason}.
type termFixBody struct {
	Term        string `json:"term"`
	PerformerID int64  `json:"performer_id"`
	ReleaseID   int64  `json:"release_id"`
	Alias       string `json:"alias"`
	Title       string `json:"title"`
	Reason      string `json:"reason"`
}

// fixValue is the explicit value or, without one, the term as typed.
func fixValue(explicit, term string) (string, error) {
	v := strings.Join(strings.Fields(explicit), " ")
	if v == "" {
		v = strings.Join(strings.Fields(term), " ")
	}
	if v == "" {
		return "", badRequest("value is required", nil)
	}
	if utf8.RuneCountInString(v) > 300 {
		return "", badRequest("value too long", nil)
	}
	return v, nil
}

// aliasFromTerm: POST /api/admin/search/terms/alias {term, performer_id,
// alias?, reason?} adds a performer alias (review_events 'add_alias').
func (srv *Server) aliasFromTerm(w http.ResponseWriter, r *http.Request) {
	var b termFixBody
	actor, ok := srv.adminSearchCall(w, r, &b)
	if !ok {
		return
	}
	term, err := termParam("term", b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if b.PerformerID <= 0 {
		reply(w, nil, badRequest("performer_id is required", nil))
		return
	}
	name, err := fixValue(b.Alias, b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.AddPerformerAliasFromTerm(r.Context(), b.PerformerID, term, name, strings.TrimSpace(b.Reason), actor)
	replyCreated(w, v, err)
}

// englishTitleFromTerm: POST /api/admin/search/terms/english-title {term,
// release_id, title?, reason?} stores a reviewed English title translation
// (review_events 'add_english_title').
func (srv *Server) englishTitleFromTerm(w http.ResponseWriter, r *http.Request) {
	var b termFixBody
	actor, ok := srv.adminSearchCall(w, r, &b)
	if !ok {
		return
	}
	term, err := termParam("term", b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if b.ReleaseID <= 0 {
		reply(w, nil, badRequest("release_id is required", nil))
		return
	}
	title, err := fixValue(b.Title, b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.search.AddEnglishTitleFromTerm(r.Context(), b.ReleaseID, term, title, strings.TrimSpace(b.Reason), actor)
	replyCreated(w, v, err)
}
