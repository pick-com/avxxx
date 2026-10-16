// Package recs is recommendations V1 (PRD 4.1, 4.2, 6.1, 6.2, 10, 18; tech
// plan 3.5): rules plus item co-watch, no vectors (V1.1).
//
// It has three layers, like internal/audit:
//   - pure offline math (this file, trending.go): completion-weighted item
//     co-watch, TF-IDF content neighbours, performer similarity and trending
//     scores, all over plain structs;
//   - a Runner (runner.go) that loads inputs through the narrow Store
//     interface and replaces the precomputed tables; the cmd/audit worker runs
//     it for the 'recs_similarity' (nightly) and 'recs_trending' (hourly) jobs;
//   - the pure online pipeline (rank.go) behind GET /api/recs: merge the
//     recall channels, score, filter, re-rank for diversity, attach reasons.
//
// The PostgreSQL side lives in internal/store/recs.go.
package recs

import (
	"cmp"
	"math"
	"slices"
	"strings"
)

// Interaction is one user's engagement with one item (a release, or a
// performer for audience similarity). Weight is in (0, 1].
type Interaction struct {
	UserID int64
	ItemID int64
	Weight float64
}

// CompletionWeight turns a watch ratio (progress / duration) into an
// interaction weight: a started play counts 0.2, a finished one 1. A negative
// ratio means the duration is unknown and counts as half watched.
func CompletionWeight(ratio float64) float64 {
	if ratio < 0 || math.IsNaN(ratio) {
		ratio = 0.5
	}
	return 0.2 + 0.8*min(ratio, 1)
}

// CoWatchConfig tunes CoWatch. Zero fields take the defaults.
type CoWatchConfig struct {
	MinSupport      int     // co-watching users a pair needs, default 3
	Shrink          float64 // support shrinkage s/(s+Shrink), default 5
	TopK            int     // neighbours kept per item, default 50
	MaxItemsPerUser int     // heaviest items kept per user, default 200
}

// DefaultCoWatch is the configuration used for zero fields.
var DefaultCoWatch = CoWatchConfig{MinSupport: 3, Shrink: 5, TopK: 50, MaxItemsPerUser: 200}

func (c CoWatchConfig) withDefaults() CoWatchConfig {
	d := DefaultCoWatch
	if c.MinSupport > 0 {
		d.MinSupport = c.MinSupport
	}
	if c.Shrink > 0 {
		d.Shrink = c.Shrink
	}
	if c.TopK > 0 {
		d.TopK = c.TopK
	}
	if c.MaxItemsPerUser > 0 {
		d.MaxItemsPerUser = c.MaxItemsPerUser
	}
	return d
}

// Neighbor is one directed similarity row.
type Neighbor struct {
	ItemID     int64
	NeighborID int64
	Score      float64
	Support    int // co-watching users (CoWatch only)
}

type pair struct{ a, b int64 }

type pairAgg struct {
	sum     float64
	support int
}

// CoWatch is item-based collaborative filtering ("看过 A 的人也看了 B"):
//
//	f_u      = 1 / log2(2 + n_u)                   (heavy users count less)
//	N_a      = Σ_u f_u · w_ua
//	co(a,b)  = Σ_u f_u · min(w_ua, w_ub)
//	sim(a,b) = co / sqrt(N_a · N_b) · s / (s + Shrink)
//
// where w is the completion weight and s the number of users who watched
// both. sim is in [0, 1). Pairs below MinSupport are dropped; each item keeps
// its TopK neighbours, best first (ties by neighbour id).
func CoWatch(in []Interaction, cfg CoWatchConfig) []Neighbor {
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
	pairs := map[pair]*pairAgg{}
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
				k := pair{min(a.id, b.id), max(a.id, b.id)}
				p := pairs[k]
				if p == nil {
					p = &pairAgg{}
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

// TopKPerItem keeps the k best neighbours of each item, ordered by item,
// then score descending, then neighbour id.
func TopKPerItem(in []Neighbor, k int) []Neighbor {
	slices.SortFunc(in, func(a, b Neighbor) int {
		if c := cmp.Compare(a.ItemID, b.ItemID); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.NeighborID, b.NeighborID)
	})
	out := in[:0]
	n := 0
	for i, x := range in {
		if i == 0 || x.ItemID != in[i-1].ItemID {
			n = 0
		}
		if n < k {
			out = append(out, x)
		}
		n++
	}
	return out
}

// FeatureSet is one entity's sparse feature vector for content similarity.
// Feature keys carry a kind prefix ("t:" tag, "m:" maker, "s:" series);
// values are term frequencies. Order positions the entity inside large
// posting lists (see ContentConfig.PostingCap).
type FeatureSet struct {
	ID       int64
	Order    int64
	Features map[string]float64
}

// ContentConfig tunes ContentNeighbors. Zero fields take the defaults.
type ContentConfig struct {
	// KindWeights multiplies features by kind prefix ("t", "m", "s").
	// Missing kinds weigh 1.
	KindWeights map[string]float64
	// PostingCap bounds the work per feature: an entity is only compared
	// with the PostingCap entities nearest to it (by Order) among those
	// sharing the feature, so a maker with 10 000 works costs no more than
	// one with 200. Default 200.
	PostingCap int
	TopK       int     // default 50
	MinScore   float64 // default 0.05
}

func (c ContentConfig) withDefaults() ContentConfig {
	if c.PostingCap <= 0 {
		c.PostingCap = 200
	}
	if c.TopK <= 0 {
		c.TopK = 50
	}
	if c.MinScore <= 0 {
		c.MinScore = 0.05
	}
	return c
}

// ContentMatch is one content neighbour with the shared features that
// contributed most (kind-prefixed keys, biggest first).
type ContentMatch struct {
	ID     int64
	Score  float64
	Shared []string
	Kind   string // prefix of the biggest shared feature: "t", "m" or "s"
}

// ContentNeighbors returns, per entity id, its TopK most similar entities by
// cosine over TF-IDF vectors (idf = ln(1 + N/df), times the kind weight).
func ContentNeighbors(sets []FeatureSet, cfg ContentConfig) map[int64][]ContentMatch {
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
			ms[k].Shared, ms[k].Kind = sharedFeatures(vecs[i], vecs[index[ms[k].ID]])
		}
		out[sets[i].ID] = ms
	}
	return out
}

func kindOf(f string) string {
	k, _, _ := strings.Cut(f, ":")
	return k
}

// sharedFeatures lists the features of a and b by contribution a[f]*b[f],
// biggest first, and the kind of the biggest.
func sharedFeatures(a, b map[string]float64) ([]string, string) {
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
