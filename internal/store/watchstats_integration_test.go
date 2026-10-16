package store

import (
	"avxxx/internal/watchstats"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// TestWatchStatsIntegration covers migration 043 (idempotent), the
// watch_stats job (events + watch_history, time zones, history pause,
// pending deletion, deleted history entries, idempotent re-runs), the
// owner summary, clearing with the watch history, the data export and the
// cascade on account deletion, against an isolated database.
func TestWatchStatsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "watchstats")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}

	// Migration 043 re-runs cleanly on every start.
	s.Close()
	if s, err = Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}

	user := func(email, tz string, paused bool) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, email).Scan(&id))
		if tz != "" || paused {
			exec(`INSERT INTO user_settings(user_id,timezone,history_paused) VALUES($1,COALESCE(NULLIF($2,''),'UTC'),$3)`, id, tz, paused)
		}
		return id
	}
	alice := user("alice@t", "", false) // no settings row: UTC
	tokyo := user("tokyo@t", "Asia/Tokyo", false)
	paused := user("paused@t", "", true)
	leaving := user("leaving@t", "", false)
	exec(`UPDATE users SET deletion_requested_at=now() WHERE id=$1`, leaving)

	var perf int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,status) VALUES('ws-p','Perf','active') RETURNING id`).Scan(&perf))
	release := func(slug string, runtime int) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,$1,'published',$2) RETURNING id`, slug, runtime).Scan(&id))
		exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, id, perf)
		exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,'Drama','drama')`, id)
		return id
	}
	r1, r2, r3 := release("ws-r1", 100), release("ws-r2", 30), release("ws-r3", 60)

	// Yesterday (UTC) at 23:00: a 2 h play crosses midnight in UTC and lands
	// entirely on "today" in Tokyo (08:00–10:00).
	now := time.Now().UTC()
	yday := watchstats.UTCDate(now).AddDate(0, 0, -1)
	start := yday.Add(23 * time.Hour)
	if start.After(now.Add(-2 * time.Hour)) {
		t.Skip("too close to midnight UTC for a stable fixture")
	}
	exec(`SELECT events_ensure_partition($1::date)`, start)
	exec(`SELECT events_ensure_partition($1::date)`, now)
	evSeq := 0
	ev := func(name string, uid int64, play string, rel int64, at time.Time, props string) {
		t.Helper()
		evSeq++
		var relArg any
		if rel > 0 {
			relArg = rel
		}
		exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,user_id,session_id,play_id,platform,app_ver,release_id,props)
			VALUES('ws-'||$1::int,$2,1,$3,$3,$4,'s',$5,'web','1',$6,$7::jsonb)`, evSeq, name, at, uid, play, relArg, props)
	}
	for _, u := range []int64{alice, tokyo, paused, leaving} {
		play := fmt.Sprintf("p-%d", u)
		ev("play_request", u, play, r1, start, `{"entry":"home_row"}`)
		ev("play_end", u, play, 0, start.Add(2*time.Hour), `{"reason":"completed","watched_seconds":7200}`)
		exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,5900,6000,$3)`, u, r1, start.Add(2*time.Hour))
	}
	// Alice: a play without play_end (closed tab): heartbeat 10 min in.
	ev("play_request", alice, "p-hb", r2, start.Add(-3*time.Hour), `{}`)
	ev("heartbeat", alice, "p-hb", 0, start.Add(-3*time.Hour+10*time.Minute), `{}`)
	exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,watched_at) VALUES($1,$2,600,$3)`, alice, r2, start.Add(-3*time.Hour+10*time.Minute))
	// Alice: a play whose history entry was deleted does not count.
	ev("play_request", alice, "p-del", r3, start.Add(-5*time.Hour), `{}`)
	ev("play_end", alice, "p-del", 0, start.Add(-4*time.Hour), `{"reason":"closed","watched_seconds":3600}`)

	rn := &watchstats.Runner{Store: s, Now: func() time.Time { return now }, Days: []time.Time{yday, yday.AddDate(0, 0, 1)}}
	rep, err := rn.Run(ctx)
	must(err)
	if rep.Users != 2 {
		t.Errorf("report %+v: want alice and tokyo only", rep)
	}
	type row struct {
		secs, plays, titles, finished int
		hours                         []int32
		tz                            string
	}
	get := func(uid int64, day time.Time) (row, bool) {
		var r row
		err := s.QueryRow(ctx, `SELECT watch_seconds,plays,titles,finished,by_hour,tz FROM user_watch_daily WHERE user_id=$1 AND day=$2::date`, uid, day).
			Scan(&r.secs, &r.plays, &r.titles, &r.finished, &r.hours, &r.tz)
		return r, err == nil
	}
	a1, ok := get(alice, yday)
	if !ok || a1.secs != 3600+600 || a1.plays != 2 || a1.titles != 2 || a1.finished != 1 || a1.hours[23] != 3600 || a1.hours[20] != 600 || a1.tz != "UTC" {
		t.Errorf("alice yesterday %+v %v", a1, ok)
	}
	a2, ok := get(alice, yday.AddDate(0, 0, 1))
	if !ok || a2.secs != 3600 || a2.plays != 0 || a2.hours[0] != 3600 {
		t.Errorf("alice today %+v %v", a2, ok)
	}
	if _, ok := get(tokyo, yday); ok {
		t.Error("tokyo has a row on the UTC date before")
	}
	if tk, ok := get(tokyo, yday.AddDate(0, 0, 1)); !ok || tk.secs != 7200 || tk.hours[8] != 3600 || tk.hours[9] != 3600 || tk.tz != "Asia/Tokyo" || tk.finished != 1 {
		t.Errorf("tokyo %+v %v", tk, ok)
	}
	if n := count(`SELECT count(*) FROM user_watch_daily WHERE user_id = ANY($1)`, []int64{paused, leaving}); n != 0 {
		t.Errorf("paused / pending deletion rows: %d", n)
	}
	var perfJSON, tagJSON string
	must(s.QueryRow(ctx, `SELECT top_performers::text, top_tags::text FROM user_watch_daily WHERE user_id=$1 AND day=$2::date`, alice, yday).Scan(&perfJSON, &tagJSON))
	var perfs []watchstats.PerformerSeconds
	must(json.Unmarshal([]byte(perfJSON), &perfs))
	if len(perfs) != 1 || perfs[0].ID != perf || perfs[0].Seconds != 4200 {
		t.Errorf("top performers %s", perfJSON)
	}

	// Idempotent: a second run leaves the same rows.
	before := count(`SELECT count(*) FROM user_watch_daily`)
	var sumBefore int
	must(s.QueryRow(ctx, `SELECT COALESCE(sum(watch_seconds),0) FROM user_watch_daily`).Scan(&sumBefore))
	_, err = rn.Run(ctx)
	must(err)
	var sumAfter int
	must(s.QueryRow(ctx, `SELECT COALESCE(sum(watch_seconds),0) FROM user_watch_daily`).Scan(&sumAfter))
	if after := count(`SELECT count(*) FROM user_watch_daily`); after != before || sumAfter != sumBefore {
		t.Errorf("rerun changed rows: %d→%d, %d→%d", before, after, sumBefore, sumAfter)
	}
	if n := count(`SELECT count(*) FROM nightly_runs WHERE job=$1`, watchstats.JobKind); n != 2 {
		t.Errorf("nightly_runs: %d", n)
	}

	// Owner summary (in the user's time zone).
	sum, err := s.WatchStatsFor(ctx, alice, "7d", now)
	must(err)
	if !sum.HasData || sum.Totals.Seconds != 7800 || sum.Totals.Finished != 1 || sum.Totals.Titles != 2 || sum.TZ != "UTC" || len(sum.Daily) != 7 {
		t.Errorf("summary %+v", sum.Summary)
	}
	if sum.Streak.Current != 2 || sum.Streak.Longest != 2 {
		t.Errorf("streak %+v", sum.Streak)
	}
	if len(sum.Performers) != 1 || sum.Performers[0].Slug != "ws-p" || len(sum.Genres) != 1 || sum.Genres[0].Name != "Drama" {
		t.Errorf("tops %+v %+v", sum.Performers, sum.Genres)
	}
	if sum.UpdatedAt == nil {
		t.Error("updated_at missing")
	}
	empty, err := s.WatchStatsFor(ctx, paused, "30d", now)
	must(err)
	if empty.HasData || empty.Totals.Seconds != 0 || len(empty.Performers) != 0 || empty.UpdatedAt != nil {
		t.Errorf("empty summary %+v", empty)
	}

	// Export carries the rows.
	var buf bytes.Buffer
	must(s.WriteDataExport(ctx, alice, &buf))
	var exp map[string]json.RawMessage
	must(json.Unmarshal(buf.Bytes(), &exp))
	var daily []map[string]any
	must(json.Unmarshal(exp["user_watch_daily"], &daily))
	if len(daily) != 2 || daily[0]["watch_seconds"].(float64) != 4200 {
		t.Errorf("export user_watch_daily %s", exp["user_watch_daily"])
	}

	// Clearing the history clears the statistics, and a rebuild does not
	// bring the plays back.
	_, err = s.ClearWatchHistory(ctx, alice)
	must(err)
	if n := count(`SELECT count(*) FROM user_watch_daily WHERE user_id=$1`, alice); n != 0 {
		t.Errorf("rows after clear: %d", n)
	}
	_, err = rn.Run(ctx)
	must(err)
	if n := count(`SELECT count(*) FROM user_watch_daily WHERE user_id=$1`, alice); n != 0 {
		t.Errorf("rows after clear + rebuild: %d", n)
	}

	// Rows go with the account.
	exec(`DELETE FROM users WHERE id=$1`, tokyo)
	if n := count(`SELECT count(*) FROM user_watch_daily WHERE user_id=$1`, tokyo); n != 0 {
		t.Errorf("rows after account delete: %d", n)
	}
}
