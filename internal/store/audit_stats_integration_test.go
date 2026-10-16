package store

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"testing"
	"time"
)

// TestAuditStatsIntegration runs R-STATS (rule G 统计重建) against a fresh,
// isolated database: drifted review likes, a drifted danmaku valid count and
// a drifted search_query_daily day are repaired; a drifted trending board,
// an inconsistent pre-retention search row and an inconsistent release
// curve are reported only; raw events are never touched. Skipped unless
// AVXXX_TEST_DATABASE_URL is set.
func TestAuditStatsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "audit_stats")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	id := func(q string, args ...any) int64 {
		t.Helper()
		var n int64
		if err := s.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}

	now := time.Now().UTC()
	var users []int64
	for i := range 3 {
		users = append(users, id(`INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, fmt.Sprintf("stats%d@example.test", i)))
	}
	rel := id(`INSERT INTO releases(slug,canonical_title,status) VALUES('stats-a','Stats A','published') RETURNING id`)

	// reviews.likes: two likes stored as 5.
	rv := id(`INSERT INTO reviews(release_id,user_id,body,likes) VALUES($1,$2,'good',5) RETURNING id`, rel, users[0])
	rvOK := id(`INSERT INTO reviews(release_id,user_id,body,likes) VALUES($1,$2,'fine',1) RETURNING id`, rel, users[1])
	exec(`INSERT INTO review_likes(user_id,review_id) VALUES($1,$3),($2,$3),($1,$4)`, users[1], users[2], rv, rvOK)

	// danmaku_curves.valid_count: three valid danmaku, stored as 99.
	for i, u := range users {
		exec(`INSERT INTO danmaku(release_id,t_ms,body,user_id) VALUES($1,$2,$3,$4)`, rel, 1000*(i+1)*60, fmt.Sprintf("wow %d", i), u)
	}
	if _, err := s.RecomputeReleaseCurves(ctx, rel); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM danmaku_curve_queue`)
	exec(`UPDATE danmaku_curves SET valid_count=99 WHERE release_id=$1`, rel)

	// search_query_daily: day D (5 days ago) has 2 searches of one query, one
	// clicked; stored rows say 7 and add a query nobody searched.
	day := now.Truncate(24*time.Hour).AddDate(0, 0, -5)
	oldest := now.Truncate(24*time.Hour).AddDate(0, 0, -20).Add(6 * time.Hour)
	event := func(eid, name string, ts time.Time, relID any, playID any, props string) {
		t.Helper()
		if err := s.EnsureEventPartition(ctx, ts); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,session_id,platform,app_ver,ui_lang,release_id,play_id,props)
			VALUES($1,$2,1,$3,$3,'sess','web','1','en',$4,$5,$6::jsonb)`, eid, name, ts, relID, playID, props)
	}
	event("s-old", "search", oldest, nil, nil, `{"query":"older query","result_count":3}`)
	event("s-1", "search", day.Add(2*time.Hour), nil, nil, `{"query":"Tokyo  Hot","result_count":4,"clicked_rank":3}`)
	event("s-2", "search", day.Add(3*time.Hour), nil, nil, `{"query":"tokyo hot","result_count":4}`)
	exec(`INSERT INTO search_query_daily(day,q_norm,lang,searches) VALUES($1::date,'tokyo hot','en',7),($1::date,'never searched','en',3)`,
		day.Format(time.DateOnly))
	// Before the retention window: cannot be rebuilt, clicks > searches.
	exec(`INSERT INTO search_query_daily(day,q_norm,lang,searches,clicks) VALUES($1::date,'ancient','',1,4)`,
		now.AddDate(0, 0, -200).Format(time.DateOnly))

	// trending 'today': two plays in the window, stored as 5 (report only).
	for i := range 2 {
		event(fmt.Sprintf("p-%d", i), "play_request", now.Add(-time.Hour), rel, fmt.Sprintf("play-%d", i), `{}`)
	}
	exec(`INSERT INTO trending_snapshots(period,rank,release_id,score,plays,computed_at) VALUES('today',1,$1,1,5,$2)`, rel, now.Add(-10*time.Minute))

	// release_curves: viewers above valid_plays (report only).
	exec(`INSERT INTO release_curves(release_id,version_key,kind,data,valid_plays) VALUES($1,'main','viewers',array_fill(5,ARRAY[200]),3)`, rel)

	eventsBefore := id(`SELECT count(*) FROM events`)
	r := &audit.Runner{Store: s, Rules: []audit.Rule{audit.RuleStats}, BatchSize: 1}
	if _, err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	run, ok, err := s.LatestRun(ctx, "audit.R-STATS")
	if err != nil || !ok {
		t.Fatalf("latest run: %v %v", ok, err)
	}
	if run.Result != audit.ResultIssues {
		t.Fatalf("run = %+v", run)
	}

	if n := id(`SELECT likes FROM reviews WHERE id=$1`, rv); n != 2 {
		t.Errorf("review likes = %d, want 2", n)
	}
	if n := id(`SELECT likes FROM reviews WHERE id=$1`, rvOK); n != 1 {
		t.Errorf("untouched review likes = %d", n)
	}
	if n := id(`SELECT valid_count FROM danmaku_curves WHERE release_id=$1 AND version_key='main'`, rel); n != 3 {
		t.Errorf("danmaku valid_count = %d, want 3", n)
	}
	var searches, clicks int64
	if err := s.QueryRow(ctx, `SELECT searches,clicks FROM search_query_daily WHERE day=$1::date AND q_norm='tokyo hot' AND lang='en'`,
		day.Format(time.DateOnly)).Scan(&searches, &clicks); err != nil || searches != 2 || clicks != 1 {
		t.Errorf("search day = %d searches %d clicks (%v), want 2 / 1", searches, clicks, err)
	}
	if n := id(`SELECT count(*) FROM search_query_daily WHERE day=$1::date`, day.Format(time.DateOnly)); n != 1 {
		t.Errorf("search day rows = %d, want 1", n)
	}
	if n := id(`SELECT clicks FROM search_query_daily WHERE q_norm='ancient'`); n != 4 {
		t.Errorf("pre-retention row was changed: clicks %d", n)
	}
	if n := id(`SELECT plays FROM trending_snapshots WHERE release_id=$1`, rel); n != 5 {
		t.Errorf("trending board was written: plays %d", n)
	}
	if n := id(`SELECT count(*) FROM events`); n != eventsBefore {
		t.Errorf("events %d -> %d", eventsBefore, n)
	}

	counters, _ := run.Detail["counters"].(map[string]any)
	field := func(counter, f string) float64 {
		t.Helper()
		c, _ := counters[counter].(map[string]any)
		v, _ := c[f].(float64)
		return v
	}
	if field(audit.CounterReviewLikes, "fixed") != 1 || field(audit.CounterReviewLikes, "checked") != 2 {
		t.Errorf("likes detail = %+v", counters[audit.CounterReviewLikes])
	}
	if field(audit.CounterDanmakuValid, "fixed") != 1 {
		t.Errorf("danmaku detail = %+v", counters[audit.CounterDanmakuValid])
	}
	// tokyo hot: searches 7→2, clicks 0→1, first_page_clicks 0→1; never searched: searches 3→0.
	if field(audit.CounterSearchDaily, "fixed") != 4 || field(audit.CounterSearchDaily, "reported") != 1 {
		t.Errorf("search detail = %+v", counters[audit.CounterSearchDaily])
	}
	if field(audit.CounterTrendingPlays, "drifted") != 1 || field(audit.CounterTrendingPlays, "reported") != 1 {
		t.Errorf("trending detail = %+v", counters[audit.CounterTrendingPlays])
	}
	if field(audit.CounterReleaseCurves, "reported") != 1 {
		t.Errorf("release curves detail = %+v", counters[audit.CounterReleaseCurves])
	}
	if run.Fixed != 6 || run.Issues != 3 {
		t.Errorf("run fixed %d issues %d, want 6 / 3", run.Fixed, run.Issues)
	}

	// Second run: nothing left to repair; the report-only findings remain.
	if _, err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if run, _, _ = s.LatestRun(ctx, "audit.R-STATS"); run.Fixed != 0 || run.Issues != 3 {
		t.Errorf("second run fixed %d issues %d, want 0 / 3", run.Fixed, run.Issues)
	}
}
