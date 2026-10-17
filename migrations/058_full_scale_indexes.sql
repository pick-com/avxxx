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

-- GET /api/releases?sort=runtime (发现 时长): the expression and directions
-- match releaseSorts["runtime"] (internal/store/catalog_query.go), like
-- releases_public_newest / _oldest do for the date sorts. Without it every
-- page sorted all published releases: 32 ms, or 75 ms with a common tag;
-- with it 0.02 ms / 0.5 ms (a rare tag still starts from release_tags).
-- Per-release rating aggregates (GET /api/recs hydration via
-- releaseRatingAggSQL, the rating summary, the site-wide prior, 高分短片)
-- read every active vote of a release. Covering them makes that an
-- index-only scan: hydrating ~160 recs candidates 98 ms -> 9 ms, most of
-- it heap reads for the busiest releases' votes.
CREATE INDEX IF NOT EXISTS ratings_active_release
  ON ratings (release_id) INCLUDE (weight, score)
  WHERE status='active';

-- Live per-performer work counts (GET /api/performers page rows, the
-- performer profile stats) probe releases once per credit to keep only
-- published, unmerged works and read their date. For the busiest
-- performers (1.5k works each, and sort=works / heat list exactly those)
-- that was ~8k heap reads per page: 166 ms. Covering the probe makes it
-- index-only: 9 ms (the performer works page 209 ms -> 21 ms).
CREATE INDEX IF NOT EXISTS releases_public_id_date
  ON releases (id) INCLUDE (release_date)
  WHERE status='published' AND merged_into_id IS NULL;

-- Identifier lookups by value alone (GET /api/releases?q= product codes,
-- the legacy release search, release-by-identifier, admin search): the
-- existing indexes lead with identifier_type, so each one seq-scanned all
-- identifiers (600k rows: 15-19 ms, in parallel workers). Now 0.2-0.9 ms.
CREATE INDEX IF NOT EXISTS release_identifiers_value
  ON release_identifiers (normalized_value);

CREATE INDEX IF NOT EXISTS releases_public_runtime
  ON releases ((COALESCE(runtime_minutes,0)) DESC, id DESC)
  WHERE status='published' AND merged_into_id IS NULL;
