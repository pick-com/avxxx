-- Community (PRD 5.5, 5.5.1, 11, 19; tech plan 3.8): danmaku and the peak
-- curve, ratings, short reviews, reports and the moderation queue.
-- Re-run on every start: every statement is idempotent.

-- Danmaku. version_key is the timeline the comment belongs to (PRD 5.5.1):
-- 'main' is shared by the main, high-quality and dub versions; an alternate
-- version (duration differs by more than 2%) has its own 'alt:<source id>'.
-- status: visible | shadow (only the author sees it; waits in the moderation
-- queue) | folded (spoiler-folded by a moderator) | deleted.
CREATE TABLE IF NOT EXISTS danmaku (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  version_key text NOT NULL DEFAULT 'main' CHECK (version_key ~ '^(main|alt:[0-9]+)$'),
  t_ms integer NOT NULL CHECK (t_ms >= 0),
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 50),
  color text NOT NULL DEFAULT '#FFFFFF' CHECK (color ~ '^#[0-9A-F]{6}$'),
  mode text NOT NULL DEFAULT 'scroll' CHECK (mode IN ('top','bottom','scroll')),
  lang text NOT NULL DEFAULT '',
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  status text NOT NULL DEFAULT 'visible' CHECK (status IN ('visible','shadow','folded','deleted')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS danmaku_timeline ON danmaku (release_id, version_key, t_ms);
CREATE INDEX IF NOT EXISTS danmaku_user_recent ON danmaku (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS danmaku_shadow_queue ON danmaku (created_at, id) WHERE status = 'shadow';

-- Peak curve (PRD 5.5.1): 200 integers 0-255 per release and version, or
-- data NULL when fewer than 50 valid danmaku. bucket_ms is the bucket width
-- the curve was computed with (duration / 200, at least 1 s).
CREATE TABLE IF NOT EXISTS danmaku_curves (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  version_key text NOT NULL,
  data smallint[] CHECK (data IS NULL OR array_length(data, 1) = 200),
  bucket_ms integer NOT NULL DEFAULT 1000 CHECK (bucket_ms >= 1000),
  valid_count integer NOT NULL DEFAULT 0,
  computed_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, version_key)
);

-- Releases whose curve must be recomputed: any new danmaku, any moderation
-- change and any mute marks the release dirty; the curve job clears the row
-- once it has recomputed with everything up to dirty_since.
CREATE TABLE IF NOT EXISTS danmaku_curve_queue (
  release_id bigint PRIMARY KEY REFERENCES releases(id) ON DELETE CASCADE,
  dirty_since timestamptz NOT NULL DEFAULT now()
);

-- Ratings (PRD 19). One vote per (release, voter_key); voter_key is
-- 'u:<user id>' or 'd:<hash of the device id>' for anonymous votes.
-- weight is fixed when the vote is cast (anonymous 0.3, account < 7 days
-- 0.5, abnormal account 0.1, otherwise 1); muting a user lowers it later.
-- status 'removed' = struck by a moderator (rating flood); kept for audit.
CREATE TABLE IF NOT EXISTS ratings (
  id bigserial NOT NULL UNIQUE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  voter_key text NOT NULL CHECK (voter_key ~ '^(u:[0-9]+|d:[0-9a-f]{32})$'),
  user_id bigint REFERENCES users(id) ON DELETE CASCADE,
  device_id text,
  score smallint NOT NULL CHECK (score BETWEEN 1 AND 5),
  weight real NOT NULL DEFAULT 1 CHECK (weight > 0 AND weight <= 1),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','removed')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, voter_key),
  CHECK ((user_id IS NULL) <> (device_id IS NULL))
);
CREATE INDEX IF NOT EXISTS ratings_user ON ratings (user_id) WHERE user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS ratings_low_trust_recent ON ratings (created_at) WHERE status = 'active' AND weight < 1;

-- Short reviews (PRD 19): <= 140 characters, one live review per user and
-- release. watch_ratio is the author's watch progress (0-1) when written.
CREATE TABLE IF NOT EXISTS reviews (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 140),
  spoiler boolean NOT NULL DEFAULT false,
  watch_ratio real NOT NULL DEFAULT 0 CHECK (watch_ratio >= 0 AND watch_ratio <= 1),
  likes integer NOT NULL DEFAULT 0 CHECK (likes >= 0),
  status text NOT NULL DEFAULT 'visible' CHECK (status IN ('visible','shadow','folded','deleted')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS reviews_one_live ON reviews (release_id, user_id) WHERE status <> 'deleted';
CREATE INDEX IF NOT EXISTS reviews_newest ON reviews (release_id, created_at DESC, id DESC) WHERE status IN ('visible','folded');
CREATE INDEX IF NOT EXISTS reviews_shadow_queue ON reviews (created_at, id) WHERE status = 'shadow';

CREATE TABLE IF NOT EXISTS review_likes (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  review_id bigint NOT NULL REFERENCES reviews(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, review_id)
);
CREATE INDEX IF NOT EXISTS review_likes_review ON review_likes (review_id);

-- Unified report inbox (tech plan 3.8, AdminOps). due_at is the SLA deadline
-- (internal/community.ReportDue). One open report per reporter and target.
CREATE TABLE IF NOT EXISTS reports (
  id bigserial PRIMARY KEY,
  target_type text NOT NULL CHECK (target_type IN ('danmaku','review','rating','release','performer_info','source')),
  target_id bigint NOT NULL,
  reporter_id bigint REFERENCES users(id) ON DELETE SET NULL,
  reason text NOT NULL,
  note text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
  due_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  resolution text,
  resolved_by text
);
CREATE UNIQUE INDEX IF NOT EXISTS reports_open_once ON reports (target_type, target_id, reporter_id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS reports_queue ON reports (due_at, id) WHERE status = 'open';
CREATE INDEX IF NOT EXISTS reports_target ON reports (target_type, target_id) WHERE status = 'open';

-- Keyword list for the synchronous check: a hit stores the danmaku or review
-- as 'shadow'. word is stored normalized (internal/community.NormalizeKeyword).
CREATE TABLE IF NOT EXISTS moderation_words (
  word text PRIMARY KEY CHECK (char_length(word) BETWEEN 1 AND 64),
  category text NOT NULL DEFAULT 'other' CHECK (category IN ('ad','abuse','spoiler','illegal','political','other')),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Posting bans ("删除并禁言"). A row with until > now() blocks danmaku and
-- reviews; the muted user's danmaku history leaves the peak curve.
CREATE TABLE IF NOT EXISTS user_mutes (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  until timestamptz NOT NULL,
  reason text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT 'admin',
  created_at timestamptz NOT NULL DEFAULT now()
);
