package search

import (
	"net/url"
	"strings"
)

// ApplySynonyms expands q with search_synonyms (term -> canonical, both in
// NormalizeQuery form): a rule for the whole query wins; otherwise every
// space-separated token with a rule is replaced. It returns q unchanged when
// no rule applies.
func ApplySynonyms(q string, rules map[string]string) string {
	if len(rules) == 0 || q == "" {
		return q
	}
	if c, ok := rules[q]; ok && c != "" {
		return c
	}
	toks := Tokens(q)
	changed := false
	for i, t := range toks {
		if c, ok := rules[t]; ok && c != "" {
			toks[i] = c
			changed = true
		}
	}
	if !changed {
		return q
	}
	return NormalizeQuery(strings.Join(toks, " "))
}

// SynonymKeys are the terms to look up for q: the whole query and each token.
func SynonymKeys(q string) []string {
	keys := []string{q}
	for _, t := range Tokens(q) {
		if t != q {
			keys = append(keys, t)
		}
	}
	return keys
}

// ValidRedirectTarget reports whether a search_redirects.target_url is
// acceptable: a site-relative path ("/help/download", not "//host") or an
// absolute https URL with a host. Anything else (javascript:, http:, data:)
// is rejected so a redirect rule can never become an open-redirect or XSS
// vector in the client.
func ValidRedirectTarget(target string) bool {
	if target == "" || len(target) > 2000 || strings.ContainsAny(target, " \t\r\n\\") {
		return false
	}
	if strings.HasPrefix(target, "/") {
		return !strings.HasPrefix(target, "//")
	}
	u, err := url.Parse(target)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
