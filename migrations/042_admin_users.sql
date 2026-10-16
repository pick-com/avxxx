-- AdminUsers (PRD 11, tech plan 3.6): back-office accounts, separate from
-- site users, with mandatory TOTP, the 7 roles, short sessions, the admin
-- op log, dual approval for destructive operations, and site-user bans.
-- Idempotent: every statement can run again on an up-to-date schema.

-- Admin accounts are a table of their own, not a flag on users: a site
-- account is reachable through OAuth, emailed reset links and self-service
-- deletion, none of which may ever grant back-office access.
CREATE TABLE IF NOT EXISTS admin_users (
  id bigserial PRIMARY KEY,
  -- username is the review_events actor and a login identifier.
  username text NOT NULL CHECK (username ~ '^[a-z0-9][a-z0-9._-]{1,31}$'),
  email text NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
  display_name text NOT NULL DEFAULT '',
  -- argon2id PHC string (same parameters as users.password_hash); '' until enrolled.
  password_hash text NOT NULL DEFAULT '',
  -- "v1:<AES-GCM>" with ADMIN_TOTP_KEY, else "plain:<base32>"; '' = none.
  totp_secret text NOT NULL DEFAULT '',
  totp_enrolled_at timestamptz,
  -- Highest accepted RFC 6238 step: a code is never accepted twice.
  totp_last_step bigint NOT NULL DEFAULT 0,
  -- invited: waiting for the enrollment link (password + TOTP);
  -- active: can sign in; disabled: cannot, sessions revoked.
  status text NOT NULL DEFAULT 'invited' CHECK (status IN ('invited', 'active', 'disabled')),
  failed_attempts integer NOT NULL DEFAULT 0,
  locked_until timestamptz,
  last_login_at timestamptz,
  invite_token_hash text,
  invite_expires_at timestamptz,
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS admin_users_username ON admin_users (username);
CREATE UNIQUE INDEX IF NOT EXISTS admin_users_email ON admin_users (lower(email));
CREATE UNIQUE INDEX IF NOT EXISTS admin_users_invite ON admin_users (invite_token_hash) WHERE invite_token_hash IS NOT NULL;

-- The 7 roles of artboard AdminUsers; what each may do is the matrix in
-- internal/adminauth/roles.go (route groups), enforced by the API.
CREATE TABLE IF NOT EXISTS admin_roles (
  role text PRIMARY KEY,
  label text NOT NULL,
  sort smallint NOT NULL DEFAULT 0
);
INSERT INTO admin_roles(role, label, sort) VALUES
  ('super_admin', '超级管理员', 1),
  ('content_editor', '内容编辑', 2),
  ('ingest_ops', '入库运维', 3),
  ('moderator', '审核员', 4),
  ('operations', '运营', 5),
  ('ad_sales', '广告销售', 6),
  ('translator', '外部译者', 7)
ON CONFLICT (role) DO UPDATE SET label = EXCLUDED.label, sort = EXCLUDED.sort;

CREATE TABLE IF NOT EXISTS admin_user_roles (
  admin_id bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  role text NOT NULL REFERENCES admin_roles(role),
  granted_by text NOT NULL DEFAULT '',
  granted_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (admin_id, role)
);

-- One-time recovery codes, SHA-256 of the normalized code (≈ 49 random bits each).
CREATE TABLE IF NOT EXISTS admin_recovery_codes (
  admin_id bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  code_hash text NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (admin_id, code_hash)
);

-- Admin sessions: not user_sessions. 30 min idle, 12 h absolute (enforced
-- in the lookup); only the SHA-256 of the token is stored.
CREATE TABLE IF NOT EXISTS admin_sessions (
  id bigserial PRIMARY KEY,
  admin_id bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  token_hash text NOT NULL UNIQUE,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  ip_hash text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS admin_sessions_admin ON admin_sessions (admin_id) WHERE revoked_at IS NULL;

-- The step between password and TOTP: a short-lived, single-use token.
CREATE TABLE IF NOT EXISTS admin_login_challenges (
  token_hash text PRIMARY KEY,
  admin_id bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  attempts smallint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- Admin op log: one row per admin write, per break-glass request, per
-- read of personal data (site users) and per sign-in event. review_events
-- stays the per-entity change history (before / after values); this is the
-- per-request journal with who, route, entity, result and the hashed IP.
CREATE TABLE IF NOT EXISTS admin_audit_log (
  id bigserial PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now(),
  admin_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  actor text NOT NULL,
  -- session: signed-in admin; token: ADMIN_TOKEN break-glass; anonymous: failed sign-in.
  auth text NOT NULL CHECK (auth IN ('session', 'token', 'anonymous')),
  action text NOT NULL,
  method text NOT NULL DEFAULT '',
  route text NOT NULL DEFAULT '',
  path text NOT NULL DEFAULT '',
  entity_type text NOT NULL DEFAULT '',
  entity_id text NOT NULL DEFAULT '',
  status integer NOT NULL DEFAULT 0,
  ip_hash text NOT NULL DEFAULT '',
  approval_id bigint,
  detail jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS admin_audit_log_created ON admin_audit_log (created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS admin_audit_log_actor ON admin_audit_log (actor, created_at DESC);
CREATE INDEX IF NOT EXISTS admin_audit_log_entity ON admin_audit_log (entity_type, entity_id, created_at DESC);

-- Dual approval: a destructive request is stored instead of executed and
-- runs only when a different admin approves it (internal/api/admin_approvals.go).
CREATE TABLE IF NOT EXISTS admin_approvals (
  id bigserial PRIMARY KEY,
  route text NOT NULL,
  method text NOT NULL,
  path text NOT NULL,
  query text NOT NULL DEFAULT '',
  body bytea NOT NULL DEFAULT ''::bytea,
  summary text NOT NULL DEFAULT '',
  requested_by bigint NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  requested_by_name text NOT NULL,
  requested_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'executing', 'executed', 'failed', 'rejected', 'cancelled', 'expired')),
  decided_by text,
  decided_by_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  decided_at timestamptz,
  decision_note text NOT NULL DEFAULT '',
  result_status integer
);
CREATE INDEX IF NOT EXISTS admin_approvals_status ON admin_approvals (status, requested_at DESC);

-- Site-user bans (处罚 next to the existing user_mutes): until NULL is
-- permanent. A ban revokes the user's sessions and blocks new ones.
CREATE TABLE IF NOT EXISTS user_bans (
  user_id bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  until timestamptz,
  reason text NOT NULL DEFAULT '',
  created_by text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
