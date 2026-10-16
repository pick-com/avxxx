-- 推荐反馈 (YouTube-style 「不感兴趣」 / 「不推荐此演员」 on recommendation
-- cards). One row per (owner, kind, target); internal/recs filters and
-- demotes with the active rows (undone_at IS NULL) on every personalized
-- slot (home, related, tonight, performers). Never part of a public
-- response: only the owner reads them (GET /api/me/rec-feedback, the account
-- export). Re-run on every start: every statement is idempotent.
--
-- owner_key: 'u:<user id>' for signed-in users, 'd:<hash>' for visitors
--            (community.DeviceHash of the X-Device-ID, like ratings and
--            reactions; the raw device id is never stored).
-- kind:      not_interested_release (target_id = releases.id)
--            not_interested_performer (target_id = performers.id; releases
--              whose lead performer it is are dropped)
--            not_interested_tag (target_tag = release_tags.normalized_tag;
--              releases with it are demoted)
-- undone_at: set by 撤销; giving the same feedback again clears it.
CREATE TABLE IF NOT EXISTS rec_feedback (
  id bigserial PRIMARY KEY,
  owner_key text NOT NULL CHECK (owner_key ~ '^(u:[0-9]+|d:[0-9a-f]{32})$'),
  user_id bigint REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('not_interested_release','not_interested_performer','not_interested_tag')),
  target_id bigint,
  target_tag text CHECK (target_tag IS NULL OR char_length(target_tag) BETWEEN 1 AND 100),
  created_at timestamptz NOT NULL DEFAULT now(),
  undone_at timestamptz,
  CHECK ((owner_key LIKE 'u:%') = (user_id IS NOT NULL)),
  CHECK (CASE WHEN kind = 'not_interested_tag' THEN target_tag IS NOT NULL AND target_id IS NULL
              ELSE target_id IS NOT NULL AND target_id > 0 AND target_tag IS NULL END)
);
CREATE UNIQUE INDEX IF NOT EXISTS rec_feedback_target ON rec_feedback (owner_key, kind, (COALESCE(target_id, 0)), (COALESCE(target_tag, '')));
CREATE INDEX IF NOT EXISTS rec_feedback_active ON rec_feedback (owner_key, created_at DESC) WHERE undone_at IS NULL;
CREATE INDEX IF NOT EXISTS rec_feedback_user ON rec_feedback (user_id) WHERE user_id IS NOT NULL;
