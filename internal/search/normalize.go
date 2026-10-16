// Package search holds the database-free parts of catalog search (PRD 4.5,
// tech plan 3.4 / 3.13): query normalization, product-code detection,
// modifier stripping for the zero-result retry, synonym expansion, the
// AdminSearch classification heuristics, the aggregation of `search` events
// into search_query_daily and the hot-search (热搜) ranking.
package search

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"avxxx/internal/identity"

	"golang.org/x/text/unicode/norm"
)

// MaxQueryRunes bounds a query (the `search` event caps it at 200 too).
const MaxQueryRunes = 200

// MinQueryRunes is the shortest normalized query kept by the aggregation
// and the hot-search ranking.
const MinQueryRunes = 2

// NormalizeQuery folds a query to its stored / matching form (q_norm):
// Unicode NFKC (full-width Latin and digits to half-width), lower case,
// every run of whitespace collapsed to one space, trimmed, and cut to
// MaxQueryRunes runes. "  ＪＵＱ　743 " becomes "juq 743".
func NormalizeQuery(s string) string {
	s = strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(s))), " ")
	if utf8.RuneCountInString(s) > MaxQueryRunes {
		s = strings.TrimSpace(string([]rune(s)[:MaxQueryRunes]))
	}
	return s
}

// DisplayQuery is a query as the user typed it, for showing back (最近搜索):
// case and width kept, every run of whitespace collapsed to one space,
// trimmed, and cut to MaxQueryRunes runes. Two queries with the same
// NormalizeQuery are the same search.
func DisplayQuery(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxQueryRunes {
		s = strings.TrimSpace(string([]rune(s)[:MaxQueryRunes]))
	}
	return s
}

// Meaningful reports whether a normalized query is long enough to keep
// (at least MinQueryRunes runes).
func Meaningful(q string) bool { return utf8.RuneCountInString(q) >= MinQueryRunes }

var (
	codeChars = regexp.MustCompile(`^[a-z0-9][a-z0-9 _-]*[a-z0-9]$`)
	hasLetter = regexp.MustCompile(`[a-z]`)
	hasDigit  = regexp.MustCompile(`[0-9]`)
)

// LooksLikeCode reports whether a query is shaped like a product code
// (品番 / DVD ID / content ID): only ASCII letters, digits and at most three
// separators (space, '-', '_'), with both letters and digits, 4-24 runes.
// "juq743", "JUQ-743", "juq00743", "118abp00123" and "fc2-ppv-1234567" are
// codes; "1080p" also passes, which is harmless because code hits are only
// added in front of the normal text matches.
func LooksLikeCode(q string) bool {
	q = NormalizeQuery(q)
	if len(q) < 4 || len(q) > 24 || !codeChars.MatchString(q) {
		return false
	}
	if strings.Count(q, " ")+strings.Count(q, "-")+strings.Count(q, "_") > 3 {
		return false
	}
	return hasLetter.MatchString(q) && hasDigit.MatchString(q)
}

// CodeKeys returns the product-code matching keys of q (identity.CodeKeys:
// "juq743", "JUQ-743" and "juq00743" all give "juq-743"), or nil when q
// does not look like a code. The SQL function search_code_keys
// (migrations/023_search.sql) computes the same keys for stored identifiers.
func CodeKeys(q string) []string {
	if !LooksLikeCode(q) {
		return nil
	}
	return identity.CodeKeys(q)
}

// CodePrefix returns the release_identifiers.normalized_value prefix of a
// partially typed code for suggestions ("juq7" -> "juq-7", "JUQ-" ->
// "juq-"), or "" when q is not letters optionally followed by digits.
func CodePrefix(q string) string {
	m := codePrefixShape.FindStringSubmatch(NormalizeQuery(q))
	if m == nil {
		return ""
	}
	if m[2] == "" && m[3] == "" {
		return m[1]
	}
	return m[1] + "-" + m[3]
}

var codePrefixShape = regexp.MustCompile(`^([a-z]{2,10})([ _-]?)([0-9]{0,8})$`)

// Tokens splits a normalized query on spaces.
func Tokens(q string) []string { return strings.Fields(q) }
