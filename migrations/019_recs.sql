-- Recommendations V1: rules + item co-watch (PRD 4.1, 4.2, 6.1, 6.2, 10, 18;
-- tech plan 3.5). Everything the online /api/recs path reads is precomputed
-- here by the cmd/audit worker (internal/recs): the nightly 'recs_similarity'
-- job writes release_similar and performer_similar, the hourly
-- 'recs_trending' job writes the trending and performer heat snapshots.
-- Re-run on every start: every statement is idempotent.

-- Release -> release neighbours. reason says which signal produced the row:
--   cowatch : item co-watch from watch_history + play events (completion-weighted)
--   series  : content similarity dominated by the same series
--   maker   : content similarity dominated by the same maker
--   tags    : content similarity from shared tags (TF-IDF cosine)
-- A pair can have one cowatch row and one content row. Top 50 per release
-- and reason; the nightly job replaces the whole table in one transaction.
CREATE TABLE IF NOT EXISTS release_similar (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  similar_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  score real NOT NULL CHECK (score >= 0),
  reason text NOT NULL CHECK (reason IN ('cowatch','series','maker','tags')),
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, reason, similar_id),
  CHECK (release_id <> similar_id)
);
CREATE INDEX IF NOT EXISTS release_similar_lookup ON release_similar (release_id, score DESC);

-- Performer -> performer neighbours (PRD 6.1 「92% 相似」). score is 0-1, the
-- weighted mix of metadata (shared tags / makers), co-star and audience
-- co-watch under similarity_weights; evidence carries the parts and the
-- shared tags / makers the reason text is built from. Top 50 per performer.
CREATE TABLE IF NOT EXISTS performer_similar (
  performer_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  similar_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  score real NOT NULL CHECK (score >= 0 AND score <= 1),
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (performer_id, similar_id),
  CHECK (performer_id <> similar_id)
);
CREATE INDEX IF NOT EXISTS performer_similar_lookup ON performer_similar (performer_id, score DESC);

-- Trending boards (PRD 4.2): today | week | month | year, recomputed hourly.
-- Only the latest snapshot per period is kept; prev_rank is the release's
-- rank in the snapshot it replaced (NULL = newly on the board).
CREATE TABLE IF NOT EXISTS trending_snapshots (
  period text NOT NULL CHECK (period IN ('today','week','month','year')),
  rank integer NOT NULL CHECK (rank >= 1),
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  score real NOT NULL,
  plays integer NOT NULL DEFAULT 0,
  completion real NOT NULL DEFAULT 0,
  growth real NOT NULL DEFAULT 0,
  prev_rank integer,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (period, rank),
  UNIQUE (period, release_id)
);

-- Performer heat ranking (PRD 4.2 右侧演员热度榜, 4.1 热门演员), same periods.
CREATE TABLE IF NOT EXISTS performer_heat_snapshots (
  period text NOT NULL CHECK (period IN ('today','week','month','year')),
  rank integer NOT NULL CHECK (rank >= 1),
  performer_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  score real NOT NULL,
  plays integer NOT NULL DEFAULT 0,
  new_follows integer NOT NULL DEFAULT 0,
  prev_rank integer,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (period, rank),
  UNIQUE (period, performer_id)
);

-- Performer similarity weights (tech plan 3.13 AdminActors / A9): one row.
-- Saved from the admin API; the nightly job reads them, so a change takes
-- effect on the next run. visual is reserved for V1.1 (embeddings) and unused.
CREATE TABLE IF NOT EXISTS similarity_weights (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  metadata double precision NOT NULL DEFAULT 0.35 CHECK (metadata >= 0 AND metadata <= 1),
  co_star double precision NOT NULL DEFAULT 0.25 CHECK (co_star >= 0 AND co_star <= 1),
  co_watch double precision NOT NULL DEFAULT 0.40 CHECK (co_watch >= 0 AND co_watch <= 1),
  visual double precision NOT NULL DEFAULT 0 CHECK (visual >= 0 AND visual <= 1),
  updated_by text,
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO similarity_weights (id) VALUES (true) ON CONFLICT DO NOTHING;

-- Online recall: followed performers' new works and "new" fall back on
-- existing indexes (release_performers_performer, releases_public_newest).
-- Watched filter and co-watch seeds read watch_history_recent.
-- Trending input: play_request / progress events by release and time.
CREATE INDEX IF NOT EXISTS events_progress_release ON events (release_id, ts_server) WHERE event = 'progress';
CREATE INDEX IF NOT EXISTS watch_history_watched ON watch_history (watched_at);
CREATE INDEX IF NOT EXISTS follows_created ON follows (created_at);
