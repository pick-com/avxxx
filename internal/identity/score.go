package identity

import (
	"math"
	"sort"
)

// Entity types, matching the CHECK constraints in the catalog schema.
const (
	PerformerType = "performer"
	ReleaseType   = "release"
)

// Scoring weights. A score is the clamped sum of the weights of the rules
// that fired; negative weights are counter-evidence.
const (
	// Performers.
	WeightNameMatch       = 0.50 // a normalized name or alias is shared
	WeightNameReordered   = 0.35 // only a token-reordered name is shared
	WeightBirthDateMatch  = 0.30 // both birth dates known and equal
	WeightBirthDateDiffer = -0.50
	WeightSharedRelease   = 0.10 // per shared release
	MaxSharedRelease      = 0.20

	// Releases.
	WeightCodeMatch      = 0.60 // strict product-code key shared
	WeightCodeLooseMatch = 0.45 // only the loose (label-prefix-less) key shared
	WeightCodeDiffer     = -0.50
	WeightTitle          = 0.25 // scaled by title similarity when >= MinTitleSimilarity
	MinTitleSimilarity   = 0.5
	WeightRuntimeMatch   = 0.10 // runtimes within 2%
	WeightRuntimeDiffer  = -0.15
	WeightSharedPerf     = 0.05 // per shared performer
	MaxSharedPerf        = 0.15

	// Candidates below these scores are not stored.
	PerformerThreshold = 0.50
	ReleaseThreshold   = 0.40
)

// Rule is one piece of evidence that contributed to a score.
type Rule struct {
	Rule   string  `json:"rule"`
	Weight float64 `json:"weight"`
	Detail any     `json:"detail,omitempty"`
}

// Evidence is stored as identity_candidates.evidence.
type Evidence struct {
	Score float64 `json:"score"`
	Rules []Rule  `json:"rules"`
}

func (e *Evidence) add(rule string, w float64, detail any) {
	e.Rules = append(e.Rules, Rule{Rule: rule, Weight: round4(w), Detail: detail})
	e.Score += w
}

func (e *Evidence) finish() Evidence {
	e.Score = round4(math.Max(0, math.Min(1, e.Score)))
	if e.Rules == nil {
		e.Rules = []Rule{}
	}
	return *e
}

func round4(f float64) float64 { return math.Round(f*1e4) / 1e4 }

// Performer is the feature vector of one canonical performer.
type Performer struct {
	ID         int64
	Names      []string // canonical name, Japanese name and aliases
	BirthDate  string   // YYYY-MM-DD or ""
	ReleaseIDs []int64
}

// Release is the feature vector of one canonical release.
type Release struct {
	ID             int64
	Codes          []string // raw identifier values
	Titles         []string // canonical title, Japanese title
	RuntimeMinutes int
	PerformerIDs   []int64
}

func nameKeys(names []string) (exact, reordered map[string]bool) {
	exact, reordered = map[string]bool{}, map[string]bool{}
	for _, n := range names {
		if k := NormalizeName(n); k != "" {
			exact[k] = true
		}
		if k := ReorderedNameKey(n); k != "" {
			reordered[k] = true
		}
	}
	return exact, reordered
}

func codeKeys(codes []string) (strict, loose map[string]bool) {
	strict, loose = map[string]bool{}, map[string]bool{}
	for _, c := range codes {
		ks := CodeKeys(c)
		for i, k := range ks {
			if i == 0 {
				strict[k] = true
			}
			loose[k] = true
		}
	}
	return strict, loose
}

func intersect[K comparable](a, b map[K]bool) []K {
	var out []K
	for k := range a {
		if b[k] {
			out = append(out, k)
		}
	}
	return out
}

func idSet(ids []int64) map[int64]bool {
	m := make(map[int64]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func sortedStrings(s []string) []string { sort.Strings(s); return s }

// ScorePerformers scores how likely a and b are the same person.
func ScorePerformers(a, b Performer) Evidence {
	var e Evidence
	ae, ar := nameKeys(a.Names)
	be, br := nameKeys(b.Names)
	if shared := intersect(ae, be); len(shared) > 0 {
		e.add("name_match", WeightNameMatch, sortedStrings(shared))
	} else if shared := intersect(ar, br); len(shared) > 0 {
		e.add("name_reordered", WeightNameReordered, sortedStrings(shared))
	}
	if a.BirthDate != "" && b.BirthDate != "" {
		if a.BirthDate == b.BirthDate {
			e.add("birth_date_match", WeightBirthDateMatch, a.BirthDate)
		} else {
			e.add("birth_date_differ", WeightBirthDateDiffer, []string{a.BirthDate, b.BirthDate})
		}
	}
	if n := len(intersect(idSet(a.ReleaseIDs), idSet(b.ReleaseIDs))); n > 0 {
		e.add("shared_releases", math.Min(MaxSharedRelease, WeightSharedRelease*float64(n)), n)
	}
	return e.finish()
}

// ScoreReleases scores how likely a and b are the same product.
func ScoreReleases(a, b Release) Evidence {
	var e Evidence
	as, al := codeKeys(a.Codes)
	bs, bl := codeKeys(b.Codes)
	if shared := intersect(as, bs); len(shared) > 0 {
		e.add("code_match", WeightCodeMatch, sortedStrings(shared))
	} else if shared := intersect(al, bl); len(shared) > 0 {
		e.add("code_loose_match", WeightCodeLooseMatch, sortedStrings(shared))
	} else if len(as) > 0 && len(bs) > 0 {
		e.add("code_differ", WeightCodeDiffer, nil)
	}
	best := 0.0
	for _, ta := range a.Titles {
		for _, tb := range b.Titles {
			best = math.Max(best, TitleSimilarity(ta, tb))
		}
	}
	if best >= MinTitleSimilarity {
		e.add("title_similarity", WeightTitle*best, round4(best))
	}
	if a.RuntimeMinutes > 0 && b.RuntimeMinutes > 0 {
		hi := math.Max(float64(a.RuntimeMinutes), float64(b.RuntimeMinutes))
		diff := math.Abs(float64(a.RuntimeMinutes-b.RuntimeMinutes)) / hi
		switch {
		case diff <= 0.02:
			e.add("runtime_match", WeightRuntimeMatch, []int{a.RuntimeMinutes, b.RuntimeMinutes})
		case diff > 0.10:
			e.add("runtime_differ", WeightRuntimeDiffer, []int{a.RuntimeMinutes, b.RuntimeMinutes})
		}
	}
	if n := len(intersect(idSet(a.PerformerIDs), idSet(b.PerformerIDs))); n > 0 {
		e.add("shared_performers", math.Min(MaxSharedPerf, WeightSharedPerf*float64(n)), n)
	}
	return e.finish()
}
