package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestDiscoverIntegration covers migration 039 and the discover / search /
// performer reads: performer_stats rebuilt by RefreshPerformerStats and the
// NavActors sorts and filters on it, lead / guest credits, playback-source
// facets, the 即将上线 list (followed, performer, paging), upcoming release
// pages and cast, performer extras, typed search counts, trending genre
// chips and signed-in search history. Runs in its own database (it asserts
// on global rows). Skipped unless AVXXX_TEST_DATABASE_URL is set.
func TestDiscoverIntegration(t *testing.T) {
	openIsolatedTestDB(t, "discover")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // 039 must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	id := func(sql string, args ...any) (v int64) {
		t.Helper()
		if err := s.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	performer := func(slug string) int64 {
		return id(`INSERT INTO performers(slug,canonical_name,canonical_name_ja) VALUES($1,$2,$3) RETURNING id`, slug, "Name "+slug, "名"+slug)
	}
	release := func(slug, status string, date *time.Time, sched *time.Time) int64 {
		return id(`INSERT INTO releases(slug,canonical_title,status,release_date,scheduled_at) VALUES($1,$2,$3,$4,$5) RETURNING id`,
			slug, "星海 "+slug, status, date, sched)
	}
	day := func(y, m, d int) *time.Time { t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC); return &t }
	today := time.Now().UTC().Truncate(24 * time.Hour)
	in := func(days int) *time.Time { t := today.AddDate(0, 0, days); return &t }

	a, b, c := performer("aa"), performer("bb"), performer("cc")
	r1 := release("r1", "published", day(2015, 3, 1), nil)
	r2 := release("r2", "published", day(2022, 6, 1), nil)
	r3 := release("r3", "published", in(10), nil)      // published, future-dated
	r4 := release("r4", "upcoming", nil, in(3))        // upcoming, scheduled
	r5 := release("r5", "upcoming", nil, nil)          // upcoming, 日期待定
	r6 := release("r6", "draft", day(2020, 1, 1), nil) // never public
	r7 := release("r7", "published", day(2023, 1, 1), nil)
	credit := func(rel, p int64, primary bool, role string, order int) {
		exec(`INSERT INTO release_performers(release_id,performer_id,is_primary,role,billing_order) VALUES($1,$2,$3,NULLIF($4,''),$5)`, rel, p, primary, role, order)
	}
	credit(r1, a, true, "", 1)
	credit(r1, b, false, "", 2)
	credit(r2, b, true, "", 1)
	credit(r2, a, false, "guest", 2)
	credit(r3, a, false, "", 1) // no lead marked: everyone is a lead
	credit(r4, a, false, "", 1)
	credit(r5, b, false, "", 1)
	credit(r6, a, true, "", 1)
	credit(r7, b, true, "", 1)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,'剧情','drama'),($2,'剧情','drama'),($2,'偶像','idol'),($3,'偶像','idol'),($3,'单体','solo')`, r1, r2, r7)
	exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url,max_height,audio_langs,subtitle_langs) VALUES
		($1,'external_link','p','main','https://e/1',1080,'{ja}','{zh-CN}'),
		($2,'external_link','p','main','https://e/2',720,'{ja}','{}'),
		($3,'external_link','p','main','https://e/7',2160,'{en}','{}')`, r1, r2, r7)
	exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url,max_height,audio_langs,status) VALUES($1,'external_link','p','x','https://e/1b',2160,'{ko}','disabled')`, r1)
	u1 := id(`INSERT INTO users(email,password_hash) VALUES('u1@example.test','x') RETURNING id`)
	u2 := id(`INSERT INTO users(email,password_hash) VALUES('u2@example.test','x') RETURNING id`)
	exec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$2),($3,$2)`, u1, a, u2)
	exec(`INSERT INTO reservations(user_id,release_id) VALUES($1,$2)`, u1, r4)
	exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,watched_at) VALUES($1,$2,60,now()-interval '2 days'),($3,$2,60,now()-interval '1 day')`, u1, r1, u2)
	exec(`INSERT INTO performer_heat_snapshots(period,rank,performer_id,score) VALUES('month',1,$1,1.5)`, a)
	exec(`INSERT INTO performer_aliases(performer_id,name,normalized_name,language,alias_type) VALUES
		($1,'Alias AA','alias aa','en','stage'),($1,'Old AA','old aa',NULL,'merged')`, a)
	exec(`INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,reviewed_value) VALUES
		('performer',$1::text,'name','en','Ai A','reviewed','h','Ai A'),('performer',$1::text,'name','ko','에이','draft','h',NULL)`, fmt.Sprint(a))

	// ---- performer_stats
	at := time.Now().UTC().Truncate(time.Microsecond)
	n, err := s.RefreshPerformerStats(ctx, at)
	if err != nil || n != 3 {
		t.Fatalf("RefreshPerformerStats = %d, %v", n, err)
	}
	type statRow struct {
		works, leads, upcoming, followers, heat int
		plays                                   int64
		rank                                    *int
		first, latest                           *time.Time
		tags, langs                             []string
	}
	stat := func(p int64) statRow {
		t.Helper()
		var x statRow
		if err := s.QueryRow(ctx, `SELECT works_count,lead_count,upcoming_count,followers,heat_30d,total_plays,heat_rank,first_work_date,latest_work_date,top_tags,audio_langs
			FROM performer_stats WHERE performer_id=$1`, p).Scan(&x.works, &x.leads, &x.upcoming, &x.followers, &x.heat, &x.plays, &x.rank, &x.first, &x.latest, &x.tags, &x.langs); err != nil {
			t.Fatalf("stats %d: %v", p, err)
		}
		return x
	}
	sa := stat(a)
	if sa.works != 3 || sa.leads != 2 || sa.upcoming != 2 || sa.followers != 2 || sa.heat != 1500 || sa.rank == nil || *sa.rank != 1 || sa.plays != 2 {
		t.Errorf("stats a = %+v", sa)
	}
	if !slices.Equal(sa.tags, []string{"drama", "idol"}) || !slices.Equal(sa.langs, []string{"ja"}) || !sa.first.Equal(*day(2015, 3, 1)) {
		t.Errorf("stats a tags %v langs %v first %v", sa.tags, sa.langs, sa.first)
	}
	sb := stat(b)
	if sb.works != 3 || sb.leads != 2 || sb.upcoming != 1 || sb.heat != 0 || sb.rank != nil || !slices.Equal(sb.tags, []string{"drama", "idol", "solo"}) || !slices.Equal(sb.langs, []string{"en", "ja"}) {
		t.Errorf("stats b = %+v", sb)
	}
	if sc := stat(c); sc.works != 0 || sc.first != nil {
		t.Errorf("stats c = %+v", sc)
	}
	// A performer that stops qualifying loses its row on the next run.
	exec(`UPDATE performers SET status='hidden' WHERE id=$1`, c)
	if n, err = s.RefreshPerformerStats(ctx, at.Add(time.Hour)); err != nil || n != 2 {
		t.Fatalf("second RefreshPerformerStats = %d, %v", n, err)
	}
	if v := id(`SELECT count(*) FROM performer_stats`); v != 2 {
		t.Errorf("performer_stats rows = %d, want 2", v)
	}

	// ---- NavActors sorts and filters
	slugs := func(f PerformerFilter) []string {
		t.Helper()
		ps, _, err := s.PerformerPage(ctx, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		var out []string
		for _, p := range ps {
			out = append(out, p.Slug)
		}
		return out
	}
	if got := slugs(PerformerFilter{Sort: "heat"}); !slices.Equal(got, []string{"aa", "bb"}) {
		t.Errorf("heat = %v", got)
	}
	u3 := id(`INSERT INTO users(email,password_hash) VALUES('u3@example.test','x') RETURNING id`)
	exec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$4),($2,$4),($3,$4)`, u1, u2, u3, b)
	if _, err = s.RefreshPerformerStats(ctx, at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := slugs(PerformerFilter{Sort: "followers"}); !slices.Equal(got, []string{"bb", "aa"}) {
		t.Errorf("followers = %v", got)
	}
	if got := slugs(PerformerFilter{Sort: "rising"}); !slices.Equal(got, []string{"bb", "aa"}) && !slices.Equal(got, []string{"aa", "bb"}) {
		t.Errorf("rising = %v", got)
	}
	if got := slugs(PerformerFilter{Tag: "solo"}); !slices.Equal(got, []string{"bb"}) {
		t.Errorf("tag solo = %v", got)
	}
	if got := slugs(PerformerFilter{AudioLang: "en"}); !slices.Equal(got, []string{"bb"}) {
		t.Errorf("audio en = %v", got)
	}
	if got := slugs(PerformerFilter{Era: 2010, Sort: "works"}); len(got) != 2 {
		t.Errorf("era 2010 = %v", got)
	}
	if got := slugs(PerformerFilter{Era: 1990}); len(got) != 0 {
		t.Errorf("era 1990 = %v", got)
	}
	// Keyset paging over a stats sort.
	p1, next, err := s.PerformerPage(ctx, PerformerFilter{Sort: "heat", Limit: 1})
	if err != nil || len(p1) != 1 || next == "" {
		t.Fatalf("heat page 1 = %v %q %v", p1, next, err)
	}
	cur, _ := DecodeCursor(next)
	p2, _, err := s.PerformerPage(ctx, PerformerFilter{Sort: "heat", Limit: 1, Cursor: &cur})
	if err != nil || len(p2) != 1 || p2[0].Slug != "bb" || p1[0].Heat != 1500 || p1[0].Followers != 2 {
		t.Errorf("heat page 2 = %+v (page 1 %+v), %v", p2, p1, err)
	}

	// ---- releases: lead / guest credits and playback facets
	rels := func(f ReleaseFilter) []string {
		t.Helper()
		rs, _, err := s.ReleasePage(ctx, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		var out []string
		for _, r := range rs {
			out = append(out, r.Slug)
		}
		slices.Sort(out)
		return out
	}
	if got := rels(ReleaseFilter{Performer: "aa", Role: CreditLead}); !slices.Equal(got, []string{"r1", "r3"}) {
		t.Errorf("aa lead = %v", got)
	}
	if got := rels(ReleaseFilter{Performer: "aa", Role: CreditGuest}); !slices.Equal(got, []string{"r2"}) {
		t.Errorf("aa guest = %v", got)
	}
	if got := rels(ReleaseFilter{MinHeight: 1080}); !slices.Equal(got, []string{"r1", "r7"}) {
		t.Errorf("min_height 1080 = %v", got)
	}
	if got := rels(ReleaseFilter{MinHeight: 2160}); !slices.Equal(got, []string{"r7"}) { // r1's 2160 source is disabled
		t.Errorf("min_height 2160 = %v", got)
	}
	if got := rels(ReleaseFilter{Subtitle: "any"}); !slices.Equal(got, []string{"r1"}) {
		t.Errorf("subtitle any = %v", got)
	}
	if got := rels(ReleaseFilter{Subtitle: "en"}); len(got) != 0 {
		t.Errorf("subtitle en = %v", got)
	}
	if got := rels(ReleaseFilter{AudioLang: "en"}); !slices.Equal(got, []string{"r7"}) {
		t.Errorf("audio en = %v", got)
	}
	f, err := s.CatalogFacets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Heights) != 3 || f.Heights[0] != (FacetCount{"720", 3}) || f.Heights[2] != (FacetCount{"2160", 1}) || f.Subtitled != 1 ||
		len(f.AudioLangs) != 2 || f.AudioLangs[0] != (FacetCount{"ja", 2}) || len(f.SubtitleLangs) != 1 {
		t.Errorf("facets = %+v", f)
	}

	// ---- 即将上线
	upcoming := func(f UpcomingFilter) ([]string, []UpcomingRelease) {
		t.Helper()
		us, _, err := s.UpcomingPage(ctx, f)
		if err != nil {
			t.Fatalf("%+v: %v", f, err)
		}
		var out []string
		for _, u := range us {
			out = append(out, u.Slug)
		}
		return out, us
	}
	got, us := upcoming(UpcomingFilter{})
	if !slices.Equal(got, []string{"r4", "r3", "r5"}) {
		t.Fatalf("upcoming = %v", got)
	}
	if us[0].Date == nil || *us[0].Date != in(3).Format(time.DateOnly) || !us[0].Reservable || us[1].Reservable || us[2].Date != nil ||
		len(us[0].Performers) != 1 || us[0].Performers[0].Slug != "aa" {
		t.Errorf("upcoming rows = %+v", us)
	}
	if got, _ := upcoming(UpcomingFilter{FollowedBy: u2}); !slices.Equal(got, []string{"r4", "r3", "r5"}) {
		t.Errorf("followed u2 = %v", got)
	}
	exec(`DELETE FROM follows WHERE user_id=$1 AND performer_id=$2`, u2, b)
	if got, _ := upcoming(UpcomingFilter{FollowedBy: u2}); !slices.Equal(got, []string{"r4", "r3"}) {
		t.Errorf("followed u2 (aa only) = %v", got)
	}
	if got, _ := upcoming(UpcomingFilter{Performer: "bb"}); !slices.Equal(got, []string{"r5"}) {
		t.Errorf("performer bb = %v", got)
	}
	_, next, err = s.UpcomingPage(ctx, UpcomingFilter{Limit: 2})
	if err != nil || next == "" {
		t.Fatalf("upcoming page 1: %q %v", next, err)
	}
	cur, _ = DecodeCursor(next)
	if got, _ := upcoming(UpcomingFilter{Limit: 2, Cursor: &cur}); !slices.Equal(got, []string{"r5"}) {
		t.Errorf("upcoming page 2 = %v", got)
	}
	reserved, err := s.ReservedAmong(ctx, u1, []int64{r3, r4})
	if err != nil || !reserved[r4] || reserved[r3] {
		t.Errorf("reserved = %v, %v", reserved, err)
	}

	// ---- upcoming release page and cast
	d, err := s.ReleaseDetailBySlug(ctx, "r4")
	if err != nil || d.Status != "upcoming" || d.ScheduledAt == nil {
		t.Errorf("upcoming detail = %+v, %v", d, err)
	}
	if _, err = s.ReleaseDetailBySlug(ctx, "r6"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("draft detail err = %v", err)
	}
	cast, err := s.ReleaseCast(ctx, "r2")
	if err != nil || len(cast) != 2 || cast[0].Slug != "bb" || !cast[0].Lead || cast[1].Lead || cast[1].Role != "guest" {
		t.Errorf("cast r2 = %+v, %v", cast, err)
	}
	if cast, err = s.ReleaseCast(ctx, "r4"); err != nil || len(cast) != 1 || !cast[0].Lead {
		t.Errorf("cast r4 = %+v, %v", cast, err)
	}
	if _, err = s.ReleaseCast(ctx, "r6"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("draft cast err = %v", err)
	}

	// ---- performer extras
	x, err := s.PerformerExtrasByID(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Aliases) != 1 || x.Aliases[0].Name != "Alias AA" || len(x.Names) != 1 || x.Names[0] != (LocalizedName{"en", "Ai A"}) {
		t.Errorf("extras names = %+v", x)
	}
	if st := x.Stats; st.WorksCount != 3 || st.LeadCount != 2 || st.UpcomingCount != 2 || st.Followers != 2 || st.Heat != 1500 || st.TotalPlays != 2 || st.ComputedAt == nil {
		t.Errorf("extras stats = %+v", st)
	}

	// ---- typed search and counts
	res, err := s.SearchWith(ctx, "星海", 60, SearchOptions{Counts: true})
	if err != nil || res.Counts[SearchTypeRelease] != 4 || res.Counts[SearchTypePerformer] != 0 {
		t.Errorf("search counts = %v, %v", res.Counts, err)
	}
	res, err = s.SearchWith(ctx, "name", 60, SearchOptions{Type: SearchTypePerformer, Counts: true})
	if err != nil || len(res.Items) != 2 || res.Counts[SearchTypePerformer] != 2 {
		t.Errorf("performer search = %v %v, %v", res.Items, res.Counts, err)
	}
	if res, err = s.SearchWith(ctx, "星海", 60, SearchOptions{Type: SearchTypeRelease}); err != nil || len(res.Items) != 4 || res.Counts != nil {
		t.Errorf("release search = %d items, counts %v, %v", len(res.Items), res.Counts, err)
	}
	if _, err = s.SearchWith(ctx, "x", 10, SearchOptions{Type: "tag"}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("bad type err = %v", err)
	}

	// ---- trending genre chips
	exec(`INSERT INTO trending_snapshots(period,rank,release_id,score) VALUES('week',1,$1,2),('week',2,$2,1),('week',3,$3,0.5)`, r1, r2, r7)
	board, err := s.TrendingBoard(ctx, "week", "idol", 0, 10)
	if err != nil || len(board) != 2 || board[0].ID != r2 || board[0].Rank != 2 {
		t.Errorf("idol board = %+v, %v", board, err)
	}
	if board, err = s.TrendingBoard(ctx, "week", "", 0, 10); err != nil || len(board) != 3 {
		t.Errorf("board = %+v, %v", board, err)
	}
	tags, err := s.TrendingTags(ctx, "week", 10)
	if err != nil || len(tags) != 3 || tags[0] != (TagCount{"drama", "剧情", 2}) || tags[2].Slug != "solo" {
		t.Errorf("trending tags = %+v, %v", tags, err)
	}

	// ---- search history
	for i := range 22 {
		if err := s.RecordSearch(ctx, u1, fmt.Sprintf("q%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordSearch(ctx, u1, "q05"); err != nil { // repeat moves to the top
		t.Fatal(err)
	}
	hist, err := s.SearchHistory(ctx, u1, 0)
	if err != nil || len(hist) != SearchHistoryKeep || hist[0] != "q05" || hist[1] != "q21" || slices.Contains(hist, "q01") {
		t.Errorf("history = %v, %v", hist, err)
	}
	if n, err := s.ClearSearchHistory(ctx, u1); err != nil || n != SearchHistoryKeep {
		t.Errorf("clear = %d, %v", n, err)
	}
	if hist, _ = s.SearchHistory(ctx, u1, 5); len(hist) != 0 {
		t.Errorf("history after clear = %v", hist)
	}
}
