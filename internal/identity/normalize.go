// Package identity holds the database-free parts of duplicate detection and
// merging: name and product-code normalization, pair scoring, candidate
// generation over in-memory features, and merge plan construction.
package identity

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeName folds a display name to a matching key: Unicode NFKC, lower
// case, and every space, separator and punctuation rune removed. Full-width
// Latin, half-width katakana and "Yui  Hatano"/"yui-hatano"/"YuiHatano" all
// fold to the same key.
func NormalizeName(s string) string {
	s = strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// nameTokens splits a NFKC-folded, lower-cased name on everything that is not
// a letter, digit or mark.
func nameTokens(s string) []string {
	s = strings.ToLower(norm.NFKC.String(s))
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r))
	})
}

// ReorderedNameKey is the order-insensitive key of a multi-token name, so
// "Hatano Yui" and "Yui Hatano" share a key. Single-token names return "".
func ReorderedNameKey(s string) string {
	t := nameTokens(s)
	if len(t) < 2 {
		return ""
	}
	sort.Strings(t)
	return strings.Join(t, " ")
}

// AliasForm is the value stored in performer_aliases.normalized_name: NFKC,
// lower case, whitespace collapsed to single spaces. It keeps spaces so the
// existing ILIKE alias search keeps matching word fragments.
func AliasForm(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(norm.NFKC.String(s))), " ")
}

var codeShape = regexp.MustCompile(`^(\d*)([a-z]+)(\d+)([a-z]?)$`)

// CodeKeys returns the matching keys of a product code (品番 / DVD ID /
// content ID). The first key is strict ("300mium-123"); when the code carries
// a numeric label prefix, as DMM content IDs do ("118abp00123"), a second,
// loose key without that prefix is added ("abp-123"). Digits are de-padded to
// at least three places, so "JUQ-743", "juq743", "JUQ 743" and "juq00743"
// share the strict key "juq-743". Codes that do not look like
// letters+digits fall back to the bare alphanumeric string.
func CodeKeys(s string) []string {
	s = strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	raw := b.String()
	if raw == "" {
		return nil
	}
	m := codeShape.FindStringSubmatch(raw)
	if m == nil {
		return []string{raw}
	}
	n, err := strconv.ParseUint(strings.TrimLeft(m[3], "0"), 10, 64)
	if err != nil { // all zeros or overflow
		n = 0
	}
	core := fmt.Sprintf("%s-%03d%s", m[2], n, m[4])
	if m[1] == "" {
		return []string{core}
	}
	return []string{m[1] + core, core}
}

// TitleSimilarity is the Sørensen–Dice coefficient over character bigrams of
// the NormalizeName forms of a and b, in [0,1]. Bigrams work for Japanese
// titles without word segmentation.
func TitleSimilarity(a, b string) float64 {
	ra, rb := []rune(NormalizeName(a)), []rune(NormalizeName(b))
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}
	if string(ra) == string(rb) {
		return 1
	}
	if len(ra) < 2 || len(rb) < 2 {
		return 0
	}
	grams := map[string]int{}
	for i := 0; i+1 < len(ra); i++ {
		grams[string(ra[i:i+2])]++
	}
	inter := 0
	for i := 0; i+1 < len(rb); i++ {
		g := string(rb[i : i+2])
		if grams[g] > 0 {
			grams[g]--
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ra)-1+len(rb)-1)
}
