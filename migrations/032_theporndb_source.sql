-- ThePornDB API source (adapter "theporndb", FINAL_DATA_DESIGN 2.4 tier
-- third_party_api: batch lookup and missing-field fallback, never the sole
-- source of truth) and the third-party cross-check job's attempt log
-- (internal/crosscheck). Re-run on every start: every statement is
-- idempotent. (031 is reserved for other work.)
--
-- Seeded disabled with license_status official_api (a documented API used
-- with a personal token, THEPORNDB_API_TOKEN, never stored here) and
-- terms_reviewed_at NULL on purpose: the crawl gate (024) and the
-- cross-check job refuse automatic runs until an admin records a terms
-- review (PUT /api/admin/sources/{id}/license). terms_url points at the API
-- documentation; the API spec links no terms of service or attribution
-- requirement, so the review must locate and confirm them.
--
-- One row serves both modes: the cross-check job (primary) looks releases up
-- by DVD code under this source; when enabled, the scheduler also runs the
-- incremental discovery crawl of GET /jav (newest release date first).
-- Either way store.SaveCatalogRecord lets third_party_api records attach to
-- existing releases / performers only.

INSERT INTO sources(name, url, enabled, adapter, source_kind, license_status, terms_url, egress_policy, priority)
VALUES
  ('ThePornDB JAV',
   'https://api.theporndb.net/jav?orderBy=recently_released&per_page=100',
   false, 'theporndb', 'third_party_api', 'official_api', 'https://api.theporndb.net/docs', 'direct', 50)
ON CONFLICT (url) DO NOTHING;

UPDATE sources SET attribution_html='Metadata cross-checked with <a href="https://theporndb.net/">ThePornDB</a>'
WHERE adapter='theporndb' AND attribution_html IS NULL;

-- Conservative defaults: the API documents no rate limit, so space requests
-- 2s apart and cap the shared daily budget (discovery pages + lookups) at
-- 1000 requests; discovery runs daily. Existing rows are left alone.
INSERT INTO source_crawl_policies(source_id, discovery_interval, detail_refresh_interval, min_request_interval, daily_page_budget, daily_byte_budget)
SELECT id, interval '1 day', NULL, interval '2 seconds', 1000, 0
FROM sources WHERE adapter='theporndb'
ON CONFLICT (source_id) DO NOTHING;

-- One row per (release, source): the latest cross-check attempt.
-- status:      found | not_found | ambiguous | error
-- next_try_at: when the release is due again; NULL = never automatically
--              (found: evidence saved; ambiguous: an admin decides).
--              Backoff in internal/crosscheck (not_found 14 d, 60 d after 3
--              misses; error 30 min doubling to 24 h).
-- candidates:  JSON array of source record keys only; never response bodies.
-- reasons:     JSON array of why the release was selected (missing_* fields,
--              open_conflict).
-- last_error:  short error text (no credentials, no bodies).
CREATE TABLE IF NOT EXISTS crosscheck_attempts (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  source_id bigint NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  status text NOT NULL CHECK (status IN ('found','not_found','ambiguous','error')),
  tried_at timestamptz NOT NULL DEFAULT now(),
  next_try_at timestamptz,
  attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 0),
  matched_key text,
  candidates jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(candidates) = 'array'),
  reasons jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(reasons) = 'array'),
  last_error text,
  PRIMARY KEY (release_id, source_id)
);
CREATE INDEX IF NOT EXISTS crosscheck_attempts_due ON crosscheck_attempts(source_id, next_try_at);
CREATE INDEX IF NOT EXISTS crosscheck_attempts_recent ON crosscheck_attempts(source_id, tried_at DESC);
