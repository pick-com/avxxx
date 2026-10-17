package recs

import (
	"cmp"
	"slices"
	"strings"
)

// Weights are the performer similarity weights (similarity_weights, admin
// AdminActors / A9). Visual is reserved for V1.1 embeddings and unused.
type Weights struct {
	Metadata float64 `json:"metadata"`
	CoStar   float64 `json:"co_star"`
	CoWatch  float64 `json:"co_watch"`
	Visual   float64 `json:"visual"`
}

// DefaultWeights mirror the migration defaults.
var DefaultWeights = Weights{Metadata: 0.35, CoStar: 0.25, CoWatch: 0.40}

// Validate checks every weight is in [0, 1] and at least one used weight
// (metadata, co_star, co_watch) is positive.
func (w Weights) Validate() string {
	for _, v := range []float64{w.Metadata, w.CoStar, w.CoWatch, w.Visual} {
		if v < 0 || v > 1 || v != v {
			return "weights must be between 0 and 1"
		}
	}
	if w.Metadata+w.CoStar+w.CoWatch <= 0 {
		return "at least one of metadata, co_star, co_watch must be positive"
	}
	return ""
}

// ReleaseDoc is one published release as the offline jobs see it.
type ReleaseDoc struct {
	ID         int64
	Order      int64 // release date as days since epoch (0 when unknown)
	Tags       []string
	Maker      string
	Series     string
	Performers []int64 // active performers, billing order
}

// ReleaseFeatureSets builds content vectors for releases: tags, maker and
// series features.
func ReleaseFeatureSets(docs []ReleaseDoc) []FeatureSet {
	out := make([]FeatureSet, 0, len(docs))
	for _, d := range docs {
		f := map[string]float64{}
		for _, t := range d.Tags {
			f["t:"+t] = 1
		}
		if d.Maker != "" {
			f["m:"+d.Maker] = 1
		}
		if d.Series != "" {
			f["s:"+d.Series] = 1
		}
		out = append(out, FeatureSet{ID: d.ID, Order: d.Order, Features: f})
	}
	return out
}

// ReleaseContentConfig weighs series above maker above a single tag.
var ReleaseContentConfig = ContentConfig{KindWeights: map[string]float64{"t": 1, "m": 1.5, "s": 3}, TopK: 50, MinScore: 0.1}

// ContentReason maps the dominant shared feature kind to a release_similar
// reason.
func ContentReason(kind string) string {
	switch kind {
	case "s":
		return ReasonSeries
	case "m":
		return ReasonMaker
	}
	return ReasonTags
}

// PerformerFeatureSets aggregates tag and maker counts over each performer's
// releases (only performers with at least one release appear). Order is the
// performer's work count, so capped postings compare performers of similar
// output.
func PerformerFeatureSets(docs []ReleaseDoc) []FeatureSet {
	byID := map[int64]*FeatureSet{}
	var ids []int64
	for _, d := range docs {
		for _, p := range d.Performers {
			fs := byID[p]
			if fs == nil {
				fs = &FeatureSet{ID: p, Features: map[string]float64{}}
				byID[p] = fs
				ids = append(ids, p)
			}
			fs.Order++
			for _, t := range d.Tags {
				fs.Features["t:"+t]++
			}
			if d.Maker != "" {
				fs.Features["m:"+d.Maker]++
			}
		}
	}
	slices.Sort(ids)
	out := make([]FeatureSet, len(ids))
	for i, id := range ids {
		out[i] = *byID[id]
	}
	return out
}

// MaxCastForCoStar skips releases with a larger cast (compilations) when
// counting co-star pairs.
const MaxCastForCoStar = 8

// CoStarCounts counts releases shared by each performer pair (a < b).
func CoStarCounts(docs []ReleaseDoc) map[[2]int64]int {
	out := map[[2]int64]int{}
	for _, d := range docs {
		ps := slices.Compact(slices.Sorted(slices.Values(d.Performers)))
		if len(ps) < 2 || len(ps) > MaxCastForCoStar {
			continue
		}
		for i := range ps {
			for j := i + 1; j < len(ps); j++ {
				out[[2]int64{ps[i], ps[j]}]++
			}
		}
	}
	return out
}

// PerformerInteractions lifts release interactions to performers: a user's
// weight for a performer is the maximum over that performer's releases.
func PerformerInteractions(in []Interaction, docs []ReleaseDoc) []Interaction {
	cast := make(map[int64][]int64, len(docs))
	for _, d := range docs {
		cast[d.ID] = d.Performers
	}
	type key struct{ u, p int64 }
	agg := map[key]float64{}
	for _, x := range in {
		for _, p := range cast[x.ItemID] {
			k := key{x.UserID, p}
			agg[k] = max(agg[k], x.Weight)
		}
	}
	out := make([]Interaction, 0, len(agg))
	for k, w := range agg {
		out = append(out, Interaction{UserID: k.u, ItemID: k.p, Weight: w})
	}
	return out
}

// CoStarScore saturates the shared-work count: 1 → 0.33, 2 → 0.5, 6 → 0.75.
func CoStarScore(n int) float64 {
	if n <= 0 {
		return 0
	}
	return float64(n) / float64(n+2)
}

// Evidence is performer_similar.evidence: the parts of the score and what
// the reason text is built from.
type Evidence struct {
	Metadata     float64  `json:"metadata"`
	CoStar       int      `json:"co_star"`
	CoStarScore  float64  `json:"co_star_score"`
	CoWatch      float64  `json:"co_watch"`
	SharedTags   []string `json:"shared_tags,omitempty"`
	SharedMakers []string `json:"shared_makers,omitempty"`
}

// PerformerMatch is one performer_similar row.
type PerformerMatch struct {
	PerformerID int64
	SimilarID   int64
	Score       float64
	Evidence    Evidence
}

// PerformerConfig tunes PerformerSimilarity.
type PerformerConfig struct {
	TopK     int     // default 50
	MinScore float64 // default 0.05
	CoWatch  CoWatchConfig
	Content  ContentConfig
}

// PerformerSimilarity combines, for every performer pair with any signal:
//
//	metadata = TF-IDF cosine over the tags and makers of their works
//	co_star  = CoStarScore(shared works)
//	co_watch = CoWatch over audience (user × performer) interactions
//	score    = (w_m·metadata + w_c·co_star + w_w·co_watch) / (w_m + w_c + w_w)
//
// so score is in [0, 1] (shown as 「92% 相似」). Visual is ignored.
func PerformerSimilarity(docs []ReleaseDoc, audience []Interaction, w Weights, cfg PerformerConfig) []PerformerMatch {
	if w.Validate() != "" {
		w = DefaultWeights
	}
	if cfg.TopK <= 0 {
		cfg.TopK = 50
	}
	if cfg.MinScore <= 0 {
		cfg.MinScore = 0.05
	}
	if cfg.Content.TopK <= 0 {
		cfg.Content.TopK = 100
	}
	if cfg.Content.MaxSharedPerKind <= 0 {
		// The evidence keeps the first 3 tags and 2 makers.
		cfg.Content.MaxSharedPerKind = 3
	}
	type parts struct {
		meta    float64
		costar  int
		cowatch float64
		shared  []string
	}
	// Pairs index into one slice: a map of pointers cost one allocation per
	// pair (millions at 50k performers) and a GC scan of each.
	all := map[[2]int64]int32{}
	var pairs []parts
	get := func(a, b int64) *parts {
		k := [2]int64{a, b}
		i, ok := all[k]
		if !ok {
			i = int32(len(pairs))
			all[k] = i
			pairs = append(pairs, parts{})
		}
		return &pairs[i]
	}
	if w.Metadata > 0 {
		EachContentNeighbors(PerformerFeatureSets(docs), cfg.Content, func(a int64, ms []ContentMatch) {
			for _, m := range ms {
				p := get(a, m.ID)
				p.meta, p.shared = m.Score, m.Shared
			}
		})
	}
	if w.CoStar > 0 {
		for k, n := range CoStarCounts(docs) {
			get(k[0], k[1]).costar = n
			get(k[1], k[0]).costar = n
		}
	}
	if w.CoWatch > 0 {
		cw := cfg.CoWatch
		cw.TopK = max(cw.TopK, 100)
		for _, nb := range CoWatch(audience, cw) {
			get(nb.ItemID, nb.NeighborID).cowatch = nb.Score
		}
	}
	var out []PerformerMatch
	for k, i := range all {
		p := &pairs[i]
		cs := CoStarScore(p.costar)
		score := mixScore(w, p.meta, cs, p.cowatch)
		if score < cfg.MinScore {
			continue
		}
		ev := Evidence{Metadata: round3(p.meta), CoStar: p.costar, CoStarScore: round3(cs), CoWatch: round3(p.cowatch)}
		for _, f := range p.shared {
			kind, name, _ := strings.Cut(f, ":")
			switch {
			case kind == "t" && len(ev.SharedTags) < 3:
				ev.SharedTags = append(ev.SharedTags, name)
			case kind == "m" && len(ev.SharedMakers) < 2:
				ev.SharedMakers = append(ev.SharedMakers, name)
			}
		}
		out = append(out, PerformerMatch{PerformerID: k[0], SimilarID: k[1], Score: min(score, 1), Evidence: ev})
	}
	slices.SortFunc(out, func(a, b PerformerMatch) int {
		if c := cmp.Compare(a.PerformerID, b.PerformerID); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.SimilarID, b.SimilarID)
	})
	kept := out[:0]
	n := 0
	for i, m := range out {
		if i == 0 || m.PerformerID != out[i-1].PerformerID {
			n = 0
		}
		if n < cfg.TopK {
			kept = append(kept, m)
		}
		n++
	}
	return kept
}

// mixScore is the weighted mean of the three similarity parts.
func mixScore(w Weights, meta, coStar, coWatch float64) float64 {
	return (w.Metadata*meta + w.CoStar*coStar + w.CoWatch*coWatch) / (w.Metadata + w.CoStar + w.CoWatch)
}

// ScoreEvidence recomputes a pair's score from its stored evidence under w
// (the same mix as PerformerSimilarity; evidence parts are rounded to three
// decimals). Used to preview weights without rebuilding performer_similar.
func ScoreEvidence(ev Evidence, w Weights) float64 {
	if w.Validate() != "" {
		w = DefaultWeights
	}
	return min(mixScore(w, ev.Metadata, ev.CoStarScore, ev.CoWatch), 1)
}

// StoredNeighbor is one stored neighbour of a performer.
type StoredNeighbor struct {
	ID       int64
	Stored   float64 // performer_similar.score under the saved weights
	Evidence Evidence
}

// Ranked is a candidate scored under preview weights.
type Ranked struct {
	ID         int64   `json:"id"`
	Score      float64 `json:"score"`
	Match      int     `json:"match_percent"`
	Rank       int     `json:"rank"`        // 1-based, under the preview weights
	StoredRank int     `json:"stored_rank"` // 1-based, in the stored order
	Reason     string  `json:"reason"`
}

// Rerank scores cands under w and returns the top k (best first, ties by
// id). Pairs below minScore are dropped, as the nightly job would. Only the
// stored neighbours can be re-ranked: a pair outside the stored top list, or
// a part whose weight was 0 when the job ran (not computed), cannot appear.
func Rerank(cands []StoredNeighbor, w Weights, k int, minScore float64) []Ranked {
	stored := make([]StoredNeighbor, len(cands))
	copy(stored, cands)
	slices.SortFunc(stored, func(a, b StoredNeighbor) int {
		if c := cmp.Compare(b.Stored, a.Stored); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	storedRank := make(map[int64]int, len(stored))
	for i, c := range stored {
		storedRank[c.ID] = i + 1
	}
	out := make([]Ranked, 0, len(cands))
	for _, c := range cands {
		sc := ScoreEvidence(c.Evidence, w)
		if sc < minScore {
			continue
		}
		out = append(out, Ranked{ID: c.ID, Score: round3(sc), Match: int(sc*100 + 0.5), StoredRank: storedRank[c.ID], Reason: PerformerReason(c.Evidence)})
	}
	slices.SortFunc(out, func(a, b Ranked) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if k > 0 && len(out) > k {
		out = out[:k]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

// PerformerReason is the one-line reason for a similar performer (PRD 6.1:
// 「共演 6 部、观众高度重合」): up to two parts, strongest first.
func PerformerReason(ev Evidence) string {
	var parts []string
	if ev.CoStar >= 2 {
		parts = append(parts, "共演 "+itoa(ev.CoStar)+" 部")
	}
	switch {
	case ev.CoWatch >= 0.3:
		parts = append(parts, "观众高度重合")
	case ev.CoWatch >= 0.1:
		parts = append(parts, "观众重合")
	}
	if len(parts) < 2 && ev.Metadata >= 0.2 {
		switch {
		case len(ev.SharedTags) > 0:
			parts = append(parts, "风格相近 · "+ev.SharedTags[0])
		case len(ev.SharedMakers) > 0:
			parts = append(parts, "同厂商 · "+ev.SharedMakers[0])
		}
	}
	if len(parts) == 0 && ev.CoStar == 1 {
		parts = append(parts, "共演 1 部")
	}
	if len(parts) == 0 {
		return "相似演员"
	}
	if len(parts) > 2 {
		parts = parts[:2]
	}
	return strings.Join(parts, "、")
}
