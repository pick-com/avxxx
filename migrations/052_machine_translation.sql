-- Machine translation (ROADMAP 机器翻译, tech plan 3.10): OpenRouter drafts
-- (internal/i18n/openrouter.go) land as translations.status='machine' and
-- are never served until a human submits and reviews them. Re-run on every
-- start: every statement is idempotent.
--
-- translations.mt_model / mt_at: provenance of a machine draft (the model id
-- that produced it and when), kept after a human edits the draft so
-- reviewers can see it started as machine output.
ALTER TABLE translations ADD COLUMN IF NOT EXISTS mt_model text;
ALTER TABLE translations ADD COLUMN IF NOT EXISTS mt_at timestamptz;

-- Fields an admin asked to machine-translate (POST
-- /api/admin/i18n/machine-translate); the 'i18n_machine' job (cmd/audit)
-- works through them before filling missing release titles. A failed item
-- backs off via next_try_at and is dropped after a few attempts.
CREATE TABLE IF NOT EXISTS mt_requests (
  entity_type text NOT NULL,
  entity_id text NOT NULL,
  field text NOT NULL,
  lang text NOT NULL CHECK (lang IN ('zh-CN','zh-TW','en','ja','ko','vi','es','th')),
  requested_by text,
  requested_at timestamptz NOT NULL DEFAULT now(),
  attempts int NOT NULL DEFAULT 0,
  next_try_at timestamptz NOT NULL DEFAULT now(),
  last_error text,
  PRIMARY KEY (entity_type, entity_id, field, lang)
);
CREATE INDEX IF NOT EXISTS mt_requests_due ON mt_requests (next_try_at, requested_at);

-- Requests made to a provider per UTC day, shared by every process: the
-- OPENROUTER_DAILY_CAP free-tier budget.
CREATE TABLE IF NOT EXISTS mt_usage (
  day date NOT NULL,
  provider text NOT NULL,
  requests int NOT NULL DEFAULT 0 CHECK (requests >= 0),
  PRIMARY KEY (day, provider)
);
