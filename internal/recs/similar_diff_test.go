package recs

import (
	"math"
	"math/rand/v2"
	"testing"
)

// applyDiff plays a SimilarDiff's plan onto a stored table.
func applyDiff(table map[similarKey]float32, d *SimilarDiff) {
	for _, r := range d.Delete {
		delete(table, similarKey{r.SimilarID, r.Reason})
	}
	for _, r := range d.Upsert {
		table[similarKey{r.SimilarID, r.Reason}] = float32(r.Score)
	}
}

// checkEquivalent checks the SimilarDiff contract for one release: same
// pairs, scores within eps, strict new order kept.
func checkEquivalent(t *testing.T, table map[similarKey]float32, next []ReleaseSimilarRow, eps float64) {
	t.Helper()
	if len(table) != len(next) {
		t.Fatalf("table has %d rows, want %d", len(table), len(next))
	}
	for _, a := range next {
		ga, ok := table[similarKey{a.SimilarID, a.Reason}]
		if !ok {
			t.Fatalf("%v missing", a)
		}
		na := float32(a.Score)
		if eps == 0 && ga != na {
			t.Fatalf("%v: stored %v, want exactly %v", a, ga, na)
		}
		if math.Abs(float64(ga)-float64(na)) > eps {
			t.Fatalf("%v: stored %v drifts more than %g from %v", a, ga, eps, na)
		}
		for _, b := range next {
			if na > float32(b.Score) && !(ga > table[similarKey{b.SimilarID, b.Reason}]) {
				t.Fatalf("order: new %v > %v but stored %v, %v", na, float32(b.Score), ga, table[similarKey{b.SimilarID, b.Reason}])
			}
		}
	}
}

func storedOf(table map[similarKey]float32) []StoredSimilar {
	var out []StoredSimilar
	for k, v := range table {
		out = append(out, StoredSimilar{k.id, k.reason, v})
	}
	return out
}

// TestSimilarDiffNights runs random releases through many nights of drift
// (mostly below eps, some above, dense near-ties, new and vanished pairs),
// checks the contract after each, that the counts match the plan, and that
// replanning the same night writes nothing.
func TestSimilarDiffNights(t *testing.T) {
	reasons := []string{ReasonCoWatch, ReasonSeries, ReasonMaker, ReasonTags}
	for _, eps := range []float64{0, 1e-4, ReleaseSimilarEpsilon, 0.05} {
		rng := rand.New(rand.NewPCG(7, uint64(eps*1e6)))
		for rel := int64(1); rel <= 200; rel++ {
			table := map[similarKey]float32{}
			score := map[similarKey]float64{}
			for range rng.IntN(60) {
				k := similarKey{rng.Int64N(500) + 1000, reasons[rng.IntN(4)]}
				// Clustered scores: many neighbours within 1e-4 of another.
				score[k] = math.Round(rng.Float64()*200) / 200 * (1 + rng.Float64()*1e-4)
			}
			for night := range 8 {
				var next []ReleaseSimilarRow
				for k, v := range score {
					switch p := rng.Float64(); {
					case p < 0.03:
						delete(score, k)
						continue
					case p < 0.1:
						v = rng.Float64()
					case p < 0.9:
						v = max(0, v+(rng.Float64()*2-1)*eps*0.6)
					}
					score[k] = v
					next = append(next, ReleaseSimilarRow{ReleaseID: rel, SimilarID: k.id, Score: v, Reason: k.reason})
				}
				if night%3 == 1 {
					k := similarKey{rng.Int64N(500) + 1000, reasons[rng.IntN(4)]}
					if _, ok := score[k]; !ok {
						score[k] = rng.Float64()
						next = append(next, ReleaseSimilarRow{ReleaseID: rel, SimilarID: k.id, Score: score[k], Reason: k.reason})
					}
				}
				d := &SimilarDiff{Eps: eps}
				before := len(table)
				d.Release(rel, next, storedOf(table))
				applyDiff(table, d)
				checkEquivalent(t, table, next, eps)
				if w := d.Write; int(w.Inserted+w.Updated) != len(d.Upsert) || int(w.Deleted) != len(d.Delete) ||
					before+int(w.Inserted)-int(w.Deleted) != len(table) {
					t.Fatalf("eps %g release %d night %d: counts %+v, %d upserts, %d deletes", eps, rel, night, w, len(d.Upsert), len(d.Delete))
				}
				for _, r := range d.Upsert {
					if r.ReleaseID != rel {
						t.Fatalf("upsert for release %d", r.ReleaseID)
					}
				}
				again := &SimilarDiff{Eps: eps}
				again.Release(rel, next, storedOf(table))
				if len(again.Upsert)+len(again.Delete) != 0 {
					t.Fatalf("eps %g release %d night %d: replanning writes %+v", eps, rel, night, again.Write)
				}
			}
		}
	}
}

// TestSimilarDiffSavesWrites: sub-eps drift writes nothing, eps 0 rewrites
// every changed score.
func TestSimilarDiffSavesWrites(t *testing.T) {
	stored := []StoredSimilar{{2, ReasonTags, 0.9}, {3, ReasonTags, 0.5}, {4, ReasonCoWatch, 0.1}}
	next := []ReleaseSimilarRow{{1, 2, 0.9004, ReasonTags}, {1, 3, 0.4995, ReasonTags}, {1, 4, 0.1001, ReasonCoWatch}}
	d := &SimilarDiff{Eps: ReleaseSimilarEpsilon}
	d.Release(1, next, stored)
	if len(d.Upsert)+len(d.Delete) != 0 {
		t.Errorf("sub-eps drift wrote %+v", d.Write)
	}
	d = &SimilarDiff{}
	d.Release(1, next, stored)
	if d.Write.Updated != 3 {
		t.Errorf("eps 0: %+v", d.Write)
	}
}

// TestSimilarDiffOrder: two neighbours swap by less than eps; keeping both
// stored scores would keep the old order, so the second is lowered below
// the first (by one ulp) instead.
func TestSimilarDiffOrder(t *testing.T) {
	stored := []StoredSimilar{{2, ReasonTags, 0.5}, {3, ReasonTags, 0.50004}}
	next := []ReleaseSimilarRow{{1, 2, 0.50006, ReasonTags}, {1, 3, 0.50002, ReasonTags}}
	d := &SimilarDiff{Eps: ReleaseSimilarEpsilon}
	d.Release(1, next, stored)
	if d.Write.Updated != 1 || d.Write.Exact != 0 || len(d.Upsert) != 1 || d.Upsert[0].SimilarID != 3 {
		t.Fatalf("plan %+v %+v", d.Write, d.Upsert)
	}
	if got := float32(d.Upsert[0].Score); got != math.Nextafter32(0.5, 0) {
		t.Errorf("3 stored as %v, want just below 0.5", got)
	}
	table := map[similarKey]float32{{2, ReasonTags}: 0.5, {3, ReasonTags}: 0.50004}
	applyDiff(table, d)
	checkEquivalent(t, table, next, ReleaseSimilarEpsilon)
}

// TestSimilarDiffExactFallback: with no room below a kept score (it is 0),
// the release is written with exact scores.
func TestSimilarDiffExactFallback(t *testing.T) {
	stored := []StoredSimilar{{2, ReasonCoWatch, 0}}
	next := []ReleaseSimilarRow{{1, 2, 0.3, ReasonCoWatch}, {1, 3, 0.2, ReasonCoWatch}}
	d := &SimilarDiff{Eps: 0.5}
	d.Release(1, next, stored)
	if d.Write.Exact != 1 || d.Write.Inserted != 1 || d.Write.Updated != 1 {
		t.Fatalf("plan %+v", d.Write)
	}
	table := map[similarKey]float32{{2, ReasonCoWatch}: 0}
	applyDiff(table, d)
	checkEquivalent(t, table, next, 0)
}
