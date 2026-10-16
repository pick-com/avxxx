package series

import (
	"testing"
	"time"
)

func TestSlugBase(t *testing.T) {
	cases := map[string]string{
		"Best Of Yui":       "best-of-yui",
		"  S1 NO.1 STYLE  ": "s1-no-1-style",
		"ＡＢＣ　Ｓｅｒｉｅｓ":        "abc-series", // full-width -> NFKC
		"美少女シリーズ":           "",
		"痴漢電車 Vol.":         "vol",
		"--Hello__World!!":  "hello-world",
		"":                  "",
	}
	for in, want := range cases {
		if got := SlugBase(in); got != want {
			t.Errorf("SlugBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlug(t *testing.T) {
	none := func(string) bool { return false }
	if got := Slug("Best Of", 7, none); got != "best-of" {
		t.Errorf("got %q", got)
	}
	// Japanese-only names get a stable, non-empty fallback.
	if got := Slug("美少女シリーズ", 42, none); got != "series-42" {
		t.Errorf("got %q", got)
	}
	taken := map[string]bool{"best-of": true, "series-9": true}
	if got := Slug("Best Of", 8, func(s string) bool { return taken[s] }); got != "best-of--8" {
		t.Errorf("collision: got %q", got)
	}
	if got := Slug("シリーズ", 9, func(s string) bool { return taken[s] }); got != "series-9--9" {
		t.Errorf("fallback collision: got %q", got)
	}
	if got := Slug("x", 1, nil); got != "x" {
		t.Errorf("nil taken: got %q", got)
	}
}

func TestParseEpisode(t *testing.T) {
	cases := []struct {
		title string
		n     int
		ok    bool
	}{
		{"Summer Diary Vol.3", 3, true},
		{"Summer Diary vol 12 special", 12, true},
		{"Story Part 2", 2, true},
		{"Episode 05: return", 5, true},
		{"Collection #4", 4, true},
		{"人妻温泉旅行 第3話", 3, true},
		{"人妻温泉旅行 第３弾", 3, true}, // full-width digit
		{"人妻温泉旅行 第三十二弾", 32, true},
		{"人妻温泉旅行 第十集", 10, true},
		{"湯けむり その5", 5, true},
		{"Piano 3 Lessons", 0, false},
		{"Deep 2", 0, false},
		{"Plain title", 0, false},
		{"Vol.0", 0, false},
	}
	for _, c := range cases {
		n, ok := ParseEpisode(c.title)
		if n != c.n || ok != c.ok {
			t.Errorf("ParseEpisode(%q) = %d,%v want %d,%v", c.title, n, ok, c.n, c.ok)
		}
	}
}

func TestCodeNumber(t *testing.T) {
	cases := map[string]int{"JUQ-743": 743, "juq00743": 743, "1sdab-012": 12, "300MIUM-123": 123, "ssis 001": 1}
	for in, want := range cases {
		if n, ok := CodeNumber(in); !ok || n != want {
			t.Errorf("CodeNumber(%q) = %d,%v want %d", in, n, ok, want)
		}
	}
	for _, in := range []string{"", "title-src-key-x", "12345"} {
		if n, ok := CodeNumber(in); ok {
			t.Errorf("CodeNumber(%q) = %d, want none", in, n)
		}
	}
}

func day(s string) *time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return &t
}

func ids(ks []Keyed) []int64 {
	out := make([]int64, len(ks))
	for i, k := range ks {
		out[i] = k.ID
	}
	return out
}

func eq(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOrderEpisodeByTitle(t *testing.T) {
	items := []Item{
		{ID: 1, Title: "Diary Vol.3", Code: "ABC-010", ReleaseDate: day("2024-01-01")},
		{ID: 2, Title: "Diary Vol.1", Code: "ABC-030", ReleaseDate: day("2024-03-01")},
		{ID: 3, Title: "Diary Special", Code: "ABC-020", ReleaseDate: day("2023-01-01")},
		{ID: 4, Title: "Diary Vol.2", Code: "ABC-040", ReleaseDate: nil},
	}
	got, basis := Order(items, SortEpisode)
	if basis != BasisTitle || !eq(ids(got), []int64{2, 4, 1, 3}) {
		t.Fatalf("episode by title: basis %s order %v", basis, ids(got))
	}
	if got[0].Episode == nil || *got[0].Episode != 1 || got[3].Episode != nil {
		t.Errorf("episode numbers: %+v", got)
	}
}

func TestOrderEpisodeByCodeAndDates(t *testing.T) {
	items := []Item{
		{ID: 1, Title: "a", Code: "ABC-010", ReleaseDate: day("2024-05-01")},
		{ID: 2, Title: "b", Code: "ABC-002", ReleaseDate: day("2024-06-01")},
		{ID: 3, Title: "c Vol.9", Code: "", ReleaseDate: day("2020-01-01")},
	}
	got, basis := Order(items, SortEpisode)
	if basis != BasisCode || !eq(ids(got), []int64{2, 1, 3}) {
		t.Fatalf("episode by code: basis %s order %v", basis, ids(got))
	}
	got, _ = Order(items, SortNewest)
	if !eq(ids(got), []int64{2, 1, 3}) {
		t.Errorf("newest: %v", ids(got))
	}
	got, _ = Order(items, SortOldest)
	if !eq(ids(got), []int64{3, 1, 2}) {
		t.Errorf("oldest: %v", ids(got))
	}
	// Undated releases sort last both ways.
	items = append(items, Item{ID: 4, Title: "d"})
	if got, _ = Order(items, SortNewest); got[len(got)-1].ID != 4 {
		t.Errorf("newest undated: %v", ids(got))
	}
	if got, _ = Order(items, SortOldest); got[len(got)-1].ID != 4 {
		t.Errorf("oldest undated: %v", ids(got))
	}
	if _, basis = Order(nil, SortEpisode); basis != BasisDate {
		t.Errorf("empty basis = %s", basis)
	}
}

func TestAfter(t *testing.T) {
	items := []Item{{ID: 1, ReleaseDate: day("2024-01-01")}, {ID: 2, ReleaseDate: day("2024-01-01")}, {ID: 3, ReleaseDate: day("2024-02-01")}}
	ordered, _ := Order(items, SortOldest)
	rest := After(ordered, ordered[0].Key, ordered[0].ID)
	if !eq(ids(rest), []int64{2, 3}) {
		t.Errorf("after first: %v", ids(rest))
	}
	if rest = After(ordered, ordered[2].Key, ordered[2].ID); len(rest) != 0 {
		t.Errorf("after last: %v", ids(rest))
	}
}
