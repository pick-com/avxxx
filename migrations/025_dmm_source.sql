-- DMM/FANZA Affiliate Web API v3 sources (adapter "dmm", FINAL_DATA_DESIGN
-- 2.4 tier official_catalog). Seeded disabled: enable them only after
-- DMM_API_ID / DMM_AFFILIATE_ID are deployed and a dry run looks right.
-- Credentials are never stored here; the adapter adds them per request.
-- Re-run on every start: every statement is idempotent.
--
-- Crawl governance (024): license_status official_api permits automatic
-- crawls only after an admin records a terms review
-- (PUT /api/admin/sources/{id}/license sets terms_reviewed_at), so the seed
-- leaves terms_reviewed_at NULL on purpose; until then only manual verify
-- jobs (which write nothing) can run. terms_url points at the API guide,
-- whose usage terms the review must cover.
--
-- ActressSearch is seeded newest-first (sort=-id) so incremental runs, which
-- read the head and stop at the last run's head records, see new actresses.

-- Credit text the UI must show verbatim where DMM data is displayed.
ALTER TABLE sources ADD COLUMN IF NOT EXISTS attribution_html text;

INSERT INTO sources(name, url, enabled, adapter, source_kind, license_status, terms_url, egress_policy, priority)
VALUES
  ('DMM/FANZA ItemList (digital/videoa)',
   'https://api.dmm.com/affiliate/v3/ItemList?floor=videoa&hits=100&output=json&service=digital&site=FANZA&sort=date',
   false, 'dmm', 'official_catalog', 'official_api', 'https://affiliate.dmm.com/api/', 'direct', 10),
  ('DMM/FANZA ActressSearch',
   'https://api.dmm.com/affiliate/v3/ActressSearch?hits=100&output=json&sort=-id',
   false, 'dmm', 'official_catalog', 'official_api', 'https://affiliate.dmm.com/api/', 'direct', 10)
ON CONFLICT (url) DO NOTHING;

UPDATE sources SET attribution_html='Powered by <a href="https://affiliate.dmm.com/api/">FANZA Webサービス</a>'
WHERE adapter='dmm' AND attribution_html IS NULL;

-- Conservative defaults: the API publishes no numeric rate limit, so space
-- requests 2s apart and cap daily pages (100 items each). Existing rows,
-- e.g. tuned by an admin, are left alone.
INSERT INTO source_crawl_policies(source_id, discovery_interval, detail_refresh_interval, min_request_interval, daily_page_budget, daily_byte_budget)
SELECT id,
       CASE WHEN url LIKE '%/ActressSearch%' THEN interval '7 days' ELSE interval '6 hours' END,
       NULL, interval '2 seconds',
       CASE WHEN url LIKE '%/ActressSearch%' THEN 500 ELSE 2000 END,
       0
FROM sources WHERE adapter='dmm'
ON CONFLICT (source_id) DO NOTHING;
