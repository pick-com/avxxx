package recs

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

// randomInteractions: skewed users × items, with duplicates, zero weights and
// a few heavy users past MaxItemsPerUser.
func randomInteractions(r *rand.Rand, users, items, perUser int) []Interaction {
	var in []Interaction
	for u := int64(1); u <= int64(users); u++ {
		k := 1 + r.IntN(perUser)
		if u%97 == 0 {
			k = 3 * perUser
		}
		for range k {
			it := 1 + int64(float64(items)*math.Pow(r.Float64(), 2))
			w := CompletionWeight(r.Float64()*1.2 - 0.1)
			if r.IntN(50) == 0 {
				w = 0
			}
			in = append(in, Interaction{u, it, w})
		}
	}
	return in
}

func TestCoWatchMatchesReference(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		r := rand.New(rand.NewPCG(seed, 7))
		in := randomInteractions(r, 400, 300, 25)
		for _, cfg := range []CoWatchConfig{{}, {MinSupport: 1, TopK: 5, MaxItemsPerUser: 10}, {MinSupport: 2, Shrink: 1}} {
			got, want := CoWatch(in, cfg), coWatchRef(in, cfg)
			// Same float operations in the same order: bit-identical.
			if !slices.Equal(got, want) {
				t.Fatalf("seed %d cfg %+v: %d rows, reference %d; first diff %s", seed, cfg, len(got), len(want), firstDiff(got, want))
			}
		}
	}
	if got := CoWatch(nil, CoWatchConfig{}); len(got) != 0 {
		t.Errorf("empty input: %v", got)
	}
}

func firstDiff(a, b []Neighbor) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return fmt.Sprintf("#%d %+v vs %+v", i, a[i], b[i])
		}
	}
	return "length"
}

func randomFeatureSets(r *rand.Rand, n int) []FeatureSet {
	sets := make([]FeatureSet, n)
	for i := range sets {
		f := map[string]float64{}
		for range 1 + r.IntN(6) {
			f[fmt.Sprintf("t:%d", int(40*math.Pow(r.Float64(), 2)))]++
		}
		f[fmt.Sprintf("m:%d", r.IntN(15))] = 1
		if r.IntN(3) == 0 {
			f[fmt.Sprintf("s:%d", r.IntN(60))] = 1
		}
		if r.IntN(20) == 0 {
			f["t:zero"] = 0
		}
		sets[i] = FeatureSet{ID: int64(1000 + i), Order: int64(r.IntN(n / 2)), Features: f}
	}
	return sets
}

func TestContentNeighborsMatchesReference(t *testing.T) {
	for seed := uint64(1); seed <= 3; seed++ {
		sets := randomFeatureSets(rand.New(rand.NewPCG(seed, 11)), 600)
		// TopK above any candidate count, so float noise in the last bits
		// cannot move a neighbour across the cut.
		for _, cfg := range []ContentConfig{
			{KindWeights: ReleaseContentConfig.KindWeights, TopK: 1000, MinScore: 0.1},
			{PostingCap: 20, TopK: 1000},
		} {
			got, want := ContentNeighbors(sets, cfg), contentNeighborsRef(sets, cfg)
			if len(got) != len(want) {
				t.Fatalf("seed %d: %d entities, reference %d", seed, len(got), len(want))
			}
			for id, w := range want {
				g := got[id]
				if len(g) != len(w) {
					t.Fatalf("seed %d id %d: %d matches, reference %d", seed, id, len(g), len(w))
				}
				byID := map[int64]ContentMatch{}
				for _, m := range g {
					byID[m.ID] = m
				}
				for _, m := range w {
					x, ok := byID[m.ID]
					if !ok || math.Abs(x.Score-m.Score) > 1e-12 || x.Kind != m.Kind || !slices.Equal(x.Shared, m.Shared) {
						t.Fatalf("seed %d id %d: %+v, reference %+v", seed, id, x, m)
					}
				}
			}
		}
	}
}

func TestContentNeighborsTopKAndKindOnly(t *testing.T) {
	sets := randomFeatureSets(rand.New(rand.NewPCG(5, 5)), 800)
	cfg := ReleaseContentConfig
	full := ContentNeighbors(sets, cfg)
	cfg.KindOnly = true
	kinds := ContentNeighbors(sets, cfg)
	ref := contentNeighborsRef(sets, ReleaseContentConfig)
	for id, ms := range full {
		// Exactly TopK slots: no candidate-sized backing array is retained
		// (that kept ~20 GB live at 300k releases).
		if cap(ms) != len(ms) || len(ms) > cfg.TopK {
			t.Fatalf("id %d: len %d cap %d", id, len(ms), cap(ms))
		}
		if len(ms) != len(ref[id]) {
			t.Fatalf("id %d: %d matches, reference %d", id, len(ms), len(ref[id]))
		}
		for k, m := range ms {
			// Ties at the TopK cut may differ in the last float bits; the
			// scores must agree.
			if math.Abs(m.Score-ref[id][k].Score) > 1e-12 {
				t.Fatalf("id %d #%d: %+v, reference %+v", id, k, m, ref[id][k])
			}
			km := kinds[id][k]
			if km.Shared != nil || km.ID != m.ID || km.Score != m.Score || km.Kind != m.Kind {
				t.Fatalf("KindOnly id %d #%d: %+v vs %+v", id, k, km, m)
			}
		}
	}
}

func TestEachContentNeighborsMatchesMap(t *testing.T) {
	sets := randomFeatureSets(rand.New(rand.NewPCG(8, 1)), 500)
	want := ContentNeighbors(sets, ReleaseContentConfig)
	last, n := -1, 0
	pos := map[int64]int{}
	for i, s := range sets {
		pos[s.ID] = i
	}
	EachContentNeighbors(sets, ReleaseContentConfig, func(id int64, ms []ContentMatch) {
		if pos[id] <= last {
			t.Fatalf("emit out of sets order at %d", id)
		}
		last = pos[id]
		n++
		w := want[id]
		if len(ms) != len(w) {
			t.Fatalf("id %d: %d matches, map %d", id, len(ms), len(w))
		}
		for k := range ms {
			if ms[k].ID != w[k].ID || ms[k].Score != w[k].Score || ms[k].Kind != w[k].Kind || !slices.Equal(ms[k].Shared, w[k].Shared) {
				t.Fatalf("id %d #%d: %+v, map %+v", id, k, ms[k], w[k])
			}
		}
	})
	if n != len(want) {
		t.Errorf("emitted %d entities, map has %d", n, len(want))
	}
}

func TestPerformerEvidenceWithSharedCap(t *testing.T) {
	r := rand.New(rand.NewPCG(21, 3))
	var docs []ReleaseDoc
	for i := int64(1); i <= 1500; i++ {
		var tags []string
		for range 2 + r.IntN(6) {
			tags = append(tags, fmt.Sprintf("tag%d", int(60*math.Pow(r.Float64(), 2))))
		}
		slices.Sort(tags)
		cast := []int64{1 + int64(200*math.Pow(r.Float64(), 2))}
		if r.IntN(3) == 0 {
			cast = append(cast, 1+int64(r.IntN(200)))
		}
		docs = append(docs, ReleaseDoc{ID: i, Order: i, Tags: slices.Compact(tags), Maker: fmt.Sprintf("M%d", r.IntN(12)), Performers: cast})
	}
	audience := PerformerInteractions(randomInteractions(r, 300, 1500, 20), docs)
	capped := PerformerSimilarity(docs, audience, DefaultWeights, PerformerConfig{})
	full := PerformerSimilarity(docs, audience, DefaultWeights, PerformerConfig{Content: ContentConfig{MaxSharedPerKind: 1 << 30}})
	if len(capped) == 0 || len(capped) != len(full) {
		t.Fatalf("%d rows capped, %d uncapped", len(capped), len(full))
	}
	for i := range full {
		a, b := capped[i], full[i]
		if a.PerformerID != b.PerformerID || a.SimilarID != b.SimilarID || a.Score != b.Score ||
			!slices.Equal(a.Evidence.SharedTags, b.Evidence.SharedTags) || !slices.Equal(a.Evidence.SharedMakers, b.Evidence.SharedMakers) ||
			a.Evidence.Metadata != b.Evidence.Metadata || a.Evidence.CoStar != b.Evidence.CoStar || a.Evidence.CoWatch != b.Evidence.CoWatch {
			t.Fatalf("#%d: %+v, uncapped %+v", i, a, b)
		}
	}
}

func BenchmarkContentNeighbors(b *testing.B) {
	sets := randomFeatureSets(rand.New(rand.NewPCG(9, 9)), 5000)
	b.ReportAllocs()
	for b.Loop() {
		ContentNeighbors(sets, ReleaseContentConfig)
	}
}
