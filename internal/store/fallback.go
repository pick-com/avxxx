package store

import (
	"avxxx/internal/catalog"
	"context"
	"errors"
	"slices"

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
// attaches to: the release its earlier save attached to, else the one
// release fallbackCodeReleaseIDs finds for slug (the code slug) and code;
// "" when there is none, or several. Matching by the code key lets
// "HEYZO-123" (third-party APIs drop zero padding) reach "HEYZO-0123", and
// two releases sharing a key ("ABC-001" and "ABC-1") stay unresolved for an
// admin rather than one of them getting the record.
func fallbackReleaseSlug(ctx context.Context, tx pgx.Tx, sourceID int64, key, slug, code string) (string, error) {
	var id int64
	err := tx.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_id=$1 AND entity_type='release' AND source_record_key=$2 AND release_id IS NOT NULL`,
		sourceID, key).Scan(&id)
	switch {
	case err == nil:
		if id, err = survivingID(ctx, tx, "releases", id); err != nil {
			return "", err
		}
	case errors.Is(err, pgx.ErrNoRows):
		ids, err := fallbackCodeReleaseIDs(ctx, tx, slug, code)
		if err != nil || len(ids) != 1 {
			return "", err
		}
		id = ids[0]
	default:
		return "", err
	}
	var out string
	err = tx.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, id).Scan(&out)
	return out, err
}

// fallbackCodeReleaseIDs are the surviving releases (merges followed) a
// fallback record could attach to, in id order: the release under slug and,
// when code is set, every release with a DVD code of the same
// catalog.CodeMatchKey (SQL code_match_key, migration 033).
func fallbackCodeReleaseIDs(ctx context.Context, tx pgx.Tx, slug, code string) ([]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM releases WHERE slug=$1
		UNION SELECT release_id FROM release_identifiers
		WHERE $2<>'' AND identifier_type='dvd_code' AND code_match_key(normalized_value)=code_match_key($2)`, slug, code)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, err
	}
	out := []int64{}
	for _, id := range ids {
		sid, err := survivingID(ctx, tx, "releases", id)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(out, sid) {
			out = append(out, sid)
		}
	}
	slices.Sort(out)
	return out, nil
}

// CrossCheckCodeReleases implements crosscheck.Store: the surviving
// releases SaveCatalogRecord would weigh for a fallback record with DVD
// code code (fallbackCodeReleaseIDs under its code slug), in id order. The
// record attaches only when this is exactly one release.
func (s *Store) CrossCheckCodeReleases(ctx context.Context, code string) ([]int64, error) {
	tx, err := s.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	return fallbackCodeReleaseIDs(ctx, tx, catalog.ReleaseSlug(code, "", "", ""), code)
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
