package store

// Public source evidence (GET /api/releases/{slug}/evidence,
// GET /api/performers/{slug}/evidence; internal/evidence). Reads only
// published releases / active performers. Never selects source_records.raw_data,
// override reasons or actors; source values are restricted to JSON scalars
// of the reported fields, and a hidden birthday is left out entirely.

import (
	"avxxx/internal/evidence"
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReleaseEvidence returns the evidence of a published release. Unknown or
// unpublished: pgx.ErrNoRows.
func (s *Store) ReleaseEvidence(ctx context.Context, slug string) (evidence.Evidence, error) {
	in := evidence.Input{EntityType: "release", Slug: slug, Hidden: map[string]bool{}}
	var id int64
	var canonical []byte
	err := s.QueryRow(ctx, `SELECT r.id,r.updated_at,json_build_object(
			'canonical_title',r.canonical_title,'title_ja',r.title_ja,'release_date',r.release_date::text,
			'runtime_minutes',r.runtime_minutes,'maker_name',r.maker_name,'label_name',r.label_name,
			'series_name',r.series_name,'director_name',r.director_name,'synopsis',r.synopsis,
			'dvd_code',(SELECT i.identifier_value FROM release_identifiers i WHERE i.release_id=r.id AND i.identifier_type='dvd_code' ORDER BY i.id LIMIT 1),
			'content_id',(SELECT i.identifier_value FROM release_identifiers i WHERE i.release_id=r.id AND i.identifier_type='content_id' ORDER BY i.id LIMIT 1))
		FROM releases r WHERE r.slug=$1 AND r.status='published' AND r.merged_into_id IS NULL`, slug).Scan(&id, &in.UpdatedAt, &canonical)
	if err != nil {
		return evidence.Evidence{}, err
	}
	if err = s.loadEvidence(ctx, &in, id, canonical); err != nil {
		return evidence.Evidence{}, err
	}
	return evidence.Build(in), nil
}

// PerformerEvidence returns the evidence of an active performer. Unknown or
// hidden: pgx.ErrNoRows. A hidden birthday (PRD 15) has no birth_date entry.
func (s *Store) PerformerEvidence(ctx context.Context, slug string) (evidence.Evidence, error) {
	in := evidence.Input{EntityType: "performer", Slug: slug, Hidden: map[string]bool{}}
	var id int64
	var hidden bool
	var canonical []byte
	err := s.QueryRow(ctx, `SELECT p.id,p.updated_at,`+BirthHiddenSQL("p")+`,json_build_object(
			'canonical_name',p.canonical_name,'canonical_name_ja',p.canonical_name_ja,
			'birth_date',(`+PublicBirthDate("p")+`)::text,'birth_place',p.birth_place,'debut_date',p.debut_date::text,
			'blood_type',p.blood_type,'height_cm',p.height_cm,'bust_cm',p.bust_cm,'waist_cm',p.waist_cm,'hip_cm',p.hip_cm,
			'cup_size',p.cup_size,'shoe_size',p.shoe_size,'biography',p.biography)
		FROM performers p WHERE p.slug=$1 AND p.status='active' AND p.merged_into_id IS NULL`, slug).Scan(&id, &in.UpdatedAt, &hidden, &canonical)
	if err != nil {
		return evidence.Evidence{}, err
	}
	if hidden {
		in.Hidden["birth_date"] = true
	}
	if err = s.loadEvidence(ctx, &in, id, canonical); err != nil {
		return evidence.Evidence{}, err
	}
	return evidence.Build(in), nil
}

// loadEvidence fills overrides, source records (of the entity and of
// entities merged into it), current scalar field values and field policies.
func (s *Store) loadEvidence(ctx context.Context, in *evidence.Input, id int64, canonical []byte) error {
	if err := json.Unmarshal(canonical, &in.Canonical); err != nil {
		return err
	}
	for f := range in.Hidden {
		delete(in.Canonical, f)
	}
	table, overrideTable, fk, col := "releases", "release_overrides", "release_id", "release_id"
	if in.EntityType == "performer" {
		table, overrideTable, fk, col = "performers", "performer_overrides", "performer_id", "performer_id"
	}
	names, err := collect[string](s.Query(ctx, `SELECT field_name FROM `+overrideTable+` WHERE `+fk+`=$1`, id))(func(r pgx.Rows) (string, error) {
		var f string
		return f, r.Scan(&f)
	})
	if err != nil {
		return err
	}
	in.Overridden = map[string]bool{}
	for _, f := range names {
		in.Overridden[f] = true
	}
	entityFilter := `sr.entity_type=$2 AND sr.` + col + ` IN (SELECT e.id FROM ` + table + ` e WHERE e.id=$1 OR e.merged_into_id=$1)`
	in.Records, err = collect[evidence.Record](s.Query(ctx, `SELECT sr.id,sr.source_id,s.name,s.source_kind,s.priority,sr.source_url,sr.first_seen_at,sr.last_seen_at
		FROM source_records sr JOIN sources s ON s.id=sr.source_id WHERE `+entityFilter+` ORDER BY sr.id`, id, in.EntityType))(func(r pgx.Rows) (evidence.Record, error) {
		var x evidence.Record
		var prio int16
		err := r.Scan(&x.ID, &x.SourceID, &x.SourceName, &x.SourceKind, &prio, &x.URL, &x.FirstSeenAt, &x.LastSeenAt)
		x.Priority = int(prio)
		return x, err
	})
	if err != nil {
		return err
	}
	parsers := evidence.ParserNames(evidence.Fields(in.EntityType))
	in.Values, err = collect[evidence.Value](s.Query(ctx, `SELECT sf.source_record_id,sf.field_name,sf.normalized_value,sf.captured_at
		FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id
		WHERE sf.is_current AND `+entityFilter+` AND sf.field_name=ANY($3)
		AND jsonb_typeof(sf.normalized_value) IN ('string','number','boolean')`, id, in.EntityType, parsers))(func(r pgx.Rows) (evidence.Value, error) {
		var v evidence.Value
		var raw []byte
		var captured time.Time
		err := r.Scan(&v.RecordID, &v.Field, &raw, &captured)
		v.JSON, v.CapturedAt = json.RawMessage(raw), captured
		return v, err
	})
	if err != nil {
		return err
	}
	// Hidden fields: drop their source values here too (Build also skips them).
	if len(in.Hidden) > 0 {
		hiddenParsers := map[string]bool{}
		for _, f := range evidence.Fields(in.EntityType) {
			if in.Hidden[f.Name] {
				for _, p := range f.Parsers {
					hiddenParsers[p] = true
				}
			}
		}
		kept := in.Values[:0]
		for _, v := range in.Values {
			if !hiddenParsers[v.Field] {
				kept = append(kept, v)
			}
		}
		in.Values = kept
	}
	type policy struct {
		source   int64
		field    string
		priority int16
	}
	ps, err := collect[policy](s.Query(ctx, `SELECT source_id,field_name,priority FROM source_field_policies WHERE entity_type=$1`, in.EntityType))(func(r pgx.Rows) (policy, error) {
		var p policy
		return p, r.Scan(&p.source, &p.field, &p.priority)
	})
	if err != nil {
		return err
	}
	in.Policies = map[string]int{}
	for _, p := range ps {
		in.Policies[strconv.FormatInt(p.source, 10)+"/"+p.field] = int(p.priority)
	}
	return nil
}
