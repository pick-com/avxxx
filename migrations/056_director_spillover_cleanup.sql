-- Clear javdatabase parser spill-over from releases.director_name (#182).
-- An empty "Director:" row made the parser take the next row, so directors
-- were stored as "Genre(s): 4K Exclusive Distribution …". The parser is
-- fixed, but a re-crawl that now reports no director never blanks the column
-- (a degraded page must not blank data), so the stored values stay.
--
-- A director that starts with another javdatabase release-page label
-- ("Genre(s):", "Studio:", "Label:", "Series:", "Release Date:", "Runtime:",
-- ...) is never a name. Such values are set to NULL, and the matching current
-- source evidence is retired (is_current=false; the row stays as history), so
-- the next save under the field policy refills the column from a real value
-- instead of the stale spill-over. A release with an admin director override
-- is left alone. Re-run on every start: once cleaned, nothing matches.
-- Each run that changes anything reports its counts in a NOTICE.
DO $$
DECLARE
  spill constant text := '^[[:space:]]*(Genre\(s\)|Studio|Label|Series|Release Date|Runtime|Director|Content ID|DVD ID|Idol\(s\)/Actress\(es\)|Tags)[[:space:]]*:';
  n_rel bigint;
  n_ev bigint;
BEGIN
  UPDATE source_field_values sf SET is_current = false
   WHERE sf.is_current
     AND sf.field_name IN ('director', 'director_name')
     AND jsonb_typeof(sf.normalized_value) = 'string'
     AND (sf.normalized_value #>> '{}') ~ spill;
  GET DIAGNOSTICS n_ev = ROW_COUNT;

  UPDATE releases r SET director_name = NULL, updated_at = now()
   WHERE r.director_name ~ spill
     AND NOT EXISTS (SELECT 1 FROM release_overrides o WHERE o.release_id = r.id AND o.field_name = 'director_name');
  GET DIAGNOSTICS n_rel = ROW_COUNT;

  IF n_rel > 0 OR n_ev > 0 THEN
    RAISE NOTICE '056_director_spillover_cleanup: cleared % release director(s), retired % evidence row(s)', n_rel, n_ev;
  END IF;
END
$$;
