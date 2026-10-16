-- Series (FINAL_DATA_DESIGN 2.2 `series`, 9 GET /api/series; PRD 5.4 同系列)
-- and nightly completeness snapshots (AdminDashboard / AdminAudit 完整性报告).
-- Re-run on every start: idempotent.

-- One row per (series name, maker). The same series title from two makers
-- is two series. releases.series_name stays the editable source of truth
-- (crawl, admin editor, overrides); releases.series_id is derived from
-- (series_name, maker_name) by the releases_series_sync trigger below and
-- the backfill at the end of this file.
--
-- Slug rule (mirrors internal/series.SlugBase / Slug):
--   NFKC -> lower -> every run of non [a-z0-9] becomes '-' -> trim '-'.
--   A name with no ASCII letters or digits (Japanese-only titles, the common
--   case) has an empty base and gets the stable fallback 'series-<id>'.
--   A slug already taken gets the stable suffix '--<id>'.
-- Slugs never change once assigned; renaming a series creates a new row.
CREATE TABLE IF NOT EXISTS series (
  id bigserial PRIMARY KEY,
  slug text NOT NULL UNIQUE,
  name text NOT NULL,
  name_ja text,
  maker_name text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (name, maker_name)
);

ALTER TABLE releases ADD COLUMN IF NOT EXISTS series_id bigint REFERENCES series(id);
CREATE INDEX IF NOT EXISTS releases_series ON releases(series_id) WHERE series_id IS NOT NULL;

CREATE OR REPLACE FUNCTION series_slug_base(n text) RETURNS text
  LANGUAGE sql STABLE PARALLEL SAFE AS $$
  SELECT btrim(regexp_replace(lower(normalize(COALESCE(n, ''), NFKC)), '[^a-z0-9]+', '-', 'g'), '-')
$$;

-- series_ensure returns the id of the series (name, maker), creating it when
-- missing. NULL for an empty name. Under a concurrent slug race the insert is
-- skipped and NULL may be returned; the next backfill (every start) fixes it.
CREATE OR REPLACE FUNCTION series_ensure(p_name text, p_maker text) RETURNS bigint
  LANGUAGE plpgsql AS $$
DECLARE
  n text := btrim(COALESCE(p_name, ''));
  m text := btrim(COALESCE(p_maker, ''));
  sid bigint;
  cand text;
BEGIN
  IF n = '' THEN
    RETURN NULL;
  END IF;
  SELECT id INTO sid FROM series WHERE name = n AND maker_name = m;
  IF FOUND THEN
    RETURN sid;
  END IF;
  sid := nextval(pg_get_serial_sequence('series', 'id'));
  cand := series_slug_base(n);
  IF cand = '' THEN
    cand := 'series-' || sid;
  END IF;
  IF EXISTS (SELECT 1 FROM series WHERE slug = cand) THEN
    cand := cand || '--' || sid;
  END IF;
  INSERT INTO series(id, slug, name, name_ja, maker_name)
    VALUES (sid, cand, n, CASE WHEN n !~ '^[ -~]*$' THEN n END, m)
    ON CONFLICT DO NOTHING;
  IF NOT FOUND THEN
    SELECT id INTO sid FROM series WHERE name = n AND maker_name = m;
  END IF;
  RETURN sid;
END $$;

CREATE OR REPLACE FUNCTION releases_series_sync() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  NEW.series_id := series_ensure(NEW.series_name, NEW.maker_name);
  RETURN NEW;
END $$;

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgname = 'releases_series_sync' AND tgrelid = 'releases'::regclass) THEN
    CREATE TRIGGER releases_series_sync BEFORE INSERT OR UPDATE OF series_name, maker_name ON releases
      FOR EACH ROW EXECUTE FUNCTION releases_series_sync();
  END IF;
END
$$;

-- Backfill: create series in a deterministic (name, maker) order so slug
-- suffixes are stable, then point releases at them. Both steps only touch
-- rows that are out of date, so a re-run is a no-op.
DO $$
DECLARE
  d record;
BEGIN
  FOR d IN
    SELECT DISTINCT btrim(r.series_name) AS n, btrim(COALESCE(r.maker_name, '')) AS m
      FROM releases r
     WHERE btrim(COALESCE(r.series_name, '')) <> ''
       AND NOT EXISTS (SELECT 1 FROM series s WHERE s.name = btrim(r.series_name) AND s.maker_name = btrim(COALESCE(r.maker_name, '')))
     ORDER BY 1, 2
  LOOP
    PERFORM series_ensure(d.n, d.m);
  END LOOP;
END
$$;
UPDATE releases r SET series_id = s.id
  FROM series s
 WHERE btrim(COALESCE(r.series_name, '')) <> ''
   AND s.name = btrim(r.series_name) AND s.maker_name = btrim(COALESCE(r.maker_name, ''))
   AND r.series_id IS DISTINCT FROM s.id;
UPDATE releases SET series_id = NULL
 WHERE series_id IS NOT NULL AND btrim(COALESCE(series_name, '')) = '';

-- i18n: series names are translatable (entity_type 'series', field 'name';
-- internal/i18n.EntitySeries). Extends 015's CHECK, i18n_source and
-- i18n_sources; 015 re-creates the older versions on every start and this
-- file (applied after it) restores these.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'translations_entity_type_check'
               AND pg_get_constraintdef(oid) NOT LIKE '%series%') THEN
    ALTER TABLE translations DROP CONSTRAINT translations_entity_type_check;
    ALTER TABLE translations ADD CONSTRAINT translations_entity_type_check
      CHECK (entity_type IN ('release','performer','tag','series'));
  END IF;
END
$$;

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
  ELSIF et = 'series' AND f = 'name' AND eid ~ '^[0-9]{1,18}$' THEN
    RETURN (SELECT name FROM series WHERE id = eid::bigint);
  END IF;
  RETURN NULL;
END $$;

CREATE OR REPLACE VIEW i18n_sources AS
  SELECT 'release'::text AS entity_type, r.id::text AS entity_id, f.field, f.value
    FROM releases r, LATERAL (VALUES ('title', r.canonical_title), ('synopsis', r.synopsis)) f(field, value)
    WHERE r.merged_into_id IS NULL AND COALESCE(f.value, '') <> ''
  UNION ALL
  SELECT 'performer', p.id::text, f.field, f.value
    FROM performers p, LATERAL (VALUES ('name', p.canonical_name), ('biography', p.biography)) f(field, value)
    WHERE p.merged_into_id IS NULL AND COALESCE(f.value, '') <> ''
  UNION ALL
  SELECT 'tag', normalized_tag, 'name', min(tag) FROM release_tags GROUP BY normalized_tag
  UNION ALL
  SELECT 'series', s.id::text, 'name', s.name FROM series s;

-- Completeness snapshots (internal/completeness): one row per day and
-- entity type, written by the nightly 'completeness_snapshot' job
-- (cmd/scheduler enqueues, cmd/audit runs). fields maps report field ->
-- entities with the field present; sources maps source name -> entities
-- with a record from that source. GET /api/admin/reports/completeness reads
-- the last N rows as its trend.
CREATE TABLE IF NOT EXISTS completeness_snapshots (
  snapshot_date date NOT NULL,
  entity_type text NOT NULL CHECK (entity_type IN ('release','performer')),
  total bigint NOT NULL DEFAULT 0,
  required_complete bigint NOT NULL DEFAULT 0,
  fields jsonb NOT NULL DEFAULT '{}'::jsonb,
  sources jsonb NOT NULL DEFAULT '{}'::jsonb,
  job_id bigint REFERENCES jobs(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (snapshot_date, entity_type)
);
CREATE INDEX IF NOT EXISTS completeness_snapshots_recent ON completeness_snapshots(entity_type, snapshot_date DESC);
