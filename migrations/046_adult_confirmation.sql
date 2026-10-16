-- 进站 18+ 确认 (DESIGN_ALIGNMENT #8, PRD 15): the site is adults-only.
-- Idempotent: every statement can run again on an up-to-date schema.
--
-- Visitors confirm their age at the site-entry gate (POST /api/age-gate);
-- that answer lives only in a signed cookie, nothing is stored for them.
-- Accounts record the confirmation itself: adult_confirmed_at is when the
-- user confirmed being 18 or over (registration, the OAuth sign-up, or the
-- one-time prompt for accounts created before this migration). The birth
-- year-month is kept only when the user typed one at that moment (the gate
-- cookie was not already ok); NULL otherwise. No day of birth is ever stored.
ALTER TABLE users ADD COLUMN IF NOT EXISTS adult_confirmed_at timestamptz;
ALTER TABLE users ADD COLUMN IF NOT EXISTS adult_birth_ym text;
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'users_adult_birth_ym_check') THEN
    ALTER TABLE users ADD CONSTRAINT users_adult_birth_ym_check
      CHECK (adult_birth_ym IS NULL OR adult_birth_ym ~ '^[0-9]{4}-(0[1-9]|1[0-2])$');
  END IF;
END
$$;

-- An OAuth sign-up confirms 18+ on the login page before leaving for the
-- provider; the confirmation rides on the authorization request so the
-- callback can refuse to create an account without it.
ALTER TABLE oauth_states ADD COLUMN IF NOT EXISTS adult_confirmed boolean NOT NULL DEFAULT false;
ALTER TABLE oauth_states ADD COLUMN IF NOT EXISTS adult_birth_ym text;
