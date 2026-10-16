-- Official maker catalog sources (adapter "maker:<slug>", internal/catalog/
-- maker.go; FINAL_DATA_DESIGN 2.4 tier official_maker, priority 20: wins over
-- structured databases and aggregators for the maker's own titles). One row
-- per studio so each has its own robots.txt, request spacing, daily budget,
-- checkpoints and license review. Re-run on every start: every statement is
-- idempotent. (033 is code_match_key.)
--
-- Seeded disabled with license_status review_required ("terms not reviewed
-- yet") and terms_reviewed_at / terms_url NULL on purpose: the crawl gate
-- (024) refuses baseline and incremental runs until an admin records a terms
-- review with the studio's terms URL (PUT /api/admin/sources/{id}/license).
-- Until then only manual verify jobs (parse-only, at most 3 pages, nothing
-- written) can run. The profiles' selectors were written against synthetic
-- fixtures; docs/MAKER_CATALOG.md is the enabling checklist.
--
-- url is the profile's listing_url (newest first); keep them in sync with
-- internal/catalog/maker_profiles.json.

INSERT INTO sources(name, url, enabled, adapter, source_kind, license_status, terms_url, egress_policy, priority, license_note)
VALUES
  ('S1 NO.1 STYLE (official)', 'https://s1s1s1.com/works/date', false, 'maker:s1', 'official_maker', 'review_required', NULL, 'direct', 20,
   'Not reviewed. Selectors untested against the live site (synthetic fixtures only); see docs/MAKER_CATALOG.md.'),
  ('IDEA POCKET (official)', 'https://ideapocket.com/works/date', false, 'maker:ideapocket', 'official_maker', 'review_required', NULL, 'direct', 20,
   'Not reviewed. Selectors untested against the live site (synthetic fixtures only); see docs/MAKER_CATALOG.md.')
ON CONFLICT (url) DO NOTHING;

-- Conservative per-studio defaults: one request every 5 s, 300 requests a
-- day (listing pages + detail pages), daily incremental discovery. Existing
-- rows, e.g. tuned by an admin, are left alone.
INSERT INTO source_crawl_policies(source_id, discovery_interval, detail_refresh_interval, min_request_interval, daily_page_budget, daily_byte_budget)
SELECT id, interval '1 day', NULL, interval '5 seconds', 300, 0
FROM sources WHERE adapter LIKE 'maker:%'
ON CONFLICT (source_id) DO NOTHING;
