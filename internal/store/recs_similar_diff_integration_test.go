package store_test

import (
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

// simKey is a release_similar primary key.
type simKey struct {
	release, similar int64
	reason           string
}

// simSnapshot reads release_similar rows of the given releases.
func simSnapshot(t *testing.T, ctx context.Context, s *store.Store, ids []int64) map[simKey]float32 {
	t.Helper()
	rows, err := s.Query(ctx, `SELECT release_id, similar_id, reason, score FROM release_similar WHERE release_id = ANY($1)`, ids)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[simKey]float32{}
	for rows.Next() {
		var k simKey
		var v float32
		if err := rows.Scan(&k.release, &k.similar, &k.reason, &v); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// recallOrder is what the recall queries read: each release's neighbours by
// score descending (ties by key, which the queries leave unordered).
func recallOrder(t *testing.T, ctx context.Context, s *store.Store, release int64) []simKey {
	t.Helper()
	rows, err := s.Query(ctx, `SELECT similar_id, reason FROM release_similar WHERE release_id=$1 ORDER BY score DESC, reason, similar_id`, release)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []simKey
	for rows.Next() {
		k := simKey{release: release}
		if err := rows.Scan(&k.similar, &k.reason); err != nil {
			t.Fatal(err)
		}
		out = append(out, k)
	}
	return out
}

// compareSimilar checks that got (the diff write) is equivalent to want (a
// full rewrite of the same rows): the same pairs, every score within eps
// (bit-identical when eps is 0), and for every release, every pair of rows
// with a strict order in want keeps that order in got.
func compareSimilar(t *testing.T, got, want map[simKey]float32, eps float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("rows: diff %d, rewrite %d", len(got), len(want))
	}
	byRelease := map[int64][]simKey{}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%v missing from the diff write", k)
			continue
		}
		if eps == 0 && math.Float32bits(g) != math.Float32bits(w) {
			t.Errorf("%v: diff %v, rewrite %v (eps 0 must be exact)", k, g, w)
		}
		if d := math.Abs(float64(g) - float64(w)); d > eps {
			t.Errorf("%v: diff %v, rewrite %v: |Δ| %g > eps %g", k, g, w, d, eps)
		}
		byRelease[k.release] = append(byRelease[k.release], k)
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%v only in the diff write", k)
		}
	}
	for r, ks := range byRelease {
		for _, a := range ks {
			for _, b := range ks {
				if want[a] > want[b] && !(got[a] > got[b]) {
					t.Errorf("release %d: rewrite orders %v (%v) before %v (%v), diff has %v, %v", r, a, want[a], b, want[b], got[a], got[b])
				}
			}
		}
	}
}

func sameRecall(t *testing.T, ctx context.Context, s *store.Store, ids []int64, want map[int64][]simKey) {
	t.Helper()
	for _, r := range ids {
		if got := recallOrder(t, ctx, s, r); !slices.Equal(got, want[r]) {
			t.Errorf("release %d recall order: diff %v, rewrite %v", r, got, want[r])
		}
	}
}

// TestReleaseSimilarDiffIntegration proves SyncReleaseSimilar (the nightly
// diff write) leaves release_similar equivalent to RewriteReleaseSimilar
// (DELETE + COPY) on the same input: synthetic nights with drift below and
// above eps, new and vanished pairs and reorderings, at eps 0 (exact) and at
// recs.ReleaseSimilarEpsilon.
func TestReleaseSimilarDiffIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	const n = 60
	ids := make([]int64, n)
	for i := range ids {
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,$1,'published') RETURNING id`,
			fmt.Sprintf("simdiff-%d-%s", i, sfx)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	reasons := []string{recs.ReasonCoWatch, recs.ReasonSeries, recs.ReasonMaker, recs.ReasonTags}
	rng := rand.New(rand.NewPCG(1, 2))
	// night0: up to 25 neighbours per release with random reasons and scores.
	var night0 []recs.ReleaseSimilarRow
	seen := map[simKey]bool{}
	for i, r := range ids {
		for range 5 + rng.IntN(20) {
			k := simKey{r, ids[(i+1+rng.IntN(n-1))%n], reasons[rng.IntN(len(reasons))]}
			if seen[k] {
				continue
			}
			seen[k] = true
			night0 = append(night0, recs.ReleaseSimilarRow{ReleaseID: k.release, SimilarID: k.similar, Score: rng.Float64(), Reason: k.reason})
		}
	}
	eps := recs.ReleaseSimilarEpsilon
	// night1: what a night does to them. Most scores drift a little (below
	// eps), some a lot, some pairs vanish and new ones appear.
	var night1 []recs.ReleaseSimilarRow
	for _, x := range night0 {
		switch p := rng.Float64(); {
		case p < 0.05: // vanished
			continue
		case p < 0.15:
			x.Score = rng.Float64()
		case p < 0.85:
			x.Score = max(0, x.Score+(rng.Float64()*2-1)*eps*0.9)
		}
		night1 = append(night1, x)
	}
	for i, r := range ids {
		if i%3 == 0 {
			k := simKey{r, ids[(i+n/2)%n], recs.ReasonTags}
			if !seen[k] {
				night1 = append(night1, recs.ReleaseSimilarRow{ReleaseID: k.release, SimilarID: k.similar, Score: rng.Float64(), Reason: k.reason})
			}
		}
	}
	// A release whose two neighbours swap places by less than eps each:
	// keeping both old scores would keep the old (wrong) order, so one of
	// them must be rewritten. Plus an exact tie.
	flip := ids[0]
	for _, rows := range []*[]recs.ReleaseSimilarRow{&night0, &night1} {
		*rows = slices.DeleteFunc(*rows, func(x recs.ReleaseSimilarRow) bool { return x.ReleaseID == flip })
	}
	night0 = append(night0,
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[1], Score: 0.50000, Reason: recs.ReasonTags},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[2], Score: 0.50004, Reason: recs.ReasonTags},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[3], Score: 0.25, Reason: recs.ReasonCoWatch},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[4], Score: 0.25, Reason: recs.ReasonCoWatch})
	night1 = append(night1,
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[1], Score: 0.50006, Reason: recs.ReasonTags},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[2], Score: 0.50002, Reason: recs.ReasonTags},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[3], Score: 0.25, Reason: recs.ReasonCoWatch},
		recs.ReleaseSimilarRow{ReleaseID: flip, SimilarID: ids[4], Score: 0.25, Reason: recs.ReasonCoWatch})

	at0, at1 := time.Now().Add(-24*time.Hour), time.Now()
	rewrite := func(rows []recs.ReleaseSimilarRow, at time.Time) (map[simKey]float32, map[int64][]simKey) {
		t.Helper()
		if err := s.RewriteReleaseSimilar(ctx, rows, at); err != nil {
			t.Fatal(err)
		}
		order := map[int64][]simKey{}
		for _, r := range ids {
			order[r] = recallOrder(t, ctx, s, r)
		}
		return simSnapshot(t, ctx, s, ids), order
	}
	want, wantOrder := rewrite(night1, at1)
	before, _ := rewrite(night0, at0)

	var diffs []recs.ReleaseSimilarWrite
	for _, e := range []float64{0, eps} {
		t.Run(fmt.Sprint("eps=", e), func(t *testing.T) {
			rewrite(night0, at0)
			w, err := s.SyncReleaseSimilar(ctx, night1, at1, e)
			if err != nil {
				t.Fatal(err)
			}
			diffs = append(diffs, w)
			got := simSnapshot(t, ctx, s, ids)
			compareSimilar(t, got, want, e)
			sameRecall(t, ctx, s, ids, wantOrder)
			// The counts match the snapshots: every insert is a new pair,
			// every delete a vanished one, every update a changed score.
			var ins, del, upd int64
			for k, v := range got {
				old, ok := before[k]
				switch {
				case !ok:
					ins++
				case old != v:
					upd++
				}
			}
			for k := range before {
				if _, ok := got[k]; !ok {
					del++
				}
			}
			if w.Inserted != ins || w.Deleted != del || w.Updated != upd {
				t.Errorf("write %+v, snapshots say inserted %d updated %d deleted %d", w, ins, upd, del)
			}
			if ins == 0 || del == 0 || upd == 0 {
				t.Errorf("the scenario must insert, update and delete: %d/%d/%d", ins, upd, del)
			}
			// Unchanged rows keep their computed_at; written ones get at1.
			var stale, fresh int64
			if err := s.QueryRow(ctx, `SELECT count(*) FILTER (WHERE computed_at=$2), count(*) FILTER (WHERE computed_at=$3)
				FROM release_similar WHERE release_id = ANY($1)`, ids, at0, at1).Scan(&stale, &fresh); err != nil {
				t.Fatal(err)
			}
			if fresh != ins+upd || stale != int64(len(got))-fresh {
				t.Errorf("computed_at: %d rows at night0, %d at night1; want %d written", stale, fresh, ins+upd)
			}
			// A second identical run writes nothing.
			if w2, err := s.SyncReleaseSimilar(ctx, night1, at1.Add(time.Hour), e); err != nil || w2 != (recs.ReleaseSimilarWrite{}) {
				t.Errorf("rerun wrote %+v, %v", w2, err)
			}
			compareSimilar(t, simSnapshot(t, ctx, s, ids), want, e)
		})
	}
	if len(diffs) == 2 {
		if diffs[0].Exact != 0 {
			t.Errorf("eps 0 never needs exact releases: %+v", diffs[0])
		}
		if diffs[1].Updated >= diffs[0].Updated/2 {
			t.Errorf("eps should skip the sub-eps drift: eps 0 %+v, eps %g %+v", diffs[0], eps, diffs[1])
		}
	}
}

// capture is the real store with ReplaceReleaseSimilar recording its input.
type capture struct {
	*store.Store
	rows []recs.ReleaseSimilarRow
}

func (c *capture) ReplaceReleaseSimilar(ctx context.Context, rows []recs.ReleaseSimilarRow, at time.Time) (recs.ReleaseSimilarWrite, error) {
	c.rows = slices.Clone(rows)
	return c.Store.ReplaceReleaseSimilar(ctx, rows, at)
}

// TestReleaseSimilarJobDiffIntegration runs the real similarity job twice
// over a seeded catalog that changes in between (new release, new tags, new
// plays), and compares the table the job's diff write leaves with a full
// rewrite of the same job output.
func TestReleaseSimilarJobDiffIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rng := rand.New(rand.NewPCG(3, 4))
	var ids []int64
	addRelease := func(i int) int64 {
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,maker_name,series_name,release_date)
			VALUES($1,$1,'published',$2,$3,current_date - $4::int) RETURNING id`,
			fmt.Sprintf("simjob-%d-%s", i, sfx), fmt.Sprintf("maker%d-%s", i%5, sfx), fmt.Sprintf("series%d-%s", i%9, sfx), i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for k := range 2 + rng.IntN(4) {
			tag := fmt.Sprintf("tag%d-%s", (i*7+k*3)%17, sfx)
			exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$2) ON CONFLICT DO NOTHING`, id, tag)
		}
		ids = append(ids, id)
		return id
	}
	for i := range 80 {
		addRelease(i)
	}
	users := make([]int64, 12)
	for i := range users {
		m, err := s.Register(ctx, fmt.Sprintf("simjob%d-%s@example.test", i, sfx), "password123", "")
		if err != nil {
			t.Fatal(err)
		}
		users[i] = m["user"].(map[string]any)["id"].(int64)
	}
	watch := func() {
		for _, u := range users {
			for range 6 {
				exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,$3,1500,now() - interval '1 hour')
					ON CONFLICT (user_id,release_id) DO UPDATE SET progress_seconds=EXCLUDED.progress_seconds`, u, ids[rng.IntN(20)], 100+rng.IntN(1400))
			}
		}
	}
	watch()
	c := &capture{Store: s}
	runner := &recs.Runner{Store: c}
	if _, err := runner.RunSimilarity(ctx); err != nil {
		t.Fatal(err)
	}
	// The next night: two new releases, a retagged one, more plays.
	addRelease(80)
	addRelease(81)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$2)`, ids[5], "fresh-"+sfx)
	watch()
	rep, err := runner.RunSimilarity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	w := rep.ReleaseWrite
	t.Logf("second night: %d rows, write %+v", rep.CoWatchRows+rep.ContentRows, w)
	if w.Inserted == 0 {
		t.Errorf("new releases must insert rows: %+v", w)
	}
	got := simSnapshot(t, ctx, s, ids)
	gotOrder := map[int64][]simKey{}
	for _, r := range ids {
		gotOrder[r] = recallOrder(t, ctx, s, r)
	}
	if err := s.RewriteReleaseSimilar(ctx, c.rows, time.Now()); err != nil {
		t.Fatal(err)
	}
	want := simSnapshot(t, ctx, s, ids)
	if len(want) == 0 {
		t.Fatal("the job wrote no rows for the seeded releases")
	}
	compareSimilar(t, got, want, recs.ReleaseSimilarEpsilon)
	// Recall reads the same neighbours in the same order; only exact ties
	// may come back in another order, so compare tie-free releases.
	for _, r := range ids {
		wo := recallOrder(t, ctx, s, r)
		tied := false
		for i := 1; i < len(wo); i++ {
			if want[wo[i]] == want[wo[i-1]] {
				tied = true
			}
		}
		if !tied && !slices.Equal(gotOrder[r], wo) {
			t.Errorf("release %d recall: diff %v, rewrite %v", r, gotOrder[r], wo)
		}
	}
}
