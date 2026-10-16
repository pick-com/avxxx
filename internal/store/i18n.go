package store

// Multi-language content storage (migration 015): public translation
// lookups, stale tracking, the admin translation workbench, glossary and UI
// strings. Workflow rules live in internal/i18n; this file only persists.

import (
	"avxxx/internal/i18n"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// markStale flags reviewed translations of one entity whose source field
// changed since they were written (source_hash mismatch). Stale rows keep
// being served publicly until re-reviewed.
func markStale(ctx context.Context, q execer, entityType, entityID string) error {
	_, err := q.Exec(ctx, `UPDATE translations SET status='stale',updated_at=now()
		WHERE entity_type=$1 AND entity_id=$2 AND status='reviewed'
		AND source_hash<>i18n_hash(i18n_source(entity_type,entity_id,field))`, entityType, entityID)
	return err
}

// MarkStaleTranslations sweeps every reviewed translation against its
// current source and returns how many were marked stale. It catches source
// edits made outside SaveCatalogRecord and ResolveConflict.
func (s *Store) MarkStaleTranslations(ctx context.Context) (int64, error) {
	tag, err := s.Exec(ctx, `UPDATE translations SET status='stale',updated_at=now()
		WHERE status='reviewed' AND source_hash<>i18n_hash(i18n_source(entity_type,entity_id,field))`)
	return tag.RowsAffected(), err
}

// PublicTranslations batch-loads the publicly servable translations of refs
// in langs, in one query: each row's reviewed_value, i.e. the last reviewed
// text (status reviewed or stale, or a draft re-edit of one). Machine and
// never-reviewed draft text is never returned.
func (s *Store) PublicTranslations(ctx context.Context, langs []string, refs []i18n.Ref) (i18n.Set, error) {
	set := i18n.Set{}
	if len(langs) == 0 || len(refs) == 0 {
		return set, nil
	}
	ets, ids, fs := make([]string, len(refs)), make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		ets[i], ids[i], fs[i] = r.EntityType, r.EntityID, r.Field
	}
	rows, err := s.Query(ctx, `SELECT t.entity_type,t.entity_id,t.field,t.lang,t.reviewed_value
		FROM translations t JOIN (SELECT DISTINCT * FROM unnest($1::text[],$2::text[],$3::text[])) k(et,eid,f)
		  ON t.entity_type=k.et AND t.entity_id=k.eid AND t.field=k.f
		WHERE t.lang=ANY($4::text[]) AND t.reviewed_value IS NOT NULL`, ets, ids, fs, langs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r i18n.Ref
		var lang, v string
		if err = rows.Scan(&r.EntityType, &r.EntityID, &r.Field, &lang, &v); err != nil {
			return nil, err
		}
		set.Add(r, lang, v)
	}
	return set, rows.Err()
}

// ---- admin workbench ----

// StatusMissing is the task status of a source field with no translation
// row in the requested language.
const StatusMissing = "missing"

// TranslationTask is one row of the translation workbench: a source field
// and its translation in one language (Status "missing" when there is none).
type TranslationTask struct {
	EntityType    string     `json:"entity_type"`
	EntityID      string     `json:"entity_id"`
	Field         string     `json:"field"`
	Lang          string     `json:"lang"`
	Source        *string    `json:"source"` // current source text, null if the entity is gone
	ID            *int64     `json:"id"`
	Value         *string    `json:"value"`
	Status        string     `json:"status"`
	ReviewedValue *string    `json:"reviewed_value"` // the text served publicly
	Submitted     bool       `json:"submitted"`      // draft awaiting review
	Outdated      bool       `json:"outdated"`       // written against an older source
	SubmittedAt   *time.Time `json:"submitted_at"`
	ReviewedAt    *time.Time `json:"reviewed_at"`
	UpdatedBy     *string    `json:"updated_by"`
	UpdatedAt     *time.Time `json:"updated_at"`
}

// TaskFilter selects translation tasks. Missing lists source fields without
// a translation in Lang (Lang required, Status ignored).
type TaskFilter struct {
	Lang       string
	Status     string
	EntityType string
	Missing    bool
	Cursor     *Cursor
	Limit      int
}

const taskSort = "i18n_task"

type taskKey struct {
	EntityType, EntityID, Field, Lang string
}

// DecodeTaskCursor parses a cursor produced by TranslationTasks.
func DecodeTaskCursor(raw string) (*Cursor, error) {
	c, err := DecodeCursor(raw)
	if err != nil {
		return nil, err
	}
	var k taskKey
	if c.Sort != taskSort || json.Unmarshal([]byte(c.Key), &k) != nil {
		return nil, invalidQuery("malformed cursor")
	}
	return &c, nil
}

const taskColumns = `t.id,t.value,t.status,t.reviewed_value,t.submitted_at IS NOT NULL,
	t.source_hash<>i18n_hash(src),t.submitted_at,t.reviewed_at,t.updated_by,t.updated_at`

// buildTaskQuery renders f as SQL selecting limit+1 tasks in
// (entity_type, entity_id, field, lang) order.
func buildTaskQuery(f TaskFilter) (string, []any, int, error) {
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	var k taskKey
	if f.Cursor != nil {
		if f.Cursor.Sort != taskSort || json.Unmarshal([]byte(f.Cursor.Key), &k) != nil {
			return "", nil, 0, invalidQuery("malformed cursor")
		}
	}
	if f.Missing {
		if f.Lang == "" {
			return "", nil, 0, invalidQuery("missing=true needs lang")
		}
		lp := b.arg(f.Lang)
		b.cond("NOT EXISTS (SELECT 1 FROM translations t WHERE t.entity_type=s.entity_type AND t.entity_id=s.entity_id AND t.field=s.field AND t.lang=" + lp + ")")
		if f.EntityType != "" {
			b.cond("s.entity_type=" + b.arg(f.EntityType))
		}
		if f.Cursor != nil {
			b.cond(fmt.Sprintf("(s.entity_type,s.entity_id,s.field)>(%s,%s,%s)", b.arg(k.EntityType), b.arg(k.EntityID), b.arg(k.Field)))
		}
		sql := `SELECT s.entity_type,s.entity_id,s.field,` + lp + `::text,s.value FROM i18n_sources s` + b.whereSQL() +
			` ORDER BY s.entity_type,s.entity_id,s.field LIMIT ` + b.arg(limit+1)
		return sql, b.args, limit, nil
	}
	if f.Lang != "" {
		b.cond("t.lang=" + b.arg(f.Lang))
	}
	if f.Status != "" {
		b.cond("t.status=" + b.arg(f.Status))
	}
	if f.EntityType != "" {
		b.cond("t.entity_type=" + b.arg(f.EntityType))
	}
	if f.Cursor != nil {
		b.cond(fmt.Sprintf("(t.entity_type,t.entity_id,t.field,t.lang)>(%s,%s,%s,%s)", b.arg(k.EntityType), b.arg(k.EntityID), b.arg(k.Field), b.arg(k.Lang)))
	}
	sql := `SELECT t.entity_type,t.entity_id,t.field,t.lang,src,` + taskColumns + `
		FROM translations t CROSS JOIN LATERAL i18n_source(t.entity_type,t.entity_id,t.field) src` + b.whereSQL() +
		` ORDER BY t.entity_type,t.entity_id,t.field,t.lang LIMIT ` + b.arg(limit+1)
	return sql, b.args, limit, nil
}

// TranslationTasks returns one page of the translation workbench.
func (s *Store) TranslationTasks(ctx context.Context, f TaskFilter) ([]TranslationTask, string, error) {
	sql, args, limit, err := buildTaskQuery(f)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []TranslationTask{}
	for rows.Next() {
		var t TranslationTask
		if f.Missing {
			err = rows.Scan(&t.EntityType, &t.EntityID, &t.Field, &t.Lang, &t.Source)
			t.Status = StatusMissing
		} else {
			err = rows.Scan(&t.EntityType, &t.EntityID, &t.Field, &t.Lang, &t.Source, &t.ID, &t.Value, &t.Status, &t.ReviewedValue,
				&t.Submitted, &t.Outdated, &t.SubmittedAt, &t.ReviewedAt, &t.UpdatedBy, &t.UpdatedAt)
		}
		if err != nil {
			return nil, "", err
		}
		out = append(out, t)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		key, _ := json.Marshal(taskKey{last.EntityType, last.EntityID, last.Field, last.Lang})
		next = EncodeCursor(Cursor{Sort: taskSort, Key: string(key), ID: 1})
	}
	return out, next, nil
}

func (s *Store) translationTask(ctx context.Context, q querier, ref i18n.Ref, lang string) (TranslationTask, error) {
	rows, err := q.Query(ctx, `SELECT t.entity_type,t.entity_id,t.field,t.lang,src,`+taskColumns+`
		FROM translations t CROSS JOIN LATERAL i18n_source(t.entity_type,t.entity_id,t.field) src
		WHERE t.entity_type=$1 AND t.entity_id=$2 AND t.field=$3 AND t.lang=$4`, ref.EntityType, ref.EntityID, ref.Field, lang)
	if err != nil {
		return TranslationTask{}, err
	}
	t, err := pgx.CollectExactlyOneRow(rows, func(r pgx.CollectableRow) (TranslationTask, error) {
		var t TranslationTask
		err := r.Scan(&t.EntityType, &t.EntityID, &t.Field, &t.Lang, &t.Source, &t.ID, &t.Value, &t.Status, &t.ReviewedValue,
			&t.Submitted, &t.Outdated, &t.SubmittedAt, &t.ReviewedAt, &t.UpdatedBy, &t.UpdatedAt)
		return t, err
	})
	return t, err
}

// UpsertTranslation saves value as an unsubmitted draft of ref in lang,
// recording the current source hash. A previously reviewed value stays
// public until the draft is reviewed. A missing entity or empty source
// field is pgx.ErrNoRows.
func (s *Store) UpsertTranslation(ctx context.Context, ref i18n.Ref, lang, value, by string) (TranslationTask, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return TranslationTask{}, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,updated_by)
		SELECT $1,$2,$3,$4,$5,'draft',i18n_hash(src),NULLIF($6,'') FROM i18n_source($1,$2,$3) src WHERE COALESCE(src,'')<>''
		ON CONFLICT(entity_type,entity_id,field,lang) DO UPDATE SET value=EXCLUDED.value,status='draft',
		source_hash=EXCLUDED.source_hash,submitted_at=NULL,updated_by=EXCLUDED.updated_by,updated_at=now()`,
		ref.EntityType, ref.EntityID, ref.Field, lang, value, by)
	if err != nil {
		return TranslationTask{}, err
	}
	if tag.RowsAffected() == 0 {
		return TranslationTask{}, pgx.ErrNoRows
	}
	t, err := s.translationTask(ctx, tx, ref, lang)
	if err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

// TransitionTranslation applies a workflow action (i18n.ActionSubmit or
// i18n.ActionReview) to the translation of ref in lang. A missing row is
// pgx.ErrNoRows; a disallowed transition is i18n.ErrTransition.
func (s *Store) TransitionTranslation(ctx context.Context, ref i18n.Ref, lang, action, by string) (TranslationTask, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return TranslationTask{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var status, current string
	var submitted, matches bool
	err = tx.QueryRow(ctx, `SELECT id,status,submitted_at IS NOT NULL,i18n_hash(src),source_hash=i18n_hash(src)
		FROM translations CROSS JOIN LATERAL i18n_source(entity_type,entity_id,field) src
		WHERE entity_type=$1 AND entity_id=$2 AND field=$3 AND lang=$4 FOR UPDATE OF translations`,
		ref.EntityType, ref.EntityID, ref.Field, lang).Scan(&id, &status, &submitted, &current, &matches)
	if err != nil {
		return TranslationTask{}, err
	}
	next, err := i18n.Next(status, submitted, action, matches)
	if err != nil {
		return TranslationTask{}, err
	}
	switch action {
	case i18n.ActionSubmit:
		_, err = tx.Exec(ctx, `UPDATE translations SET status=$2,submitted_at=COALESCE(submitted_at,now()),updated_by=COALESCE(NULLIF($3,''),updated_by),updated_at=now() WHERE id=$1`, id, next, by)
	case i18n.ActionReview:
		// Re-confirming a stale translation attests it matches the new source.
		_, err = tx.Exec(ctx, `UPDATE translations SET status=$2,reviewed_value=value,reviewed_at=now(),submitted_at=NULL,
			source_hash=CASE WHEN $5 THEN $4 ELSE source_hash END,updated_by=COALESCE(NULLIF($3,''),updated_by),updated_at=now()
			WHERE id=$1`, id, next, by, current, status == i18n.StatusStale)
	default:
		err = i18n.ErrTransition
	}
	if err != nil {
		return TranslationTask{}, err
	}
	t, err := s.translationTask(ctx, tx, ref, lang)
	if err != nil {
		return t, err
	}
	return t, tx.Commit(ctx)
}

// TranslationStats counts translations by language and status; "missing"
// is the number of source fields with no row in that language.
func (s *Store) TranslationStats(ctx context.Context) (map[string]any, error) {
	var total int64
	if err := s.QueryRow(ctx, `SELECT count(*) FROM i18n_sources`).Scan(&total); err != nil {
		return nil, err
	}
	byLang := map[string]map[string]int64{}
	for _, l := range i18n.Languages {
		m := map[string]int64{StatusMissing: total}
		for _, st := range i18n.Statuses {
			m[st] = 0
		}
		byLang[l] = m
	}
	rows, err := s.Query(ctx, `SELECT lang,status,count(*),count(*) FILTER (WHERE i18n_source(entity_type,entity_id,field) IS NOT NULL)
		FROM translations GROUP BY lang,status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var lang, status string
		var n, live int64
		if err = rows.Scan(&lang, &status, &n, &live); err != nil {
			return nil, err
		}
		if m := byLang[lang]; m != nil {
			m[status] = n
			m[StatusMissing] -= live
		}
	}
	return map[string]any{"total_fields": total, "by_lang": byLang}, rows.Err()
}

// ---- glossary ----

// GlossaryEntry is one glossary_terms row.
type GlossaryEntry struct {
	ID          int64     `json:"id"`
	Term        string    `json:"term"`
	Lang        string    `json:"lang"`
	Translation string    `json:"translation"`
	Note        string    `json:"note"`
	UpdatedBy   *string   `json:"updated_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

const glossaryColumns = `id,term,lang,translation,note,updated_by,updated_at`

func scanGlossary(r pgx.CollectableRow) (GlossaryEntry, error) {
	var g GlossaryEntry
	err := r.Scan(&g.ID, &g.Term, &g.Lang, &g.Translation, &g.Note, &g.UpdatedBy, &g.UpdatedAt)
	return g, err
}

// GlossaryTerms lists glossary entries, optionally by lang and a term/
// translation substring, ordered by term.
func (s *Store) GlossaryTerms(ctx context.Context, lang, q string, limit int) ([]GlossaryEntry, error) {
	rows, err := s.Query(ctx, `SELECT `+glossaryColumns+` FROM glossary_terms
		WHERE ($1='' OR lang=$1) AND ($2='' OR term ILIKE '%'||$2||'%' OR translation ILIKE '%'||$2||'%')
		ORDER BY term,lang LIMIT $3`, lang, q, pageLimit(limit))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanGlossary)
}

// UpsertGlossaryTerm creates or replaces the entry for (term, lang).
func (s *Store) UpsertGlossaryTerm(ctx context.Context, g GlossaryEntry, by string) (GlossaryEntry, error) {
	rows, err := s.Query(ctx, `INSERT INTO glossary_terms(term,lang,translation,note,updated_by) VALUES($1,$2,$3,$4,NULLIF($5,''))
		ON CONFLICT(term,lang) DO UPDATE SET translation=EXCLUDED.translation,note=EXCLUDED.note,updated_by=EXCLUDED.updated_by,updated_at=now()
		RETURNING `+glossaryColumns, g.Term, g.Lang, g.Translation, g.Note, by)
	if err != nil {
		return GlossaryEntry{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanGlossary)
}

// UpdateGlossaryTerm rewrites entry id. A missing id is pgx.ErrNoRows; a
// (term, lang) clash with another entry is a unique violation.
func (s *Store) UpdateGlossaryTerm(ctx context.Context, id int64, g GlossaryEntry, by string) (GlossaryEntry, error) {
	rows, err := s.Query(ctx, `UPDATE glossary_terms SET term=$2,lang=$3,translation=$4,note=$5,updated_by=NULLIF($6,''),updated_at=now()
		WHERE id=$1 RETURNING `+glossaryColumns, id, g.Term, g.Lang, g.Translation, g.Note, by)
	if err != nil {
		return GlossaryEntry{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanGlossary)
}

// DeleteGlossaryTerm removes entry id; a missing id is pgx.ErrNoRows.
func (s *Store) DeleteGlossaryTerm(ctx context.Context, id int64) error {
	tag, err := s.Exec(ctx, `DELETE FROM glossary_terms WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// ---- UI strings ----

// UIString is one ui_strings row.
type UIString struct {
	Key       string    `json:"key"`
	Lang      string    `json:"lang"`
	Value     string    `json:"value"`
	Status    string    `json:"status"`
	UpdatedBy *string   `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

const uiColumns = `key,lang,value,status,updated_by,updated_at`

func scanUI(r pgx.CollectableRow) (UIString, error) {
	var u UIString
	err := r.Scan(&u.Key, &u.Lang, &u.Value, &u.Status, &u.UpdatedBy, &u.UpdatedAt)
	return u, err
}

// UIStrings lists UI strings for the admin, optionally by lang, status and
// a key prefix, ordered by key.
func (s *Store) UIStrings(ctx context.Context, lang, status, prefix string) ([]UIString, error) {
	rows, err := s.Query(ctx, `SELECT `+uiColumns+` FROM ui_strings
		WHERE ($1='' OR lang=$1) AND ($2='' OR status=$2) AND ($3='' OR starts_with(key,$3))
		ORDER BY key,lang LIMIT 5000`, lang, status, prefix)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanUI)
}

// UpsertUIString creates or replaces (key, lang).
func (s *Store) UpsertUIString(ctx context.Context, u UIString, by string) (UIString, error) {
	rows, err := s.Query(ctx, `INSERT INTO ui_strings(key,lang,value,status,updated_by) VALUES($1,$2,$3,$4,NULLIF($5,''))
		ON CONFLICT(key,lang) DO UPDATE SET value=EXCLUDED.value,status=EXCLUDED.status,updated_by=EXCLUDED.updated_by,updated_at=now()
		RETURNING `+uiColumns, u.Key, u.Lang, u.Value, u.Status, by)
	if err != nil {
		return UIString{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanUI)
}

// PublicUIStrings returns the reviewed UI strings of lang as key -> value.
func (s *Store) PublicUIStrings(ctx context.Context, lang string) (map[string]string, error) {
	rows, err := s.Query(ctx, `SELECT key,value FROM ui_strings WHERE lang=$1 AND status='reviewed'`, lang)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}
