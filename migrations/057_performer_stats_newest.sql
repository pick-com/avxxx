-- GET /api/performers?sort=newest_work reads the page from performer_stats
-- (internal/store/catalog_query.go: the expression and order match), like
-- sort=works does with performer_stats_works (039), instead of sorting
-- every performer. Re-run on every start: idempotent.
CREATE INDEX IF NOT EXISTS performer_stats_newest
  ON performer_stats ((COALESCE(latest_work_date, DATE '0001-01-01')) DESC, performer_id DESC);
