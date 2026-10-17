package store_test

import (
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

type perfKey struct{ performer, similar int64 }

type perfRow struct {
	score    float32
	evidence string // jsonb text: equal text is equal jsonb
}

// perfSnapshot reads performer_similar rows of the given performers.
func perfSnapshot(t *testing.T, ctx context.Context, s *store.Store, ids []int64) map[perfKey]perfRow {
	t.Helper()
	rows, err := s.Query(ctx, `SELECT performer_id, similar_id, score, evidence::text FROM performer_similar WHERE performer_id = ANY($1)`, ids)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[perfKey]perfRow{}
	for rows.Next() {
		var k perfKey
		var v perfRow
		if err := rows.Scan(&k.performer, &k.similar, &v.score, &v.evidence); err != nil {
			t.Fatal(err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// perfReads is what the readers see for each performer: SimilarPerformers
// (ORDER BY score DESC, id) as ids and reason texts.
func perfReads(t *testing.T, ctx context.Context, s *store.Store, ids []int64) map[int64][]string {
	t.Helper()
	out := map[int64][]string{}
	for _, id := range ids {
		recsOf, err := s.SimilarPerformers(ctx, id, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range recsOf {
			out[id] = append(out[id], fmt.Sprintf("%d:%s", r.ID, recs.PerformerReason(*r.Evidence)))
		}
	}
	return out
}

// comparePerf checks that got (the diff write) is equivalent to want (a
// full rewrite of the same rows): the same pairs; at eps 0 bit-identical
// scores and identical evidence; else scores within eps and evidence that
// recs.SameEvidence accepts.
func comparePerf(t *testing.T, got, want map[perfKey]perfRow, eps float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("rows: diff %d, rewrite %d", len(got), len(want))
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%v missing from the diff write", k)
			continue
		}
		if eps == 0 {
			if math.Float32bits(g.score) != math.Float32bits(w.score) || g.evidence != w.evidence {
				t.Errorf("%v: diff %+v, rewrite %+v (eps 0 must be exact)", k, g, w)
			}
			continue
		}
		if d := math.Abs(float64(g.score) - float64(w.score)); d > eps {
			t.Errorf("%v: score diff %v, rewrite %v: |Δ| %g > eps %g", k, g.score, w.score, d, eps)
		}
		var ge, we recs.Evidence
		if json.Unmarshal([]byte(g.evidence), &ge) != nil || json.Unmarshal([]byte(w.evidence), &we) != nil || !recs.SameEvidence(ge, we, eps) {
			t.Errorf("%v: evidence diff %s, rewrite %s", k, g.evidence, w.evidence)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%v only in the diff write", k)
		}
	}
}

// TestPerformerSimilarDiffIntegration proves SyncPerformerSimilar (the
// nightly diff write) leaves performer_similar equivalent to
// RewritePerformerSimilar (DELETE + COPY) on the same input: synthetic
// nights with score and evidence drift below and above eps, changed tags
// and co-star counts, new and vanished pairs, a swap by less than eps and
// exact ties (broken by similar_id), at eps 0 (bit-identical) and at
// recs.PerformerSimilarEpsilon.
func TestPerformerSimilarDiffIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	const n = 50
	ids := make([]int64, n)
	for i := range ids {
		if err := s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$1) RETURNING id`,
			fmt.Sprintf("perfdiff-%d-%s", i, sfx)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	rng := rand.New(rand.NewPCG(5, 6))
	tags := []string{"巨乳", "美少女", "人妻", "OL"}
	r3 := func(x float64) float64 { return math.Round(x*1000) / 1000 }
	evidence := func() recs.Evidence {
		ev := recs.Evidence{Metadata: r3(rng.Float64()), CoStar: rng.IntN(4), CoWatch: r3(rng.Float64() * 0.5)}
		ev.CoStarScore = r3(recs.CoStarScore(ev.CoStar))
		for _, tg := range tags[:rng.IntN(3)] {
			ev.SharedTags = append(ev.SharedTags, tg)
		}
		if rng.IntN(3) == 0 {
			ev.SharedMakers = []string{"S1"}
		}
		return ev
	}
	var night0 []recs.PerformerMatch
	seen := map[perfKey]bool{}
	for i, p := range ids {
		for range 3 + rng.IntN(25) {
			k := perfKey{p, ids[(i+1+rng.IntN(n-1))%n]}
			if seen[k] {
				continue
			}
			seen[k] = true
			// Coarse scores: exact ties are common, as for co-star-only pairs.
			night0 = append(night0, recs.PerformerMatch{PerformerID: k.performer, SimilarID: k.similar, Score: math.Round(rng.Float64()*40) / 40, Evidence: evidence()})
		}
	}
	eps := recs.PerformerSimilarEpsilon
	var night1 []recs.PerformerMatch
	for _, x := range night0 {
		x.Evidence.SharedTags = slices.Clone(x.Evidence.SharedTags)
		switch p := rng.Float64(); {
		case p < 0.05: // vanished
			continue
		case p < 0.12:
			x.Score, x.Evidence = rng.Float64(), evidence()
		case p < 0.75: // a night's drift: sub-eps score, one rounding step of evidence
			x.Score = min(1, max(0, x.Score+(rng.Float64()*2-1)*eps*0.9))
			x.Evidence.Metadata = max(0, r3(x.Evidence.Metadata+float64(rng.IntN(3)-1)/1000))
			x.Evidence.CoWatch = max(0, r3(x.Evidence.CoWatch+float64(rng.IntN(3)-1)/1000))
		case p < 0.8: // a new shared tag in front
			x.Evidence.SharedTags = append([]string{"新人"}, x.Evidence.SharedTags...)
		case p < 0.85: // one more co-star work
			x.Evidence.CoStar++
			x.Evidence.CoStarScore = r3(recs.CoStarScore(x.Evidence.CoStar))
		}
		night1 = append(night1, x)
	}
	for i, p := range ids {
		if i%3 == 0 {
			k := perfKey{p, ids[(i+n/2)%n]}
			if !seen[k] {
				night1 = append(night1, recs.PerformerMatch{PerformerID: k.performer, SimilarID: k.similar, Score: rng.Float64(), Evidence: evidence()})
			}
		}
	}
	// A performer whose two neighbours swap by less than eps each, and two
	// exact ties whose order is the similar_id.
	flip := ids[0]
	for _, rows := range []*[]recs.PerformerMatch{&night0, &night1} {
		*rows = slices.DeleteFunc(*rows, func(x recs.PerformerMatch) bool { return x.PerformerID == flip })
	}
	ev := recs.Evidence{Metadata: 0.3, CoStar: 1, CoStarScore: 0.333}
	night0 = append(night0,
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[1], Score: 0.50000, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[2], Score: 0.50004, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[4], Score: 0.25, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[3], Score: 0.25, Evidence: ev})
	night1 = append(night1,
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[1], Score: 0.50006, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[2], Score: 0.50002, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[4], Score: 0.25, Evidence: ev},
		recs.PerformerMatch{PerformerID: flip, SimilarID: ids[3], Score: 0.2502, Evidence: ev})

	at0, at1 := time.Now().Add(-24*time.Hour), time.Now()
	rewrite := func(rows []recs.PerformerMatch, at time.Time) (map[perfKey]perfRow, map[int64][]string) {
		t.Helper()
		if err := s.RewritePerformerSimilar(ctx, slices.Clone(rows), at); err != nil {
			t.Fatal(err)
		}
		return perfSnapshot(t, ctx, s, ids), perfReads(t, ctx, s, ids)
	}
	want, wantReads := rewrite(night1, at1)
	before, _ := rewrite(night0, at0)

	var diffs []recs.ReleaseSimilarWrite
	for _, e := range []float64{0, eps} {
		t.Run(fmt.Sprint("eps=", e), func(t *testing.T) {
			rewrite(night0, at0)
			w, err := s.SyncPerformerSimilar(ctx, slices.Clone(night1), at1, e)
			if err != nil {
				t.Fatal(err)
			}
			diffs = append(diffs, w)
			got := perfSnapshot(t, ctx, s, ids)
			comparePerf(t, got, want, e)
			// The readers see the same neighbours in the same order (ties by
			// id included) with the same reason texts.
			gotReads := perfReads(t, ctx, s, ids)
			for _, p := range ids {
				if !slices.Equal(gotReads[p], wantReads[p]) {
					t.Errorf("performer %d reads: diff %v, rewrite %v", p, gotReads[p], wantReads[p])
				}
			}
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
			var stale, fresh int64
			if err := s.QueryRow(ctx, `SELECT count(*) FILTER (WHERE computed_at=$2), count(*) FILTER (WHERE computed_at=$3)
				FROM performer_similar WHERE performer_id = ANY($1)`, ids, at0, at1).Scan(&stale, &fresh); err != nil {
				t.Fatal(err)
			}
			if fresh != ins+upd || stale != int64(len(got))-fresh {
				t.Errorf("computed_at: %d rows at night0, %d at night1; want %d written", stale, fresh, ins+upd)
			}
			if w2, err := s.SyncPerformerSimilar(ctx, slices.Clone(night1), at1.Add(time.Hour), e); err != nil || w2 != (recs.ReleaseSimilarWrite{}) {
				t.Errorf("rerun wrote %+v, %v", w2, err)
			}
			comparePerf(t, perfSnapshot(t, ctx, s, ids), want, e)
		})
	}
	if len(diffs) == 2 {
		t.Logf("eps 0: %+v, eps %g: %+v", diffs[0], eps, diffs[1])
		if diffs[0].Exact != 0 {
			t.Errorf("eps 0 never needs exact performers: %+v", diffs[0])
		}
		if diffs[1].Updated >= diffs[0].Updated/2 {
			t.Errorf("eps should skip the sub-eps drift: eps 0 %+v, eps %g %+v", diffs[0], eps, diffs[1])
		}
	}

	// Stored evidence that does not decode as an Evidence (a key the struct
	// lacks) is rewritten even though the score is unchanged.
	k := night1[0]
	if _, err := s.Exec(ctx, `UPDATE performer_similar SET evidence = evidence || '{"legacy":1}' WHERE performer_id=$1 AND similar_id=$2`, k.PerformerID, k.SimilarID); err != nil {
		t.Fatal(err)
	}
	if w, err := s.SyncPerformerSimilar(ctx, slices.Clone(night1), at1, eps); err != nil || w != (recs.ReleaseSimilarWrite{Updated: 1}) {
		t.Errorf("legacy evidence: write %+v, %v; want one update", w, err)
	}
	comparePerf(t, perfSnapshot(t, ctx, s, ids), want, eps)
}

// perfCapture is the real store with ReplacePerformerSimilar recording its
// input.
type perfCapture struct {
	*store.Store
	rows []recs.PerformerMatch
}

func (c *perfCapture) ReplacePerformerSimilar(ctx context.Context, rows []recs.PerformerMatch, at time.Time) (recs.ReleaseSimilarWrite, error) {
	c.rows = slices.Clone(rows)
	return c.Store.ReplacePerformerSimilar(ctx, rows, at)
}

// TestPerformerSimilarJobDiffIntegration runs the real similarity job twice
// over a seeded catalog with performers that changes in between (new
// releases and casts, new tags, more plays), and compares the
// performer_similar table the diff write leaves with a full rewrite of the
// same job output.
func TestPerformerSimilarJobDiffIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	rng := rand.New(rand.NewPCG(9, 10))
	perfs := make([]int64, 30)
	for i := range perfs {
		if err := s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$1) RETURNING id`,
			fmt.Sprintf("perfjob-%d-%s", i, sfx)).Scan(&perfs[i]); err != nil {
			t.Fatal(err)
		}
	}
	var rels []int64
	addRelease := func(i int) {
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,maker_name,release_date)
			VALUES($1,$1,'published',$2,current_date - $3::int) RETURNING id`,
			fmt.Sprintf("perfjob-r%d-%s", i, sfx), fmt.Sprintf("maker%d-%s", i%4, sfx), i).Scan(&id); err != nil {
			t.Fatal(err)
		}
		for k := range 2 + rng.IntN(3) {
			exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$2) ON CONFLICT DO NOTHING`, id, fmt.Sprintf("tag%d-%s", (i*5+k*3)%11, sfx))
		}
		for k := range 1 + rng.IntN(3) {
			exec(`INSERT INTO release_performers(release_id,performer_id,billing_order) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, perfs[(i+k*7)%len(perfs)], k)
		}
		rels = append(rels, id)
	}
	for i := range 90 {
		addRelease(i)
	}
	users := make([]int64, 10)
	for i := range users {
		m, err := s.Register(ctx, fmt.Sprintf("perfjob%d-%s@example.test", i, sfx), "password123", "")
		if err != nil {
			t.Fatal(err)
		}
		users[i] = m["user"].(map[string]any)["id"].(int64)
	}
	watch := func() {
		for _, u := range users {
			for range 8 {
				exec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,$3,1500,now() - interval '1 hour')
					ON CONFLICT (user_id,release_id) DO UPDATE SET progress_seconds=EXCLUDED.progress_seconds`, u, rels[rng.IntN(len(rels))], 100+rng.IntN(1400))
			}
		}
	}
	watch()
	c := &perfCapture{Store: s}
	runner := &recs.Runner{Store: c}
	if _, err := runner.RunSimilarity(ctx); err != nil {
		t.Fatal(err)
	}
	addRelease(90)
	addRelease(91)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$2)`, rels[3], "fresh-"+sfx)
	watch()
	rep, err := runner.RunSimilarity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("second night: %d performer rows, write %+v", rep.PerformerRows, rep.PerformerWrite)
	got, gotReads := perfSnapshot(t, ctx, s, perfs), perfReads(t, ctx, s, perfs)
	if err := s.RewritePerformerSimilar(ctx, c.rows, time.Now()); err != nil {
		t.Fatal(err)
	}
	want, wantReads := perfSnapshot(t, ctx, s, perfs), perfReads(t, ctx, s, perfs)
	if len(want) == 0 {
		t.Fatal("the job wrote no rows for the seeded performers")
	}
	comparePerf(t, got, want, recs.PerformerSimilarEpsilon)
	for _, p := range perfs {
		if !slices.Equal(gotReads[p], wantReads[p]) {
			t.Errorf("performer %d reads: diff %v, rewrite %v", p, gotReads[p], wantReads[p])
		}
	}
}
