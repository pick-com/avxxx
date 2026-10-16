-- Search (PRD 4.5, 11 AdminSearch; tech plan 3.4, 3.13): synonyms and
-- redirects applied before matching, product-code keys, pg_trgm indexes for
-- title / alias / translation matching, the daily search-term aggregate and
-- the hot-search board. Re-run on every start: every statement is idempotent.

-- pg_trgm accelerates the ILIKE matching of titles, aliases and reviewed
-- translations and adds fuzzy (similarity) recall. It is a trusted
-- extension (PostgreSQL 13+), but a managed database may still refuse it:
-- then the migration carries on without it and internal/store/search.go
-- falls back to plain ILIKE (logged once as "search: pg_trgm unavailable").
DO $$
BEGIN
  CREATE EXTENSION IF NOT EXISTS pg_trgm;
EXCEPTION WHEN OTHERS THEN
  RAISE NOTICE 'pg_trgm unavailable (%); search falls back to ILIKE', SQLERRM;
END
$$;

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm') THEN
    CREATE INDEX IF NOT EXISTS releases_title_trgm ON releases USING gin (canonical_title gin_trgm_ops)
      WHERE merged_into_id IS NULL;
    CREATE INDEX IF NOT EXISTS releases_title_ja_trgm ON releases USING gin (title_ja gin_trgm_ops)
      WHERE merged_into_id IS NULL AND title_ja IS NOT NULL;
    CREATE INDEX IF NOT EXISTS performers_name_trgm ON performers USING gin (canonical_name gin_trgm_ops)
      WHERE merged_into_id IS NULL;
    CREATE INDEX IF NOT EXISTS performers_name_ja_trgm ON performers USING gin (canonical_name_ja gin_trgm_ops)
      WHERE merged_into_id IS NULL AND canonical_name_ja IS NOT NULL;
    CREATE INDEX IF NOT EXISTS performer_aliases_name_trgm ON performer_aliases USING gin (normalized_name gin_trgm_ops);
    CREATE INDEX IF NOT EXISTS translations_reviewed_trgm ON translations USING gin (reviewed_value gin_trgm_ops)
      WHERE reviewed_value IS NOT NULL AND field IN ('title','name');
    CREATE INDEX IF NOT EXISTS release_tags_tag_trgm ON release_tags USING gin (tag gin_trgm_ops);
  END IF;
END
$$;

-- search_code_keys mirrors internal/identity.CodeKeys for stored
-- identifiers: NFKC, lower case, only [a-z0-9] kept; letters+digits codes
-- become "<prefix><letters>-<digits de-padded to >= 3>" plus, with a
-- numeric label prefix (DMM content ids), the loose key without it.
-- "JUQ-743", "juq743" and "juq00743" all give {juq-743}.
CREATE OR REPLACE FUNCTION search_code_keys(v text) RETURNS text[]
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
  SELECT CASE
    WHEN raw = '' THEN '{}'::text[]
    WHEN m IS NULL THEN ARRAY[raw]
    WHEN m[1] = '' THEN ARRAY[m[2] || '-' || num || m[4]]
    ELSE ARRAY[m[1] || m[2] || '-' || num || m[4], m[2] || '-' || num || m[4]]
  END
  FROM (
    SELECT raw, m,
      CASE WHEN m IS NULL THEN NULL
           WHEN length(ltrim(m[3], '0')) >= 3 THEN ltrim(m[3], '0')
           ELSE lpad(ltrim(m[3], '0'), 3, '0') END AS num
    FROM (
      SELECT raw, regexp_match(raw, '^([0-9]*)([a-z]+)([0-9]+)([a-z]?)$') AS m
      FROM (SELECT regexp_replace(lower(normalize(v, NFKC)), '[^a-z0-9]', '', 'g') AS raw) a
    ) b
  ) c
$$;
CREATE INDEX IF NOT EXISTS release_identifiers_code_keys ON release_identifiers USING gin (search_code_keys(normalized_value));

-- Synonyms (AdminSearch 同义词): a query, or a space-separated token of it,
-- equal to term is replaced by canonical before matching. term and
-- canonical are stored in internal/search.NormalizeQuery form.
CREATE TABLE IF NOT EXISTS search_synonyms (
  id bigserial PRIMARY KEY,
  term text NOT NULL UNIQUE CHECK (char_length(term) BETWEEN 1 AND 200),
  canonical text NOT NULL CHECK (char_length(canonical) BETWEEN 1 AND 200),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (term <> canonical)
);

-- Redirects (AdminSearch 跳帮助页 / 建预约页): a query equal to term returns
-- target_url (a site path or an https URL) as `redirect` next to the
-- results; the client decides whether to navigate.
CREATE TABLE IF NOT EXISTS search_redirects (
  id bigserial PRIMARY KEY,
  term text NOT NULL UNIQUE CHECK (char_length(term) BETWEEN 1 AND 200),
  target_url text NOT NULL CHECK (char_length(target_url) BETWEEN 1 AND 2000),
  reason text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Terms never shown as hot searches (on top of moderation_words).
CREATE TABLE IF NOT EXISTS search_blocklist (
  id bigserial PRIMARY KEY,
  term text NOT NULL UNIQUE CHECK (char_length(term) BETWEEN 1 AND 200),
  reason text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Daily search-term aggregate, rebuilt hourly by the 'search_stats' job
-- (internal/search) from `search` events: no user or device ids. q_norm is
-- NFKC + lower case + collapsed spaces; queries under 2 runes are dropped.
-- lang is events.ui_lang ('' when unknown). clicks counts searches that
-- ended with a click, first_page_clicks those at rank <= 20, rescued those
-- the server's modifier-stripped retry answered (props.rescued).
CREATE TABLE IF NOT EXISTS search_query_daily (
  day date NOT NULL,
  q_norm text NOT NULL,
  lang text NOT NULL DEFAULT '',
  searches integer NOT NULL DEFAULT 0 CHECK (searches >= 0),
  zero_results integer NOT NULL DEFAULT 0 CHECK (zero_results >= 0),
  clicks integer NOT NULL DEFAULT 0 CHECK (clicks >= 0),
  first_page_clicks integer NOT NULL DEFAULT 0 CHECK (first_page_clicks >= 0),
  rescued integer NOT NULL DEFAULT 0 CHECK (rescued >= 0),
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (day, q_norm, lang)
);
CREATE INDEX IF NOT EXISTS search_query_daily_zero ON search_query_daily (day, q_norm) WHERE zero_results > 0;

-- Hot searches (热搜词 / 大家都在搜): top 20 over the last 24 h by distinct
-- sessions, recomputed hourly; only the latest board per period is kept.
CREATE TABLE IF NOT EXISTS search_trending (
  period text NOT NULL CHECK (period IN ('24h')),
  rank integer NOT NULL CHECK (rank >= 1),
  q_norm text NOT NULL,
  searches integer NOT NULL CHECK (searches >= 0),
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (period, rank),
  UNIQUE (period, q_norm)
);

-- The job reads `search` events by time; events_event_ts covers it.
