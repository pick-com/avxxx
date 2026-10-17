-- 片段 (clips, YouTube parity): a signed-in viewer shares a 5–60 s segment
-- of a release under a short public id (/clips/{public_id}). The clip only
-- points at the release's timeline; nothing is cut or stored. status follows
-- danmaku / reviews / comments: visible | shadow (title keyword hit; only the
-- author sees it until moderated) | folded | deleted (by a moderator; the
-- author deletes the row). Re-run on every start: every statement is
-- idempotent.
CREATE TABLE IF NOT EXISTS clips (
  id bigserial PRIMARY KEY,
  public_id text NOT NULL UNIQUE CHECK (public_id ~ '^[A-Za-z0-9]{10}$'),
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  start_ms integer NOT NULL CHECK (start_ms >= 0),
  end_ms integer NOT NULL,
  title text NOT NULL DEFAULT '' CHECK (char_length(title) <= 100),
  status text NOT NULL DEFAULT 'visible' CHECK (status IN ('visible','shadow','folded','deleted')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (end_ms - start_ms BETWEEN 5000 AND 60000)
);
CREATE INDEX IF NOT EXISTS clips_release ON clips (release_id);
CREATE INDEX IF NOT EXISTS clips_user_recent ON clips (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS clips_shadow_queue ON clips (created_at, id) WHERE status = 'shadow';

-- Clips join the unified report inbox (017 reports), the same way comments
-- did in 040: the check is replaced only while it does not list 'clip' yet,
-- keeping every type the current check allows.
DO $$
DECLARE
  def text;
BEGIN
  SELECT pg_get_constraintdef(c.oid) INTO def
  FROM pg_constraint c
  WHERE c.conrelid = 'reports'::regclass AND c.conname = 'reports_target_type_check';
  IF def IS NOT NULL AND position('''clip''' IN def) = 0 THEN
    EXECUTE 'ALTER TABLE reports DROP CONSTRAINT reports_target_type_check';
    EXECUTE 'ALTER TABLE reports ADD CONSTRAINT reports_target_type_check CHECK ('
      || regexp_replace(regexp_replace(def, '^CHECK \(', ''), '\)$', '')
      || ' OR target_type = ''clip'')';
  END IF;
END $$;
