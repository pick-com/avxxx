package store

// Catalog image storage (internal/images, migrations/031_media_storage.sql,
// docs/DATA_RETENTION_AND_IMAGES.md): the media_fetch and media_purge job
// stores, public image lookups and the admin media routes.

import (
	"avxxx/internal/images"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Job kinds of the image jobs (cmd/scheduler enqueues, cmd/audit runs).
const (
	JobKindMediaFetch = images.JobKindFetch
	JobKindMediaPurge = images.JobKindPurge
)

// imageEntityPublicSQL is true while the entity of media_assets row ma is
// public: a published or upcoming release / an active performer, unmerged.
// Images of any other entity are hidden and purged after the window.
const imageEntityPublicSQL = `(CASE ma.entity_type
	WHEN 'release' THEN EXISTS (SELECT 1 FROM releases r WHERE r.id=ma.entity_id AND r.merged_into_id IS NULL AND r.status IN ('published','upcoming'))
	WHEN 'performer' THEN EXISTS (SELECT 1 FROM performers p WHERE p.id=ma.entity_id AND p.merged_into_id IS NULL AND p.status='active')
	ELSE false END)`

const imageEvidenceSQL = `jsonb_strip_nulls(jsonb_build_object('cover_url', sr.raw_data->'cover_url', 'cover', sr.raw_data->'cover', 'image_urls', sr.raw_data->'image_urls'))`

// ImageSources implements images.FetchStore.
func (s *Store) ImageSources(ctx context.Context) ([]images.SourcePolicy, error) {
	return collect[images.SourcePolicy](s.Query(ctx, `SELECT id,name,adapter,license_status,terms_reviewed_at,images_allowed,images_reviewed_at,
		COALESCE(images_reviewed_by,''),COALESCE(images_note,'') FROM sources ORDER BY id`))(func(r pgx.Rows) (images.SourcePolicy, error) {
		var p images.SourcePolicy
		err := r.Scan(&p.ID, &p.Name, &p.Adapter, &p.LicenseStatus, &p.TermsReviewedAt, &p.ImagesAllowed, &p.ImagesReviewedAt, &p.ImagesReviewedBy, &p.ImagesNote)
		return p, err
	})
}

// ImageCandidates implements images.FetchStore. Releases and performers are
// interleaved (newest ids first in each), so neither starves the other.
func (s *Store) ImageCandidates(ctx context.Context, sourceIDs []int64, now time.Time, limit int) ([]images.Candidate, error) {
	if len(sourceIDs) == 0 || limit <= 0 {
		return nil, nil
	}
	const branch = `SELECT '%[1]s'::text et, e.id, '%[2]s'::text at, ev.source_id, ev.id rid, ev.evidence, COALESCE(a.attempts,0) attempts,
			row_number() OVER (ORDER BY e.id DESC) rn
		FROM %[3]s e
		CROSS JOIN LATERAL (SELECT sr.source_id, sr.id, ` + imageEvidenceSQL + ` evidence
			FROM source_records sr JOIN sources s ON s.id=sr.source_id
			WHERE sr.%[1]s_id=e.id AND sr.source_id=ANY($1) AND sr.raw_data ?| array['cover_url','cover','image_urls']
			ORDER BY s.priority, sr.last_seen_at DESC, sr.id DESC LIMIT 1) ev
		LEFT JOIN media_fetch_attempts a ON a.entity_type='%[1]s' AND a.entity_id=e.id AND a.asset_type='%[2]s'
		WHERE e.merged_into_id IS NULL AND %[4]s
		  AND NOT EXISTS (SELECT 1 FROM media_assets ma WHERE ma.entity_type='%[1]s' AND ma.entity_id=e.id AND ma.asset_type='%[2]s')
		  AND NOT EXISTS (SELECT 1 FROM media_blocks b WHERE b.entity_type='%[1]s' AND b.entity_id=e.id AND b.asset_type='%[2]s')
		  AND (a.entity_id IS NULL OR a.next_try_at<=$2) %[5]s
		ORDER BY e.id DESC LIMIT $3`
	rel := fmt.Sprintf(branch, images.EntityRelease, images.AssetCover, "releases", "e.status IN ('published','upcoming')", "")
	perf := fmt.Sprintf(branch, images.EntityPerformer, images.AssetAvatar, "performers", "e.status='active'",
		"AND NOT EXISTS (SELECT 1 FROM performer_requests pr WHERE pr.performer_id=e.id AND pr.kind='remove_photo' AND pr.status='approved')")
	rows, err := s.Query(ctx, `SELECT et,id,at,source_id,rid,evidence,attempts FROM ((`+rel+`) UNION ALL (`+perf+`)) c ORDER BY rn, et DESC LIMIT $3`, sourceIDs, now, limit)
	return collect[images.Candidate](rows, err)(func(r pgx.Rows) (images.Candidate, error) {
		var c images.Candidate
		var raw []byte
		if err := r.Scan(&c.EntityType, &c.EntityID, &c.AssetType, &c.SourceID, &c.SourceRecordID, &raw, &c.Attempts); err != nil {
			return c, err
		}
		return c, json.Unmarshal(raw, &c.Evidence)
	})
}

// ObjectKeyStored implements images.FetchStore.
func (s *Store) ObjectKeyStored(ctx context.Context, key string) (bool, error) {
	var ok bool
	err := s.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_assets WHERE object_key=$1)`, key).Scan(&ok)
	return ok, err
}

// SaveImage implements images.FetchStore: the original row (content_sha256
// = hash of the downloaded bytes, the dedupe key), one row per derived size,
// and a 'stored' attempt, in one transaction. Existing rows are kept.
func (s *Store) SaveImage(ctx context.Context, img images.StoredImage) error {
	c, p := img.Candidate, img.Processed
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	const ins = `INSERT INTO media_assets(entity_type,entity_id,asset_type,storage_provider,object_key,source_url,content_sha256,mime_type,
		width,height,byte_size,is_original,variant,derived_from,source_id,source_record_id)
		VALUES($1,$2,$3,'s3',$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) ON CONFLICT DO NOTHING RETURNING id`
	var origID int64
	err = tx.QueryRow(ctx, ins, c.EntityType, c.EntityID, c.AssetType, img.Keys[images.VariantOriginal], img.SourceURL, p.SourceSHA256,
		p.Original.MIME, p.Original.Width, p.Original.Height, len(p.Original.Body), true, images.VariantOriginal, nil, c.SourceID, c.SourceRecordID).Scan(&origID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT id FROM media_assets WHERE entity_type=$1 AND entity_id=$2 AND asset_type=$3 AND content_sha256=$4`,
			c.EntityType, c.EntityID, c.AssetType, p.SourceSHA256).Scan(&origID)
	}
	if err != nil {
		return err
	}
	for _, d := range p.Derived {
		var id int64
		err = tx.QueryRow(ctx, ins, c.EntityType, c.EntityID, c.AssetType, img.Keys[d.Variant], img.SourceURL, d.SHA256,
			d.MIME, d.Width, d.Height, len(d.Body), false, d.Variant, origID, c.SourceID, c.SourceRecordID).Scan(&id)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	if err = upsertAttempt(ctx, tx, c, img.SourceURL, "stored", "", time.Now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func upsertAttempt(ctx context.Context, q execer, c images.Candidate, url, status, errText string, next time.Time) error {
	_, err := q.Exec(ctx, `INSERT INTO media_fetch_attempts(entity_type,entity_id,asset_type,source_id,source_url,status,attempts,last_error,tried_at,next_try_at)
		VALUES($1,$2,$3,$4,$5,$6,CASE WHEN $6='failed' THEN 1 ELSE 0 END,NULLIF($7,''),now(),$8)
		ON CONFLICT (entity_type,entity_id,asset_type) DO UPDATE SET source_id=EXCLUDED.source_id,source_url=EXCLUDED.source_url,status=EXCLUDED.status,
			attempts=CASE WHEN EXCLUDED.status='failed' THEN media_fetch_attempts.attempts+1 ELSE 0 END,
			last_error=EXCLUDED.last_error,tried_at=now(),next_try_at=EXCLUDED.next_try_at`,
		c.EntityType, c.EntityID, c.AssetType, c.SourceID, url, status, errText, next)
	return err
}

// RecordFetchAttempt implements images.FetchStore.
func (s *Store) RecordFetchAttempt(ctx context.Context, c images.Candidate, sourceURL, status, errText string, next time.Time) error {
	return upsertAttempt(ctx, s, c, sourceURL, status, errText, next)
}

// ---- purge

// MarkRetention implements images.PurgeStore.
func (s *Store) MarkRetention(ctx context.Context, now time.Time, after time.Duration) (marked, cleared int64, err error) {
	tag, err := s.Exec(ctx, `WITH due AS (
			SELECT ma.id, COALESCE(ma.suppressed_at, o.suppressed_at) sup
			FROM media_assets ma LEFT JOIN media_assets o ON o.id=ma.derived_from
			WHERE ma.retained_until IS NULL
			  AND (ma.suppressed_at IS NOT NULL OR o.suppressed_at IS NOT NULL OR NOT `+imageEntityPublicSQL+`))
		UPDATE media_assets m SET retained_until=COALESCE(due.sup,$1::timestamptz)+$2::interval FROM due WHERE m.id=due.id`, now, after)
	if err != nil {
		return 0, 0, err
	}
	marked = tag.RowsAffected()
	tag, err = s.Exec(ctx, `UPDATE media_assets ma SET retained_until=NULL
		WHERE ma.retained_until IS NOT NULL AND ma.suppressed_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM media_assets o WHERE o.id=ma.derived_from AND o.suppressed_at IS NOT NULL)
		  AND `+imageEntityPublicSQL)
	if err != nil {
		return marked, 0, err
	}
	return marked, tag.RowsAffected(), nil
}

const assetColumns = `ma.id,ma.entity_type,ma.entity_id,ma.asset_type,ma.variant,ma.derived_from,ma.object_key,ma.mime_type,ma.width,ma.height,
	ma.byte_size,ma.source_id,COALESCE(ma.source_url,''),ma.content_sha256,ma.suppressed_at,COALESCE(ma.suppressed_reason,''),ma.retained_until,ma.created_at`

func scanAsset(r pgx.Rows) (images.Asset, error) {
	var a images.Asset
	err := r.Scan(&a.ID, &a.EntityType, &a.EntityID, &a.AssetType, &a.Variant, &a.DerivedFrom, &a.ObjectKey, &a.MIMEType, &a.Width, &a.Height,
		&a.ByteSize, &a.SourceID, &a.SourceURL, &a.SHA256, &a.SuppressedAt, &a.SuppressedReason, &a.RetainedUntil, &a.CreatedAt)
	return a, err
}

// ExpiredAssets implements images.PurgeStore.
func (s *Store) ExpiredAssets(ctx context.Context, now time.Time, limit int) ([]images.Asset, error) {
	return collect[images.Asset](s.Query(ctx, `SELECT `+assetColumns+` FROM media_assets ma
		WHERE ma.retained_until IS NOT NULL AND ma.retained_until<=$1
		ORDER BY (ma.derived_from IS NULL), ma.retained_until, ma.id LIMIT $2`, now, limit))(scanAsset)
}

// DeleteAssets implements images.PurgeStore.
func (s *Store) DeleteAssets(ctx context.Context, ids []int64) error {
	_, err := s.Exec(ctx, `DELETE FROM media_assets WHERE id=ANY($1)`, ids)
	return err
}

// KnownObjectKeys implements images.PurgeStore.
func (s *Store) KnownObjectKeys(ctx context.Context, keys []string) (map[string]bool, error) {
	rows, err := s.Query(ctx, `SELECT DISTINCT object_key FROM media_assets WHERE object_key=ANY($1)`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// StorageState implements images.PurgeStore ("" when unset).
func (s *Store) StorageState(ctx context.Context, name string) (string, error) {
	var v string
	err := s.QueryRow(ctx, `SELECT value FROM media_storage_state WHERE name=$1`, name).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetStorageState implements images.PurgeStore.
func (s *Store) SetStorageState(ctx context.Context, name, value string) error {
	_, err := s.Exec(ctx, `INSERT INTO media_storage_state(name,value) VALUES($1,$2)
		ON CONFLICT (name) DO UPDATE SET value=EXCLUDED.value,updated_at=now()`, name, value)
	return err
}

// ---- public reads

// PublicImages returns, per entity id, the visible variants of the newest
// unsuppressed original of assetType. Callers only pass public entities.
func (s *Store) PublicImages(ctx context.Context, entityType, assetType string, ids []int64) (map[int64][]images.PublicAsset, error) {
	out := map[int64][]images.PublicAsset{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Query(ctx, `WITH o AS (
			SELECT DISTINCT ON (ma.entity_id) ma.id, ma.entity_id FROM media_assets ma
			WHERE ma.entity_type=$1 AND ma.asset_type=$2 AND ma.entity_id=ANY($3) AND ma.variant='original' AND ma.suppressed_at IS NULL
			ORDER BY ma.entity_id, ma.id DESC)
		SELECT o.entity_id, v.variant, v.object_key, COALESCE(v.width,0), COALESCE(v.height,0)
		FROM o JOIN media_assets v ON (v.id=o.id OR v.derived_from=o.id) AND v.suppressed_at IS NULL`, entityType, assetType, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var a images.PublicAsset
		if err := rows.Scan(&a.EntityID, &a.Variant, &a.ObjectKey, &a.Width, &a.Height); err != nil {
			return nil, err
		}
		out[a.EntityID] = append(out[a.EntityID], a)
	}
	return out, rows.Err()
}

// PublicImageObject returns the MIME type of a servable object key: owned by
// an unsuppressed row (whose original is unsuppressed) of a public entity.
// Anything else is pgx.ErrNoRows.
func (s *Store) PublicImageObject(ctx context.Context, key string) (string, error) {
	var mt string
	err := s.QueryRow(ctx, `SELECT ma.mime_type FROM media_assets ma LEFT JOIN media_assets o ON o.id=ma.derived_from
		WHERE ma.object_key=$1 AND ma.suppressed_at IS NULL AND (o.id IS NULL OR o.suppressed_at IS NULL) AND `+imageEntityPublicSQL+`
		LIMIT 1`, key).Scan(&mt)
	return mt, err
}

// ---- admin

// MediaBlock is one media_blocks row.
type MediaBlock struct {
	AssetType string    `json:"asset_type"`
	Reason    string    `json:"reason,omitempty"`
	Actor     string    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
}

// EntityMedia is the admin view of one entity's images.
type EntityMedia struct {
	EntityType string             `json:"entity_type"`
	EntityID   int64              `json:"entity_id"`
	Assets     []images.Asset     `json:"assets"`
	Blocks     []MediaBlock       `json:"blocks"`
	Attempts   []MediaFetchStatus `json:"attempts"`
}

// MediaFetchStatus is one media_fetch_attempts row.
type MediaFetchStatus struct {
	AssetType string    `json:"asset_type"`
	SourceID  *int64    `json:"source_id,omitempty"`
	SourceURL string    `json:"source_url"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	LastError string    `json:"last_error,omitempty"`
	TriedAt   time.Time `json:"tried_at"`
	NextTryAt time.Time `json:"next_try_at"`
}

func validMediaEntity(entityType string, id int64) error {
	if entityType != images.EntityRelease && entityType != images.EntityPerformer {
		return invalid("entity_type must be release or performer")
	}
	if id <= 0 {
		return invalid("entity_id must be positive")
	}
	return nil
}

// EntityMedia lists every image row (suppressed and pending purge
// included), blocks and fetch attempts of one entity.
func (s *Store) EntityMedia(ctx context.Context, entityType string, id int64) (EntityMedia, error) {
	out := EntityMedia{EntityType: entityType, EntityID: id}
	if err := validMediaEntity(entityType, id); err != nil {
		return out, err
	}
	var err error
	if out.Assets, err = collect[images.Asset](s.Query(ctx, `SELECT `+assetColumns+` FROM media_assets ma
		WHERE ma.entity_type=$1 AND ma.entity_id=$2 ORDER BY ma.asset_type, COALESCE(ma.derived_from, ma.id) DESC, ma.derived_from NULLS FIRST, ma.id`, entityType, id))(scanAsset); err != nil {
		return out, err
	}
	if out.Blocks, err = collect[MediaBlock](s.Query(ctx, `SELECT asset_type,COALESCE(reason,''),actor,created_at FROM media_blocks
		WHERE entity_type=$1 AND entity_id=$2 ORDER BY asset_type`, entityType, id))(func(r pgx.Rows) (MediaBlock, error) {
		var b MediaBlock
		return b, r.Scan(&b.AssetType, &b.Reason, &b.Actor, &b.CreatedAt)
	}); err != nil {
		return out, err
	}
	out.Attempts, err = collect[MediaFetchStatus](s.Query(ctx, `SELECT asset_type,source_id,source_url,status,attempts,COALESCE(last_error,''),tried_at,next_try_at
		FROM media_fetch_attempts WHERE entity_type=$1 AND entity_id=$2 ORDER BY asset_type`, entityType, id))(func(r pgx.Rows) (MediaFetchStatus, error) {
		var a MediaFetchStatus
		return a, r.Scan(&a.AssetType, &a.SourceID, &a.SourceURL, &a.Status, &a.Attempts, &a.LastError, &a.TriedAt, &a.NextTryAt)
	})
	return out, err
}

// mediaOriginal locks the original of asset id (id itself or the row it
// was derived from) and returns it.
func mediaOriginal(ctx context.Context, tx pgx.Tx, id int64) (images.Asset, error) {
	rows, err := tx.Query(ctx, `SELECT `+assetColumns+` FROM media_assets ma
		WHERE ma.id=(SELECT COALESCE(derived_from, id) FROM media_assets WHERE id=$1) FOR UPDATE`, id)
	list, err := collect[images.Asset](rows, err)(scanAsset)
	if err != nil {
		return images.Asset{}, err
	}
	if len(list) == 0 {
		return images.Asset{}, pgx.ErrNoRows
	}
	return list[0], nil
}

func mediaEventState(a images.Asset) map[string]any {
	return map[string]any{"asset_id": a.ID, "asset_type": a.AssetType, "object_key": a.ObjectKey, "content_sha256": a.SHA256,
		"suppressed_at": a.SuppressedAt, "suppressed_reason": a.SuppressedReason}
}

// SuppressMediaAsset hides an image (the original of asset id and every
// derived size) at once, blocks new downloads for the entity's asset type
// and writes review_events 'media_suppress'. The purge job deletes the
// objects after the retention window. Unknown asset: pgx.ErrNoRows.
func (s *Store) SuppressMediaAsset(ctx context.Context, id int64, reason, actor string) (EntityMedia, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return EntityMedia{}, err
	}
	defer tx.Rollback(ctx)
	orig, err := suppressMediaTx(ctx, tx, id, reason, actor)
	if err != nil {
		return EntityMedia{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EntityMedia{}, err
	}
	return s.EntityMedia(ctx, orig.EntityType, orig.EntityID)
}

// suppressMediaTx is SuppressMediaAsset inside the caller's transaction
// (copyright takedowns suppress through it); it returns the original.
func suppressMediaTx(ctx context.Context, tx pgx.Tx, id int64, reason, actor string) (images.Asset, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return images.Asset{}, invalid("reason is required")
	}
	if len(reason) > MaxReasonLen {
		return images.Asset{}, invalid("reason too long")
	}
	orig, err := mediaOriginal(ctx, tx, id)
	if err != nil {
		return orig, err
	}
	before := mediaEventState(orig)
	if _, err = tx.Exec(ctx, `UPDATE media_assets SET suppressed_at=COALESCE(suppressed_at,now()),suppressed_reason=COALESCE(suppressed_reason,$2)
		WHERE id=$1 OR derived_from=$1`, orig.ID, reason); err != nil {
		return orig, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO media_blocks(entity_type,entity_id,asset_type,reason,actor) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (entity_type,entity_id,asset_type) DO NOTHING`, orig.EntityType, orig.EntityID, orig.AssetType, reason, actor); err != nil {
		return orig, err
	}
	if orig, err = mediaOriginal(ctx, tx, orig.ID); err != nil {
		return orig, err
	}
	after := mediaEventState(orig)
	after["blocked"] = true
	return orig, writeReviewEvent(ctx, tx, orig.EntityType, orig.EntityID, "media_suppress", before, after, reason, actor)
}

// RestoreMediaAsset undoes SuppressMediaAsset while the rows still exist
// (before the purge), lifts the block and writes review_events
// 'media_restore'. A performer with an approved remove_photo request
// cannot have photos restored here.
func (s *Store) RestoreMediaAsset(ctx context.Context, id int64, reason, actor string) (EntityMedia, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return EntityMedia{}, err
	}
	defer tx.Rollback(ctx)
	orig, err := restoreMediaTx(ctx, tx, id, reason, actor)
	if err != nil {
		return EntityMedia{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return EntityMedia{}, err
	}
	return s.EntityMedia(ctx, orig.EntityType, orig.EntityID)
}

// restoreMediaTx is RestoreMediaAsset inside the caller's transaction
// (a counter-notice restores through it); it returns the original.
func restoreMediaTx(ctx context.Context, tx pgx.Tx, id int64, reason, actor string) (images.Asset, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return images.Asset{}, invalid("reason is required")
	}
	if len(reason) > MaxReasonLen {
		return images.Asset{}, invalid("reason too long")
	}
	orig, err := mediaOriginal(ctx, tx, id)
	if err != nil {
		return orig, err
	}
	if orig.EntityType == images.EntityPerformer {
		var approved bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM performer_requests WHERE performer_id=$1 AND kind='remove_photo' AND status='approved')`,
			orig.EntityID).Scan(&approved); err != nil {
			return orig, err
		}
		if approved {
			return orig, invalid("the performer has an approved remove_photo request")
		}
	}
	before := mediaEventState(orig)
	if _, err = tx.Exec(ctx, `UPDATE media_assets SET suppressed_at=NULL,suppressed_reason=NULL,retained_until=NULL WHERE id=$1 OR derived_from=$1`, orig.ID); err != nil {
		return orig, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM media_blocks WHERE entity_type=$1 AND entity_id=$2 AND asset_type=$3`, orig.EntityType, orig.EntityID, orig.AssetType); err != nil {
		return orig, err
	}
	if orig, err = mediaOriginal(ctx, tx, orig.ID); err != nil {
		return orig, err
	}
	return orig, writeReviewEvent(ctx, tx, orig.EntityType, orig.EntityID, "media_restore", before, mediaEventState(orig), reason, actor)
}

// SourceImagePolicy is a source's image policy and the resulting decision.
type SourceImagePolicy struct {
	images.SourcePolicy
	Decision images.Decision `json:"decision"`
}

// SourceImagePolicies lists every source's image policy.
func (s *Store) SourceImagePolicies(ctx context.Context) ([]SourceImagePolicy, error) {
	ps, err := s.ImageSources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SourceImagePolicy, len(ps))
	for i, p := range ps {
		out[i] = SourceImagePolicy{SourcePolicy: p, Decision: images.SourceAllowed(p)}
	}
	return out, nil
}

// SetSourceImages records an admin's image review of a source. Enabling
// requires an image-capable license_status (images.LicensePermitsImages)
// and a note; every change writes review_events 'images_review'.
func (s *Store) SetSourceImages(ctx context.Context, id int64, allowed bool, note, actor string) (SourceImagePolicy, error) {
	note = strings.TrimSpace(note)
	if len(note) > MaxReasonLen {
		return SourceImagePolicy{}, invalid("note too long")
	}
	if allowed && note == "" {
		return SourceImagePolicy{}, invalid("note is required: cite the terms that allow storing images")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return SourceImagePolicy{}, err
	}
	defer tx.Rollback(ctx)
	scan := func(row pgx.Row) (images.SourcePolicy, error) {
		var p images.SourcePolicy
		err := row.Scan(&p.ID, &p.Name, &p.Adapter, &p.LicenseStatus, &p.TermsReviewedAt, &p.ImagesAllowed, &p.ImagesReviewedAt, &p.ImagesReviewedBy, &p.ImagesNote)
		return p, err
	}
	const cols = `id,name,adapter,license_status,terms_reviewed_at,images_allowed,images_reviewed_at,COALESCE(images_reviewed_by,''),COALESCE(images_note,'')`
	before, err := scan(tx.QueryRow(ctx, `SELECT `+cols+` FROM sources WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return SourceImagePolicy{}, err
	}
	if allowed && !images.LicensePermitsImages(before.LicenseStatus) {
		return SourceImagePolicy{}, invalid("license_status %q does not cover images; record an official_api or licensed review first", before.LicenseStatus)
	}
	after, err := scan(tx.QueryRow(ctx, `UPDATE sources SET images_allowed=$2,images_reviewed_at=now(),images_reviewed_by=$3,images_note=NULLIF($4,''),version=version+1
		WHERE id=$1 RETURNING `+cols, id, allowed, actor, note))
	if err != nil {
		return SourceImagePolicy{}, err
	}
	state := func(p images.SourcePolicy) map[string]any {
		return map[string]any{"images_allowed": p.ImagesAllowed, "images_reviewed_at": p.ImagesReviewedAt, "images_reviewed_by": p.ImagesReviewedBy,
			"license_status": p.LicenseStatus, "decision": images.SourceAllowed(p)}
	}
	if err = sourceEvent(ctx, tx, id, "images_review", state(before), state(after), note, actor); err != nil {
		return SourceImagePolicy{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return SourceImagePolicy{}, err
	}
	return SourceImagePolicy{SourcePolicy: after, Decision: images.SourceAllowed(after)}, nil
}
