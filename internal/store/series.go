package store

// Public series queries (migrations/027_series.sql; GET /api/series,
// /api/series/{slug}, /api/series/{slug}/releases). Like every public
// catalog query they only count published, unmerged releases and active,
// unmerged performers; a series without a published release is not found.

import (
	"avxxx/internal/model"
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SeriesRef is the series of a release on the release detail.
type SeriesRef struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// SeriesListItem is one series with its published-release stats.
type SeriesListItem struct {
	ID                int64      `json:"id"`
	Slug              string     `json:"slug"`
	Name              string     `json:"name"`
	NameJA            string     `json:"name_ja,omitempty"`
	MakerName         string     `json:"maker_name,omitempty"`
	ReleaseCount      int64      `json:"release_count"`
	FirstReleaseDate  *time.Time `json:"first_release_date,omitempty"`
	LatestReleaseDate *time.Time `json:"latest_release_date,omitempty"`
}

var seriesSorts = map[string]sortSpec{
	"name":   {key: "s.name", kind: kindText},
	"works":  {key: "st.works", kind: kindInt, keyDesc: true},
	"newest": {key: "COALESCE(st.newest,DATE '0001-01-01')", kind: kindDate, keyDesc: true, idDesc: true},
}

func init() {
	for name, s := range seriesSorts {
		s.keyText = keyTextExpr(s)
		seriesSorts[name] = s
	}
}

// ValidSeriesSort reports whether s is an accepted series list sort.
func ValidSeriesSort(s string) bool { _, ok := seriesSorts[s]; return ok }

// SeriesFilter selects a page of series. Sort "" means newest.
type SeriesFilter struct {
	Q      string
	Sort   string
	Cursor *Cursor
	Limit  int
}

const seriesStats = ` CROSS JOIN LATERAL (SELECT count(*) works,min(r.release_date) first,max(r.release_date) newest
	FROM releases r WHERE r.series_id=s.id AND r.status='published' AND r.merged_into_id IS NULL) st`

func buildSeriesQuery(f SeriesFilter) (string, []any, int, error) {
	if f.Sort == "" {
		f.Sort = "newest"
	}
	s, ok := seriesSorts[f.Sort]
	if !ok {
		return "", nil, 0, invalidQuery("unknown sort %q", f.Sort)
	}
	if f.Cursor != nil && f.Cursor.Sort != f.Sort {
		return "", nil, 0, invalidQuery("cursor does not match sort")
	}
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	b.cond("st.works>0")
	if f.Q != "" {
		p := b.arg(f.Q)
		b.cond(fmt.Sprintf("(s.name ILIKE '%%'||%[1]s||'%%' OR s.name_ja ILIKE '%%'||%[1]s||'%%' OR s.slug ILIKE '%%'||%[1]s||'%%')", p))
	}
	if err := b.after(s, "s.id", f.Cursor); err != nil {
		return "", nil, 0, err
	}
	sql := `SELECT s.id,s.slug,s.name,COALESCE(s.name_ja,''),s.maker_name,st.works,st.first,st.newest,` + s.keyText +
		` FROM series s` + seriesStats + b.whereSQL() + " ORDER BY " + s.orderBy("s.id") + " LIMIT " + b.arg(limit+1)
	return sql, b.args, limit, nil
}

// SeriesPage returns one page of series with at least one published
// release, and the next cursor ("" on the last page).
func (s *Store) SeriesPage(ctx context.Context, f SeriesFilter) ([]SeriesListItem, string, error) {
	sql, args, limit, err := buildSeriesQuery(f)
	if err != nil {
		return nil, "", err
	}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	rows, err := s.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []SeriesListItem{}
	var keys []string
	for rows.Next() {
		var x SeriesListItem
		var key string
		if err = rows.Scan(&x.ID, &x.Slug, &x.Name, &x.NameJA, &x.MakerName, &x.ReleaseCount, &x.FirstReleaseDate, &x.LatestReleaseDate, &key); err != nil {
			return nil, "", err
		}
		out = append(out, x)
		keys = append(keys, key)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: f.Sort, Key: keys[limit-1], ID: out[limit-1].ID})
	}
	return out, next, nil
}

// SeriesMaker is a maker of a series' published releases.
type SeriesMaker struct {
	Name         string `json:"name"`
	ReleaseCount int64  `json:"release_count"`
}

// SeriesPerformer is a performer credited in a series.
type SeriesPerformer struct {
	ID           int64  `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	NameJA       string `json:"name_ja,omitempty"`
	ReleaseCount int64  `json:"release_count"`
}

// SeriesDetail is GET /api/series/{slug}.
type SeriesDetail struct {
	SeriesListItem
	Makers        []SeriesMaker     `json:"makers"`
	TopPerformers []SeriesPerformer `json:"top_performers"`
}

// SeriesDetailBySlug returns a series with at least one published release.
// Unknown or empty: pgx.ErrNoRows.
func (s *Store) SeriesDetailBySlug(ctx context.Context, slug string, topPerformers int) (SeriesDetail, error) {
	var x SeriesDetail
	err := s.QueryRow(ctx, `SELECT s.id,s.slug,s.name,COALESCE(s.name_ja,''),s.maker_name,st.works,st.first,st.newest
		FROM series s`+seriesStats+` WHERE s.slug=$1 AND st.works>0`, slug).
		Scan(&x.ID, &x.Slug, &x.Name, &x.NameJA, &x.MakerName, &x.ReleaseCount, &x.FirstReleaseDate, &x.LatestReleaseDate)
	if err != nil {
		return x, err
	}
	if x.Makers, err = collect[SeriesMaker](s.Query(ctx, `SELECT COALESCE(NULLIF(btrim(r.maker_name),''),'(unknown)'),count(*)
		FROM releases r WHERE r.series_id=$1 AND r.status='published' AND r.merged_into_id IS NULL
		GROUP BY 1 ORDER BY 2 DESC,1`, x.ID))(func(r pgx.Rows) (SeriesMaker, error) {
		var m SeriesMaker
		return m, r.Scan(&m.Name, &m.ReleaseCount)
	}); err != nil {
		return x, err
	}
	if topPerformers <= 0 || topPerformers > 50 {
		topPerformers = 10
	}
	x.TopPerformers, err = collect[SeriesPerformer](s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,''),count(*)
		FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id
		WHERE r.series_id=$1 AND r.status='published' AND r.merged_into_id IS NULL AND p.status='active' AND p.merged_into_id IS NULL
		GROUP BY p.id ORDER BY count(*) DESC,p.canonical_name,p.id LIMIT $2`, x.ID, topPerformers))(func(r pgx.Rows) (SeriesPerformer, error) {
		var p SeriesPerformer
		return p, r.Scan(&p.ID, &p.Slug, &p.Name, &p.NameJA, &p.ReleaseCount)
	})
	return x, err
}

// MaxSeriesReleases bounds how many releases of one series are ordered in
// memory (episode order needs every title of the series).
const MaxSeriesReleases = 5000

// SeriesRelease is a release in a series listing.
type SeriesRelease struct {
	model.Release
	ProductCode string `json:"product_code,omitempty"`
}

// SeriesReleases returns every published release of a series (up to
// MaxSeriesReleases, newest first) with its product code. Unknown series or
// one without published releases: pgx.ErrNoRows.
func (s *Store) SeriesReleases(ctx context.Context, slug string) ([]SeriesRelease, error) {
	var id int64
	if err := s.QueryRow(ctx, `SELECT s.id FROM series s`+seriesStats+` WHERE s.slug=$1 AND st.works>0`, slug).Scan(&id); err != nil {
		return nil, err
	}
	return collect[SeriesRelease](s.Query(ctx, `SELECT `+releaseColumns+`,
		COALESCE((SELECT i.identifier_value FROM release_identifiers i WHERE i.release_id=r.id AND i.identifier_type='dvd_code' ORDER BY i.id LIMIT 1),'')
		FROM releases r WHERE r.series_id=$1 AND r.status='published' AND r.merged_into_id IS NULL
		ORDER BY r.release_date DESC NULLS LAST,r.id DESC LIMIT $2`, id, MaxSeriesReleases))(func(r pgx.Rows) (SeriesRelease, error) {
		var x SeriesRelease
		return x, r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status, &x.ProductCode)
	})
}
