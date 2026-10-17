-- performer_similar autovacuum (docs/PERFORMANCE.md, "performer_similar
-- write"). Re-run on every start: idempotent (SET only overwrites the
-- options).
--
-- Like release_similar (059), the nightly recs_similarity job now writes
-- only the performer_similar rows that changed (store.SyncPerformerSimilar)
-- instead of deleting and re-copying all ~2.4M of them, so each night leaves
-- a few percent of the table dead. The default scale factors (0.2 / 0.1)
-- would wait for ~480k dead rows, several nights, while the bloat piles up;
-- 2% (~50k rows) cleans up after every night or two so the next night's
-- writes reuse the space, and keeps the score statistics current.
ALTER TABLE performer_similar SET (
  autovacuum_vacuum_scale_factor = 0.02,
  autovacuum_vacuum_threshold = 10000,
  autovacuum_analyze_scale_factor = 0.02,
  autovacuum_analyze_threshold = 10000
);
