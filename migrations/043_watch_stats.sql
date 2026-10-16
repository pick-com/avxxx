-- 观看统计 WatchStats (PRD 20, tech plan 3.13): user_watch_daily, one row per
-- user and local day, rebuilt by the 'watch_stats' (hourly, today) and
-- 'watch_stats_nightly' (the days before) jobs of cmd/audit
-- (internal/watchstats) from play events and watch_history within the 90-day
-- event retention. Only the owner reads it (GET /api/me/watch-stats); it is
-- part of the account data export, cleared with the watch history and
-- removed with the account (ON DELETE CASCADE). Re-run on every start: every
-- statement is idempotent.
--
-- day is the calendar date in tz, the user's user_settings.timezone when the
-- row was built (UTC without a settings row). Watch time is spread over the
-- local hours a play covered, so a play across midnight counts on both days;
-- plays, titles and finished count on the day the play started.
CREATE TABLE IF NOT EXISTS user_watch_daily (
  user_id bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  day date NOT NULL,
  tz text NOT NULL DEFAULT 'UTC',
  watch_seconds integer NOT NULL DEFAULT 0 CHECK (watch_seconds BETWEEN 0 AND 86400),
  plays integer NOT NULL DEFAULT 0 CHECK (plays >= 0),             -- plays started
  titles integer NOT NULL DEFAULT 0 CHECK (titles >= 0),           -- distinct releases started
  finished integer NOT NULL DEFAULT 0 CHECK (finished >= 0),       -- distinct releases watched to >= 90 %
  -- watch seconds per local hour 0..23 (sums to watch_seconds).
  by_hour integer[] NOT NULL DEFAULT array_fill(0, ARRAY[24]) CHECK (cardinality(by_hour) = 24),
  -- at most 10 each, by seconds: [{"id": performer id, "seconds": n}] and
  -- [{"tag": release_tags.normalized_tag, "seconds": n}].
  top_performers jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(top_performers) = 'array'),
  top_tags jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(top_tags) = 'array'),
  built_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, day)
);
CREATE INDEX IF NOT EXISTS user_watch_daily_day ON user_watch_daily (day);
