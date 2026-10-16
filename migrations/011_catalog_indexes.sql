-- Indexes for the public catalog list endpoints (filters, sorts, keyset pagination).
-- Expressions match internal/store/catalog_query.go exactly so the planner can use them.
CREATE INDEX IF NOT EXISTS releases_public_newest
  ON releases ((COALESCE(release_date, DATE '0001-01-01')) DESC, id DESC)
  WHERE status='published' AND merged_into_id IS NULL;
CREATE INDEX IF NOT EXISTS releases_public_oldest
  ON releases ((COALESCE(release_date, DATE '9999-12-31')), id)
  WHERE status='published' AND merged_into_id IS NULL;
CREATE INDEX IF NOT EXISTS releases_public_maker
  ON releases (maker_name) WHERE status='published' AND merged_into_id IS NULL;
-- release_performers' PK leads with release_id; performer filters and stats need the reverse.
CREATE INDEX IF NOT EXISTS release_performers_performer ON release_performers (performer_id, release_id);
-- release_tags' PK leads with release_id; tag filters need normalized_tag first.
CREATE INDEX IF NOT EXISTS release_tags_tag ON release_tags (normalized_tag, release_id);
CREATE INDEX IF NOT EXISTS release_identifiers_release ON release_identifiers (release_id);
CREATE INDEX IF NOT EXISTS performers_public_name
  ON performers (canonical_name, id) WHERE status='active' AND merged_into_id IS NULL;
