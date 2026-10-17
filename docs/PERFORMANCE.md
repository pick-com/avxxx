# Performance

This file covers the performance passes of 2026-10-17: what they found,
what changed, and how to rerun them. "Full-scale pass" has the
production-volume measurements: 50k performers, 300k releases and 200k
users. The earlier sections ("Findings and fixes") come from small
throwaway databases of 15k to 26k rows. Read those as before/after ratios
and plan shapes.

## Full-scale pass (production volume)

**Setup.** `scripts/perf/setup.sh` with 50,000 performers, 300,000
releases and 200,000 users (`max_wal_size=1GB`): 4.4 GB, about 25M rows.
The heaviest tables are watch_history (4.1M rows), events (3.4M), release_tags (1.8M),
danmaku (1.5M), ratings (1.0M) and comments (720k). Postgres 17 ran in Docker on the
development Mac. `recs_trending` / `recs_similarity` ran once before any
measurement, as production would have.

- **Latency.** `loadgen -n 200 -c 8` over `endpoints.txt`, against an API
  started with `MEDIA_LINK_ON_DEMAND=false`. "Before" is origin/main
  (fe986f3) on the freshly seeded database. "After" is this branch, with
  migration 058 applied.
- **Jobs.** `go run ./cmd/audit -<job>`, timed with `/usr/bin/time -l` for
  the peak RSS.
- **SSR.** The built web server (`node server.mjs`) with
  `API_INTERNAL_URL` set to the API, `loadgen -c 4` over
  `ssr-endpoints.txt`, with a development age-gate cookie.

Absolute numbers are from one laptop, with 8 concurrent clients competing
for the same CPUs as Postgres. Compare before against after, row by row.

### Latency, before / after (ms, 8 concurrent)

| Endpoint | p50 / p95 / p99 before | p50 / p95 / p99 after | Fix |
|---|---|---|---|
| `recs?slot=home` (visitor) | 62 / 91 / 104 | 13 / 25 / 54 | ratings index, prior cache |
| `recs?slot=home` (signed in) | 83 / 99 / 113 | 29 / 42 / 49 | same |
| `trending?period=week` | 30 / 38 / 41 | 8.7 / 12 / 13 | ratings index |
| `recs?slot=related` | 124 / 307 / 665 | 41 / 61 / 84 | same |
| `releases/{r}/comments?sort=top` | 2.6 / 34 / 109 | 2.6 / 10 / 13 | `comments_top` |
| `releases?tag=&sort=runtime` | 95 / 305 / 459 | 8.2 / 19 / 23 | `releases_public_runtime` |
| `releases?q=` | 82 / 153 / 174 | 2.2 / 3.8 / 5.1 | `release_identifiers_value` |
| `catalog/search?q=` (broad word) | 888 / 988 / 1080 | 455 / 490 / 510 | fuzzy guard |
| `catalog/search?…&counts=1` | 1359 / 1534 / 1750 | 556 / 615 / 701 | same |
| `sitemap/counts` | 2373 / 3841 / 9353, 191 errors | 0.1 / 0.3 / 0.5, 0 errors | cache, GROUP BY |
| `sitemap/tags` | 10 / 166 / 906, 197 errors | 0.3 / 0.9 / 1.1, 0 errors | first-page cache |
| `performers?sort=works` | 77 / 86 / 96 | 67 / 98 / 136 | `performer_stats_pending`, see item 1 |
| `performers?sort=newest_work` | 48 / 59 / 63 | 49 / 55 / 61 | same |
| `performers/{p}` | 6.6 / 14 / 42 | 4.4 / 13 / 32 | `releases_public_id_date` |
| `sitemap/releases` (5000 rows) | 152 / 221 / 231 | 147 / 199 / 218 | 51 ms alone; a bulk page |

Every other endpoint stayed under 20 ms p95, before and after:

- home slots and 高分短片 (cached);
- release detail, cast, play-config and playback;
- comments newest;
- performer releases;
- releases newest / by tag / with the HD + subtitle facet;
- performers by name and heat;
- upcoming and catalog facets;
- product-code search;
- history, history `?q=`, `/api/me/clips` and favorites (signed in).

The errors were Postgres failing to allocate parallel-query shared memory:
`could not resize shared memory segment … No space left on device`.
See "Infrastructure" below.

### Slow queries and fixes

Each fix has its own commit and test. All of them keep behaviour, except
three caches, each noted in its item.

1. **Recs hydration (`RecInfos`): 98 → 9 ms.**
   - Cause: summing every active vote of ~160 candidates. Busy releases
     have thousands of votes, and 66 ms went to heap reads.
   - Fix: `ratings_active_release (release_id) INCLUDE (weight, score)
     WHERE status='active'` makes the aggregate index-only.
2. **Rating prior: no more stacked scans.** `RatingPrior`, about 170 ms
   over 1M ratings, is cached for 10 minutes, but every request that
   arrived after expiry rescanned it until one of them stored the result.
   `staleCache` lets one caller reload while the others keep the previous
   value.
3. **Top comments: 53 → 1.1 ms** on a release with 5.7k comments.
   - Before: every top-level comment was sorted, with a hash join over all
     users.
   - Now: `comments_top` matches `commentTopOrder` exactly.
4. **Runtime sort: 32 → 0.02 ms untagged, 75 → 0.5 ms with a common tag.**
   `releases_public_runtime` matches `releaseSorts["runtime"]`.
5. **`?q=` product codes: 19 → 0.9 ms.** Lookups by `normalized_value`
   seq-scanned 600k identifiers, because every index led with
   `identifier_type`. Fix: `release_identifiers_value`.
6. **Live work counts: 166 → 9 ms per performer page.** Covers performer
   pages, the works / heat sorts and profile stats. Each credit probed the
   releases heap. `releases_public_id_date (id) INCLUDE (release_date)`,
   partial on published and unmerged, makes the probe index-only.
7. **Catalog search: about 2× faster.** A broad word matched about 27k
   titles. The query then also ran the trigram branches and joined every
   candidate back to releases to rank it.
   - The fuzzy branches now run only while the ILIKE branch has fewer rows
     than the page or the count cap (`fuzzyGuard`). Fuzzy rows always rank
     after ILIKE rows.
   - Release candidates carry their own ranking columns.
   - Checks: byte-identical responses on 20 sample queries, plus
     `TestSearchFuzzyGuardMatchesReferenceIntegration` against the old SQL.
8. **Sitemap counts: 1.7 s → 0.9 s uncached, then cached.**
   - The tag count is grouped instead of `count(DISTINCT)`, which sorted
     1.75M rows.
   - Cache: 5 minutes, one reload at a time. The counts can now be up to
     5 minutes old, like the lists' `Cache-Control`.
9. **Sitemap tags: first page cached.** The default page, which is what the
   web asks for, is cached for 5 minutes.
10. **`/api/tags` cached for 5 minutes, per limit.** It took about 100 ms,
    counting every tag row, and SSR home asks on every render.
11. **`cachedJSON` single build.** 高分短片 and home slots now build once
    per miss instead of once per concurrent request. 高分短片 costs
    250–400 ms per language per expiry.

Migration `058_full_scale_indexes.sql` adds:

| Index | Size at this volume |
|---|---|
| `comments_top` | 28 MB |
| `ratings_active_release` | 30 MB |
| `releases_public_id_date` | 9 MB |
| `release_identifiers_value` | 15 MB |
| `releases_public_runtime` | 9 MB |

The migration uses plain `CREATE INDEX IF NOT EXISTS`, like the others,
because migrations run in one batch at API start.

### Jobs

| Job | Before | After |
|---|---|---|
| `recs_similarity` | **602 s, 15.4 GB RSS** | 230 s, 4.9 GB RSS |
| `recs_trending` | 58 s cold / 36 s warm | 35 s |
| `watch_stats` (hourly) | **564 s** | 21–26 s |
| `site_metrics` | 10 s (2 s warm) | 2 s |
| `completeness` | 5 s | 5 s |
| `search_stats` | 0.6 s | 0.3 s |
| `media_fetch` / `media_purge` | < 1 s | < 1 s |
| `release_curves` | 715 s first run (no watermark); 0.7 s incremental | unchanged |

`media_fetch` / `media_purge` have nothing to do here: no
`CATALOG_CONTACT`, no image sources, no assets.

**`recs_similarity` fixes:**

- **`CoWatch`** kept a map of every co-watched pair, about 50M entries. It
  now walks one item at a time over dense arrays and keeps that item's
  TopK: 41 → 2.7 s. The float operations run in the same order, so the
  results are bit-identical.
- **`ContentNeighbors`** kept a candidate-sized backing array behind every
  entity's TopK slice, about 20 GB live. It now interns features, scores
  into a dense array and copies exactly TopK: 177 → 20 s. Feature ids are
  in name order, so the scores no longer depend on map order.
- **Release side.** It streams its matches with kind only
  (`EachContentNeighbors`, `KindOnly`).
- **Performer side.** It keeps 3 shared features per kind, which the
  evidence uses, and stores pairs by index. It now runs before the release
  rows are built.

The output row counts are unchanged: 322,434 co-watch, 14,748,193 content
and 2,437,157 performer rows. The write of those rows, roughly 160 s, is
now a diff for both tables; see "`recs_similarity` write" and
"`performer_similar` write" below.

### `recs_similarity` write

`release_similar` used to be replaced every night: `DELETE` all rows, then
`COPY` the new ones, in one transaction. At full scale that is about 15M
deleted plus 15M inserted rows, roughly 160 s of the 230 s, and 15M dead
rows for autovacuum. Five runs grew the database from 4.4 to 19 GB.

**What changed.**

- **Diff write.** `store.SyncReleaseSimilar` streams the current rows in
  release order inside the write transaction. `recs.SimilarDiff` plans
  each release against its new rows. The planned inserts, updates and
  deletes are copied into a temp table and applied with one `DELETE` and
  one `INSERT … ON CONFLICT DO UPDATE`. Rows that need no change are not
  written, so they keep their `computed_at`; nothing reads that column.
- **Locking.** Locking is the same as before: one transaction with row
  locks only. Readers see the old set or the new set, never a mix, and
  are never blocked.
- **Equivalence.** The table after a diff write is equivalent to a full
  rewrite of the same rows:
  - It holds the same pairs.
  - Every score is within `recs.ReleaseSimilarEpsilon` (1e-3) of the new
    score.
  - Each release keeps its neighbour order. If one new score is strictly
    above another, its stored score is strictly above too, so
    `ORDER BY score DESC LIMIT n` returns the same rows in the same order,
    ties aside.
- **How a row is chosen.** A stored score is kept when it meets both rules
  above. Otherwise the row gets its new score. If the new score would sit
  above the previous neighbour's kept score, the row gets the next real
  below that kept score instead, which is still within 1e-3. At float
  rounding edges, where neither works, the release is written exactly
  (`exact_releases` in the report). With an epsilon of 0 the result is
  bit-identical to a full rewrite.
- **Autovacuum.** Migration 059 sets `release_similar`'s autovacuum and
  analyze scale factors to 2% (threshold 10k rows). The default 20% would
  wait about a week of nights before vacuuming.
- **Report.** The job report has a new `release_write` field with inserted,
  updated, deleted, `exact_releases` and `write_ms`.

**Why the epsilon.** Each night's new releases and plays move idf and the
co-watch norms, so nearly every score changes slightly.

- **Drift.** For one simulated night at 100k releases, the drift between
  stored and new scores was:
  - 1e-5 to 1e-4 for 2.9M of the 5.1M rows;
  - 1e-4 to 1e-3 for 1.9M rows, mostly `maker` and `series`, whose df
    moves with each new release;
  - above 1e-3 for 13k rows.
- **Rows written by epsilon.** Writing only rows that moved more than
  epsilon, ignoring order, would write:

  | Epsilon | Rows written |
  |---|---|
  | 0 | 5.13M |
  | 1e-5 | 4.81M |
  | 1e-4 | 1.95M |
  | 3e-4 | 345k |
  | 1e-3 | 33k |

  Order repairs add about 100k rows at 1e-3.
- **What 1e-3 means.** It is a tenth of a match percent on a cosine in
  [0, 1].
- **Rejected design.** A first SQL-only version rewrote a whole release
  exactly whenever tolerated drift would reorder it. That hit 40% of
  releases every night: 3.2–3.7M rows updated, a 23–26 s write and 1.39 GB
  after three nights. The per-row repair above replaced it.

**Measured.** The run used `scripts/perf/setup.sh` with 17k performers,
100k releases and 67k users, giving 5.1M `release_similar` rows.

- **Starting point.** The similarity job ran once with the full rewrite,
  followed by `VACUUM`. The database was then cloned, once for "before"
  (full rewrite, default autovacuum) and once for "after" (diff write,
  migration 059).
- **Each night.** One night added 100 new releases (0.1%, clones of random
  ones with their maker, series, tags and cast) and about 2k plays from 1%
  of users, using the same seed for both clones. The job then ran, and the
  measurement waited for autovacuum to finish with the table.
- **Columns.** Rows written is `n_tup_ins + n_tup_upd + n_tup_del` from
  `pg_stat_user_tables`. Size is `pg_total_relation_size` after
  autovacuum, starting from 942 MB.

| Night | Write before | Write after | Rows written before | Rows written after (ins / upd / del) | Size before | Size after |
|---|---|---|---|---|---|---|
| 1 | 29.3 s | 5.3 s | 10,213,094 | 102,705 (13,753 / 80,803 / 8,149) | 1,571 MB | 950 MB |
| 2 | 28.0 s | 5.1 s | 10,224,099 | 135,979 (13,118 / 115,144 / 7,717) | 1,575 MB | 959 MB |
| 3 | 28.9 s | 5.0 s | 10,235,147 | 133,209 (12,749 / 113,358 / 7,102) | 1,578 MB | 962 MB |

- **Job time.** The whole job went from 50–52 s to 27–29 s. The "after"
  write time includes reading the 5.1M current rows.
- **Table size.** The full rewrite doubles the table on its first night,
  because the new copy cannot reuse space that its own transaction still
  sees. After that it stays at about 1.6 GB only if autovacuum finishes
  between nights. The diff grew the table by 2% over three nights.
- **Database size.** The whole database, which also includes the churn and
  the unchanged `performer_similar` rewrite, ended at 3.69 GB before and
  3.30 GB after.

**Equivalence checks.**

- **`TestReleaseSimilarDiffIntegration`.** It runs synthetic nights
  through both paths on the same rows, at epsilon 0 and 1e-3. The nights
  include drift below and above epsilon, new and vanished pairs, a pair
  that swaps by less than epsilon, and an exact tie. It checks:
  - at epsilon 0, a bit-identical table;
  - at 1e-3, the same pairs, scores within epsilon, the same order and
    the same recall query results;
  - counts that match the snapshots, `computed_at` only on written rows,
    and nothing written on a rerun.
- **`TestReleaseSimilarJobDiffIntegration`.** It runs the real job twice
  over a catalog that changes in between, then compares the result with
  `RewriteReleaseSimilar` of the same job output.
- **`internal/recs/similar_diff_test.go`.** It runs 200 random releases
  through 8 nights each at four epsilons, and also covers the order repair
  and the exact fallback.
- **At 100k scale.** On the fourth night, the new rows were staged next to
  the diff write and compared in SQL:
  - the same 5,125,927 pairs, with none missing and none extra;
  - a largest drift of 0.00099999;
  - 0 order violations;
  - 0 rows whose per-release rank differs.
- **`performer_similar`.** It now takes the same diff write; see
  "`performer_similar` write" below.

**`watch_stats`.** `WatchStatsUsers` scans the whole window whatever its
`LIMIT`, and the runner called it once per 200-user batch: 590 scans for
118k active users. It now reads candidates in pages of 20k and still
writes 200 users per transaction. Users, rows and the report are the same.

### `performer_similar` write

`performer_similar` was still replaced every night: `DELETE` all rows,
then `COPY` them again, about 2.4M rows at full scale. It now takes the
same diff write as `release_similar`.

**What changed.**

- **Diff write.** `store.SyncPerformerSimilar` streams the current rows in
  performer order inside the write transaction. `recs.PerformerSimilarDiff`
  plans each performer by running `recs.SimilarDiff` on the scores, with
  the reason left empty, so similar_id is the tie-break. It then checks
  the evidence of each row the score rule would keep. Changes go through a
  temp table: one `DELETE` and one `INSERT … ON CONFLICT DO UPDATE`.
  Locking is the same as before.
- **Scores and order.** These follow the `release_similar` rules, with
  `recs.PerformerSimilarEpsilon` (1e-3):
  - the same pairs;
  - every score within 1e-3;
  - the same per-performer order. The readers use `ORDER BY score DESC,
    similar_id`, and `SimilarDiff` sorts on the same key, so even exact
    ties come back in the same order. Exact ties are common here, since
    many co-star-only pairs have the same score.
- **Evidence rule (`recs.SameEvidence`).** A row whose score is kept still
  gets rewritten unless all of these hold between its stored evidence and
  the new one:
  - `co_star`, the shared-work count, is equal;
  - `shared_tags` and `shared_makers` hold the same names in the same
    order. Absent and empty count as the same, since both are omitted
    from the JSON;
  - `metadata`, `co_star_score` and `co_watch` each differ by at most
    1e-3. They are rounded to 3 decimals, so this allows one rounding
    step;
  - `PerformerReason` gives the same text, so no reason threshold
    (`co_watch` 0.1 / 0.3, `metadata` 0.2) lies between them.
- **What the rule keeps.** Everything shown to users is unchanged: the
  reason text, the shared tag or maker it names, and the co-star count.
  The admin weights preview (`ScoreEvidence`) is a weighted mean of the
  three numbers, so its scores stay within 1e-3 too.
- **Epsilon 0.** Every evidence field must be equal, so the table is
  bit-identical to a full rewrite, scores and jsonb alike.
- **Undecodable evidence.** Stored evidence that does not decode strictly
  as `recs.Evidence` (bad JSON, unknown keys) is always rewritten.
- **Autovacuum.** Migration 060 sets `performer_similar`'s autovacuum and
  analyze scale factors to 2% (threshold 10k), as 059 does for
  `release_similar`.
- **Report.** The job report has a new `performer_write` field with the
  same counts as `release_write`.

**Measured.** The run used `scripts/perf/setup.sh` with 50k performers,
100k releases and 67k users, giving 2.33M `performer_similar` rows.

- **Starting point.** The similarity job ran once with the full rewrite,
  followed by `VACUUM ANALYZE`. The database was then cloned, once for
  "before" (full rewrite, default autovacuum) and once for "after" (diff
  write, migration 060).
- **Each night.** Each night ran as in "`recs_similarity` write": 100 new
  releases cloned from random ones (with maker, series, tags and cast),
  plus 3 plays each from 1% of users. Both clones used the same seed. The
  release side used the diff write in both. The measurement waited for
  autovacuum to finish with `performer_similar`.
- **Columns.** Rows written is `n_tup_ins + n_tup_upd + n_tup_del`. Size is
  `pg_total_relation_size`, starting from 661 MB.

| Night | Write before | Write after | Rows written before | Rows written after (ins / upd / del) | Size before | Size after |
|---|---|---|---|---|---|---|
| 1 | 16.4 s | 4.8 s | 4,663,890 | 60,690 (24,089 / 12,954 / 23,647) | 1,388 MB | 671 MB |
| 2 | 15.2 s | 4.9 s | 4,664,622 | 63,173 (24,275 / 14,913 / 23,985) | 1,389 MB | 682 MB |
| 3 | 14.4 s | 5.3 s | 4,665,491 | 65,543 (25,311 / 15,500 / 24,732) | 1,391 MB | 685 MB |

- **Job time.** The whole job went from 41–44 s to 32–33 s. The "after"
  write time includes reading and decoding the 2.33M current rows.
- **What gets written.** About half of the written rows are inserts and
  deletes: pairs entering or leaving a performer's top 50. That churn is
  real, and no tolerance removes it.
- **Without the tolerance.** On a fourth night from the same state,
  epsilon 0 updated 256,756 rows and epsilon 1e-3 updated 16,499. Inserts
  and deletes were the same (26,947 / 26,367).
- **Database size.** After three nights the database was 3,647 MB before
  and 2,941 MB after.

**Equivalence checks.**

- **`TestPerformerSimilarDiffIntegration`.** It runs synthetic nights
  through `SyncPerformerSimilar` and `RewritePerformerSimilar` on the same
  rows, at epsilon 0 and 1e-3. The nights include:
  - score drift below and above epsilon;
  - evidence drift of one rounding step;
  - a new leading shared tag and an extra co-star work;
  - new and vanished pairs;
  - a swap by less than epsilon, and exact ties.

  It checks:
  - at epsilon 0, bit-identical scores and identical jsonb;
  - at 1e-3, the same pairs, scores within epsilon and evidence that
    `SameEvidence` accepts;
  - at both, the same `SimilarPerformers` results: ids in the same order,
    with the same reason texts;
  - counts that match the snapshots, `computed_at` only on written rows,
    nothing written on a rerun, and an undecodable evidence row
    rewritten.
- **`TestPerformerSimilarJobDiffIntegration`.** It runs the real job twice
  over a catalog with performers that changes in between, then compares
  the table with `RewritePerformerSimilar` of the same job output.
- **`internal/recs/performer_diff_test.go`.** It runs 200 random
  performers through 8 nights each at three epsilons, with coarse scores
  for many exact ties. It checks the read order, ties included, and the
  evidence rule. A separate table test covers `SameEvidence`.
- **At 50k scale.** After the fourth night, the table was compared with
  the job output it was written from:
  - the same 2,333,615 pairs, with none missing and none extra;
  - a largest drift of 0.00099967;
  - 0 rows with evidence outside the rule;
  - 0 order violations in `ORDER BY score DESC, similar_id`.

### Memory

- **API RSS.** It starts at 88 MiB and peaks at about 155 MiB during a full
  `loadgen` round. In 4 back-to-back rounds on one process it settled
  between 50 and 100 MiB with no growth, so no leak was found.
- **Limiter.** `MemoryLimiter` flooded with 300k new IPv6 keys stayed at its
  100k cap, with 23 MiB of heap.
- **Web SSR server.** About 320 MiB RSS under load.

### SSR (web server render, 4 concurrent)

| Page | p50 / p95 before | p50 / p95 after |
|---|---|---|
| `/` (home) | 360 / 529 | 38 / 56 |
| `/releases/{r}` | 9.3 / 14 | 9.2 / 13 |
| `/performers/{p}` | 14 / 38 | 12 / 35 |
| `/discover`, `/search?q=` | 11 / 15, 4.8 / 5.8 | 11 / 14, 4.8 / 6.5 |

Home was slow because of `tags?limit=12`, an uncached ~100 ms count over
all tag rows on every render. Item 10 above caches it.

### Infrastructure

**`/dev/shm`.** Postgres puts parallel-query shared memory in `/dev/shm`,
which is 64 MB by default in Docker. At 8 concurrent catalog-wide queries
it ran out, and every parallel query failed with SQLSTATE 53100.

- **Fix.** The compose `db` service, which production deploys, and
  `setup.sh` now set `shm_size: 256mb`.
- **Deploying it.** The next `docker compose up -d db` recreates the db
  container, which means a short database restart.

### Remaining risks

- **The diff write still reads every `release_similar` row each night.**
  That is about 15M rows at full scale. The read is sequential and keeps
  only one release in memory, but its time grows with the table.
- **The performer list's displayed counts are live.** `sort=works` counts
  the works of its 21 page rows live, and those are the most prolific
  performers. That is about 1.5 ms of a query, 9 ms p50 at 8 concurrent
  requests and 50k performers.
- **Broad catalog searches are CPU-bound.** About 100 ms of database CPU
  for a word in about 10% of titles. Ranking needs `word_similarity` on
  every match.
- **`release_curves` first run takes about 12 minutes** over 207k
  releases, one release at a time. The steady state is incremental and
  under 1 s.
- **`recs_trending` takes about 35 s.** The heat query recomputes the
  trending inputs, so each period scans events and watch_history twice. It
  is within the 60 s budget, but it grows with history.
- **Hot performer profiles count works live.** About 15–80 ms for a
  performer with 1.5k works.

## Findings and fixes

### 1. `GET /api/performers?sort=works|newest_work`

**Before:** `buildPerformerQuery` (`internal/store/catalog_query.go`) joined
every active performer to a `CROSS JOIN LATERAL` that counted that
performer's published works. It then sorted on that count. Each request
counted the works of every performer, about 630 ms at 50k performers (from
an earlier perf pass).

**Now:**

- **Sort key.** The page is chosen from `performer_stats.works_count` /
  `latest_work_date`, which are rebuilt hourly by `RefreshPerformerStats`.
  They follow the same rule as the displayed counts: published, unmerged
  works.
  - `works` reads index `performer_stats_works` (migration 039).
  - `newest_work` reads index `performer_stats_newest` (migration 057).
  - In both cases the inner scan stops after `limit+1` rows.
- **Displayed counts.** `works_count` and `latest_release_date` stay live.
  The lateral count now runs only for the page's `limit+1` rows.
- **Performers missing from `performer_stats`.** These are performers
  created, reactivated or unmerged since the last hourly run.
  - They are paged in a second `UNION ALL` branch, `ps.performer_id IS NULL`,
    sorted on the live count. The order stays exact.
  - A single `COALESCE(ps.works_count, (live subquery))` key was rejected.
    The subquery never runs for rows that have stats, but the planner still
    costs it once per performer. At production volume that pushes the
    estimate over `jit_above_cost` (100k, and postgres:17 has JIT on by
    default). In a 4k-performer test, JIT alone added about 6 ms.
- **Finding the performers without stats.** The missing-stats branch used
  to hash anti-join every active performer against `performer_stats` on
  every request. That took about 1 ms at 4k performers and 12–15 ms warm
  at 50k. It now starts from `performer_stats_pending` (migration 061):
  - **What it holds.** A superset of the listed performers without a
    stats row, usually a handful.
  - **The trigger.** `performers_stats_pending` marks a performer when it
    is inserted active and unmerged, or updated to that from anything
    else (reactivated or unmerged). Those are the only ways to become
    listed. Updates of a performer that is already listed mark nothing.
  - **Clearing.** `RefreshPerformerStats` removes, in its own transaction
    and after its writes, the performers that now have a stats row.
    Stats rows are only removed by that same job (for performers not
    listed when it ran) or by cascade, so nobody listed without a row is
    ever unmarked. A performer listed while the job runs has no row and
    stays marked.
  - **Unlisted leftovers.** The job also drops marked performers that are
    no longer listed, such as a new performer merged away before its
    first stats row. It holds a share lock on them, so a concurrent
    reactivation is either seen (and kept) or waits and marks the
    performer again.
  - **Stale marks are harmless.** The branch still requires
    `ps.performer_id IS NULL` and the listing filters, so the order is
    exactly the same as before.
  - **Backfill.** The migration marks every listed performer without a
    stats row, which is all of them before the first stats run.

**Tests.** `TestPerformerWorkSortsIntegration` covers:

- the paged order across cursors equals ordering by live counts, with fresh
  stats and with performers missing from `performer_stats`: new,
  reactivated (`hidden` to `active`) and unmerged ones, and a pending
  performer hidden again;
- `performer_stats_pending` always lists every listed performer without a
  stats row, is empty after a stats run (including performers hidden or
  merged before their first run), and gains nothing when an already listed
  performer is updated;
- under `EXPLAIN ANALYZE`, with 5,000 more performers and one pending,
  the per-performer work count runs at most `limit+2` times (the page
  rows plus the pending performer's live sort key), and the missing-stats
  branch reads only the pending performer from `performers`.

**Measured.** Throwaway postgres:17 with 4,000 performers, 8,000 releases,
9,941 credits and 4,000 stats rows. Each case compares origin/main against
this change:

| Measure | origin/main | this change |
|---|---|---|
| `EXPLAIN ANALYZE`, `sort=works` | 22–31 ms, lateral `loops=4000` | 2.0 ms, lateral `loops=21`, fallback subplan never executed |
| `EXPLAIN ANALYZE`, `sort=newest_work` | — | 1.3 ms, index scan `performer_stats_newest` |
| Plan total cost | — | ~1.1k–1.4k (well under `jit_above_cost`) |
| HTTP p50 / p95, `sort=works` | 45.9 / 79.8 ms | 3.3 / 6.0 ms |
| HTTP p50 / p95, `sort=newest_work` | 42.4 / 51.0 ms | 3.0 / 5.5 ms |
| HTTP p50 / p95, `sort=name` (unchanged) | 1.6 / 3.5 ms | 1.6 / 3.6 ms |

The HTTP rows come from `scripts/perf/loadgen` with `-n 200 -c 4`, the same
database, and each API binary in turn. With an empty `performer_stats`
(before the first hourly run), every performer goes through the live
branch. The cost is then about the same as before. That is the expected
worst case, and it disappears after the next `recs_trending` run.

**Measured with `performer_stats_pending`.** The database came from
`scripts/perf/setup.sh` with 50k performers, 100k releases and 67k users,
after a `RefreshPerformerStats` run. Then 20 performers were added with
0–6 works each, as an hour of new performers would be. "Before" is the
API at the parent commit, "after" is this change, on the same database.
The loadgen runs used `-n 200 -c 8`, three times each, after a warm-up.

| Measure | Before | After |
|---|---|---|
| `EXPLAIN ANALYZE`, `sort=works` | 13.5–14.8 ms | 1.5–1.8 ms |
| `EXPLAIN ANALYZE`, `sort=newest_work` | 12.0–12.3 ms | 0.2 ms |
| HTTP p50 / p95, `sort=works` | 49–51 / 56–69 ms | 8.6–8.7 / 13–16 ms |
| HTTP p50 / p95, `sort=newest_work` | 44–45 / 50–77 ms | 3.4–4.3 / 4.9–11 ms |
| HTTP p50 / p95, `sort=name` (unchanged) | 1.9–2.5 / 2.8–5.3 ms | 1.9–2.0 / 2.7–2.9 ms |

- **What is left of `sort=works`.** The live displayed counts of its 21
  page rows. They are the most prolific performers, about 6k buffer hits
  per page.
- **The new branch.** It is a nested loop from the 20 pending rows to
  `performers_pkey`, then an anti-join probe of `performer_stats_pkey`.
- **Cost on writes.** The trigger costs one primary-key insert for each
  performer that becomes listed.

### 2. `community.MemoryLimiter`

**Before:**

- A sweep ran at most every 5 minutes and looked at no more than 10k keys.
  Under many new keys the map could grow without bound. Sources of new keys
  include anonymous device ids on 赞/踩, and per-address keys from client
  addresses (the #189 / #190 limits).
- The sweep cut keys at `max(window of the calling rule, 5 min)`. A
  10-minute per-address window (`anon-vote:`, `media-*:`) could be forgotten
  after 5 minutes when a 1-minute rule triggered the sweep.

**Now** (`internal/community/limiter.go`):

- **Per-window lists.** Keys sit in one most-recently-used list per window
  length.
  - Each `Allow` drops the expired keys from the backs of the lists. This is
    amortised O(1) and exact per window, so no periodic full sweep runs.
  - A key is never dropped while it still has hits inside its own window.
- **Hard cap.** At most `DefaultLimiterMaxKeys` (100k) keys. Past the cap, a
  new key evicts the least recently used key.
  - A key that is being refused counts as used, so a throttled client is
    evicted last.
  - Eviction can only loosen the limit for the evicted key. It never refuses
    anyone else. `Evicted()` counts these.
- **Small entries.**
  - Hits are stored as `int64` and capped at the rule's limit.
  - Keys over 128 bytes are stored as their SHA-256.
- **Memory bound.** At most about 1.2 KB per key at the largest limit in
  use (120 per 10 minutes), so under 120 MB at the cap. Realistic traffic
  uses a few MB.

**Scope.** Every caller of `srv.allow` / `srv.limiter.Allow` uses this one
limiter:

- posting limits (danmaku / reviews / comments / clips);
- 赞/踩 per voter;
- auth and registration;
- rec feedback;
- compliance intake;
- admin performer requests;
- the per-address media (#189) and anonymous-vote (#190) limits.

So the bound covers all of them. Client addresses come from
`clientip.Resolver`, which returns parsed IPs, so those keys are short
anyway.

**Follow-up, not changed here.** The per-address limits key on the full
IPv6 address. A client with a /64 can rotate addresses: it can get past the
per-address limit and churn the limiter's LRU. Keying IPv6 by /64 would
close this, but it changes behaviour and needs its own review.

**Tests:**

- `TestMemoryLimiterBoundedUnderNewKeys`: 50k new keys at cap 1000. `Len`
  never exceeds the cap, and a throttled key stays limited.
- `TestMemoryLimiterExpiresProportionally`: below the cap, live keys track
  the arrival rate.
- `TestMemoryLimiterKeepsLongWindows`
- `TestMemoryLimiterHashesLongKeys`
- a `-race` concurrency test.

### 3. Media caches (`internal/media`)

**Before:**

- The search `ResultCache` is keyed on the caller's `q`. The resolve cache
  (the L1 entries and the `?refresh=1` throttle map `refreshes`) is keyed on
  the caller's `{id...}`.
- Both capped the entry count but not the key length. A multi-KB `q` or id
  became a multi-KB map key.
- The search cache had no byte budget, and a full cache was reset
  wholesale.

**Now:**

- **Key length.** `cacheKey` replaces any key over 256 bytes with its prefix
  plus its SHA-256. Resolve keys go through it too, so the L1 entry, the
  refresh-throttle entry and the L2 `media_resolve_cache` row stay small.
- **Query length.** `GET /api/search` rejects `q` over 512 bytes
  (`400 invalid_query`) before contacting the provider.
- **Byte budget.** `ResultCache` stores encoded JSON with these limits:

  | Limit | Value |
  |---|---|
  | Total (keys + values) | 32 MB |
  | Entries | 10k |
  | Per value | 512 KB (larger results are served but not cached) |

  Past a bound it evicts expired entries first, then the oldest, instead of
  resetting.

**Tests** (`internal/media/cache_bounds_test.go`):

- byte budget under 200 MB of offered results;
- entry cap evicts the oldest;
- long keys are hashed;
- `q` over 512 bytes is rejected without calling the provider;
- a 100 KB resolve id gives bounded L1 and refresh keys and is still cached
  and throttled.

## Full load test

> **Disk warning.** A production-volume seed (50k performers, 300k releases,
> 200k users: about 25M rows with events, danmaku and comments) needs
> **several GB** of free Docker disk for data plus WAL. Run it only when
> `docker run --rm --entrypoint df alpine:3 -h /` shows at least ~15 GB
> available. The Docker VM is shared with other projects: a full disk breaks
> them too. `scripts/perf/setup.sh` refuses to seed more than its small
> defaults when less than `PERF_MIN_FREE_GB` (default 15) is free.
> `PERF_FORCE=1` overrides that check.

The scripts live in `scripts/perf/`:

| File | What it does |
|---|---|
| `setup.sh` | Starts a disposable `postgres:17` container (`avxxx-perf-db`, random localhost port). Applies the migrations by booting the API once. Seeds the database and prints its URL. Never touches `DATABASE_URL` or the compose `db`. |
| `seed.sql` | Synthetic, skewed catalog and activity data. Its defaults are small: 1k performers, 5k releases, 1k users, about 150k rows, under 200 MB. Activity tables scale with `users`. |
| `loadgen/` | Replays `endpoints.txt` and prints p50 / p95 / p99 per endpoint. With `-pid` it also samples the API's peak RSS. |
| `endpoints.txt` | The endpoints that `loadgen` replays. Run the API with `MEDIA_LINK_ON_DEMAND=false`. |
| `ssr-endpoints.txt` | SSR pages for `loadgen -base <web server>`. Pass the age-gate cookie with `-header "Cookie: ag=…"`. |

1. Check the free disk:

   ```sh
   docker run --rm --entrypoint df alpine:3 -h /
   ```

2. Seed the database. Use the default small run unless the full volume
   fits:

   ```sh
   scripts/perf/setup.sh   # small (default)
   PERF_PERFORMERS=50000 PERF_RELEASES=300000 PERF_USERS=200000 scripts/perf/setup.sh   # full
   ```

3. Fill `performer_stats` and the other hourly tables the way production
   does, by running the recommendation jobs (`recs_trending`) once against
   the printed URL. Otherwise every performer sort takes the missing-stats
   path:

   ```sh
   DATABASE_URL=<url> go run ./cmd/audit -recs
   ```

4. Start the API against the printed URL:

   ```sh
   DATABASE_URL=<url> API_ADDR=127.0.0.1:38181 METRICS_ADDR=off go run ./cmd/api &
   ```

5. Run the load. The `-releases` and `-performers` values must match the
   seeded volumes:

   ```sh
   go run ./scripts/perf/loadgen -base http://127.0.0.1:38181 -n 500 -c 8 \
     -releases 300000 -performers 50000 -pid <api pid>
   ```

6. For a single query, run `EXPLAIN (ANALYZE, BUFFERS)` on it against the
   same database. Check `loops=` on per-row nodes and whether a JIT section
   appears.

7. Tear down **with `-v`**. Otherwise the multi-GB data volume is left
   behind:

   ```sh
   docker rm -f -v avxxx-perf-db
   ```

**What to scale, and how:**

- `PERF_PERFORMERS`, `PERF_RELEASES` and `PERF_USERS` drive everything.
  - `series` scales with releases.
  - comments, danmaku, reviews, clips, and the play and search events scale
    with users.
- To try one query at a given scale without the whole seed, insert only the
  tables the query reads. Section 1 used performers, releases,
  `release_performers` and `performer_stats`, about 26k rows. Keep such
  checks to a few tens of thousands of rows on a nearly full disk.
- `make test-integration` uses its own `--rm` container and removes it with
  `-v`.

**Signed-in endpoints.** `auth:` endpoints need `-token`. On the seeded
database, insert a `user_sessions` row with `token_hash` set to the
SHA-256 hex of a random token, for a seeded user. Pass that token.

**Slow-query log.** On the throwaway database only, run
`ALTER SYSTEM SET log_min_duration_statement = 25; SELECT pg_reload_conf();`
and read `docker logs avxxx-perf-db` after a run.
