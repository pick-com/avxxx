package catalog

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Kana -> romaji (modified Hepburn, ASCII only: no macrons) for slugs.
//
// The table is small and hand-written on purpose (no dictionary, no network,
// no third-party data). It only covers kana: a name with kanji cannot be
// romanized without a reading, so Romaji reports ok=false for it and the
// caller falls back to a reading (DMM "ruby") or a source-scoped slug.
//
// Conventions (stable: slugs derived from them are never rewritten):
//   - ん is always "n" (no apostrophe, no m before b/m/p): うんぱい -> unpai
//   - っ doubles the next consonant, "tch" before ch: まっちゃ -> matcha
//   - ー repeats the previous vowel: ルーシー -> ruushii; long vowels written
//     with kana stay as written: ゆうき -> yuuki
//   - を -> o, ゐ -> i, ゑ -> e; ぢ -> ji, づ -> zu
//   - small vowels after a consonant kana form extended syllables
//     (ファ fa, ティ ti, ヴォ vo, シェ she); alone they are plain vowels
//   - katakana and half-width katakana fold to hiragana first (NFKC)

var kanaMono = map[rune]string{
	'あ': "a", 'い': "i", 'う': "u", 'え': "e", 'お': "o",
	'か': "ka", 'き': "ki", 'く': "ku", 'け': "ke", 'こ': "ko",
	'さ': "sa", 'し': "shi", 'す': "su", 'せ': "se", 'そ': "so",
	'た': "ta", 'ち': "chi", 'つ': "tsu", 'て': "te", 'と': "to",
	'な': "na", 'に': "ni", 'ぬ': "nu", 'ね': "ne", 'の': "no",
	'は': "ha", 'ひ': "hi", 'ふ': "fu", 'へ': "he", 'ほ': "ho",
	'ま': "ma", 'み': "mi", 'む': "mu", 'め': "me", 'も': "mo",
	'や': "ya", 'ゆ': "yu", 'よ': "yo",
	'ら': "ra", 'り': "ri", 'る': "ru", 'れ': "re", 'ろ': "ro",
	'わ': "wa", 'ゐ': "i", 'ゑ': "e", 'を': "o", 'ん': "n",
	'が': "ga", 'ぎ': "gi", 'ぐ': "gu", 'げ': "ge", 'ご': "go",
	'ざ': "za", 'じ': "ji", 'ず': "zu", 'ぜ': "ze", 'ぞ': "zo",
	'だ': "da", 'ぢ': "ji", 'づ': "zu", 'で': "de", 'ど': "do",
	'ば': "ba", 'び': "bi", 'ぶ': "bu", 'べ': "be", 'ぼ': "bo",
	'ぱ': "pa", 'ぴ': "pi", 'ぷ': "pu", 'ぺ': "pe", 'ぽ': "po",
	'ゔ': "vu",
	'ぁ': "a", 'ぃ': "i", 'ぅ': "u", 'ぇ': "e", 'ぉ': "o",
	'ゃ': "ya", 'ゅ': "yu", 'ょ': "yo", 'ゎ': "wa",
	'ゕ': "ka", 'ゖ': "ke",
}

// kanaDi are two-kana syllables (a kana followed by a small kana).
var kanaDi = map[string]string{
	"きゃ": "kya", "きゅ": "kyu", "きょ": "kyo",
	"しゃ": "sha", "しゅ": "shu", "しょ": "sho", "しぇ": "she",
	"ちゃ": "cha", "ちゅ": "chu", "ちょ": "cho", "ちぇ": "che",
	"にゃ": "nya", "にゅ": "nyu", "にょ": "nyo",
	"ひゃ": "hya", "ひゅ": "hyu", "ひょ": "hyo",
	"みゃ": "mya", "みゅ": "myu", "みょ": "myo",
	"りゃ": "rya", "りゅ": "ryu", "りょ": "ryo",
	"ぎゃ": "gya", "ぎゅ": "gyu", "ぎょ": "gyo",
	"じゃ": "ja", "じゅ": "ju", "じょ": "jo", "じぇ": "je",
	"ぢゃ": "ja", "ぢゅ": "ju", "ぢょ": "jo",
	"びゃ": "bya", "びゅ": "byu", "びょ": "byo",
	"ぴゃ": "pya", "ぴゅ": "pyu", "ぴょ": "pyo",
	"ふぁ": "fa", "ふぃ": "fi", "ふぇ": "fe", "ふぉ": "fo", "ふゅ": "fyu",
	"てぃ": "ti", "でぃ": "di", "とぅ": "tu", "どぅ": "du", "でゅ": "dyu",
	"うぃ": "wi", "うぇ": "we", "うぉ": "wo",
	"ゔぁ": "va", "ゔぃ": "vi", "ゔぇ": "ve", "ゔぉ": "vo",
	"つぁ": "tsa", "つぃ": "tsi", "つぇ": "tse", "つぉ": "tso",
	"いぇ": "ye", "くぁ": "kwa", "ぐぁ": "gwa",
}

// toHiragana folds katakana (after NFKC, so half-width forms too) to hiragana.
func toHiragana(r rune) rune {
	switch {
	case r >= 'ァ' && r <= 'ヶ':
		return r - 0x60
	case r == 'ヴ':
		return 'ゔ'
	}
	return r
}

func isKana(r rune) bool {
	_, ok := kanaMono[r]
	return ok || r == 'っ' || r == 'ー'
}

// Romaji transliterates a kana (hiragana / katakana) string to lower-case
// ASCII Hepburn. ASCII letters and digits pass through; spaces, the middle dot
// and other separators become a single space. ok is false when s holds any
// other letter (kanji, hangul, ...) or no letter at all, so callers never get
// a partial romanization.
func Romaji(s string) (string, bool) {
	rs := []rune(norm.NFKC.String(s))
	for i, r := range rs {
		rs[i] = toHiragana(r)
	}
	var b strings.Builder
	letters := 0
	geminate := false
	lastVowel := byte(0)
	emit := func(syl string) {
		if geminate {
			if strings.HasPrefix(syl, "ch") {
				b.WriteByte('t')
			} else if c := syl[0]; !strings.ContainsRune("aeioun", rune(c)) {
				b.WriteByte(c)
			}
			geminate = false
		}
		b.WriteString(syl)
		lastVowel = syl[len(syl)-1]
		letters++
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 'っ':
			geminate = true
			continue
		case r == 'ー':
			if strings.IndexByte("aeiou", lastVowel) >= 0 {
				b.WriteByte(lastVowel)
			}
			continue
		}
		if i+1 < len(rs) {
			if syl, ok := kanaDi[string(rs[i:i+2])]; ok {
				emit(syl)
				i++
				continue
			}
		}
		if syl, ok := kanaMono[r]; ok {
			emit(syl)
			continue
		}
		geminate = false
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
			lastVowel = byte(unicode.ToLower(r))
			letters++
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || r == '・':
			b.WriteByte(' ')
			lastVowel = 0
		default:
			return "", false // kanji or another script: no partial romaji
		}
	}
	if letters == 0 {
		return "", false
	}
	return strings.Join(strings.Fields(b.String()), " "), true
}

// HasKana reports whether s contains hiragana or katakana.
func HasKana(s string) bool {
	for _, r := range norm.NFKC.String(s) {
		if isKana(toHiragana(r)) {
			return true
		}
	}
	return false
}
