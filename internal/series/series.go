// Package series holds the pure rules behind the series entity
// (migrations/027_series.sql): slugging and the episode / volume ordering of
// a series' releases (PRD 5.4 同系列优先接下一集).
package series

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// SlugBase is the URL form of a series name: NFKC, lower case, every run of
// characters outside [a-z0-9] becomes '-', leading/trailing '-' trimmed. It
// mirrors SQL series_slug_base. A name without ASCII letters or digits (a
// Japanese-only title) yields "".
func SlugBase(name string) string {
	s := strings.ToLower(norm.NFKC.String(name))
	return strings.Trim(nonSlug.ReplaceAllString(s, "-"), "-")
}

// Slug is the slug assigned to a new series with id: SlugBase(name), or the
// stable fallback "series-<id>" when the base is empty; a candidate already
// taken gets the stable suffix "--<id>". It mirrors SQL series_ensure.
func Slug(name string, id int64, taken func(string) bool) string {
	cand := SlugBase(name)
	if cand == "" {
		cand = "series-" + strconv.FormatInt(id, 10)
	}
	if taken != nil && taken(cand) {
		cand += "--" + strconv.FormatInt(id, 10)
	}
	return cand
}

// Episode patterns, tried in order on the NFKC form of a title.
var (
	latinEpisode = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:vol|volume|part|pt|ep|episode|chapter|season|no)\s*\.?\s*(\d{1,4})(?:\D|$)`)
	jaEpisode    = regexp.MustCompile(`第\s*(\d{1,4})\s*[話话弾弹集章巻卷部回夜幕作]`)
	jaKanji      = regexp.MustCompile(`第\s*([一二三四五六七八九十百]{1,4})\s*[話话弾弹集章巻卷部回夜幕作]`)
	sonoEpisode  = regexp.MustCompile(`その\s*(\d{1,4})`)
	hashEpisode  = regexp.MustCompile(`#\s*(\d{1,4})(?:\D|$)`)
	codeNumber   = regexp.MustCompile(`(?i)^[a-z0-9]*?[a-z]+[-_ ]?0*(\d{1,6})`)
)

// ParseEpisode extracts an episode / volume number from a release title:
// "Vol.3", "Part 2", "#4", "第3話", "第三弾", "その5" (full-width digits
// included). ok is false when the title has none.
func ParseEpisode(title string) (n int, ok bool) {
	t := norm.NFKC.String(title)
	for _, re := range []*regexp.Regexp{jaEpisode, latinEpisode, sonoEpisode, hashEpisode} {
		if m := re.FindStringSubmatch(t); m != nil {
			if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
				return v, true
			}
		}
	}
	if m := jaKanji.FindStringSubmatch(t); m != nil {
		if v := kanjiNumber(m[1]); v > 0 {
			return v, true
		}
	}
	return 0, false
}

// kanjiNumber parses 一…九, 十, 百 combinations up to 999 (三十二 = 32).
func kanjiNumber(s string) int {
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, cur := 0, 0
	for _, r := range s {
		switch r {
		case '十', '百':
			unit := 10
			if r == '百' {
				unit = 100
			}
			if cur == 0 {
				cur = 1
			}
			total += cur * unit
			cur = 0
		default:
			d, ok := digits[r]
			if !ok {
				return 0
			}
			cur = d
		}
	}
	return total + cur
}

// CodeNumber is the numeric part of a product code ("JUQ-743" -> 743,
// "juq00743" -> 743, "1sdab-012" -> 12). ok is false when there is none.
func CodeNumber(code string) (int, bool) {
	m := codeNumber.FindStringSubmatch(strings.TrimSpace(norm.NFKC.String(code)))
	if m == nil {
		return 0, false
	}
	v, err := strconv.Atoi(m[1])
	return v, err == nil
}

// Release sorts accepted by GET /api/series/{slug}/releases.
const (
	SortEpisode = "episode"
	SortNewest  = "newest"
	SortOldest  = "oldest"
)

// ValidSort reports whether s is an accepted series release sort.
func ValidSort(s string) bool { return s == SortEpisode || s == SortNewest || s == SortOldest }

// Item is what ordering needs from one release of a series.
type Item struct {
	ID          int64
	Title       string
	Code        string // product code (dvd_code) or slug
	ReleaseDate *time.Time
}

// Keyed is an item with its sort key; pages are ordered by (Key, ID).
type Keyed struct {
	Item
	Key     string
	Episode *int // the number the episode order used, nil when none
}

// Basis tells which number the episode order used for a series.
const (
	BasisTitle = "title"        // episode / volume parsed from titles
	BasisCode  = "product_code" // numeric part of the product code
	BasisDate  = "release_date" // neither: release date
)

// Order computes sort keys for every item under sort and returns them
// ordered by (Key, ID), plus the basis used for "episode".
//
// episode: when at least half the releases carry an episode number in
// their title, that number orders them; otherwise the product-code number
// does (codes of one maker's series are issued in sequence). Releases
// without the chosen number follow, by release date. Ties fall back to
// release date, then id.
func Order(items []Item, sortName string) ([]Keyed, string) {
	out := make([]Keyed, len(items))
	basis := BasisDate
	if sortName == SortEpisode {
		titled, coded := 0, 0
		for _, it := range items {
			if _, ok := ParseEpisode(it.Title); ok {
				titled++
			}
			if _, ok := CodeNumber(it.Code); ok {
				coded++
			}
		}
		switch {
		case len(items) > 0 && titled*2 >= len(items):
			basis = BasisTitle
		case coded > 0:
			basis = BasisCode
		}
	}
	for i, it := range items {
		out[i].Item = it
		switch sortName {
		case SortNewest:
			out[i].Key = invDate(it.ReleaseDate)
		case SortOldest:
			out[i].Key = date(it.ReleaseDate)
		default:
			var n int
			ok := false
			switch basis {
			case BasisTitle:
				n, ok = ParseEpisode(it.Title)
			case BasisCode:
				n, ok = CodeNumber(it.Code)
			}
			if ok {
				v := n
				out[i].Episode = &v
				out[i].Key = fmt.Sprintf("0%07d%s", n, date(it.ReleaseDate))
			} else {
				out[i].Key = "1" + date(it.ReleaseDate)
			}
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Key != out[b].Key {
			return out[a].Key < out[b].Key
		}
		return out[a].ID < out[b].ID
	})
	return out, basis
}

// date renders a release date as a sortable key; unknown dates sort last.
func date(t *time.Time) string {
	if t == nil {
		return "99999999"
	}
	return t.Format("20060102")
}

// invDate sorts newest first; unknown dates sort last.
func invDate(t *time.Time) string {
	if t == nil {
		return "99999999"
	}
	n, _ := strconv.Atoi(t.Format("20060102"))
	return fmt.Sprintf("%08d", 99999998-n)
}

// After returns the items strictly after the cursor position (key, id) in
// an ordered slice from Order.
func After(ordered []Keyed, key string, id int64) []Keyed {
	i := sort.Search(len(ordered), func(i int) bool {
		k := ordered[i]
		return k.Key > key || (k.Key == key && k.ID > id)
	})
	return ordered[i:]
}
