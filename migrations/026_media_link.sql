-- Offline release -> playback provider linking (internal/medialink).
-- The hourly 'media_link' job (cmd/scheduler enqueues, cmd/audit runs) and
-- the on-demand fallback in GET /api/releases/{slug}/playback search the
-- provider by each DVD code and store a unique exact match in
-- release_media_sources. One row per (release, provider) records the latest
-- attempt so neither path searches again before next_try_at. Idempotent.
--
-- status:      found | not_found | ambiguous | error
-- next_try_at: when the release is due again; NULL = never automatically
--              (found, ambiguous: an admin decides). Backoff is in
--              internal/medialink (not_found 7 d, 30 d after 3 misses;
--              error 10 min doubling to 6 h).
-- attempts:    consecutive attempts that ended in the current status.
-- candidates:  JSON array of provider media ids only; never response bodies.
-- last_error:  short error text (no credentials, no bodies).
CREATE TABLE IF NOT EXISTS media_link_attempts (
  release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
  provider text NOT NULL,
  status text NOT NULL CHECK (status IN ('found','not_found','ambiguous','error')),
  tried_at timestamptz NOT NULL DEFAULT now(),
  next_try_at timestamptz,
  attempts integer NOT NULL DEFAULT 1 CHECK (attempts >= 0),
  matched_media_id text,
  candidates jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(candidates) = 'array'),
  last_error text,
  PRIMARY KEY (release_id, provider)
);
CREATE INDEX IF NOT EXISTS media_link_attempts_due ON media_link_attempts(provider, next_try_at);
-- Admin list (newest attempts first) and the daily-cap count.
CREATE INDEX IF NOT EXISTS media_link_attempts_recent ON media_link_attempts(provider, tried_at DESC, release_id DESC);
