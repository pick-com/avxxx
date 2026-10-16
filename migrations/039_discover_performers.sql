-- Discover, search and performers (PRD 4.2-4.5, 6; tech plan 3.4, 3.13).
-- Re-run on every start: every statement is idempotent.

-- Performer statistics (Actor 统计, NavActors filters and sorts), rebuilt by
-- the hourly 'recs_trending' job (internal/recs RunTrending ->
-- RefreshPerformerStats) right after the performer heat boards. One row per
-- active, unmerged performer; rows of performers that stopped qualifying are
-- deleted by the same run.
--   works_count      published, unmerged works
--   lead_count       of which a lead credit (store.LeadCreditSQL: 主演)
--   upcoming_count   upcoming works (status upcoming, or a future release date)
--   total_plays      plays of the works over everything still retained
--                    (events 90 days, watch_history), same counting as the boards
--   followers        follows rows
--   heat_30d         performer_heat_snapshots 'month' score x 1000 (0 = none)
--   heat_rank        rank on that board (NULL = not on it)
--   first/latest_work_date  release dates of the first / latest work (新晋, 活跃年代)
--   top_tags         the five most frequent tags of the works (擅长类型)
--   audio_langs      audio languages of the works' active playback sources (作品语言)
CREATE TABLE IF NOT EXISTS performer_stats (
  performer_id bigint PRIMARY KEY REFERENCES performers(id) ON DELETE CASCADE,
  works_count integer NOT NULL DEFAULT 0,
  lead_count integer NOT NULL DEFAULT 0,
  upcoming_count integer NOT NULL DEFAULT 0,
  total_plays bigint NOT NULL DEFAULT 0,
  followers integer NOT NULL DEFAULT 0,
  heat_30d integer NOT NULL DEFAULT 0,
  heat_rank integer,
  first_work_date date,
  latest_work_date date,
  top_tags text[] NOT NULL DEFAULT '{}',
  audio_langs text[] NOT NULL DEFAULT '{}',
  computed_at timestamptz NOT NULL DEFAULT now()
);
-- Keyset sorts of GET /api/performers (expressions match internal/store/catalog_query.go).
CREATE INDEX IF NOT EXISTS performer_stats_heat ON performer_stats (heat_30d DESC, performer_id DESC);
CREATE INDEX IF NOT EXISTS performer_stats_followers ON performer_stats (followers DESC, performer_id DESC);
CREATE INDEX IF NOT EXISTS performer_stats_works ON performer_stats (works_count DESC, performer_id DESC);
CREATE INDEX IF NOT EXISTS performer_stats_rising
  ON performer_stats ((COALESCE(first_work_date, DATE '0001-01-01')) DESC, performer_id DESC);
CREATE INDEX IF NOT EXISTS performer_stats_tags ON performer_stats USING gin (top_tags);
CREATE INDEX IF NOT EXISTS performer_stats_langs ON performer_stats USING gin (audio_langs);

-- Signed-in recent searches (MobileSearchTyping 最近搜索, tech plan 3.13).
-- query is search.NormalizeQuery's form; one row per distinct query, the
-- API keeps the newest 20 per user.
CREATE TABLE IF NOT EXISTS user_search_history (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  query text NOT NULL CHECK (btrim(query) <> '' AND length(query) <= 200),
  searched_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, query)
);
CREATE INDEX IF NOT EXISTS user_search_history_recent ON user_search_history (user_id, searched_at DESC);

-- 即将上线 (NavNew, GET /api/upcoming): the published branch (a future
-- release date, compared with a bound date) range-scans
-- releases_public_newest; the upcoming branch reads releases_upcoming_due
-- (014). Expression statistics let the planner see how few releases lie
-- ahead (otherwise it assumes a third of the table and scans it).
CREATE STATISTICS IF NOT EXISTS releases_newest_key_stats ON (COALESCE(release_date, DATE '0001-01-01')) FROM releases;

-- Playback-source facets of GET /api/releases (清晰度 min_height, 字幕
-- subtitle, 语言 audio_lang). Common values walk releases_public_newest and
-- probe playback_sources_release; these let a rare value start from the
-- matching sources instead (conditions match catalog_query.go).
CREATE INDEX IF NOT EXISTS playback_sources_height ON playback_sources (max_height, release_id)
  WHERE status<>'disabled';
CREATE INDEX IF NOT EXISTS playback_sources_subtitled ON playback_sources (release_id)
  WHERE status<>'disabled' AND cardinality(subtitle_langs)>0;
CREATE INDEX IF NOT EXISTS playback_sources_subtitle_langs ON playback_sources USING gin (subtitle_langs)
  WHERE status<>'disabled';
CREATE INDEX IF NOT EXISTS playback_sources_audio_langs ON playback_sources USING gin (audio_langs)
  WHERE status<>'disabled';
-- Without it 字幕 "any" is estimated at a third of all sources.
CREATE STATISTICS IF NOT EXISTS playback_sources_subtitled_stats ON (cardinality(subtitle_langs)) FROM playback_sources;
