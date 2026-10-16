-- Admin performer editor (AdminActors / A9, PRD 6.1, 6.4, 11, 15 演员个人信息).
-- Re-run on every start: idempotent.

-- PRD 15: a performer (本人) or their agency (经纪方) may ask to hide the
-- birthday. birth_date keeps the real value: R-AGE (internal/audit) and the
-- admin API still read it, because the under-18 safety rule must never be
-- weakened by a privacy request. Every PUBLIC read goes through
-- store.PublicBirthDateSQL (NULL while hidden), so a hidden birthday has no
-- birth_date, no debut_age and no age at filming in any public response, and
-- falls out of any age filter (NULL never satisfies a comparison).
ALTER TABLE performers ADD COLUMN IF NOT EXISTS birth_date_hidden boolean NOT NULL DEFAULT false;
-- performers.status: active | hidden (no CHECK, like releases.status). Public
-- queries only return status='active'; hidden_reason says why an admin hid
-- the performer (junk name from R-PERFORMER-NAME, takedown request, ...).
ALTER TABLE performers ADD COLUMN IF NOT EXISTS hidden_reason text;

-- remove_photo requests suppress performer photos instead of deleting them
-- (the object stays for the retention policy). Any public photo read must
-- filter suppressed_at IS NULL.
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS suppressed_at timestamptz;
ALTER TABLE media_assets ADD COLUMN IF NOT EXISTS suppressed_reason text;

-- Correction / hide requests about a performer's personal information
-- (PRD 15), filed through the public correction page (POST
-- /api/performer-requests, no login) and decided in AdminActors. evidence
-- (requester role 本人/经纪方, message, links) and requester_contact are
-- private to admins: they never appear in public responses nor in
-- review_events. A correct_field request names field_name / proposed_value;
-- approving it writes the value as a performer_overrides row. Each decision
-- writes review_events (entity_type 'performer').
CREATE TABLE IF NOT EXISTS performer_requests (
  id bigserial PRIMARY KEY,
  performer_id bigint NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('correct_field','hide_birth_date','remove_photo','other')),
  field_name text,
  proposed_value jsonb,
  requester_contact text NOT NULL,
  evidence jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'open' CHECK (status IN ('open','approved','rejected')),
  due_at timestamptz NOT NULL,
  resolved_by text,
  resolution_note text,
  created_at timestamptz NOT NULL DEFAULT now(),
  resolved_at timestamptz,
  CHECK (kind <> 'correct_field' OR field_name IS NOT NULL),
  CHECK ((status = 'open') = (resolved_at IS NULL))
);
-- Admin queue: open requests by due date; per-performer history.
CREATE INDEX IF NOT EXISTS performer_requests_queue ON performer_requests (status, due_at, id);
CREATE INDEX IF NOT EXISTS performer_requests_performer ON performer_requests (performer_id, id DESC);

-- R-PERFORMER-NAME: a page <title> the same source used on several
-- performers' pages is the site's own title (store.PerformerNames).
CREATE INDEX IF NOT EXISTS source_records_performer_page_title ON source_records (source_id, (raw_data->>'page_title'))
  WHERE entity_type = 'performer' AND raw_data ? 'page_title';

-- AdminActors list filters.
CREATE INDEX IF NOT EXISTS performers_status ON performers (status) WHERE merged_into_id IS NULL;
CREATE INDEX IF NOT EXISTS performers_birth_hidden ON performers (id) WHERE birth_date_hidden;
CREATE INDEX IF NOT EXISTS identity_candidates_performer_left ON identity_candidates (left_id) WHERE entity_type = 'performer' AND status = 'pending';
CREATE INDEX IF NOT EXISTS identity_candidates_performer_right ON identity_candidates (right_id) WHERE entity_type = 'performer' AND status = 'pending';
