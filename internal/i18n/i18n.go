// Package i18n holds the database-free rules of multi-language catalog
// content (PRD section 8, tech plan 3.10): supported languages, the
// translation status machine, the public fallback chain and source hashing
// used for stale detection. Storage lives in internal/store/i18n.go and the
// HTTP surface in internal/api/i18n.go and internal/api/admin_i18n.go.
package i18n

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Supported content and UI languages (PRD 8 launch list), BCP 47 tags.
const (
	ZhCN = "zh-CN"
	ZhTW = "zh-TW"
	En   = "en"
	Ja   = "ja"
	Ko   = "ko"
	Vi   = "vi"
	Es   = "es"
	Th   = "th"
)

// SourceLang is the language of the original (canonical) catalog fields.
// When a request falls back to the original, the served language is
// reported as Original because canonical fields are not always Japanese.
const SourceLang = Ja

// Original is the *_lang value reported when the canonical field is served.
const Original = "original"

// Languages lists the supported languages in PRD order. Migration
// 015_i18n.sql repeats this list in its CHECK constraints.
var Languages = []string{ZhCN, ZhTW, En, Ja, Ko, Vi, Es, Th}

// Supported reports whether lang is one of Languages (exact, canonical case).
func Supported(lang string) bool {
	for _, l := range Languages {
		if l == lang {
			return true
		}
	}
	return false
}

// Normalize maps a user-supplied language tag to a supported language, or
// "" when it has none. It is case-insensitive, accepts "_" for "-", and maps
// script/region variants: zh-Hant, zh-HK, zh-MO -> zh-TW; zh, zh-Hans,
// zh-SG -> zh-CN; en-US -> en; ja-JP -> ja, etc.
func Normalize(tag string) string {
	t := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(tag), "_", "-"))
	if t == "" {
		return ""
	}
	parts := strings.Split(t, "-")
	if parts[0] == "zh" {
		for _, p := range parts[1:] {
			switch p {
			case "hant", "tw", "hk", "mo":
				return ZhTW
			}
		}
		return ZhCN
	}
	for _, l := range Languages {
		if strings.ToLower(l) == parts[0] {
			return l
		}
	}
	return ""
}

// FromAcceptLanguage picks the best supported language from an
// Accept-Language header value, honouring q-weights (ties keep header
// order). It returns "" when nothing matches.
func FromAcceptLanguage(header string) string {
	type cand struct {
		lang string
		q    float64
		pos  int
	}
	var cs []cand
	for i, part := range strings.Split(header, ",") {
		fields := strings.Split(part, ";")
		q := 1.0
		for _, p := range fields[1:] {
			if v, ok := strings.CutPrefix(strings.TrimSpace(p), "q="); ok {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					f = 0
				}
				q = f
			}
		}
		if l := Normalize(fields[0]); l != "" && q > 0 {
			cs = append(cs, cand{l, q, i})
		}
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].q > cs[j].q })
	if len(cs) == 0 {
		return ""
	}
	return cs[0].lang
}

// FallbackChain returns the languages tried, in order, for a request in
// lang before the original is used: lang itself, then zh-TW, zh-CN, en
// (PRD 8), without duplicates. Japanese is the source language, so a ja
// request goes straight from ja to the original rather than through Chinese.
func FallbackChain(lang string) []string {
	if lang == "" {
		return nil
	}
	if lang == SourceLang {
		return []string{SourceLang}
	}
	chain := []string{lang}
	for _, l := range []string{ZhTW, ZhCN, En} {
		if l != lang {
			chain = append(chain, l)
		}
	}
	return chain
}

// Translation statuses (tech plan 3.10).
const (
	StatusMachine  = "machine"  // produced by a Translator, never public
	StatusDraft    = "draft"    // edited by a translator, never public
	StatusReviewed = "reviewed" // approved, public
	StatusStale    = "stale"    // was reviewed, source changed since; still public
)

// Statuses lists every translation status.
var Statuses = []string{StatusMachine, StatusDraft, StatusReviewed, StatusStale}

// ValidStatus reports whether s is a translation status.
func ValidStatus(s string) bool {
	for _, x := range Statuses {
		if x == s {
			return true
		}
	}
	return false
}

// ErrTransition is returned for a status change the workflow does not allow.
var ErrTransition = errors.New("invalid status transition")

// Actions on a translation.
const (
	ActionEdit   = "edit"   // translator saves a value: anything -> draft
	ActionSubmit = "submit" // translator asks for review: machine|draft -> submitted draft
	ActionReview = "review" // reviewer approves: submitted draft|stale -> reviewed
)

// Next returns the status after action on a translation with status from
// (submitted tells whether a draft is awaiting review). Editing always
// yields an unsubmitted draft; submitting is allowed from machine or draft;
// reviewing needs a submitted draft, or a stale translation (re-confirming
// it still matches the new source). Reviewing yields reviewed when the
// translation's source hash still matches the source, else stale.
func Next(from string, submitted bool, action string, hashMatches bool) (string, error) {
	switch action {
	case ActionEdit:
		return StatusDraft, nil
	case ActionSubmit:
		if from == StatusMachine || from == StatusDraft {
			return StatusDraft, nil
		}
	case ActionReview:
		if from == StatusStale || (from == StatusDraft && submitted) {
			if hashMatches || from == StatusStale {
				return StatusReviewed, nil
			}
			return StatusStale, nil
		}
	}
	return "", ErrTransition
}

// Public reports whether a translation's reviewed value may be served: only
// reviewed and stale rows, never machine or draft (a draft edit of a
// previously reviewed row keeps serving its last reviewed value, which the
// store keeps separately).
func Public(status string) bool { return status == StatusReviewed || status == StatusStale }

// SourceHash fingerprints a source field value. It must equal the SQL
// function i18n_hash in migration 015: hex(sha256(utf8 text)), "" for NULL.
func SourceHash(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// IsStale reports whether a reviewed translation recorded against
// storedHash must be marked stale now that the source is current.
func IsStale(status, storedHash, current string) bool {
	return status == StatusReviewed && storedHash != SourceHash(current)
}

// Entity types and their translatable fields.
const (
	EntityRelease   = "release"
	EntityPerformer = "performer"
	EntityTag       = "tag"
)

// Fields maps each entity type to its translatable fields; the comment
// names the canonical column each one translates.
var Fields = map[string][]string{
	EntityRelease:   {"title", "synopsis"}, // releases.canonical_title, releases.synopsis
	EntityPerformer: {"name", "biography"}, // performers.canonical_name, performers.biography
	EntityTag:       {"name"},              // release_tags.tag (by normalized_tag)
}

// Ref names one translatable field of one entity. EntityID is the numeric
// id as text for releases and performers, and normalized_tag for tags.
type Ref struct {
	EntityType string
	EntityID   string
	Field      string
}

// Validate checks that r names a known entity type and field and that the
// id has the right shape for its type.
func (r Ref) Validate() error {
	fields, ok := Fields[r.EntityType]
	if !ok {
		return errors.New("unknown entity_type")
	}
	found := false
	for _, f := range fields {
		found = found || f == r.Field
	}
	if !found {
		return errors.New("unknown field for " + r.EntityType)
	}
	if r.EntityType == EntityTag {
		if r.EntityID == "" || len(r.EntityID) > 200 {
			return errors.New("invalid entity_id")
		}
		return nil
	}
	if n, err := strconv.ParseInt(r.EntityID, 10, 64); err != nil || n <= 0 || strconv.FormatInt(n, 10) != r.EntityID {
		return errors.New("invalid entity_id")
	}
	return nil
}

// Set holds publicly servable translations: Ref -> lang -> value.
type Set map[Ref]map[string]string

// Add records value for ref in lang.
func (s Set) Add(ref Ref, lang, value string) {
	m := s[ref]
	if m == nil {
		m = map[string]string{}
		s[ref] = m
	}
	m[lang] = value
}

// Resolve returns the value to show for ref following chain, then original.
// lang is the language actually served, Original for the canonical value,
// or "" when there is nothing to show.
func (s Set) Resolve(ref Ref, chain []string, original string) (value, lang string) {
	for _, l := range chain {
		if v, ok := s[ref][l]; ok && v != "" {
			return v, l
		}
	}
	if original == "" {
		return "", ""
	}
	return original, Original
}

// GlossaryTerm is one glossary entry given to translators and machines.
type GlossaryTerm struct {
	Term        string
	Lang        string
	Translation string
}

// Translator produces machine translations, stored with status machine.
//
// TODO(tech plan 3.10): the machine-translation provider is still to be
// chosen; there is deliberately no implementation and nothing calls an
// external service yet.
type Translator interface {
	Translate(ctx context.Context, text, from, to string, glossary []GlossaryTerm) (string, error)
}
