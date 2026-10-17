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
//
// Memory is O(interactions + items): it walks one item at a time, summing
// its co-watch row into dense per-item arrays (users in ascending id order,
// so the float sums do not depend on map order), and keeps only that item's
// TopK. A map of every co-watched pair (the previous approach) grew to
// ~50M entries and several GB at 200k users.
func CoWatch(in []Interaction, cfg CoWatchConfig) []Neighbor {
	cfg = cfg.withDefaults()
	xs := make([]Interaction, 0, len(in))
	for _, x := range in {
		if x.Weight <= 0 || x.UserID == 0 || x.ItemID == 0 {
			continue
		}
		x.Weight = min(x.Weight, 1)
		xs = append(xs, x)
	}
	if len(xs) == 0 {
		return nil
	}
	slices.SortFunc(xs, func(a, b Interaction) int {
		if c := cmp.Compare(a.UserID, b.UserID); c != 0 {
			return c
		}
		return cmp.Compare(a.ItemID, b.ItemID)
	})
	// Per user (ascending id): the deduplicated items, heaviest first, cut
	// to MaxItemsPerUser; flattened into kept with userStart offsets.
	type iw struct {
		id int64
		w  float64
	}
	var kept []iw
	var userStart []int
	var userF []float64
	items := []iw{}
	for i := 0; i < len(xs); {
		u := xs[i].UserID
		items = items[:0]
		for ; i < len(xs) && xs[i].UserID == u; i++ {
			if n := len(items); n > 0 && items[n-1].id == xs[i].ItemID {
				items[n-1].w = max(items[n-1].w, xs[i].Weight)
				continue
			}
			items = append(items, iw{xs[i].ItemID, xs[i].Weight})
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
		userStart = append(userStart, len(kept))
		userF = append(userF, 1/math.Log2(2+float64(len(items))))
		kept = append(kept, items...)
	}
	userStart = append(userStart, len(kept))
	xs = nil

	// Dense item indexes (ascending id) and each item's viewers.
	ids := make([]int64, len(kept))
	for k, x := range kept {
		ids[k] = x.id
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	idx := make(map[int64]int32, len(ids))
	for k, id := range ids {
		idx[id] = int32(k)
	}
	type entry struct {
		ref int32 // user index (in viewers) or item index (in rows)
		w   float64
	}
	rows := make([]entry, len(kept)) // kept, with dense item indexes
	count := make([]int32, len(ids)+1)
	norm := make([]float64, len(ids))
	for u := range userF {
		for k := userStart[u]; k < userStart[u+1]; k++ {
			it := idx[kept[k].id]
			rows[k] = entry{it, kept[k].w}
			norm[it] += userF[u] * kept[k].w
			count[it+1]++
		}
	}
	kept, idx = nil, nil
	for k := 1; k < len(count); k++ {
		count[k] += count[k-1]
	}
	viewers := make([]entry, len(rows))
	fill := slices.Clone(count[:len(ids)])
	for u := range userF {
		for k := userStart[u]; k < userStart[u+1]; k++ {
			it := rows[k].ref
			viewers[fill[it]] = entry{int32(u), rows[k].w}
			fill[it]++
		}
	}
	fill = nil

	sum := make([]float64, len(ids))
	support := make([]int32, len(ids))
	var touched []int32
	var cand []Neighbor
	var out []Neighbor
	for a := range ids {
		touched = touched[:0]
		for _, v := range viewers[count[a]:count[a+1]] {
			f := userF[v.ref]
			for _, r := range rows[userStart[v.ref]:userStart[v.ref+1]] {
				b := r.ref
				if int(b) == a {
					continue
				}
				if support[b] == 0 {
					touched = append(touched, b)
				}
				sum[b] += f * min(v.w, r.w)
				support[b]++
			}
		}
		cand = cand[:0]
		for _, b := range touched {
			s, n := sum[b], int(support[b])
			sum[b], support[b] = 0, 0
			if n < cfg.MinSupport {
				continue
			}
			d := math.Sqrt(norm[min(a, int(b))] * norm[max(a, int(b))])
			if d == 0 {
				continue
			}
			cand = append(cand, Neighbor{ids[a], ids[b], s / d * float64(n) / (float64(n) + cfg.Shrink), n})
		}
		slices.SortFunc(cand, func(x, y Neighbor) int {
			if c := cmp.Compare(y.Score, x.Score); c != 0 {
				return c
			}
			return cmp.Compare(x.NeighborID, y.NeighborID)
		})
		out = append(out, cand[:min(len(cand), cfg.TopK)]...)
	}
	return out
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
	// KindOnly leaves ContentMatch.Shared nil and fills only Kind. The
	// release job stores just the reason, and 300k releases × TopK shared
	// lists are about a GB of garbage.
	KindOnly bool
	// MaxSharedPerKind keeps at most this many shared features of each kind
	// in ContentMatch.Shared (still biggest first); 0 keeps all. Performers
	// share hundreds of tags, and the evidence shows three.
	MaxSharedPerKind int
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

// featureWeight is one entry of a normalized sparse vector: feature id,
// weight and the entity's position in that feature's posting list (-1 when
// the feature has no posting list, df = 1).
type featureWeight struct {
	f   int32
	pos int32
	w   float64
}

// posting is one entity in a feature's posting list with its weight there.
type posting struct {
	i int32
	w float64
}

// ContentNeighbors returns, per entity id, its TopK most similar entities by
// cosine over TF-IDF vectors (idf = ln(1 + N/df), times the kind weight).
// Each entity's slice holds exactly its matches (no spare capacity).
func ContentNeighbors(sets []FeatureSet, cfg ContentConfig) map[int64][]ContentMatch {
	out := make(map[int64][]ContentMatch, len(sets))
	EachContentNeighbors(sets, cfg, func(id int64, ms []ContentMatch) {
		top := make([]ContentMatch, len(ms))
		copy(top, ms)
		out[id] = top
	})
	return out
}

// EachContentNeighbors is ContentNeighbors without the result map: it calls
// emit once per entity that has matches, in sets order. ms is reused after
// emit returns, so emit must copy what it keeps.
//
// Features are interned to ints and the per-entity scores accumulate in a
// dense array, so the inner loop does no map lookups.
func EachContentNeighbors(sets []FeatureSet, cfg ContentConfig, emit func(id int64, ms []ContentMatch)) {
	cfg = cfg.withDefaults()
	n := len(sets)
	// Feature ids in name order: vectors sorted by id sum in a fixed order,
	// so scores (and ties at the TopK cut) do not depend on map order.
	fid := map[string]int32{}
	var names []string
	for _, s := range sets {
		for f, v := range s.Features {
			if _, ok := fid[f]; v > 0 && !ok {
				fid[f] = 0
				names = append(names, f)
			}
		}
	}
	slices.Sort(names)
	for k, f := range names {
		fid[f] = int32(k)
	}
	df := make([]int, len(names))
	for _, s := range sets {
		for f, v := range s.Features {
			if v > 0 {
				df[fid[f]]++
			}
		}
	}
	kindW := make([]float64, len(names))
	for k, f := range names {
		kindW[k] = 1
		if kw, ok := cfg.KindWeights[kindOf(f)]; ok {
			kindW[k] = kw
		}
	}
	// Normalized vectors, sorted by feature id.
	vecs := make([][]featureWeight, n)
	for i, s := range sets {
		v := make([]featureWeight, 0, len(s.Features))
		for f, tf := range s.Features {
			if tf <= 0 {
				continue
			}
			id := fid[f]
			if w := tf * math.Log(1+float64(n)/float64(df[id])) * kindW[id]; w > 0 {
				v = append(v, featureWeight{f: id, pos: -1, w: w})
			}
		}
		slices.SortFunc(v, func(a, b featureWeight) int { return cmp.Compare(a.f, b.f) })
		var sq float64
		for _, x := range v {
			sq += x.w * x.w
		}
		if sq > 0 {
			l := math.Sqrt(sq)
			for k := range v {
				v[k].w /= l
			}
		}
		vecs[i] = v
	}
	// Posting lists sorted by (Order, ID).
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
	postings := make([][]posting, len(names))
	for _, i := range order {
		for k, fw := range vecs[i] {
			if df[fw.f] > 1 {
				vecs[i][k].pos = int32(len(postings[fw.f]))
				postings[fw.f] = append(postings[fw.f], posting{int32(i), fw.w})
			}
		}
	}
	index := make(map[int64]int, n)
	for i, s := range sets {
		index[s.ID] = i
	}
	acc := make([]float64, n)
	seen := make([]bool, n)
	var touched []int32
	var ms []ContentMatch
	var scratch []sharedContribution
	for i := range sets {
		touched = touched[:0]
		for _, fw := range vecs[i] {
			p := postings[fw.f]
			if len(p) > cfg.PostingCap {
				k := int(fw.pos)
				lo := max(0, k-cfg.PostingCap/2)
				hi := min(len(p), lo+cfg.PostingCap+1)
				lo = max(0, hi-cfg.PostingCap-1)
				p = p[lo:hi]
			}
			for _, e := range p {
				if int(e.i) == i {
					continue
				}
				if !seen[e.i] {
					seen[e.i] = true
					touched = append(touched, e.i)
				}
				acc[e.i] += fw.w * e.w
			}
		}
		ms = ms[:0]
		for _, j := range touched {
			if sc := acc[j]; sc >= cfg.MinScore {
				ms = append(ms, ContentMatch{ID: sets[j].ID, Score: min(sc, 1)})
			}
			acc[j], seen[j] = 0, false
		}
		if len(ms) == 0 {
			continue
		}
		slices.SortFunc(ms, func(a, b ContentMatch) int {
			if c := cmp.Compare(b.Score, a.Score); c != 0 {
				return c
			}
			return cmp.Compare(a.ID, b.ID)
		})
		top := ms[:min(len(ms), cfg.TopK)]
		for k := range top {
			top[k].Shared, top[k].Kind = sharedFeatures(vecs[i], vecs[index[top[k].ID]], names, cfg, &scratch)
		}
		emit(sets[i].ID, top)
	}
}

func kindOf(f string) string {
	k, _, _ := strings.Cut(f, ":")
	return k
}

// sharedContribution is one shared feature and its contribution a[f]*b[f].
type sharedContribution struct {
	f string
	c float64
}

// sharedFeatures lists the features of a and b (sorted by feature id) by
// contribution a[f]*b[f], biggest first (ties by name), and the kind of the
// biggest. cfg.KindOnly returns only the kind; cfg.MaxSharedPerKind caps the
// list per kind. buf is reused scratch space.
func sharedFeatures(a, b []featureWeight, names []string, cfg ContentConfig, buf *[]sharedContribution) ([]string, string) {
	xs := (*buf)[:0]
	var best sharedContribution
	found := false
	for x, y := 0, 0; x < len(a) && y < len(b); {
		switch {
		case a[x].f < b[y].f:
			x++
		case a[x].f > b[y].f:
			y++
		default:
			e := sharedContribution{names[a[x].f], a[x].w * b[y].w}
			if cfg.KindOnly {
				if !found || e.c > best.c || (e.c == best.c && e.f < best.f) {
					best, found = e, true
				}
			} else {
				xs = append(xs, e)
			}
			x++
			y++
		}
	}
	*buf = xs
	if cfg.KindOnly {
		if !found {
			return nil, ""
		}
		return nil, kindOf(best.f)
	}
	if len(xs) == 0 {
		return nil, ""
	}
	slices.SortFunc(xs, func(x, y sharedContribution) int {
		if c := cmp.Compare(y.c, x.c); c != 0 {
			return c
		}
		return strings.Compare(x.f, y.f)
	})
	var perKind map[string]int
	if cfg.MaxSharedPerKind > 0 {
		perKind = map[string]int{}
	}
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if perKind != nil {
			k := kindOf(x.f)
			if perKind[k] >= cfg.MaxSharedPerKind {
				continue
			}
			perKind[k]++
		}
		out = append(out, x.f)
	}
	if perKind != nil && len(out) < len(xs) {
		out = slices.Clone(out) // do not keep the len(xs) array alive
	}
	return out, kindOf(xs[0].f)
}
