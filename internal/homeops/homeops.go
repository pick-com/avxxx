// Package homeops holds the database-free rules of home curation and site
// announcements (PRD 4.1 主推横幅, PRD 11 AdminOps; migrations/038_home_ops.sql):
// input validation, targeting and per-language text selection.
package homeops

import (
	"avxxx/internal/i18n"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Slot and target types (home_slots.slot_type / target_type).
const (
	SlotHero  = "hero"
	SlotShelf = "shelf"

	TargetRelease = "release"
	TargetSeries  = "series"
	TargetLink    = "link"
)

// Announcement severities (announcements.severity).
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Limits on admin input.
const (
	MaxTitleLen    = 80
	MaxSubtitleLen = 160
	MaxBodyLen     = 300
	MaxLinkLabel   = 40
	MaxLinkLen     = 500
	MaxRegions     = 50
	// MaxReorder caps one drag-reorder request.
	MaxReorder = 200
)

// ValidSlotType reports whether t is a home slot type.
func ValidSlotType(t string) bool { return t == SlotHero || t == SlotShelf }

// ValidTargetType reports whether t is a home slot target type.
func ValidTargetType(t string) bool {
	return t == TargetRelease || t == TargetSeries || t == TargetLink
}

// ValidSeverity reports whether s is an announcement severity.
func ValidSeverity(s string) bool {
	return s == SeverityInfo || s == SeverityWarning || s == SeverityCritical
}

// Text is one language's copy. Slots use Title / Subtitle, announcements
// Body / LinkLabel.
type Text struct {
	Title     string `json:"title,omitempty"`
	Subtitle  string `json:"subtitle,omitempty"`
	Body      string `json:"body,omitempty"`
	LinkLabel string `json:"link_label,omitempty"`
}

func (t Text) empty() bool {
	return t.Title == "" && t.Subtitle == "" && t.Body == "" && t.LinkLabel == ""
}

// Texts maps a supported language (i18n.Languages) to its copy.
type Texts map[string]Text

// Error is an input validation failure; the message is safe for clients.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func invalid(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// NormalizeTexts trims every field, maps language keys to their supported
// spelling (zh-tw -> zh-TW) and drops empty entries. Unknown languages,
// duplicate keys after normalization and over-long fields are errors.
func NormalizeTexts(in Texts) (Texts, error) {
	out := Texts{}
	for k, v := range in {
		lang := i18n.Normalize(k)
		if lang == "" || !strings.EqualFold(lang, strings.ReplaceAll(strings.TrimSpace(k), "_", "-")) {
			return nil, invalid("unsupported text language %q", k)
		}
		v = Text{Title: strings.TrimSpace(v.Title), Subtitle: strings.TrimSpace(v.Subtitle), Body: strings.TrimSpace(v.Body), LinkLabel: strings.TrimSpace(v.LinkLabel)}
		for _, f := range []struct {
			name, v string
			max     int
		}{{"title", v.Title, MaxTitleLen}, {"subtitle", v.Subtitle, MaxSubtitleLen}, {"body", v.Body, MaxBodyLen}, {"link_label", v.LinkLabel, MaxLinkLabel}} {
			if utf8.RuneCountInString(f.v) > f.max {
				return nil, invalid("%s %s longer than %d characters", lang, f.name, f.max)
			}
		}
		if v.empty() {
			continue
		}
		if _, dup := out[lang]; dup {
			return nil, invalid("duplicate text language %q", lang)
		}
		out[lang] = v
	}
	return out, nil
}

// NormalizeLangs maps each entry to a supported language, dedupes and sorts
// in i18n.Languages order. Empty means every language.
func NormalizeLangs(in []string) ([]string, error) {
	seen := map[string]bool{}
	for _, v := range in {
		if strings.TrimSpace(v) == "" {
			continue
		}
		l := i18n.Normalize(v)
		if l == "" {
			return nil, invalid("unsupported lang %q", v)
		}
		seen[l] = true
	}
	out := []string{}
	for _, l := range i18n.Languages {
		if seen[l] {
			out = append(out, l)
		}
	}
	return out, nil
}

var regionCode = regexp.MustCompile(`^[A-Z]{2}$`)

// NormalizeRegion upper-cases an ISO 3166-1 alpha-2 code; "" stays "" and
// anything else is "" with ok false.
func NormalizeRegion(v string) (string, bool) {
	v = strings.ToUpper(strings.TrimSpace(v))
	if v == "" {
		return "", true
	}
	if !regionCode.MatchString(v) {
		return "", false
	}
	return v, true
}

// NormalizeRegions validates, upper-cases, dedupes and sorts region codes.
// Empty means every region.
func NormalizeRegions(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		r, ok := NormalizeRegion(v)
		if !ok {
			return nil, invalid("invalid region %q (ISO 3166-1 alpha-2)", v)
		}
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		out = append(out, r)
	}
	if len(out) > MaxRegions {
		return nil, invalid("at most %d regions", MaxRegions)
	}
	sort.Strings(out)
	return out, nil
}

// NormalizeLink accepts a site path ("/discover?tag=x", not "//host") or an
// absolute https:// URL with a host. "" stays "".
func NormalizeLink(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if len(v) > MaxLinkLen {
		return "", invalid("link longer than %d characters", MaxLinkLen)
	}
	if strings.ContainsAny(v, " \t\r\n\\") {
		return "", invalid("link must not contain spaces or backslashes")
	}
	if strings.HasPrefix(v, "/") {
		if strings.HasPrefix(v, "//") {
			return "", invalid("link must be a site path or an https URL")
		}
		if _, err := url.Parse(v); err != nil {
			return "", invalid("invalid link")
		}
		return v, nil
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", invalid("link must be a site path or an https URL")
	}
	return v, nil
}

// CheckWindow validates a schedule window: a start is required and the
// end, when set, is after it.
func CheckWindow(starts time.Time, ends *time.Time) error {
	if starts.IsZero() {
		return invalid("starts_at is required")
	}
	if ends != nil && !ends.After(starts) {
		return invalid("ends_at must be after starts_at")
	}
	return nil
}

// Live reports whether a schedule window covers at: starts <= at < ends.
func Live(starts time.Time, ends *time.Time, at time.Time) bool {
	return !starts.After(at) && (ends == nil || ends.After(at))
}

// Targets reports whether a langs / regions targeting matches a request.
// Empty lists match everyone; a region-targeted entry needs a known region.
func Targets(langs, regions []string, lang, region string) bool {
	if len(langs) > 0 && !contains(langs, lang) {
		return false
	}
	return len(regions) == 0 || (region != "" && contains(regions, region))
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// Chain is the order languages are tried when picking copy for lang: lang,
// its i18n fallback chain, then zh-CN and en (the languages operators write
// first). "" (no language requested) means zh-CN.
func Chain(lang string) []string {
	if lang == "" {
		lang = i18n.ZhCN
	}
	out := []string{}
	add := func(l string) {
		if !contains(out, l) {
			out = append(out, l)
		}
	}
	add(lang)
	for _, l := range i18n.FallbackChain(lang) {
		add(l)
	}
	add(i18n.ZhCN)
	add(i18n.En)
	return out
}

// Pick returns the first non-empty field along Chain(lang), then any
// language in i18n.Languages order; the language it came from, or "".
func Pick(t Texts, lang string, field func(Text) string) (string, string) {
	for _, l := range Chain(lang) {
		if v := field(t[l]); v != "" {
			return v, l
		}
	}
	for _, l := range i18n.Languages {
		if v := field(t[l]); v != "" {
			return v, l
		}
	}
	return "", ""
}

// Field accessors for Pick.
func Title(t Text) string     { return t.Title }
func Subtitle(t Text) string  { return t.Subtitle }
func Body(t Text) string      { return t.Body }
func LinkLabel(t Text) string { return t.LinkLabel }

// HasAny reports whether some language has a non-empty field.
func HasAny(t Texts, field func(Text) string) bool {
	for _, v := range t {
		if field(v) != "" {
			return true
		}
	}
	return false
}
