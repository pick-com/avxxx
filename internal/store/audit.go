package store

// PostgreSQL side of the data-quality audit (internal/audit): the
// audit.Store implementation, the admin issue list / dismiss, the
// AdminDashboard counts, and the non-crawl job queue (kind='audit').

import (
	"avxxx/internal/audit"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var _ audit.Store = (*Store)(nil)

// JobKindAudit is jobs.kind for audit runs (migrations/013_audit.sql).
const JobKindAudit = "audit"

type performerJSON struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	BirthDate string `json:"birth_date"`
}

const releaseFactsSelect = `SELECT r.id,r.canonical_title,r.status,r.release_date,COALESCE(r.runtime_minutes,0),
	(SELECT count(*) FROM watch_history w WHERE w.release_id=r.id),
	COALESCE((SELECT json_agg(json_build_object('id',p.id,'name',p.canonical_name,'birth_date',p.birth_date) ORDER BY rp.billing_order NULLS LAST,p.id)
		FROM release_performers rp JOIN performers p0 ON p0.id=rp.performer_id
		JOIN performers p ON p.id=COALESCE(p0.merged_into_id,p0.id)
		WHERE rp.release_id=r.id),'[]'::json)
	FROM releases r`

func scanReleaseFacts(row pgx.Row) (audit.ReleaseFacts, error) {
	var x audit.ReleaseFacts
	var raw []byte
	if err := row.Scan(&x.ID, &x.Title, &x.Status, &x.ReleaseDate, &x.RuntimeMinutes, &x.Popularity, &raw); err != nil {
		return x, err
	}
	var ps []performerJSON
	if err := json.Unmarshal(raw, &ps); err != nil {
		return x, err
	}
	seen := map[int64]bool{}
	for _, p := range ps {
		if seen[p.ID] { // two credits merged into the same performer
			continue
		}
		seen[p.ID] = true
		f := audit.PerformerFacts{ID: p.ID, Name: p.Name}
		if p.BirthDate != "" {
			t, err := time.Parse(time.DateOnly, p.BirthDate)
			if err != nil {
				return x, fmt.Errorf("performer %d birth_date: %w", p.ID, err)
			}
			f.BirthDate = &t
		}
		x.Performers = append(x.Performers, f)
	}
	return x, nil
}

// PublishedReleases implements audit.Store.
func (s *Store) PublishedReleases(ctx context.Context, afterID int64, limit int) ([]audit.ReleaseFacts, error) {
	rows, err := s.Query(ctx, releaseFactsSelect+` WHERE r.status='published' AND r.merged_into_id IS NULL AND r.id>$1 ORDER BY r.id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.ReleaseFacts
	for rows.Next() {
		x, err := scanReleaseFacts(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// ReleaseFacts implements audit.Store.
func (s *Store) ReleaseFacts(ctx context.Context, id int64) (audit.ReleaseFacts, error) {
	return scanReleaseFacts(s.QueryRow(ctx, releaseFactsSelect+` WHERE r.id=$1`, id))
}

// HoldRelease implements audit.Store.
func (s *Store) HoldRelease(ctx context.Context, releaseID int64, reason string, detail map[string]any) (bool, error) {
	after := map[string]any{"status": audit.HoldStatus}
	for k, v := range detail {
		after[k] = v
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return false, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var before string
	if err = tx.QueryRow(ctx, `SELECT status FROM releases WHERE id=$1 FOR UPDATE`, releaseID).Scan(&before); err != nil {
		return false, err
	}
	switch before {
	case "published", "draft", "upcoming":
	default:
		return false, nil // already held, or in a status the audit must not touch
	}
	if _, err = tx.Exec(ctx, `UPDATE releases SET status=$2,updated_at=now(),version=version+1 WHERE id=$1`, releaseID, audit.HoldStatus); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES('release',$1,'hold',jsonb_build_object('status',$2::text),$3::jsonb,$4,$5)`, releaseID, before, string(afterJSON), reason, audit.Actor); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// OpenIssue implements audit.Store.
func (s *Store) OpenIssue(ctx context.Context, is audit.Issue) (audit.OpenResult, error) {
	var dismissed bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_issues WHERE rule=$1 AND entity_type=$2 AND entity_id=$3 AND fingerprint=$4 AND status='dismissed')`,
		string(is.Rule), is.EntityType, is.EntityID, is.Fingerprint).Scan(&dismissed); err != nil {
		return 0, err
	}
	if dismissed {
		return audit.IssueSuppressed, nil
	}
	detail, err := json.Marshal(is.Detail)
	if err != nil {
		return 0, err
	}
	seen := is.LastSeenAt
	if seen.IsZero() {
		seen = time.Now()
	}
	var inserted bool
	err = s.QueryRow(ctx, `INSERT INTO audit_issues(rule,entity_type,entity_id,severity,detail,fingerprint,priority,last_seen_at)
		VALUES($1,$2,$3,$4,$5::jsonb,$6,$7,$8)
		ON CONFLICT (rule,entity_type,entity_id) WHERE status='open' DO UPDATE SET severity=EXCLUDED.severity,detail=EXCLUDED.detail,
		fingerprint=EXCLUDED.fingerprint,priority=EXCLUDED.priority,last_seen_at=EXCLUDED.last_seen_at
		RETURNING (xmax=0)`, string(is.Rule), is.EntityType, is.EntityID, string(is.Severity), string(detail), is.Fingerprint, is.Priority, seen).Scan(&inserted)
	if err != nil {
		return 0, err
	}
	if inserted {
		return audit.IssueCreated, nil
	}
	return audit.IssueRefreshed, nil
}

// ResolveStale implements audit.Store.
func (s *Store) ResolveStale(ctx context.Context, rule audit.Rule, before time.Time) (int, error) {
	tag, err := s.Exec(ctx, `UPDATE audit_issues SET status='fixed',resolved_at=now(),resolution_note='no longer detected'
		WHERE rule=$1 AND status='open' AND last_seen_at<$2`, string(rule), before)
	return int(tag.RowsAffected()), err
}

// whitespacePattern prefilters values with leading/trailing, repeated or
// non-space whitespace (ideographic space and NBSP included explicitly).
const whitespacePattern = `^[\s　 ]|[\s　 ]$|[\s　 ]{2}|[\t\n\r\f\v ]`

// TextFieldsNeedingTrim implements audit.Store.
func (s *Store) TextFieldsNeedingTrim(ctx context.Context, limit int) ([]audit.TextField, error) {
	rows, err := s.Query(ctx, `SELECT * FROM (
		SELECT 'release',id,'canonical_title',canonical_title FROM releases WHERE merged_into_id IS NULL AND canonical_title ~ $1
		UNION ALL SELECT 'release',id,'title_ja',title_ja FROM releases WHERE merged_into_id IS NULL AND title_ja ~ $1
		UNION ALL SELECT 'performer',id,'canonical_name',canonical_name FROM performers WHERE merged_into_id IS NULL AND canonical_name ~ $1
		UNION ALL SELECT 'performer',id,'canonical_name_ja',canonical_name_ja FROM performers WHERE merged_into_id IS NULL AND canonical_name_ja ~ $1
		) x LIMIT $2`, whitespacePattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []audit.TextField
	for rows.Next() {
		var f audit.TextField
		if err = rows.Scan(&f.EntityType, &f.EntityID, &f.Field, &f.Value); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// trimmableColumns is the allowlist FixTextField may write (table, column).
var trimmableColumns = map[string][2]string{
	"release.canonical_title":     {"releases", "canonical_title"},
	"release.title_ja":            {"releases", "title_ja"},
	"performer.canonical_name":    {"performers", "canonical_name"},
	"performer.canonical_name_ja": {"performers", "canonical_name_ja"},
}

// FixTextField implements audit.Store.
func (s *Store) FixTextField(ctx context.Context, f audit.TextField, value string) (bool, error) {
	col, ok := trimmableColumns[f.EntityType+"."+f.Field]
	if !ok {
		return false, fmt.Errorf("field is not auto-fixable: %s.%s", f.EntityType, f.Field)
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s=$1,updated_at=now() WHERE id=$2 AND %s=$3`, col[0], col[1], col[1]), value, f.EntityID, f.Value)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil // changed since it was read
	}
	if _, err = tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES($1,$2,'auto_fix',jsonb_build_object($3::text,$4::text),jsonb_build_object($3::text,$5::text),$6,$7)`,
		f.EntityType, f.EntityID, f.Field, f.Value, value, "R-TITLE: whitespace normalized", audit.Actor); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// RecordRun implements audit.Store.
func (s *Store) RecordRun(ctx context.Context, r audit.Run) error {
	detail, err := json.Marshal(r.Detail)
	if err != nil {
		return err
	}
	if r.Detail == nil {
		detail = []byte("{}")
	}
	_, err = s.Exec(ctx, `INSERT INTO nightly_runs(job,job_id,started_at,duration_ms,checked,fixed,issues,result,detail)
		VALUES($1,NULLIF($2,0),$3,$4,$5,$6,$7,$8,$9::jsonb)`, r.Job, r.JobID, r.StartedAt, r.DurationMS, r.Checked, r.Fixed, r.Issues, r.Result, string(detail))
	return err
}

// ---------------------------------------------------------------- admin

// AuditIssues lists issues by priority (popularity) then recency, keyset
// paginated. It returns the next cursor, or "" on the last page.
func (s *Store) AuditIssues(ctx context.Context, f audit.IssueFilter) ([]audit.Issue, string, error) {
	limit := pageLimit(f.Limit)
	status := f.Status
	if status == "" {
		status = audit.StatusOpen
	}
	var hasCursor bool
	var curPriority, curID int64
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		if c.Sort != "audit" {
			return nil, "", invalidQuery("cursor does not match this list")
		}
		if curPriority, err = strconv.ParseInt(c.Key, 10, 64); err != nil {
			return nil, "", invalidQuery("malformed cursor")
		}
		hasCursor, curID = true, c.ID
	}
	rows, err := s.Query(ctx, `SELECT id,rule,entity_type,entity_id,severity,detail,priority,status,created_at,last_seen_at,resolved_at
		FROM audit_issues WHERE ($1='' OR rule=$1) AND status=$2 AND (NOT $3 OR (priority,id)<($4,$5))
		ORDER BY priority DESC,id DESC LIMIT $6`, string(f.Rule), status, hasCursor, curPriority, curID, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []audit.Issue{}
	for rows.Next() {
		var x audit.Issue
		var rule, sev string
		var detail []byte
		if err = rows.Scan(&x.ID, &rule, &x.EntityType, &x.EntityID, &sev, &detail, &x.Priority, &x.Status, &x.CreatedAt, &x.LastSeenAt, &x.ResolvedAt); err != nil {
			return nil, "", err
		}
		x.Rule, x.Severity = audit.Rule(rule), audit.Severity(sev)
		if err = json.Unmarshal(detail, &x.Detail); err != nil {
			return nil, "", err
		}
		out = append(out, x)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = EncodeCursor(Cursor{Sort: "audit", Key: strconv.FormatInt(last.Priority, 10), ID: last.ID})
	}
	return out, next, nil
}

// DismissAuditIssue marks an open issue dismissed and records who/why in
// review_events. A dismissed finding is not raised again while its data is
// unchanged. It does not unhold a held release. Missing issue: pgx.ErrNoRows;
// already fixed or dismissed: audit.ErrIssueNotOpen.
func (s *Store) DismissAuditIssue(ctx context.Context, id int64, note string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var rule, entityType string
	var entityID int64
	err = tx.QueryRow(ctx, `UPDATE audit_issues SET status='dismissed',resolved_at=now(),resolution_note=NULLIF($2,'')
		WHERE id=$1 AND status='open' RETURNING rule,entity_type,entity_id`, id, note).Scan(&rule, &entityType, &entityID)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_issues WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return audit.ErrIssueNotOpen
		}
		return pgx.ErrNoRows
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,after_value,reason,actor)
		VALUES($1,$2,'audit_dismiss',jsonb_build_object('issue_id',$3::bigint,'rule',$4::text),NULLIF($5,''),'admin')`,
		entityType, entityID, id, rule, note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AdminDashboard returns the AdminDashboard (A1) overview counts.
func (s *Store) AdminDashboard(ctx context.Context) (audit.Dashboard, error) {
	d := audit.NewDashboard(time.Now().UTC())
	type group struct {
		sql  string
		scan func(pgx.Rows) error
	}
	groups := []group{
		{`SELECT status,count(*) FROM releases WHERE merged_into_id IS NULL GROUP BY status`, func(r pgx.Rows) error {
			var st string
			var n int64
			if err := r.Scan(&st, &n); err != nil {
				return err
			}
			d.Releases.ByStatus[st] = n
			d.Releases.Total += n
			if st == audit.HoldStatus {
				d.Releases.Held = n
			}
			return nil
		}},
		{`SELECT status,count(*) FROM performers WHERE merged_into_id IS NULL GROUP BY status`, func(r pgx.Rows) error {
			var st string
			var n int64
			if err := r.Scan(&st, &n); err != nil {
				return err
			}
			d.Performers.ByStatus[st] = n
			d.Performers.Total += n
			return nil
		}},
		{`SELECT rule,severity,count(*) FROM audit_issues WHERE status='open' GROUP BY rule,severity`, func(r pgx.Rows) error {
			var rule, sev string
			var n int64
			if err := r.Scan(&rule, &sev, &n); err != nil {
				return err
			}
			d.AddIssueCount(rule, sev, n)
			return nil
		}},
		{`SELECT kind,count(*) FILTER (WHERE status='pending'),count(*) FILTER (WHERE status='running'),
			count(*) FILTER (WHERE status='failed' AND finished_at>now()-interval '24 hours')
			FROM jobs WHERE status IN ('pending','running','failed') GROUP BY kind`, func(r pgx.Rows) error {
			var kind string
			var c audit.JobCounts
			if err := r.Scan(&kind, &c.Pending, &c.Running, &c.Failed24h); err != nil {
				return err
			}
			d.Jobs[kind] = c
			return nil
		}},
		{`SELECT DISTINCT ON (job) job,COALESCE(job_id,0),started_at,duration_ms,checked,fixed,issues,result,detail
			FROM nightly_runs ORDER BY job,started_at DESC`, func(r pgx.Rows) error {
			var x audit.Run
			var detail []byte
			if err := r.Scan(&x.Job, &x.JobID, &x.StartedAt, &x.DurationMS, &x.Checked, &x.Fixed, &x.Issues, &x.Result, &detail); err != nil {
				return err
			}
			if err := json.Unmarshal(detail, &x.Detail); err != nil {
				return err
			}
			d.NightlyRuns = append(d.NightlyRuns, x)
			return nil
		}},
	}
	for _, g := range groups {
		rows, err := s.Query(ctx, g.sql)
		if err != nil {
			return d, err
		}
		for rows.Next() {
			if err = g.scan(rows); err != nil {
				rows.Close()
				return d, err
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return d, err
		}
	}
	var withIssues int64
	if err := s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM identity_candidates WHERE status='pending'),
		(SELECT count(*) FROM field_conflicts WHERE status='pending'),
		(SELECT count(DISTINCT a.entity_id) FROM audit_issues a JOIN releases r ON r.id=a.entity_id
			WHERE a.status='open' AND a.entity_type='release' AND r.status='published' AND r.merged_into_id IS NULL)`).
		Scan(&d.IdentityCandidatesOpen, &d.ConflictsOpen, &withIssues); err != nil {
		return d, err
	}
	d.SetHealth(d.Releases.ByStatus["published"], withIssues)
	if err := s.sourceHealthSummary(ctx, &d); err != nil {
		return d, err
	}
	return d, nil
}

// ---------------------------------------------------------------- jobs

// EnqueueAudit queues an audit run unless one is already pending or running,
// in which case it returns that job's id with enqueued=false.
func (s *Store) EnqueueAudit(ctx context.Context) (jobID int64, enqueued bool, err error) {
	err = s.QueryRow(ctx, `INSERT INTO jobs(kind) VALUES($1) ON CONFLICT DO NOTHING RETURNING id`, JobKindAudit).Scan(&jobID)
	if err == nil {
		return jobID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = s.QueryRow(ctx, `SELECT id FROM jobs WHERE kind=$1 AND source_id IS NULL AND status IN ('pending','running') ORDER BY id DESC LIMIT 1`, JobKindAudit).Scan(&jobID)
	return jobID, false, err
}

// EnqueueAuditSince queues the nightly audit for the slot starting at since,
// unless an audit job was already created at or after it (or one is active).
func (s *Store) EnqueueAuditSince(ctx context.Context, since time.Time) (bool, error) {
	return s.EnqueueKindSince(ctx, JobKindAudit, since)
}

// EnqueueKindSince queues a sourceless job of kind for the slot starting at
// since, unless a job of that kind was already created at or after it (or
// one is pending or running: jobs_active_kind). Used for the nightly audit
// and the 10-minute source health run.
func (s *Store) EnqueueKindSince(ctx context.Context, kind string, since time.Time) (bool, error) {
	var id int64
	err := s.QueryRow(ctx, `INSERT INTO jobs(kind) SELECT $1::text WHERE NOT EXISTS
		(SELECT 1 FROM jobs WHERE kind=$1::text AND created_at>=$2) ON CONFLICT DO NOTHING RETURNING id`, kind, since).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ClaimJob leases the oldest due sourceless job of kind for this worker. It
// returns pgx.ErrNoRows when none is due. Renew the lease with Heartbeat.
func (s *Store) ClaimJob(ctx context.Context, kind string) (id, version int64, err error) {
	err = s.QueryRow(ctx, `WITH picked AS (SELECT id FROM jobs WHERE kind=$2 AND source_id IS NULL AND status='pending' AND available_at<=now()
		ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE jobs j SET status='running',started_at=COALESCE(j.started_at,now()),attempt=j.attempt+1,worker_id=$1,
		lease_until=now()+interval '90 seconds',heartbeat_at=now(),execution_version=j.execution_version+1
		FROM picked WHERE j.id=picked.id RETURNING j.id,j.execution_version`, s.workerID, kind).Scan(&id, &version)
	return id, version, err
}

// FinishJob completes a job claimed with ClaimJob. On failure it is retried
// after 5 minutes until max_attempts. counts is stored in result_counts.
func (s *Store) FinishJob(ctx context.Context, id, version int64, counts any, cause error) error {
	if cause != nil {
		_, err := s.Exec(ctx, `UPDATE jobs SET status=CASE WHEN attempt<max_attempts THEN 'pending' ELSE 'failed' END,error=$3,error_code='job_failed',
			available_at=now()+interval '5 minutes',finished_at=CASE WHEN attempt>=max_attempts THEN now() ELSE NULL END,worker_id=NULL,lease_until=NULL
			WHERE id=$1 AND status='running' AND execution_version=$2`, id, version, cause.Error())
		return err
	}
	raw, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `UPDATE jobs SET status='succeeded',finished_at=now(),worker_id=NULL,lease_until=NULL,error='',error_code='',result_counts=$3::jsonb
		WHERE id=$1 AND status='running' AND execution_version=$2`, id, version, string(raw))
	return err
}
