-- Admin data follow-ups. Re-run on every start: every statement is idempotent.

-- AdminDashboard site KPIs (internal/sitemetrics): one row per UTC day,
-- rebuilt from events by the sourceless 'site_metrics' job (cmd/audit,
-- hourly from cmd/scheduler). Raw events are purged after 90 days; these
-- aggregates are kept long-term, and a run never rewrites a day older than
-- the event retention. Definitions are in internal/sitemetrics.
CREATE TABLE IF NOT EXISTS site_daily_metrics (
  day date PRIMARY KEY,
  plays bigint NOT NULL DEFAULT 0 CHECK (plays >= 0),
  dau bigint NOT NULL DEFAULT 0 CHECK (dau >= 0),
  signed_in bigint NOT NULL DEFAULT 0 CHECK (signed_in >= 0),
  watch_seconds bigint NOT NULL DEFAULT 0 CHECK (watch_seconds >= 0),
  completed_plays bigint NOT NULL DEFAULT 0 CHECK (completed_plays >= 0 AND completed_plays <= plays),
  computed_at timestamptz NOT NULL DEFAULT now()
);
