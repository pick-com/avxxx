# Performance

This file covers the performance pass of 2026-10-17: what it found, what
changed, and how to rerun it. **The full-scale measurements (50k
performers, 300k releases, 200k users) are still to do.** The Docker VM had
about 4 GB of free disk, which is not enough for a production-volume
database. The numbers below come from small throwaway databases of 15k to
26k rows. Read them as before/after ratios and plan shapes. They are not
production latencies. Rerun "Full load test" when Docker has room.

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
- **Remaining O(N) part.** The missing-stats branch does a hash anti-join of
  active performers against `performer_stats`. It took about 1 ms at 4k
  performers and grows linearly. Expect roughly 10–15 ms at 50k. Confirm
  this in the full run.

**Tests.** `TestPerformerWorkSortsIntegration` covers:

- the paged order across cursors equals ordering by live counts, with fresh
  stats and with performers missing from `performer_stats`;
- under `EXPLAIN ANALYZE`, the per-performer work count runs at most
  `limit+1` times.

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
| `endpoints.txt` | The endpoints that `loadgen` replays. |

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

**Pending (needs disk):**

- the full-volume run of all endpoints;
- the missing-stats anti-join cost at 50k performers;
- peak RSS under a flood of new limiter keys at the 100k cap.
