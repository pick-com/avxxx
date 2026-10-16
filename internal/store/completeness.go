package store

// Completeness report data (internal/completeness; migrations/027_series.sql
// completeness_snapshots). Admin only.

import (
	"avxxx/internal/completeness"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobKindCompletenessSnapshot is jobs.kind of the nightly snapshot job.
const JobKindCompletenessSnapshot = completeness.JobKind

// completenessSQL holds, per entity type, the presence expression of every
// field of completeness.Fields (same order), the table alias and filters.
var completenessSQL = map[string]struct {
	table, alias, name, maker, popularity, where string
	have                                         map[string]string
}{
	completeness.EntityRelease: {
		table: "releases", alias: "r", name: "r.canonical_title", maker: "COALESCE(btrim(r.maker_name),'')",
		popularity: "(SELECT count(*) FROM watch_history w WHERE w.release_id=r.id)",
		where:      "r.merged_into_id IS NULL",
		have: map[string]string{
			"canonical_title": "btrim(r.canonical_title)<>''",
			"product_code":    "EXISTS (SELECT 1 FROM release_identifiers i WHERE i.release_id=r.id AND i.identifier_type IN ('dvd_code','content_id'))",
			"release_date":    "r.release_date IS NOT NULL",
			"maker_name":      "COALESCE(btrim(r.maker_name),'')<>''",
			"performers":      "EXISTS (SELECT 1 FROM release_performers rp WHERE rp.release_id=r.id)",
			"cover": "(EXISTS (SELECT 1 FROM media_assets ma WHERE ma.entity_type='release' AND ma.entity_id=r.id AND ma.asset_type IN ('cover','thumbnail') AND ma.suppressed_at IS NULL)" +
				" OR EXISTS (SELECT 1 FROM source_records csr JOIN source_field_values csf ON csf.source_record_id=csr.id AND csf.is_current" +
				" WHERE csr.entity_type='release' AND csr.release_id=r.id AND csf.field_name IN ('cover','cover_url') AND jsonb_typeof(csf.normalized_value)='string' AND csf.normalized_value<>'\"\"'::jsonb))",
			"title_ja":        "COALESCE(btrim(r.title_ja),'')<>''",
			"runtime_minutes": "COALESCE(r.runtime_minutes,0)>0",
			"label_name":      "COALESCE(btrim(r.label_name),'')<>''",
			"series":          "(r.series_id IS NOT NULL OR COALESCE(btrim(r.series_name),'')<>'')",
			"director_name":   "COALESCE(btrim(r.director_name),'')<>''",
			"tags":            "EXISTS (SELECT 1 FROM release_tags t WHERE t.release_id=r.id)",
			"synopsis":        "COALESCE(btrim(r.synopsis),'')<>''",
		},
	},
	completeness.EntityPerformer: {
		table: "performers", alias: "p", name: "p.canonical_name", maker: "''",
		popularity: "(SELECT count(*) FROM release_performers rp JOIN watch_history w ON w.release_id=rp.release_id WHERE rp.performer_id=p.id)",
		where:      "p.merged_into_id IS NULL AND p.status='active'",
		have: map[string]string{
			"canonical_name":    "btrim(p.canonical_name)<>''",
			"canonical_name_ja": "COALESCE(btrim(p.canonical_name_ja),'')<>''",
			"birth_date":        "p.birth_date IS NOT NULL",
			"birth_place":       "COALESCE(btrim(p.birth_place),'')<>''",
			"debut_date":        "p.debut_date IS NOT NULL",
			"height_cm":         "p.height_cm IS NOT NULL",
			"photo":             hasPhotoSQL,
			"measurements":      "(p.bust_cm IS NOT NULL AND p.waist_cm IS NOT NULL AND p.hip_cm IS NOT NULL)",
			"cup_size":          "COALESCE(btrim(p.cup_size),'')<>''",
			"biography":         "COALESCE(btrim(p.biography),'')<>''",
			"aliases":           "EXISTS (SELECT 1 FROM performer_aliases pa WHERE pa.performer_id=p.id)",
			"works": "EXISTS (SELECT 1 FROM release_performers rp JOIN releases wr ON wr.id=rp.release_id" +
				" WHERE rp.performer_id=p.id AND wr.status='published' AND wr.merged_into_id IS NULL)",
		},
	},
}

// completenessQuery renders the facts query of entity. $1/$2 are the parser
// and report field arrays, $3 the source id filter (0 = none).
func completenessQuery(entity string) (string, error) {
	q, ok := completenessSQL[entity]
	if !ok {
		return "", completeness.ErrUnknownEntity
	}
	fields := completeness.Fields(entity)
	have := make([]string, len(fields))
	for i, f := range fields {
		expr, ok := q.have[f.Name]
		if !ok {
			return "", fmt.Errorf("completeness: no SQL for %s.%s", entity, f.Name)
		}
		have[i] = "(" + expr + ")"
	}
	a, fk := q.alias, entity+"_id"
	return `SELECT ` + a + `.id,` + a + `.slug,` + q.name + `,` + q.maker + `,` + q.popularity + `,
		ARRAY[` + strings.Join(have, ",") + `]::boolean[],
		COALESCE((SELECT array_agg(DISTINCT sr.source_id) FROM source_records sr WHERE sr.entity_type='` + entity + `' AND sr.` + fk + `=` + a + `.id),'{}'::bigint[]),
		COALESCE((SELECT array_agg(DISTINCT sr.source_id::text||'|'||m.f) FROM source_records sr
			JOIN source_field_values sf ON sf.source_record_id=sr.id AND sf.is_current
			JOIN unnest($1::text[],$2::text[]) m(p,f) ON m.p=sf.field_name
			WHERE sr.entity_type='` + entity + `' AND sr.` + fk + `=` + a + `.id
			AND sf.normalized_value IS NOT NULL AND sf.normalized_value NOT IN ('null'::jsonb,'""'::jsonb,'[]'::jsonb,'{}'::jsonb)),'{}'::text[])
		FROM ` + q.table + ` ` + a + ` WHERE ` + q.where + `
		AND ($3::bigint=0 OR EXISTS (SELECT 1 FROM source_records fsr WHERE fsr.entity_type='` + entity + `' AND fsr.` + fk + `=` + a + `.id AND fsr.source_id=$3))
		ORDER BY ` + a + `.id`, nil
}

// CompletenessSources implements completeness.Reader.
func (s *Store) CompletenessSources(ctx context.Context) ([]completeness.SourceInfo, error) {
	return collect[completeness.SourceInfo](s.Query(ctx, `SELECT id,name,source_kind FROM sources ORDER BY id`))(func(r pgx.Rows) (completeness.SourceInfo, error) {
		var x completeness.SourceInfo
		return x, r.Scan(&x.ID, &x.Name, &x.Kind)
	})
}

// CompletenessFacts implements completeness.Reader.
func (s *Store) CompletenessFacts(ctx context.Context, entity string, sourceID int64, fn func(completeness.Facts) error) error {
	sql, err := completenessQuery(entity)
	if err != nil {
		return err
	}
	pm := completeness.ParserMap(entity)
	parsers, fields := make([]string, 0, len(pm)), make([]string, 0, len(pm))
	for p, f := range pm {
		parsers, fields = append(parsers, p), append(fields, f)
	}
	rows, err := s.Query(ctx, sql, parsers, fields, sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f completeness.Facts
		var provided []string
		if err = rows.Scan(&f.ID, &f.Slug, &f.Name, &f.Maker, &f.Popularity, &f.Have, &f.Sources, &provided); err != nil {
			return err
		}
		for _, p := range provided {
			src, field, ok := strings.Cut(p, "|")
			id, perr := strconv.ParseInt(src, 10, 64)
			if !ok || perr != nil {
				continue
			}
			if f.Provided == nil {
				f.Provided = map[int64][]string{}
			}
			f.Provided[id] = append(f.Provided[id], field)
		}
		if err = fn(f); err != nil {
			return err
		}
	}
	return rows.Err()
}

// CompletenessSnapshots implements completeness.Reader: the latest n
// snapshots of entity.
func (s *Store) CompletenessSnapshots(ctx context.Context, entity string, n int) ([]completeness.Snapshot, error) {
	return collect[completeness.Snapshot](s.Query(ctx, `SELECT snapshot_date,entity_type,total,required_complete,fields,sources,COALESCE(job_id,0)
		FROM completeness_snapshots WHERE entity_type=$1 ORDER BY snapshot_date DESC LIMIT $2`, entity, n))(func(r pgx.Rows) (completeness.Snapshot, error) {
		var x completeness.Snapshot
		var fields, sources []byte
		if err := r.Scan(&x.Date, &x.Entity, &x.Total, &x.RequiredComplete, &fields, &sources, &x.JobID); err != nil {
			return x, err
		}
		if err := json.Unmarshal(fields, &x.Fields); err != nil {
			return x, err
		}
		return x, json.Unmarshal(sources, &x.Sources)
	})
}

// SaveCompletenessSnapshot implements completeness.Store; a second run on
// the same day replaces that day's row.
func (s *Store) SaveCompletenessSnapshot(ctx context.Context, x completeness.Snapshot) error {
	fields, err := json.Marshal(x.Fields)
	if err != nil {
		return err
	}
	sources, err := json.Marshal(x.Sources)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO completeness_snapshots(snapshot_date,entity_type,total,required_complete,fields,sources,job_id)
		VALUES($1,$2,$3,$4,$5::jsonb,$6::jsonb,NULLIF($7,0))
		ON CONFLICT(snapshot_date,entity_type) DO UPDATE SET total=EXCLUDED.total,required_complete=EXCLUDED.required_complete,
		fields=EXCLUDED.fields,sources=EXCLUDED.sources,job_id=EXCLUDED.job_id,created_at=now()`,
		x.Date.Format(time.DateOnly), x.Entity, x.Total, x.RequiredComplete, string(fields), string(sources), x.JobID)
	return err
}

// CompletenessReport builds GET /api/admin/reports/completeness.
func (s *Store) CompletenessReport(ctx context.Context, opts completeness.Options) (completeness.Report, error) {
	return completeness.BuildReport(ctx, s, opts, time.Now().UTC())
}
