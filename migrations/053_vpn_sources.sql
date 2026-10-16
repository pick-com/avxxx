-- Per-source catalog egress (internal/catalog/proxy.go) and the XSList /
-- JAVLibrary sources (adapters "xslist", "javlibrary"). Re-run on every
-- start: every statement is idempotent.
--
-- ---- source_crawl_policies.proxy_url: the network path one source's crawl
-- takes (FINAL_DATA_DESIGN 8 "来源出口"). NULL inherits CATALOG_PROXY_URL,
-- then HTTPS_PROXY / HTTP_PROXY. Values: 'direct' (no proxy at all),
-- 'env:CATALOG_PROXY_<X>' (the proxy URL, credentials included, is read from
-- that deployment secret) or an http/https/socks5/socks5h proxy URL WITHOUT
-- credentials. Credentials never live in the database: the CHECK refuses any
-- scheme://user[:pass]@ value (the admin API validates the same way).
ALTER TABLE source_crawl_policies ADD COLUMN IF NOT EXISTS proxy_url text;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'source_crawl_policies_proxy_url_no_credentials') THEN
    ALTER TABLE source_crawl_policies ADD CONSTRAINT source_crawl_policies_proxy_url_no_credentials
      CHECK (proxy_url IS NULL OR (length(proxy_url) <= 512 AND proxy_url !~ '^[A-Za-z][A-Za-z0-9+.-]*://[^/]*@'));
  END IF;
END
$$;

-- ---- XSList (performer profiles: aliases, birth date, measurements) and
-- JAVLibrary (release pages; cast, genres and user ratings as evidence only).
-- FINAL_DATA_DESIGN 2.4 tier aggregator. Both answered every request from
-- our egress on 2026-10-16, robots.txt included, with a Cloudflare managed
-- challenge (HTTP 403, cf-mitigated: challenge); no terms page was found.
-- The crawler never tries to pass a challenge: a challenged source is parked
-- as blocked (reason "challenge").
--
-- Seeded disabled, license_status review_required, terms_reviewed_at /
-- terms_url NULL, egress_policy vpn: the crawl gate (024) allows only manual
-- verify jobs (parse-only, <= 3 pages, nothing written) until an admin
-- records a terms review, and crawlrun refuses an egress_policy=vpn source
-- that has no proxy configured (proxy_url, CATALOG_PROXY_URL or
-- HTTPS_PROXY), unless its proxy_url is 'direct' (the host itself routes
-- through the VPN). Enabling steps: docs/DEPLOYMENT.md "Catalog egress".
INSERT INTO sources(name, url, enabled, adapter, source_kind, license_status, terms_url, egress_policy, priority, license_note)
VALUES
  ('XSList', 'https://xslist.org/en/ranking', false, 'xslist', 'aggregator', 'review_required', NULL, 'vpn', 45,
   'Not reviewed. 2026-10-16: robots.txt and pages behind a Cloudflare challenge from our egress; no terms page found. Selectors from synthetic fixtures only.'),
  ('JAVLibrary', 'https://www.javlibrary.com/en/vl_newrelease.php', false, 'javlibrary', 'aggregator', 'review_required', NULL, 'vpn', 70,
   'Not reviewed. 2026-10-16: robots.txt and pages behind a Cloudflare challenge from our egress; no terms page found. Community evidence only (cast, genres, ratings never override). Selectors from synthetic fixtures only.')
ON CONFLICT (url) DO NOTHING;

-- Conservative crawl policy: one request every 10 s (robots.txt Crawl-delay
-- can only raise it), 100 requests a day for the incremental pass, a
-- baseline of at most 50 requests a day in 2-page slices, daily discovery.
-- Existing rows (an admin's tuning) are left alone.
INSERT INTO source_crawl_policies(source_id, discovery_interval, detail_refresh_interval, min_request_interval, daily_page_budget, daily_byte_budget,
  baseline_daily_page_budget, baseline_daily_byte_budget, baseline_pages_per_run)
SELECT id, interval '1 day', interval '30 days', interval '10 seconds', 100, 0, 50, 0, 2
FROM sources WHERE adapter IN ('xslist', 'javlibrary')
ON CONFLICT (source_id) DO NOTHING;

-- Field policy (lower wins; tiers: official_catalog 10, official_maker 20,
-- structured_database 30, aggregator 40, third_party_api 50, other 60).
-- XSList profile fields sit at the aggregator tier (40; sources.priority 45
-- breaks the tie ahead of Jav.Guru, as SOURCE_SCHEMA_PLAN 5 orders them).
-- JAVLibrary release fields sit at 70: below every other tier, so they only
-- fill fields nobody else has. Its user rating (javlibrary_user_rating) has
-- no canonical column and is never compared with other sources.
INSERT INTO source_field_policies(entity_type, field_name, source_id, priority, updated_by)
SELECT 'performer', f, s.id, 40, 'migration:vpn_sources'
FROM sources s, unnest(ARRAY['name','name_ja','birth_date','birth_place','debut_date','height_cm','cup_size']) AS f
WHERE s.adapter = 'xslist'
ON CONFLICT (entity_type, field_name, source_id) DO NOTHING;
INSERT INTO source_field_policies(entity_type, field_name, source_id, priority, updated_by)
SELECT 'release', f, s.id, 70, 'migration:vpn_sources'
FROM sources s, unnest(ARRAY['title','release_date','runtime_minutes','maker','label','director']) AS f
WHERE s.adapter = 'javlibrary'
ON CONFLICT (entity_type, field_name, source_id) DO NOTHING;
