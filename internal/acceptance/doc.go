// Package acceptance holds the end-to-end production acceptance suite
// (docs/ROADMAP_STATUS.md 全量验收; FINAL_DATA_DESIGN 10–11). Its tests run
// only with AVXXX_TEST_DATABASE_URL set (make test-integration, CI
// "integration" job); each test creates and drops its own database next to
// that one.
//
// Every scenario drives the production crawl path — store.Enqueue,
// store.Claim, crawlrun.Runner.Execute (Fetcher, robots.txt, license gate,
// budgets, checkpoints, block parking), store.Finish — against
// httptest-served fixture sites for each catalog adapter (javdatabase,
// xcity, javguru, dmm) and an RSS feed, then compares database snapshots:
//
//   - re-crawling (baseline, incremental, second baseline, re-ingest with
//     reordered / re-cased / re-spaced pages) never adds entities, identifiers,
//     aliases, credits or tags;
//   - blocking and failures (403, 429, challenge pages, robots.txt, license
//     gate, budget exhaustion, parse failures, skipped records, degraded and
//     empty pages) never delete or blank stored values or move checkpoints;
//   - admin overrides, holds, merges and splits survive re-crawls;
//   - no public API response carries raw_data, source payload canaries,
//     override reasons, DMM credentials, hidden birthdays or unpublished /
//     held releases.
package acceptance
