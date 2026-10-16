package store

import (
	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"avxxx/internal/i18n"
	"avxxx/internal/model"
	"avxxx/migrations"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strconv"
	"strings"
	"time"
)

type Store struct {
	*pgxpool.Pool
	workerID string
}

type CrawlCheckpoint struct {
	SourceID       int64      `json:"source_id"`
	TaskType       string     `json:"task_type"`
	PartitionKey   string     `json:"partition_key"`
	Cursor         string     `json:"cursor"`
	ParserVersion  string     `json:"parser_version"`
	LastErrorCode  string     `json:"last_error_code"`
	Watermark      *time.Time `json:"watermark"`
	CompletedPages int64      `json:"completed_pages"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
}

func (s *Store) WorkerID() string { return s.workerID }

func (s *Store) Checkpoint(ctx context.Context, sourceID int64, taskType, partition, parserVersion string) (CrawlCheckpoint, error) {
	var c CrawlCheckpoint
	c.SourceID = sourceID
	c.TaskType = taskType
	c.PartitionKey = partition
	c.ParserVersion = parserVersion
	err := s.QueryRow(ctx, `SELECT COALESCE(cursor,''),watermark,completed_pages,last_success_at,COALESCE(last_error_code,''),parser_version FROM crawl_checkpoints WHERE source_id=$1 AND task_type=$2 AND partition_key=$3`, sourceID, taskType, partition).Scan(&c.Cursor, &c.Watermark, &c.CompletedPages, &c.LastSuccessAt, &c.LastErrorCode, &c.ParserVersion)
	return c, err
}

func (s *Store) AdvanceCheckpoint(ctx context.Context, c CrawlCheckpoint, cursor string, watermark *time.Time, pages int64) error {
	if pages <= 0 {
		pages = 1
	}
	_, err := s.Exec(ctx, `INSERT INTO crawl_checkpoints(source_id,task_type,partition_key,cursor,watermark,completed_pages,last_success_at,last_error_code,parser_version) VALUES($1,$2,$3,$4,$5,$6,now(),NULL,$7) ON CONFLICT(source_id,task_type,partition_key) DO UPDATE SET cursor=EXCLUDED.cursor,watermark=EXCLUDED.watermark,completed_pages=crawl_checkpoints.completed_pages+EXCLUDED.completed_pages,last_success_at=now(),last_error_code=NULL,parser_version=EXCLUDED.parser_version`, c.SourceID, c.TaskType, c.PartitionKey, cursor, watermark, pages, c.ParserVersion)
	return err
}

func (s *Store) FailCheckpoint(ctx context.Context, c CrawlCheckpoint, code string) error {
	_, err := s.Exec(ctx, `INSERT INTO crawl_checkpoints(source_id,task_type,partition_key,last_error_code,parser_version) VALUES($1,$2,$3,$4,$5) ON CONFLICT(source_id,task_type,partition_key) DO UPDATE SET last_error_code=EXCLUDED.last_error_code,parser_version=EXCLUDED.parser_version`, c.SourceID, c.TaskType, c.PartitionKey, code, c.ParserVersion)
	return err
}

func (s *Store) IdentityCandidates(ctx context.Context, entityType string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	r, err := s.Query(ctx, `SELECT id,entity_type,left_id,right_id,score,evidence,status FROM identity_candidates c WHERE ($1='' OR entity_type=$1) AND status='pending'
		AND NOT EXISTS (SELECT 1 FROM performers p WHERE c.entity_type='performer' AND p.id IN (c.left_id,c.right_id) AND p.merged_into_id IS NOT NULL)
		AND NOT EXISTS (SELECT 1 FROM releases r WHERE c.entity_type='release' AND r.id IN (c.left_id,c.right_id) AND r.merged_into_id IS NOT NULL)
		ORDER BY score DESC,id LIMIT $2`, entityType, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var id, left, right int64
		var typ, status string
		var score float64
		var evidence []byte
		if err = r.Scan(&id, &typ, &left, &right, &score, &evidence, &status); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "entity_type": typ, "left_id": left, "right_id": right, "score": score, "evidence": json.RawMessage(evidence), "status": status})
	}
	return out, r.Err()
}

func Open(ctx context.Context) (*Store, error) {
	p, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return nil, e
	}
	s := &Store{Pool: p, workerID: fmt.Sprintf("worker-%d", time.Now().UnixNano())}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(842010)")
	if e == nil {
		_, e = tx.Exec(ctx, migrations.Schema)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) AddSource(ctx context.Context, x model.Source) error {
	_, e := s.Exec(ctx, "INSERT INTO sources(name,url) VALUES($1,$2) ON CONFLICT(url) DO UPDATE SET name=excluded.name", x.Name, x.URL)
	return e
}
func (s *Store) Items(ctx context.Context, q string) ([]model.Item, error) {
	r, e := s.Query(ctx, "SELECT id,source_id,title,url,summary,updated_at FROM items WHERE title ILIKE '%'||$1||'%' ORDER BY updated_at DESC LIMIT 100", q)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Item{}
	for r.Next() {
		var x model.Item
		if e = r.Scan(&x.ID, &x.SourceID, &x.Title, &x.URL, &x.Summary, &x.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}

func (s *Store) Performers(ctx context.Context, q string, limit int) ([]model.Performer, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	r, e := s.Query(ctx, `SELECT id,slug,canonical_name,COALESCE(canonical_name_ja,''),birth_date,
		COALESCE(height_cm,0),COALESCE(bust_cm,0),COALESCE(waist_cm,0),COALESCE(hip_cm,0),status
		FROM performers WHERE merged_into_id IS NULL AND status='active'
		AND ($1='' OR canonical_name ILIKE '%'||$1||'%' OR canonical_name_ja ILIKE '%'||$1||'%'
		 OR id IN (SELECT performer_id FROM performer_aliases WHERE normalized_name ILIKE '%'||lower($1)||'%'))
		ORDER BY canonical_name,id LIMIT $2`, q, limit)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Performer{}
	for r.Next() {
		var x model.Performer
		if e = r.Scan(&x.ID, &x.Slug, &x.CanonicalName, &x.CanonicalNameJA, &x.BirthDate, &x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.Status); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}

func (s *Store) PerformerBySlug(ctx context.Context, slug string) (model.Performer, error) {
	var x model.Performer
	e := s.QueryRow(ctx, `SELECT id,slug,canonical_name,COALESCE(canonical_name_ja,''),birth_date,
		COALESCE(height_cm,0),COALESCE(bust_cm,0),COALESCE(waist_cm,0),COALESCE(hip_cm,0),status
		FROM performers WHERE slug=$1 AND merged_into_id IS NULL AND status='active'`, slug).Scan(&x.ID, &x.Slug, &x.CanonicalName, &x.CanonicalNameJA, &x.BirthDate, &x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.Status)
	return x, e
}

func (s *Store) PerformerDetail(ctx context.Context, slug string) (map[string]any, error) {
	var raw []byte
	err := s.QueryRow(ctx, `SELECT row_to_json(x) FROM (SELECT p.id,p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,sr.raw_data->>'name_ja') canonical_name_ja,COALESCE(p.birth_date::text,sr.raw_data->>'birth_date') birth_date,p.birth_place,COALESCE(p.debut_date::text,sr.raw_data->>'debut_date') debut_date,p.debut_age,p.blood_type,COALESCE(p.height_cm::text,sr.raw_data->>'height_cm') height_cm,p.bust_cm,p.waist_cm,p.hip_cm,COALESCE(p.cup_size,sr.raw_data->>'cup_size') cup_size,p.shoe_size,p.hair_color,p.hair_length,p.activity_start,p.activity_end,p.biography,p.status FROM performers p LEFT JOIN LATERAL (SELECT raw_data FROM source_records WHERE performer_id=p.id ORDER BY last_seen_at DESC LIMIT 1) sr ON true WHERE p.slug=$1 AND p.merged_into_id IS NULL AND p.status='active') x`, slug).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) Releases(ctx context.Context, q string, limit int) ([]model.Release, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	r, e := s.Query(ctx, `SELECT id,slug,canonical_title,COALESCE(title_ja,''),release_date,
		COALESCE(runtime_minutes,0),COALESCE(maker_name,''),COALESCE(label_name,''),COALESCE(series_name,''),status
		FROM releases WHERE merged_into_id IS NULL AND status='published'
		AND ($1='' OR canonical_title ILIKE '%'||$1||'%' OR title_ja ILIKE '%'||$1||'%'
		 OR id IN (SELECT release_id FROM release_identifiers WHERE normalized_value=lower($1)))
		ORDER BY release_date DESC NULLS LAST,id DESC LIMIT $2`, q, limit)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Release{}
	for r.Next() {
		var x model.Release
		if e = r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}

func (s *Store) ReleaseBySlug(ctx context.Context, slug string) (model.Release, error) {
	var x model.Release
	e := s.QueryRow(ctx, `SELECT id,slug,canonical_title,COALESCE(title_ja,''),release_date,
		COALESCE(runtime_minutes,0),COALESCE(maker_name,''),COALESCE(label_name,''),COALESCE(series_name,''),status
		FROM releases WHERE slug=$1 AND merged_into_id IS NULL AND status='published'`, slug).Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status)
	return x, e
}

func (s *Store) PerformerReleases(ctx context.Context, slug string, limit int) ([]model.Release, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	r, err := s.Query(ctx, `SELECT r.id,r.slug,r.canonical_title,COALESCE(r.title_ja,''),r.release_date,COALESCE(r.runtime_minutes,0),COALESCE(r.maker_name,''),COALESCE(r.label_name,''),COALESCE(r.series_name,''),r.status FROM releases r JOIN release_performers rp ON rp.release_id=r.id JOIN performers p ON p.id=rp.performer_id WHERE p.slug=$1 AND r.status='published' AND r.merged_into_id IS NULL ORDER BY r.release_date DESC NULLS LAST,r.id DESC LIMIT $2`, slug, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []model.Release{}
	for r.Next() {
		var x model.Release
		if err = r.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, r.Err()
}

func (s *Store) ReleasePerformers(ctx context.Context, slug string) ([]model.Performer, error) {
	r, err := s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,''),p.birth_date,COALESCE(p.height_cm,0),COALESCE(p.bust_cm,0),COALESCE(p.waist_cm,0),COALESCE(p.hip_cm,0),p.status FROM performers p JOIN release_performers rp ON rp.performer_id=p.id JOIN releases r ON r.id=rp.release_id WHERE r.slug=$1 AND p.status='active' AND p.merged_into_id IS NULL ORDER BY rp.billing_order NULLS LAST,p.id`, slug)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []model.Performer{}
	for r.Next() {
		var x model.Performer
		if err = r.Scan(&x.ID, &x.Slug, &x.CanonicalName, &x.CanonicalNameJA, &x.BirthDate, &x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.Status); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, r.Err()
}

func (s *Store) ReleaseSources(ctx context.Context, slug string) ([]map[string]any, error) {
	r, err := s.Query(ctx, `SELECT sr.id,s.name,sr.source_record_key,sr.source_url,sr.last_seen_at FROM source_records sr JOIN sources s ON s.id=sr.source_id JOIN releases rel ON rel.id=sr.release_id WHERE rel.slug=$1 AND sr.entity_type='release' AND rel.status='published' ORDER BY sr.last_seen_at DESC`, slug)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var id int64
		var name, key, u string
		var seen time.Time
		if err = r.Scan(&id, &name, &key, &u, &seen); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"source_record_id": id, "source": name, "source_key": key, "url": u, "last_seen_at": seen})
	}
	return out, r.Err()
}

func (s *Store) ReleasePlayback(ctx context.Context, slug, source string) (map[string]any, error) {
	var key, u, name string
	err := s.QueryRow(ctx, `SELECT rms.media_id,COALESCE(rms.media_url,''),rms.source FROM release_media_sources rms JOIN releases r ON r.id=rms.release_id WHERE r.slug=$1 AND lower(rms.source)=lower($2) LIMIT 1`, slug, source).Scan(&key, &u, &name)
	if err == nil {
		return map[string]any{"source": name, "source_key": key, "source_url": u, "detail_path": "/api/detail/" + strings.ToLower(source) + "/" + key, "resolve_path": "/api/resolve/" + strings.ToLower(source) + "/" + key}, nil
	}
	err = s.QueryRow(ctx, `SELECT sr.source_record_key,sr.source_url,s.name FROM source_records sr JOIN sources s ON s.id=sr.source_id JOIN releases r ON r.id=sr.release_id WHERE r.slug=$1 AND sr.entity_type='release' AND lower(s.name)=lower($2) ORDER BY sr.last_seen_at DESC LIMIT 1`, slug, source).Scan(&key, &u, &name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source": name, "source_key": key, "source_url": u, "detail_path": "/api/detail/" + strings.ToLower(source) + "/" + key, "resolve_path": "/api/resolve/" + strings.ToLower(source) + "/" + key}, nil
}

// LinkMediaToRelease stores a provider media id against a canonical release.
// Repeated discovery is idempotent and changing the URL refreshes the link.
func (s *Store) LinkMediaToRelease(ctx context.Context, releaseID int64, source, mediaID, mediaURL string) error {
	_, err := s.Exec(ctx, `INSERT INTO release_media_sources(release_id,source,media_id,media_url)
		VALUES($1,lower($2),$3,NULLIF($4,'')) ON CONFLICT(release_id,source)
		DO UPDATE SET media_id=EXCLUDED.media_id,media_url=COALESCE(EXCLUDED.media_url,release_media_sources.media_url),updated_at=now()`, releaseID, source, mediaID, mediaURL)
	return err
}

// LinkMediaByIdentifier associates a provider id with the release carrying a
// normalized identifier (for example JUQ-743). It returns false when no
// unique release match exists, leaving ambiguous matches for review.
func (s *Store) LinkMediaByIdentifier(ctx context.Context, source, mediaID, mediaURL, identifier string) (bool, error) {
	var releaseID int64
	err := s.QueryRow(ctx, `SELECT release_id FROM release_identifiers WHERE normalized_value=$1 LIMIT 1`, catalog.NormalizeIdentifier(identifier)).Scan(&releaseID)
	if err != nil {
		return false, err
	}
	return true, s.LinkMediaToRelease(ctx, releaseID, source, mediaID, mediaURL)
}

func (s *Store) Tags(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	r, err := s.Query(ctx, `SELECT normalized_tag,MIN(tag),count(*) FROM release_tags GROUP BY normalized_tag ORDER BY count(*) DESC,normalized_tag LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var slug, name string
		var count int64
		if err = r.Scan(&slug, &name, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"slug": slug, "name": name, "count": count})
	}
	return out, r.Err()
}

func (s *Store) Makers(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	r, err := s.Query(ctx, `SELECT maker_name,count(*) FROM releases WHERE status='published' AND maker_name<>'' GROUP BY maker_name ORDER BY count(*) DESC,maker_name LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var name string
		var count int64
		if err = r.Scan(&name, &count); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"name": name, "count": count})
	}
	return out, r.Err()
}

func (s *Store) CatalogSearch(ctx context.Context, q string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	r, err := s.Query(ctx, `SELECT 'performer',id,slug,canonical_name FROM performers WHERE status='active' AND merged_into_id IS NULL AND (canonical_name ILIKE '%'||$1||'%' OR canonical_name_ja ILIKE '%'||$1||'%') UNION ALL SELECT 'release',id,slug,canonical_title FROM releases WHERE status='published' AND merged_into_id IS NULL AND (canonical_title ILIKE '%'||$1||'%' OR title_ja ILIKE '%'||$1||'%') ORDER BY 1,4 LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var typ, name, slug string
		var id int64
		if err = r.Scan(&typ, &id, &slug, &name); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"type": typ, "id": id, "slug": slug, "name": name})
	}
	return out, r.Err()
}

func (s *Store) SaveMediaAsset(ctx context.Context, asset map[string]any) error {
	_, err := s.Exec(ctx, `INSERT INTO media_assets(entity_type,entity_id,asset_type,storage_provider,object_key,source_url,content_sha256,mime_type,width,height,byte_size,is_original,retained_until) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13) ON CONFLICT(entity_type,entity_id,asset_type,content_sha256) DO UPDATE SET object_key=EXCLUDED.object_key,byte_size=EXCLUDED.byte_size,width=EXCLUDED.width,height=EXCLUDED.height,retained_until=EXCLUDED.retained_until`, asset["entity_type"], asset["entity_id"], asset["asset_type"], asset["storage_provider"], asset["object_key"], asset["source_url"], asset["content_sha256"], asset["mime_type"], asset["width"], asset["height"], asset["byte_size"], asset["is_original"], asset["retained_until"])
	return err
}

func (s *Store) PendingConflicts(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	r, err := s.Query(ctx, `SELECT id,entity_type,entity_id,field_name,current_value,candidates,severity,created_at FROM field_conflicts WHERE status='pending' ORDER BY severity DESC,created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []map[string]any{}
	for r.Next() {
		var id, entityID int64
		var typ, field, severity string
		var current, candidates []byte
		var created time.Time
		if err = r.Scan(&id, &typ, &entityID, &field, &current, &candidates, &severity, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "entity_type": typ, "entity_id": entityID, "field_name": field, "current_value": json.RawMessage(current), "candidates": json.RawMessage(candidates), "severity": severity, "created_at": created})
	}
	return out, r.Err()
}

// ConflictResolution is the outcome of ResolveConflict.
type ConflictResolution struct {
	ConflictID int64           `json:"conflict_id"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Field      string          `json:"field"`
	Column     string          `json:"column"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	// Audit is the inbound R-AGE / R-META re-check of the affected releases,
	// run when the resolved column feeds age at filming.
	Audit []audit.InboundResult `json:"audit,omitempty"`
}

// ResolveConflict settles a pending field conflict. The conflict's field name
// (a parser field such as "title" or "maker", or a column name) is mapped to
// its canonical column by ConflictField and the value is typed for that
// column. value is the JSON value to keep; when it is empty, the current
// value of sourceRecordID ("采用来源值") is used. The column is updated, an
// override is written so the next crawl does not revert it, reviewed
// translations of the entity are marked stale, and a review_events row
// records before / after and actor. Unknown conflict: pgx.ErrNoRows; already
// resolved: *ConflictError; bad field or value: *ValidationError.
func (s *Store) ResolveConflict(ctx context.Context, conflictID, sourceRecordID int64, value json.RawMessage, note, actor string) (ConflictResolution, error) {
	res := ConflictResolution{ConflictID: conflictID}
	if actor == "" {
		actor = "admin"
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	var status string
	if err = tx.QueryRow(ctx, `SELECT entity_type,entity_id,field_name,status FROM field_conflicts WHERE id=$1 FOR UPDATE`, conflictID).Scan(&res.EntityType, &res.EntityID, &res.Field, &status); err != nil {
		return res, err
	}
	if status != "pending" {
		return res, conflict("conflict is already %s", status)
	}
	spec, ok := ConflictField(res.EntityType, res.Field)
	if !ok {
		return res, &ValidationError{Msg: fmt.Sprintf("%s: %s", ErrNotReviewable, res.Field)}
	}
	res.Column = spec.Column
	if sourceRecordID > 0 {
		var belongs bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_records WHERE id=$1 AND entity_type=$2
			AND (($2='release' AND release_id=$3) OR ($2='performer' AND performer_id=$3)))`, sourceRecordID, res.EntityType, res.EntityID).Scan(&belongs); err != nil {
			return res, err
		}
		if !belongs {
			return res, invalid("source_record_id is not a source of this %s", res.EntityType)
		}
		if len(value) == 0 {
			var raw []byte
			err = tx.QueryRow(ctx, `SELECT normalized_value FROM source_field_values WHERE source_record_id=$1 AND field_name=$2 AND is_current`, sourceRecordID, res.Field).Scan(&raw)
			if errors.Is(err, pgx.ErrNoRows) {
				return res, invalid("source record has no current value for %s", res.Field)
			}
			if err != nil {
				return res, err
			}
			value = raw
		}
	}
	if len(value) == 0 {
		return res, invalid("value or source_record_id is required")
	}
	text, err := CoerceFieldValue(spec, value)
	if err != nil {
		return res, err
	}
	table := "performers"
	if res.EntityType == "release" {
		table = "releases"
	}
	var before, after []byte
	if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(%s) FROM %s WHERE id=$1 FOR UPDATE`, spec.Column, table), res.EntityID).Scan(&before); err != nil {
		return res, err
	}
	if err = tx.QueryRow(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s=$1::%[3]s,updated_at=now(),version=version+1 WHERE id=$2 RETURNING to_jsonb(%[2]s)`,
		table, spec.Column, spec.Kind.sqlType()), text, res.EntityID).Scan(&after); err != nil {
		return res, err
	}
	// to_jsonb(NULL) is SQL NULL, not JSON null: never build ''::jsonb from it.
	res.Before, res.After = rawOrNull(before), rawOrNull(after)
	before, after = res.Before, res.After
	if err = upsertOverride(ctx, tx, res.EntityType, res.EntityID, spec.Column, after, note, actor, conflictID); err != nil {
		return res, err
	}
	if err = markStale(ctx, tx, res.EntityType, strconv.FormatInt(res.EntityID, 10)); err != nil {
		return res, err
	}
	if _, err = tx.Exec(ctx, `UPDATE field_conflicts SET status='resolved',current_value=$4::jsonb,selected_source_record_id=NULLIF($2,0),
		resolution_note=$3,resolved_at=now(),updated_at=now() WHERE id=$1`, conflictID, sourceRecordID, note, string(after)); err != nil {
		return res, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO review_events(conflict_id,entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES($1,$2,$3,'resolve',jsonb_build_object('field',$4::text,'value',$5::jsonb),
		jsonb_build_object('field',$4::text,'value',$6::jsonb,'source_record_id',NULLIF($7::bigint,0)),NULLIF($8,''),$9)`,
		conflictID, res.EntityType, res.EntityID, spec.Column, string(before), string(after), sourceRecordID, note, actor); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	res.Audit, err = s.recheckAfterEdit(ctx, res.EntityType, res.EntityID, spec.Column)
	return res, err
}

// recheckAfterEdit re-runs the inbound audit (R-AGE + R-META) for the
// releases whose age at filming depends on the edited column: the release
// itself for release_date / shoot_year, every credited release for a
// performer's birth_date. R-AGE may hold them.
func (s *Store) recheckAfterEdit(ctx context.Context, entityType string, id int64, column string) ([]audit.InboundResult, error) {
	var ids []int64
	switch {
	case entityType == "release" && (column == "release_date" || column == "shoot_year"):
		ids = []int64{id}
	case entityType == "performer" && column == "birth_date":
		rows, err := s.Query(ctx, `SELECT DISTINCT release_id FROM release_performers WHERE performer_id=$1 ORDER BY 1`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var rid int64
			if err = rows.Scan(&rid); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, rid)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	var out []audit.InboundResult
	for _, rid := range ids {
		r, err := audit.CheckRelease(ctx, s, rid, audit.DefaultAgeConfig, time.Now())
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

// SaveCatalogRecord persists one parsed source record and its normalized entity
// in one transaction. It is intentionally idempotent on (source, type, key).
func (s *Store) SaveCatalogRecord(ctx context.Context, sourceID int64, parserVersion string, rec catalog.Record) (int64, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var performerID, releaseID *int64
	if rec.EntityType == catalog.Performer {
		name, _ := rec.Fields["name"].(string)
		if name == "" {
			return 0, fmt.Errorf("performer name is required")
		}
		slug := catalog.Slug(name)
		if slug == "" {
			slug = catalog.Slug(rec.SourceKey)
		}
		var id int64
		nameJA, _ := rec.Fields["name_ja"].(string)
		birthPlace, _ := rec.Fields["birth_place"].(string)
		birthDate, _ := rec.Fields["birth_date"].(string)
		debutDate, _ := rec.Fields["debut_date"].(string)
		height, _ := rec.Fields["height_cm"].(any)
		cup, _ := rec.Fields["cup_size"].(string)
		err = tx.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,canonical_name_ja,birth_date,birth_place,debut_date,height_cm,cup_size) VALUES($1,$2,NULLIF($3,''),NULLIF($4,'')::date,NULLIF($5,''),NULLIF($6,'')::date,NULLIF($7,'')::numeric,NULLIF($8,''))
			ON CONFLICT(slug) DO UPDATE SET `+performerIngestUpdate+`,updated_at=now() RETURNING id`, slug, name, nameJA, birthDate, birthPlace, debutDate, height, cup).Scan(&id)
		if err != nil {
			return 0, err
		}
		if id, err = survivingID(ctx, tx, "performers", id); err != nil {
			return 0, err
		}
		if err = markStale(ctx, tx, i18n.EntityPerformer, strconv.FormatInt(id, 10)); err != nil {
			return 0, err
		}
		performerID = &id
	} else if rec.EntityType == catalog.Release {
		title, _ := rec.Fields["title"].(string)
		code, _ := rec.Fields["dvd_code"].(string)
		if title == "" {
			return 0, fmt.Errorf("release title is required")
		}
		slug := catalog.ReleaseSlug(code, title, "source", rec.SourceKey)
		var id int64
		var date any = nil
		if v, ok := rec.Fields["release_date"].(string); ok && v != "" {
			date = v
		}
		maker, _ := rec.Fields["maker"].(string)
		director, _ := rec.Fields["director"].(string)
		err = tx.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,release_date,maker_name,director_name)
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(slug) DO UPDATE SET `+releaseIngestUpdate+`,updated_at=now() RETURNING id`, slug, title, date, maker, director).Scan(&id)
		if err != nil {
			return 0, err
		}
		if id, err = survivingID(ctx, tx, "releases", id); err != nil {
			return 0, err
		}
		if err = markStale(ctx, tx, i18n.EntityRelease, strconv.FormatInt(id, 10)); err != nil {
			return 0, err
		}
		releaseID = &id
		if code != "" {
			scope := "global"
			if maker != "" {
				scope = "maker:" + catalog.Slug(maker)
			}
			_, err = tx.Exec(ctx, `INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value,identifier_scope,source_id) VALUES($1,'dvd_code',$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, code, catalog.NormalizeIdentifier(code), scope, sourceID)
			if err != nil {
				return 0, err
			}
		}
		if cid, _ := rec.Fields["content_id"].(string); cid != "" {
			_, err = tx.Exec(ctx, `INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value,identifier_scope,source_id) VALUES($1,'content_id',$2,$3,'global',$4) ON CONFLICT DO NOTHING`, id, cid, catalog.NormalizeIdentifier(cid), sourceID)
			if err != nil {
				return 0, err
			}
		}
	} else {
		return 0, fmt.Errorf("unsupported entity type %q", rec.EntityType)
	}
	entityID := performerID
	if rec.EntityType == catalog.Release {
		entityID = releaseID
	}
	// Manual overrides win over every source (FINAL_DATA_DESIGN 6): the
	// upserts above already keep overridden columns; relations and conflicts
	// below skip overridden fields. Source evidence is still recorded.
	overridden, err := overriddenFields(ctx, tx, string(rec.EntityType), *entityID)
	if err != nil {
		return 0, err
	}
	var sourceRecordID int64
	err = tx.QueryRow(ctx, `INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,performer_id,release_id,raw_data,parser_version,resolution_status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'resolved') ON CONFLICT(source_id,entity_type,source_record_key)
		DO UPDATE SET source_url=EXCLUDED.source_url,performer_id=EXCLUDED.performer_id,release_id=EXCLUDED.release_id,
		raw_data=EXCLUDED.raw_data,parser_version=EXCLUDED.parser_version,last_seen_at=now(),resolution_status='resolved' RETURNING id`, sourceID, string(rec.EntityType), rec.SourceKey, rec.URL, performerID, releaseID, rec.Fields, parserVersion).Scan(&sourceRecordID)
	if err != nil {
		return 0, err
	}
	if rec.EntityType == catalog.Release && releaseID != nil {
		if names, ok := rec.Fields["performers"].([]string); ok {
			for order, name := range names {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				slug := catalog.Slug(name)
				if slug == "" {
					continue
				}
				var performerID int64
				if err = tx.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$2) ON CONFLICT(slug) DO UPDATE SET updated_at=now() RETURNING id`, slug, name).Scan(&performerID); err != nil {
					return 0, err
				}
				if performerID, err = survivingID(ctx, tx, "performers", performerID); err != nil {
					return 0, err
				}
				if !overridden[OverridePerformers] {
					if _, err = tx.Exec(ctx, `INSERT INTO release_performers(release_id,performer_id,billing_order,credited_name,is_primary) VALUES($1,$2,$3,$4,$5) ON CONFLICT(release_id,performer_id) DO UPDATE SET billing_order=EXCLUDED.billing_order,credited_name=EXCLUDED.credited_name`, *releaseID, performerID, order, name, order == 0); err != nil {
						return 0, err
					}
				}
				if _, err = tx.Exec(ctx, `INSERT INTO release_performer_sources(release_id,performer_id,source_record_id,billing_order,credited_name) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, *releaseID, performerID, sourceRecordID, order, name); err != nil {
					return 0, err
				}
			}
		}
		if tags, ok := rec.Fields["tags"].([]string); ok {
			for _, tag := range tags {
				tag = strings.TrimSpace(tag)
				normalized := catalog.Slug(tag)
				if normalized == "" {
					continue
				}
				if _, err = tx.Exec(ctx, `INSERT INTO release_tag_sources(release_id,normalized_tag,source_record_id,source_tag) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, *releaseID, normalized, sourceRecordID, tag); err != nil {
					return 0, err
				}
				if overridden[OverrideTags] {
					continue
				}
				if _, err = tx.Exec(ctx, `INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$3) ON CONFLICT(release_id,normalized_tag) DO UPDATE SET tag=EXCLUDED.tag`, *releaseID, tag, normalized); err != nil {
					return 0, err
				}
				if err = markStale(ctx, tx, i18n.EntityTag, normalized); err != nil {
					return 0, err
				}
			}
		}
	}
	fieldNames := make([]string, 0, len(rec.Fields))
	for field, value := range rec.Fields {
		raw, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return 0, marshalErr
		}
		if _, err = tx.Exec(ctx, `UPDATE source_field_values SET is_current=false WHERE source_record_id=$1 AND field_name=$2 AND is_current`, sourceRecordID, field); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO source_field_values(source_record_id,field_name,raw_value,normalized_value,confidence,captured_at,is_current) VALUES($1,$2,$3::jsonb,$3::jsonb,'verified',now(),true)`, sourceRecordID, field, string(raw)); err != nil {
			return 0, err
		}
		fieldNames = append(fieldNames, field)
	}
	// Canonical columns follow source_field_policies / source_kind tiers;
	// conflicts open only where a source disagrees with the winning value.
	if err = applyFieldPolicy(ctx, tx, string(rec.EntityType), *entityID, fieldNames, overridden); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return sourceRecordID, nil
}
func (s *Store) Heartbeat(ctx context.Context, id int64, worker string, version int64) error {
	r, e := s.Exec(ctx, "UPDATE jobs SET lease_until=now()+interval '90 seconds',heartbeat_at=now() WHERE id=$1 AND status='running' AND worker_id=$2 AND (execution_version=$3 OR $3=0) AND lease_until>now()", id, worker, version)
	if e == nil && r.RowsAffected() != 1 {
		return fmt.Errorf("lease lost")
	}
	return e
}
func (s *Store) ReapExpired(ctx context.Context) error {
	_, e := s.Exec(ctx, "UPDATE jobs SET status=CASE WHEN attempt<max_attempts THEN 'pending' ELSE 'failed' END,available_at=now()+interval '5 seconds',finished_at=CASE WHEN attempt>=max_attempts THEN now() ELSE NULL END,worker_id=NULL,lease_until=NULL,error_code='lease_expired',error='worker lease expired' WHERE status='running' AND lease_until<now()")
	return e
}
func (s *Store) Finish(ctx context.Context, id int64, items []model.Item, cause error) error {
	if code, msg := finishCode(cause); code != "" {
		// Blocked sources, robots.txt refusals and gate/config refusals are
		// not retried by the job queue: BlockSource parks a blocked source
		// and the scheduler creates a fresh job later; the others need a
		// change on the source (robots.txt, license, configuration) first.
		_, e := s.Exec(ctx, "UPDATE jobs SET status='failed',error=$2,error_code=$3,finished_at=now(),worker_id=NULL,lease_until=NULL WHERE id=$1 AND status='running'", id, msg, code)
		return e
	}
	if cause != nil {
		_, e := s.Exec(ctx, "UPDATE jobs SET status=CASE WHEN attempt<max_attempts THEN 'pending' ELSE 'failed' END,error=$2,error_code='fetch_failed',available_at=now()+interval '5 seconds',finished_at=CASE WHEN attempt>=max_attempts THEN now() ELSE NULL END,worker_id=NULL,lease_until=NULL WHERE id=$1 AND status='running'", id, cause.Error())
		return e
	}
	tx, e := s.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, x := range items {
		_, e = tx.Exec(ctx, "INSERT INTO items(source_id,title,url,summary) VALUES($1,$2,$3,$4) ON CONFLICT(url) DO UPDATE SET title=excluded.title,summary=excluded.summary,updated_at=now()", x.SourceID, x.Title, x.URL, x.Summary)
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE jobs SET status='succeeded',finished_at=now(),worker_id=NULL,lease_until=NULL,error='',error_code='' WHERE id=$1 AND status='running'", id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
