package recs

// Reference copies of the map-based CoWatch and ContentNeighbors as they were
// before the 2026-10-17 full-scale pass. similarity_perf_test.go checks that
// the optimised versions return the same neighbours and scores.

import (
	"cmp"
	"math"
	"slices"
	"strings"
)

type refPair struct{ a, b int64 }

type refPairAgg struct {
	sum     float64
	support int
}

// coWatchRef is the map-based CoWatch:
//
//	f_u      = 1 / log2(2 + n_u)                   (heavy users count less)
//	N_a      = Σ_u f_u · w_ua
//	co(a,b)  = Σ_u f_u · min(w_ua, w_ub)
//	sim(a,b) = co / sqrt(N_a · N_b) · s / (s + Shrink)
//
// where w is the completion weight and s the number of users who watched
// both. sim is in [0, 1). Pairs below MinSupport are dropped; each item keeps
// its TopK neighbours, best first (ties by neighbour id).
func coWatchRef(in []Interaction, cfg CoWatchConfig) []Neighbor {
	cfg = cfg.withDefaults()
	byUser := map[int64]map[int64]float64{}
	for _, x := range in {
		if x.Weight <= 0 || x.UserID == 0 || x.ItemID == 0 {
			continue
		}
		m := byUser[x.UserID]
		if m == nil {
			m = map[int64]float64{}
			byUser[x.UserID] = m
		}
		m[x.ItemID] = max(m[x.ItemID], min(x.Weight, 1))
	}
	norm := map[int64]float64{}
	pairs := map[refPair]*refPairAgg{}
	type iw struct {
		id int64
		w  float64
	}
	users := make([]int64, 0, len(byUser))
	for u := range byUser {
		users = append(users, u)
	}
	slices.Sort(users) // deterministic float summation
	for _, u := range users {
		m := byUser[u]
		items := make([]iw, 0, len(m))
		for id, w := range m {
			items = append(items, iw{id, w})
		}
		slices.SortFunc(items, func(a, b iw) int {
			if c := cmp.Compare(b.w, a.w); c != 0 {
				return c
			}
			return cmp.Compare(a.id, b.id)
		})
		if len(items) > cfg.MaxItemsPerUser {
			items = items[:cfg.MaxItemsPerUser]
		}
		f := 1 / math.Log2(2+float64(len(items)))
		for _, it := range items {
			norm[it.id] += f * it.w
		}
		for i := range items {
			for j := i + 1; j < len(items); j++ {
				a, b := items[i], items[j]
				k := refPair{min(a.id, b.id), max(a.id, b.id)}
				p := pairs[k]
				if p == nil {
					p = &refPairAgg{}
					pairs[k] = p
				}
				p.sum += f * min(a.w, b.w)
				p.support++
			}
		}
	}
	var out []Neighbor
	for k, p := range pairs {
		if p.support < cfg.MinSupport {
			continue
		}
		d := math.Sqrt(norm[k.a] * norm[k.b])
		if d == 0 {
			continue
		}
		s := p.sum / d * float64(p.support) / (float64(p.support) + cfg.Shrink)
		out = append(out, Neighbor{k.a, k.b, s, p.support}, Neighbor{k.b, k.a, s, p.support})
	}
	return TopKPerItem(out, cfg.TopK)
}

func contentNeighborsRef(sets []FeatureSet, cfg ContentConfig) map[int64][]ContentMatch {
	cfg = cfg.withDefaults()
	n := len(sets)
	df := map[string]int{}
	for _, s := range sets {
		for f, v := range s.Features {
			if v > 0 {
				df[f]++
			}
		}
	}
	// Normalized vectors.
	vecs := make([]map[string]float64, n)
	for i, s := range sets {
		v := make(map[string]float64, len(s.Features))
		var sq float64
		for f, tf := range s.Features {
			if tf <= 0 {
				continue
			}
			w := tf * math.Log(1+float64(n)/float64(df[f]))
			if kw, ok := cfg.KindWeights[kindOf(f)]; ok {
				w *= kw
			}
			if w > 0 {
				v[f] = w
				sq += w * w
			}
		}
		if sq > 0 {
			l := math.Sqrt(sq)
			for f := range v {
				v[f] /= l
			}
		}
		vecs[i] = v
	}
	// Posting lists sorted by (Order, ID); pos[f][i] is i's index in f's list.
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		if c := cmp.Compare(sets[a].Order, sets[b].Order); c != 0 {
			return c
		}
		return cmp.Compare(sets[a].ID, sets[b].ID)
	})
	postings := map[string][]int{}
	for _, i := range order {
		for f := range vecs[i] {
			if df[f] > 1 {
				postings[f] = append(postings[f], i)
			}
		}
	}
	pos := map[string]map[int]int{}
	for f, p := range postings {
		if len(p) > cfg.PostingCap {
			m := make(map[int]int, len(p))
			for k, i := range p {
				m[i] = k
			}
			pos[f] = m
		}
	}
	index := make(map[int64]int, n)
	for i, s := range sets {
		index[s.ID] = i
	}
	out := make(map[int64][]ContentMatch, n)
	acc := map[int]float64{}
	for i := range sets {
		clear(acc)
		for f, w := range vecs[i] {
			p := postings[f]
			if len(p) > cfg.PostingCap {
				k := pos[f][i]
				lo := max(0, k-cfg.PostingCap/2)
				hi := min(len(p), lo+cfg.PostingCap+1)
				lo = max(0, hi-cfg.PostingCap-1)
				p = p[lo:hi]
			}
			for _, j := range p {
				if j != i {
					acc[j] += w * vecs[j][f]
				}
			}
		}
		ms := make([]ContentMatch, 0, len(acc))
		for j, sc := range acc {
			if sc >= cfg.MinScore {
				ms = append(ms, ContentMatch{ID: sets[j].ID, Score: min(sc, 1)})
			}
		}
		slices.SortFunc(ms, func(a, b ContentMatch) int {
			if c := cmp.Compare(b.Score, a.Score); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		if len(ms) > cfg.TopK {
			ms = ms[:cfg.TopK]
		}
		if len(ms) == 0 {
			continue
		}
		for k := range ms {
			ms[k].Shared, ms[k].Kind = sharedFeaturesRef(vecs[i], vecs[index[ms[k].ID]])
		}
		out[sets[i].ID] = ms
	}
	return out
}

// sharedFeatures lists the features of a and b by contribution a[f]*b[f],
// biggest first, and the kind of the biggest.
func sharedFeaturesRef(a, b map[string]float64) ([]string, string) {
	type fc struct {
		f string
		c float64
	}
	var xs []fc
	for f, w := range a {
		if v, ok := b[f]; ok {
			xs = append(xs, fc{f, w * v})
		}
	}
	slices.SortFunc(xs, func(x, y fc) int {
		if c := cmp.Compare(y.c, x.c); c != 0 {
			return c
		}
		return strings.Compare(x.f, y.f)
	})
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = x.f
	}
	if len(xs) == 0 {
		return nil, ""
	}
	return out, kindOf(xs[0].f)
}
