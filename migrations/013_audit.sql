-- Data-quality audit (AdminAudit / A5) and nightly run log (AdminDashboard / A1).
-- See internal/audit. Idempotent: the whole schema is re-applied on every start.

-- One row per finding. At most one OPEN issue per (rule, entity); reruns
-- refresh it (detail, priority, last_seen_at) instead of duplicating it.
-- fingerprint identifies the exact finding: a dismissed issue suppresses the
-- same fingerprint on later runs, while changed data is raised again.
CREATE TABLE IF NOT EXISTS audit_issues (
  id bigserial PRIMARY KEY,
  rule text NOT NULL,
  entity_type text NOT NULL CHECK(entity_type IN ('release','performer')),
  entity_id bigint NOT NULL,
  severity text NOT NULL CHECK(severity IN ('blocking','high','medium','low')),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb,
  fingerprint text NOT NULL DEFAULT '',
  priority bigint NOT NULL DEFAULT 0, -- popularity (watch_history count); ties fall back to recency
  status text NOT NULL DEFAULT 'open' CHECK(status IN ('open','fixed','dismissed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  resolution_note text
);
CREATE UNIQUE INDEX IF NOT EXISTS audit_issues_open_unique
  ON audit_issues(rule, entity_type, entity_id) WHERE status='open';
CREATE INDEX IF NOT EXISTS audit_issues_queue ON audit_issues(status, priority DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_issues_rule_queue ON audit_issues(rule, status, priority DESC, id DESC);
CREATE INDEX IF NOT EXISTS audit_issues_dismissed
  ON audit_issues(rule, entity_type, entity_id, fingerprint) WHERE status='dismissed';

-- One row per job step (the audit writes one per rule, job = 'audit.R-AGE' ...).
CREATE TABLE IF NOT EXISTS nightly_runs (
  id bigserial PRIMARY KEY,
  job text NOT NULL,
  job_id bigint REFERENCES jobs(id) ON DELETE SET NULL,
  started_at timestamptz NOT NULL,
  duration_ms bigint NOT NULL DEFAULT 0,
  checked integer NOT NULL DEFAULT 0,
  fixed integer NOT NULL DEFAULT 0,
  issues integer NOT NULL DEFAULT 0,
  result text NOT NULL CHECK(result IN ('ok','issues','failed','not_applicable')),
  detail jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX IF NOT EXISTS nightly_runs_recent ON nightly_runs(job, started_at DESC);

-- Who made a review change: 'audit' for automatic fixes and holds, 'admin'
-- for the admin API. reviewer_id stays the human reviewer when there is one.
ALTER TABLE review_events ADD COLUMN IF NOT EXISTS actor text;

-- Non-crawl jobs. Crawl jobs keep kind='crawl' and a source; other kinds
-- (currently 'audit') have no source. The crawl worker's Claim joins sources,
-- so it never picks up sourceless jobs; ReapExpired and leases apply to all.
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'crawl';
ALTER TABLE jobs ALTER COLUMN source_id DROP NOT NULL;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'jobs_kind_source') THEN
    ALTER TABLE jobs ADD CONSTRAINT jobs_kind_source CHECK (kind <> 'crawl' OR source_id IS NOT NULL);
  END IF;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_kind ON jobs(kind) WHERE source_id IS NULL AND status IN ('pending','running');
CREATE INDEX IF NOT EXISTS jobs_kind_pending ON jobs(kind, available_at, id) WHERE status='pending';

-- releases.status has no CHECK constraint; the audit adds the value
-- 'held_review' (age at filming may be under 18). Public queries only return
-- status='published', so held releases disappear from them. Only a human
-- moves a release out of held_review.

-- R-META ranks issues by popularity = watch_history rows per release; the
-- primary key leads with user_id, so counting by release needs this index.
CREATE INDEX IF NOT EXISTS watch_history_release ON watch_history(release_id);
