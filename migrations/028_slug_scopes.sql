-- Non-ASCII names: performer external ids, non-empty slugs, maker-scoped
-- DVD codes (FINAL_DATA_DESIGN 3). Idempotent: every statement is a no-op on
-- re-run.

-- Source-issued performer ids (namespace 'dmm_actress' = DMM actress id), so
-- a DMM performer credited on a release (performer_refs) and the same
-- actress from ActressSearch resolve to one performer whatever its slug.
-- performer_id may point at a merged performer; readers follow
-- merged_into_id (survivingID), so merges and unmerges need no rewrite.
CREATE TABLE IF NOT EXISTS performer_external_ids (
  namespace text NOT NULL,
  external_id text NOT NULL,
  performer_id bigint NOT NULL REFERENCES performers(id),
  source_id bigint REFERENCES sources(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(namespace, external_id)
);
CREATE INDEX IF NOT EXISTS performer_external_ids_performer ON performer_external_ids(performer_id);

-- Backfill from DMM performer records saved before this table existed (the
-- first record of each actress id wins, as at ingest).
INSERT INTO performer_external_ids(namespace,external_id,performer_id,source_id)
SELECT DISTINCT ON (sr.raw_data->>'dmm_actress_id') 'dmm_actress', sr.raw_data->>'dmm_actress_id', sr.performer_id, sr.source_id
FROM source_records sr
WHERE sr.entity_type='performer' AND sr.performer_id IS NOT NULL AND sr.raw_data->>'dmm_actress_id' ~ '^[0-9]+$'
ORDER BY sr.raw_data->>'dmm_actress_id', sr.id
ON CONFLICT DO NOTHING;

-- Never an empty slug: repair any (none are expected) and enforce it.
UPDATE performers SET slug='performer-'||id WHERE btrim(slug)='';
UPDATE releases SET slug='release-'||id WHERE btrim(slug)='';
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='performers_slug_nonempty') THEN
    ALTER TABLE performers ADD CONSTRAINT performers_slug_nonempty CHECK (btrim(slug)<>'');
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname='releases_slug_nonempty') THEN
    ALTER TABLE releases ADD CONSTRAINT releases_slug_nonempty CHECK (btrim(slug)<>'');
  END IF;
END $$;

-- DVD codes saved with a maker whose name has no ASCII slug got the bare
-- scope 'maker:', so every Japanese-named maker shared one scope. Recompute
-- the scope exactly as catalog.MakerScope does, from the saving source's
-- record: 'dmm-maker:<id>' when it carries a numeric DMM maker_id, else
-- 'maker-name:<first 16 hex of md5(maker)>' ('global' for a blank maker).
-- Rows whose source record is gone stay as they are. The partial index keeps
-- the re-run on every Open cheap once nothing is bare.
CREATE INDEX IF NOT EXISTS release_identifiers_bare_maker_scope ON release_identifiers(id) WHERE identifier_scope='maker:';
WITH bare AS (
  SELECT ri.id, (
    SELECT CASE
      WHEN btrim(COALESCE(sr.raw_data->>'maker',''), E' \t\n\r　')='' THEN 'global'
      WHEN sr.raw_data->>'maker_id' ~ '^[0-9]+$' THEN 'dmm-maker:'||(sr.raw_data->>'maker_id')
      ELSE 'maker-name:'||substr(md5(sr.raw_data->>'maker'),1,16) END
    FROM source_records sr
    WHERE sr.release_id=ri.release_id AND sr.source_id=ri.source_id AND sr.entity_type='release'
    ORDER BY sr.last_seen_at DESC, sr.id DESC LIMIT 1) AS scope
  FROM release_identifiers ri WHERE ri.identifier_scope='maker:'
)
UPDATE release_identifiers ri SET identifier_scope=bare.scope
FROM bare
WHERE ri.id=bare.id AND bare.scope IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM release_identifiers x WHERE x.identifier_type=ri.identifier_type
    AND x.normalized_value=ri.normalized_value AND x.identifier_scope=bare.scope);
-- A bare row left behind because the release already carries the same code
-- under another scope is redundant.
DELETE FROM release_identifiers ri WHERE ri.identifier_scope='maker:'
  AND EXISTS (SELECT 1 FROM release_identifiers x WHERE x.release_id=ri.release_id AND x.identifier_type=ri.identifier_type
    AND x.normalized_value=ri.normalized_value AND x.identifier_scope<>'maker:');
