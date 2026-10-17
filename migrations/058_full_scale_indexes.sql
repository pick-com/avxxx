-- Indexes from the production-volume performance pass (docs/PERFORMANCE.md,
-- 50k performers / 300k releases / 200k users). Re-run on every start:
-- idempotent.

-- GET /api/releases/{slug}/comments?sort=top: the expression and directions
-- match commentTopOrder (internal/store/interactions.go), so a page of a
-- busy release reads its first rows in order instead of sorting every
-- top-level comment (and hashing the users table to join them): 53 ms ->
-- 1 ms for a release with 5.7k comments.
CREATE INDEX IF NOT EXISTS comments_top
  ON comments (release_id, (CASE WHEN status='deleted' THEN 0 ELSE likes END) DESC, created_at DESC, id DESC)
  WHERE parent_id IS NULL;
