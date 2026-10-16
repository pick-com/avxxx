package store_test

import (
	"avxxx/internal/api"
	"avxxx/internal/recs"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"
)

// recsP99Target is the tech plan 3.5 budget for the precomputed paths.
const recsP99Target = 50 * time.Millisecond

// TestRecsLatencyIntegration is the synthetic-data benchmark for GET
// /api/recs and /api/trending: 5 000 releases, 300 performers, 2 000 users
// with 20 skewed watches each (40 000 watch_history rows) and 3 follows,
// a playback source per release. It runs both jobs, then times 1 400
// in-process requests across every slot and fails when the overall P99
// exceeds 50 ms. RECS_BENCH_REQUESTS overrides the requests per slot.
func TestRecsLatencyIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	ids := func(q string, args ...any) []int64 {
		t.Helper()
		var out []int64
		if err := s.QueryRow(ctx, `WITH ins AS (`+q+` RETURNING id) SELECT array_agg(id ORDER BY id) FROM ins`, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if len(out) == 0 || out[len(out)-1]-out[0]+1 != int64(len(out)) {
			t.Fatalf("ids not contiguous")
		}
		return out
	}
	const nRel, nPerf, nUsers, perUser = 5000, 300, 2000, 20
	seedStart := time.Now()
	perfs := ids(`INSERT INTO performers(slug,canonical_name) SELECT 'bench-p-'||$1||'-'||g, 'BenchP'||g FROM generate_series(1,$2::int) g`, sfx, nPerf)
	rels := ids(`INSERT INTO releases(slug,canonical_title,status,maker_name,series_name,runtime_minutes,release_date)
		SELECT 'bench-r-'||$1||'-'||g, 'Bench '||g, 'published', 'BMaker'||(g%50), CASE WHEN g%5=0 THEN 'BSeries'||(g%200) END,
		20+(g%150), current_date - (g%1000) FROM generate_series(1,$2::int) g`, sfx, nRel)
	p0, r0 := perfs[0], rels[0]
	exec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary)
		SELECT $1+g-1, $2+(g*7)%$4, 1, true FROM generate_series(1,$3::int) g`, r0, p0, nRel, nPerf)
	exec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary)
		SELECT $1+g-1, $2+(g*13+1)%$4, 2, false FROM generate_series(1,$3::int) g WHERE g%3=0 ON CONFLICT DO NOTHING`, r0, p0, nRel, nPerf)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag)
		SELECT DISTINCT $1+g-1, 'btag'||((g*k)%100), 'btag'||((g*k)%100) FROM generate_series(1,$2::int) g, generate_series(1,3) k`, r0, nRel)
	exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url,status)
		SELECT $1+g-1, 'external_link', 'bench', 'main', 'https://bench.test/'||$3||'/'||g, CASE WHEN g%10=0 THEN 'disabled' ELSE 'active' END
		FROM generate_series(1,$2::int) g`, r0, nRel, sfx)
	users := ids(`INSERT INTO users(email,password_hash) SELECT 'bench-'||$1||'-'||g||'@example.test', 'x' FROM generate_series(1,$2::int) g`, sfx, nUsers)
	u0 := users[0]
	// Skewed popularity: release index = floor(n · random()^2).
	exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at)
		SELECT $1+u-1, $2+floor($4::int*random()^2)::bigint, (random()*3000)::int, 3000, now() - random()*interval '20 days'
		FROM generate_series(1,$3::int) u, generate_series(1,$5::int) k ON CONFLICT DO NOTHING`, u0, r0, nUsers, nRel, perUser)
	exec(`INSERT INTO follows(user_id,performer_id,created_at)
		SELECT $1+u-1, $2+floor($4::int*random())::bigint, now() - random()*interval '10 days'
		FROM generate_series(1,$3::int) u, generate_series(1,3) k ON CONFLICT DO NOTHING`, u0, p0, nUsers, nPerf)
	exec(`ANALYZE`)
	m, err := s.Register(ctx, "bench-"+sfx+"@example.test", "password123", "")
	if err != nil {
		t.Fatal(err)
	}
	token := m["token"].(string)
	me := m["user"].(map[string]any)["id"].(int64)
	exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at)
		SELECT $1, $2+floor($3::int*random()^2)::bigint, 2500, 3000, now() - random()*interval '5 days' FROM generate_series(1,40) ON CONFLICT DO NOTHING`, me, r0, nRel)
	exec(`INSERT INTO follows(user_id,performer_id) SELECT $1, $2+g*11 FROM generate_series(1,5) g ON CONFLICT DO NOTHING`, me, p0)
	t.Logf("seeded in %s", time.Since(seedStart).Round(time.Millisecond))

	runner := &recs.Runner{Store: s}
	jobStart := time.Now()
	sim, err := runner.RunSimilarity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("similarity job: %s (releases=%d interactions=%d cowatch=%d content=%d performer=%d)",
		time.Since(jobStart).Round(time.Millisecond), sim.Releases, sim.Interactions, sim.CoWatchRows, sim.ContentRows, sim.PerformerRows)
	jobStart = time.Now()
	if _, err := runner.RunTrending(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("trending job: %s", time.Since(jobStart).Round(time.Millisecond))
	exec(`ANALYZE release_similar; ANALYZE performer_similar; ANALYZE trending_snapshots; ANALYZE performer_heat_snapshots`)

	h := api.New(s, nil)
	perSlot := 200
	if v, err := strconv.Atoi(os.Getenv("RECS_BENCH_REQUESTS")); err == nil && v > 0 {
		perSlot = v
	}
	r := rand.New(rand.NewPCG(7, 8))
	type slot struct {
		name  string
		token string
		path  func() string
	}
	relSlug := func() string { return fmt.Sprintf("bench-r-%s-%d", sfx, r.IntN(nRel)+1) }
	slots := []slot{
		{"home(user)", token, func() string { return "/api/recs?slot=home&lang=zh-CN" }},
		{"home(visitor)", "", func() string { return "/api/recs?slot=home" }},
		{"related", token, func() string { return "/api/recs?slot=related&release=" + relSlug() }},
		{"tonight", token, func() string { return "/api/recs?slot=tonight&duration=60" }},
		{"similar_performers", "", func() string {
			return fmt.Sprintf("/api/recs?slot=similar_performers&performer=bench-p-%s-%d", sfx, r.IntN(nPerf)+1)
		}},
		{"performers(user)", token, func() string { return "/api/recs?slot=performers" }},
		{"trending", "", func() string { return "/api/trending?period=week" }},
	}
	// Warm up connections and caches.
	for _, sl := range slots {
		req := httptest.NewRequest("GET", sl.path(), nil)
		if sl.token != "" {
			req.Header.Set("Authorization", "Bearer "+sl.token)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", sl.name, rec.Code, rec.Body)
		}
	}
	var all []time.Duration
	for _, sl := range slots {
		var ds []time.Duration
		for range perSlot {
			req := httptest.NewRequest("GET", sl.path(), nil)
			if sl.token != "" {
				req.Header.Set("Authorization", "Bearer "+sl.token)
			}
			rec := httptest.NewRecorder()
			start := time.Now()
			h.ServeHTTP(rec, req)
			ds = append(ds, time.Since(start))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", sl.name, rec.Code, rec.Body)
			}
		}
		all = append(all, ds...)
		t.Logf("%-20s n=%d p50=%s p95=%s p99=%s max=%s", sl.name, len(ds), pct(ds, 50), pct(ds, 95), pct(ds, 99), pct(ds, 100))
	}
	p99 := pct(all, 99)
	t.Logf("%-20s n=%d p50=%s p95=%s p99=%s", "ALL", len(all), pct(all, 50), pct(all, 95), p99)
	if p99 > recsP99Target {
		t.Errorf("overall P99 %s exceeds %s", p99, recsP99Target)
	}
}

func pct(ds []time.Duration, p int) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	i := (len(s)*p + 99) / 100
	i = min(max(i, 1), len(s))
	return s[i-1].Round(10 * time.Microsecond)
}
