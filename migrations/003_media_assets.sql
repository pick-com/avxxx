CREATE TABLE IF NOT EXISTS media_assets (
  id bigserial PRIMARY KEY,
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  entity_id bigint NOT NULL,
  asset_type text NOT NULL CHECK(asset_type IN ('cover','gallery','avatar','thumbnail')),
  storage_provider text NOT NULL DEFAULT 's3', object_key text NOT NULL,
  source_url text, content_sha256 text NOT NULL, mime_type text NOT NULL,
  width integer, height integer, byte_size bigint NOT NULL DEFAULT 0,
  is_original boolean NOT NULL DEFAULT true, retained_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(storage_provider, object_key),
  UNIQUE(entity_type, entity_id, asset_type, content_sha256)
);
CREATE INDEX IF NOT EXISTS media_assets_entity ON media_assets(entity_type, entity_id, asset_type);
CREATE INDEX IF NOT EXISTS media_assets_retention ON media_assets(retained_until) WHERE retained_until IS NOT NULL;
