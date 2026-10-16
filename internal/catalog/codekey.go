package catalog

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var codeMatchShape = regexp.MustCompile(`^([0-9]*[a-z]+)([0-9]+)([a-z]*)$`)

// CodeMatchKey is the key a fallback-only source (ThePornDB) record is
// matched and attached to a catalog release by: NFKC, only [A-Z0-9] kept,
// then "<PREFIX>-<number without leading zeros>[-<SUFFIX>]" for a
// letters+digits(+letters) code, else the bare alphanumeric string.
// "HEYZO-0123", "heyzo123" and "HEYZO 00123" all give "HEYZO-123";
// "ABC-0123-DL" gives "ABC-123-DL". Third-party APIs drop the zero padding
// the catalog keeps, so the code slug alone cannot match them.
//
// Distinct titles can share a key ("ABC-001" and "ABC-1"), so a match on it
// must be unique to be trusted. The SQL function code_match_key (migration
// 033) mirrors this for stored identifiers.
func CodeMatchKey(code string) string {
	s := strings.ToLower(norm.NFKC.String(code))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	raw := b.String()
	m := codeMatchShape.FindStringSubmatch(raw)
	if m == nil {
		return strings.ToUpper(raw)
	}
	num := strings.TrimLeft(m[2], "0")
	if num == "" {
		num = "0"
	}
	key := m[1] + "-" + num
	if m[3] != "" {
		key += "-" + m[3]
	}
	return strings.ToUpper(key)
}
