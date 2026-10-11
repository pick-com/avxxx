CREATE TABLE IF NOT EXISTS sources (id bigserial PRIMARY KEY,name text NOT NULL,url text NOT NULL UNIQUE,enabled boolean NOT NULL DEFAULT true);
CREATE TABLE IF NOT EXISTS items (id bigserial PRIMARY KEY,source_id bigint NOT NULL REFERENCES sources(id),title text NOT NULL,url text NOT NULL UNIQUE,summary text NOT NULL DEFAULT '',updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS jobs (id bigserial PRIMARY KEY,source_id bigint NOT NULL REFERENCES sources(id),status text NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','running','succeeded','failed')),error text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),started_at timestamptz,finished_at timestamptz);
CREATE UNIQUE INDEX IF NOT EXISTS jobs_active_source ON jobs(source_id) WHERE status IN ('pending','running');
CREATE INDEX IF NOT EXISTS items_updated ON items(updated_at DESC);
