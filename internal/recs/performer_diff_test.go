package recs

import (
	"cmp"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
)

func TestSameEvidence(t *testing.T) {
	base := Evidence{Metadata: 0.412, CoStar: 2, CoStarScore: 0.5, CoWatch: 0.123, SharedTags: []string{"a", "b"}, SharedMakers: []string{"m"}}
	with := func(f func(*Evidence)) Evidence {
		e := base
		e.SharedTags = slices.Clone(base.SharedTags)
		e.SharedMakers = slices.Clone(base.SharedMakers)
		f(&e)
		return e
	}
	for _, c := range []struct {
		name         string
		a, b         Evidence
		want, wantE0 bool
	}{
		{"identical", base, base, true, true},
		{"one rounding step", base, with(func(e *Evidence) { e.Metadata, e.CoWatch = 0.413, 0.122 }), true, false},
		{"two steps", base, with(func(e *Evidence) { e.Metadata = 0.414 }), false, false},
		{"co_star", base, with(func(e *Evidence) { e.CoStar, e.CoStarScore = 3, 0.6 }), false, false},
		{"tag order", base, with(func(e *Evidence) { e.SharedTags = []string{"b", "a"} }), false, false},
		{"tag added", base, with(func(e *Evidence) { e.SharedTags = append(e.SharedTags, "c") }), false, false},
		{"maker gone", base, with(func(e *Evidence) { e.SharedMakers = nil }), false, false},
		// nil and [] marshal the same (omitempty).
		{"empty is absent", with(func(e *Evidence) { e.SharedMakers = nil }), with(func(e *Evidence) { e.SharedMakers = []string{} }), true, true},
		// co_watch 0.1 is a reason threshold: one step across it changes
		// the text, so it is not the same evidence.
		{"reason threshold",
			with(func(e *Evidence) { e.CoStar, e.CoStarScore, e.CoWatch = 0, 0, 0.099 }),
			with(func(e *Evidence) { e.CoStar, e.CoStarScore, e.CoWatch = 0, 0, 0.1 }), false, false},
	} {
		if got := SameEvidence(c.a, c.b, PerformerSimilarEpsilon); got != c.want {
			t.Errorf("%s: SameEvidence eps %g = %v, want %v", c.name, PerformerSimilarEpsilon, got, c.want)
		}
		if got := SameEvidence(c.a, c.b, 0); got != c.wantE0 {
			t.Errorf("%s: SameEvidence eps 0 = %v, want %v", c.name, got, c.wantE0)
		}
	}
}

type perfTable map[int64]StoredPerformerSimilar

func applyPerf(table perfTable, d *PerformerSimilarDiff) {
	for _, r := range d.Delete {
		delete(table, r.SimilarID)
	}
	for _, r := range d.Upsert {
		table[r.SimilarID] = StoredPerformerSimilar{SimilarID: r.SimilarID, Score: float32(r.Score), Evidence: r.Evidence, EvidenceOK: true}
	}
	d.Upsert, d.Delete = d.Upsert[:0], d.Delete[:0]
}

// readOrder is what the readers see: ORDER BY score DESC, similar_id.
func readOrder(table perfTable) []int64 {
	var out []int64
	for id := range table {
		out = append(out, id)
	}
	slices.SortFunc(out, func(a, b int64) int {
		if c := cmp.Compare(table[b].Score, table[a].Score); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return out
}

// TestPerformerSimilarDiffNights runs random performers through nights of
// drift in score and evidence and checks the contract after each: same
// pairs, scores within eps, the readers' order (ties by id included), the
// evidence rule, bit-identical at eps 0, and nothing written on a replan.
func TestPerformerSimilarDiffNights(t *testing.T) {
	for _, eps := range []float64{0, PerformerSimilarEpsilon, 0.05} {
		rng := rand.New(rand.NewPCG(11, uint64(eps*1e6)))
		var exact int64
		for perf := int64(1); perf <= 200; perf++ {
			table := perfTable{}
			cur := map[int64]PerformerMatch{}
			for range rng.IntN(50) {
				id := rng.Int64N(300) + 1000
				// Coarse scores: many exact ties, broken by id.
				cur[id] = PerformerMatch{PerformerID: perf, SimilarID: id, Score: math.Round(rng.Float64()*50) / 50,
					Evidence: Evidence{Metadata: round3(rng.Float64()), CoStar: rng.IntN(3), CoWatch: round3(rng.Float64() * 0.5), SharedTags: []string{"t1"}}}
			}
			d := &PerformerSimilarDiff{Eps: eps}
			for night := range 8 {
				var next []PerformerMatch
				for id, m := range cur {
					switch p := rng.Float64(); {
					case p < 0.03:
						delete(cur, id)
						continue
					case p < 0.1:
						m.Score = rng.Float64()
					case p < 0.8:
						m.Score = max(0, m.Score+(rng.Float64()*2-1)*eps*0.6)
						m.Evidence.Metadata = max(0, round3(m.Evidence.Metadata+float64(rng.IntN(3)-1)/1000))
					case p < 0.85:
						m.Evidence.SharedTags = []string{"t2", "t1"}
					}
					m.Evidence.CoStarScore = round3(CoStarScore(m.Evidence.CoStar))
					cur[id] = m
					next = append(next, m)
				}
				if night > 0 && rng.IntN(2) == 0 {
					id := rng.Int64N(300) + 2000
					if _, ok := cur[id]; !ok {
						m := PerformerMatch{PerformerID: perf, SimilarID: id, Score: rng.Float64()}
						cur[id] = m
						next = append(next, m)
					}
				}
				rng.Shuffle(len(next), func(i, j int) { next[i], next[j] = next[j], next[i] })
				var stored []StoredPerformerSimilar
				for _, s := range table {
					stored = append(stored, s)
				}
				d.Performer(perf, next, stored)
				applyPerf(table, d)

				want := perfTable{}
				for _, m := range next {
					want[m.SimilarID] = StoredPerformerSimilar{SimilarID: m.SimilarID, Score: float32(m.Score), Evidence: m.Evidence, EvidenceOK: true}
				}
				if len(table) != len(want) {
					t.Fatalf("eps %g perf %d night %d: %d rows, want %d", eps, perf, night, len(table), len(want))
				}
				for id, w := range want {
					g := table[id]
					if eps == 0 && !reflect.DeepEqual(g, w) {
						t.Fatalf("eps 0: %+v, want exactly %+v", g, w)
					}
					if math.Abs(float64(g.Score)-float64(w.Score)) > eps || !SameEvidence(g.Evidence, w.Evidence, eps) {
						t.Fatalf("eps %g: stored %+v, new %+v", eps, g, w)
					}
				}
				if got, w := readOrder(table), readOrder(want); !slices.Equal(got, w) {
					t.Fatalf("eps %g perf %d night %d: order %v, want %v", eps, perf, night, got, w)
				}
				// Replanning the same night writes nothing.
				stored = stored[:0]
				for _, s := range table {
					stored = append(stored, s)
				}
				before := d.Write
				d.Performer(perf, next, stored)
				if len(d.Upsert)+len(d.Delete) != 0 || d.Write.Inserted+d.Write.Updated+d.Write.Deleted != before.Inserted+before.Updated+before.Deleted {
					t.Fatalf("replan wrote %d/%d", len(d.Upsert), len(d.Delete))
				}
			}
			exact += d.Write.Exact
		}
		if eps == 0 && exact != 0 {
			t.Errorf("eps 0 never needs exact performers, got %d", exact)
		}
	}
}

// TestPerformerSimilarDiffBadEvidence: stored evidence that did not decode
// is rewritten even when the score is unchanged.
func TestPerformerSimilarDiffBadEvidence(t *testing.T) {
	m := PerformerMatch{PerformerID: 1, SimilarID: 2, Score: 0.5, Evidence: Evidence{CoStar: 1}}
	d := &PerformerSimilarDiff{Eps: PerformerSimilarEpsilon}
	d.Performer(1, []PerformerMatch{m}, []StoredPerformerSimilar{{SimilarID: 2, Score: 0.5, Evidence: m.Evidence}})
	if len(d.Upsert) != 1 || d.Write.Updated != 1 {
		t.Fatalf("undecoded evidence not rewritten: %+v", d.Write)
	}
	d = &PerformerSimilarDiff{Eps: PerformerSimilarEpsilon}
	d.Performer(1, []PerformerMatch{m}, []StoredPerformerSimilar{{SimilarID: 2, Score: 0.5, Evidence: m.Evidence, EvidenceOK: true}})
	if len(d.Upsert) != 0 {
		t.Fatalf("unchanged row rewritten: %+v", d.Upsert)
	}
}
