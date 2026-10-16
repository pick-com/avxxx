package search

// SuggestLimit is the maximum number of typed suggestions.
const SuggestLimit = 8

// Suggestion types, in display order (MobileSearchTyping 分类型联想).
const (
	SuggestRelease   = "release"
	SuggestPerformer = "performer"
	SuggestUpcoming  = "upcoming"
	SuggestTag       = "tag"
	SuggestQuery     = "query" // a hot search term
)

// SuggestOrder is the display order of suggestion types.
var SuggestOrder = []string{SuggestRelease, SuggestPerformer, SuggestUpcoming, SuggestTag, SuggestQuery}

// suggestQuota is how many of each type are shown before leftovers fill
// the remaining slots.
var suggestQuota = map[string]int{SuggestRelease: 3, SuggestPerformer: 2, SuggestUpcoming: 1, SuggestTag: 1, SuggestQuery: 1}

// MergeSuggestions picks at most limit items from per-type candidate lists
// (each already ranked): first up to each type's quota in SuggestOrder,
// then leftovers in the same order until limit is reached. The result is
// grouped by type in SuggestOrder.
func MergeSuggestions[T any](groups map[string][]T, limit int) []T {
	if limit <= 0 || limit > SuggestLimit {
		limit = SuggestLimit
	}
	taken := map[string]int{}
	total := 0
	for _, typ := range SuggestOrder {
		n := min(suggestQuota[typ], len(groups[typ]), limit-total)
		taken[typ] = n
		total += n
	}
	for _, typ := range SuggestOrder {
		if total >= limit {
			break
		}
		extra := min(len(groups[typ])-taken[typ], limit-total)
		taken[typ] += extra
		total += extra
	}
	out := make([]T, 0, total)
	for _, typ := range SuggestOrder {
		out = append(out, groups[typ][:taken[typ]]...)
	}
	return out
}
