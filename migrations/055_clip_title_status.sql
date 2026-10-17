-- 片段 title moderation (待审片段): a clip whose title hits a moderation
-- keyword is still created and shared, but its title is shadowed — only the
-- author sees it — until a moderator approves it (visible) or removes it
-- (removed: the title is cleared; review_events keeps the old text). The
-- clip's own status (054) still covers the clip as a whole (fold / delete).
-- Re-run on every start: every statement is idempotent.
ALTER TABLE clips ADD COLUMN IF NOT EXISTS title_status text NOT NULL DEFAULT 'visible'
  CHECK (title_status IN ('visible','shadow','removed'));
CREATE INDEX IF NOT EXISTS clips_title_shadow_queue ON clips (created_at, id) WHERE title_status = 'shadow';
