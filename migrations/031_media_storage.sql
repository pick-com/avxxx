-- Image storage (docs/DATA_RETENTION_AND_IMAGES.md, docs/IMAGE_STORAGE.md):
-- per-source image permission, media_assets provenance and variants, fetch
-- attempts, sticky admin blocks and the purge job's bucket scan cursor.
-- Re-run on every start: every statement is idempotent.

-- ---- Per-source image permission. Independent of the crawl license: an
-- admin must explicitly confirm that the source's terms allow storing and
-- re-serving its images. internal/images.SourceAllowed also requires an
-- image-capable license_status (official_api / licensed / internal) and a
-- terms review, checked at run time, so a later downgrade stops downloads.
ALTER TABLE sources ADD COLUMN IF NOT EXISTS images_allowed boolean NOT NULL DEFAULT false;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS images_reviewed_at timestamptz;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS images_reviewed_by text;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS images_note text;

-- ---- media_assets provenance and variants. variant 'original' is the
-- stored original (capped at IMAGE_MAX_DIMENSION); 'w320' / 'w720' are
-- derived from it (derived_from, deleted with it).
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS source_id bigint REFERENCES sources(id) ON DELETE SET NULL;
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS source_record_id bigint REFERENCES source_records(id) ON DELETE SET NULL;
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS variant text NOT NULL DEFAULT 'original';
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS derived_from bigint REFERENCES media_assets(id) ON DELETE CASCADE;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'media_assets_variant_valid') THEN
    ALTER TABLE media_assets ADD CONSTRAINT media_assets_variant_valid
      CHECK (variant IN ('original','w320','w720') AND ((variant = 'original') = (derived_from IS NULL)));
  END IF;
END
$$;
-- Public reads: the newest visible original per entity and its variants.
CREATE INDEX IF NOT EXISTS media_assets_public ON media_assets(entity_type, entity_id, asset_type, id DESC)
  WHERE suppressed_at IS NULL AND variant = 'original';
CREATE INDEX IF NOT EXISTS media_assets_derived ON media_assets(derived_from) WHERE derived_from IS NOT NULL;
CREATE INDEX IF NOT EXISTS media_assets_object_key ON media_assets(object_key);

-- ---- Download attempts (one row per entity + asset type). Failures back
-- off exponentially (internal/images.NextTry); no empty asset is written.
CREATE TABLE IF NOT EXISTS media_fetch_attempts (
  entity_type text NOT NULL CHECK (entity_type IN ('performer','release')),
  entity_id bigint NOT NULL,
  asset_type text NOT NULL,
  source_id bigint REFERENCES sources(id) ON DELETE SET NULL,
  source_url text NOT NULL,
  status text NOT NULL CHECK (status IN ('stored','failed','skipped')),
  attempts integer NOT NULL DEFAULT 0,
  last_error text,
  tried_at timestamptz NOT NULL DEFAULT now(),
  next_try_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_type, entity_id, asset_type)
);
CREATE INDEX IF NOT EXISTS media_fetch_attempts_due ON media_fetch_attempts(next_try_at);

-- ---- Sticky blocks: an admin suppression keeps the image job from
-- downloading a replacement for (entity, asset type) even after the purge
-- job deleted the suppressed rows. Restore removes the block.
CREATE TABLE IF NOT EXISTS media_blocks (
  entity_type text NOT NULL CHECK (entity_type IN ('performer','release')),
  entity_id bigint NOT NULL,
  asset_type text NOT NULL,
  reason text,
  actor text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_type, entity_id, asset_type)
);

-- ---- Small key/value state for the image jobs (the orphan scan cursor).
CREATE TABLE IF NOT EXISTS media_storage_state (
  name text PRIMARY KEY,
  value text NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
