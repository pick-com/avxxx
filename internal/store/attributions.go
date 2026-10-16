package store

// Source credits (sources.attribution_html, migrations/025_dmm_source.sql):
// the text a source's terms require wherever its data is displayed, e.g.
// DMM's "Powered by <a href=...>FANZA Webサービス</a>". Public read for
// GET /api/attributions (internal/api/attributions.go).

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Attribution is one source's required credit, exactly as stored.
type Attribution struct {
	Source string `json:"source"`
	HTML   string `json:"html"`
}

// AttributionScope picks the entity whose displayed data the credits cover.
// Kind "" means any data on the site.
type AttributionScope struct {
	Kind string // "", "release", "performer" or "series"
	Slug string
}

// ValidAttributionKind reports whether k is an accepted scope kind.
func ValidAttributionKind(k string) bool {
	switch k {
	case "", "release", "performer", "series":
		return true
	}
	return false
}

// Attributions returns the credits of sources that carry attribution_html
// and have at least one source record behind the scope's displayed data:
// a published release (and releases merged into it), an active performer
// (likewise), the published releases of a series, or anything (Kind "").
// Whether the source is still enabled does not matter: data already
// displayed keeps its credit. Unknown slugs give an empty list.
func (s *Store) Attributions(ctx context.Context, scope AttributionScope) ([]Attribution, error) {
	var filter string
	args := []any{}
	switch scope.Kind {
	case "release":
		filter = ` AND sr.entity_type='release' AND sr.release_id IN (
			SELECT e.id FROM releases e JOIN releases p ON e.id=p.id OR e.merged_into_id=p.id
			WHERE p.slug=$1 AND p.status='published' AND p.merged_into_id IS NULL)`
		args = append(args, scope.Slug)
	case "performer":
		filter = ` AND sr.entity_type='performer' AND sr.performer_id IN (
			SELECT e.id FROM performers e JOIN performers p ON e.id=p.id OR e.merged_into_id=p.id
			WHERE p.slug=$1 AND p.status='active' AND p.merged_into_id IS NULL)`
		args = append(args, scope.Slug)
	case "series":
		filter = ` AND sr.entity_type='release' AND sr.release_id IN (
			SELECT r.id FROM releases r JOIN series x ON x.id=r.series_id
			WHERE x.slug=$1 AND r.status='published' AND r.merged_into_id IS NULL)`
		args = append(args, scope.Slug)
	}
	return collect[Attribution](s.Query(ctx, `SELECT s.name,s.attribution_html FROM sources s
		WHERE s.attribution_html IS NOT NULL AND btrim(s.attribution_html)<>''
		AND EXISTS (SELECT 1 FROM source_records sr WHERE sr.source_id=s.id`+filter+`)
		ORDER BY s.priority,s.id`, args...))(func(r pgx.Rows) (Attribution, error) {
		var a Attribution
		return a, r.Scan(&a.Source, &a.HTML)
	})
}
