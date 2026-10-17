-- Synthetic production-volume data for the performance pass (docs/PERFORMANCE.md).
--
-- Run it ONLY against a disposable, migrated database (scripts/perf/setup.sh
-- starts one): it assumes empty catalog / user tables and inserts with
-- contiguous ids.
--
-- Defaults are SMALL (1k performers, 5k releases, 1k users: ~150k rows,
-- well under 200 MB on disk) so it is safe on a nearly full Docker disk.
-- Production volume is 50k performers, 300k releases, 200k users (~25M
-- rows, several GB of data + WAL): check free Docker disk first
-- (docs/PERFORMANCE.md "Full load test").
--
--   psql "$URL" -v ON_ERROR_STOP=1 -v performers=50000 -v releases=300000 -v users=200000 -f scripts/perf/seed.sql
--
-- Activity tables (comments, danmaku, reviews, clips, play / search events)
-- scale with users; series with releases.
--
-- Popularity is skewed (index = n * random()^k) so hot releases, performers
-- and tags look like a real catalog: a long tail with a few heavy hitters.
\set ON_ERROR_STOP 1
\if :{?performers}
\else
\set performers 1000
\endif
\if :{?releases}
\else
\set releases 5000
\endif
\if :{?users}
\else
\set users 1000
\endif
\timing on
SELECT greatest(:releases/25,1) AS n_series, :users*3 AS n_comments, greatest(:users/2,1) AS n_reviews,
  :users*15/2 AS n_danmaku, :users*3/4 AS n_clips, :users*3 AS n_plays, :users*2 AS n_searches,
  greatest(:users*3/4,1) AS n_devices \gset
SELECT setseed(0.42);
SET synchronous_commit = off;

-- ---- catalog -----------------------------------------------------------
INSERT INTO sources(name,url,enabled,adapter,source_kind,license_status)
VALUES ('Perf Catalog','https://perf.invalid/catalog',true,'rss','fixture','internal')
ON CONFLICT(url) DO NOTHING;

INSERT INTO series(slug,name,maker_name)
SELECT 'perf-series-'||g, 'Series '||g, 'Maker '||(g%400) FROM generate_series(1,:n_series) g;

INSERT INTO performers(slug,canonical_name,canonical_name_ja,birth_date,debut_date,height_cm,bust_cm,waist_cm,hip_cm,cup_size,status,created_at)
SELECT 'perf-p-'||g,
  (ARRAY['Yui','Mio','Rin','Aoi','Hana','Saki','Nana','Emi','Ai','Mei','Riko','Kana','Yuna','Momo','Sora','Hikari'])[1+g%16]||' '||
  (ARRAY['Tanaka','Suzuki','Sato','Takahashi','Ito','Watanabe','Yamamoto','Nakamura','Kobayashi','Kato','Yoshida','Yamada'])[1+(g/16)%12]||' '||g,
  '演者'||g,
  date '1985-01-01' + (g*37)%7000, date '2005-01-01' + (g*53)%7300,
  148+(g%30), 78+(g%25), 54+(g%10), 80+(g%20), chr(65+(g%8)),
  CASE WHEN g%97=0 THEN 'retired' ELSE 'active' END,
  now() - ((g%3000)||' days')::interval
FROM generate_series(1,:performers) g;

INSERT INTO performer_aliases(performer_id,name,normalized_name,language,alias_type)
SELECT p.id, 'Alias '||p.id, 'alias '||p.id, 'ja', 'stage' FROM performers p WHERE p.slug LIKE 'perf-p-%' AND p.id%3=0;

INSERT INTO releases(slug,canonical_title,title_ja,release_date,runtime_minutes,maker_name,label_name,series_name,series_id,director_name,synopsis,status,scheduled_at,created_at,shoot_year)
SELECT 'perf-r-'||g,
  (ARRAY['Summer','Secret','Office','Campus','Night','Hot Spring','Private','First','Forbidden','Weekend'])[1+g%10]||' '||
  (ARRAY['Story','Diary','Lesson','Holiday','Affair','Memories','Encounter','Days','Debut','Special'])[1+(g/10)%10]||' '||g,
  '作品タイトル'||g,
  CASE WHEN g%100=0 THEN current_date + (g%60) ELSE current_date - ((g::bigint*7919)%7300)::int END,
  60+(g%180),
  'Maker '||(floor(400*random()^2))::int, 'Label '||(g%900),
  CASE WHEN g%4=0 THEN 'Series '||(1+g%:n_series) END,
  CASE WHEN g%4=0 THEN (SELECT min(id) FROM series)+(g%:n_series) END,
  'Director '||(g%2000),
  'Synopsis for release '||g||'. '||repeat('Lorem ipsum dolor sit amet. ', 6),
  CASE WHEN g%100=0 THEN 'upcoming' WHEN g%250=1 THEN 'unpublished' ELSE 'published' END,
  CASE WHEN g%100=0 THEN now() + ((g%60)||' days')::interval END,
  now() - (((g::bigint*7919)%7300)::int||' days')::interval,
  2005+(g%20)
FROM generate_series(1,:releases) g;

CREATE TEMP TABLE perf_bounds AS
SELECT (SELECT min(id) FROM performers WHERE slug LIKE 'perf-p-%') AS p0,
       (SELECT min(id) FROM releases WHERE slug LIKE 'perf-r-%') AS r0,
       :performers::bigint AS np, :releases::bigint AS nr;

-- codes: one dvd_code and one content_id per release (400 prefixes).
INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value)
SELECT b.r0+g-1, 'dvd_code', upper(pfx)||'-'||lpad((g/400+1)::text,3,'0'), pfx||'-'||lpad((g/400+1)::text,3,'0')
FROM perf_bounds b, generate_series(1,:releases) g,
  LATERAL (SELECT chr(97+(g%400)%26)||chr(97+((g%400)/26)%26)||chr(97+((g%400)*7)%26) AS pfx) x;
INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value)
SELECT ri.release_id, 'content_id', replace(ri.normalized_value,'-','00'), replace(ri.normalized_value,'-','00')
FROM release_identifiers ri WHERE ri.identifier_type='dvd_code';

-- credits: a skewed lead plus 0-3 co-stars.
INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary,role)
SELECT b.r0+g-1, b.p0+floor(b.np*random()^2)::bigint, 1, true, 'lead'
FROM perf_bounds b, generate_series(1,:releases) g;
INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary,role)
SELECT b.r0+g-1, b.p0+floor(b.np*random()^1.5)::bigint, 1+k, false, 'guest'
FROM perf_bounds b, generate_series(1,:releases) g, generate_series(1,3) k
WHERE random() < 0.35
ON CONFLICT DO NOTHING;

-- tags: 5-7 of 400 per release, skewed.
INSERT INTO release_tags(release_id,tag,normalized_tag)
SELECT DISTINCT b.r0+g-1, 'Tag'||t, 'tag'||t
FROM perf_bounds b, generate_series(1,:releases) g, generate_series(1,6) k,
  LATERAL (SELECT floor(400*random()^2)::int + 0*g + 0*k AS t) x
ON CONFLICT DO NOTHING;

-- playback: one active external link per published release, a second
-- (degraded / hd) on a third of them, 5 % disabled.
INSERT INTO playback_sources(release_id,kind,provider,label,url,max_height,duration_seconds,audio_langs,subtitle_langs,version_kind,priority,status,health_score)
SELECT b.r0+g-1, 'external_link', (ARRAY['javtiful','missav','sextb','javguru'])[1+g%4], 'main',
  'https://perf.invalid/watch/'||g, (ARRAY[480,720,1080])[1+g%3], 3600+(g%5400), ARRAY['ja'],
  CASE WHEN g%5=0 THEN ARRAY['zh','en'] ELSE '{}' END, 'main', 0,
  CASE WHEN g%20=0 THEN 'disabled' ELSE 'active' END, 0.9
FROM perf_bounds b, generate_series(1,:releases) g;
INSERT INTO playback_sources(release_id,kind,provider,label,url,max_height,duration_seconds,audio_langs,version_kind,priority,status,health_score)
SELECT b.r0+g-1, 'external_link', 'sextb', 'HD', 'https://perf.invalid/hd/'||g, 1080, 3600, ARRAY['ja'], 'high_quality', 1,
  CASE WHEN g%7=0 THEN 'degraded' ELSE 'active' END, 0.6
FROM perf_bounds b, generate_series(1,:releases) g WHERE g%3=0;
INSERT INTO release_media_sources(release_id,source,media_id,media_url,confidence)
SELECT b.r0+g-1, 'javtiful', 'm'||g, 'https://perf.invalid/m/'||g, 'exact'
FROM perf_bounds b, generate_series(1,:releases) g WHERE g%10<7;

-- ---- users and activity -------------------------------------------------
INSERT INTO users(email,password_hash,display_name,created_at,adult_confirmed_at)
SELECT 'perf-'||g||'@example.test', 'x', 'User '||g, now() - ((g%700)||' days')::interval, now()
FROM generate_series(1,:users) g;
ALTER TABLE perf_bounds ADD COLUMN u0 bigint, ADD COLUMN nu bigint;
UPDATE perf_bounds SET u0=(SELECT min(id) FROM users WHERE email LIKE 'perf-%'), nu=:users;

-- watch history: ~20 per user on average, heavy-tailed (some users 100+).
INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at)
SELECT b.u0+u-1, b.r0+floor(b.nr*random()^3)::bigint, (random()*5400)::int, 5400, now() - random()*random()*interval '365 days'
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1, 1+(u*7919%40)) k
ON CONFLICT DO NOTHING;

INSERT INTO favorites(user_id,release_id,created_at)
SELECT b.u0+u-1, b.r0+floor(b.nr*random()^3)::bigint, now() - random()*interval '300 days'
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,5) k
ON CONFLICT DO NOTHING;

INSERT INTO follows(user_id,performer_id,created_at)
SELECT b.u0+u-1, b.p0+floor(b.np*random()^3)::bigint, now() - random()*interval '300 days'
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,3) k
ON CONFLICT DO NOTHING;

INSERT INTO watch_later(user_id,release_id,position)
SELECT b.u0+u-1, b.r0+floor(b.nr*random()^2)::bigint, k
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,2) k
ON CONFLICT DO NOTHING;

INSERT INTO playlists(user_id,name,visibility,share_token,position)
SELECT b.u0+u-1, 'List '||k, CASE WHEN (u+k)%5=0 THEN 'public' ELSE 'private' END,
  md5('pl'||u||'-'||k), k
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,2) k WHERE u%3=0;
INSERT INTO playlist_items(playlist_id,release_id,position)
SELECT pl.id, (SELECT r0 FROM perf_bounds)+floor((SELECT nr FROM perf_bounds)*random()^2)::bigint + 0*pl.id, k
FROM playlists pl, generate_series(1,10) k
ON CONFLICT DO NOTHING;

INSERT INTO ratings(release_id,voter_key,user_id,score,weight,created_at)
SELECT b.r0+floor(b.nr*random()^3)::bigint, 'u:'||(b.u0+u-1), b.u0+u-1, 1+floor(5*random()^0.5)::int, 1, now() - random()*interval '200 days'
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,5) k
ON CONFLICT DO NOTHING;

INSERT INTO release_reactions(release_id,voter_key,user_id,value)
SELECT b.r0+floor(b.nr*random()^3)::bigint, 'u:'||(b.u0+u-1), b.u0+u-1, CASE WHEN random()<0.85 THEN 1 ELSE -1 END
FROM perf_bounds b, generate_series(1,:users) u, generate_series(1,3) k
ON CONFLICT DO NOTHING;

INSERT INTO comments(release_id,user_id,body,likes,status,created_at)
SELECT b.r0+floor(b.nr*random()^4)::bigint, b.u0+floor(b.nu*random())::bigint,
  'Comment '||g||' '||repeat('great scene ', 1+g%8), floor(20*random()^4)::int,
  CASE WHEN g%50=0 THEN 'shadow' ELSE 'visible' END, now() - random()*interval '365 days'
FROM perf_bounds b, generate_series(1,:n_comments) g;
INSERT INTO comments(release_id,parent_id,user_id,body,status,created_at)
SELECT c.release_id, c.id, (SELECT u0 FROM perf_bounds)+floor((SELECT nu FROM perf_bounds)*random())::bigint, 'Reply to '||c.id, 'visible', c.created_at + interval '1 hour'
FROM comments c WHERE c.id%5=0 AND c.parent_id IS NULL;

INSERT INTO reviews(release_id,user_id,body,watch_ratio,status,created_at)
SELECT b.r0+floor(b.nr*random()^3)::bigint, b.u0+floor(b.nu*random())::bigint, 'Review '||g, 0.9, 'visible', now() - random()*interval '365 days'
FROM perf_bounds b, generate_series(1,:n_reviews) g
ON CONFLICT DO NOTHING;

INSERT INTO danmaku(release_id,version_key,t_ms,body,color,mode,lang,user_id,status,created_at)
SELECT b.r0+floor(b.nr*random()^5)::bigint, 'main', floor(5400000*random())::int, 'dm '||g, '#FFFFFF', 'scroll', 'zh',
  b.u0+floor(b.nu*random())::bigint, 'visible', now() - random()*interval '200 days'
FROM perf_bounds b, generate_series(1,:n_danmaku) g;

INSERT INTO rec_feedback(owner_key,user_id,kind,target_id,target_tag,created_at)
SELECT 'u:'||(b.u0+u-1), b.u0+u-1, k.kind,
  CASE k.kind WHEN 'not_interested_release' THEN b.r0+floor(b.nr*random()^2)::bigint
              WHEN 'not_interested_performer' THEN b.p0+floor(b.np*random()^2)::bigint END,
  CASE WHEN k.kind='not_interested_tag' THEN 'tag'||floor(400*random())::int END,
  now() - random()*interval '90 days'
FROM perf_bounds b, generate_series(1,:users) u,
  (VALUES ('not_interested_release'),('not_interested_performer'),('not_interested_tag')) k(kind)
WHERE u%4=0
ON CONFLICT DO NOTHING;

INSERT INTO clips(public_id,release_id,user_id,start_ms,end_ms,title,status,title_status,created_at)
SELECT substr(md5('clip'||g),1,10), b.r0+floor(b.nr*random()^3)::bigint, b.u0+floor(b.nu*random()^2)::bigint,
  s, s+5000+floor(55000*random())::int, 'Clip '||g, 'visible', 'visible', now() - random()*interval '120 days'
FROM perf_bounds b, generate_series(1,:n_clips) g, LATERAL (SELECT floor(3000000*random())::int + 0*g AS s) x;

INSERT INTO user_search_history(user_id,query,searched_at)
SELECT b.u0+u-1, 'tag'||floor(400*random())::int, now() - random()*interval '30 days'
FROM perf_bounds b, generate_series(1,:users) u WHERE u%5=0
ON CONFLICT DO NOTHING;

-- ---- events: 30 days of plays and searches --------------------------------
SELECT events_ensure_partition(((now() AT TIME ZONE 'UTC') - interval '1 month')::date);
CREATE TEMP TABLE perf_plays AS
SELECT 'pp'||g AS play_id,
  now() - random()*interval '30 days' AS ts,
  CASE WHEN g%3=0 THEN NULL ELSE (SELECT u0 FROM perf_bounds)+floor((SELECT nu FROM perf_bounds)*random())::bigint END AS user_id,
  'dev'||(g%:n_devices) AS device_id, 'sess'||(g/3) AS session_id,
  (SELECT r0 FROM perf_bounds)+floor((SELECT nr FROM perf_bounds)*random()^3)::bigint AS release_id,
  floor(3600*random())::int AS watched
FROM generate_series(1,:n_plays) g;
INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,user_id,device_id,session_id,play_id,platform,app_ver,ui_lang,page,release_id,props)
SELECT md5(p.play_id||e.event||e.n), e.event, 1, p.ts + e.off, p.ts + e.off, p.user_id, p.device_id, p.session_id, p.play_id, 'web', '1.0.0', 'zh', 'release', p.release_id,
  CASE e.event
    WHEN 'play_end' THEN jsonb_build_object('watched_seconds', p.watched, 'reason', CASE WHEN p.watched > 3000 THEN 'completed' ELSE 'user' END)
    WHEN 'progress' THEN jsonb_build_object('milestone', CASE WHEN p.watched > 3000 THEN '100' ELSE '25' END)
    ELSE '{}'::jsonb END
FROM perf_plays p,
  LATERAL (VALUES ('play_request', 0, interval '0'), ('heartbeat', 1, interval '60 seconds'), ('heartbeat', 2, interval '120 seconds'),
                  ('progress', 3, interval '150 seconds'), ('play_end', 4, make_interval(secs => p.watched + 1))) e(event, n, off);
INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,user_id,device_id,session_id,platform,app_ver,ui_lang,page,props)
SELECT md5('search'||g), 'search', 1, t, t, CASE WHEN g%2=0 THEN (SELECT u0 FROM perf_bounds)+floor((SELECT nu FROM perf_bounds)*random())::bigint END,
  'dev'||(g%:n_devices), 'sess-s'||(g/4), 'web', '1.0.0', 'zh', 'search',
  jsonb_build_object('query', 'tag'||floor(400*random()^2)::int, 'result_count', floor(50*random())::int,
    'clicked_rank', CASE WHEN g%3=0 THEN floor(20*random())::int END)
FROM generate_series(1,:n_searches) g, LATERAL (SELECT now() - random()*interval '30 days' + 0*g*interval '1s' AS t) x;

ANALYZE;
SELECT relname, n_live_tup FROM pg_stat_user_tables WHERE n_live_tup > 1000 ORDER BY n_live_tup DESC;
SELECT pg_size_pretty(pg_database_size(current_database())) AS database_size;
