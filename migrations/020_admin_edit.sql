-- Admin video editing (AdminVideo / A3, PRD 11, 6.4, 15) and manual
-- overrides (FINAL_DATA_DESIGN 2.2 / 6). Re-run on every start: idempotent.

-- PRD 6.4: age at filming is shoot year − birth year. NULL = unknown (the
-- normal case for crawled JAV data); R-AGE then falls back to the release date.
ALTER TABLE releases ADD COLUMN IF NOT EXISTS shoot_year smallint;
-- PRD 15 未成年人: content rating used by the (future) teen mode.
-- NULL = not rated yet. Values mirror store.AgeRatings.
ALTER TABLE releases ADD COLUMN IF NOT EXISTS age_rating text;
-- PRD 15 地区限制 at release level (ISO 3166-1 alpha-2, upper case). Applied
-- on top of every playback source's own region_allow / region_deny.
ALTER TABLE releases ADD COLUMN IF NOT EXISTS region_allow text[];
ALTER TABLE releases ADD COLUMN IF NOT EXISTS region_deny text[];
-- Free-text editorial note for featured placement (推荐位).
ALTER TABLE releases ADD COLUMN IF NOT EXISTS featured_note text;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'releases_shoot_year_range') THEN
    ALTER TABLE releases ADD CONSTRAINT releases_shoot_year_range CHECK (shoot_year IS NULL OR shoot_year BETWEEN 1900 AND 2100);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'releases_age_rating_valid') THEN
    ALTER TABLE releases ADD CONSTRAINT releases_age_rating_valid CHECK (age_rating IS NULL OR age_rating IN ('all','13','16','18'));
  END IF;
END
$$;

-- Manual overrides. One row per (entity, field) the admin set by hand: the
-- canonical column holds the effective value, and catalog ingest
-- (store.SaveCatalogRecord) never overwrites a field that has a row here, nor
-- opens new field_conflicts for it. Clearing an override is an explicit admin
-- action (DELETE /api/admin/releases/{id}/overrides/{field}); the next crawl
-- then writes the field again. field_name is the releases / performers column
-- name, or 'tags' / 'performers' for the release relations. value is the
-- value as set (jsonb, NULL = cleared by hand).
CREATE TABLE IF NOT EXISTS release_overrides (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  field_name text NOT NULL,
  value jsonb,
  reason text,
  actor text NOT NULL,
  conflict_id bigint REFERENCES field_conflicts(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, field_name)
);
CREATE TABLE IF NOT EXISTS performer_overrides (
  performer_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  field_name text NOT NULL,
  value jsonb,
  reason text,
  actor text NOT NULL,
  conflict_id bigint REFERENCES field_conflicts(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (performer_id, field_name)
);

-- releases.status values (no CHECK, see 013/014): draft | upcoming |
-- published | unpublished | held_review. Transitions are enforced in
-- store.CheckTransition; every one writes review_events.

-- Admin detail pages read the history of one entity.
CREATE INDEX IF NOT EXISTS review_events_entity ON review_events(entity_type, entity_id, id DESC);
CREATE INDEX IF NOT EXISTS audit_issues_entity ON audit_issues(entity_type, entity_id);
