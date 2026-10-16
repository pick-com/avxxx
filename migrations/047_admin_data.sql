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

-- 其他版本 (AdminDedupe 合并为其他版本): two releases that are versions of
-- the same work (a re-release, an uncensored or 4K edition with its own
-- catalog entry) stay separate entities but are linked, and the release page
-- lists them under 其他版本. Existing relations do not fit: series groups
-- different works, edition_type is a property of one release, and
-- playback_sources.version_kind='alt' is a cut inside one release. A link
-- is one pair stored canonically (a_id < b_id); the versions of a release
-- are every release reachable through links. Creating or removing a link
-- writes a review_events row (link_versions / unlink_versions), and the
-- identity candidate it came from is closed with status 'versions'.
CREATE TABLE IF NOT EXISTS release_versions (
  a_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  b_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  review_event_id bigint REFERENCES review_events(id),
  created_by text,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (a_id, b_id),
  CHECK (a_id < b_id)
);
CREATE INDEX IF NOT EXISTS release_versions_b ON release_versions (b_id);
