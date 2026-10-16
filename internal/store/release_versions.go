package store

// AdminDedupe 合并为其他版本 and the compare panel's entity summaries
// (migration 047 release_versions).
//
// A video candidate has three decisions: 新片源 is the ordinary merge
// (MergeCandidate: the other side's playback sources, identifiers and user
// data move to the kept release), 其他版本 links the two releases as
// versions of one work without merging (LinkCandidateVersions), and 不是重复
// is MarkNotDuplicate. Every decision writes a review_events row.

import (
	"avxxx/internal/identity"
	"avxxx/internal/model"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxReleaseVersions bounds the versions listed for one release.
const MaxReleaseVersions = 20

// VersionRef is another version of a release, as the release page lists it.
type VersionRef struct {
	ID             int64      `json:"id"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	ReleaseDate    *time.Time `json:"release_date,omitempty"`
	RuntimeMinutes *int16     `json:"runtime_minutes,omitempty"`
	EditionType    string     `json:"edition_type"`
	Censorship     *string    `json:"censorship_type,omitempty"`
	Code           string     `json:"code,omitempty"`
	Status         string     `json:"status"`
}

// releaseVersionsSQL lists the releases linked to $1 directly or through
// other links (a merged release stands for the release it was merged into),
// excluding $1 itself. publicOnly ($2) keeps published, unmerged releases.
var releaseVersionsSQL = `
WITH RECURSIVE edges AS (
  SELECT COALESCE(ra.merged_into_id, v.a_id) AS a, COALESCE(rb.merged_into_id, v.b_id) AS b
  FROM release_versions v JOIN releases ra ON ra.id = v.a_id JOIN releases rb ON rb.id = v.b_id
),
reach(id, depth) AS (
  SELECT $1::bigint, 0
  UNION
  SELECT CASE WHEN e.a = r.id THEN e.b ELSE e.a END, r.depth + 1
  FROM reach r JOIN edges e ON e.a = r.id OR e.b = r.id
  WHERE r.depth < 5
)
SELECT r.id, r.slug, r.canonical_title, r.release_date, r.runtime_minutes, r.edition_type, r.censorship_type,
  COALESCE((SELECT i.identifier_value FROM release_identifiers i WHERE i.release_id = r.id
    ORDER BY (i.identifier_type = 'dvd_code') DESC, i.identifier_type, i.identifier_value LIMIT 1), ''),
  r.status
FROM (SELECT DISTINCT id FROM reach WHERE id <> $1) x
JOIN releases r ON r.id = x.id AND r.merged_into_id IS NULL
WHERE NOT $2::boolean OR r.status = 'published'
ORDER BY r.release_date NULLS LAST, r.id
LIMIT ` + itoa(MaxReleaseVersions)

// ReleaseVersions returns the other versions of a release; publicOnly keeps
// published ones (the release page).
func (s *Store) ReleaseVersions(ctx context.Context, releaseID int64, publicOnly bool) ([]VersionRef, error) {
	out, err := collectRows(ctx, s, releaseVersionsSQL, []any{releaseID, publicOnly}, func(r pgx.Rows) (VersionRef, error) {
		var v VersionRef
		return v, r.Scan(&v.ID, &v.Slug, &v.Title, &v.ReleaseDate, &v.RuntimeMinutes, &v.EditionType, &v.Censorship, &v.Code, &v.Status)
	})
	if out == nil {
		out = []VersionRef{}
	}
	return out, err
}

// LinkCandidateVersions decides a release candidate as 其他版本: the two
// releases stay separate and are linked in release_versions. The pair is
// also remembered in not_duplicate_pairs (they are different entities, so
// candidate generation and ingest never fold them together).
func (s *Store) LinkCandidateVersions(ctx context.Context, candidateID int64, actor, reason string) (DecisionResult, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return DecisionResult{}, err
	}
	defer tx.Rollback(ctx)
	c, err := lockCandidate(ctx, tx, candidateID)
	if err != nil {
		return DecisionResult{}, err
	}
	if c.EntityType != "release" {
		return DecisionResult{}, invalid("only video candidates can be linked as versions")
	}
	ents, err := lockEntities(ctx, tx, "releases", c.Left, c.Right)
	if err != nil {
		return DecisionResult{}, err
	}
	for _, id := range []int64{c.Left, c.Right} {
		e, ok := ents[id]
		if !ok {
			return DecisionResult{}, pgx.ErrNoRows
		}
		if e.MergedTo != nil {
			return DecisionResult{}, conflict("release %d was merged into %d", id, *e.MergedTo)
		}
	}
	p := identity.CanonicalPair(c.Left, c.Right)
	before, _ := json.Marshal(map[string]any{"candidate_id": c.ID, "status": c.Status, "left_id": c.Left, "right_id": c.Right})
	after, _ := json.Marshal(map[string]any{"candidate_id": c.ID, "status": "versions", "a_id": p.Lo, "b_id": p.Hi,
		"a_slug": ents[p.Lo].Slug, "b_slug": ents[p.Hi].Slug})
	res := DecisionResult{CandidateID: c.ID, Status: "versions"}
	if err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor,candidate_id)
		VALUES('release',$1,'link_versions',$2::jsonb,$3::jsonb,NULLIF($4,''),$5,$6) RETURNING id`,
		p.Lo, string(before), string(after), reason, actor, c.ID).Scan(&res.ReviewEventID); err != nil {
		return DecisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO release_versions(a_id,b_id,review_event_id,created_by) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
		p.Lo, p.Hi, res.ReviewEventID, actor); err != nil {
		return DecisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO not_duplicate_pairs(entity_type,a_id,b_id,review_event_id) VALUES('release',$1,$2,$3) ON CONFLICT DO NOTHING`,
		p.Lo, p.Hi, res.ReviewEventID); err != nil {
		return DecisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity_candidates SET status='versions' WHERE id=$1`, c.ID); err != nil {
		return DecisionResult{}, err
	}
	return res, tx.Commit(ctx)
}

// UnlinkReleaseVersion removes the direct link between two releases and
// records an unlink_versions review event. The pair stays in
// not_duplicate_pairs: they remain different entities.
func (s *Store) UnlinkReleaseVersion(ctx context.Context, releaseID, otherID int64, actor, reason string) (int64, error) {
	if releaseID == otherID {
		return 0, invalid("a release is not a version of itself")
	}
	p := identity.CanonicalPair(releaseID, otherID)
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var linkEvent *int64
	var by *string
	if err = tx.QueryRow(ctx, `DELETE FROM release_versions WHERE a_id=$1 AND b_id=$2 RETURNING review_event_id, created_by`, p.Lo, p.Hi).Scan(&linkEvent, &by); err != nil {
		return 0, err
	}
	before, _ := json.Marshal(map[string]any{"a_id": p.Lo, "b_id": p.Hi, "review_event_id": linkEvent, "created_by": by})
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES('release',$1,'unlink_versions',$2::jsonb,NULL,NULLIF($3,''),$4) RETURNING id`, p.Lo, string(before), reason, actor).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

// ---------------------------------------------------------------- summaries

// SummarySource is one catalog source record of an entity.
type SummarySource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

// SummaryPlayback is one playback source or crawled media mapping.
type SummaryPlayback struct {
	Kind     string `json:"kind"` // playback_source | media_source
	Provider string `json:"provider"`
	Label    string `json:"label"`
	URL      string `json:"url,omitempty"`
	Status   string `json:"status"`
}

// EntitySummary is one side of the AdminDedupe compare panel.
type EntitySummary struct {
	ID          int64      `json:"id"`
	EntityType  string     `json:"entity_type"`
	Slug        string     `json:"slug"`
	Title       string     `json:"title"`
	TitleJA     *string    `json:"title_ja,omitempty"`
	Status      string     `json:"status"`
	MergedInto  *int64     `json:"merged_into_id,omitempty"`
	ReleaseDate *time.Time `json:"release_date,omitempty"`
	// Release fields.
	Codes          []string          `json:"codes,omitempty"`
	RuntimeMinutes *int16            `json:"runtime_minutes,omitempty"`
	Maker          *string           `json:"maker,omitempty"`
	Label          *string           `json:"label,omitempty"`
	Series         *string           `json:"series,omitempty"`
	EditionType    string            `json:"edition_type,omitempty"`
	Censorship     *string           `json:"censorship_type,omitempty"`
	Performers     []string          `json:"performers,omitempty"`
	Playback       []SummaryPlayback `json:"playback,omitempty"`
	Versions       []VersionRef      `json:"versions,omitempty"`
	// Performer fields.
	BirthDate *time.Time `json:"birth_date,omitempty"`
	Aliases   []string   `json:"aliases,omitempty"`
	Works     *int       `json:"works,omitempty"`
	// Both. Cover is the stored cover / portrait (set by the API).
	Sources []SummarySource `json:"sources"`
	Cover   *model.Image    `json:"cover,omitempty"`
}

// CandidateCompare is GET /api/admin/identity-candidates/{id}/compare.
type CandidateCompare struct {
	CandidateID int64           `json:"candidate_id"`
	EntityType  string          `json:"entity_type"`
	Status      string          `json:"status"`
	Score       float64         `json:"score"`
	Evidence    json.RawMessage `json:"evidence"`
	Left        EntitySummary   `json:"left"`
	Right       EntitySummary   `json:"right"`
}

// CandidateCompare loads both sides of an identity candidate.
func (s *Store) CandidateCompare(ctx context.Context, candidateID int64) (CandidateCompare, error) {
	var c CandidateCompare
	var left, right int64
	if err := s.QueryRow(ctx, `SELECT id,entity_type,status,score::float8,evidence,left_id,right_id FROM identity_candidates WHERE id=$1`, candidateID).
		Scan(&c.CandidateID, &c.EntityType, &c.Status, &c.Score, &c.Evidence, &left, &right); err != nil {
		return c, err
	}
	var err error
	if c.Left, err = s.EntitySummary(ctx, c.EntityType, left); err != nil {
		return c, err
	}
	c.Right, err = s.EntitySummary(ctx, c.EntityType, right)
	return c, err
}

func strRows(ctx context.Context, s *Store, sql string, args ...any) ([]string, error) {
	out, err := collectRows(ctx, s, sql, args, func(r pgx.Rows) (string, error) {
		var v string
		return v, r.Scan(&v)
	})
	if out == nil {
		out = []string{}
	}
	return out, err
}

// EntitySummary loads the compare-panel summary of a release or performer
// (any status, merged ones included so a stale candidate still shows).
func (s *Store) EntitySummary(ctx context.Context, entityType string, id int64) (EntitySummary, error) {
	e := EntitySummary{ID: id, EntityType: entityType}
	var err error
	switch entityType {
	case "release":
		err = s.QueryRow(ctx, `SELECT slug,canonical_title,title_ja,status,merged_into_id,release_date,runtime_minutes,maker_name,label_name,series_name,edition_type,censorship_type
			FROM releases WHERE id=$1`, id).Scan(&e.Slug, &e.Title, &e.TitleJA, &e.Status, &e.MergedInto, &e.ReleaseDate, &e.RuntimeMinutes,
			&e.Maker, &e.Label, &e.Series, &e.EditionType, &e.Censorship)
		if err != nil {
			return e, err
		}
		if e.Codes, err = strRows(ctx, s, `SELECT identifier_value FROM release_identifiers WHERE release_id=$1 ORDER BY identifier_type, identifier_value`, id); err != nil {
			return e, err
		}
		if e.Performers, err = strRows(ctx, s, `SELECT COALESCE(NULLIF(rp.credited_name,''), p.canonical_name) FROM release_performers rp JOIN performers p ON p.id=rp.performer_id
			WHERE rp.release_id=$1 ORDER BY rp.is_primary DESC, rp.billing_order NULLS LAST, p.id`, id); err != nil {
			return e, err
		}
		e.Playback, err = collectRows(ctx, s, `SELECT 'playback_source', provider, label, COALESCE(url,''), status FROM playback_sources WHERE release_id=$1
			UNION ALL
			SELECT 'media_source', source, media_id, COALESCE(media_url,''), confidence FROM release_media_sources WHERE release_id=$1
			ORDER BY 1 DESC, 2, 3`, []any{id}, func(r pgx.Rows) (SummaryPlayback, error) {
			var p SummaryPlayback
			return p, r.Scan(&p.Kind, &p.Provider, &p.Label, &p.URL, &p.Status)
		})
		if err != nil {
			return e, err
		}
		if e.Versions, err = s.ReleaseVersions(ctx, id, false); err != nil {
			return e, err
		}
	case "performer":
		err = s.QueryRow(ctx, `SELECT slug,canonical_name,canonical_name_ja,status,merged_into_id,birth_date,
			(SELECT count(*)::int FROM release_performers WHERE performer_id=$1) FROM performers WHERE id=$1`, id).
			Scan(&e.Slug, &e.Title, &e.TitleJA, &e.Status, &e.MergedInto, &e.BirthDate, &e.Works)
		if err != nil {
			return e, err
		}
		if e.Aliases, err = strRows(ctx, s, `SELECT name FROM performer_aliases WHERE performer_id=$1 ORDER BY name LIMIT 30`, id); err != nil {
			return e, err
		}
	default:
		return e, invalid("unknown entity type %q", entityType)
	}
	col := "release_id"
	if entityType == "performer" {
		col = "performer_id"
	}
	e.Sources, err = collectRows(ctx, s, `SELECT so.name, sr.source_url FROM source_records sr JOIN sources so ON so.id=sr.source_id
		WHERE sr.`+col+`=$1 ORDER BY so.name, sr.id LIMIT 20`, []any{id}, func(r pgx.Rows) (SummarySource, error) {
		var x SummarySource
		return x, r.Scan(&x.Source, &x.URL)
	})
	if e.Sources == nil {
		e.Sources = []SummarySource{}
	}
	return e, err
}
