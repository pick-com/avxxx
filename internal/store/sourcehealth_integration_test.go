package store

import (
	"avxxx/internal/audit"
	"avxxx/internal/events"
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// TestSourceHealthIntegration seeds play events for playback sources and
// runs the source health job against a real, disposable PostgreSQL database
// (migration 016, the aggregation SQL, status changes, audit issues, the
// admin list and the dashboard block). Skipped unless AVXXX_TEST_DATABASE_URL
// is set; the database is modified. Assertions are scoped to the rows this
// test creates, so it can share the database with the other integration tests.
func TestSourceHealthIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // migrations must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()

	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// Only this test's sources are scored: earlier tests' rows have no events.
	exec(`UPDATE playback_sources SET health_checked_at=NULL`)
	exec(`DELETE FROM source_health_runs`)
	var rel, a, b, c int64
	if err = s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,'Health','published') RETURNING id`, "health-"+sfx).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		id    *int64
		label string
	}{{&a, "good"}, {&b, "bad"}, {&c, "sparse"}} {
		if err = s.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url) VALUES($1,'external_link','p',$2,$3) RETURNING id`,
			rel, x.label, "https://example.test/"+x.label+sfx).Scan(x.id); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	base := now.Add(-time.Hour)
	var evs []events.Event
	n := 0
	add := func(name, play string, src *int64, at time.Time, serverAt time.Time, props map[string]any) {
		n++
		evs = append(evs, events.Event{EventID: fmt.Sprintf("%s-%d", sfx, n), Name: name, SchemaVer: 1, TSClient: at, TSServer: serverAt,
			SessionID: "s", PlayID: play, Platform: "web", AppVer: "1", Consent: []string{"necessary"}, ReleaseID: &rel, SourceID: src, Props: props})
	}
	type play struct {
		src           *int64
		ff            []int64 // first_frame elapsed_ms values, in client order
		fatalBeforeFF bool
		fatalAfterFF  bool
		switchAway    string // "", "auto", "manual"
		serverAt      time.Time
		rebuffers     int
	}
	plays := 0
	seed := func(p play) {
		plays++
		id := fmt.Sprintf("p%s-%d", sfx, plays)
		t0 := base.Add(time.Duration(plays) * time.Second)
		at := p.serverAt
		if at.IsZero() {
			at = base
		}
		add("play_request", id, p.src, t0, at, map[string]any{"release_id": rel, "source_id": *p.src, "entry": "search"})
		if p.fatalBeforeFF {
			add("play_end", id, nil, t0.Add(500*time.Millisecond), at, map[string]any{"reason": "error", "watched_seconds": 0})
		}
		for i, ms := range p.ff {
			add("first_frame", id, p.src, t0.Add(time.Duration(i+1)*time.Second), at, map[string]any{"source_id": *p.src, "quality": "1080p", "elapsed_ms": ms, "network": "4g"})
		}
		for range p.rebuffers {
			add("rebuffer", id, p.src, t0.Add(3*time.Second), at, map[string]any{"source_id": *p.src, "duration_ms": 900, "position_ms": 1000})
		}
		if p.fatalAfterFF {
			add("play_end", id, nil, t0.Add(5*time.Second), at, map[string]any{"reason": "error", "watched_seconds": 4})
		}
		if p.switchAway != "" {
			add("source_switch", id, nil, t0.Add(8*time.Second), at, map[string]any{"from_source_id": *p.src, "to_source_id": a, "mode": p.switchAway, "reason": "timeout"})
		}
	}
	// A: 40 plays, 36 fast first frames, 4 slow → 100×(0.7×1 + 0.3×0.9) = 97.
	for i := range 40 {
		ms := int64(800)
		if i < 4 {
			ms = 2000
		}
		p := play{src: &a, ff: []int64{ms}}
		if i < 2 {
			p.rebuffers = 1
		}
		seed(p)
	}
	// Outside the 24 h window: must not count against A.
	for range 10 {
		seed(play{src: &a, serverAt: now.Add(-25 * time.Hour)})
	}
	// B: 40 plays → attempts 40, playable 36, ff_ok 6 → 100×(0.63 + 0.3×6/36) = 68.
	for i := range 40 {
		p := play{src: &b}
		switch {
		case i < 2: // fatal error before the first frame: not playable
			p.ff, p.fatalBeforeFF = []int64{900}, true
		case i < 8: // fast
			p.ff = []int64{1000}
		case i < 10: // error after the first frame: still playable (slow)
			p.ff, p.fatalAfterFF = []int64{3000}, true
		case i == 10: // two first frames: the first (slow) one counts
			p.ff = []int64{3000, 500}
		case i < 38:
			p.ff = []int64{3000}
		case i == 38:
			p.switchAway = "auto"
		default:
			p.switchAway = "manual" // not counted as switch-away
		}
		seed(p)
	}
	// C: 10 failing plays → below the 30-attempt minimum.
	for range 10 {
		seed(play{src: &c})
	}
	if _, err = s.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}

	samples, err := s.SourceHealthSamples(ctx, now.Add(-24*time.Hour), now.Add(time.Minute), 1500)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]audit.SourceSample{}
	for _, x := range samples {
		got[x.SourceID] = x
	}
	if x := got[a]; x.Attempts != 40 || x.Playable != 40 || x.FirstFrameOK != 36 || x.Rebuffers != 2 || x.SwitchesAway != 0 {
		t.Errorf("A sample %+v", x)
	}
	if x := got[b]; x.Attempts != 40 || x.Playable != 36 || x.FirstFrameOK != 6 || x.SwitchesAway != 1 || x.ReleaseTitle != "Health" {
		t.Errorf("B sample %+v", x)
	}
	if x := got[c]; x.Attempts != 10 || x.Playable != 0 {
		t.Errorf("C sample %+v", x)
	}

	runAt := now.Add(time.Minute)
	runner := &audit.HealthRunner{Store: s, Now: func() time.Time { return runAt }}
	rep, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Changes) != 1 {
		t.Errorf("run 1 changes %+v", rep.Changes)
	}
	type state struct {
		status  string
		health  float32
		checked bool
	}
	load := func(id int64) state {
		t.Helper()
		var st state
		if err := s.QueryRow(ctx, `SELECT status,health_score,health_checked_at IS NOT NULL FROM playback_sources WHERE id=$1`, id).Scan(&st.status, &st.health, &st.checked); err != nil {
			t.Fatal(err)
		}
		return st
	}
	near := func(a float32, b float64) bool { return math.Abs(float64(a)-b) < 1e-4 }
	if st := load(a); st.status != "active" || !near(st.health, 0.97) || !st.checked {
		t.Errorf("A after run 1: %+v", st)
	}
	if st := load(b); st.status != "degraded" || !near(st.health, 0.68) {
		t.Errorf("B after run 1: %+v", st)
	}
	if st := load(c); st.status != "active" || st.health != 1 || st.checked {
		t.Errorf("C after run 1 (insufficient): %+v", st)
	}
	var band string
	var insufficient bool
	var score *float32
	if err = s.QueryRow(ctx, `SELECT band,insufficient_data,score FROM source_health_runs WHERE source_id=$1`, c).Scan(&band, &insufficient, &score); err != nil {
		t.Fatal(err)
	}
	if band != audit.BandInsufficient || !insufficient || score == nil || *score != 0 {
		t.Errorf("C run row band=%s insufficient=%v score=%v", band, insufficient, score)
	}
	countEvents := func(id int64) int {
		var n int
		if err := s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='playback_source' AND entity_id=$1 AND actor='health' AND action='status_change'`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if countEvents(a) != 0 || countEvents(b) != 1 || countEvents(c) != 0 {
		t.Errorf("review events a=%d b=%d c=%d", countEvents(a), countEvents(b), countEvents(c))
	}
	issueSeverity := func(id int64) string {
		var sev string
		if err := s.QueryRow(ctx, `SELECT COALESCE(max(severity),'') FROM audit_issues WHERE rule='R-SOURCE' AND entity_type='playback_source' AND entity_id=$1 AND status='open'`, id).Scan(&sev); err != nil {
			t.Fatal(err)
		}
		return sev
	}
	if sev := issueSeverity(b); sev != "medium" {
		t.Errorf("B issue severity %q", sev)
	}
	facts, hasRuns, err := s.SourceHealthIssues(ctx)
	if err != nil || !hasRuns {
		t.Fatalf("SourceHealthIssues: %v %v", hasRuns, err)
	}
	foundB := false
	for _, f := range facts {
		if f.SourceID == b {
			foundB = f.Status == "degraded" && f.Score != nil && math.Abs(*f.Score-68) < 1e-3 && f.Attempts == 40
		}
		if f.SourceID == a || f.SourceID == c {
			t.Errorf("unexpected R-SOURCE fact %+v", f)
		}
	}
	if !foundB {
		t.Errorf("R-SOURCE facts %+v missing degraded B", facts)
	}

	// Admin list: scored sources worst first, keyset paginated.
	items, next, err := s.SourceHealthList(ctx, audit.SourceHealthFilter{Limit: 1})
	if err != nil || len(items) != 1 || items[0].SourceID != b || next == "" {
		t.Fatalf("list page 1: %+v next=%q err=%v", items, next, err)
	}
	if it := items[0]; it.Score == nil || *it.Score != 68 || it.Band != audit.BandDegraded || it.Latest == nil || it.Latest.Attempts != 40 ||
		it.Latest.SwitchesAway != 1 || len(it.Trend) != 1 || it.ReleaseSlug != "health-"+sfx {
		t.Errorf("B item %+v latest %+v", it, it.Latest)
	}
	items, _, err = s.SourceHealthList(ctx, audit.SourceHealthFilter{Limit: 1, Cursor: next})
	if err != nil || len(items) != 1 || items[0].SourceID != a || *items[0].Score != 97 || items[0].Band != audit.BandExcellent {
		t.Fatalf("list page 2: %+v err=%v", items, err)
	}
	if items, _, err = s.SourceHealthList(ctx, audit.SourceHealthFilter{Band: audit.BandDegraded}); err != nil || len(items) != 1 || items[0].SourceID != b {
		t.Errorf("band=degraded: %+v %v", items, err)
	}
	items, _, err = s.SourceHealthList(ctx, audit.SourceHealthFilter{Band: audit.BandInsufficient, Limit: 100})
	foundC := false
	for _, it := range items {
		foundC = foundC || (it.SourceID == c && it.Score == nil && it.Latest != nil && it.Latest.Insufficient)
	}
	if err != nil || !foundC {
		t.Errorf("band=insufficient_data: %+v %v", items, err)
	}

	d, err := s.AdminDashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Pooled: attempts 90, playable 76, ff_ok 42 → 100×(0.7×76/90 + 0.3×42/76) = 75.7.
	sh := d.SourceHealth
	if sh.Score == nil || *sh.Score != 75 || sh.Band != audit.BandDegraded || sh.Attempts != 90 || sh.ByStatus["degraded"] < 1 ||
		sh.ByBand[audit.BandExcellent] < 1 || sh.ByBand[audit.BandDegraded] < 1 || sh.LastRunAt == nil {
		t.Errorf("dashboard source_health %+v score %v", sh, sh.Score)
	}

	// Run 2: B keeps failing → disabled; an admin disabled C, and C's good
	// plays must not re-enable it.
	exec(`UPDATE playback_sources SET status='disabled' WHERE id=$1`, c)
	evs = nil
	for range 40 {
		seed(play{src: &b})
	}
	for range 30 {
		seed(play{src: &c, ff: []int64{700}})
	}
	if _, err = s.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	runAt = now.Add(11 * time.Minute)
	if rep, err = runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	// B: 80 attempts, 36 playable, 6 ok → 100×(0.315 + 0.05) = 36.5.
	if st := load(b); st.status != "disabled" || !near(st.health, 0.365) {
		t.Errorf("B after run 2: %+v", st)
	}
	// C: 40 attempts, 30 playable, 30 ok → 82.5, but stays disabled.
	if st := load(c); st.status != "disabled" || !near(st.health, 0.825) || !st.checked {
		t.Errorf("C after run 2: %+v", st)
	}
	if countEvents(b) != 2 || countEvents(c) != 0 {
		t.Errorf("review events after run 2: b=%d c=%d", countEvents(b), countEvents(c))
	}
	if sev := issueSeverity(b); sev != "high" {
		t.Errorf("B issue severity after disable %q", sev)
	}
	if items, _, err = s.SourceHealthList(ctx, audit.SourceHealthFilter{Band: audit.BandSevere}); err != nil || len(items) != 1 || items[0].SourceID != b || len(items[0].Trend) != 2 {
		t.Errorf("band=severe after run 2: %+v %v", items, err)
	}

	// Recovery path: an admin re-enables B as degraded; good data plus the
	// hysteresis threshold brings it back to active and resolves the issue.
	exec(`UPDATE playback_sources SET status='degraded' WHERE id=$1`, b)
	exec(`DELETE FROM events WHERE source_id=$1 OR play_id IN (SELECT play_id FROM events WHERE source_id=$1)`, b)
	evs = nil
	for range 40 {
		seed(play{src: &b, ff: []int64{600}})
	}
	if _, err = s.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	runAt = now.Add(21 * time.Minute)
	if _, err = runner.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if st := load(b); st.status != "active" || !near(st.health, 1) {
		t.Errorf("B after recovery: %+v", st)
	}
	if sev := issueSeverity(b); sev != "" {
		t.Errorf("B issue still open after recovery: %q", sev)
	}

	// Jobs: the scheduler slot is enqueued once, and the worker can claim it.
	slot := time.Now().Truncate(10 * time.Minute)
	exec(`DELETE FROM jobs WHERE kind=$1`, JobKindSourceHealth)
	if ok, err := s.EnqueueKindSince(ctx, JobKindSourceHealth, slot); err != nil || !ok {
		t.Fatalf("enqueue: %v %v", ok, err)
	}
	if ok, err := s.EnqueueKindSince(ctx, JobKindSourceHealth, slot); err != nil || ok {
		t.Fatalf("second enqueue in the same slot: %v %v", ok, err)
	}
	id, version, err := s.ClaimJob(ctx, JobKindSourceHealth)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.FinishJob(ctx, id, version, map[string]any{"ok": true}, nil); err != nil {
		t.Fatal(err)
	}
}
