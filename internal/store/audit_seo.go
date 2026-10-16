package store

// R-SEO inputs (internal/audit/seo.go). Entities are the ones the sitemap
// lists (sitemap.go: playableReleaseSQL, sitemapPerformerSQL), so a page the
// crawler cannot find through the sitemap is never raised here.

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
)

// storedImageSQL: a visible stored original of the public asset type
// (images.StoredAssetTypes: release cover, performer avatar).
const storedImageSQL = `EXISTS (SELECT 1 FROM media_assets ma WHERE ma.entity_type=%[1]s AND ma.entity_id=%[2]s
	AND ma.asset_type=%[3]s AND ma.variant='original' AND ma.suppressed_at IS NULL)`

// SEOEntities implements audit.Store.
func (s *Store) SEOEntities(ctx context.Context, entityType string, afterID int64, limit int) ([]audit.SEOFacts, error) {
	var sql string
	switch entityType {
	case "release":
		sql = `SELECT r.id,r.slug,r.canonical_title,
			COALESCE(btrim(r.synopsis),'')<>'',
			(COALESCE(btrim(r.maker_name),'')<>'' OR r.release_date IS NOT NULL OR COALESCE(r.runtime_minutes,0)>0
				OR EXISTS (SELECT 1 FROM release_performers rp WHERE rp.release_id=r.id)),
			` + fmt.Sprintf(storedImageSQL, "'release'", "r.id", "'cover'") + `,
			(SELECT count(*) FROM watch_history w WHERE w.release_id=r.id)
			FROM releases r WHERE r.id>$1 AND ` + playableReleaseSQL + ` ORDER BY r.id LIMIT $2`
	case "performer":
		sql = `SELECT p.id,p.slug,p.canonical_name,
			COALESCE(btrim(p.biography),'')<>'',
			true,
			` + fmt.Sprintf(storedImageSQL, "'performer'", "p.id", "'avatar'") + `,
			(SELECT count(DISTINCT rp.release_id) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
				WHERE rp.performer_id=p.id AND r.status='published' AND r.merged_into_id IS NULL)
			FROM performers p WHERE p.id>$1 AND ` + sitemapPerformerSQL + ` ORDER BY p.id LIMIT $2`
	default:
		return nil, fmt.Errorf("seo entities: unknown entity type %q", entityType)
	}
	rows, err := s.Query(ctx, sql, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.SEOFacts
	for rows.Next() {
		f := audit.SEOFacts{EntityType: entityType}
		if err = rows.Scan(&f.ID, &f.Slug, &f.Title, &f.HasSynopsis, &f.HasFacts, &f.HasImage, &f.Popularity); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// seoTitleKey normalizes a title for duplicate detection: case and runs of
// whitespace do not make two pages different.
const seoTitleKey = `lower(regexp_replace(btrim(%s), '\s+', ' ', 'g'))`

// SEODuplicateTitles implements audit.Store.
func (s *Store) SEODuplicateTitles(ctx context.Context) ([]audit.TitleGroup, error) {
	canonical := fmt.Sprintf(seoTitleKey, "r.canonical_title")
	translated := fmt.Sprintf(seoTitleKey, "t.reviewed_value")
	rows, err := s.Query(ctx, `
		SELECT '' AS lang,`+canonical+` AS k,array_agg(r.id ORDER BY r.id) FROM releases r
		WHERE `+playableReleaseSQL+` AND btrim(r.canonical_title)<>''
		GROUP BY k HAVING count(*)>1
		UNION ALL
		SELECT t.lang,`+translated+` AS k,array_agg(r.id ORDER BY r.id) FROM translations t
		JOIN releases r ON r.id::text=t.entity_id
		WHERE t.entity_type='release' AND t.field='title' AND t.status IN ('reviewed','stale')
		  AND btrim(COALESCE(t.reviewed_value,''))<>'' AND `+playableReleaseSQL+`
		GROUP BY t.lang,k HAVING count(*)>1
		ORDER BY 1,2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.TitleGroup
	for rows.Next() {
		var g audit.TitleGroup
		if err = rows.Scan(&g.Lang, &g.Title, &g.IDs); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// SEOSite implements audit.Store.
func (s *Store) SEOSite(ctx context.Context) (audit.SEOSiteFacts, error) {
	var f audit.SEOSiteFacts
	err := s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM releases r WHERE `+playableReleaseSQL+`),
		(SELECT count(*) FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL),
		(SELECT count(*) FROM performers p WHERE `+sitemapPerformerSQL+`),
		(SELECT count(*) FROM releases r WHERE `+playableReleaseSQL+` AND `+fmt.Sprintf(storedImageSQL, "'release'", "r.id", "'cover'")+`)`).
		Scan(&f.SitemapReleases, &f.PublishedReleases, &f.SitemapPerformers, &f.ReleasesWithCover)
	if err != nil {
		return f, err
	}
	rows, err := s.Query(ctx, `SELECT t.lang,count(DISTINCT r.id) FROM translations t
		JOIN releases r ON r.id::text=t.entity_id
		WHERE t.entity_type='release' AND t.field='title' AND t.status IN ('reviewed','stale') AND `+playableReleaseSQL+`
		GROUP BY t.lang ORDER BY t.lang`)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	f.TitleTranslations = map[string]int64{}
	for rows.Next() {
		var lang string
		var n int64
		if err = rows.Scan(&lang, &n); err != nil {
			return f, err
		}
		f.TitleTranslations[lang] = n
	}
	return f, rows.Err()
}
