-- 收藏夹 (playlists, PRD 7 NavFavorites; tech plan 3.13) and the watch-later
-- auto-remove switch (NavLater 「看完自动移出（可关）」). Re-run on every
-- start: every statement is idempotent.
--
-- A user has any number of playlists; exactly one is_default playlist backs
-- the release page's quick 收藏 and the legacy /api/me/favorites routes.
-- visibility: private (owner only), public (anyone with the link; may be
-- indexed), link (anyone with the link; noindex). share_token is the public
-- URL key (/playlists/{share_token}) and can be rotated by the owner, which
-- revokes old links.
--
-- The single-list favorites table (006) is migrated once, when the playlists
-- table is first created: every user with favorites gets a default playlist
-- holding the same releases with their original created_at. favorites is
-- left in place untouched (no data is dropped) but is no longer written.

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM information_schema.tables
    WHERE table_schema = current_schema() AND table_name = 'playlists'
  ) THEN
    CREATE TABLE playlists (
      id bigserial PRIMARY KEY,
      user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
      name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
      description text NOT NULL DEFAULT '' CHECK (char_length(description) <= 300),
      visibility text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private','public','link')),
      share_token text NOT NULL UNIQUE DEFAULT replace(gen_random_uuid()::text, '-', ''),
      is_default boolean NOT NULL DEFAULT false,
      position integer NOT NULL DEFAULT 0,
      created_at timestamptz NOT NULL DEFAULT now(),
      updated_at timestamptz NOT NULL DEFAULT now()
    );
    CREATE TABLE playlist_items (
      playlist_id bigint NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
      release_id bigint NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
      created_at timestamptz NOT NULL DEFAULT now(),
      PRIMARY KEY (playlist_id, release_id)
    );
    INSERT INTO playlists(user_id, name, is_default, created_at)
    SELECT user_id, '默认收藏夹', true, min(created_at) FROM favorites GROUP BY user_id;
    INSERT INTO playlist_items(playlist_id, release_id, created_at)
    SELECT p.id, f.release_id, f.created_at
    FROM favorites f JOIN playlists p ON p.user_id = f.user_id AND p.is_default;
  END IF;
END
$$;
CREATE UNIQUE INDEX IF NOT EXISTS playlists_one_default ON playlists (user_id) WHERE is_default;
CREATE INDEX IF NOT EXISTS playlists_user ON playlists (user_id, position, id);
CREATE INDEX IF NOT EXISTS playlist_items_release ON playlist_items (release_id);
CREATE INDEX IF NOT EXISTS playlist_items_recent ON playlist_items (playlist_id, created_at DESC);

-- 看完自动移出: drop a watch-later entry once watch progress reaches 90 %.
ALTER TABLE user_settings ADD COLUMN IF NOT EXISTS later_auto_remove boolean NOT NULL DEFAULT true;
