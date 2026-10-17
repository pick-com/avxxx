-- release_similar autovacuum (docs/PERFORMANCE.md, "recs_similarity write").
-- Re-run on every start: idempotent (SET only overwrites the options).
--
-- The nightly recs_similarity job now updates, inserts and deletes only the
-- rows that changed (store.SyncReleaseSimilar) instead of rewriting all of
-- them, so each run leaves a few percent of the table dead instead of all of
-- it. With the default scale factors (0.2 / 0.1) autovacuum would wait for
-- 20% of ~17.5M rows to be dead, about a week of nights, while the bloat
-- piles up. 2% (~350k rows) lets it clean up after every night or two, so
-- the next night's writes reuse the freed space instead of growing the file,
-- and keeps the planner's statistics on score current.
ALTER TABLE release_similar SET (
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_threshold = 10000,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 10000
);
