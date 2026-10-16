-- Splitting entities that were never merged (FINAL_DATA_DESIGN 5.6). A split
-- is a review_events row (action 'split', the full plan in after_value), its
-- undo a merge event (action 'undo_split') whose reverts_event_id names it,
-- and a not_duplicate_pairs row whose review_event_id names it. No new
-- tables: source_records.performer_id / release_id and
-- release_performer_sources already map evidence to entities. Idempotent.

-- Ingest looks up a release record's previous credits by source record.
CREATE INDEX IF NOT EXISTS release_performer_sources_record ON release_performer_sources(source_record_id);
-- ... and whether an entity is either side of a not-duplicate pair.
CREATE INDEX IF NOT EXISTS not_duplicate_pairs_b ON not_duplicate_pairs(entity_type, b_id);
CREATE INDEX IF NOT EXISTS not_duplicate_pairs_event ON not_duplicate_pairs(review_event_id) WHERE review_event_id IS NOT NULL;
-- Split history of an entity in the admin views.
CREATE INDEX IF NOT EXISTS review_events_split ON review_events(entity_type, entity_id) WHERE action IN ('split','undo_split');
