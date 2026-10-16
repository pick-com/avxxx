-- Small follow-ups to #99–#103. Re-run on every start: every statement is idempotent.

-- 收藏夹 manual order (NavFavorites 拖动排序). position orders a playlist's
-- items ascending; new items get min(position) - 1 so they appear first,
-- which matches the previous newest-first order. Rows without a position
-- (all rows on the first run, or rows written by an older binary during a
-- rolling deploy) are numbered newest-first ahead of the ordered ones on the
-- next start. Reordering rewrites 1..n.
ALTER TABLE playlist_items ADD COLUMN IF NOT EXISTS position integer;
WITH unordered AS (
  SELECT playlist_id, release_id,
         row_number() OVER (PARTITION BY playlist_id ORDER BY created_at DESC, release_id DESC)::int AS rn,
         count(*) OVER (PARTITION BY playlist_id)::int AS k
  FROM playlist_items WHERE position IS NULL
), base AS (
  SELECT playlist_id, min(position) AS lo FROM playlist_items WHERE position IS NOT NULL GROUP BY playlist_id
)
UPDATE playlist_items i SET position = u.rn - u.k - 1 + COALESCE(b.lo, u.k + 1)
FROM unordered u LEFT JOIN base b ON b.playlist_id = u.playlist_id
WHERE i.playlist_id = u.playlist_id AND i.release_id = u.release_id;
CREATE INDEX IF NOT EXISTS playlist_items_order ON playlist_items (playlist_id, position);
