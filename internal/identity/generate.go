package identity

import (
	"sort"
	"unicode/utf8"
)

// Pair is an unordered entity pair stored as (Lo, Hi) with Lo < Hi, so
// (a,b) and (b,a) are the same key.
type Pair struct{ Lo, Hi int64 }

// CanonicalPair orders a and b.
func CanonicalPair(a, b int64) Pair {
	if a > b {
		a, b = b, a
	}
	return Pair{a, b}
}

// Candidate is a scored pair ready to insert into identity_candidates as
// (left_id, right_id) = (Lo, Hi).
type Candidate struct {
	Pair
	Evidence Evidence
}

// Stats summarizes one generation run for one entity type.
type Stats struct {
	Entities            int `json:"entities"`
	Compared            int `json:"compared"`
	Generated           int `json:"generated"`
	Inserted            int `json:"inserted"`
	SkippedExisting     int `json:"skipped_existing"`
	SkippedNotDuplicate int `json:"skipped_not_duplicate"`
	BelowThreshold      int `json:"below_threshold"`
	OversizedBlocks     int `json:"oversized_blocks"`
}

// MaxBlockSize bounds the pairs compared inside one blocking key, so a very
// common alias does not turn generation quadratic. Oversized blocks are
// skipped and counted.
const MaxBlockSize = 200

// Known pairs are skipped before scoring.
type Known struct {
	Existing     map[Pair]bool // any identity_candidates row, any status
	NotDuplicate map[Pair]bool // not_duplicate_pairs
}

// generate compares every pair that shares a blocking key exactly once.
func generate(ids []int64, keys [][]string, known Known, threshold float64, score func(i, j int) Evidence) ([]Candidate, Stats) {
	st := Stats{Entities: len(ids)}
	blocks := map[string][]int{}
	for i, ks := range keys {
		for _, k := range ks {
			blocks[k] = append(blocks[k], i)
		}
	}
	blockKeys := make([]string, 0, len(blocks))
	for k := range blocks {
		blockKeys = append(blockKeys, k)
	}
	sort.Strings(blockKeys)
	seen := map[Pair]bool{}
	var out []Candidate
	for _, k := range blockKeys {
		idx := blocks[k]
		if len(idx) < 2 {
			continue
		}
		if len(idx) > MaxBlockSize {
			st.OversizedBlocks++
			continue
		}
		for x := 0; x < len(idx); x++ {
			for y := x + 1; y < len(idx); y++ {
				i, j := idx[x], idx[y]
				if ids[i] == ids[j] {
					continue
				}
				p := CanonicalPair(ids[i], ids[j])
				if seen[p] {
					continue
				}
				seen[p] = true
				if known.NotDuplicate[p] {
					st.SkippedNotDuplicate++
					continue
				}
				if known.Existing[p] {
					st.SkippedExisting++
					continue
				}
				st.Compared++
				ev := score(i, j)
				if ev.Score < threshold {
					st.BelowThreshold++
					continue
				}
				out = append(out, Candidate{Pair: p, Evidence: ev})
			}
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Evidence.Score != out[b].Evidence.Score {
			return out[a].Evidence.Score > out[b].Evidence.Score
		}
		if out[a].Lo != out[b].Lo {
			return out[a].Lo < out[b].Lo
		}
		return out[a].Hi < out[b].Hi
	})
	st.Generated = len(out)
	return out, st
}

// PerformerCandidates blocks performers on normalized and token-reordered
// names (so birth date or shared releases alone never pair two co-stars) and
// returns pairs scoring at least PerformerThreshold.
func PerformerCandidates(ps []Performer, known Known) ([]Candidate, Stats) {
	ids := make([]int64, len(ps))
	keys := make([][]string, len(ps))
	for i, p := range ps {
		ids[i] = p.ID
		exact, reordered := nameKeys(p.Names)
		for k := range exact {
			keys[i] = append(keys[i], "n:"+k)
		}
		for k := range reordered {
			keys[i] = append(keys[i], "r:"+k)
		}
	}
	return generate(ids, keys, known, PerformerThreshold, func(i, j int) Evidence { return ScorePerformers(ps[i], ps[j]) })
}

// minTitleKeyRunes keeps very short titles out of title blocking.
const minTitleKeyRunes = 4

// ReleaseCandidates blocks releases on product-code keys and exact normalized
// titles, and returns pairs scoring at least ReleaseThreshold. A title-only
// match can become a candidate but never scores high enough on its own to look
// like a code match.
func ReleaseCandidates(rs []Release, known Known) ([]Candidate, Stats) {
	ids := make([]int64, len(rs))
	keys := make([][]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
		_, loose := codeKeys(r.Codes)
		for k := range loose {
			keys[i] = append(keys[i], "c:"+k)
		}
		for _, t := range r.Titles {
			if k := NormalizeName(t); utf8.RuneCountInString(k) >= minTitleKeyRunes {
				keys[i] = append(keys[i], "t:"+k)
			}
		}
	}
	return generate(ids, keys, known, ReleaseThreshold, func(i, j int) Evidence { return ScoreReleases(rs[i], rs[j]) })
}
