-- Home curation and site announcements (PRD 4.1 主推横幅, PRD 11 AdminOps,
-- tech plan 3.2 home_slots / 3.11 运营排期). Re-run on every start: every
-- statement is idempotent.
--
-- home_slots: scheduled curated entries of the home page. slot_type 'hero'
-- is the 主推横幅 carousel, 'shelf' a curated card row under it. A slot
-- points at exactly one target: a release, a series, or a link (a site path
-- "/..." or an https:// URL). texts holds the per-language title / subtitle
-- overrides: {"zh-CN": {"title": "...", "subtitle": "..."}, "en": {...}};
-- without one the target's own (localized) title is shown. langs / regions
-- target the slot ({} = everyone); regions are ISO 3166-1 alpha-2 codes. A
-- slot is live while enabled and starts_at <= now < ends_at (NULL = open
-- ended). Higher priority first; AdminOps drag reorder rewrites priority.
-- Only slots whose release is published and unmerged, or whose series has
-- such a release, are served publicly (GET /api/home/slots).
CREATE TABLE IF NOT EXISTS home_slots (
  id bigserial PRIMARY KEY,
  slot_type text NOT NULL CHECK (slot_type IN ('hero','shelf')),
  target_type text NOT NULL CHECK (target_type IN ('release','series','link')),
  release_id bigint REFERENCES releases(id) ON DELETE CASCADE,
  series_id bigint REFERENCES series(id) ON DELETE CASCADE,
  link_url text,
  texts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(texts) = 'object'),
  langs text[] NOT NULL DEFAULT '{}',
  regions text[] NOT NULL DEFAULT '{}',
  starts_at timestamptz NOT NULL,
  ends_at timestamptz,
  priority integer NOT NULL DEFAULT 0,
  enabled boolean NOT NULL DEFAULT true,
  created_by text NOT NULL DEFAULT '',
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT home_slots_target CHECK (
    (target_type = 'release' AND release_id IS NOT NULL AND series_id IS NULL AND link_url IS NULL) OR
    (target_type = 'series' AND series_id IS NOT NULL AND release_id IS NULL AND link_url IS NULL) OR
    (target_type = 'link' AND link_url IS NOT NULL AND release_id IS NULL AND series_id IS NULL)),
  CONSTRAINT home_slots_window CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS home_slots_live ON home_slots (slot_type, priority DESC, starts_at DESC) WHERE enabled;
CREATE INDEX IF NOT EXISTS home_slots_window_idx ON home_slots (starts_at, ends_at);

-- announcements: the site-wide announcement bar in the app shell. severity
-- info | warning | critical; texts per language: {"zh-CN": {"body": "...",
-- "link_label": "..."}}. dismissible = false keeps it on screen (e.g. an
-- outage notice). Same langs / regions / window semantics as home_slots.
CREATE TABLE IF NOT EXISTS announcements (
  id bigserial PRIMARY KEY,
  severity text NOT NULL DEFAULT 'info' CHECK (severity IN ('info','warning','critical')),
  texts jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(texts) = 'object'),
  link_url text,
  langs text[] NOT NULL DEFAULT '{}',
  regions text[] NOT NULL DEFAULT '{}',
  starts_at timestamptz NOT NULL,
  ends_at timestamptz,
  dismissible boolean NOT NULL DEFAULT true,
  enabled boolean NOT NULL DEFAULT true,
  created_by text NOT NULL DEFAULT '',
  updated_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT announcements_window CHECK (ends_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS announcements_live ON announcements (starts_at DESC) WHERE enabled;

-- Signed-in dismissals (visitors keep theirs in localStorage).
CREATE TABLE IF NOT EXISTS announcement_dismissals (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  announcement_id bigint NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
  dismissed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, announcement_id)
);
