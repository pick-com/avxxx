-- Account and auth (PRD 7, 15, 20; tech plan 3.6, 3.13): devices and
-- sessions, remember-me, password reset, terms acceptance, account deletion
-- with a 30-day grace period, data export rate limits, Google / Apple sign-in,
-- onboarding interests and the notification timing settings.
-- Re-run on every start: every statement is idempotent.

-- Users: deletion is a mark (cancelled by logging in) until the purge job
-- hard-deletes the row 30 days later. OAuth-only accounts have password_hash ''
-- (never matches: verifyPassword rejects anything that is not argon2id/legacy).
ALTER TABLE users ADD COLUMN IF NOT EXISTS deletion_requested_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS password_changed_at timestamptz;
CREATE INDEX IF NOT EXISTS users_deletion_due ON users (deletion_requested_at)
  WHERE deletion_requested_at IS NOT NULL;

-- Sessions: a public id (the token hash is never exposed), device info and
-- last activity for Settings › 已登录设备, and whether it was a remember-me
-- login (30 d) or a browser session (24 h).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = current_schema() AND table_name = 'user_sessions' AND column_name = 'id'
  ) THEN
    ALTER TABLE user_sessions ADD COLUMN id bigserial;
  END IF;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS user_sessions_id ON user_sessions (id);
CREATE INDEX IF NOT EXISTS user_sessions_user ON user_sessions (user_id);
ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS user_agent text NOT NULL DEFAULT '';
ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS last_seen_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS remember boolean NOT NULL DEFAULT true;
ALTER TABLE user_sessions ADD COLUMN IF NOT EXISTS auth_method text NOT NULL DEFAULT 'password';

-- Password reset: single-use tokens, only sha256(token) is stored. The token
-- is minted by the notifier when it sends the email, so the plaintext never
-- touches the database (the outbox row only says "send a reset mail").
CREATE TABLE IF NOT EXISTS password_resets (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  used_at timestamptz
);
CREATE INDEX IF NOT EXISTS password_resets_user ON password_resets (user_id, created_at DESC);

-- Which terms version a user accepted, and when (web/app/lib/legal.ts
-- TERMS_VERSION). The web re-prompts when the latest accepted version differs.
CREATE TABLE IF NOT EXISTS terms_acceptances (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  version text NOT NULL,
  source text NOT NULL DEFAULT 'prompt' CHECK (source IN ('register','oauth','prompt')),
  accepted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, version)
);

-- Security / rate-limit ledger of account actions (password reset requests,
-- data exports, password changes, deletion requests). No IPs are stored.
CREATE TABLE IF NOT EXISTS account_events (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS account_events_user_kind ON account_events (user_id, kind, created_at DESC);

-- Google / Apple sign-in (OIDC). One identity per provider subject; one
-- identity per provider per user.
CREATE TABLE IF NOT EXISTS user_identities (
  id bigserial PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider text NOT NULL CHECK (provider IN ('google','apple')),
  subject text NOT NULL,
  email text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  last_login_at timestamptz,
  UNIQUE (provider, subject),
  UNIQUE (user_id, provider)
);

-- In-flight authorization requests (state, PKCE verifier, nonce). Rows live
-- 10 minutes and are deleted when used. bind_hash ties the request to the
-- browser that started it (sha256 of an HttpOnly cookie) against login CSRF.
-- link_user_id is set when a signed-in user links a provider from Settings.
CREATE TABLE IF NOT EXISTS oauth_states (
  state_hash text PRIMARY KEY,
  provider text NOT NULL,
  code_verifier text NOT NULL,
  nonce text NOT NULL,
  bind_hash text NOT NULL,
  next_path text NOT NULL DEFAULT '/',
  remember boolean NOT NULL DEFAULT true,
  link_user_id bigint REFERENCES users(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_states_expiry ON oauth_states (expires_at);

-- One-time codes handed to the web app after a successful callback (URL
-- fragment); exchanged once, within 2 minutes, for a session token. Only the
-- hash is stored.
CREATE TABLE IF NOT EXISTS oauth_login_codes (
  code_hash text PRIMARY KEY,
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  remember boolean NOT NULL DEFAULT true,
  provider text NOT NULL,
  created_user boolean NOT NULL DEFAULT false,
  expires_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS oauth_login_codes_expiry ON oauth_login_codes (expires_at);

-- Onboarding interests (welcome.tsx step 2), synced instead of localStorage only.
CREATE TABLE IF NOT EXISTS user_interests (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tag_slug text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, tag_slug)
);
ALTER TABLE users ADD COLUMN IF NOT EXISTS onboarded_at timestamptz;

-- Notification timing (PRD 17, Settings › 通知):
--   notify_daily_cap     push/email deliveries per channel per local day (1 = today's merge)
--   reservation_timing   go_live | day_before | both (reminder one day before scheduled_at)
--   personalize_recs     use watch history for recommendations (Settings › 隐私)
ALTER TABLE user_settings ADD COLUMN IF NOT EXISTS notify_daily_cap smallint NOT NULL DEFAULT 1
  CHECK (notify_daily_cap BETWEEN 1 AND 10);
ALTER TABLE user_settings ADD COLUMN IF NOT EXISTS reservation_timing text NOT NULL DEFAULT 'go_live'
  CHECK (reservation_timing IN ('go_live','day_before','both'));
ALTER TABLE user_settings ADD COLUMN IF NOT EXISTS personalize_recs boolean NOT NULL DEFAULT true;

-- Ledger of day-before reservation reminders: one per user per release.
CREATE TABLE IF NOT EXISTS reservation_reminders (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, release_id)
);
