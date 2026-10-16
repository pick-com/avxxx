-- code_match_key mirrors internal/catalog.CodeMatchKey for stored DVD codes:
-- NFKC, only [A-Z0-9] kept, letters+digits(+letters) codes become
-- "<PREFIX>-<number without leading zeros>[-<SUFFIX>]". Fallback-only
-- sources (ThePornDB) drop the zero padding the catalog keeps
-- ("HEYZO-0123" -> "HEYZO-123"), so store.SaveCatalogRecord and the
-- cross-check job match their records by this key, and only when it names
-- exactly one release. Re-run on every start: every statement is idempotent.
CREATE OR REPLACE FUNCTION code_match_key(v text) RETURNS text
  LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
  SELECT CASE
    WHEN m IS NULL THEN upper(raw)
    ELSE upper(m[1] || '-' || COALESCE(NULLIF(ltrim(m[2], '0'), ''), '0')
      || CASE WHEN m[3] = '' THEN '' ELSE '-' || m[3] END)
  END
  FROM (
    SELECT raw, regexp_match(raw, '^([0-9]*[a-z]+)([0-9]+)([a-z]*)$') AS m
    FROM (SELECT regexp_replace(lower(normalize(v, NFKC)), '[^a-z0-9]', '', 'g') AS raw) a
  ) b
$$;
CREATE INDEX IF NOT EXISTS release_identifiers_dvd_code_match_key ON release_identifiers (code_match_key(normalized_value))
  WHERE identifier_type = 'dvd_code';
