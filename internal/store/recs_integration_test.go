package store_test

// External test package so it can drive the real HTTP handler (api imports
// store). Runs only under `make test-integration` (AVXXX_TEST_DATABASE_URL);
// assertions are scoped to the rows these tests create.

import (
	"avxxx/internal/api"
	"avxxx/internal/events"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func openRecsDB(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = store.Open(ctx); err != nil { // migrations must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s, ctx
}

type recsResp struct {
	Items []struct {
		ID     int64       `json:"id"`
		Slug   string      `json:"slug"`
		Reason recs.Reason `json:"reason"`
		Match  int         `json:"match_percent"`
	} `json:"items"`
	Item *struct {
		ID int64 `json:"id"`
	} `json:"item"`
	Alternates []struct {
		ID int64 `json:"id"`
	} `json:"alternates"`
}

func getJSON(t *testing.T, h http.Handler, method, path, token, body string) (int, recsResp, string) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out recsResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

func (r recsResp) ids() []int64 {
	var out []int64
	for _, x := range r.Items {
		out = append(out, x.ID)
	}
	return out
}

// TestRecsIntegration seeds watch history, events, follows and playback
// sources, runs the similarity and trending jobs, and checks the co-watch
// output, the boards and the public / admin API end to end.
func TestRecsIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := s.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	perf := func(name string) int64 {
		return id(`INSERT INTO performers(slug,canonical_name) VALUES($1,$2) RETURNING id`, name+"-"+sfx, name+sfx)
	}
	p1, p2, p3 := perf("p1"), perf("p2"), perf("p3")
	rel := func(name, status, maker string, runtime int, cast ...int64) int64 {
		r := id(`INSERT INTO releases(slug,canonical_title,status,maker_name,runtime_minutes,release_date) VALUES($1,$2,$3,$4,$5,current_date - 3) RETURNING id`,
			name+"-"+sfx, "Title "+name, status, maker+sfx, runtime)
		for i, p := range cast {
			exec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,$3,$4)`, r, p, i+1, i == 0)
		}
		exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$2)`, r, "tag"+sfx)
		exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url) VALUES($1,'external_link','p','main',$2)`, r, "https://example.test/"+name+sfx)
		return r
	}
	a := rel("a", "published", "M", 25, p1)
	b := rel("b", "published", "M", 25, p1)
	c := rel("c", "published", "N", 25, p1, p2)
	d := rel("d", "published", "N", 25, p2)
	e := rel("e", "published", "Z", 25, p3)
	held := rel("held", "held_review", "M", 25, p1)
	exec(`UPDATE playback_sources SET status='disabled' WHERE release_id=$1`, e) // e is not playable

	users := make([]int64, 6)
	tokens := make([]string, 6)
	for i := range users {
		m, err := s.Register(ctx, fmt.Sprintf("recs%d-%s@example.test", i, sfx), "password123", "")
		if err != nil {
			t.Fatal(err)
		}
		tokens[i] = m["token"].(string)
		users[i] = m["user"].(map[string]any)["id"].(int64)
	}
	watch := func(u, r int64, progress int) {
		exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,$3,1500,now() - interval '1 hour')
			ON CONFLICT (user_id,release_id) DO UPDATE SET progress_seconds=EXCLUDED.progress_seconds`, u, r, progress)
	}
	// users 0-3 finished a and b; 0-2 also started c; user 5 watched a, held and e.
	for _, u := range users[:4] {
		watch(u, a, 1500)
		watch(u, b, 1500)
	}
	for _, u := range users[:3] {
		watch(u, c, 100)
	}
	watch(users[5], a, 1500)
	watch(users[5], held, 1500)
	watch(users[5], e, 1500)
	// Events: user 4 completes a and d (play_end carries no release_id); with
	// users 0-2 having nothing on d this only adds evidence for a-d.
	var evs []events.Event
	now := time.Now().UTC()
	n := 0
	add := func(u int64, name, play string, r *int64, props map[string]any) {
		n++
		evs = append(evs, events.Event{EventID: fmt.Sprintf("recs-%s-%d", sfx, n), Name: name, SchemaVer: 1, TSClient: now, TSServer: now.Add(-time.Minute),
			UserID: &u, SessionID: "s", PlayID: play, Platform: "web", AppVer: "1", Consent: []string{"necessary", "analytics"}, ReleaseID: r, Props: props})
	}
	for _, r := range []int64{a, d} {
		play := fmt.Sprintf("play-%s-%d", sfx, r)
		add(users[4], "play_request", play, &r, map[string]any{"release_id": r, "source_id": 1, "entry": "search"})
		add(users[4], "progress", play, &r, map[string]any{"release_id": r, "milestone": 100})
		add(users[4], "play_end", play, nil, map[string]any{"reason": "completed", "watched_seconds": 1500})
	}
	if _, err := s.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$2)`, users[4], p2)

	// ---- offline jobs
	runner := &recs.Runner{Store: s}
	sim, err := runner.RunSimilarity(ctx)
	if err != nil {
		t.Fatalf("RunSimilarity: %v (%+v)", err, sim)
	}
	if sim.CoWatchRows == 0 || sim.PerformerRows == 0 {
		t.Errorf("similarity report %+v", sim)
	}
	scoreOf := func(x, y int64, reason string) float64 {
		var v float64
		err := s.QueryRow(ctx, `SELECT score FROM release_similar WHERE release_id=$1 AND similar_id=$2 AND reason=$3`, x, y, reason).Scan(&v)
		if err != nil {
			return -1
		}
		return v
	}
	ab, ac := scoreOf(a, b, "cowatch"), scoreOf(a, c, "cowatch")
	if ab <= 0 || ac <= 0 || ab <= ac {
		t.Errorf("co-watch a~b %v (4 finished) should beat a~c %v (3 started)", ab, ac)
	}
	if scoreOf(b, a, "cowatch") != ab {
		t.Errorf("co-watch must be symmetric")
	}
	if scoreOf(a, d, "cowatch") != -1 {
		t.Errorf("a~d has support 1 < 3 and must not be written")
	}
	if v := id(`SELECT count(*) FROM release_similar WHERE $1 IN (release_id, similar_id)`, held); v != 0 {
		t.Errorf("held_review release in release_similar: %d rows", v)
	}
	if scoreOf(a, b, "maker") <= 0 {
		t.Errorf("a and b share maker and tag: want a content row")
	}
	var ev []byte
	if err := s.QueryRow(ctx, `SELECT evidence FROM performer_similar WHERE performer_id=$1 AND similar_id=$2`, p1, p2).Scan(&ev); err != nil {
		t.Fatalf("p1~p2 (co-star in c): %v", err)
	}
	var evidence recs.Evidence
	if err := json.Unmarshal(ev, &evidence); err != nil || evidence.CoStar != 1 || evidence.CoStarScore <= 0 {
		t.Errorf("p1~p2 evidence %s", ev)
	}
	if id(`SELECT count(*) FROM nightly_runs WHERE job='recs.similarity' AND result='ok'`) == 0 {
		t.Errorf("no nightly_runs row")
	}

	if _, err := runner.RunTrending(ctx); err != nil {
		t.Fatalf("RunTrending: %v", err)
	}
	var rank int
	var prev *int
	if err := s.QueryRow(ctx, `SELECT rank, prev_rank FROM trending_snapshots WHERE period='today' AND release_id=$1`, a).Scan(&rank, &prev); err != nil {
		t.Fatalf("a not on today's board: %v", err)
	}
	if prev != nil {
		t.Errorf("first run prev_rank = %v, want NULL", *prev)
	}
	if id(`SELECT count(*) FROM trending_snapshots WHERE release_id=$1`, held) != 0 {
		t.Errorf("held release on a board")
	}
	if _, err := runner.RunTrending(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.QueryRow(ctx, `SELECT rank, prev_rank FROM trending_snapshots WHERE period='today' AND release_id=$1`, a).Scan(&rank, &prev); err != nil || prev == nil || *prev != rank {
		t.Errorf("second run: rank %d prev %v err %v", rank, prev, err)
	}
	if id(`SELECT count(*) FROM performer_heat_snapshots WHERE period='week' AND performer_id=$1`, p1) != 1 {
		t.Errorf("p1 missing from performer heat")
	}

	// ---- API
	t.Setenv("ADMIN_TOKEN", "recs-secret")
	h := api.New(s, nil)
	aSlug := "a-" + sfx

	code, out, raw := getJSON(t, h, "GET", "/api/recs?slot=related&release="+aSlug+"&limit=50", "", "")
	if code != 200 {
		t.Fatalf("related: %d %s", code, raw)
	}
	got := out.ids()
	if !slices.Contains(got, b) || slices.Contains(got, held) || slices.Contains(got, a) || slices.Contains(got, e) {
		t.Errorf("related ids %v: want b, never a (seed), held or e (unplayable)", got)
	}
	if out.Items[0].ID != b || out.Items[0].Reason.Text != "看过此视频的人也看了" {
		t.Errorf("top related = %+v", out.Items[0])
	}
	// User 0 already watched b and c: filtered.
	_, out, _ = getJSON(t, h, "GET", "/api/recs?slot=related&release="+aSlug+"&limit=50", tokens[0], "")
	if got := out.ids(); slices.Contains(got, b) || slices.Contains(got, c) {
		t.Errorf("watched filter: %v", got)
	}
	// Home for user 5 (watched a): b via co-watch with a because-watched reason.
	_, out, raw = getJSON(t, h, "GET", "/api/recs?slot=home&limit=50", tokens[5], "")
	found := false
	for _, it := range out.Items {
		if it.ID == b && it.Reason.Text == "因为你看过《Title a》" {
			found = true
		}
		if it.ID == held || it.ID == a {
			t.Errorf("home recommended held or watched release %d", it.ID)
		}
	}
	if !found {
		t.Errorf("home for user 5 lacks b because-watched: %s", raw)
	}
	// Followed performer's new work (user 4 follows p2; d and c are p2's).
	_, out, raw = getJSON(t, h, "GET", "/api/recs?slot=home&limit=50", tokens[4], "")
	if !strings.Contains(raw, "你关注的 p2"+sfx+" 有新作") {
		t.Errorf("followed new missing: %s", raw)
	}
	// Visitor home: trending + new only, still filtered.
	code, out, raw = getJSON(t, h, "GET", "/api/recs?slot=home", "", "")
	if code != 200 || len(out.Items) == 0 || slices.Contains(out.ids(), held) {
		t.Errorf("visitor home %d %s", code, raw)
	}
	// Tonight for user 0: never watched, alternates distinct.
	code, out, raw = getJSON(t, h, "GET", "/api/recs?slot=tonight&duration=30", tokens[0], "")
	if code != 200 || out.Item == nil || slices.Contains([]int64{a, b, c, held, e}, out.Item.ID) {
		t.Errorf("tonight %d %s", code, raw)
	}
	if out.Item != nil {
		_, out2, _ := getJSON(t, h, "GET", fmt.Sprintf("/api/recs?slot=tonight&duration=30&exclude=%d", out.Item.ID), tokens[0], "")
		if out2.Item != nil && out2.Item.ID == out.Item.ID {
			t.Errorf("换一个 returned the excluded release")
		}
	}
	// Similar performers.
	_, out, raw = getJSON(t, h, "GET", "/api/recs?slot=similar_performers&performer=p1-"+sfx, "", "")
	if !slices.Contains(out.ids(), p2) {
		t.Errorf("similar performers of p1: %s", raw)
	}
	// Trending board.
	code, _, raw = getJSON(t, h, "GET", "/api/trending?period=today&limit=50", "", "")
	if code != 200 || !strings.Contains(raw, fmt.Sprintf(`"id":%d`, a)) || !strings.Contains(raw, `"performers":[`) {
		t.Errorf("trending %d %s", code, raw)
	}
	if code, _, raw = getJSON(t, h, "GET", "/api/trending/performers?period=week", "", ""); code != 200 {
		t.Errorf("performer heat %d %s", code, raw)
	}

	// ---- admin
	code, _, raw = getJSON(t, h, "PUT", "/api/admin/similarity-weights", "recs-secret", `{"co_star":0.9}`)
	if code != 200 || !strings.Contains(raw, `"co_star":0.9`) {
		t.Errorf("put weights %d %s", code, raw)
	}
	w, err := s.SimilarityWeights(ctx)
	if err != nil || w.CoStar < 0.89 || w.CoStar > 0.91 {
		t.Errorf("weights after put %+v %v", w, err)
	}
	if _, err := s.SetSimilarityWeights(ctx, recs.DefaultWeights, "test"); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM jobs WHERE kind IN ($1,$2) AND status='pending'`, recs.JobKindSimilarity, recs.JobKindTrending)
	code, _, raw = getJSON(t, h, "POST", "/api/admin/recs/rebuild", "recs-secret", "")
	if code != 202 || strings.Count(raw, `"enqueued":true`) != 2 {
		t.Errorf("rebuild %d %s", code, raw)
	}
	if code, _, raw = getJSON(t, h, "POST", "/api/admin/recs/rebuild", "recs-secret", ""); code != 202 || strings.Contains(raw, `"enqueued":true`) {
		t.Errorf("second rebuild must reuse the pending jobs: %d %s", code, raw)
	}
	jid, ver, err := s.ClaimJob(ctx, recs.JobKindTrending)
	if err != nil {
		t.Fatalf("claim trending job: %v", err)
	}
	if err := s.FinishJob(ctx, jid, ver, map[string]int{}, nil); err != nil {
		t.Fatal(err)
	}
	exec(`DELETE FROM jobs WHERE kind=$1 AND status='pending'`, recs.JobKindSimilarity)
}
