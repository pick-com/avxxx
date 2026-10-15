ALTER TABLE sources ADD COLUMN IF NOT EXISTS source_kind text NOT NULL DEFAULT 'aggregator';
ALTER TABLE sources ADD COLUMN IF NOT EXISTS license_status text NOT NULL DEFAULT 'review_required';
ALTER TABLE sources ADD COLUMN IF NOT EXISTS terms_url text;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS egress_policy text NOT NULL DEFAULT 'direct';
ALTER TABLE sources ADD COLUMN IF NOT EXISTS priority smallint NOT NULL DEFAULT 100;

CREATE TABLE IF NOT EXISTS performers (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  canonical_name text NOT NULL,
  canonical_name_ja text,
  birth_date date,
  birth_place text,
  debut_date date,
  debut_age smallint,
  blood_type text,
  height_cm numeric(5,2),
  bust_cm numeric(5,2), waist_cm numeric(5,2), hip_cm numeric(5,2),
  cup_size text, shoe_size numeric(4,1), hair_color text, hair_length text,
  activity_start date, activity_end date, biography text,
  status text NOT NULL DEFAULT 'active',
  merged_into_id bigint REFERENCES performers(id),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS performer_aliases (
  id bigserial PRIMARY KEY, performer_id bigint NOT NULL REFERENCES performers(id),
  name text NOT NULL, normalized_name text NOT NULL, language text,
  alias_type text NOT NULL DEFAULT 'stage', source_id bigint REFERENCES sources(id),
  UNIQUE(performer_id, normalized_name)
);
CREATE INDEX IF NOT EXISTS performer_alias_search ON performer_aliases(normalized_name);

CREATE TABLE IF NOT EXISTS releases (
  id bigserial PRIMARY KEY, slug text NOT NULL UNIQUE, canonical_title text NOT NULL,
  title_ja text, release_date date, runtime_minutes smallint, maker_name text,
  label_name text, series_name text, director_name text, synopsis text,
  censorship_type text, edition_type text NOT NULL DEFAULT 'original',
  edition_of_release_id bigint REFERENCES releases(id),
  status text NOT NULL DEFAULT 'draft', merged_into_id bigint REFERENCES releases(id),
  version bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS release_identifiers (
  id bigserial PRIMARY KEY, release_id bigint NOT NULL REFERENCES releases(id),
  identifier_type text NOT NULL, identifier_value text NOT NULL,
  normalized_value text NOT NULL, identifier_scope text NOT NULL DEFAULT 'global',
  source_id bigint REFERENCES sources(id),
  UNIQUE(identifier_type, normalized_value, identifier_scope)
);
CREATE INDEX IF NOT EXISTS release_identifier_lookup ON release_identifiers(identifier_type, normalized_value);

CREATE TABLE IF NOT EXISTS release_performers (
  release_id bigint NOT NULL REFERENCES releases(id), performer_id bigint NOT NULL REFERENCES performers(id),
  billing_order smallint, role text, credited_name text, is_primary boolean NOT NULL DEFAULT false,
  PRIMARY KEY(release_id, performer_id)
);
CREATE TABLE IF NOT EXISTS release_tags (
  release_id bigint NOT NULL REFERENCES releases(id), tag text NOT NULL, normalized_tag text NOT NULL,
  PRIMARY KEY(release_id, normalized_tag)
);

CREATE TABLE IF NOT EXISTS source_records (
  id bigserial PRIMARY KEY, source_id bigint NOT NULL REFERENCES sources(id),
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  source_record_key text NOT NULL, source_url text NOT NULL,
  performer_id bigint REFERENCES performers(id), release_id bigint REFERENCES releases(id),
  raw_data jsonb NOT NULL DEFAULT '{}', parser_version text NOT NULL,
  first_seen_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(),
  last_http_status smallint, resolution_status text NOT NULL DEFAULT 'unresolved',
  UNIQUE(source_id, entity_type, source_record_key),
  CHECK(NOT (performer_id IS NOT NULL AND release_id IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS source_records_unresolved ON source_records(resolution_status, last_seen_at);

-- Links a canonical release to a playback provider's public media identifier.
-- This is deliberately separate from source_records: media providers expose
-- playable pages while catalog sources provide metadata and identifiers.
CREATE TABLE IF NOT EXISTS release_media_sources (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  source text NOT NULL,
  media_id text NOT NULL,
  media_url text,
  confidence text NOT NULL DEFAULT 'identifier',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(release_id, source),
  UNIQUE(source, media_id)
);
CREATE INDEX IF NOT EXISTS release_media_sources_lookup ON release_media_sources(source, media_id);
CREATE TABLE IF NOT EXISTS release_performer_sources (
  release_id bigint NOT NULL REFERENCES releases(id), performer_id bigint NOT NULL REFERENCES performers(id),
  source_record_id bigint NOT NULL REFERENCES source_records(id), billing_order smallint,
  role text, credited_name text, PRIMARY KEY(release_id, performer_id, source_record_id)
);
CREATE TABLE IF NOT EXISTS release_tag_sources (
  release_id bigint NOT NULL REFERENCES releases(id), normalized_tag text NOT NULL,
  source_record_id bigint NOT NULL REFERENCES source_records(id), source_tag text NOT NULL,
  PRIMARY KEY(release_id, normalized_tag, source_record_id)
);

CREATE TABLE IF NOT EXISTS source_field_values (
  id bigserial PRIMARY KEY, source_record_id bigint NOT NULL REFERENCES source_records(id),
  field_name text NOT NULL, raw_value jsonb, normalized_value jsonb,
  confidence text NOT NULL DEFAULT 'verified', captured_at timestamptz NOT NULL,
  is_current boolean NOT NULL DEFAULT true,
  UNIQUE(source_record_id, field_name, captured_at)
);
CREATE UNIQUE INDEX IF NOT EXISTS source_field_values_current
  ON source_field_values(source_record_id, field_name) WHERE is_current;
CREATE TABLE IF NOT EXISTS source_field_policies (
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  field_name text NOT NULL, source_id bigint NOT NULL REFERENCES sources(id), priority smallint NOT NULL,
  PRIMARY KEY(entity_type, field_name, source_id)
);

CREATE TABLE IF NOT EXISTS performer_source_metrics (
  performer_id bigint NOT NULL REFERENCES performers(id), source_id bigint NOT NULL REFERENCES sources(id),
  favorites_count bigint, works_count int, rating numeric(3,2), rating_count int, views_count bigint,
  captured_at timestamptz NOT NULL, PRIMARY KEY(performer_id, source_id, captured_at)
);
CREATE TABLE IF NOT EXISTS release_source_metrics (
  release_id bigint NOT NULL REFERENCES releases(id), source_id bigint NOT NULL REFERENCES sources(id),
  favorites_count bigint, views_count bigint, rating numeric(3,2), rating_count int, comments_count int,
  captured_at timestamptz NOT NULL, PRIMARY KEY(release_id, source_id, captured_at)
);

CREATE TABLE IF NOT EXISTS source_crawl_policies (
  source_id bigint PRIMARY KEY REFERENCES sources(id), discovery_interval interval,
  detail_refresh_interval interval, min_request_interval interval NOT NULL DEFAULT interval '5 seconds',
  daily_page_budget integer NOT NULL DEFAULT 0, daily_byte_budget bigint NOT NULL DEFAULT 0,
  enabled_at timestamptz, baseline_completed_at timestamptz
);
CREATE TABLE IF NOT EXISTS crawl_checkpoints (
  source_id bigint NOT NULL REFERENCES sources(id), task_type text NOT NULL, partition_key text NOT NULL DEFAULT '',
  cursor text, watermark timestamptz, completed_pages bigint NOT NULL DEFAULT 0,
  last_success_at timestamptz, last_error_code text, parser_version text NOT NULL,
  PRIMARY KEY(source_id, task_type, partition_key)
);

CREATE TABLE IF NOT EXISTS identity_candidates (
  id bigserial PRIMARY KEY, entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  left_id bigint NOT NULL, right_id bigint NOT NULL, score numeric(5,4) NOT NULL,
  evidence jsonb NOT NULL, status text NOT NULL DEFAULT 'pending',
  UNIQUE(entity_type, left_id, right_id)
);
CREATE TABLE IF NOT EXISTS field_conflicts (
  id bigserial PRIMARY KEY, entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  entity_id bigint NOT NULL, field_name text NOT NULL, current_value jsonb,
  candidates jsonb NOT NULL, severity text NOT NULL DEFAULT 'medium', status text NOT NULL DEFAULT 'pending',
  selected_source_record_id bigint REFERENCES source_records(id), resolution_note text,
  resolved_by bigint, resolved_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS field_conflicts_queue ON field_conflicts(status, severity, updated_at);
CREATE UNIQUE INDEX IF NOT EXISTS field_conflicts_pending_unique
  ON field_conflicts(entity_type, entity_id, field_name) WHERE status='pending';
CREATE TABLE IF NOT EXISTS review_events (
  id bigserial PRIMARY KEY, conflict_id bigint REFERENCES field_conflicts(id), entity_type text NOT NULL,
  entity_id bigint NOT NULL, action text NOT NULL, before_value jsonb, after_value jsonb,
  reason text, reviewer_id bigint, created_at timestamptz NOT NULL DEFAULT now()
);
