package homeops

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeTexts(t *testing.T) {
	got, err := NormalizeTexts(Texts{"zh-cn": {Title: "  主推  "}, "EN": {Subtitle: "sub"}, "ja": {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["zh-CN"].Title != "主推" || got["en"].Subtitle != "sub" {
		t.Errorf("got %+v", got)
	}
	for _, bad := range []Texts{
		{"fr": {Title: "x"}},
		{"zh": {Title: "x"}}, // not an exact supported code
		{"en": {Title: strings.Repeat("a", MaxTitleLen+1)}},
		{"en": {Title: "a"}, "EN": {Title: "b"}},
	} {
		if _, err := NormalizeTexts(bad); err == nil {
			t.Errorf("NormalizeTexts(%v) accepted", bad)
		}
	}
}

func TestNormalizeLangsRegions(t *testing.T) {
	ls, err := NormalizeLangs([]string{"en", "zh-cn", "en", " "})
	if err != nil || strings.Join(ls, ",") != "zh-CN,en" {
		t.Errorf("langs = %v %v", ls, err)
	}
	if _, err := NormalizeLangs([]string{"klingon"}); err == nil {
		t.Error("unknown lang accepted")
	}
	rs, err := NormalizeRegions([]string{"us", "JP", "us", ""})
	if err != nil || strings.Join(rs, ",") != "JP,US" {
		t.Errorf("regions = %v %v", rs, err)
	}
	for _, bad := range []string{"USA", "1A", "u"} {
		if _, err := NormalizeRegions([]string{bad}); err == nil {
			t.Errorf("region %q accepted", bad)
		}
	}
}

func TestNormalizeLink(t *testing.T) {
	for _, ok := range []string{"", "/discover?tag=x", "/series/abc", "https://example.com/a?b=c"} {
		if _, err := NormalizeLink(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"//evil.com", "http://example.com", "javascript:alert(1)", "https://", "/a b", "/a\\b", "https://u:p@example.com", "data:text/html,x"} {
		if _, err := NormalizeLink(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWindowAndLive(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	end := now.Add(time.Hour)
	if err := CheckWindow(time.Time{}, nil); err == nil {
		t.Error("zero start accepted")
	}
	if err := CheckWindow(end, &now); err == nil {
		t.Error("end before start accepted")
	}
	if err := CheckWindow(now, &end); err != nil {
		t.Error(err)
	}
	if !Live(now, &end, now) || Live(now, &end, end) || Live(end, nil, now) || !Live(now, nil, end) {
		t.Error("Live window bounds wrong")
	}
}

func TestTargets(t *testing.T) {
	cases := []struct {
		langs, regions []string
		lang, region   string
		want           bool
	}{
		{nil, nil, "en", "", true},
		{[]string{"en"}, nil, "ja", "", false},
		{[]string{"en"}, nil, "en", "", true},
		{nil, []string{"JP"}, "en", "", false}, // unknown region never matches a targeted slot
		{nil, []string{"JP"}, "en", "JP", true},
		{nil, []string{"JP"}, "en", "US", false},
	}
	for _, c := range cases {
		if got := Targets(c.langs, c.regions, c.lang, c.region); got != c.want {
			t.Errorf("Targets(%v,%v,%q,%q) = %v", c.langs, c.regions, c.lang, c.region, got)
		}
	}
}

func TestPick(t *testing.T) {
	tx := Texts{"zh-CN": {Title: "中文"}, "en": {Title: "English"}, "ja": {Subtitle: "サブ"}}
	cases := []struct{ lang, want, from string }{
		{"en", "English", "en"},
		{"", "中文", "zh-CN"},
		{"zh-TW", "中文", "zh-CN"}, // zh-TW -> zh-CN
		{"ko", "中文", "zh-CN"},    // ko -> zh-TW -> zh-CN
		{"ja", "中文", "zh-CN"},    // ja chain is ja only, then the operators' zh-CN
	}
	for _, c := range cases {
		if got, from := Pick(tx, c.lang, Title); got != c.want || from != c.from {
			t.Errorf("Pick(%q) = %q from %q, want %q from %q", c.lang, got, from, c.want, c.from)
		}
	}
	if got, from := Pick(tx, "en", Subtitle); got != "サブ" || from != "ja" {
		t.Errorf("subtitle falls back to any language: %q %q", got, from)
	}
	if got, _ := Pick(Texts{}, "en", Title); got != "" {
		t.Errorf("empty texts: %q", got)
	}
}
