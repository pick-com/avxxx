package store

// Crawl governance (FINAL_DATA_DESIGN 2.4, 7, 10.3; migrations/024): the
// license/terms gate on enqueue and claim, baseline/incremental/verify job
// phases, and the admin license review. The gate decision itself is
// catalog.Gate so the rules live in one tested place.

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// sourceColumns is the column list scanned by scanSource (alias s).
const sourceColumns = `s.id,s.name,s.url,s.enabled,s.version,s.adapter,s.source_kind,s.license_status,s.egress_policy,
COALESCE(s.terms_url,''),s.terms_reviewed_at,COALESCE(s.terms_reviewed_by,''),COALESCE(s.license_note,''),s.baseline_requested_at`

func scanSource(row pgx.Row, x *model.Source, extra ...any) error {
	dest := append([]any{&x.ID, &x.Name, &x.URL, &x.Enabled, &x.Version, &x.Adapter, &x.SourceKind, &x.LicenseStatus, &x.EgressPolicy,
		&x.TermsURL, &x.TermsReviewedAt, &x.TermsReviewedBy, &x.LicenseNote, &x.BaselineRequestedAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		return err
	}
	d := catalog.Gate(governance(*x), catalog.PhaseIncremental, 0)
	x.AutomaticAllowed, x.GateReason = d.Allowed, d.Reason
	return nil
}

func governance(x model.Source) catalog.SourceGovernance {
	return catalog.SourceGovernance{Adapter: x.Adapter, LicenseStatus: x.LicenseStatus, TermsURL: x.TermsURL, TermsReviewedAt: x.TermsReviewedAt}
}

// Sources lists every source with its governance state.
func (s *Store) Sources(ctx context.Context) ([]model.Source, error) {
	r, e := s.Query(ctx, "SELECT "+sourceColumns+" FROM sources s ORDER BY s.id")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Source{}
	for r.Next() {
		var x model.Source
		if e = scanSource(r, &x); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}

// GateRefusal is an enqueue the governance gate refused. The API maps it to
// 409 with Reason; Code is the jobs.error_code it corresponds to.
type GateRefusal struct{ Code, Reason string }

func (e *GateRefusal) Error() string { return e.Code + ": " + e.Reason }

// EnqueueRequest is a manual (admin) job request.
type EnqueueRequest struct {
	Phase    catalog.Phase
	MaxPages int // verify only; clamped by the gate
	Actor    string
}

// Enqueue creates one job for a source on an admin's request. Automatic
// phases need the source to pass the license gate; a baseline request is also
// recorded on the source so the scheduler keeps the pass going until it
// completes. Verify jobs are page-capped. A second active job of the same
// phase is a ConflictError.
func (s *Store) Enqueue(ctx context.Context, id int64, req EnqueueRequest) (int64, error) {
	if req.Phase == "" {
		req.Phase = catalog.PhaseIncremental
	}
	if req.Actor == "" {
		req.Actor = "admin"
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var x model.Source
	if err = scanSource(tx.QueryRow(ctx, "SELECT "+sourceColumns+" FROM sources s WHERE s.id=$1 FOR UPDATE", id), &x); err != nil {
		return 0, err
	}
	if !x.Enabled {
		return 0, conflict("source disabled")
	}
	d := catalog.Gate(governance(x), req.Phase, req.MaxPages)
	if !d.Allowed {
		return 0, &GateRefusal{Code: d.Code, Reason: d.Reason}
	}
	var pageCap *int
	if d.MaxPages > 0 {
		pageCap = &d.MaxPages
	}
	var jobID int64
	err = tx.QueryRow(ctx, `INSERT INTO jobs(source_id,phase,page_cap,requested_by) VALUES($1,$2,$3,$4)
		ON CONFLICT (source_id,phase) WHERE source_id IS NOT NULL AND status IN ('pending','running') DO NOTHING RETURNING id`,
		id, string(req.Phase), pageCap, req.Actor).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, conflict("a %s job for this source is already pending or running", req.Phase)
	}
	if err != nil {
		return 0, err
	}
	if req.Phase == catalog.PhaseBaseline {
		if _, err = tx.Exec(ctx, `UPDATE sources SET baseline_requested_at=now(),baseline_requested_by=$2 WHERE id=$1`, id, req.Actor); err != nil {
			return 0, err
		}
	}
	return jobID, tx.Commit(ctx)
}

// recordGated inserts a failed job explaining why a source's automatic job
// was not created, unless the latest job of that phase already says so.
func recordGated(ctx context.Context, tx pgx.Tx, sourceID int64, phase catalog.Phase, d catalog.GateDecision) error {
	_, err := tx.Exec(ctx, `INSERT INTO jobs(source_id,phase,status,error,error_code,finished_at,requested_by)
		SELECT $1,$2,'failed',$3,$4,now(),'scheduler'
		WHERE NOT EXISTS (SELECT 1 FROM (SELECT error_code,error FROM jobs WHERE source_id=$1 AND phase=$2 ORDER BY id DESC LIMIT 1) last
		                  WHERE last.error_code=$4 AND last.error=$3)`, sourceID, string(phase), d.Reason, d.Code)
	return err
}

// EnqueueDue creates the scheduler's jobs; the scheduler never performs
// network I/O, workers claim the resulting jobs.
//
//   - incremental: at most one per due source (next_run_at reached), then
//     next_run_at advances by the source's discovery interval;
//   - baseline: one while an admin baseline request is open
//     (sources.baseline_requested_at), until a full pass completes.
//
// Sources that fail the license gate get no runnable job: a failed job with
// error_code license_gate records why (once per distinct reason), and an
// open baseline request on such a source is withdrawn.
func (s *Store) EnqueueDue(ctx context.Context) (int, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	type due struct {
		src      model.Source
		baseline bool
	}
	rows, err := tx.Query(ctx, `SELECT `+sourceColumns+`, (s.next_run_at IS NULL OR s.next_run_at<=now()) FROM sources s
		WHERE s.enabled AND (s.blocked_until IS NULL OR s.blocked_until<=now())
		  AND (s.next_run_at IS NULL OR s.next_run_at<=now() OR s.baseline_requested_at IS NOT NULL)
		ORDER BY COALESCE(s.next_run_at,now()),s.id FOR UPDATE OF s SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	var list []due
	var incDue []bool
	for rows.Next() {
		var d due
		var isDue bool
		if err = scanSource(rows, &d.src, &isDue); err != nil {
			rows.Close()
			return 0, err
		}
		d.baseline = d.src.BaselineRequestedAt != nil
		list = append(list, d)
		incDue = append(incDue, isDue)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	created := 0
	insert := func(id int64, phase catalog.Phase) error {
		tag, err := tx.Exec(ctx, `INSERT INTO jobs(source_id,phase,requested_by) VALUES($1,$2,'scheduler')
			ON CONFLICT (source_id,phase) WHERE source_id IS NOT NULL AND status IN ('pending','running') DO NOTHING`, id, string(phase))
		created += int(tag.RowsAffected())
		return err
	}
	for i, d := range list {
		gov := governance(d.src)
		if incDue[i] {
			if g := catalog.Gate(gov, catalog.PhaseIncremental, 0); g.Allowed {
				err = insert(d.src.ID, catalog.PhaseIncremental)
			} else {
				err = recordGated(ctx, tx, d.src.ID, catalog.PhaseIncremental, g)
			}
			if err != nil {
				return 0, err
			}
			if _, err = tx.Exec(ctx, `UPDATE sources SET next_run_at=now()+COALESCE((SELECT discovery_interval FROM source_crawl_policies WHERE source_id=$1),interval '1 hour') WHERE id=$1`, d.src.ID); err != nil {
				return 0, err
			}
		}
		if d.baseline && !gov.IsFeed() {
			if g := catalog.Gate(gov, catalog.PhaseBaseline, 0); g.Allowed {
				err = insert(d.src.ID, catalog.PhaseBaseline)
			} else if err = recordGated(ctx, tx, d.src.ID, catalog.PhaseBaseline, g); err == nil {
				_, err = tx.Exec(ctx, `UPDATE sources SET baseline_requested_at=NULL WHERE id=$1`, d.src.ID)
			}
			if err != nil {
				return 0, err
			}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return created, nil
}

// ClaimedJob is a leased crawl job.
type ClaimedJob struct {
	ID      int64
	Source  model.Source
	Phase   catalog.Phase
	PageCap int
}

// claimAttempts bounds how many gated jobs one Claim call may settle before
// it returns pgx.ErrNoRows.
const claimAttempts = 20

// Claim leases the next runnable crawl job. Only one job per source runs at
// a time; verify jobs go first, then incremental, then baseline, so a long
// baseline never starves the daily pass. The license gate is re-checked at
// claim time (a source's license may have changed since enqueue): a refused
// job is closed as failed with error_code license_gate and the next one is
// tried. Returns pgx.ErrNoRows when nothing is runnable.
func (s *Store) Claim(ctx context.Context) (ClaimedJob, error) {
	for range claimAttempts {
		job, retry, err := s.claimOne(ctx)
		if !retry {
			return job, err
		}
	}
	return ClaimedJob{}, pgx.ErrNoRows
}

func (s *Store) claimOne(ctx context.Context) (job ClaimedJob, retry bool, err error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return job, false, err
	}
	defer tx.Rollback(ctx)
	var phase string
	var pageCap *int
	err = scanSource(tx.QueryRow(ctx, `SELECT `+sourceColumns+`, j.id, j.phase, j.page_cap FROM jobs j JOIN sources s ON s.id=j.source_id
		WHERE j.status='pending' AND j.available_at<=now() AND s.enabled AND (s.blocked_until IS NULL OR s.blocked_until<=now())
		  AND NOT EXISTS (SELECT 1 FROM jobs r WHERE r.source_id=j.source_id AND r.status='running')
		ORDER BY CASE j.phase WHEN 'verify' THEN 0 WHEN 'incremental' THEN 1 ELSE 2 END, j.id
		FOR UPDATE OF j, s SKIP LOCKED LIMIT 1`), &job.Source, &job.ID, &phase, &pageCap)
	if err != nil {
		return job, false, err
	}
	job.Phase = catalog.Phase(phase)
	d := catalog.Gate(governance(job.Source), job.Phase, intOr(pageCap, 0))
	if !d.Allowed {
		if _, err = tx.Exec(ctx, `UPDATE jobs SET status='failed',error=$2,error_code=$3,finished_at=now() WHERE id=$1`, job.ID, d.Reason, d.Code); err != nil {
			return job, false, err
		}
		return ClaimedJob{}, true, tx.Commit(ctx)
	}
	job.PageCap = d.MaxPages
	if _, err = tx.Exec(ctx, `UPDATE jobs SET status='running',started_at=COALESCE(started_at,now()),attempt=attempt+1,worker_id=$2,
		lease_until=now()+interval '90 seconds',heartbeat_at=now(),execution_version=execution_version+1 WHERE id=$1`, job.ID, s.workerID); err != nil {
		return job, false, err
	}
	return job, false, tx.Commit(ctx)
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

// Jobs lists the latest 100 jobs.
func (s *Store) Jobs(ctx context.Context) ([]model.Job, error) {
	r, e := s.Query(ctx, "SELECT id,COALESCE(source_id,0),phase,page_cap,status,error,error_code,created_at,attempt,lease_until,execution_version FROM jobs ORDER BY id DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Job{}
	for r.Next() {
		var x model.Job
		if e = r.Scan(&x.ID, &x.SourceID, &x.Phase, &x.PageCap, &x.Status, &x.Error, &x.ErrorCode, &x.CreatedAt, &x.Attempt, &x.LeaseUntil, &x.ExecutionVersion); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}

// SetJobResult stores a run summary in jobs.result_counts.
func (s *Store) SetJobResult(ctx context.Context, id int64, counts any) error {
	b, err := json.Marshal(counts)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `UPDATE jobs SET result_counts=$2::jsonb WHERE id=$1`, id, string(b))
	return err
}

// MarkBaselineComplete records a finished baseline pass and closes the
// admin's baseline request.
func (s *Store) MarkBaselineComplete(ctx context.Context, sourceID int64) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO source_crawl_policies(source_id,baseline_completed_at) VALUES($1,now())
		ON CONFLICT(source_id) DO UPDATE SET baseline_completed_at=now()`, sourceID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE sources SET baseline_requested_at=NULL WHERE id=$1`, sourceID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LicenseReview is an admin's license/terms decision for a source.
type LicenseReview struct {
	LicenseStatus string  `json:"license_status"`
	TermsURL      *string `json:"terms_url"`
	Note          string  `json:"note"`
	// Reviewed stamps terms_reviewed_at/by with now and the actor. Clearing
	// it (false) withdraws a previous review, which stops automatic crawls.
	Reviewed bool `json:"reviewed"`
}

// SetSourceLicense applies a license review and appends a review_events row
// (entity_type 'source', action 'license_review') with the before/after state.
func (s *Store) SetSourceLicense(ctx context.Context, id int64, in LicenseReview, actor string) (model.Source, error) {
	in.LicenseStatus = strings.TrimSpace(in.LicenseStatus)
	if !catalog.ValidLicenseStatus(in.LicenseStatus) {
		return model.Source{}, invalid("license_status must be one of %s", strings.Join(catalog.LicenseStatuses, ", "))
	}
	if len(in.Note) > MaxReasonLen {
		return model.Source{}, invalid("note too long")
	}
	if in.TermsURL != nil {
		u := strings.TrimSpace(*in.TermsURL)
		if u != "" && !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			return model.Source{}, invalid("terms_url must be an http(s) URL")
		}
		if len(u) > 2000 {
			return model.Source{}, invalid("terms_url too long")
		}
		in.TermsURL = &u
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return model.Source{}, err
	}
	defer tx.Rollback(ctx)
	var before model.Source
	if err = scanSource(tx.QueryRow(ctx, "SELECT "+sourceColumns+" FROM sources s WHERE s.id=$1 FOR UPDATE", id), &before); err != nil {
		return model.Source{}, err
	}
	var after model.Source
	err = scanSource(tx.QueryRow(ctx, `UPDATE sources s SET license_status=$2,
		terms_url=CASE WHEN $3::boolean THEN NULLIF($4,'') ELSE s.terms_url END,
		license_note=NULLIF($5,''),
		terms_reviewed_at=CASE WHEN $6::boolean THEN now() ELSE NULL END,
		terms_reviewed_by=CASE WHEN $6::boolean THEN $7 ELSE NULL END,
		version=s.version+1
		WHERE s.id=$1 RETURNING `+sourceColumns, id, in.LicenseStatus, in.TermsURL != nil, strOr(in.TermsURL), in.Note, in.Reviewed, actor), &after)
	if err != nil {
		return model.Source{}, err
	}
	state := func(x model.Source) map[string]any {
		return map[string]any{"license_status": x.LicenseStatus, "terms_url": x.TermsURL, "terms_reviewed_at": x.TermsReviewedAt,
			"terms_reviewed_by": x.TermsReviewedBy, "automatic_allowed": x.AutomaticAllowed}
	}
	if err = sourceEvent(ctx, tx, id, "license_review", state(before), state(after), in.Note, actor); err != nil {
		return model.Source{}, err
	}
	return after, tx.Commit(ctx)
}

func strOr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// sourceEvent appends an audit row about a source to review_events.
func sourceEvent(ctx context.Context, q execer, sourceID int64, action string, before, after any, reason, actor string) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor) VALUES('source',$1,$2,$3::jsonb,$4::jsonb,NULLIF($5,''),$6)`,
		sourceID, action, string(b), string(a), reason, actor)
	return err
}

// SourceCheckpoints lists one source's crawl checkpoints (admin view).
func (s *Store) SourceCheckpoints(ctx context.Context, sourceID int64) ([]CrawlCheckpoint, error) {
	r, err := s.Query(ctx, `SELECT task_type,partition_key,COALESCE(cursor,''),watermark,completed_pages,last_success_at,COALESCE(last_error_code,''),parser_version
		FROM crawl_checkpoints WHERE source_id=$1 AND task_type=$2 ORDER BY partition_key`, sourceID, catalog.TaskDiscovery)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []CrawlCheckpoint{}
	for r.Next() {
		c := CrawlCheckpoint{SourceID: sourceID}
		if err = r.Scan(&c.TaskType, &c.PartitionKey, &c.Cursor, &c.Watermark, &c.CompletedPages, &c.LastSuccessAt, &c.LastErrorCode, &c.ParserVersion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, r.Err()
}

// finishCode maps a run error to the jobs.error_code of a run that must not
// be retried by the job queue ("" = ordinary, retryable failure).
func finishCode(cause error) (string, string) {
	var g *catalog.GateError
	if errors.As(cause, &g) {
		return g.Code, cause.Error()
	}
	if r, ok := catalog.IsRobots(cause); ok {
		return catalog.CodeRobotsDisallow, r.Error()
	}
	if b, ok := catalog.IsBlocked(cause); ok {
		return "source_blocked", b.Error()
	}
	return "", ""
}
