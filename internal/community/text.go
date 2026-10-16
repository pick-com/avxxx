package community

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Body limits (PRD 5.5, 19).
const (
	MaxDanmakuRunes = 50
	MaxReviewRunes  = 140
	MaxCommentRunes = 500
	MaxKeywordRunes = 64
)

var (
	ErrEmptyBody   = errors.New("body is empty")
	ErrBodyTooLong = errors.New("body too long")
	ErrControlChar = errors.New("body contains control characters")
)

// CleanBody trims surrounding space and validates length (in runes) and
// characters. Newlines are not allowed in danmaku; reviews may keep them
// (allowNewline).
func CleanBody(s string, maxRunes int, allowNewline bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ErrEmptyBody
	}
	if !utf8.ValidString(s) {
		return "", ErrControlChar
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return "", ErrBodyTooLong
	}
	for _, r := range s {
		if r == '\n' && allowNewline {
			continue
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return "", ErrControlChar
		}
	}
	return s, nil
}

// foldText applies NFKC (full-width to half-width, compatibility forms),
// lower-cases and keeps only letters and digits, so "加 微 信", "加.微.信"
// and "ＡＢＣ" match "加微信" and "abc".
func foldText(s string) []rune {
	s = strings.ToLower(norm.NFKC.String(s))
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out = append(out, r)
		}
	}
	return out
}

// NormalizeKeyword is the stored form of a moderation word ("" if nothing is
// left after folding).
func NormalizeKeyword(s string) string { return string(foldText(s)) }

// DedupeKey maps near-identical danmaku to the same key: folded text with
// runs of one character collapsed ("哈哈哈哈" -> "哈", "23333" -> "23") and
// then reduced to its shortest repeating unit ("hahaha" -> "ha",
// "awsl awsl" -> "awsl"). Text with no letters or digits (only emoji or
// punctuation) keys on its trimmed raw form.
func DedupeKey(s string) string {
	rs := foldText(s)
	if len(rs) == 0 {
		return "raw:" + strings.TrimSpace(s)
	}
	collapsed := rs[:1]
	for _, r := range rs[1:] {
		if r != collapsed[len(collapsed)-1] {
			collapsed = append(collapsed, r)
		}
	}
	return string(collapsed[:period(collapsed)])
}

// period is the length of the shortest unit u such that s is u repeated
// (a trailing partial unit is allowed only when s has at least two full
// units, so "abca" stays "abca" but "ababa" becomes "ab").
func period(s []rune) int {
	n := len(s)
	for p := 1; p <= n/2; p++ {
		ok := true
		for i := p; i < n; i++ {
			if s[i] != s[i-p] {
				ok = false
				break
			}
		}
		if ok {
			return p
		}
	}
	return n
}

// Keyword is one moderation_words row.
type Keyword struct {
	Word     string `json:"word"`
	Category string `json:"category"`
}

// KeywordCategories are the allowed moderation_words.category values.
var KeywordCategories = []string{"ad", "abuse", "spoiler", "illegal", "political", "other"}

// ValidKeywordCategory reports whether c is an allowed category.
func ValidKeywordCategory(c string) bool {
	for _, k := range KeywordCategories {
		if k == c {
			return true
		}
	}
	return false
}

// Matcher is the synchronous keyword check (tech plan 3.8): a substring
// match on folded text. It is immutable and safe for concurrent use.
type Matcher struct {
	words []Keyword // Word already normalized
}

// NewMatcher builds a matcher; words are normalized and empty ones dropped.
func NewMatcher(words []Keyword) *Matcher {
	m := &Matcher{}
	for _, w := range words {
		if n := NormalizeKeyword(w.Word); n != "" {
			m.words = append(m.words, Keyword{Word: n, Category: w.Category})
		}
	}
	return m
}

// Match returns the first keyword contained in text.
func (m *Matcher) Match(text string) (Keyword, bool) {
	if m == nil || len(m.words) == 0 {
		return Keyword{}, false
	}
	folded := string(foldText(text))
	for _, w := range m.words {
		if strings.Contains(folded, w.Word) {
			return w, true
		}
	}
	return Keyword{}, false
}
