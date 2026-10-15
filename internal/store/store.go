package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/migrations"
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"time"
)

type Store struct {
	*pgxpool.Pool
	workerID string
}

type CrawlCheckpoint struct {
	SourceID                                                     int64
	TaskType, PartitionKey, Cursor, ParserVersion, LastErrorCode string
	Watermark                                                    *time.Time
	CompletedPages                                               int64
	LastSuccessAt                                                *time.Time
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
func (s *Store) Sources(ctx context.Context) ([]model.Source, error) {
	r, e := s.Query(ctx, "SELECT id,name,url,enabled,version,adapter,source_kind,license_status,egress_policy FROM sources ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Source{}
	for r.Next() {
		var x model.Source
		if e = r.Scan(&x.ID, &x.Name, &x.URL, &x.Enabled, &x.Version, &x.Adapter, &x.SourceKind, &x.LicenseStatus, &x.EgressPolicy); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}
func (s *Store) AddSource(ctx context.Context, x model.Source) error {
	_, e := s.Exec(ctx, "INSERT INTO sources(name,url) VALUES($1,$2) ON CONFLICT(url) DO UPDATE SET name=excluded.name", x.Name, x.URL)
	return e
}
func (s *Store) Enqueue(ctx context.Context, id int64) error {
	var enabled bool
	if e := s.QueryRow(ctx, "SELECT enabled FROM sources WHERE id=$1", id).Scan(&enabled); e != nil {
		return e
	}
	if !enabled {
		return fmt.Errorf("source disabled")
	}
	_, e := s.Exec(ctx, "INSERT INTO jobs(source_id) VALUES($1)", id)
	return e
}

// EnqueueDue creates at most one job per due source and advances its schedule.
// The scheduler never performs network I/O; workers claim the resulting jobs.
func (s *Store) EnqueueDue(ctx context.Context) (int, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	r, err := tx.Query(ctx, `SELECT s.id FROM sources s LEFT JOIN source_crawl_policies p ON p.source_id=s.id WHERE s.enabled AND (s.next_run_at IS NULL OR s.next_run_at<=now()) ORDER BY COALESCE(s.next_run_at,now()),s.id FOR UPDATE OF s SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	defer r.Close()
	ids := []int64{}
	for r.Next() {
		var id int64
		if err = r.Scan(&id); err != nil {
			return 0, err
		}
		ids = append(ids, id)
	}
	if err = r.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err = tx.Exec(ctx, `INSERT INTO jobs(source_id) SELECT $1 WHERE NOT EXISTS (SELECT 1 FROM jobs WHERE source_id=$1 AND status IN ('pending','running'))`, id); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `UPDATE sources SET next_run_at=now()+COALESCE((SELECT discovery_interval FROM source_crawl_policies WHERE source_id=$1),interval '1 hour') WHERE id=$1`, id); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(ids), nil
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

func (s *Store) ResolveConflict(ctx context.Context, conflictID, sourceRecordID int64, value json.RawMessage, note string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var typ, field string
	var entityID int64
	var before []byte
	if err = tx.QueryRow(ctx, `SELECT entity_type,entity_id,field_name,current_value FROM field_conflicts WHERE id=$1 AND status='pending' FOR UPDATE`, conflictID).Scan(&typ, &entityID, &field, &before); err != nil {
		return err
	}
	allowed := map[string]bool{"canonical_name": true, "canonical_name_ja": true, "birth_date": true, "height_cm": true, "title": true, "title_ja": true, "release_date": true, "maker_name": true, "director_name": true}
	if !allowed[field] {
		return fmt.Errorf("field is not reviewable: %s", field)
	}
	table := "performers"
	if typ == "release" {
		table = "releases"
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s=$1,updated_at=now() WHERE id=$2", table, field), string(value), entityID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE field_conflicts SET status='resolved',selected_source_record_id=NULLIF($2,0),resolution_note=$3,resolved_at=now(),updated_at=now() WHERE id=$1`, conflictID, sourceRecordID, note); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO review_events(conflict_id,entity_type,entity_id,action,before_value,after_value,reason) VALUES($1,$2,$3,'resolve',$4::jsonb,$5::jsonb,$6)`, conflictID, typ, entityID, string(before), string(value), note); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		err = tx.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$2)
			ON CONFLICT(slug) DO UPDATE SET updated_at=now() RETURNING id`, slug, name).Scan(&id)
		if err != nil {
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
			VALUES($1,$2,$3,$4,$5) ON CONFLICT(slug) DO UPDATE SET canonical_title=EXCLUDED.canonical_title,
			maker_name=COALESCE(NULLIF(EXCLUDED.maker_name,''),releases.maker_name),updated_at=now() RETURNING id`, slug, title, date, maker, director).Scan(&id)
		if err != nil {
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
				if _, err = tx.Exec(ctx, `INSERT INTO release_performers(release_id,performer_id,billing_order,credited_name,is_primary) VALUES($1,$2,$3,$4,$5) ON CONFLICT(release_id,performer_id) DO UPDATE SET billing_order=EXCLUDED.billing_order,credited_name=EXCLUDED.credited_name`, *releaseID, performerID, order, name, order == 0); err != nil {
					return 0, err
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
				if _, err = tx.Exec(ctx, `INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$3) ON CONFLICT(release_id,normalized_tag) DO UPDATE SET tag=EXCLUDED.tag`, *releaseID, tag, normalized); err != nil {
					return 0, err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO release_tag_sources(release_id,normalized_tag,source_record_id,source_tag) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, *releaseID, normalized, sourceRecordID, tag); err != nil {
					return 0, err
				}
			}
		}
	}
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
		entityID := performerID
		if rec.EntityType == catalog.Release {
			entityID = releaseID
		}
		var distinct int
		var candidates []byte
		if err = tx.QueryRow(ctx, `SELECT count(DISTINCT sf.normalized_value), COALESCE(jsonb_agg(jsonb_build_object('source_record_id',sf.source_record_id,'value',sf.normalized_value)),'[]'::jsonb)
			FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id
			WHERE sf.is_current AND sf.field_name=$1 AND sr.entity_type=$2 AND (($2='performer' AND sr.performer_id=$3) OR ($2='release' AND sr.release_id=$3))`, field, string(rec.EntityType), *entityID).Scan(&distinct, &candidates); err != nil {
			return 0, err
		}
		if distinct > 1 {
			if _, err = tx.Exec(ctx, `INSERT INTO field_conflicts(entity_type,entity_id,field_name,current_value,candidates,severity,status)
				VALUES($1,$2,$3,NULL,$4,'medium','pending')
				ON CONFLICT DO NOTHING`, string(rec.EntityType), *entityID, field, candidates); err != nil {
				return 0, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return sourceRecordID, nil
}
func (s *Store) Jobs(ctx context.Context) ([]model.Job, error) {
	r, e := s.Query(ctx, "SELECT id,source_id,status,error,created_at,attempt,lease_until,execution_version FROM jobs ORDER BY id DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Job{}
	for r.Next() {
		var x model.Job
		if e = r.Scan(&x.ID, &x.SourceID, &x.Status, &x.Error, &x.CreatedAt, &x.Attempt, &x.LeaseUntil, &x.ExecutionVersion); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}
func (s *Store) Claim(ctx context.Context) (int64, model.Source, error) {
	var id int64
	var x model.Source
	worker := s.workerID
	e := s.QueryRow(ctx, `WITH picked AS (SELECT j.id FROM jobs j JOIN sources s ON s.id=j.source_id WHERE j.status='pending' AND j.available_at<=now() AND s.enabled ORDER BY j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1), claimed AS (UPDATE jobs j SET status='running',started_at=COALESCE(started_at,now()),attempt=attempt+1,worker_id=$1,lease_until=now()+interval '90 seconds',heartbeat_at=now(),execution_version=execution_version+1 WHERE j.id IN (SELECT id FROM picked) RETURNING j.id,j.source_id) SELECT c.id,s.id,s.name,s.url,s.enabled,s.version,s.adapter,s.source_kind,s.license_status,s.egress_policy FROM claimed c JOIN sources s ON s.id=c.source_id`, worker).Scan(&id, &x.ID, &x.Name, &x.URL, &x.Enabled, &x.Version, &x.Adapter, &x.SourceKind, &x.LicenseStatus, &x.EgressPolicy)
	return id, x, e
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
