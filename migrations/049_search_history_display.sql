-- Recent searches keep the spelling the user typed (最近搜索 showed "Summer"
-- as "summer"). query stays the normalized key (search.NormalizeQuery) that
-- dedupes case- and width-insensitively; display is search.DisplayQuery's
-- form, updated to the latest spelling on a repeat. Rows written before this
-- migration, or by an older binary during a rolling deploy, have no display
-- and show the normalized query. Idempotent: re-run on every start.
ALTER TABLE user_search_history ADD COLUMN IF NOT EXISTS display text;
