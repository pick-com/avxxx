-- Identity merge, not-duplicate memory and slug redirects (FINAL_DATA_DESIGN 5-6,
-- DESIGN_ALIGNMENT AdminDedupe). Re-run on every start, so every statement is
-- idempotent.

-- Who made a review decision, which candidate it decided, and which earlier
-- event it reverts (undo_merge -> merge). An event can be reverted only once.
ALTER TABLE review_events ADD COLUMN IF NOT EXISTS actor text;
ALTER TABLE review_events ADD COLUMN IF NOT EXISTS candidate_id bigint REFERENCES identity_candidates(id);
ALTER TABLE review_events ADD COLUMN IF NOT EXISTS reverts_event_id bigint REFERENCES review_events(id);
CREATE UNIQUE INDEX IF NOT EXISTS review_events_reverts_once ON review_events(reverts_event_id) WHERE reverts_event_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS review_events_entity ON review_events(entity_type, entity_id, created_at);

-- review_events is append-only: corrections are new events, never edits.
CREATE OR REPLACE FUNCTION review_events_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'review_events is append-only';
END $$;
DROP TRIGGER IF EXISTS review_events_append_only ON review_events;
CREATE TRIGGER review_events_append_only BEFORE UPDATE OR DELETE ON review_events
  FOR EACH ROW EXECUTE FUNCTION review_events_append_only();

-- Pairs an editor ruled are different entities; candidate generation never
-- suggests them again. Stored canonically with a_id < b_id.
CREATE TABLE IF NOT EXISTS not_duplicate_pairs (
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  a_id bigint NOT NULL, b_id bigint NOT NULL,
  review_event_id bigint REFERENCES review_events(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(entity_type, a_id, b_id),
  CHECK(a_id < b_id)
);

-- Old public slug -> surviving entity. Public detail reads answer 301 with the
-- surviving entity's current slug (following any later merges).
CREATE TABLE IF NOT EXISTS slug_redirects (
  entity_type text NOT NULL CHECK(entity_type IN ('performer','release')),
  old_slug text NOT NULL,
  entity_id bigint NOT NULL,
  review_event_id bigint REFERENCES review_events(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(entity_type, old_slug)
);
CREATE INDEX IF NOT EXISTS slug_redirects_event ON slug_redirects(review_event_id);

CREATE INDEX IF NOT EXISTS identity_candidates_queue ON identity_candidates(entity_type, status, score DESC);
CREATE INDEX IF NOT EXISTS performers_merged_into ON performers(merged_into_id) WHERE merged_into_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS releases_merged_into ON releases(merged_into_id) WHERE merged_into_id IS NOT NULL;
