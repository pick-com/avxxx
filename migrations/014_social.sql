-- User-side social features: settings, follows, reservations, watch-later and
-- in-app notifications (PRD 7, 17, 20; tech plan 3.2, 3.7, 3.13).
-- Re-run on every start: every statement is idempotent.

-- Upcoming releases. releases.status has no CHECK constraint (002), so
-- 'upcoming' needs no schema change; it is documented here:
--   draft | upcoming | published (plus whatever admin tooling uses).
-- scheduled_at: when an upcoming release goes live. NULL means "date not
-- fixed": shown on the performer page and reservable, but never auto-published.
ALTER TABLE releases ADD COLUMN IF NOT EXISTS scheduled_at timestamptz;
-- notified_at: when the new-work fan-out ran for the release (NULL = pending).
-- Added once with a backfill so the first run does not notify followers about
-- the whole existing catalog; the column's existence is the idempotency marker.
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'releases' AND column_name = 'notified_at'
  ) THEN
    ALTER TABLE releases ADD COLUMN notified_at timestamptz;
    UPDATE releases SET notified_at = now() WHERE status = 'published';
  END IF;
END
$$;
CREATE INDEX IF NOT EXISTS releases_fanout_pending ON releases (id)
  WHERE status = 'published' AND notified_at IS NULL;
CREATE INDEX IF NOT EXISTS releases_upcoming_due ON releases (scheduled_at)
  WHERE status = 'upcoming';

-- One row per user; missing row = defaults (store.DefaultSettings mirrors these).
CREATE TABLE IF NOT EXISTS user_settings (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  ui_lang text NOT NULL DEFAULT 'zh-CN',
  content_lang text NOT NULL DEFAULT 'zh-CN',
  subtitle_langs text[] NOT NULL DEFAULT '{}',
  quality_pref text NOT NULL DEFAULT 'auto'
    CHECK (quality_pref IN ('auto','2160p','1080p','720p','480p','360p')),
  data_saver boolean NOT NULL DEFAULT false,
  autoplay boolean NOT NULL DEFAULT true,
  remember_rate boolean NOT NULL DEFAULT true,
  history_paused boolean NOT NULL DEFAULT false,
  theme text NOT NULL DEFAULT 'system' CHECK (theme IN ('system','dark','light')),
  timezone text NOT NULL DEFAULT 'UTC',
  -- {kind: {channel: bool}}; kinds new_work|reservation|comment_reply|favorite_update,
  -- channels in_app|push|email. Always stored fully expanded by the API.
  notify_prefs jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- {"start":"HH:MM","end":"HH:MM"} in the user's timezone, or NULL for none.
  quiet_hours jsonb DEFAULT '{"start":"22:00","end":"08:00"}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS follows (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  performer_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  notify_mode text NOT NULL DEFAULT 'all' CHECK (notify_mode IN ('all','lead_only','off')),
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, performer_id)
);
CREATE INDEX IF NOT EXISTS follows_performer ON follows (performer_id);

CREATE TABLE IF NOT EXISTS reservations (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, release_id)
);
CREATE INDEX IF NOT EXISTS reservations_release ON reservations (release_id);

CREATE TABLE IF NOT EXISTS watch_later (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  position integer NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, release_id)
);
CREATE INDEX IF NOT EXISTS watch_later_order ON watch_later (user_id, position);

CREATE TABLE IF NOT EXISTS notifications (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL,
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  -- "<user_id>:<YYYY-MM-DD>" for the merged daily new-work notice; NULL for unmerged kinds.
  merged_key text,
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notifications_user ON notifications (user_id, id DESC);
CREATE INDEX IF NOT EXISTS notifications_unread ON notifications (user_id, id DESC) WHERE read_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS notifications_merged ON notifications (user_id, merged_key)
  WHERE merged_key IS NOT NULL;

-- Ledger of "user was told about release": guarantees one notice per user per
-- release across follows and reservations, retries and re-runs.
CREATE TABLE IF NOT EXISTS notification_receipts (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  reason text NOT NULL CHECK (reason IN ('follow','reservation')),
  notification_id bigint REFERENCES notifications(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, release_id)
);

-- Push / email deliveries for a future sender worker. One pending row per
-- user, channel and day (merged_key); quiet hours only delay send_after.
CREATE TABLE IF NOT EXISTS notification_outbox (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  channel text NOT NULL CHECK (channel IN ('push','email')),
  payload jsonb NOT NULL DEFAULT '{}'::jsonb,
  merged_key text,
  send_after timestamptz NOT NULL DEFAULT now(),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','sending','sent','failed','cancelled')),
  attempt integer NOT NULL DEFAULT 0,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz
);
CREATE UNIQUE INDEX IF NOT EXISTS notification_outbox_merged ON notification_outbox (user_id, channel, merged_key)
  WHERE merged_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS notification_outbox_due ON notification_outbox (send_after, id) WHERE status = 'pending';
