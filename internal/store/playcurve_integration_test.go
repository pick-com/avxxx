package store_test

import (
	"avxxx/internal/api"
	"avxxx/internal/playcurve"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// TestReleaseCurvesIntegration seeds play events for more than 1000 plays
// (generated in SQL) and runs the PRD 14.3 curve job against a real,
// disposable PostgreSQL database: migration 021, version resolution, the
// anti-fraud filters, the stored curves, play-config's replay_curve /
// peak_curve and the admin curves endpoint. Skipped unless
// AVXXX_TEST_DATABASE_URL is set; the database is modified.
func TestReleaseCurvesIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("ADMIN_TOKEN", "curves-admin")
	ctx := context.Background()
	s, err := store.Open(ctx)
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
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec(`SELECT events_ensure_partition(((now() AT TIME ZONE 'UTC') - interval '1 day')::date)`)
	exec(`DELETE FROM release_curve_state`)

	var rel, mainSrc, altSrc int64
	slug := "curves-" + sfx
	if err = s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,'Curves','published',10) RETURNING id`, slug).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	// Main: 600 s. The second source is stored as main but runs 700 s (> 2%
	// off), so it is an alternate version with its own timeline.
	if err = s.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,duration_seconds,priority)
		VALUES($1,'external_link','p','main',$2,600,1) RETURNING id`, rel, "https://example.test/m"+sfx).Scan(&mainSrc); err != nil {
		t.Fatal(err)
	}
	if err = s.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,duration_seconds,priority)
		VALUES($1,'external_link','p','cut',$2,700,2) RETURNING id`, rel, "https://example.test/a"+sfx).Scan(&altSrc); err != nil {
		t.Fatal(err)
	}

	// ev inserts one event per row of a generate_series(1, n) as g. play,
	// device and user are SQL expressions over g; at is the ts_client/ts_server.
	ev := func(n int, name, play, device, user, at, props string, release, source any) {
		t.Helper()
		exec(fmt.Sprintf(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,user_id,device_id,session_id,play_id,
			platform,app_ver,consent,release_id,source_id,props)
			SELECT $1||'-'||$2||'-'||g, $2, 1, %[4]s, %[4]s, %[3]s, %[2]s, 's-'||(%[1]s), %[1]s, 'web', '1', '{necessary,analytics}', $3, $4, (%[5]s)::jsonb
			FROM generate_series(1,$5::int) g`, play, device, user, at, props), sfx, name, release, source, n)
	}
	base := `now() - interval '2 hours' + g * interval '1 second'`
	later := `now() - interval '100 minutes' + g * interval '1 second'`
	mainPlay := `'m-` + sfx + `-'||g`
	mainDev := `'dev-` + sfx + `-'||g`

	// 1200 ordinary main plays on distinct devices:
	//   g%4=0: replays buckets 100-104 (count 2);
	//   g%4=1: seeks 3 s → 30 s (buckets 1 → 10) and never plays 2-9;
	//   else : watches everything once.
	ev(1200, "play_request", mainPlay, mainDev, "NULL", base, `'{"entry":"home_row"}'`, rel, mainSrc)
	ev(1200, "play_end", mainPlay, mainDev, "NULL", later,
		`json_build_object('reason','completed','watched_seconds',600,'buckets',
		  CASE g%4 WHEN 0 THEN 'r1:1*100,2*5,1*95' WHEN 1 THEN 'r1:1*2,0*8,1*190' ELSE 'r1:1*200' END)`, nil, nil)
	exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,device_id,session_id,play_id,platform,app_ver,props)
		SELECT $1||'-seek-'||g,'seek',1,now()-interval '110 minutes',now()-interval '110 minutes','dev-'||$1||'-'||g,'s','m-'||$1||'-'||g,'web','1',
		  '{"from_ms":3000,"to_ms":30000,"method":"drag"}'
		FROM generate_series(1,1200) g WHERE g%4=1`, sfx)

	// A bot device: one play without interaction and 3 h of heartbeats, and
	// two interactive plays; all three are dropped.
	botPlay := `'bot-` + sfx + `-'||g`
	botDev := `'botdev-` + sfx + `'`
	ev(3, "play_request", botPlay, botDev, "NULL", base, `'{"entry":"home_row"}'`, rel, mainSrc)
	ev(3, "play_end", botPlay, botDev, "NULL", later, `'{"reason":"closed","watched_seconds":60,"buckets":"r1:1*200"}'`, nil, nil)
	ev(1, "heartbeat", botPlay, botDev, "NULL", base, `'{}'`, nil, nil)
	exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,device_id,session_id,play_id,platform,app_ver,props)
		VALUES($1||'-hb1','heartbeat',1,now()+interval '1 hour',now()-interval '1 hour','botdev-'||$1,'s','bot-'||$1||'-1','web','1','{}'),
		      ($1||'-pz2','pause',1,now()-interval '1 hour',now()-interval '1 hour','botdev-'||$1,'s','bot-'||$1||'-2','web','1','{"position_ms":1}'),
		      ($1||'-pz3','pause',1,now()-interval '1 hour',now()-interval '1 hour','botdev-'||$1,'s','bot-'||$1||'-3','web','1','{"position_ms":1}')`, sfx)

	// One user, five plays on five devices the same day: two over the cap.
	capPlay := `'cap-` + sfx + `-'||g`
	capDev := `'capdev-` + sfx + `-'||g`
	user := fmt.Sprint(900_000_000 + time.Now().UnixNano()%1_000_000)
	ev(5, "play_request", capPlay, capDev, user, base, `'{"entry":"home_row"}'`, rel, mainSrc)
	ev(5, "play_end", capPlay, capDev, user, later, `'{"reason":"closed","watched_seconds":60,"buckets":"r1:1*200"}'`, nil, nil)

	// A play that never reported a table.
	ev(1, "play_request", `'lost-`+sfx+`'`, `'lostdev-`+sfx+`'`, "NULL", base, `'{"entry":"home_row"}'`, rel, mainSrc)

	// Three plays of the alternate version.
	altPlay := `'alt-` + sfx + `-'||g`
	ev(3, "play_request", altPlay, `'altdev-`+sfx+`-'||g`, "NULL", base, `'{"entry":"home_row"}'`, rel, altSrc)
	ev(3, "play_end", altPlay, `'altdev-`+sfx+`-'||g`, "NULL", later, `'{"reason":"closed","watched_seconds":60,"buckets":"r1:2*200"}'`, nil, nil)

	// A danmaku curve for the main timeline, peaking at bucket 50.
	exec(`INSERT INTO danmaku_curves(release_id,version_key,data,bucket_ms,valid_count)
		SELECT $1,'main',array_agg(CASE WHEN i=50 THEN 255 ELSE 0 END ORDER BY i)::smallint[],3000,60 FROM generate_series(0,199) i`, rel)

	// The job is hourly and sourceless, like source_health.
	exec(`DELETE FROM jobs WHERE kind=$1`, store.JobKindReleaseCurves)
	slot := time.Now().Truncate(time.Hour)
	if ok, err := s.EnqueueKindSince(ctx, store.JobKindReleaseCurves, slot); err != nil || !ok {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	if ok, err := s.EnqueueKindSince(ctx, store.JobKindReleaseCurves, slot); err != nil || ok {
		t.Fatalf("second enqueue in the same hour: %v %v", ok, err)
	}
	if _, _, err := s.ClaimJob(ctx, store.JobKindReleaseCurves); err != nil {
		t.Fatalf("claim: %v", err)
	}

	rep, err := (&playcurve.Runner{Store: s}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Releases < 1 || rep.Curves < 1 {
		t.Fatalf("report %+v", rep)
	}
	check := func() {
		t.Helper()
		sets, err := s.ReleaseCurves(ctx, rel)
		if err != nil {
			t.Fatal(err)
		}
		if len(sets) != 2 || sets[0].VersionKey != "main" || sets[1].VersionKey != fmt.Sprintf("alt:%d", altSrc) {
			t.Fatalf("sets = %+v", sets)
		}
		m, a := sets[0], sets[1]
		if m.ValidPlays != 1203 || m.DurationMS == nil || *m.DurationMS != 600_000 {
			t.Fatalf("main valid=%d duration=%v", m.ValidPlays, m.DurationMS)
		}
		d := m.Data
		if d["viewers"][0] != 1203 || d["viewers"][5] != 903 || d["replays"][100] != 300 || d["replays"][99] != 0 ||
			d["skips"][5] != 300 || d["skips"][1] != 0 || d["skips"][50] != 0 || d["retention"][0] != 10000 || d["retention"][5] != 7506 {
			t.Errorf("main counts: v0=%d v5=%d r100=%d r99=%d s5=%d s1=%d s50=%d ret0=%d ret5=%d", d["viewers"][0], d["viewers"][5],
				d["replays"][100], d["replays"][99], d["skips"][5], d["skips"][1], d["skips"][50], d["retention"][0], d["retention"][5])
		}
		p := d["replay_peak"]
		if len(p) != 200 || p[102] != 255 || p[100] != 170 || p[99] != 85 || p[98] != 0 || p[0] != 0 {
			t.Errorf("main replay_peak around 100: %v", p[95:110])
		}
		if a.ValidPlays != 3 || a.DurationMS == nil || *a.DurationMS != 700_000 || a.Data["viewers"][0] != 3 || a.Data["replays"][0] != 3 {
			t.Errorf("alt = %+v", a)
		}
		if peak, ok := a.Data["replay_peak"]; !ok || peak != nil {
			t.Errorf("alt replay_peak should be stored as NULL below 1000 plays: %v %v", ok, peak)
		}
		replay, err := s.ReplayCurves(ctx, rel)
		if err != nil || len(replay) != 1 || replay["main"][102] != 255 {
			t.Errorf("ReplayCurves = %v, %v", replay, err)
		}
	}
	check()

	// Re-running (watermark set, nothing new besides the slack window) is idempotent.
	if _, err := (&playcurve.Runner{Store: s}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	check()

	h := api.New(s, nil)
	get := func(path, auth string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	pc := get("/api/releases/"+slug+"/play-config", "")
	rc, _ := pc["replay_curve"].([]any)
	peak, _ := pc["peak_curve"].([]any)
	if len(rc) != 200 || rc[102].(float64) != 255 {
		t.Fatalf("play-config replay_curve = %v", pc["replay_curve"])
	}
	// 70% danmaku (255 at 50) + 30% replay (255 at 101-103, 170 at 100),
	// renormalized: 50 → 255, 102 → 109, 100 → 73.
	if len(peak) != 200 || peak[50].(float64) != 255 || peak[102].(float64) != 109 || peak[100].(float64) != 73 || peak[0].(float64) != 0 {
		t.Fatalf("play-config peak_curve: 50=%v 100=%v 102=%v", peak[50], peak[100], peak[102])
	}

	ad := get(fmt.Sprintf("/api/admin/releases/%d/curves", rel), "curves-admin")
	if ad["intro_skip_suggested"] != true {
		t.Errorf("intro_skip_suggested = %v", ad["intro_skip_suggested"])
	}
	vs, _ := ad["versions"].([]any)
	if len(vs) != 2 {
		t.Fatalf("admin versions = %v", ad["versions"])
	}
	mv := vs[0].(map[string]any)
	if mv["version_key"] != "main" || mv["valid_plays"].(float64) != 1203 || mv["sufficient"] != true ||
		mv["retention"].([]any)[5].(float64) != 0.7506 || mv["skip_rate"].([]any)[5].(float64) != 0.2494 {
		t.Errorf("admin main = %v", mv)
	}
	if av := vs[1].(map[string]any); av["sufficient"] != false || av["replay_peak"] != nil {
		t.Errorf("admin alt = %v", av)
	}
}
