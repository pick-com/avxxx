package store

import (
	"avxxx/internal/sitemetrics"
	"context"
	"testing"
	"time"
)

// TestSiteMetricsIntegration covers migration 047's site_daily_metrics and the
// site_metrics job: plays, DAU (users / devices / sessions), watch time from
// play_end or heartbeats, completion, the first-run backfill, idempotent
// re-runs, and that days older than the event retention keep their rows.
func TestSiteMetricsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "sitemetrics")
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
	// Migration 047 re-runs cleanly on every start.
	s.Close()
	if s, err = Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}

	now := time.Now().UTC()
	today := sitemetrics.UTCDate(now)
	yday := today.AddDate(0, 0, -1)
	old := today.AddDate(0, 0, -120) // before the event retention
	exec(`SELECT events_ensure_partition($1::date)`, yday)
	exec(`SELECT events_ensure_partition($1::date)`, today.AddDate(0, 0, -2))

	var u1, u2 int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('sm1@t','x') RETURNING id`).Scan(&u1))
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('sm2@t','x') RETURNING id`).Scan(&u2))

	seq := 0
	ev := func(name string, uid int64, device, session, play string, at time.Time, props string) {
		t.Helper()
		seq++
		var uidArg, devArg, playArg any
		if uid > 0 {
			uidArg = uid
		}
		if device != "" {
			devArg = device
		}
		if play != "" {
			playArg = play
		}
		exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,user_id,device_id,session_id,play_id,platform,app_ver,props)
			VALUES('sm-'||$1::int,$2,1,$3,$3,$4,$5,$6,$7,'web','1',$8::jsonb)`, seq, name, at, uidArg, devArg, session, playArg, props)
	}
	at := yday.Add(10 * time.Hour)
	// u1: two plays — one completed with watched_seconds, one by heartbeats only.
	ev("play_request", u1, "d1", "s1", "p1", at, `{}`)
	ev("play_end", u1, "d1", "s1", "p1", at.Add(40*time.Minute), `{"watched_seconds":"1800","reason":"completed"}`)
	ev("play_request", u1, "d1", "s1", "p2", at.Add(time.Hour), `{}`)
	ev("heartbeat", u1, "d1", "s1", "p2", at.Add(time.Hour+5*time.Minute), `{}`)
	ev("heartbeat", u1, "d1", "s1", "p2", at.Add(time.Hour+10*time.Minute), `{}`)
	// Duplicate play_request of p2 (resend) counts once.
	ev("play_request", u1, "d1", "s1", "p2", at.Add(time.Hour+time.Second), `{}`)
	// u2: a play finished by the 100 % milestone, watch time capped at 6 h.
	ev("play_request", u2, "d2", "s2", "p3", at.Add(2*time.Hour), `{}`)
	ev("progress", u2, "d2", "s2", "p3", at.Add(3*time.Hour), `{"milestone":"100"}`)
	ev("play_end", u2, "d2", "s2", "p3", at.Add(3*time.Hour), `{"watched_seconds":"999999"}`)
	// A consented anonymous device and two sessions without consent: 3 visitors.
	ev("page_view", 0, "d3", "s3", "", at, `{}`)
	ev("page_view", 0, "", "s4", "", at, `{}`)
	ev("page_view", 0, "", "s5", "", at, `{}`)
	// Today: one play so far.
	ev("play_request", 0, "", "s6", "p4", today.Add(time.Minute), `{}`)

	// A stored row from before the retention must survive every run.
	exec(`INSERT INTO site_daily_metrics(day,plays,dau,watch_seconds,completed_plays) VALUES($1,7,5,100,3)`, old)

	rn := &sitemetrics.Runner{Store: s, Now: func() time.Time { return now }}
	rep, err := rn.Run(ctx)
	must(err)
	if rep.Backfill || rep.Days != 2 || rep.Plays != 4 {
		t.Fatalf("run with a stored row: %+v", rep)
	}
	rows, err := s.SiteDailyMetrics(ctx, old, today)
	must(err)
	by := map[string]sitemetrics.Day{}
	for _, d := range rows {
		by[d.Day.Format(time.DateOnly)] = d
	}
	y := by[yday.Format(time.DateOnly)]
	if y.Plays != 3 || y.DAU != 5 || y.SignedIn != 2 || y.CompletedPlays != 2 {
		t.Errorf("yesterday: %+v", y)
	}
	if want := int64(1800 + 600 + sitemetrics.MaxPlaySeconds); y.WatchSeconds != want {
		t.Errorf("watch seconds %d, want %d", y.WatchSeconds, want)
	}
	if d := by[today.Format(time.DateOnly)]; d.Plays != 1 || d.DAU != 1 || d.WatchSeconds != 0 {
		t.Errorf("today: %+v", d)
	}
	if d := by[old.Format(time.DateOnly)]; d.Plays != 7 || d.DAU != 5 {
		t.Errorf("old row changed: %+v", d)
	}

	// Re-running is idempotent; an explicit backfill never reaches before the
	// retention and writes zero rows for days without events.
	_, err = rn.Run(ctx)
	must(err)
	back := &sitemetrics.Runner{Store: s, Now: func() time.Time { return now }, Days: append(sitemetrics.BackfillDays(now, 5), old)}
	rep, err = back.Run(ctx)
	must(err)
	if rep.Days != 5 || rep.Plays != 4 {
		t.Fatalf("backfill: %+v", rep)
	}
	rows, err = s.SiteDailyMetrics(ctx, old, today)
	must(err)
	if len(rows) != 6 {
		t.Fatalf("rows after backfill: %d", len(rows))
	}
	for _, d := range rows {
		if d.Day.Equal(old) && d.Plays != 7 {
			t.Errorf("old row rewritten: %+v", d)
		}
		if d.Day.Equal(yday) && d.Plays != 3 {
			t.Errorf("yesterday changed on re-run: %+v", d)
		}
	}

	// First run on an empty table backfills the whole retention window.
	exec(`DELETE FROM site_daily_metrics`)
	rep, err = rn.Run(ctx)
	must(err)
	if !rep.Backfill || rep.Days != sitemetrics.RetentionDays {
		t.Fatalf("empty-table run: %+v", rep)
	}
	var runs int
	must(s.QueryRow(ctx, `SELECT count(*) FROM nightly_runs WHERE job='site_metrics' AND result='ok'`).Scan(&runs))
	if runs != 4 {
		t.Errorf("nightly_runs rows: %d", runs)
	}
}
