package community

import (
	"slices"
	"testing"
)

func TestDetectLang(t *testing.T) {
	for _, c := range []struct{ body, hint, want string }{
		{"前方高能", "", LangZH},
		{"哈哈哈 lol", "en", LangZH}, // Han wins over Latin
		{"すごい", "", LangJA},
		{"神回ですね", "zh-CN", LangJA}, // kana beats kanji and the hint
		{"カッコイイ", "", LangJA},
		{"대박", "", LangKO},
		{"สนุกมาก", "", LangTH},
		{"xem lại đi", "", LangVI},
		{"hay quá", "", LangEN}, // á alone is not Vietnamese-only
		{"đẹp", "en", LangVI},
		{"¡qué bueno!", "", LangES},
		{"niño", "", LangES},
		{"que bueno", "es", LangES}, // no marks: the hint decides
		{"que bueno", "es-MX", LangES},
		{"nice", "", LangEN},
		{"nice", "fr", LangEN},
		{"wow", "vi", LangVI},
		{"233333", "zh-CN", LangUnknown},
		{"？？？", "", LangUnknown},
		{"👍👍", "", LangUnknown},
	} {
		got := DetectLang(c.body, c.hint)
		if got != c.want {
			t.Errorf("DetectLang(%q,%q) = %q, want %q", c.body, c.hint, got, c.want)
		}
		if !slices.Contains(DanmakuLangs, got) || !ValidLang(got) {
			t.Errorf("DetectLang(%q) = %q is not a DanmakuLangs value / valid lang", c.body, got)
		}
	}
}
