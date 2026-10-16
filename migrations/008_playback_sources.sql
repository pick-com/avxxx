-- Playable sources per release (tech plan 3.2, PRD 5.2). release_media_sources
-- stays as crawl-side evidence; only reviewed mappings are written here.
--
-- version_kind (PRD 5.2): main / high_quality / dub share playback progress;
-- alt (duration differs from the main version by more than 2%) does not.
-- is_alt_version mirrors version_kind = 'alt' and is what the player reads.
CREATE TABLE IF NOT EXISTS playback_sources (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('official_embed','official_preview','external_link','hls_owned')),
  provider text NOT NULL,                 -- licensor / partner identifier
  label text NOT NULL,                    -- shown in the UI: main / 4K / preview
  url text,                               -- embed or external URL; NULL for hls_owned
  storage_prefix text,                    -- [B] object storage prefix
  max_height integer, has_hdr boolean NOT NULL DEFAULT false,
  duration_seconds integer,
  audio_langs text[] NOT NULL DEFAULT '{}', subtitle_langs text[] NOT NULL DEFAULT '{}',
  version_kind text NOT NULL DEFAULT 'main' CHECK (version_kind IN ('main','high_quality','dub','alt')),
  is_alt_version boolean NOT NULL DEFAULT false,   -- duration differs >2%: progress not shared
  priority integer NOT NULL DEFAULT 100,
  region_allow text[], region_deny text[],         -- ISO 3166-1 alpha-2, upper case
  license_ref text,                       -- contract / license id, checked on copyright claims
  health_score real NOT NULL DEFAULT 1.0,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','degraded','disabled')),
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT playback_sources_alt_consistent CHECK (is_alt_version = (version_kind = 'alt'))
);
CREATE INDEX IF NOT EXISTS playback_sources_release ON playback_sources(release_id, status, priority);
