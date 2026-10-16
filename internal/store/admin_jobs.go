package store

// Admin 夜间任务 page (/admin/jobs): the jobs queue per kind with leases and
// attempts, the nightly_runs history per job, retrying a failed job and
// queueing a sourceless job now.

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueueJob is one job of the admin queue view.
type QueueJob struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	SourceID    *int64          `json:"source_id"`
	SourceName  string          `json:"source_name"`
	Phase       string          `json:"phase"`
	Status      string          `json:"status"`
	Attempt     int             `json:"attempt"`
	MaxAttempts int             `json:"max_attempts"`
	Error       string          `json:"error"`
	ErrorCode   string          `json:"error_code"`
	WorkerID    *string         `json:"worker_id"`
	LeaseUntil  *time.Time      `json:"lease_until"`
	HeartbeatAt *time.Time      `json:"heartbeat_at"`
	AvailableAt time.Time       `json:"available_at"`
	CreatedAt   time.Time       `json:"created_at"`
	StartedAt   *time.Time      `json:"started_at"`
	FinishedAt  *time.Time      `json:"finished_at"`
	Result      json.RawMessage `json:"result_counts,omitempty"`
}

// QueueKind summarizes one job kind.
type QueueKind struct {
	Kind            string     `json:"kind"`
	Pending         int        `json:"pending"`
	Running         int        `json:"running"`
	Failed7d        int        `json:"failed_7d"`
	Succeeded7d     int        `json:"succeeded_7d"`
	LastSucceededAt *time.Time `json:"last_succeeded_at"`
	LastFailedAt    *time.Time `json:"last_failed_at"`
}

const queueJobCols = `j.id,j.kind,j.source_id,COALESCE(s.name,''),j.phase,j.status,j.attempt,j.max_attempts,j.error,j.error_code,
	j.worker_id,j.lease_until,j.heartbeat_at,j.available_at,j.created_at,j.started_at,j.finished_at,j.result_counts`

func scanQueueJob(row pgx.Row) (QueueJob, error) {
	var j QueueJob
	err := row.Scan(&j.ID, &j.Kind, &j.SourceID, &j.SourceName, &j.Phase, &j.Status, &j.Attempt, &j.MaxAttempts, &j.Error, &j.ErrorCode,
		&j.WorkerID, &j.LeaseUntil, &j.HeartbeatAt, &j.AvailableAt, &j.CreatedAt, &j.StartedAt, &j.FinishedAt, &j.Result)
	if s := string(j.Result); s == "{}" || s == "null" {
		j.Result = nil
	}
	return j, err
}

// AdminJobQueue returns the per-kind summary (active jobs plus the last 7
// days) and the jobs to look at: every pending and running job and the
// failures of the last 7 days, newest first.
func (s *Store) AdminJobQueue(ctx context.Context) ([]QueueKind, []QueueJob, error) {
	rows, err := s.Query(ctx, `SELECT kind,
		count(*) FILTER (WHERE status='pending'), count(*) FILTER (WHERE status='running'),
		count(*) FILTER (WHERE status='failed' AND COALESCE(finished_at,created_at)>now()-interval '7 days'),
		count(*) FILTER (WHERE status='succeeded' AND finished_at>now()-interval '7 days'),
		max(finished_at) FILTER (WHERE status='succeeded'), max(COALESCE(finished_at,created_at)) FILTER (WHERE status='failed')
		FROM jobs WHERE status IN ('pending','running') OR created_at>now()-interval '30 days'
		GROUP BY kind ORDER BY kind`)
	if err != nil {
		return nil, nil, err
	}
	kinds := []QueueKind{}
	for rows.Next() {
		var k QueueKind
		if err := rows.Scan(&k.Kind, &k.Pending, &k.Running, &k.Failed7d, &k.Succeeded7d, &k.LastSucceededAt, &k.LastFailedAt); err != nil {
			rows.Close()
			return nil, nil, err
		}
		kinds = append(kinds, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows, err = s.Query(ctx, `(SELECT `+queueJobCols+` FROM jobs j LEFT JOIN sources s ON s.id=j.source_id
			WHERE j.status IN ('pending','running') ORDER BY j.id DESC LIMIT 300)
		UNION ALL
		(SELECT `+queueJobCols+` FROM jobs j LEFT JOIN sources s ON s.id=j.source_id
			WHERE j.status='failed' AND COALESCE(j.finished_at,j.created_at)>now()-interval '7 days' ORDER BY j.id DESC LIMIT 200)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	items := []QueueJob{}
	for rows.Next() {
		j, err := scanQueueJob(rows)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, j)
	}
	return kinds, items, rows.Err()
}

// RunPoint is one nightly_runs row of the history.
type RunPoint struct {
	JobID      *int64    `json:"job_id"`
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Checked    int       `json:"checked"`
	Fixed      int       `json:"fixed"`
	Issues     int       `json:"issues"`
	Result     string    `json:"result"`
}

// RunSeries is the history of one nightly_runs job, newest first.
type RunSeries struct {
	Job  string     `json:"job"`
	Runs []RunPoint `json:"runs"`
}

// MaxRunsPerJob bounds each job's history in AdminJobRuns.
const MaxRunsPerJob = 90

// AdminJobRuns returns the nightly_runs history of the last days (1-90),
// grouped by job name, at most MaxRunsPerJob rows each; job narrows it to one.
func (s *Store) AdminJobRuns(ctx context.Context, days int, job string) ([]RunSeries, error) {
	if days < 1 || days > 90 {
		return nil, invalid("days must be 1 to 90")
	}
	rows, err := s.Query(ctx, `SELECT job,job_id,started_at,duration_ms,checked,fixed,issues,result FROM (
		SELECT *, row_number() OVER (PARTITION BY job ORDER BY started_at DESC, id DESC) AS rn FROM nightly_runs
		WHERE started_at>now()-$1::int*interval '1 day' AND ($2='' OR job=$2)) r
		WHERE rn<=$3 ORDER BY job, started_at DESC, id DESC`, days, job, MaxRunsPerJob)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunSeries{}
	for rows.Next() {
		var name string
		var p RunPoint
		if err := rows.Scan(&name, &p.JobID, &p.StartedAt, &p.DurationMS, &p.Checked, &p.Fixed, &p.Issues, &p.Result); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Job != name {
			out = append(out, RunSeries{Job: name})
		}
		out[len(out)-1].Runs = append(out[len(out)-1].Runs, p)
	}
	return out, rows.Err()
}

// RetryJob puts a failed job back in the queue now, with one more attempt
// allowed. Unknown: pgx.ErrNoRows; not failed, or another job of the same
// kind (or source and phase) is already pending or running: *ConflictError.
func (s *Store) RetryJob(ctx context.Context, id int64) (QueueJob, error) {
	tag, err := s.Exec(ctx, `UPDATE jobs SET status='pending',available_at=now(),finished_at=NULL,worker_id=NULL,lease_until=NULL,
		error='',error_code='',max_attempts=GREATEST(max_attempts,attempt+1)
		WHERE id=$1 AND status='failed'`, id)
	if isUniqueViolation(err) {
		return QueueJob{}, conflict("another job of this kind (or source and phase) is already pending or running")
	}
	if err != nil {
		return QueueJob{}, err
	}
	j, err := scanQueueJob(s.QueryRow(ctx, `SELECT `+queueJobCols+` FROM jobs j LEFT JOIN sources s ON s.id=j.source_id WHERE j.id=$1`, id))
	if err != nil {
		return j, err
	}
	if tag.RowsAffected() == 0 {
		return j, conflict("job %d is %s, not failed", id, j.Status)
	}
	return j, nil
}

// EnqueueKindNow queues a sourceless job of kind to run at once unless one
// is already pending or running (jobs_active_kind), in which case it returns
// that job's id with enqueued=false.
func (s *Store) EnqueueKindNow(ctx context.Context, kind string) (jobID int64, enqueued bool, err error) {
	err = s.QueryRow(ctx, `INSERT INTO jobs(kind) VALUES($1) ON CONFLICT DO NOTHING RETURNING id`, kind).Scan(&jobID)
	if err == nil {
		return jobID, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = s.QueryRow(ctx, `SELECT id FROM jobs WHERE kind=$1 AND source_id IS NULL AND status IN ('pending','running') ORDER BY id DESC LIMIT 1`, kind).Scan(&jobID)
	return jobID, false, err
}
