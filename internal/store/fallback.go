package store

import (
	"avxxx/internal/catalog"
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Fallback-only sources (FINAL_DATA_DESIGN 2.4 tier third_party_api, e.g.
// ThePornDB): batch lookup and missing-field fallback, never the sole source
// of truth. SaveCatalogRecord therefore lets their records attach to
// existing performers and releases only; a record that matches nothing is
// kept as unresolved evidence (source_records.resolution_status
// 'unresolved', no entity), and a release they reach gets credits from them
// only when nobody else credits it. Their field values compete in the field
// policy at tier 50 like any source, but on their own never open a field
// conflict (fieldpolicy.go).

// FallbackSourceKind is the source_kind whose records never create entities.
const FallbackSourceKind = "third_party_api"

func fallbackOnlySource(ctx context.Context, tx pgx.Tx, sourceID int64) (bool, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT source_kind FROM sources WHERE id=$1`, sourceID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // the source_records insert reports the bad source id
	}
	return kind == FallbackSourceKind, err
}

// fallbackReleaseSlug is the slug of the existing release a fallback record
// attaches to: the release its earlier save attached to, else the release
// under slug (the code slug); "" when neither exists.
func fallbackReleaseSlug(ctx context.Context, tx pgx.Tx, sourceID int64, key, slug string) (string, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_id=$1 AND entity_type='release' AND source_record_key=$2 AND release_id IS NOT NULL`,
		sourceID, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, slug).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if id, err = survivingID(ctx, tx, "releases", id); err != nil {
		return "", err
	}
	var out string
	err = tx.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, id).Scan(&out)
	return out, err
}

// fallbackPerformerSlug is the slug of the existing performer a fallback
// performer record attaches to: the performer its earlier save attached to,
// else existingPerformer; "" when none exists.
func fallbackPerformerSlug(ctx context.Context, tx pgx.Tx, sourceID int64, rec catalog.Record, ref performerRef) (string, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT performer_id FROM source_records WHERE source_id=$1 AND entity_type='performer' AND source_record_key=$2 AND performer_id IS NOT NULL`,
		sourceID, rec.SourceKey).Scan(&id)
	switch {
	case err == nil:
		id, err = survivingID(ctx, tx, "performers", id)
	case errors.Is(err, pgx.ErrNoRows):
		id, err = existingPerformer(ctx, tx, ref)
	}
	if err != nil || id == 0 {
		return "", err
	}
	_, slug, err := finishResolve(ctx, tx, id, nil)
	return slug, err
}

// saveUnresolvedRecord keeps a fallback record that matches no entity as
// evidence only, and commits. A row attached by an earlier save keeps its
// entity.
func saveUnresolvedRecord(ctx context.Context, tx pgx.Tx, sourceID int64, parserVersion string, rec catalog.Record) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,raw_data,parser_version,resolution_status)
		VALUES($1,$2,$3,$4,$5,$6,'unresolved') ON CONFLICT(source_id,entity_type,source_record_key)
		DO UPDATE SET source_url=EXCLUDED.source_url,raw_data=EXCLUDED.raw_data,parser_version=EXCLUDED.parser_version,last_seen_at=now() RETURNING id`,
		sourceID, string(rec.EntityType), rec.SourceKey, rec.URL, rec.Fields, parserVersion).Scan(&id)
	if err != nil {
		return 0, err
	}
	if _, err = writeFieldValues(ctx, tx, id, rec.Fields); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
