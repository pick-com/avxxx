-- Performers waiting for their first performer_stats row (docs/PERFORMANCE.md,
-- "GET /api/performers?sort=works|newest_work"). Re-run on every start:
-- idempotent.
--
-- GET /api/performers?sort=works|newest_work pages performers that have a
-- performer_stats row on the hourly counts, and the ones that do not yet
-- (new, reactivated or unmerged since the last RefreshPerformerStats) on
-- live counts. Finding those was a hash anti-join of every active performer
-- against performer_stats on every request: 15-20 ms warm at 50k
-- performers, 50-70 ms p50 under load. This table holds a superset of them,
-- usually a handful of rows, so the branch reads it instead.
--
-- Invariant: every active, unmerged performer without a performer_stats row
-- has a row here.
--   - The trigger adds a performer when it is inserted active and unmerged,
--     or updated to active and unmerged from anything else. Those are the
--     only ways to become a listed performer.
--   - performer_stats rows are only removed by RefreshPerformerStats (for
--     performers that were not listed when it ran) and by ON DELETE CASCADE.
--   - RefreshPerformerStats deletes rows here only for performers that have
--     a performer_stats row once its own writes are done.
-- A row whose performer has a stats row (or is no longer listed) is harmless:
-- the query still requires ps.performer_id IS NULL and the listing filters.
CREATE TABLE IF NOT EXISTS performer_stats_pending (
  performer_id bigint PRIMARY KEY REFERENCES performers(id) ON DELETE CASCADE
);

CREATE OR REPLACE FUNCTION performer_stats_pending_mark() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'UPDATE' AND OLD.status = 'active' AND OLD.merged_into_id IS NULL THEN
    RETURN NULL; -- already listed: it has a stats row or is pending already
  END IF;
  INSERT INTO performer_stats_pending(performer_id) VALUES (NEW.id) ON CONFLICT DO NOTHING;
  RETURN NULL;
END $$;

DROP TRIGGER IF EXISTS performers_stats_pending ON performers;
CREATE TRIGGER performers_stats_pending
  AFTER INSERT OR UPDATE OF status, merged_into_id ON performers
  FOR EACH ROW WHEN (NEW.status = 'active' AND NEW.merged_into_id IS NULL)
  EXECUTE FUNCTION performer_stats_pending_mark();

-- Backfill: listed performers without a stats row (all of them before the
-- first RefreshPerformerStats; none in steady state).
INSERT INTO performer_stats_pending(performer_id)
SELECT p.id FROM performers p
WHERE p.status = 'active' AND p.merged_into_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM performer_stats ps WHERE ps.performer_id = p.id)
ON CONFLICT DO NOTHING;
