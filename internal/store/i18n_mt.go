package store

// Machine translation storage (migration 052, internal/mtjob): the admin
// request queue, machine drafts with provenance and the shared daily
// request budget. Machine output is only ever written where no human row
// exists (no translation, or an unedited machine draft that was never
// reviewed); it is never public until submitted and reviewed.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/mtjob"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// JobKindMachineTranslation is jobs.kind of the machine translation job.
const JobKindMachineTranslation = mtjob.JobKind

// MTProviderOpenRouter is mt_usage.provider of the OpenRouter translator.
const MTProviderOpenRouter = "openrouter"

// mtEligible is true when the job may write the translation of (et, eid,
// f, lang): no row, or a machine draft that was never reviewed.
const mtEligible = `NOT EXISTS (SELECT 1 FROM translations t WHERE t.entity_type=%[1]s.entity_type AND t.entity_id=%[1]s.entity_id
	AND t.field=%[1]s.field AND t.lang=%[1]s.lang AND (t.status<>'machine' OR t.reviewed_value IS NOT NULL))`

func mtEligibleFor(alias string) string {
	return fmt.Sprintf(mtEligible, alias)
}

// QueueMachineTranslations records an admin request to machine-translate
// refs into lang. Fields that have a human translation (draft, reviewed,
// stale) or no source text are skipped; queued is how many were (re)queued.
// A field already queued is re-armed (attempts reset).
func (s *Store) QueueMachineTranslations(ctx context.Context, lang string, refs []i18n.Ref, by string) (queued int, err error) {
	if len(refs) == 0 {
		return 0, nil
	}
	ets, ids, fs := make([]string, len(refs)), make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		ets[i], ids[i], fs[i] = r.EntityType, r.EntityID, r.Field
	}
	tag, err := s.Exec(ctx, `INSERT INTO mt_requests(entity_type,entity_id,field,lang,requested_by)
		SELECT k.entity_type,k.entity_id,k.field,k.lang,NULLIF($5,'')
		FROM (SELECT DISTINCT et AS entity_type, eid AS entity_id, f AS field, $4::text AS lang
		        FROM unnest($1::text[],$2::text[],$3::text[]) u(et,eid,f)) k
		WHERE COALESCE(btrim(i18n_source(k.entity_type,k.entity_id,k.field)),'')<>'' AND `+mtEligibleFor("k")+`
		ON CONFLICT (entity_type,entity_id,field,lang) DO UPDATE SET requested_by=EXCLUDED.requested_by,
			requested_at=now(),attempts=0,next_try_at=now(),last_error=NULL`, ets, ids, fs, lang, by)
	return int(tag.RowsAffected()), err
}

// DueMTRequests implements mtjob.Store.
func (s *Store) DueMTRequests(ctx context.Context, now time.Time, limit int) ([]mtjob.Item, error) {
	if _, err := s.Exec(ctx, `DELETE FROM mt_requests q WHERE COALESCE(btrim(i18n_source(q.entity_type,q.entity_id,q.field)),'')=''
		OR NOT (`+mtEligibleFor("q")+`)`); err != nil {
		return nil, err
	}
	rows, err := s.Query(ctx, `SELECT q.entity_type,q.entity_id,q.field,q.lang,i18n_source(q.entity_type,q.entity_id,q.field)
		,q.requested_by IS DISTINCT FROM 'auto'
		FROM mt_requests q WHERE q.next_try_at<=GREATEST($1,now())
		ORDER BY q.requested_by IS NOT DISTINCT FROM 'auto',q.requested_at,q.entity_type,q.entity_id,q.field,q.lang LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (mtjob.Item, error) {
		var it mtjob.Item
		err := r.Scan(&it.Ref.EntityType, &it.Ref.EntityID, &it.Ref.Field, &it.Lang, &it.Source, &it.Requested)
		return it, err
	})
}

// MissingReleaseTitles implements mtjob.Store: published, unmerged releases
// with a title and no translation row in a language, skipping fields with a
// request row (an admin request, or a background failure backing off).
func (s *Store) MissingReleaseTitles(ctx context.Context, langs []string, limit int) ([]mtjob.Item, error) {
	if len(langs) == 0 || limit <= 0 {
		return nil, nil
	}
	rows, err := s.Query(ctx, `SELECT r.id::text,l.lang,r.canonical_title
		FROM releases r CROSS JOIN unnest($1::text[]) WITH ORDINALITY l(lang,ord)
		WHERE r.status='published' AND r.merged_into_id IS NULL AND btrim(COALESCE(r.canonical_title,''))<>''
		  AND NOT EXISTS (SELECT 1 FROM translations t WHERE t.entity_type='release' AND t.entity_id=r.id::text AND t.field='title' AND t.lang=l.lang)
		  AND NOT EXISTS (SELECT 1 FROM mt_requests q WHERE q.entity_type='release' AND q.entity_id=r.id::text AND q.field='title' AND q.lang=l.lang)
		ORDER BY r.release_date DESC NULLS LAST, r.id DESC, l.ord LIMIT $2`, langs, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (mtjob.Item, error) {
		it := mtjob.Item{Ref: i18n.Ref{EntityType: i18n.EntityRelease, Field: "title"}}
		err := r.Scan(&it.Ref.EntityID, &it.Lang, &it.Source)
		return it, err
	})
}

// MTGlossary implements mtjob.Store (at most 2000 terms of lang).
func (s *Store) MTGlossary(ctx context.Context, lang string) ([]i18n.GlossaryTerm, error) {
	rows, err := s.Query(ctx, `SELECT term,lang,translation FROM glossary_terms WHERE lang=$1 ORDER BY term LIMIT 2000`, lang)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (i18n.GlossaryTerm, error) {
		var g i18n.GlossaryTerm
		err := r.Scan(&g.Term, &g.Lang, &g.Translation)
		return g, err
	})
}

// SaveMachineTranslation implements mtjob.Store. The row is written only
// while the source still equals it.Source and no human row exists; either
// way the request row is cleared.
func (s *Store) SaveMachineTranslation(ctx context.Context, it mtjob.Item, value, model string, at time.Time) (bool, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,updated_by,mt_model,mt_at)
		SELECT $1,$2,$3,$4,$5,'machine',i18n_hash(src),'machine',$6,$7 FROM i18n_source($1,$2,$3) src WHERE src=$8
		ON CONFLICT (entity_type,entity_id,field,lang) DO UPDATE SET value=EXCLUDED.value,source_hash=EXCLUDED.source_hash,
			updated_by=EXCLUDED.updated_by,mt_model=EXCLUDED.mt_model,mt_at=EXCLUDED.mt_at,submitted_at=NULL,updated_at=now()
		WHERE translations.status='machine' AND translations.reviewed_value IS NULL`,
		it.Ref.EntityType, it.Ref.EntityID, it.Ref.Field, it.Lang, value, model, at, it.Source)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM mt_requests WHERE entity_type=$1 AND entity_id=$2 AND field=$3 AND lang=$4`,
		it.Ref.EntityType, it.Ref.EntityID, it.Ref.Field, it.Lang); err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, tx.Commit(ctx)
}

// FailMTRequest implements mtjob.Store: attempts+1 and next_try_at per
// mtjob.Backoff (1 h, 6 h, then 30 days). A background item gets a request
// row with requested_by 'auto' so the fill skips it while it backs off.
func (s *Store) FailMTRequest(ctx context.Context, it mtjob.Item, reason string) error {
	by := "auto"
	if it.Requested {
		by = ""
	}
	_, err := s.Exec(ctx, `INSERT INTO mt_requests(entity_type,entity_id,field,lang,requested_by,attempts,next_try_at,last_error)
		VALUES($1,$2,$3,$4,NULLIF($5,''),1,now()+$6*interval '1 second',$7)
		ON CONFLICT (entity_type,entity_id,field,lang) DO UPDATE SET attempts=mt_requests.attempts+1,last_error=EXCLUDED.last_error,
			next_try_at=now()+CASE WHEN mt_requests.attempts+1<=1 THEN $6 WHEN mt_requests.attempts+1<$8 THEN $9 ELSE $10 END*interval '1 second'`,
		it.Ref.EntityType, it.Ref.EntityID, it.Ref.Field, it.Lang, by, int64(mtjob.Backoff(1).Seconds()), reason,
		mtjob.MaxAttempts, int64(mtjob.Backoff(2).Seconds()), int64(mtjob.Backoff(mtjob.MaxAttempts).Seconds()))
	return err
}

// ReserveMTRequest takes one request from provider's budget for day (UTC
// date), false when cap requests were already made that day.
func (s *Store) ReserveMTRequest(ctx context.Context, provider string, day time.Time, cap int) (bool, error) {
	var n int
	err := s.QueryRow(ctx, `INSERT INTO mt_usage(day,provider,requests) VALUES($1::date,$2,1)
		ON CONFLICT (day,provider) DO UPDATE SET requests=mt_usage.requests+1 WHERE mt_usage.requests<$3
		RETURNING requests`, day.UTC().Format(time.DateOnly), provider, cap).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err == nil && n > cap { // a first insert with cap < 1
		return false, nil
	}
	return err == nil, err
}

// MTBudget is provider's daily request budget as an i18n.RequestBudget.
func (s *Store) MTBudget(provider string) i18n.RequestBudget { return mtBudget{s, provider} }

type mtBudget struct {
	s        *Store
	provider string
}

func (b mtBudget) ReserveRequest(ctx context.Context, day time.Time, cap int) (bool, error) {
	return b.s.ReserveMTRequest(ctx, b.provider, day, cap)
}

// MTStatus is the machine translation summary for the admin workbench.
type MTStatus struct {
	UsedToday int `json:"used_today"` // requests made this UTC day
	Pending   int `json:"pending"`    // admin requests waiting (due or backing off)
	Failing   int `json:"failing"`    // requests with at least one failed attempt
}

// MachineTranslationStatus returns provider's usage on day and the queue.
func (s *Store) MachineTranslationStatus(ctx context.Context, provider string, day time.Time) (MTStatus, error) {
	var st MTStatus
	err := s.QueryRow(ctx, `SELECT
		COALESCE((SELECT requests FROM mt_usage WHERE day=$1::date AND provider=$2),0),
		(SELECT count(*) FROM mt_requests WHERE requested_by IS DISTINCT FROM 'auto'),
		(SELECT count(*) FROM mt_requests WHERE attempts>0)`, day.UTC().Format(time.DateOnly), provider).Scan(&st.UsedToday, &st.Pending, &st.Failing)
	return st, err
}
