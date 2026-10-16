-- Shared cache of POST /api/resolve results (internal/media/resolve_cache.go),
-- behind each API instance's in-process cache, so restarts and several
-- instances reuse a resolved stream instead of re-reading the provider page.
-- Re-run on every start: every statement is idempotent.
--
-- key:            resolve:<source>:<id> (source names never contain ":")
-- result:         the ResolveResult JSON (stream URLs; never cookies or
--                 auth headers, those results stay in process)
-- expires_at:     sliding expiry, moved forward on access
-- max_expires_at: hard cap (earliest stream expiry - 30 s, or resolved +
--                 MEDIA_RESOLVE_MAX_AGE)
-- refreshed_at:   when the result was resolved from the provider
-- Expired rows are purged by the API (at most every 10 min per instance).
CREATE TABLE IF NOT EXISTS media_resolve_cache (
  key text PRIMARY KEY CHECK (length(key) <= 1024),
  result jsonb NOT NULL CHECK (octet_length(result::text) <= 65536),
  expires_at timestamptz NOT NULL,
  max_expires_at timestamptz NOT NULL,
  refreshed_at timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at <= max_expires_at)
);
CREATE INDEX IF NOT EXISTS media_resolve_cache_expires_idx ON media_resolve_cache (expires_at);
