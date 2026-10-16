package community

import (
	"strings"
	"unicode"
)

// Danmaku languages for the player's language filter (PRD 5.5 语言过滤).
// DetectLang stores one of these in danmaku.lang; LangUnknown is a body with
// no letters ("233", "？？？", emoji). Migration 045 backfills older rows with
// the same rules in SQL (without the client hint).
const (
	LangZH      = "zh"
	LangJA      = "ja"
	LangKO      = "ko"
	LangTH      = "th"
	LangVI      = "vi"
	LangES      = "es"
	LangEN      = "en"
	LangUnknown = "und"
)

// DanmakuLangs are the values DetectLang returns, in filter order.
var DanmakuLangs = []string{LangZH, LangJA, LangKO, LangEN, LangES, LangVI, LangTH, LangUnknown}

// latinHints are the Latin-script languages a client hint may name for a
// body DetectLang cannot tell apart by its letters alone.
var latinHints = map[string]string{"en": LangEN, "es": LangES, "vi": LangVI}

// Letters only Vietnamese uses among the site's languages (ă â đ ê ô ơ ư and
// the tone marks that sit on them), and the ones that mark Spanish (ñ ¿ ¡).
const (
	viLetters = "ăâđêôơưạảấầẩẫậắằẳẵặẹẻẽếềểễệỉịọỏốồổỗộớờởỡợụủứừửữựỳỵỷỹ"
	esMarks   = "ñ¿¡"
)

// DetectLang guesses a danmaku's language from its scripts, cheaply and
// without a model: any kana is Japanese, any hangul Korean, any Thai letter
// Thai, other Han text Chinese. Latin text is Vietnamese when it uses a
// Vietnamese-only letter, Spanish with ñ ¿ ¡, else the client's hint when
// it names en, es or vi, else English. A body without letters is
// LangUnknown. hint is the client's lang field (BCP 47, may be empty).
func DetectLang(body, hint string) string {
	var han, latin bool
	for _, r := range body {
		switch {
		case unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || r == 'ー':
			return LangJA
		case unicode.Is(unicode.Hangul, r):
			return LangKO
		case unicode.Is(unicode.Thai, r):
			return LangTH
		case unicode.Is(unicode.Han, r):
			han = true
		case unicode.Is(unicode.Latin, r):
			latin = true
		}
	}
	switch {
	case han:
		return LangZH
	case !latin:
		return LangUnknown
	}
	lower := strings.ToLower(body)
	if strings.ContainsAny(lower, viLetters) {
		return LangVI
	}
	if strings.ContainsAny(lower, esMarks) {
		return LangES
	}
	primary, _, _ := strings.Cut(strings.ToLower(hint), "-")
	if l, ok := latinHints[primary]; ok {
		return l
	}
	return LangEN
}
