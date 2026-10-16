package store

// Catalog search (PRD 4.5, tech plan 3.4 / 3.13): query understanding
// (product codes, synonyms, redirects, modifier-stripping retry), title /
// alias / reviewed-translation matching with pg_trgm when it is installed
// (plain ILIKE otherwise), typed suggestions, hot searches, the hourly
// 'search_stats' job's reads and writes, and the AdminSearch board.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/identity"
	"avxxx/internal/search"
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// JobKindSearchStats is jobs.kind of the hourly search statistics job.
const JobKindSearchStats = search.JobKind

var _ search.Store = (*Store)(nil)

// ---- pg_trgm detection ----

var trgmByPool sync.Map // *pgxpool.Pool -> bool

// SearchTrgm reports whether pg_trgm is installed (checked once per pool).
// Without it search matches with ILIKE only and skips fuzzy recall.
func (s *Store) SearchTrgm(ctx context.Context) bool {
	if v, ok := trgmByPool.Load(s.Pool); ok {
		return v.(bool)
	}
	var ok bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_trgm')`).Scan(&ok); err != nil {
		return false // transient; try again next time
	}
	if !ok {
		log.Print("search: pg_trgm unavailable; matching with ILIKE only")
	}
	trgmByPool.Store(s.Pool, ok)
	return ok
}

// likeEscape escapes LIKE metacharacters (backslash is the default escape).
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func containsPattern(q string) string { return "%" + likeEscape(q) + "%" }
func prefixPattern(q string) string   { return likeEscape(q) + "%" }

// fuzzyOK: trigram recall only helps from 3 runes on.
func fuzzyOK(trgm bool, q string) bool { return trgm && utf8.RuneCountInString(q) >= 3 }

// ---- search ----

// SearchRedirect is a search_redirects rule that matched the query.
type SearchRedirect struct {
	TargetURL string `json:"target_url"`
	Reason    string `json:"reason,omitempty"`
}

// SearchResult is the typed catalog search response.
type SearchResult struct {
	Items []map[string]any
	// Query is the normalized input; EffectiveQuery the query that was
	// actually matched when it differs (synonym or stripped retry).
	Query          string
	EffectiveQuery string
	// Rescued: the original query matched nothing and the modifier-stripped
	// retry did.
	Rescued  bool
	Redirect *SearchRedirect
	// ExactCode is the single release a product-code query identified
	// (PRD 3.4 品番精确匹配: the client may jump straight to it).
	ExactCode map[string]any
	// Counts are the per-type match counts of the effective query
	// ({"performer": n, "release": n}, each capped at SearchCountCap) when
	// SearchOptions.Counts asked for them.
	Counts map[string]int
}

// Search result types accepted by SearchOptions.Type.
const (
	SearchTypePerformer = "performer"
	SearchTypeRelease   = "release"
)

// SearchCountCap caps each per-type count (the UI shows "1000+").
const SearchCountCap = 1000

// SearchOptions narrows the catalog search (Search results 类型 filter):
// Type "" searches both kinds, "performer" / "release" only that kind (with
// the full limit, not the performer card cap). Counts adds Counts.
type SearchOptions struct {
	Type   string
	Counts bool
}

// searchRules loads the synonyms for keys and, when redirectTerm is not
// empty, its redirect.
func (s *Store) searchRules(ctx context.Context, keys []string, redirectTerm string) (map[string]string, *SearchRedirect, error) {
	rows, err := s.Query(ctx, `SELECT 's',term,canonical,'' FROM search_synonyms WHERE term=ANY($1::text[])
		UNION ALL SELECT 'r',term,target_url,reason FROM search_redirects WHERE term=$2`, keys, redirectTerm)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	syn := map[string]string{}
	var red *SearchRedirect
	for rows.Next() {
		var kind, term, a, b string
		if err = rows.Scan(&kind, &term, &a, &b); err != nil {
			return nil, nil, err
		}
		if kind == "s" {
			syn[term] = a
		} else {
			red = &SearchRedirect{TargetURL: a, Reason: b}
		}
	}
	return syn, red, rows.Err()
}

func (s *Store) expandSynonyms(ctx context.Context, q string) (string, error) {
	syn, _, err := s.searchRules(ctx, search.SynonymKeys(q), "")
	if err != nil {
		return q, err
	}
	return search.ApplySynonyms(q, syn), nil
}

// maxSearchPerformers caps the performer cards above the videos.
const maxSearchPerformers = 6

// Search runs the catalog search for q: synonyms are applied before
// matching; a product-code query puts its exact releases first; performers
// (name, alias, reviewed name translation) come before releases (title,
// title_ja, reviewed title translation), with pg_trgm fuzzy recall from 3
// runes when available. When nothing matches, the query is retried once
// with modifier words stripped (search.StripModifiers).
func (s *Store) Search(ctx context.Context, q string, limit int) (SearchResult, error) {
	return s.SearchWith(ctx, q, limit, SearchOptions{})
}

// SearchWith is Search with a type filter and optional per-type counts.
func (s *Store) SearchWith(ctx context.Context, q string, limit int, o SearchOptions) (SearchResult, error) {
	switch o.Type {
	case "", SearchTypePerformer, SearchTypeRelease:
	default:
		return SearchResult{}, invalidQuery("unknown type %q", o.Type)
	}
	limit = pageLimit(limit)
	res := SearchResult{Query: search.NormalizeQuery(q), Items: []map[string]any{}}
	if res.Query == "" {
		return res, nil
	}
	syn, red, err := s.searchRules(ctx, search.SynonymKeys(res.Query), res.Query)
	if err != nil {
		return res, err
	}
	res.Redirect = red
	eff := search.ApplySynonyms(res.Query, syn)
	items, exact, err := s.matchCatalog(ctx, eff, limit, o.Type)
	if err != nil {
		return res, err
	}
	if len(items) == 0 {
		if stripped, ok := search.StripModifiers(res.Query); ok {
			if stripped, err = s.expandSynonyms(ctx, stripped); err != nil {
				return res, err
			}
			retry, retryExact, err := s.matchCatalog(ctx, stripped, limit, o.Type)
			if err != nil {
				return res, err
			}
			if len(retry) > 0 {
				items, exact, eff, res.Rescued = retry, retryExact, stripped, true
			}
		}
	}
	res.Items = items
	res.ExactCode = exact
	if eff != res.Query {
		res.EffectiveQuery = eff
	}
	if o.Counts {
		if res.Counts, err = s.searchCounts(ctx, eff); err != nil {
			return res, err
		}
	}
	return res, nil
}

// searchCounts counts the distinct performers and releases q matches with
// the same recall as matchCatalog (product codes included), each capped at
// SearchCountCap so a broad query stays cheap.
func (s *Store) searchCounts(ctx context.Context, q string) (map[string]int, error) {
	trgm := s.SearchTrgm(ctx)
	perf, _ := performerCandidates(q, trgm)
	rel, _ := releaseCandidates(q, trgm)
	args := []any{containsPattern(q), q, SearchCountCap}
	if keys := search.CodeKeys(q); keys != nil {
		rel += ` UNION ALL SELECT r.id,0 FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id
		  WHERE search_code_keys(ri.normalized_value) && $4::text[] AND r.status='published' AND r.merged_into_id IS NULL`
		args = append(args, keys)
	}
	var np, nr int
	err := s.QueryRow(ctx, `WITH pc AS (`+perf+`), rc AS (`+rel+`)
		SELECT (SELECT count(*) FROM (SELECT DISTINCT id FROM pc WHERE $2::text IS NOT NULL LIMIT $3) a)::int,
		       (SELECT count(*) FROM (SELECT DISTINCT id FROM rc LIMIT $3) b)::int`, args...).Scan(&np, &nr)
	if err != nil {
		return nil, err
	}
	return map[string]int{SearchTypePerformer: np, SearchTypeRelease: nr}, nil
}

// SearchHasResults reports whether q (synonyms applied) matches anything.
func (s *Store) SearchHasResults(ctx context.Context, q string) (bool, error) {
	eff, err := s.expandSynonyms(ctx, search.NormalizeQuery(q))
	if err != nil || eff == "" {
		return false, err
	}
	items, _, err := s.matchCatalog(ctx, eff, 1, "")
	return len(items) > 0, err
}

// matchCatalog returns the ranked items for a normalized query and the
// exact product-code release when exactly one matched.
func (s *Store) matchCatalog(ctx context.Context, q string, limit int, typ string) ([]map[string]any, map[string]any, error) {
	out := []map[string]any{}
	seen := map[int64]bool{}
	var exact map[string]any
	trgm := s.SearchTrgm(ctx)
	if typ == SearchTypePerformer {
		perf, err := s.matchPerformers(ctx, q, limit, trgm)
		return perf, nil, err
	}
	if keys := search.CodeKeys(q); keys != nil {
		codes, err := s.codeReleases(ctx, keys, limit)
		if err != nil {
			return nil, nil, err
		}
		for _, m := range codes {
			seen[m["id"].(int64)] = true
		}
		out = append(out, codes...)
		if len(codes) == 1 {
			exact = map[string]any{"type": "release", "id": codes[0]["id"], "slug": codes[0]["slug"]}
		}
	}
	if typ == "" {
		perf, err := s.matchPerformers(ctx, q, min(limit, maxSearchPerformers), trgm)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, perf...)
	}
	rel, err := s.matchReleases(ctx, q, limit, trgm)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range rel {
		if !seen[m["id"].(int64)] {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, exact, nil
}

func (s *Store) codeReleases(ctx context.Context, keys []string, limit int) ([]map[string]any, error) {
	rows, err := s.Query(ctx, `SELECT DISTINCT ON (r.id) r.id,r.slug,r.canonical_title,r.release_date,r.maker_name,r.runtime_minutes::int
		FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id
		WHERE search_code_keys(ri.normalized_value) && $1::text[] AND r.status='published' AND r.merged_into_id IS NULL
		ORDER BY r.id LIMIT $2`, keys, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var slug, title string
		var date *time.Time
		var maker *string
		var runtime *int32
		if err = rows.Scan(&id, &slug, &title, &date, &maker, &runtime); err != nil {
			return nil, err
		}
		m := searchItem("release", id, slug, title, date, maker, runtime, nil, nil)
		m["match"] = "code"
		out = append(out, m)
	}
	return out, rows.Err()
}

// translatedIDJoin turns translations.entity_id into a bigint without
// failing on non-numeric (tag) ids, so the join can use the primary key.
const translatedID = `CASE WHEN t.entity_id ~ '^[0-9]{1,18}$' THEN t.entity_id::bigint END`

var performerMatch = []string{"name", "alias", "translation", "fuzzy"}
var releaseMatch = []string{"title", "translation", "fuzzy"}

func matchLabel(labels []string, how int) string {
	if how >= 0 && how < len(labels) {
		return labels[how]
	}
	return "fuzzy"
}

// performerCandidates is the (id, how) candidate union for q ($1 contains
// pattern, $2 q) and the similarity expression ranking it.
func performerCandidates(q string, trgm bool) (string, string) {
	active := "p.status='active' AND p.merged_into_id IS NULL"
	parts := []string{
		`SELECT p.id,0 how FROM performers p WHERE ` + active + ` AND (p.canonical_name ILIKE $1 OR p.canonical_name_ja ILIKE $1)`,
		`SELECT p.id,1 FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE ` + active + ` AND a.normalized_name ILIKE $1`,
		`SELECT p.id,2 FROM translations t JOIN performers p ON p.id=` + translatedID + ` WHERE t.entity_type='performer' AND t.field='name'
		  AND t.reviewed_value IS NOT NULL AND t.reviewed_value ILIKE $1 AND ` + active,
	}
	sim := "0::real"
	if fuzzyOK(trgm, q) {
		parts = append(parts,
			`SELECT p.id,3 FROM performers p WHERE `+active+` AND ($2 <% p.canonical_name OR $2 <% p.canonical_name_ja)`,
			`SELECT p.id,3 FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE `+active+` AND $2 <% a.normalized_name`)
		sim = "GREATEST(word_similarity($2,p.canonical_name),word_similarity($2,COALESCE(p.canonical_name_ja,'')))"
	}
	return strings.Join(parts, " UNION ALL "), sim
}

func (s *Store) matchPerformers(ctx context.Context, q string, limit int, trgm bool) ([]map[string]any, error) {
	cand, sim := performerCandidates(q, trgm)
	sql := `WITH cand AS (` + cand + `), best AS (SELECT id,min(how) how FROM cand GROUP BY id)
	SELECT p.id,p.slug,p.canonical_name,p.canonical_name_ja,` + PublicBirthDate("p") + `,b.how
	FROM best b JOIN performers p ON p.id=b.id
	ORDER BY b.how,(lower(p.canonical_name)=$2 OR lower(COALESCE(p.canonical_name_ja,''))=$2) DESC,
	  (p.canonical_name ILIKE $3 OR p.canonical_name_ja ILIKE $3) DESC,` + sim + ` DESC,p.canonical_name,p.id LIMIT $4`
	rows, err := s.Query(ctx, sql, containsPattern(q), q, prefixPattern(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var how int
		var slug, name string
		var nameJA *string
		var birth *time.Time
		if err = rows.Scan(&id, &slug, &name, &nameJA, &birth, &how); err != nil {
			return nil, err
		}
		m := searchItem("performer", id, slug, name, nil, nil, nil, nameJA, birth)
		m["match"] = matchLabel(performerMatch, how)
		out = append(out, m)
	}
	return out, rows.Err()
}

// releaseCandidates is performerCandidates for published releases.
func releaseCandidates(q string, trgm bool) (string, string) {
	pub := "r.status='published' AND r.merged_into_id IS NULL"
	parts := []string{
		`SELECT r.id,0 how FROM releases r WHERE ` + pub + ` AND (r.canonical_title ILIKE $1 OR r.title_ja ILIKE $1)`,
		`SELECT r.id,1 FROM translations t JOIN releases r ON r.id=` + translatedID + ` WHERE t.entity_type='release' AND t.field='title'
		  AND t.reviewed_value IS NOT NULL AND t.reviewed_value ILIKE $1 AND ` + pub,
	}
	sim := "0::real"
	if fuzzyOK(trgm, q) {
		parts = append(parts,
			`SELECT r.id,2 FROM releases r WHERE `+pub+` AND ($2 <% r.canonical_title OR $2 <% r.title_ja)`,
			`SELECT r.id,2 FROM translations t JOIN releases r ON r.id=`+translatedID+` WHERE t.entity_type='release' AND t.field='title'
			  AND t.reviewed_value IS NOT NULL AND $2 <% t.reviewed_value AND `+pub)
		sim = "GREATEST(word_similarity($2,r.canonical_title),word_similarity($2,COALESCE(r.title_ja,'')))"
	}
	return strings.Join(parts, " UNION ALL "), sim
}

func (s *Store) matchReleases(ctx context.Context, q string, limit int, trgm bool) ([]map[string]any, error) {
	cand, sim := releaseCandidates(q, trgm)
	sql := `WITH cand AS (` + cand + `), best AS (SELECT id,min(how) how FROM cand GROUP BY id)
	SELECT r.id,r.slug,r.canonical_title,r.release_date,r.maker_name,r.runtime_minutes::int,b.how
	FROM best b JOIN releases r ON r.id=b.id
	ORDER BY b.how,(lower(r.canonical_title)=$2 OR lower(COALESCE(r.title_ja,''))=$2) DESC,
	  (r.canonical_title ILIKE $3 OR r.title_ja ILIKE $3) DESC,` + sim + ` DESC,r.release_date DESC NULLS LAST,r.id DESC LIMIT $4`
	rows, err := s.Query(ctx, sql, containsPattern(q), q, prefixPattern(q), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var how int
		var slug, title string
		var date *time.Time
		var maker *string
		var runtime *int32
		if err = rows.Scan(&id, &slug, &title, &date, &maker, &runtime, &how); err != nil {
			return nil, err
		}
		m := searchItem("release", id, slug, title, date, maker, runtime, nil, nil)
		m["match"] = matchLabel(releaseMatch, how)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---- suggestions and hot searches ----

// Suggest returns typed suggestions for a partially typed query
// (MobileSearchTyping): published releases (title, reviewed translation or
// product-code prefix), performers (name or alias), upcoming releases,
// tags and hot search terms, at most limit (<= search.SuggestLimit) items
// grouped in search.SuggestOrder.
func (s *Store) Suggest(ctx context.Context, q string, limit int) ([]map[string]any, error) {
	q = search.NormalizeQuery(q)
	if q == "" {
		return []map[string]any{}, nil
	}
	code := search.CodePrefix(q)
	codeCond := "($3::text='')" // always false; keeps $3 typed
	if code != "" {
		codeCond = `r.id IN (SELECT release_id FROM release_identifiers WHERE normalized_value LIKE $3)`
	}
	sql := `
	(SELECT 'release' t,r.id,r.slug,r.canonical_title,r.release_date d,row_number() OVER (ORDER BY (r.canonical_title ILIKE $2) DESC,r.release_date DESC NULLS LAST,r.id DESC) o
	   FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL AND (r.canonical_title ILIKE $1 OR r.title_ja ILIKE $1 OR ` + codeCond + `
	     OR r.id IN (SELECT ` + translatedID + ` FROM translations t WHERE t.entity_type='release' AND t.field='title' AND t.reviewed_value ILIKE $1))
	   ORDER BY o LIMIT 8)
	UNION ALL
	(SELECT 'performer',p.id,p.slug,p.canonical_name,NULL::date,row_number() OVER (ORDER BY (p.canonical_name ILIKE $2 OR p.canonical_name_ja ILIKE $2) DESC,p.canonical_name,p.id)
	   FROM performers p WHERE p.status='active' AND p.merged_into_id IS NULL AND (p.canonical_name ILIKE $1 OR p.canonical_name_ja ILIKE $1
	     OR p.id IN (SELECT performer_id FROM performer_aliases WHERE normalized_name ILIKE $1))
	   ORDER BY 6 LIMIT 8)
	UNION ALL
	(SELECT 'upcoming',r.id,r.slug,r.canonical_title,r.scheduled_at::date,row_number() OVER (ORDER BY r.scheduled_at NULLS LAST,r.id)
	   FROM releases r WHERE r.status='upcoming' AND r.merged_into_id IS NULL AND (r.canonical_title ILIKE $1 OR r.title_ja ILIKE $1 OR ` + codeCond + `)
	   ORDER BY 6 LIMIT 8)
	UNION ALL
	(SELECT 'tag',NULL::bigint,normalized_tag,min(tag),NULL::date,row_number() OVER (ORDER BY bool_or(tag ILIKE $2) DESC,count(*) DESC,normalized_tag)
	   FROM release_tags WHERE tag ILIKE $1 OR normalized_tag ILIKE $1 GROUP BY normalized_tag ORDER BY 6 LIMIT 8)
	UNION ALL
	(SELECT 'query',NULL::bigint,NULL::text,q_norm,NULL::date,rank::bigint FROM search_trending
	   WHERE period='` + search.HotPeriod + `' AND q_norm LIKE $2 AND q_norm<>$4 ORDER BY rank LIMIT 8)`
	rows, err := s.Query(ctx, sql, containsPattern(q), prefixPattern(q), likeEscape(code)+"%", q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := map[string][]map[string]any{}
	order := map[string][]int64{}
	for rows.Next() {
		var typ, name string
		var id *int64
		var slug *string
		var d *time.Time
		var o int64
		if err = rows.Scan(&typ, &id, &slug, &name, &d, &o); err != nil {
			return nil, err
		}
		m := map[string]any{"type": typ, "name": name}
		if id != nil {
			m["id"] = *id
		}
		if slug != nil {
			m["slug"] = *slug
		}
		switch typ {
		case search.SuggestRelease:
			m["release_date"] = dateOrNil(d)
		case search.SuggestUpcoming:
			m["scheduled_date"] = dateOrNil(d)
		}
		groups[typ] = append(groups[typ], m)
		order[typ] = append(order[typ], o)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for typ, g := range groups {
		ord := order[typ]
		idx := make([]int, len(g))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return ord[idx[a]] < ord[idx[b]] })
		sorted := make([]map[string]any, len(g))
		for i, j := range idx {
			sorted[i] = g[j]
		}
		groups[typ] = sorted
	}
	return search.MergeSuggestions(groups, limit), nil
}

func dateOrNil(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Format(time.DateOnly)
}

// HotSearch is one hot-search term.
type HotSearch struct {
	Rank     int    `json:"rank"`
	Q        string `json:"q"`
	Searches int    `json:"searches"`
}

// HotSearches returns the latest board of period (search.HotPeriod) and
// when it was computed (zero time when there is none yet).
func (s *Store) HotSearches(ctx context.Context, period string, limit int) ([]HotSearch, time.Time, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	rows, err := s.Query(ctx, `SELECT rank,q_norm,searches,computed_at FROM search_trending WHERE period=$1 ORDER BY rank LIMIT $2`, period, limit)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer rows.Close()
	out := []HotSearch{}
	var at time.Time
	for rows.Next() {
		var h HotSearch
		if err = rows.Scan(&h.Rank, &h.Q, &h.Searches, &at); err != nil {
			return nil, time.Time{}, err
		}
		out = append(out, h)
	}
	return out, at, rows.Err()
}

// ---- search_stats job (search.Store) ----

// LastSearchDay implements search.Store.
func (s *Store) LastSearchDay(ctx context.Context) (time.Time, bool, error) {
	var d *time.Time
	if err := s.QueryRow(ctx, `SELECT max(day) FROM search_query_daily`).Scan(&d); err != nil || d == nil {
		return time.Time{}, false, err
	}
	return *d, true, nil
}

// SearchEventCounts implements search.Store. It never reads user_id or
// device_id.
func (s *Store) SearchEventCounts(ctx context.Context, from, to time.Time) ([]search.RawCount, error) {
	rows, err := s.Query(ctx, `SELECT (ts_server AT TIME ZONE 'UTC')::date,COALESCE(props->>'query',''),COALESCE(ui_lang,''),
		count(*),
		count(*) FILTER (WHERE props->>'result_count'='0'),
		count(*) FILTER (WHERE jsonb_typeof(props->'clicked_rank')='number'),
		count(*) FILTER (WHERE CASE WHEN jsonb_typeof(props->'clicked_rank')='number' THEN (props->>'clicked_rank')::numeric END <= $3),
		count(*) FILTER (WHERE props->'rescued'='true'::jsonb)
		FROM events WHERE event='search' AND ts_server>=$1 AND ts_server<$2 GROUP BY 1,2,3`, from, to, search.FirstPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []search.RawCount
	for rows.Next() {
		var c search.RawCount
		if err = rows.Scan(&c.Day, &c.Query, &c.Lang, &c.Searches, &c.ZeroResults, &c.Clicks, &c.FirstPageClicks, &c.Rescued); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReplaceSearchDaily implements search.Store.
func (s *Store) ReplaceSearchDaily(ctx context.Context, fromDay, toDay time.Time, rows []search.DailyRow) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM search_query_daily WHERE day BETWEEN $1::date AND $2::date`,
		fromDay.Format(time.DateOnly), toDay.Format(time.DateOnly)); err != nil {
		return err
	}
	if len(rows) > 0 {
		n := len(rows)
		days, qs, langs := make([]string, n), make([]string, n), make([]string, n)
		se, zr, cl, fp, rs := make([]int32, n), make([]int32, n), make([]int32, n), make([]int32, n), make([]int32, n)
		for i, r := range rows {
			days[i], qs[i], langs[i] = r.Day.Format(time.DateOnly), r.QNorm, r.Lang
			se[i], zr[i], cl[i], fp[i], rs[i] = int32(r.Searches), int32(r.ZeroResults), int32(r.Clicks), int32(r.FirstPageClicks), int32(r.Rescued)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO search_query_daily(day,q_norm,lang,searches,zero_results,clicks,first_page_clicks,rescued)
			SELECT d::date,q,l,a,b,c,e,f FROM unnest($1::text[],$2::text[],$3::text[],$4::int[],$5::int[],$6::int[],$7::int[],$8::int[]) AS x(d,q,l,a,b,c,e,f)`,
			days, qs, langs, se, zr, cl, fp, rs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SearchHotInputs implements search.Store.
func (s *Store) SearchHotInputs(ctx context.Context, from, to time.Time) ([]search.HotInput, error) {
	rows, err := s.Query(ctx, `SELECT props->>'query',count(DISTINCT session_id),count(*),count(*) FILTER (WHERE props->>'result_count'='0')
		FROM events WHERE event='search' AND ts_server>=$1 AND ts_server<$2 AND props ? 'query' GROUP BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []search.HotInput
	for rows.Next() {
		var h search.HotInput
		if err = rows.Scan(&h.Query, &h.Sessions, &h.Searches, &h.ZeroResults); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchExclusions implements search.Store.
func (s *Store) SearchExclusions(ctx context.Context) (search.Exclusions, error) {
	var ex search.Exclusions
	err := s.QueryRow(ctx, `SELECT COALESCE((SELECT array_agg(term) FROM search_blocklist),'{}'),
		COALESCE((SELECT array_agg(word) FROM moderation_words),'{}')`).Scan(&ex.Blocked, &ex.ModerationWords)
	return ex, err
}

// ReplaceSearchTrending implements search.Store.
func (s *Store) ReplaceSearchTrending(ctx context.Context, period string, rows []search.HotRow, at time.Time) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM search_trending WHERE period=$1`, period); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err = tx.Exec(ctx, `INSERT INTO search_trending(period,rank,q_norm,searches,computed_at) VALUES($1,$2,$3,$4,$5)`,
			period, r.Rank, r.QNorm, r.Searches, at); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ---- AdminSearch ----

// NearMatch is a catalog row resembling a zero-result term.
type NearMatch struct {
	Type       string  `json:"type"` // release | performer
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Field      string  `json:"field"` // title | title_ja | name | name_ja | alias | translation:<lang>
	Status     string  `json:"status"`
	Similarity float64 `json:"similarity"`
}

// ZeroResultTerm is one row of the AdminSearch zero-result queue.
type ZeroResultTerm struct {
	Term        string      `json:"term"`
	Searches    int         `json:"searches"`
	ZeroResults int         `json:"zero_results"`
	Rescued     int         `json:"rescued"`
	LastSeen    string      `json:"last_seen"`
	Stripped    string      `json:"stripped,omitempty"`
	WouldRescue bool        `json:"would_rescue"`
	Resolved    bool        `json:"resolved"` // matches something now (e.g. after a synonym)
	NearMatches []NearMatch `json:"near_matches"`
	Class       string      `json:"class"`
	ClassLabel  string      `json:"class_label"`
	Actions     []string    `json:"actions"`
}

// ZeroResultTerms lists terms with at least minZero zero-result searches in
// the last days days (most first), each with the stripped retry, catalog
// near-matches and the heuristic class.
func (s *Store) ZeroResultTerms(ctx context.Context, days, minZero, limit int) ([]ZeroResultTerm, error) {
	rows, err := s.Query(ctx, `SELECT q_norm,sum(searches)::int,sum(zero_results)::int,sum(rescued)::int,to_char(max(day),'YYYY-MM-DD')
		FROM search_query_daily WHERE day >= (now() AT TIME ZONE 'UTC')::date - ($1::int - 1)
		GROUP BY q_norm HAVING sum(zero_results) >= $2 ORDER BY sum(zero_results) DESC,q_norm LIMIT $3`, days, minZero, limit)
	if err != nil {
		return nil, err
	}
	out := []ZeroResultTerm{}
	for rows.Next() {
		var z ZeroResultTerm
		if err = rows.Scan(&z.Term, &z.Searches, &z.ZeroResults, &z.Rescued, &z.LastSeen); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, z)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		z := &out[i]
		if z.Resolved, err = s.SearchHasResults(ctx, z.Term); err != nil {
			return nil, err
		}
		if stripped, ok := search.StripModifiers(z.Term); ok {
			z.Stripped = stripped
			if z.WouldRescue, err = s.SearchHasResults(ctx, stripped); err != nil {
				return nil, err
			}
		}
		if z.NearMatches, err = s.NearMatches(ctx, z.Term, 3); err != nil {
			return nil, err
		}
		c := search.Classify(z.Term, len(z.NearMatches) > 0, z.WouldRescue)
		z.Class, z.ClassLabel, z.Actions = string(c), c.Label(), c.Actions()
	}
	return out, nil
}

// nearMatchThreshold is the word similarity a near-match needs (lower than
// pg_trgm's 0.6 default: CJK titles share few trigrams).
const nearMatchThreshold = 0.3

// NearMatches finds catalog rows resembling term in any status (titles,
// Japanese titles, performer names, aliases, reviewed translations): with
// pg_trgm by word similarity, otherwise by ILIKE on the term and on each
// token of 2+ runes.
func (s *Store) NearMatches(ctx context.Context, term string, limit int) ([]NearMatch, error) {
	term = search.NormalizeQuery(term)
	if term == "" {
		return []NearMatch{}, nil
	}
	trgm := s.SearchTrgm(ctx)
	var cond func(col string) string
	simExpr := func(col string) string { return "0::real" }
	if trgm {
		cond = func(col string) string { return "$1 <% " + col }
		simExpr = func(col string) string { return "word_similarity($1," + col + ")" }
	} else {
		cond = func(col string) string { return col + " ILIKE ANY($2::text[])" }
	}
	pats := []string{containsPattern(term)}
	for _, t := range search.Tokens(term) {
		if t != term && search.Meaningful(t) {
			pats = append(pats, containsPattern(t))
		}
	}
	sql := `SELECT * FROM (
	  SELECT 'release',r.id,r.canonical_title,'title',r.status,` + simExpr("r.canonical_title") + ` FROM releases r WHERE r.merged_into_id IS NULL AND ` + cond("r.canonical_title") + `
	  UNION ALL SELECT 'release',r.id,r.title_ja,'title_ja',r.status,` + simExpr("r.title_ja") + ` FROM releases r WHERE r.merged_into_id IS NULL AND ` + cond("r.title_ja") + `
	  UNION ALL SELECT 'performer',p.id,p.canonical_name,'name',p.status,` + simExpr("p.canonical_name") + ` FROM performers p WHERE p.merged_into_id IS NULL AND ` + cond("p.canonical_name") + `
	  UNION ALL SELECT 'performer',p.id,p.canonical_name_ja,'name_ja',p.status,` + simExpr("p.canonical_name_ja") + ` FROM performers p WHERE p.merged_into_id IS NULL AND ` + cond("p.canonical_name_ja") + `
	  UNION ALL SELECT 'performer',p.id,a.name,'alias',p.status,` + simExpr("a.normalized_name") + ` FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE p.merged_into_id IS NULL AND ` + cond("a.normalized_name") + `
	  UNION ALL SELECT t.entity_type,` + translatedID + `,t.reviewed_value,'translation:'||t.lang,'',` + simExpr("t.reviewed_value") + ` FROM translations t
	    WHERE t.entity_type IN ('release','performer') AND t.field IN ('title','name') AND t.reviewed_value IS NOT NULL AND ` + cond("t.reviewed_value") + `
	) x(type,id,name,field,status,sim) WHERE id IS NOT NULL AND $1::text<>'' AND cardinality($2::text[])>0 ORDER BY sim DESC,type,id LIMIT $3`
	var out []NearMatch
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		args := []any{term, pats, limit * 4}
		if trgm {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL pg_trgm.word_similarity_threshold = %g`, nearMatchThreshold)); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		seen := map[string]bool{}
		for rows.Next() {
			var m NearMatch
			if err = rows.Scan(&m.Type, &m.ID, &m.Name, &m.Field, &m.Status, &m.Similarity); err != nil {
				return err
			}
			k := fmt.Sprintf("%s:%d", m.Type, m.ID)
			if seen[k] || len(out) >= limit {
				continue
			}
			seen[k] = true
			m.Similarity = float64(int64(m.Similarity*1000+0.5)) / 1000
			out = append(out, m)
		}
		return rows.Err()
	})
	if out == nil {
		out = []NearMatch{}
	}
	return out, err
}

// SearchDay is one day of AdminSearch stats.
type SearchDay struct {
	Day string `json:"day"`
	search.Totals
}

// SearchStats are the AdminSearch headline numbers over the last days days.
type SearchStats struct {
	Days   int           `json:"days"`
	Totals search.Totals `json:"totals"`
	Daily  []SearchDay   `json:"daily"`
	// Trgm reports whether pg_trgm is installed (fuzzy recall); false
	// means search falls back to plain ILIKE.
	Trgm bool `json:"trgm"`
}

// SearchStats sums search_query_daily over the last days days (UTC).
func (s *Store) SearchStats(ctx context.Context, days int) (SearchStats, error) {
	st := SearchStats{Days: days, Daily: []SearchDay{}, Trgm: s.SearchTrgm(ctx)}
	rows, err := s.Query(ctx, `SELECT day,sum(searches)::int,sum(zero_results)::int,sum(clicks)::int,sum(first_page_clicks)::int,sum(rescued)::int
		FROM search_query_daily WHERE day >= (now() AT TIME ZONE 'UTC')::date - ($1::int - 1) GROUP BY day ORDER BY day`, days)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	var all []search.DailyRow
	for rows.Next() {
		var r search.DailyRow
		if err = rows.Scan(&r.Day, &r.Searches, &r.ZeroResults, &r.Clicks, &r.FirstPageClicks, &r.Rescued); err != nil {
			return st, err
		}
		all = append(all, r)
		st.Daily = append(st.Daily, SearchDay{Day: r.Day.Format(time.DateOnly), Totals: search.Summarize([]search.DailyRow{r})})
	}
	st.Totals = search.Summarize(all)
	return st, rows.Err()
}

// ---- synonyms, redirects, blocklist ----

// SearchSynonym is a search_synonyms row.
type SearchSynonym struct {
	ID        int64     `json:"id"`
	Term      string    `json:"term"`
	Canonical string    `json:"canonical"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SearchRedirectRule is a search_redirects row.
type SearchRedirectRule struct {
	ID        int64     `json:"id"`
	Term      string    `json:"term"`
	TargetURL string    `json:"target_url"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SearchBlockedTerm is a search_blocklist row.
type SearchBlockedTerm struct {
	ID        int64     `json:"id"`
	Term      string    `json:"term"`
	Reason    string    `json:"reason"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// filterTerm is the optional ?q= substring filter of the admin lists.
func filterTerm(q string) string {
	if q = search.NormalizeQuery(q); q == "" {
		return "%"
	}
	return containsPattern(q)
}

func (s *Store) SearchSynonyms(ctx context.Context, q string) ([]SearchSynonym, error) {
	rows, err := s.Query(ctx, `SELECT id,term,canonical,created_by,created_at,updated_at FROM search_synonyms
		WHERE term ILIKE $1 OR canonical ILIKE $1 ORDER BY term LIMIT 500`, filterTerm(q))
	return scanAll(rows, err, func(r pgx.Rows) (SearchSynonym, error) {
		var x SearchSynonym
		return x, r.Scan(&x.ID, &x.Term, &x.Canonical, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	})
}

func (s *Store) CreateSearchSynonym(ctx context.Context, term, canonical, actor string) (SearchSynonym, error) {
	var x SearchSynonym
	err := s.QueryRow(ctx, `INSERT INTO search_synonyms(term,canonical,created_by) VALUES($1,$2,$3)
		RETURNING id,term,canonical,created_by,created_at,updated_at`, term, canonical, actor).
		Scan(&x.ID, &x.Term, &x.Canonical, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *Store) UpdateSearchSynonym(ctx context.Context, id int64, term, canonical string) (SearchSynonym, error) {
	var x SearchSynonym
	err := s.QueryRow(ctx, `UPDATE search_synonyms SET term=$2,canonical=$3,updated_at=now() WHERE id=$1
		RETURNING id,term,canonical,created_by,created_at,updated_at`, id, term, canonical).
		Scan(&x.ID, &x.Term, &x.Canonical, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *Store) DeleteSearchSynonym(ctx context.Context, id int64) error {
	return deleteByID(ctx, s, "search_synonyms", id)
}

func (s *Store) SearchRedirects(ctx context.Context, q string) ([]SearchRedirectRule, error) {
	rows, err := s.Query(ctx, `SELECT id,term,target_url,reason,created_by,created_at,updated_at FROM search_redirects
		WHERE term ILIKE $1 ORDER BY term LIMIT 500`, filterTerm(q))
	return scanAll(rows, err, func(r pgx.Rows) (SearchRedirectRule, error) {
		var x SearchRedirectRule
		return x, r.Scan(&x.ID, &x.Term, &x.TargetURL, &x.Reason, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	})
}

func (s *Store) CreateSearchRedirect(ctx context.Context, term, target, reason, actor string) (SearchRedirectRule, error) {
	var x SearchRedirectRule
	err := s.QueryRow(ctx, `INSERT INTO search_redirects(term,target_url,reason,created_by) VALUES($1,$2,$3,$4)
		RETURNING id,term,target_url,reason,created_by,created_at,updated_at`, term, target, reason, actor).
		Scan(&x.ID, &x.Term, &x.TargetURL, &x.Reason, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *Store) UpdateSearchRedirect(ctx context.Context, id int64, term, target, reason string) (SearchRedirectRule, error) {
	var x SearchRedirectRule
	err := s.QueryRow(ctx, `UPDATE search_redirects SET term=$2,target_url=$3,reason=$4,updated_at=now() WHERE id=$1
		RETURNING id,term,target_url,reason,created_by,created_at,updated_at`, id, term, target, reason).
		Scan(&x.ID, &x.Term, &x.TargetURL, &x.Reason, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func (s *Store) DeleteSearchRedirect(ctx context.Context, id int64) error {
	return deleteByID(ctx, s, "search_redirects", id)
}

func (s *Store) SearchBlocklist(ctx context.Context) ([]SearchBlockedTerm, error) {
	rows, err := s.Query(ctx, `SELECT id,term,reason,created_by,created_at FROM search_blocklist ORDER BY term LIMIT 1000`)
	return scanAll(rows, err, func(r pgx.Rows) (SearchBlockedTerm, error) {
		var x SearchBlockedTerm
		return x, r.Scan(&x.ID, &x.Term, &x.Reason, &x.CreatedBy, &x.CreatedAt)
	})
}

// BlockSearchTerm adds term to the blocklist and drops it from the current
// hot-search boards right away.
func (s *Store) BlockSearchTerm(ctx context.Context, term, reason, actor string) (SearchBlockedTerm, error) {
	var x SearchBlockedTerm
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO search_blocklist(term,reason,created_by) VALUES($1,$2,$3)
			RETURNING id,term,reason,created_by,created_at`, term, reason, actor).
			Scan(&x.ID, &x.Term, &x.Reason, &x.CreatedBy, &x.CreatedAt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM search_trending WHERE q_norm=$1`, term)
		return err
	})
	return x, err
}

func (s *Store) UnblockSearchTerm(ctx context.Context, id int64) error {
	return deleteByID(ctx, s, "search_blocklist", id)
}

// deleteByID deletes one row of a fixed table; a missing row is pgx.ErrNoRows.
func deleteByID(ctx context.Context, s *Store, table string, id int64) error {
	tag, err := s.Exec(ctx, `DELETE FROM `+table+` WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

func scanAll[T any](rows pgx.Rows, err error, scan func(pgx.Rows) (T, error)) ([]T, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		x, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ---- fixes from a term ----

// TermFix is the result of an AdminSearch fix applied from a term.
type TermFix struct {
	EntityType string `json:"entity_type"`
	EntityID   int64  `json:"entity_id"`
	Action     string `json:"action"`
	Value      string `json:"value"`
	Term       string `json:"term"`
}

// AddPerformerAliasFromTerm adds name as a performer_aliases row
// (alias_type 'search') and writes a review_events row 'add_alias'. A
// missing performer is pgx.ErrNoRows; a merged one or an alias that already
// exists is a *ConflictError.
func (s *Store) AddPerformerAliasFromTerm(ctx context.Context, performerID int64, term, name, reason, actor string) (TermFix, error) {
	fix := TermFix{EntityType: "performer", EntityID: performerID, Action: "add_alias", Value: name, Term: term}
	norm := identity.AliasForm(name)
	if norm == "" {
		return fix, invalid("alias is empty")
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var merged *int64
		if err := tx.QueryRow(ctx, `SELECT merged_into_id FROM performers WHERE id=$1 FOR UPDATE`, performerID).Scan(&merged); err != nil {
			return err
		}
		if merged != nil {
			return conflict("performer was merged into %d", *merged)
		}
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO performer_aliases(performer_id,name,normalized_name,alias_type) VALUES($1,$2,$3,'search')
			ON CONFLICT (performer_id,normalized_name) DO NOTHING RETURNING id`, performerID, name, norm).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("performer already has alias %q", name)
		}
		if err != nil {
			return err
		}
		return writeReviewEvent(ctx, tx, "performer", performerID, "add_alias", nil,
			map[string]any{"alias": name, "normalized_name": norm, "alias_type": "search", "search_term": term}, reason, actor)
	})
	return fix, err
}

// AddEnglishTitleFromTerm stores title as the reviewed English translation
// of the release title (served publicly and matched by search at once) and
// writes a review_events row 'add_english_title' with the previous reviewed
// English title. A missing release is pgx.ErrNoRows; a merged one is a
// *ConflictError.
func (s *Store) AddEnglishTitleFromTerm(ctx context.Context, releaseID int64, term, title, reason, actor string) (TermFix, error) {
	fix := TermFix{EntityType: "release", EntityID: releaseID, Action: "add_english_title", Value: title, Term: term}
	if strings.TrimSpace(title) == "" {
		return fix, invalid("title is empty")
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := lockRelease(ctx, tx, releaseID, 0); err != nil {
			return err
		}
		var before *string
		err := tx.QueryRow(ctx, `SELECT reviewed_value FROM translations WHERE entity_type='release' AND entity_id=$1 AND field='title' AND lang=$2`,
			strconv.FormatInt(releaseID, 10), i18n.En).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,reviewed_value,reviewed_at,updated_by)
			SELECT 'release',$1,'title',$2,$3,'reviewed',i18n_hash(src),$3,now(),$4 FROM i18n_source('release',$1,'title') src WHERE COALESCE(src,'')<>''
			ON CONFLICT(entity_type,entity_id,field,lang) DO UPDATE SET value=EXCLUDED.value,status='reviewed',reviewed_value=EXCLUDED.reviewed_value,
			  reviewed_at=now(),source_hash=EXCLUDED.source_hash,submitted_at=NULL,updated_by=EXCLUDED.updated_by,updated_at=now()`,
			strconv.FormatInt(releaseID, 10), i18n.En, title, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return pgx.ErrNoRows
		}
		var b any
		if before != nil {
			b = map[string]any{"en_title": *before}
		}
		return writeReviewEvent(ctx, tx, "release", releaseID, "add_english_title", b,
			map[string]any{"en_title": title, "search_term": term}, reason, actor)
	})
	return fix, err
}
