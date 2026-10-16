-- 社交互动 (PRD 5.4, 19; tech plan 3.13): 赞 / 踩 on releases and comments
-- with one level of replies, comment likes and comment reports in the shared
-- moderation inbox. Re-run on every start: every statement is idempotent.

-- 赞 / 踩 (tech plan 3.13 release_reactions). One vote per (release,
-- voter_key), like ratings: voter_key is 'u:<user id>' or 'd:<hash of the
-- X-Device-ID>' for anonymous visitors. value: 1 = 赞, -1 = 踩; undoing a
-- vote deletes the row. id identifies a row for admin splits.
CREATE TABLE IF NOT EXISTS release_reactions (
  id bigserial NOT NULL UNIQUE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  voter_key text NOT NULL CHECK (voter_key ~ '^(u:[0-9]+|d:[0-9a-f]{32})$'),
  user_id bigint REFERENCES users(id) ON DELETE CASCADE,
  device_id text,
  value smallint NOT NULL CHECK (value IN (-1, 1)),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (release_id, voter_key),
  CHECK ((user_id IS NULL) <> (device_id IS NULL))
);
CREATE INDEX IF NOT EXISTS release_reactions_user ON release_reactions (user_id, value) WHERE user_id IS NOT NULL;

-- 评论 (tech plan 3.13 comments; PRD marks them V1.1, gated by
-- COMMENTS_ENABLED). Threads are one level deep: parent_id is NULL for a
-- top-level comment and always points at a top-level comment otherwise.
-- status follows danmaku and reviews: visible | shadow (keyword hit; only the
-- author sees it until moderated) | folded (spoiler-folded by a moderator) |
-- deleted (by its author or a moderator; a deleted comment with live replies
-- is shown as a placeholder without its body or author).
CREATE TABLE IF NOT EXISTS comments (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  parent_id bigint REFERENCES comments(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  body text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 500),
  spoiler boolean NOT NULL DEFAULT false,
  likes integer NOT NULL DEFAULT 0 CHECK (likes >= 0),
  status text NOT NULL DEFAULT 'visible' CHECK (status IN ('visible','shadow','folded','deleted')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (parent_id IS NULL OR parent_id <> id)
);
CREATE INDEX IF NOT EXISTS comments_newest ON comments (release_id, created_at DESC, id DESC) WHERE parent_id IS NULL;
CREATE INDEX IF NOT EXISTS comments_replies ON comments (parent_id, created_at, id) WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS comments_user_recent ON comments (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS comments_shadow_queue ON comments (created_at, id) WHERE status = 'shadow';

CREATE TABLE IF NOT EXISTS comment_likes (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  comment_id bigint NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, comment_id)
);
CREATE INDEX IF NOT EXISTS comment_likes_comment ON comment_likes (comment_id);

-- Comments join the unified report inbox (017 reports). The target_type
-- check is replaced only while it does not list 'comment' yet; the new list
-- keeps every type the current check allows.
DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO def
  FROM pg_constraint c
  WHERE c.conrelid = 'reports'::regclass AND c.conname = 'reports_target_type_check';
  IF def IS NOT NULL AND position('''comment''' IN def) = 0 THEN
    EXECUTE 'ALTER TABLE reports DROP CONSTRAINT reports_target_type_check';
    EXECUTE 'ALTER TABLE reports ADD CONSTRAINT reports_target_type_check CHECK ('
      || regexp_replace(regexp_replace(def, '^CHECK \(', ''), '\)$', '')
      || ' OR target_type = ''comment'')';
  END IF;
END $$;
