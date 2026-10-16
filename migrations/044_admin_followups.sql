-- 后台补齐 (admin follow-ups after 042): scoped service tokens for
-- automation, and private copyright proof (版权凭证) on releases.
-- Idempotent: every statement can run again on an up-to-date schema.

-- Service tokens (internal/api/admin_auth.go): machine credentials for
-- scripts such as the daily SEO check, instead of the break-glass
-- ADMIN_TOKEN. Only the SHA-256 of the token is stored; the token itself is
-- shown once when a super admin creates it. scopes are exact admin route
-- patterns ("POST /api/admin/seo/report") or route groups with an access
-- level ("ingest:write"); the API never lets a service token reach the
-- admin_users or audit_log groups, the approvals queue or a dual-approval
-- route, whatever its scopes say.
CREATE TABLE IF NOT EXISTS service_tokens (
  id bigserial PRIMARY KEY,
  name text NOT NULL CHECK (name ~ '^[a-z0-9][a-z0-9._-]{1,47}$'),
  description text NOT NULL DEFAULT '',
  token_hash text NOT NULL UNIQUE,
  -- First characters of the token, to recognise it in a config file.
  token_prefix text NOT NULL DEFAULT '',
  scopes text[] NOT NULL CHECK (cardinality(scopes) > 0),
  created_by text NOT NULL,
  created_by_id bigint REFERENCES admin_users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_used_at timestamptz,
  last_used_ip_hash text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  revoked_by text
);
-- One live token per name; revoked tokens keep their name for the op log.
CREATE UNIQUE INDEX IF NOT EXISTS service_tokens_live_name ON service_tokens (name) WHERE revoked_at IS NULL;

-- The op log gains auth 'service' (actor "service:<name>").
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'admin_audit_log_auth_check'
                 AND pg_get_constraintdef(oid) LIKE '%service%') THEN
    ALTER TABLE admin_audit_log DROP CONSTRAINT IF EXISTS admin_audit_log_auth_check;
    ALTER TABLE admin_audit_log ADD CONSTRAINT admin_audit_log_auth_check
      CHECK (auth IN ('session', 'token', 'service', 'anonymous'));
  END IF;
END
$$;

-- 版权凭证 (artboard AdminVideo 发布, PRD 11 / 14.3): the licence or other
-- proof that a release may be listed. A row holds a document reference (a
-- contract number, a storage key in the private bucket) and/or a URL, with
-- notes. Back office only: no public route or query reads this table.
-- Removing a proof keeps the row (removed_at) for the record.
CREATE TABLE IF NOT EXISTS release_rights_proofs (
  id bigserial PRIMARY KEY,
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  kind text NOT NULL DEFAULT 'license' CHECK (kind IN ('license', 'contract', 'authorization', 'invoice', 'other')),
  reference text NOT NULL DEFAULT '' CHECK (char_length(reference) <= 500),
  url text NOT NULL DEFAULT '' CHECK (char_length(url) <= 2000),
  note text NOT NULL DEFAULT '' CHECK (char_length(note) <= 2000),
  valid_until date,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  removed_at timestamptz,
  removed_by text,
  CHECK (reference <> '' OR url <> '')
);
CREATE INDEX IF NOT EXISTS release_rights_proofs_release ON release_rights_proofs (release_id, created_at DESC);
