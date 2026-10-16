package catalog

import "testing"

func TestRomaji(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"うんぱい", "unpai"},
		{"あさみゆま", "asamiyuma"},
		{"はたの ゆい", "hatano yui"},
		{"つぼみ", "tsubomi"},
		{"しゃちょう", "shachou"},
		{"きょうこ", "kyouko"},
		{"じゅん", "jun"},
		{"ちひろ", "chihiro"},
		{"ふじ", "fuji"},
		{"まっちゃ", "matcha"},
		{"さっぽろ", "sapporo"},
		{"いっしき", "isshiki"},
		{"マリア", "maria"},
		{"ルーシー", "ruushii"},
		{"ティファニー", "tifanii"},
		{"ヴィーナス", "viinasu"},
		{"シェリー", "sherii"},
		{"ｱｲｶ", "aika"},            // half-width katakana
		{"ジェシカ・ロー", "jeshika roo"}, // middle dot separates
		{"をとめ", "otome"},
		{"ぢゃ", "ja"},
		{"ぁ", "a"},
		{"AIKA ちゃん", "aika chan"},
		{"れい2", "rei2"},
	} {
		got, ok := Romaji(c.in)
		if !ok || got != c.want {
			t.Errorf("Romaji(%q) = %q,%v want %q", c.in, got, ok, c.want)
		}
	}
	for _, in := range []string{"小澤マリア", "波多野結衣", "", " ", "・", "한국"} {
		if got, ok := Romaji(in); ok {
			t.Errorf("Romaji(%q) = %q, want not romanizable", in, got)
		}
	}
}

// Every kana in the table romanizes to ASCII lower-case letters.
func TestRomajiTableASCII(t *testing.T) {
	check := func(k, v string) {
		if v == "" {
			t.Errorf("%q has empty romaji", k)
		}
		for _, r := range v {
			if r < 'a' || r > 'z' {
				t.Errorf("%q -> %q is not a-z", k, v)
			}
		}
	}
	for k, v := range kanaMono {
		check(string(k), v)
	}
	for k, v := range kanaDi {
		check(k, v)
		if len([]rune(k)) != 2 {
			t.Errorf("digraph %q is not two kana", k)
		}
	}
}

func TestHasKana(t *testing.T) {
	if !HasKana("小澤マリア") || !HasKana("うんぱい") || HasKana("波多野結衣") || HasKana("Yui Hatano") {
		t.Error("HasKana")
	}
}
