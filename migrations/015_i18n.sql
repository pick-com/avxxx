-- Multi-language content (PRD 8, tech plan 3.10 / 3.13): content
-- translations with a review workflow, a glossary and UI strings.
-- The language list must match internal/i18n.Languages.

-- i18n_hash fingerprints a source value; internal/i18n.SourceHash computes
-- the same hex(sha256(utf8)) in Go. NULL hashes like ''.
CREATE OR REPLACE FUNCTION i18n_hash(t text) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE
  AS $$ SELECT encode(sha256(convert_to(COALESCE(t, ''), 'UTF8')), 'hex') $$;

-- i18n_source returns the current source text of one translatable field
-- (NULL when the entity or field does not exist). It uses the primary key /
-- release_tags_tag index, so per-row calls stay cheap. Field names match
-- internal/i18n.Fields.
CREATE OR REPLACE FUNCTION i18n_source(et text, eid text, f text) RETURNS text
  LANGUAGE plpgsql STABLE AS $$
BEGIN
  IF et = 'release' AND eid ~ '^[0-9]{1,18}$' THEN
    RETURN (SELECT CASE f WHEN 'title' THEN canonical_title WHEN 'synopsis' THEN synopsis END
              FROM releases WHERE id = eid::bigint);
  ELSIF et = 'performer' AND eid ~ '^[0-9]{1,18}$' THEN
    RETURN (SELECT CASE f WHEN 'name' THEN canonical_name WHEN 'biography' THEN biography END
              FROM performers WHERE id = eid::bigint);
  ELSIF et = 'tag' AND f = 'name' THEN
    RETURN (SELECT min(tag) FROM release_tags WHERE normalized_tag = eid);
  END IF;
  RETURN NULL;
END $$;

-- One row per (entity, field, language). value is the working text;
-- reviewed_value is the last approved text and the only one served publicly,
-- so editing a reviewed translation (back to draft) keeps the old approved
-- text live until the new one is reviewed. source_hash is i18n_hash of the
-- source field when value was written; a mismatch on a reviewed row marks it
-- stale (still served). entity_id is the numeric id for releases/performers
-- and normalized_tag for tags. submitted_at marks a draft awaiting review.
CREATE TABLE IF NOT EXISTS translations (
  id bigserial PRIMARY KEY,
  entity_type text NOT NULL CHECK (entity_type IN ('release','performer','tag')),
  entity_id text NOT NULL,
  field text NOT NULL,
  lang text NOT NULL CHECK (lang IN ('zh-CN','zh-TW','en','ja','ko','vi','es','th')),
  value text NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('machine','draft','reviewed','stale')),
  source_hash text NOT NULL,
  reviewed_value text,
  submitted_at timestamptz,
  reviewed_at timestamptz,
  updated_by text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entity_type, entity_id, field, lang),
  CHECK (status NOT IN ('reviewed','stale') OR reviewed_value IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS translations_queue ON translations (lang, status, updated_at);

CREATE TABLE IF NOT EXISTS glossary_terms (
  id bigserial PRIMARY KEY,
  term text NOT NULL,
  lang text NOT NULL CHECK (lang IN ('zh-CN','zh-TW','en','ja','ko','vi','es','th')),
  translation text NOT NULL,
  note text NOT NULL DEFAULT '',
  updated_by text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (term, lang)
);

CREATE TABLE IF NOT EXISTS ui_strings (
  key text NOT NULL,
  lang text NOT NULL CHECK (lang IN ('zh-CN','zh-TW','en','ja','ko','vi','es','th')),
  value text NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('machine','draft','reviewed')),
  updated_by text,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (key, lang)
);
CREATE INDEX IF NOT EXISTS ui_strings_public ON ui_strings (lang) WHERE status='reviewed';

-- Every translatable source field (admin task lists, "missing" filter).
-- Field names match internal/i18n.Fields.
CREATE OR REPLACE VIEW i18n_sources AS
  SELECT 'release'::text AS entity_type, r.id::text AS entity_id, f.field, f.value
    FROM releases r, LATERAL (VALUES ('title', r.canonical_title), ('synopsis', r.synopsis)) f(field, value)
    WHERE r.merged_into_id IS NULL AND COALESCE(f.value, '') <> ''
  UNION ALL
  SELECT 'performer', p.id::text, f.field, f.value
    FROM performers p, LATERAL (VALUES ('name', p.canonical_name), ('biography', p.biography)) f(field, value)
    WHERE p.merged_into_id IS NULL AND COALESCE(f.value, '') <> ''
  UNION ALL
  SELECT 'tag', normalized_tag, 'name', min(tag) FROM release_tags GROUP BY normalized_tag;
