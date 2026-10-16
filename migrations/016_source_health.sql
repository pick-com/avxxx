-- Playback source health (PRD 1 片源健康分, PRD 11 bands; tech plan 3.3).
-- internal/audit/sourcehealth.go computes it every 10 minutes over the last
-- 24 hours of events; see that file for the formula. Idempotent.

-- One row per (run, source) for every source with playback activity in the
-- window. score is the raw 0–100 health of this window (NULL when there were
-- no attempts); band is its PRD 11 band, or 'insufficient_data' when attempts
-- were below the minimum sample, in which case playback_sources was not
-- touched. Rows older than 30 days are pruned by the job.
CREATE TABLE IF NOT EXISTS source_health_runs (
  id bigserial PRIMARY KEY,
  source_id bigint NOT NULL REFERENCES playback_sources(id) ON DELETE CASCADE,
  job_id bigint REFERENCES jobs(id) ON DELETE SET NULL,
  window_start timestamptz NOT NULL,
  window_end timestamptz NOT NULL,
  attempts integer NOT NULL DEFAULT 0,
  playable integer NOT NULL DEFAULT 0,
  ff_ok integer NOT NULL DEFAULT 0,
  rebuffers integer NOT NULL DEFAULT 0,
  switches_away integer NOT NULL DEFAULT 0,
  score real,
  band text NOT NULL CHECK (band IN ('severe','degraded','healthy','excellent','insufficient_data')),
  insufficient_data boolean NOT NULL DEFAULT false,
  status_before text NOT NULL,
  status_after text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS source_health_runs_source ON source_health_runs(source_id, window_end DESC);
CREATE INDEX IF NOT EXISTS source_health_runs_end ON source_health_runs(window_end);

-- When health_score was last set from a run with enough samples. NULL means
-- the default 1.0 is not a measurement (shown as insufficient_data).
ALTER TABLE playback_sources ADD COLUMN IF NOT EXISTS health_checked_at timestamptz;
CREATE INDEX IF NOT EXISTS playback_sources_health ON playback_sources(health_score, id);

-- source_switch carries from_source_id in props (it is not lifted into
-- events.source_id); switches-away are counted per from-source.
CREATE INDEX IF NOT EXISTS events_switch_from ON events (((props->>'from_source_id')), ts_server) WHERE event = 'source_switch';

-- R-SOURCE issues are about a playback source.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'audit_issues_entity_type_check'
             AND pg_get_constraintdef(oid) NOT LIKE '%playback_source%') THEN
    ALTER TABLE audit_issues DROP CONSTRAINT audit_issues_entity_type_check;
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'audit_issues_entity_type_check') THEN
    ALTER TABLE audit_issues ADD CONSTRAINT audit_issues_entity_type_check
      CHECK (entity_type IN ('release','performer','playback_source'));
  END IF;
END
$$;
