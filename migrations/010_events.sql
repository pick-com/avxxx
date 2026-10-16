-- Analytics events (PRD 14, tech plan 3.2 "事件").
--
-- events is range-partitioned by month on ts_server (UTC month boundaries).
-- A unique constraint on a partitioned table must include the partition key,
-- and ts_server differs on every client resend, so (event_id, ts_server) could
-- never catch a duplicate. Dedupe therefore goes through events_seen, a plain
-- table keyed by event_id alone: ingestion does
--   INSERT INTO events_seen ... ON CONFLICT (event_id) DO NOTHING RETURNING event_id
-- and inserts into events only the ids that came back, in one statement.
-- events_seen rows can be pruned together with the 90-day raw-event retention.
CREATE TABLE IF NOT EXISTS events (
  event_id   text        NOT NULL,
  event      text        NOT NULL,
  schema_ver integer     NOT NULL,
  ts_client  timestamptz NOT NULL,
  ts_server  timestamptz NOT NULL,
  user_id    bigint,               -- set from the bearer token; NULL without analytics consent
  device_id  text,                 -- NULL without analytics consent
  session_id text        NOT NULL,
  play_id    text,
  platform   text        NOT NULL,
  app_ver    text        NOT NULL,
  ui_lang    text,
  theme      text,
  page       text,
  ref_page   text,
  slot       text,
  consent    text[]      NOT NULL DEFAULT '{}',
  exp        text[]      NOT NULL DEFAULT '{}',
  release_id bigint,               -- lifted from props.release_id
  source_id  bigint,               -- lifted from props.source_id
  props      jsonb       NOT NULL DEFAULT '{}'
) PARTITION BY RANGE (ts_server);

CREATE INDEX IF NOT EXISTS events_event_ts ON events (event, ts_server);
CREATE INDEX IF NOT EXISTS events_play ON events (play_id, ts_server) WHERE play_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS events_release_ts ON events (release_id, ts_server) WHERE release_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS events_user_ts ON events (user_id, ts_server) WHERE user_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS events_seen (
  event_id  text        PRIMARY KEY,
  ts_server timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS events_seen_ts ON events_seen (ts_server);

-- Events that failed schema validation (PRD 14.5 "隔离区"). raw never keeps
-- user_id / device_id.
CREATE TABLE IF NOT EXISTS events_quarantine (
  id         bigserial   PRIMARY KEY,
  event_id   text,
  event      text,
  reason     text        NOT NULL,
  raw        jsonb       NOT NULL,
  ts_server  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS events_quarantine_ts ON events_quarantine (ts_server);
CREATE INDEX IF NOT EXISTS events_quarantine_event ON events_quarantine (event, ts_server);

-- events_ensure_partition creates the partition holding the UTC month that
-- contains p_month (named events_YYYYMM) if it does not exist yet, and
-- returns its name. Safe to call repeatedly; used by this migration, by
-- store.EnsureEventPartition and by the insert path when a month is missing.
CREATE OR REPLACE FUNCTION events_ensure_partition(p_month date) RETURNS text
LANGUAGE plpgsql AS $$
DECLARE
  m        timestamp := date_trunc('month', p_month::timestamp);
  part     text      := 'events_' || to_char(m, 'YYYYMM');
  start_ts timestamptz := m AT TIME ZONE 'UTC';
  end_ts   timestamptz := (m + interval '1 month') AT TIME ZONE 'UTC';
BEGIN
  EXECUTE format('CREATE TABLE IF NOT EXISTS %I PARTITION OF events FOR VALUES FROM (%L) TO (%L)',
                 part, start_ts, end_ts);
  RETURN part;
END
$$;

SELECT events_ensure_partition((now() AT TIME ZONE 'UTC')::date);
SELECT events_ensure_partition(((now() AT TIME ZONE 'UTC') + interval '1 month')::date);
