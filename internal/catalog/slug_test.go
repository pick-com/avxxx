package catalog

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNameSlugKeepsASCIISlugs(t *testing.T) {
	// For any name Slug already handles, NameSlug is exactly Slug: existing
	// ASCII slugs never change.
	for _, n := range []string{"Yui Hatano", "Maria Ozawa (小澤マリア)", "AIKA", "Rio  Hamasaki", "s1 no.1 style"} {
		if got, want := NameSlug(n), Slug(n); got != want {
			t.Errorf("NameSlug(%q) = %q, want Slug %q", n, got, want)
		}
	}
	for n, want := range map[string]string{"ＭＩＡ　ＭＡＬＫＯＶＡ": "mia-malkova", "波多野結衣": "", "あいり2": "", "": ""} {
		if got := NameSlug(n); got != want {
			t.Errorf("NameSlug(%q) = %q, want %q", n, got, want)
		}
	}
}

func TestPerformerSlugCandidatesOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		in   PerformerSlugInput
		want []string
	}{
		{"latin name first", PerformerSlugInput{Name: "Yui Hatano", Readings: []string{"はたのゆい"}, SourceScoped: "dmm-actress-1"}, []string{"yui-hatano", "hatanoyui", "dmm-actress-1"}},
		{"romaji alias before kana", PerformerSlugInput{Name: "波多野結衣", Readings: []string{"Yui Hatano", "はたのゆい"}, SourceScoped: "dmm-actress-1"}, []string{"yui-hatano", "hatanoyui", "dmm-actress-1"}},
		{"kana ruby", PerformerSlugInput{Name: "麻美ゆま", Readings: []string{"あさみゆま"}, SourceScoped: "dmm-actress-15365"}, []string{"asamiyuma", "dmm-actress-15365"}},
		{"kana-only name", PerformerSlugInput{Name: "うんぱい", SourceScoped: "dmm-actress-1074740"}, []string{"unpai", "dmm-actress-1074740"}},
		{"source scoped only", PerformerSlugInput{Name: "小澤マリア", SourceScoped: "dmm-actress-15187"}, []string{"dmm-actress-15187"}},
		{"nothing", PerformerSlugInput{Name: "波多野結衣"}, nil},
	} {
		if got := PerformerSlugCandidates(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSlugsNeverEmpty(t *testing.T) {
	if _, err := RequireSlug(""); !errors.Is(err, ErrEmptySlug) {
		t.Errorf("RequireSlug(\"\") err = %v", err)
	}
	if _, err := RequireSlug("  "); !errors.Is(err, ErrEmptySlug) {
		t.Error("blank slug accepted")
	}
	if s, err := RequireSlug("yui-hatano"); err != nil || s != "yui-hatano" {
		t.Errorf("RequireSlug = %q %v", s, err)
	}
	for _, in := range []PerformerSlugInput{{}, {Name: "波多野結衣", Readings: []string{"", "結衣"}}, {Name: "・", SourceScoped: "---"}} {
		for _, c := range PerformerSlugCandidates(in) {
			if strings.TrimSpace(c) == "" {
				t.Errorf("empty candidate for %+v", in)
			}
		}
	}
	if FallbackPerformerSlug(42) != "performer-42" || !IsFallbackSlug("performer-42") || !IsFallbackSlug("dmm-actress-7") || IsFallbackSlug("yui-hatano") {
		t.Error("fallback slug")
	}
	if DMMActressSlug("15187") != "dmm-actress-15187" || DMMActressSlug("") != "" || DMMActressSlug("abc") != "" {
		t.Error("DMMActressSlug")
	}
}

func TestMakerScope(t *testing.T) {
	for _, c := range []struct{ name, id, want string }{
		{"", "", "global"},
		{"  ", "3152", "global"},
		{"S1 NO.1 STYLE", "3152", "maker:s1-no1-style"}, // unchanged ASCII scope
		{"エスワン ナンバーワンスタイル", "3152", "dmm-maker:3152"},
		{"桃太郎映像出版", "40016", "dmm-maker:40016"},
		{"桃太郎映像出版", "", "maker-name:03bd6aa312cc1e87"}, // = substr(md5(maker),1,16) in migration 028
	} {
		if got := MakerScope(c.name, c.id); got != c.want {
			t.Errorf("MakerScope(%q,%q) = %q, want %q", c.name, c.id, got, c.want)
		}
	}
	if MakerScope("桃太郎映像出版", "") == MakerScope("エスワン ナンバーワンスタイル", "") {
		t.Error("different Japanese makers share a scope")
	}
	for _, n := range []string{"桃太郎映像出版", "エスワン", "x", "　"} {
		if s := MakerScope(n, ""); s == "maker:" || s == "" {
			t.Errorf("bare scope for %q", n)
		}
	}
}
