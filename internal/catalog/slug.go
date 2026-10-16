package catalog

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Slug rules for names (FINAL_DATA_DESIGN 3.1). Slug itself is unchanged
// (existing slugs and identifier scopes are computed with it); the helpers
// below only decide what to do when it yields nothing usable, as it does for
// Japanese-only names.

// ErrEmptySlug is returned by RequireSlug: no row is ever written with an
// empty slug.
var ErrEmptySlug = errors.New("catalog: empty slug")

// RequireSlug is the guard every slug write goes through.
func RequireSlug(slug string) (string, error) {
	if strings.TrimSpace(slug) == "" {
		return "", ErrEmptySlug
	}
	return slug, nil
}

var hasASCIILetter = regexp.MustCompile(`[a-z]`)

// NameSlug is the slug of a Latin-script (English / romaji) name, or "" when
// the name has no Latin letters. For a name Slug already handles it returns
// exactly Slug(name), so ASCII slugs never change; otherwise it retries after
// NFKC so full-width Latin ("ＭＩＡ") is not lost. A digits-only result (the
// "2" left of "あいり2") is not a name slug.
func NameSlug(name string) string {
	if s := Slug(name); hasASCIILetter.MatchString(s) {
		return s
	}
	if s := Slug(norm.NFKC.String(name)); hasASCIILetter.MatchString(s) {
		return s
	}
	return ""
}

// RomajiSlug is the slug of a kana name or reading ("" when s is not
// romanizable, e.g. it contains kanji).
func RomajiSlug(s string) string {
	r, ok := Romaji(s)
	if !ok {
		return ""
	}
	return NameSlug(r)
}

// PerformerSlugInput is what is known about a performer when it is created.
type PerformerSlugInput struct {
	Name string
	// Readings are alternate forms of the name from the source: romaji or
	// English aliases and kana readings (DMM ruby / name_kana), best first.
	Readings []string
	// SourceScoped is the source-scoped fallback, e.g. "dmm-actress-1074740".
	SourceScoped string
}

// PerformerSlugCandidates returns the slugs to try, in order, for a new
// performer: the Latin name; each reading (Latin as is, kana romanized); the
// romanized name (kana-only names); the source-scoped key. The caller takes
// the first one not in use and finally falls back to FallbackPerformerSlug.
// Never contains "".
func PerformerSlugCandidates(in PerformerSlugInput) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(NameSlug(in.Name))
	for _, r := range in.Readings {
		if s := NameSlug(r); s != "" && !HasKana(r) {
			add(s)
			continue
		}
		add(RomajiSlug(r))
	}
	add(RomajiSlug(in.Name))
	add(Slug(in.SourceScoped))
	return out
}

// FallbackPerformerSlug is the last-resort slug, unique by construction.
func FallbackPerformerSlug(id int64) string { return "performer-" + strconv.FormatInt(id, 10) }

// DMMActressSlug is the source-scoped fallback for a DMM actress id (the
// DMM performer record key). Only numeric ids qualify ("" otherwise).
func DMMActressSlug(id string) string {
	if id = strings.TrimSpace(id); !digitsOnly.MatchString(id) {
		return ""
	}
	return "dmm-actress-" + id
}

var fallbackSlug = regexp.MustCompile(`^(performer|dmm-actress)-\d+$`)

// IsFallbackSlug reports whether slug is a source-scoped or id fallback, i.e.
// one a better (romaji) slug should be offered for as a redirect.
func IsFallbackSlug(slug string) bool { return fallbackSlug.MatchString(slug) }

// MakerScope is the identifier_scope of a maker-scoped identifier (DVD code).
// It is never the bare "maker:":
//
//	no maker name                -> "global"
//	Slug(name) != ""             -> "maker:<slug>"     (unchanged, ASCII names)
//	DMM maker id (digits) known  -> "dmm-maker:<id>"
//	otherwise                    -> "maker-name:<md5(name)[:16]>"
//
// The last two are chosen so migration 028 can recompute them in SQL from the
// stored source record (raw_data maker / maker_id) exactly; a romanization
// could not be.
func MakerScope(name, dmmMakerID string) string {
	if strings.TrimSpace(name) == "" {
		return "global"
	}
	if s := Slug(name); s != "" {
		return "maker:" + s
	}
	if id := strings.TrimSpace(dmmMakerID); digitsOnly.MatchString(id) {
		return "dmm-maker:" + id
	}
	sum := md5.Sum([]byte(name))
	return "maker-name:" + hex.EncodeToString(sum[:])[:16]
}
