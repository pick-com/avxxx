package store

// Discover, search and performer pages (PRD 4.2-4.5, 6; tech plan 3.4,
// 3.13; migrations/039_discover_performers.sql): performer statistics and
// their hourly rebuild, the 即将上线 list, performer aliases and names,
// release cast roles, playback-source facets and signed-in search history.

import (
	"avxxx/internal/model"
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// UpcomingReleaseSQL is true for a release aliased r that is 即将上线: an
// upcoming release, or a published one whose release date is still ahead.
const UpcomingReleaseSQL = "r.merged_into_id IS NULL AND (r.status='upcoming' OR (r.status='published' AND COALESCE(r.release_date, DATE '0001-01-01') > CURRENT_DATE))"

// ---- performer_stats (hourly, internal/recs RunTrending) ----

// performerStatsSQL rebuilds every active performer's performer_stats row
// ($1/$2 the plays window start, $3 its end, $4 computed_at). Plays reuse
// the trending board counting (trendingInputsSQL) over everything retained.
var performerStatsSQL = `WITH rel(id, plays, prev, done) AS (` + trendingInputsSQL + `
), pub AS (
  SELECT rp.performer_id, r.id AS release_id, r.release_date, ` + LeadCreditSQL("rp") + ` AS lead
  FROM release_performers rp JOIN releases r ON r.id=rp.release_id
  WHERE r.status='published' AND r.merged_into_id IS NULL
), w AS (
  SELECT performer_id, count(*)::int AS works, count(*) FILTER (WHERE lead)::int AS leads,
    min(release_date) AS first, max(release_date) AS latest
  FROM pub GROUP BY 1
), up AS (
  SELECT rp.performer_id, count(*)::int AS n FROM release_performers rp JOIN releases r ON r.id=rp.release_id
  WHERE ` + UpcomingReleaseSQL + ` GROUP BY 1
), pl AS (
  SELECT pub.performer_id, sum(rel.plays)::bigint AS plays FROM pub JOIN rel ON rel.id=pub.release_id GROUP BY 1
), fo AS (
  SELECT performer_id, count(*)::int AS n FROM follows GROUP BY 1
), tg AS (
  SELECT performer_id, array_agg(normalized_tag ORDER BY n DESC, normalized_tag) AS tags FROM (
    SELECT pub.performer_id, rt.normalized_tag, count(*) AS n,
      row_number() OVER (PARTITION BY pub.performer_id ORDER BY count(*) DESC, rt.normalized_tag) AS rk
    FROM pub JOIN release_tags rt ON rt.release_id=pub.release_id GROUP BY 1, 2
  ) x WHERE rk <= 5 GROUP BY 1
), al AS (
  SELECT pub.performer_id, array_agg(DISTINCT l ORDER BY l) AS langs
  FROM pub JOIN playback_sources pbs ON pbs.release_id=pub.release_id AND pbs.status<>'disabled'
  CROSS JOIN LATERAL unnest(pbs.audio_langs) AS l GROUP BY 1
), h AS (
  SELECT performer_id, round(score * 1000)::int AS heat, rank FROM performer_heat_snapshots WHERE period='month'
)
INSERT INTO performer_stats (performer_id, works_count, lead_count, upcoming_count, total_plays, followers,
  heat_30d, heat_rank, first_work_date, latest_work_date, top_tags, audio_langs, computed_at)
SELECT p.id, COALESCE(w.works,0), COALESCE(w.leads,0), COALESCE(up.n,0), COALESCE(pl.plays,0), COALESCE(fo.n,0),
  COALESCE(h.heat,0), h.rank, w.first, w.latest, COALESCE(tg.tags,'{}'), COALESCE(al.langs,'{}'), $4
FROM performers p
LEFT JOIN w ON w.performer_id=p.id
LEFT JOIN up ON up.performer_id=p.id
LEFT JOIN pl ON pl.performer_id=p.id
LEFT JOIN fo ON fo.performer_id=p.id
LEFT JOIN tg ON tg.performer_id=p.id
LEFT JOIN al ON al.performer_id=p.id
LEFT JOIN h ON h.performer_id=p.id
WHERE p.status='active' AND p.merged_into_id IS NULL
ON CONFLICT (performer_id) DO UPDATE SET works_count=EXCLUDED.works_count, lead_count=EXCLUDED.lead_count,
  upcoming_count=EXCLUDED.upcoming_count, total_plays=EXCLUDED.total_plays, followers=EXCLUDED.followers,
  heat_30d=EXCLUDED.heat_30d, heat_rank=EXCLUDED.heat_rank, first_work_date=EXCLUDED.first_work_date,
  latest_work_date=EXCLUDED.latest_work_date, top_tags=EXCLUDED.top_tags, audio_langs=EXCLUDED.audio_langs,
  computed_at=EXCLUDED.computed_at`

// statsEpoch starts the all-time plays window (raw events are kept 90 days,
// watch_history longer).
var statsEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// RefreshPerformerStats implements recs.Store: it rewrites performer_stats
// for every active performer in one transaction (after the heat boards of
// the same run, which it reads) and drops the rows of performers that no
// longer qualify. It returns the number of rows written.
func (s *Store) RefreshPerformerStats(ctx context.Context, at time.Time) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, performerStatsSQL, statsEpoch, statsEpoch, at, at)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		_, err = tx.Exec(ctx, `DELETE FROM performer_stats WHERE computed_at<>$1`, at)
		return err
	})
	return n, err
}

// ---- 即将上线 (NavNew) ----

// UpcomingFilter selects a page of the 即将上线 list. Performer narrows it
// to one performer's works (Actor 即将上线 shelf); FollowedBy (a user id) to
// works of performers that user follows (只看关注的演员).
type UpcomingFilter struct {
	Performer  string
	FollowedBy int64
	Cursor     *Cursor
	Limit      int
	// Now fixes "today" (UTC) for tests; zero means time.Now.
	Now time.Time
}

// UpcomingSort is the cursor sort of the upcoming list (soonest first).
const UpcomingSort = "upcoming"

var upcomingSort = sortSpec{key: "u.ukey", keyText: "to_char(u.ukey,'YYYY-MM-DD')", kind: kindDate}

// noDate is the sort key of an upcoming release without any date (日期待定).
var noDate = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// UpcomingRelease is one 即将上线 row. Date is the day it goes live
// (scheduled_at in UTC, else release_date; null = 日期待定). Reservable
// is true for status upcoming: 预约 notifies on release (notifications.go);
// a published, future-dated work is already listed and needs none.
type UpcomingRelease struct {
	model.Release
	ScheduledAt *time.Time     `json:"scheduled_at"`
	Date        *string        `json:"date"`
	Reservable  bool           `json:"reservable"`
	Performers  []PerformerRef `json:"performers"`
}

// PerformerRef names a performer on a card.
type PerformerRef struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func buildUpcomingQuery(f UpcomingFilter) (string, []any, int, error) {
	if f.Cursor != nil && f.Cursor.Sort != UpcomingSort {
		return "", nil, 0, invalidQuery("cursor does not match sort")
	}
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	// A bound date (not CURRENT_DATE) lets the planner estimate the future
	// published branch (releases_newest_key_stats, 039) and range-scan
	// releases_public_newest.
	now := f.Now
	if now.IsZero() {
		now = time.Now()
	}
	y, m, d := now.UTC().Date()
	today := b.arg(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) + "::date"
	if f.Performer != "" {
		b.cond("u.id IN (SELECT rp.release_id FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE p.slug=" + b.arg(f.Performer) + ")")
	}
	if f.FollowedBy != 0 {
		b.cond("u.id IN (SELECT rp.release_id FROM follows fo JOIN release_performers rp ON rp.performer_id=fo.performer_id WHERE fo.user_id=" + b.arg(f.FollowedBy) + ")")
	}
	if err := b.after(upcomingSort, "u.id", f.Cursor); err != nil {
		return "", nil, 0, err
	}
	cols := `r.id,r.slug,r.canonical_title,COALESCE(r.title_ja,'') AS title_ja,r.release_date,COALESCE(r.runtime_minutes,0) AS runtime,
	  COALESCE(r.maker_name,'') AS maker,COALESCE(r.label_name,'') AS label,COALESCE(r.series_name,'') AS series,r.status`
	sql := `SELECT u.id,u.slug,u.canonical_title,u.title_ja,u.release_date,u.runtime,u.maker,u.label,u.series,u.status,u.sched,` + upcomingSort.keyText + `
	FROM (
	  SELECT ` + cols + `,r.scheduled_at AS sched,
	    COALESCE((r.scheduled_at AT TIME ZONE 'UTC')::date, r.release_date, DATE '9999-12-31') AS ukey
	  FROM releases r WHERE r.status='upcoming' AND r.merged_into_id IS NULL
	  UNION ALL
	  SELECT ` + cols + `,NULL::timestamptz,r.release_date
	  FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL AND COALESCE(r.release_date, DATE '0001-01-01') > ` + today + `
	) u` + b.whereSQL() + ` ORDER BY ` + upcomingSort.orderBy("u.id") + ` LIMIT ` + b.arg(limit+1)
	return sql, b.args, limit, nil
}

// UpcomingPage returns one page of 即将上线, soonest first, with up to
// three credited performers per release, and the next cursor.
func (s *Store) UpcomingPage(ctx context.Context, f UpcomingFilter) ([]UpcomingRelease, string, error) {
	sql, args, limit, err := buildUpcomingQuery(f)
	if err != nil {
		return nil, "", err
	}
	rows, err := s.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", err
	}
	out := []UpcomingRelease{}
	var keys []string
	for rows.Next() {
		var x UpcomingRelease
		var key string
		if err = rows.Scan(&x.ID, &x.Slug, &x.CanonicalTitle, &x.TitleJA, &x.ReleaseDate, &x.RuntimeMinutes, &x.MakerName, &x.LabelName, &x.SeriesName, &x.Status, &x.ScheduledAt, &key); err != nil {
			rows.Close()
			return nil, "", err
		}
		if key != noDate.Format(time.DateOnly) {
			d := key
			x.Date = &d
		}
		x.Reservable = x.Status == "upcoming"
		x.Performers = []PerformerRef{}
		out = append(out, x)
		keys = append(keys, key)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: UpcomingSort, Key: keys[limit-1], ID: out[limit-1].ID})
	}
	if len(out) == 0 {
		return out, next, nil
	}
	ids := make([]int64, len(out))
	at := map[int64]int{}
	for i, x := range out {
		ids[i] = x.ID
		at[x.ID] = i
	}
	cast, err := s.Query(ctx, `SELECT release_id,id,slug,canonical_name FROM (
		SELECT rp.release_id,p.id,p.slug,p.canonical_name,
		  row_number() OVER (PARTITION BY rp.release_id ORDER BY rp.is_primary DESC,rp.billing_order NULLS LAST,p.id) AS n
		FROM release_performers rp JOIN performers p ON p.id=rp.performer_id
		WHERE rp.release_id=ANY($1) AND p.status='active' AND p.merged_into_id IS NULL) c
		WHERE n<=3 ORDER BY release_id,n`, ids)
	if err != nil {
		return nil, "", err
	}
	defer cast.Close()
	for cast.Next() {
		var rid int64
		var p PerformerRef
		if err = cast.Scan(&rid, &p.ID, &p.Slug, &p.Name); err != nil {
			return nil, "", err
		}
		i := at[rid]
		out[i].Performers = append(out[i].Performers, p)
	}
	return out, next, cast.Err()
}

// ReservedAmong returns which of ids the user has reserved.
func (s *Store) ReservedAmong(ctx context.Context, userID int64, ids []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Query(ctx, `SELECT release_id FROM reservations WHERE user_id=$1 AND release_id=ANY($2)`, userID, ids)
	if err != nil {
		return nil, err
	}
	ids2, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	for _, id := range ids2 {
		out[id] = true
	}
	return out, err
}

// ---- performer page extras ----

// PublicAlias is a performer alias shown on the Actor page (别名).
type PublicAlias struct {
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	Type     string `json:"type"`
}

// LocalizedName is a reviewed translation of a performer's name (多语言名).
type LocalizedName struct {
	Lang string `json:"lang"`
	Name string `json:"name"`
}

// PerformerStats are the Actor page stats (作品 / 总播放 / 粉丝 / 30 天热度).
// WorksCount, LeadCount, UpcomingCount and Followers are live counts;
// TotalPlays, Heat (30-day heat x 1000), HeatRank and ComputedAt come from
// the hourly performer_stats run (zero / null before it).
type PerformerStats struct {
	WorksCount    int64      `json:"works_count"`
	LeadCount     int64      `json:"lead_count"`
	UpcomingCount int64      `json:"upcoming_count"`
	Followers     int64      `json:"followers"`
	TotalPlays    int64      `json:"total_plays"`
	Heat          int64      `json:"heat"`
	HeatRank      *int       `json:"heat_rank"`
	ComputedAt    *time.Time `json:"computed_at"`
}

// PerformerExtras is what GET /api/performers/{slug} adds to the profile.
type PerformerExtras struct {
	Aliases []PublicAlias   `json:"aliases"`
	Names   []LocalizedName `json:"names"`
	Stats   PerformerStats  `json:"stats"`
}

// PerformerExtrasByID loads a performer's public aliases (not 'merged'
// ones, which name merged duplicates), reviewed name translations and stats.
func (s *Store) PerformerExtrasByID(ctx context.Context, id int64) (PerformerExtras, error) {
	x := PerformerExtras{Aliases: []PublicAlias{}, Names: []LocalizedName{}}
	rows, err := s.Query(ctx, `SELECT a.name,COALESCE(a.language,''),a.alias_type FROM performer_aliases a JOIN performers p ON p.id=a.performer_id
		WHERE a.performer_id=$1 AND a.alias_type<>'merged' AND a.name<>p.canonical_name AND a.name<>COALESCE(p.canonical_name_ja,'')
		ORDER BY a.alias_type,a.name LIMIT 50`, id)
	if err != nil {
		return x, err
	}
	if x.Aliases, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (PublicAlias, error) {
		var a PublicAlias
		err := row.Scan(&a.Name, &a.Language, &a.Type)
		return a, err
	}); err != nil {
		return x, err
	}
	rows, err = s.Query(ctx, `SELECT lang,reviewed_value FROM translations
		WHERE entity_type='performer' AND entity_id=$1 AND field='name' AND reviewed_value IS NOT NULL AND btrim(reviewed_value)<>''
		ORDER BY lang`, strconv.FormatInt(id, 10))
	if err != nil {
		return x, err
	}
	if x.Names, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (LocalizedName, error) {
		var n LocalizedName
		err := row.Scan(&n.Lang, &n.Name)
		return n, err
	}); err != nil {
		return x, err
	}
	st := &x.Stats
	err = s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		  WHERE rp.performer_id=$1 AND r.status='published' AND r.merged_into_id IS NULL),
		(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		  WHERE rp.performer_id=$1 AND r.status='published' AND r.merged_into_id IS NULL AND `+LeadCreditSQL("rp")+`),
		(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		  WHERE rp.performer_id=$1 AND `+UpcomingReleaseSQL+`),
		(SELECT count(*) FROM follows WHERE performer_id=$1),
		COALESCE(ps.total_plays,0),COALESCE(ps.heat_30d,0),ps.heat_rank,ps.computed_at
		FROM (SELECT 1) one LEFT JOIN performer_stats ps ON ps.performer_id=$1`, id).
		Scan(&st.WorksCount, &st.LeadCount, &st.UpcomingCount, &st.Followers, &st.TotalPlays, &st.Heat, &st.HeatRank, &st.ComputedAt)
	return x, err
}

// CastMember is a credited performer of a release with the credit's role
// (本片饰 / 主演 / 客串): Lead follows LeadCreditSQL, Role is the source's
// role text and CreditedName the name the release credits.
type CastMember struct {
	model.Performer
	Lead         bool   `json:"lead"`
	Role         string `json:"role,omitempty"`
	CreditedName string `json:"credited_name,omitempty"`
}

// ReleaseCast is the public cast of a published or upcoming release, leads
// first; an unknown or non-public release is pgx.ErrNoRows.
func (s *Store) ReleaseCast(ctx context.Context, slug string) ([]CastMember, error) {
	var releaseID int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status IN ('published','upcoming') AND merged_into_id IS NULL`, slug).Scan(&releaseID); err != nil {
		return nil, err
	}
	rows, err := s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,''),`+PublicBirthDate("p")+`,COALESCE(p.height_cm,0),COALESCE(p.bust_cm,0),COALESCE(p.waist_cm,0),COALESCE(p.hip_cm,0),p.status,
		`+LeadCreditSQL("rp")+`,COALESCE(rp.role,''),COALESCE(rp.credited_name,'')
		FROM performers p JOIN release_performers rp ON rp.performer_id=p.id
		WHERE rp.release_id=$1 AND p.status='active' AND p.merged_into_id IS NULL ORDER BY rp.billing_order NULLS LAST,p.id`, releaseID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (CastMember, error) {
		var x CastMember
		err := row.Scan(&x.ID, &x.Slug, &x.CanonicalName, &x.CanonicalNameJA, &x.BirthDate, &x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.Status, &x.Lead, &x.Role, &x.CreditedName)
		return x, err
	})
}

// ---- playback-source facets ----

// FacetCount is one facet value with the number of published releases.
type FacetCount struct {
	Value string `json:"value"`
	Count int64  `json:"count"`
}

// CatalogFacets says which playback-source facets have data (清晰度 / 字幕 /
// 语言): the UI offers a facet only when it would match something.
type CatalogFacets struct {
	AudioLangs    []FacetCount `json:"audio_langs"`
	SubtitleLangs []FacetCount `json:"subtitle_langs"`
	// Heights counts releases with a source of at least 720 / 1080 / 2160 lines.
	Heights []FacetCount `json:"heights"`
	// Subtitled counts releases with any subtitle.
	Subtitled int64 `json:"subtitled"`
}

// MinHeights are the 清晰度 buckets (ReleaseFilter.MinHeight values).
var MinHeights = []int{720, 1080, 2160}

// CatalogFacets aggregates the enabled playback sources of published
// releases. It scans playback_sources, so the API caches it.
func (s *Store) CatalogFacets(ctx context.Context) (CatalogFacets, error) {
	f := CatalogFacets{AudioLangs: []FacetCount{}, SubtitleLangs: []FacetCount{}, Heights: []FacetCount{}}
	const pub = `FROM playback_sources pbs JOIN releases r ON r.id=pbs.release_id
		WHERE pbs.status<>'disabled' AND r.status='published' AND r.merged_into_id IS NULL`
	for _, col := range []string{"audio_langs", "subtitle_langs"} {
		rows, err := s.Query(ctx, `SELECT l,count(DISTINCT pbs.release_id) FROM playback_sources pbs
			JOIN releases r ON r.id=pbs.release_id CROSS JOIN LATERAL unnest(pbs.`+col+`) AS l
			WHERE pbs.status<>'disabled' AND r.status='published' AND r.merged_into_id IS NULL AND btrim(l)<>''
			GROUP BY l ORDER BY 2 DESC,1 LIMIT 20`)
		if err != nil {
			return f, err
		}
		vals, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (FacetCount, error) {
			var c FacetCount
			err := row.Scan(&c.Value, &c.Count)
			return c, err
		})
		if err != nil {
			return f, err
		}
		if col == "audio_langs" {
			f.AudioLangs = vals
		} else {
			f.SubtitleLangs = vals
		}
	}
	var h720, h1080, h2160 int64
	if err := s.QueryRow(ctx, `SELECT count(DISTINCT pbs.release_id) FILTER (WHERE pbs.max_height>=720),
		count(DISTINCT pbs.release_id) FILTER (WHERE pbs.max_height>=1080),
		count(DISTINCT pbs.release_id) FILTER (WHERE pbs.max_height>=2160),
		count(DISTINCT pbs.release_id) FILTER (WHERE cardinality(pbs.subtitle_langs)>0) `+pub).
		Scan(&h720, &h1080, &h2160, &f.Subtitled); err != nil {
		return f, err
	}
	for i, n := range []int64{h720, h1080, h2160} {
		if n > 0 {
			f.Heights = append(f.Heights, FacetCount{Value: strconv.Itoa(MinHeights[i]), Count: n})
		}
	}
	return f, nil
}

// ---- signed-in search history (最近搜索) ----

// SearchHistoryKeep is how many recent searches are kept per user.
const SearchHistoryKeep = 20

// RecordSearch stores a search as the user's newest and trims the history
// to SearchHistoryKeep entries. q is the normalized form (search.NormalizeQuery),
// the key that dedupes case- and width-insensitively; display is the form as
// typed (search.DisplayQuery), shown back and updated to the latest spelling.
func (s *Store) RecordSearch(ctx context.Context, userID int64, q, display string) error {
	if display == "" {
		display = q
	}
	return pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO user_search_history(user_id,query,display) VALUES($1,$2,$3)
			ON CONFLICT (user_id,query) DO UPDATE SET display=EXCLUDED.display, searched_at=now()`, userID, q, display); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM user_search_history WHERE user_id=$1 AND query NOT IN (
			SELECT query FROM user_search_history WHERE user_id=$1 ORDER BY searched_at DESC LIMIT $2)`, userID, SearchHistoryKeep)
		return err
	})
}

// SearchHistory returns the user's recent searches as typed, newest first
// (rows from before migration 049 have only the normalized form).
func (s *Store) SearchHistory(ctx context.Context, userID int64, limit int) ([]string, error) {
	if limit <= 0 || limit > SearchHistoryKeep {
		limit = SearchHistoryKeep
	}
	rows, err := s.Query(ctx, `SELECT COALESCE(display,query) FROM user_search_history WHERE user_id=$1 ORDER BY searched_at DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClearSearchHistory deletes the user's recent searches (最近搜索 · 清除).
func (s *Store) ClearSearchHistory(ctx context.Context, userID int64) (int64, error) {
	tag, err := s.Exec(ctx, `DELETE FROM user_search_history WHERE user_id=$1`, userID)
	return tag.RowsAffected(), err
}
