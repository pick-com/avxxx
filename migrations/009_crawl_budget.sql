-- Per-source daily request usage, checked against source_crawl_policies
-- daily_page_budget / daily_byte_budget by the catalog fetcher.
CREATE TABLE IF NOT EXISTS source_crawl_usage (
  source_id bigint NOT NULL REFERENCES sources(id),
  usage_date date NOT NULL,
  pages bigint NOT NULL DEFAULT 0,
  bytes bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(source_id, usage_date)
);

-- Blocked-source isolation: a source that answers 403/429/challenge is parked
-- until blocked_until (from Retry-After) without touching other sources.
ALTER TABLE sources ADD COLUMN IF NOT EXISTS blocked_until timestamptz;
ALTER TABLE sources ADD COLUMN IF NOT EXISTS blocked_reason text;
