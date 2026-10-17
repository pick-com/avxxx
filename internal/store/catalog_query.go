package store

// Public catalog list/detail/search queries used by the web client: filtered,
// sorted, keyset-paginated release and performer lists, the release detail
// payload, and the typed catalog search. Every public query only returns
// published, unmerged releases and active, unmerged performers, and every
// performer birth date goes through PublicBirthDate (NULL while hidden).

import (
	"avxxx/internal/model"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidQuery marks a client mistake in list parameters (unknown sort,
// malformed or mismatched cursor). The API maps it to 400.
var ErrInvalidQuery = errors.New("invalid query")

func invalidQuery(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, a...))
}

const (
	defaultPageLimit = 20
	maxPageLimit     = 100
)

// pageLimit clamps a requested page size: 0 or less means the default.
func pageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	}
	return n
}

// Cursor is the decoded form of an opaque next_cursor: the sort it belongs to,
// the last row's sort key (as text) and the last row's id.
type Cursor struct {
	Sort string `json:"s"`
	Key  string `json:"k"`
	ID   int64  `json:"id"`
}

// EncodeCursor returns the opaque, URL-safe form of c.
func EncodeCursor(c Cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeCursor parses an opaque cursor produced by EncodeCursor.
func DecodeCursor(raw string) (Cursor, error) {
	var c Cursor
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return c, invalidQuery("malformed cursor")
	}
	if err = json.Unmarshal(b, &c); err != nil || c.Sort == "" || c.ID <= 0 {
		return Cursor{}, invalidQuery("malformed cursor")
	}
	return c, nil
}

// keyKind is the Go/SQL type of a sort key, used to type the cursor parameter.
type keyKind int

const (
	kindDate keyKind = iota
	kindInt
	kindText
)

// sortSpec describes one keyset ordering: a key expression plus id tiebreak.
type sortSpec struct {
	key     string // SQL expression of the sort key (never NULL)
	keyText string // SQL expression rendering the key as text for the cursor
	kind    keyKind
	keyDesc bool
	idDesc  bool
	// missingKey (performer sorts only) is the key of a performer without
	// a performer_stats row; key then only reads rows that have one.
	missingKey string
}

func dir(desc bool) string {
	if desc {
		return "DESC"
	}
	return "ASC"
}

func cmp(desc bool) string {
	if desc {
		return "<"
	}
	return ">"
}

func (s sortSpec) orderBy(idCol string) string {
	return s.key + " " + dir(s.keyDesc) + "," + idCol + " " + dir(s.idDesc)
}

// parseKey converts a cursor's text key to the parameter type of s.
func (s sortSpec) parseKey(k string) (any, error) {
	switch s.kind {
	case kindDate:
		t, err := time.Parse(time.DateOnly, k)
		if err != nil {
			return nil, invalidQuery("malformed cursor")
		}
		return t, nil
	case kindInt:
		n, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			return nil, invalidQuery("malformed cursor")
		}
		return n, nil
	}
	return k, nil
}

// sqlBuilder accumulates WHERE conditions with positional parameters.
type sqlBuilder struct {
	where []string
	args  []any
}

// arg appends v and returns its placeholder.
func (b *sqlBuilder) arg(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

func (b *sqlBuilder) cond(c string) { b.where = append(b.where, c) }

// after adds the keyset predicate "row comes after cursor c in order s".
func (b *sqlBuilder) after(s sortSpec, idCol string, c *Cursor) error {
	if c == nil {
		return nil
	}
	k, err := s.parseKey(c.Key)
	if err != nil {
		return err
	}
	b.cond(s.keysetCond(idCol, b.arg(k), b.arg(c.ID)))
	return nil
}

// keysetCond is "row comes after the cursor (key kp, id ip) in order s".
func (s sortSpec) keysetCond(idCol, kp, ip string) string {
	return fmt.Sprintf("(%s %s %s OR (%s = %s AND %s %s %s))",
		s.key, cmp(s.keyDesc), kp, s.key, kp, idCol, cmp(s.idDesc), ip)
}

func (b *sqlBuilder) whereSQL() string {
	if len(b.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(b.where, " AND ")
}

// ---- releases ----

// releaseSorts are the accepted values of ReleaseFilter.Sort.
var releaseSorts = map[string]sortSpec{
	"newest": {key: "COALESCE(r.release_date,DATE '0001-01-01')", kind: kindDate, keyDesc: true, idDesc: true},
	"oldest": {key: "COALESCE(r.release_date,DATE '9999-12-31')", kind: kindDate},
	// runtime: longest first.
	"runtime": {key: "COALESCE(r.runtime_minutes,0)", kind: kindInt, keyDesc: true, idDesc: true},
}

// ValidReleaseSort reports whether s is an accepted release sort.
func ValidReleaseSort(s string) bool { _, ok := releaseSorts[s]; return ok }

// ReleaseFilter selects a page of published releases. Zero values mean
// "no filter"; Sort "" means newest.
type ReleaseFilter struct {
	Q          string
	Tag        string // release_tags.normalized_tag
	Maker      string // exact maker_name
	Performer  string // performers.slug
	Year       int
	MinRuntime int
	MaxRuntime int
	// Role narrows Performer to lead (主演) or guest (客串) credits
	// (LeadCreditSQL); it needs Performer.
	Role string
	// Playback-source facets (清晰度 / 字幕 / 语言): a release qualifies when
	// one of its enabled playback sources has max_height >= MinHeight, a
	// subtitle in Subtitle ("any" = any subtitle) or audio in AudioLang.
	MinHeight int
	Subtitle  string
	AudioLang string
	Sort      string
	Cursor    *Cursor
	Limit     int
}

// Release performer credit roles accepted by ReleaseFilter.Role.
const (
	CreditLead  = "lead"
	CreditGuest = "guest"
)

// leadRoleNames mirrors leadRoles (notifications.go) for SQL.
const leadRoleNames = `('lead','main','starring','primary','主演')`

// LeadCreditSQL is true when the release_performers row aliased rp is a lead
// credit (主演), with IsLeadCredit / LeadPerformers semantics: is_primary or
// a lead role, or no credit on the release is marked as a lead at all
// (unknown billing: everyone counts as a lead). Anything else is a guest
// credit (客串).
func LeadCreditSQL(rp string) string {
	return "(" + rp + ".is_primary OR lower(btrim(COALESCE(" + rp + ".role,''))) IN " + leadRoleNames +
		" OR NOT EXISTS (SELECT 1 FROM release_performers lc WHERE lc.release_id=" + rp + ".release_id AND (lc.is_primary OR lower(btrim(COALESCE(lc.role,''))) IN " + leadRoleNames + ")))"
}

// playbackFacetSQL is an EXISTS over a release's enabled playback sources;
// the conditions match the partial indexes of migrations/039 so a rare
// value is found from playback_sources rather than by walking releases.
func playbackFacetSQL(cond string) string {
	return "EXISTS (SELECT 1 FROM playback_sources pbs WHERE pbs.release_id=r.id AND pbs.status<>'disabled' AND " + cond + ")"
}

func init() {
	for name, s := range releaseSorts {
		s.keyText = keyTextExpr(s)
		releaseSorts[name] = s
	}
	for name, s := range performerSorts {
		s.keyText = keyTextExpr(s)
		performerSorts[name] = s
	}
}

func keyTextExpr(s sortSpec) string {
	if s.kind == kindDate {
		return "to_char(" + s.key + ",'YYYY-MM-DD')"
	}
	return "(" + s.key + ")::text"
}

const releaseColumns = `r.id,r.slug,r.canonical_title,COALESCE(r.title_ja,''),r.release_date,
	COALESCE(r.runtime_minutes,0),COALESCE(r.maker_name,''),COALESCE(r.label_name,''),COALESCE(r.series_name,''),r.status`

// buildReleaseQuery renders f as parameterized SQL. It selects limit+1 rows so
// the caller can tell whether another page exists; the last column is the
// row's sort key as text.
func buildReleaseQuery(f ReleaseFilter) (string, []any, int, error) {
	if f.Sort == "" {
		f.Sort = "newest"
	}
	s, ok := releaseSorts[f.Sort]
	if !ok {
		return "", nil, 0, invalidQuery("unknown sort %q", f.Sort)
	}
	if f.Cursor != nil && f.Cursor.Sort != f.Sort {
		return "", nil, 0, invalidQuery("cursor does not match sort")
	}
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	b.cond("r.status='published'")
	b.cond("r.merged_into_id IS NULL")
	if f.Q != "" {
		p := b.arg(f.Q)
		b.cond(fmt.Sprintf("(r.canonical_title ILIKE '%%'||%[1]s||'%%' OR r.title_ja ILIKE '%%'||%[1]s||'%%' OR r.id IN (SELECT release_id FROM release_identifiers WHERE normalized_value=lower(%[1]s)))", p))
	}
	if f.Tag != "" {
		b.cond("r.id IN (SELECT release_id FROM release_tags WHERE normalized_tag=" + b.arg(f.Tag) + ")")
	}
	if f.Maker != "" {
		b.cond("r.maker_name=" + b.arg(f.Maker))
	}
	if f.Performer != "" {
		role := ""
		switch f.Role {
		case CreditLead:
			role = " AND " + LeadCreditSQL("rp")
		case CreditGuest:
			role = " AND NOT " + LeadCreditSQL("rp")
		case "":
		default:
			return "", nil, 0, invalidQuery("unknown role %q", f.Role)
		}
		b.cond("r.id IN (SELECT rp.release_id FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE p.slug=" + b.arg(f.Performer) + role + ")")
	} else if f.Role != "" {
		return "", nil, 0, invalidQuery("role needs performer")
	}
	if f.MinHeight > 0 {
		b.cond(playbackFacetSQL("pbs.max_height>=" + b.arg(f.MinHeight)))
	}
	if f.Subtitle == "any" {
		b.cond(playbackFacetSQL("cardinality(pbs.subtitle_langs)>0"))
	} else if f.Subtitle != "" {
		b.cond(playbackFacetSQL("pbs.subtitle_langs @> ARRAY[" + b.arg(f.Subtitle) + "::text]"))
	}
	if f.AudioLang != "" {
		b.cond(playbackFacetSQL("pbs.audio_langs @> ARRAY[" + b.arg(f.AudioLang) + "::text]"))
	}
	if f.Year != 0 {
		from := time.Date(f.Year, 1, 1, 0, 0, 0, 0, time.UTC)
		b.cond("r.release_date>=" + b.arg(from) + " AND r.release_date<" + b.arg(from.AddDate(1, 0, 0)))
	}
	if f.MinRuntime > 0 {
		b.cond("r.runtime_minutes>=" + b.arg(f.MinRuntime))
	}
	if f.MaxRuntime > 0 {
		b.cond("r.runtime_minutes<=" + b.arg(f.MaxRuntime))
	}
	if err := b.after(s, "r.id", f.Cursor); err != nil {
		return "", nil, 0, err
	}
	sql := "SELECT " + releaseColumns + "," + s.keyText + " FROM releases r" + b.whereSQL() +
		" ORDER BY " + s.orderBy("r.id") + " LIMIT " + b.arg(limit+1)
	return sql, b.args, limit, nil
}

// ReleasePage returns one page of published releases and the cursor of the
// next page ("" when this is the last page).
func (s *Store) ReleasePage(ctx context.Context, f ReleaseFilter) ([]model.Release, string, error) {
	sql, args, limit, err := buildReleaseQuery(f)
	if err != nil {
		return nil, "", err
	}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	r, err := s.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	out := []model.Release{}
	var keys []string
	for r.Next() {
		var x model.Release
		var key string
		if err = r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status, &key); err != nil {
			return nil, "", err
		}
		out = append(out, x)
		keys = append(keys, key)
	}
	if err = r.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: f.Sort, Key: keys[limit-1], ID: out[limit-1].ID})
	}
	return out, next, nil
}

// ---- performers ----

var performerSorts = map[string]sortSpec{
	"name": {key: "p.canonical_name", kind: kindText},
	// works / newest_work sort on the hourly performer_stats counts (same
	// rule as the displayed ones: published, unmerged works) so the sort no
	// longer counts every performer's works. A performer with no stats row
	// yet (added since the last run) sorts by a live count (missingKey,
	// see buildPerformerQuery). The displayed works_count /
	// latest_release_date stay live for the page's rows.
	"works": {key: "ps.works_count", missingKey: liveWorksSQL("count(*)"), kind: kindInt, keyDesc: true},
	"newest_work": {key: "COALESCE(ps.latest_work_date,DATE '0001-01-01')", missingKey: "COALESCE(" + liveWorksSQL("max(lr.release_date)") + ",DATE '0001-01-01')",
		kind: kindDate, keyDesc: true, idDesc: true},
	// From performer_stats (hourly): 热度 (30-day heat), 粉丝, 新晋 (latest debut first).
	"heat":      {key: "ps.heat_30d", kind: kindInt, keyDesc: true, idDesc: true},
	"followers": {key: "ps.followers", kind: kindInt, keyDesc: true, idDesc: true},
	"rising":    {key: "COALESCE(ps.first_work_date,DATE '0001-01-01')", kind: kindDate, keyDesc: true, idDesc: true},
}

// statsSorts read performer_stats and only list performers it has a row for.
var statsSorts = map[string]bool{"heat": true, "followers": true, "rising": true}

// ValidPerformerSort reports whether s is an accepted performer sort.
func ValidPerformerSort(s string) bool { _, ok := performerSorts[s]; return ok }

// PerformerFilter selects a page of active performers. Sort "" means name.
// Era (a decade's first year: 活跃年代), Tag (擅长类型: one of the
// performer's top tags) and AudioLang (作品语言) read performer_stats, like
// the heat / followers / rising sorts.
type PerformerFilter struct {
	Q         string
	Era       int
	Tag       string
	AudioLang string
	Sort      string
	Cursor    *Cursor
	Limit     int
}

// PerformerListItem is a performer plus published-work stats. Followers,
// Heat (30-day heat x 1000), TotalPlays and FirstWorkDate come from
// performer_stats (0 / null before its first run).
type PerformerListItem struct {
	model.Performer
	WorksCount        int64      `json:"works_count"`
	LatestReleaseDate *time.Time `json:"latest_release_date,omitempty"`
	Followers         int64      `json:"followers"`
	Heat              int64      `json:"heat"`
	TotalPlays        int64      `json:"total_plays"`
	FirstWorkDate     *time.Time `json:"first_work_date,omitempty"`
}

func buildPerformerQuery(f PerformerFilter) (string, []any, int, error) {
	if f.Sort == "" {
		f.Sort = "name"
	}
	s, ok := performerSorts[f.Sort]
	if !ok {
		return "", nil, 0, invalidQuery("unknown sort %q", f.Sort)
	}
	if f.Cursor != nil && f.Cursor.Sort != f.Sort {
		return "", nil, 0, invalidQuery("cursor does not match sort")
	}
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	b.cond("p.merged_into_id IS NULL")
	b.cond("p.status='active'")
	if f.Q != "" {
		p := b.arg(f.Q)
		b.cond(fmt.Sprintf("(p.canonical_name ILIKE '%%'||%[1]s||'%%' OR p.canonical_name_ja ILIKE '%%'||%[1]s||'%%' OR p.id IN (SELECT performer_id FROM performer_aliases WHERE normalized_name ILIKE '%%'||lower(%[1]s)||'%%'))", p))
	}
	if f.Era != 0 {
		from := time.Date(f.Era, 1, 1, 0, 0, 0, 0, time.UTC)
		b.cond("ps.first_work_date<" + b.arg(from.AddDate(10, 0, 0)) + " AND ps.latest_work_date>=" + b.arg(from))
	}
	if f.Tag != "" {
		b.cond("ps.top_tags @> ARRAY[" + b.arg(f.Tag) + "::text]")
	}
	if f.AudioLang != "" {
		b.cond("ps.audio_langs @> ARRAY[" + b.arg(f.AudioLang) + "::text]")
	}
	if statsSorts[f.Sort] || f.Era != 0 || f.Tag != "" || f.AudioLang != "" {
		b.cond("ps.performer_id IS NOT NULL")
	}
	// The inner query picks the page from performers + performer_stats
	// only; the live work counts are computed for its limit+1 rows, never
	// for every performer the sort walks. The outer ORDER BY repeats the
	// same order on the carried sort key (pg.sk).
	page := func(key, extra string, kp, ip string) string {
		k := s
		k.key = key
		conds := append(slices.Clone(b.where), extra)
		if kp != "" {
			conds = append(conds, k.keysetCond("p.id", kp, ip))
		}
		return `SELECT p.id,p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,'') name_ja,` + PublicBirthDate("p") + ` birth_date,
	COALESCE(p.height_cm,0) height_cm,COALESCE(p.bust_cm,0) bust_cm,COALESCE(p.waist_cm,0) waist_cm,COALESCE(p.hip_cm,0) hip_cm,p.status,
	COALESCE(ps.followers,0) followers,COALESCE(ps.heat_30d,0) heat,COALESCE(ps.total_plays,0) total_plays,ps.first_work_date,
	` + key + ` sk FROM performers p
	LEFT JOIN performer_stats ps ON ps.performer_id=p.id WHERE ` + strings.Join(slices.DeleteFunc(conds, func(c string) bool { return c == "" }), " AND ") +
			" ORDER BY " + k.orderBy("p.id")
	}
	var kp, ip string
	if f.Cursor != nil {
		k, err := s.parseKey(f.Cursor.Key)
		if err != nil {
			return "", nil, 0, err
		}
		kp, ip = b.arg(k), b.arg(f.Cursor.ID)
	}
	lim := b.arg(limit + 1)
	outer := sortSpec{key: "pg.sk", kind: s.kind, keyDesc: s.keyDesc, idDesc: s.idDesc}
	inner := page(s.key, "", kp, ip) + " LIMIT " + lim
	if s.missingKey != "" {
		// Rows with stats sort on them; the few without (new since the
		// last run) on the live key. Two branches rather than one
		// COALESCE(stats, live subquery): the planner costs a subquery in
		// the sort key once per performer even when it never runs, which
		// at production volume pushes the query over jit_above_cost.
		// The second branch starts from performer_stats_pending, a
		// superset of the performers without stats (migration 061), not
		// from an anti-join of every performer against performer_stats.
		inner = `SELECT * FROM ((` + page(s.key, "ps.performer_id IS NOT NULL", kp, ip) + " LIMIT " + lim + `)
	UNION ALL (` + page(s.missingKey, "ps.performer_id IS NULL AND p.id IN (SELECT performer_id FROM performer_stats_pending)", kp, ip) + " LIMIT " + lim + `)) u
	ORDER BY ` + sortSpec{key: "u.sk", keyDesc: s.keyDesc, idDesc: s.idDesc}.orderBy("u.id") + " LIMIT " + lim
	}
	sql := `SELECT pg.id,pg.slug,pg.canonical_name,pg.name_ja,pg.birth_date,pg.height_cm,pg.bust_cm,pg.waist_cm,pg.hip_cm,pg.status,
	st.works,st.newest,pg.followers,pg.heat,pg.total_plays,pg.first_work_date,` + keyTextExpr(outer) + ` FROM (
	` + inner + `) pg
	CROSS JOIN LATERAL (SELECT count(*) works,max(r.release_date) newest FROM release_performers rp
	 JOIN releases r ON r.id=rp.release_id
	 WHERE rp.performer_id=pg.id AND r.status='published' AND r.merged_into_id IS NULL) st
	ORDER BY ` + outer.orderBy("pg.id")
	return sql, b.args, limit, nil
}

// liveWorksSQL aggregates agg over performer p's published, unmerged works
// (alias lr): the performer_stats works_count / latest_work_date rule.
func liveWorksSQL(agg string) string {
	return "(SELECT " + agg + " FROM release_performers lrp JOIN releases lr ON lr.id=lrp.release_id" +
		" WHERE lrp.performer_id=p.id AND lr.status='published' AND lr.merged_into_id IS NULL)"
}

// PerformerPage returns one page of active performers and the next cursor.
func (s *Store) PerformerPage(ctx context.Context, f PerformerFilter) ([]PerformerListItem, string, error) {
	sql, args, limit, err := buildPerformerQuery(f)
	if err != nil {
		return nil, "", err
	}
	if f.Sort == "" {
		f.Sort = "name"
	}
	r, err := s.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	out := []PerformerListItem{}
	var keys []string
	for r.Next() {
		var x PerformerListItem
		var key string
		if err = r.Scan(&x.ID, &x.Slug, &x.CanonicalName, &x.CanonicalNameJA, &x.BirthDate, &x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.Status, &x.WorksCount, &x.LatestReleaseDate, &x.Followers, &x.Heat, &x.TotalPlays, &x.FirstWorkDate, &key); err != nil {
			return nil, "", err
		}
		out = append(out, x)
		keys = append(keys, key)
	}
	if err = r.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: f.Sort, Key: keys[limit-1], ID: out[limit-1].ID})
	}
	return out, next, nil
}

// ---- release detail ----

// TagRef is a tag as exposed publicly: slug is release_tags.normalized_tag.
type TagRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ReleaseIdentifier is a product code or other identifier of a release.
type ReleaseIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ReleaseDetail is model.Release plus the fields the release page needs.
type ReleaseDetail struct {
	model.Release
	DirectorName string              `json:"director_name"`
	Synopsis     string              `json:"synopsis,omitempty"` // original text; see display_synopsis for ?lang=
	Tags         []TagRef            `json:"tags"`
	Identifiers  []ReleaseIdentifier `json:"identifiers"`
	Series       *SeriesRef          `json:"series,omitempty"` // migrations/027_series.sql
	// ScheduledAt is when an upcoming release goes live (absent: published,
	// or the date is not fixed yet).
	ScheduledAt *time.Time `json:"scheduled_at,omitempty"`
	// OtherVersions are published releases linked as versions of the same
	// work (其他版本, migration 047 release_versions).
	OtherVersions []VersionRef `json:"other_versions"`
}

// ReleaseDetailBySlug returns a published or upcoming release (即将上线,
// reservable from its page) with its tags and identifiers. A missing,
// draft or unpublished release is pgx.ErrNoRows.
func (s *Store) ReleaseDetailBySlug(ctx context.Context, slug string) (ReleaseDetail, error) {
	var x ReleaseDetail
	var seriesID *int64
	var seriesSlug, seriesName *string
	err := s.QueryRow(ctx, `SELECT `+releaseColumns+`,COALESCE(r.director_name,''),COALESCE(r.synopsis,''),se.id,se.slug,se.name,
		CASE WHEN r.status='upcoming' THEN r.scheduled_at END
		FROM releases r LEFT JOIN series se ON se.id=r.series_id
		WHERE r.slug=$1 AND r.merged_into_id IS NULL AND r.status IN ('published','upcoming')`, slug).
		Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status, &x.DirectorName, &x.Synopsis, &seriesID, &seriesSlug, &seriesName, &x.ScheduledAt)
	if err != nil {
		return x, err
	}
	if seriesID != nil && seriesSlug != nil && seriesName != nil {
		x.Series = &SeriesRef{ID: *seriesID, Slug: *seriesSlug, Name: *seriesName}
	}
	x.Tags = []TagRef{}
	rows, err := s.Query(ctx, `SELECT normalized_tag,tag FROM release_tags WHERE release_id=$1 ORDER BY tag`, x.ID)
	if err != nil {
		return x, err
	}
	for rows.Next() {
		var t TagRef
		if err = rows.Scan(&t.Slug, &t.Name); err != nil {
			rows.Close()
			return x, err
		}
		x.Tags = append(x.Tags, t)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return x, err
	}
	x.Identifiers = []ReleaseIdentifier{}
	rows, err = s.Query(ctx, `SELECT identifier_type,identifier_value FROM release_identifiers WHERE release_id=$1 ORDER BY identifier_type,identifier_value`, x.ID)
	if err != nil {
		return x, err
	}
	defer rows.Close()
	for rows.Next() {
		var i ReleaseIdentifier
		if err = rows.Scan(&i.Type, &i.Value); err != nil {
			return x, err
		}
		x.Identifiers = append(x.Identifiers, i)
	}
	if err = rows.Err(); err != nil {
		return x, err
	}
	rows.Close()
	x.OtherVersions, err = s.ReleaseVersions(ctx, x.ID, true)
	return x, err
}

// ---- search ----

// SearchCatalog is the typed catalog search: performers and releases whose
// names/titles match q. Each item keeps {type,id,slug,name} and adds
// release_date, maker_name, runtime_minutes (releases) or canonical_name_ja,
// birth_date (performers), null when unknown. Dates are YYYY-MM-DD.
func (s *Store) SearchCatalog(ctx context.Context, q string, limit int) ([]map[string]any, error) {
	limit = pageLimit(limit)
	r, err := s.Query(ctx, `SELECT 'performer',id,slug,canonical_name,NULL::date,NULL::text,NULL::int,canonical_name_ja,`+PublicBirthDate("performers")+`
		FROM performers WHERE status='active' AND merged_into_id IS NULL AND (canonical_name ILIKE '%'||$1||'%' OR canonical_name_ja ILIKE '%'||$1||'%')
		UNION ALL
		SELECT 'release',id,slug,canonical_title,release_date,maker_name,runtime_minutes::int,NULL::text,NULL::date
		FROM releases WHERE status='published' AND merged_into_id IS NULL AND (canonical_title ILIKE '%'||$1||'%' OR title_ja ILIKE '%'||$1||'%')
		ORDER BY 1,4 LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var typ, slug, name string
		var id int64
		var releaseDate, birthDate *time.Time
		var maker, nameJA *string
		var runtime *int32
		if err = r.Scan(&typ, &id, &slug, &name, &releaseDate, &maker, &runtime, &nameJA, &birthDate); err != nil {
			return nil, err
		}
		out = append(out, searchItem(typ, id, slug, name, releaseDate, maker, runtime, nameJA, birthDate))
	}
	return out, r.Err()
}

// searchItem builds one search result; unknown optional fields are null.
func searchItem(typ string, id int64, slug, name string, releaseDate *time.Time, maker *string, runtime *int32, nameJA *string, birthDate *time.Time) map[string]any {
	m := map[string]any{"type": typ, "id": id, "slug": slug, "name": name}
	date := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.Format(time.DateOnly)
	}
	switch typ {
	case "release":
		m["release_date"] = date(releaseDate)
		m["maker_name"] = nil
		if maker != nil && *maker != "" {
			m["maker_name"] = *maker
		}
		m["runtime_minutes"] = nil
		if runtime != nil {
			m["runtime_minutes"] = *runtime
		}
	case "performer":
		m["canonical_name_ja"] = nil
		if nameJA != nil && *nameJA != "" {
			m["canonical_name_ja"] = *nameJA
		}
		m["birth_date"] = date(birthDate)
	}
	return m
}
