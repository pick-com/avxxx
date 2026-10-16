package search

import (
	"avxxx/internal/audit"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestNormalizeQuery(t *testing.T) {
	for in, want := range map[string]string{
		"  ＪＵＱ　743 ":    "juq 743",
		"Night  Flight": "night flight",
		"夜航的人":          "夜航的人",
		"\t三上\n悠亚 ":     "三上 悠亚",
		"":              "",
		"ＡＢＣ１２３":        "abc123",
	} {
		if got := NormalizeQuery(in); got != want {
			t.Errorf("NormalizeQuery(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("あ", 300)
	if n := len([]rune(NormalizeQuery(long))); n != MaxQueryRunes {
		t.Errorf("long query kept %d runes", n)
	}
	if Meaningful("a") || !Meaningful("ab") || !Meaningful("夜航") || Meaningful("夜") {
		t.Error("Meaningful wrong")
	}
}

func TestCodeKeys(t *testing.T) {
	for _, in := range []string{"juq743", "JUQ-743", "juq00743", "JUQ 743", "ｊｕｑ－７４３", "juq_743"} {
		if got := CodeKeys(in); !reflect.DeepEqual(got, []string{"juq-743"}) {
			t.Errorf("CodeKeys(%q) = %v", in, got)
		}
	}
	if got := CodeKeys("118abp00123"); !reflect.DeepEqual(got, []string{"118abp-123", "abp-123"}) {
		t.Errorf("content id keys = %v", got)
	}
	if got := CodeKeys("fc2-ppv-1234567"); !reflect.DeepEqual(got, []string{"fc2ppv1234567"}) {
		t.Errorf("fc2 keys = %v", got)
	}
	for _, in := range []string{"夜航的人", "night flight", "abc", "123", "4k", "juq 743 全集", "a-b-c-d-1-2", strings.Repeat("a1", 20)} {
		if got := CodeKeys(in); got != nil {
			t.Errorf("CodeKeys(%q) = %v, want nil (not a code)", in, got)
		}
	}
}

func TestCodePrefix(t *testing.T) {
	for in, want := range map[string]string{
		"juq":     "juq",
		"JUQ-":    "juq-",
		"juq7":    "juq-7",
		"juq-74":  "juq-74",
		"juq 743": "juq-743",
		"夜航":      "",
		"j":       "",
		"juq7x":   "",
	} {
		if got := CodePrefix(in); got != want {
			t.Errorf("CodePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripModifiers(t *testing.T) {
	for _, c := range []struct {
		in, want string
		ok       bool
	}{
		{"夜航的人全集", "夜航的人", true},
		{"夜航的人 第3集", "夜航的人", true},
		{"夜航的人第十二集在线看", "夜航的人", true},
		{"夜航的人 在线观看 免费 高清", "夜航的人", true},
		{"夜航的人4k中字", "夜航的人4k", true}, // CJK modifiers anywhere; "4k" glued to text stays
		{"夜航的人 4K 中字", "夜航的人", true},
		{"夜航的人演员表", "夜航的人", true},
		{"夜航的人大结局", "夜航的人", true},
		{"《夜航的人》完整版", "夜航的人", true},
		{"night flight full movie hd", "night flight", true},
		{"night flight eng sub", "night flight", true},
		{"juq-743 1080p", "juq-743", true},
		{"hdka-123", "", false}, // "hd" only as a whole word
		{"夜航的人", "", false},     // nothing to strip
		{"全集", "", false},       // nothing left
		{"免费高清在线看", "", false},
		{"", "", false},
	} {
		got, ok := StripModifiers(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("StripModifiers(%q) = %q,%v want %q,%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestApplySynonyms(t *testing.T) {
	rules := map[string]string{"夜航": "夜航的人", "yui": "波多野结衣", "night flight": "夜航的人"}
	for in, want := range map[string]string{
		"夜航":             "夜航的人",
		"night flight":   "夜航的人",
		"yui 新作":         "波多野结衣 新作",
		"夜航的人":           "夜航的人",
		"something else": "something else",
	} {
		if got := ApplySynonyms(in, rules); got != want {
			t.Errorf("ApplySynonyms(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ApplySynonyms("夜航", nil); got != "夜航" {
		t.Errorf("no rules: %q", got)
	}
	if got := SynonymKeys("yui 新作"); !reflect.DeepEqual(got, []string{"yui 新作", "yui", "新作"}) {
		t.Errorf("SynonymKeys = %v", got)
	}
}

func TestValidRedirectTarget(t *testing.T) {
	for in, want := range map[string]bool{
		"/help/download":           true,
		"/releases/juq-743":        true,
		"https://help.example.com": true,
		"//evil.example.com":       false,
		"http://example.com":       false,
		"javascript:alert(1)":      false,
		"":                         false,
		"/a b":                     false,
		"https://u:p@example.com":  false,
		`/\evil.com`:               false,
	} {
		if got := ValidRedirectTarget(in); got != want {
			t.Errorf("ValidRedirectTarget(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		term        string
		near, retry bool
		want        Class
	}{
		{"怎么下载", false, false, ClassNotVideo},
		{"会员充值", true, false, ClassNotVideo},
		{"how to download", false, false, ClassNotVideo},
		{"app", false, false, ClassNotVideo},
		{"夜航的人全集", false, true, ClassMissed},
		{"night flight", true, false, ClassMissed},
		{"不存在的片名", false, false, ClassNotInLibrary},
		{"happy days", false, false, ClassNotInLibrary}, // "app" only as a whole word
	} {
		if got := Classify(c.term, c.near, c.retry); got != c.want {
			t.Errorf("Classify(%q,%v,%v) = %s, want %s", c.term, c.near, c.retry, got, c.want)
		}
	}
	if ClassNotInLibrary.Label() != "库里没有" || ClassMissed.Label() != "库里有没搜到" || ClassNotVideo.Label() != "不是找视频" {
		t.Error("labels")
	}
	if len(ClassMissed.Actions()) == 0 || ClassNotVideo.Actions()[0] != "add_redirect" {
		t.Error("actions")
	}
}

func TestSharesText(t *testing.T) {
	for _, c := range []struct {
		term, name string
		want       bool
	}{
		{"没有的片81000", "Bench 1000", false}, // digits only: a false near-match
		{"没有的片381000123", "BenchP 1000", false},
		{"夜航abcd的人们", "夜航abcd的人", true},
		{"nigth flihgt", "Night Flight", true},
		{"ＮＩＧＨＴ", "night flight", true}, // normalized on both sides
		{"はたのゆい", "波多野結衣 はたの", true},
		{"81000", "Bench 1000", false}, // numbers must match whole
		{"other 51234", "Fallback Title 51234", true},
		{"没有的片51234", "Bench 5123", false},
		{"12", "Bench 1200", true}, // nothing to judge: trigram verdict stands
		{"juq-743", "JUQ-743 夜航", true},
		{"完全不同", "Summer Rain", false},
	} {
		if got := SharesText(c.term, c.name); got != c.want {
			t.Errorf("SharesText(%q,%q) = %v, want %v", c.term, c.name, got, c.want)
		}
	}
}

func day(s string) time.Time { t, _ := time.Parse(time.DateOnly, s); return t }

func TestAggregateDaily(t *testing.T) {
	d1, d2 := day("2026-10-14"), day("2026-10-15")
	raw := []RawCount{
		{Day: d1.Add(3 * time.Hour), Query: "JUQ 743", Lang: "zh-CN", Searches: 3, ZeroResults: 1, Clicks: 2, FirstPageClicks: 1, Rescued: 0},
		{Day: d1, Query: "ｊｕｑ　743 ", Lang: "zh-CN", Searches: 2, ZeroResults: 0, Clicks: 1, FirstPageClicks: 1, Rescued: 1},
		{Day: d1, Query: "juq 743", Lang: "en", Searches: 1},
		{Day: d2, Query: "juq 743", Lang: "zh-CN", Searches: 4, ZeroResults: 4},
		{Day: d1, Query: "a", Lang: "zh-CN", Searches: 9},  // too short
		{Day: d1, Query: "  ", Lang: "zh-CN", Searches: 9}, // empty
		{Day: d1, Query: "夜航", Lang: "", Searches: 0},      // no searches
	}
	got := AggregateDaily(raw)
	want := []DailyRow{
		{Day: d1, QNorm: "juq 743", Lang: "en", Searches: 1},
		{Day: d1, QNorm: "juq 743", Lang: "zh-CN", Searches: 5, ZeroResults: 1, Clicks: 3, FirstPageClicks: 2, Rescued: 1},
		{Day: d2, QNorm: "juq 743", Lang: "zh-CN", Searches: 4, ZeroResults: 4},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AggregateDaily =\n%+v\nwant\n%+v", got, want)
	}
	tot := Summarize(got)
	if tot.Searches != 10 || tot.ZeroResults != 5 || tot.Clicks != 3 || tot.Rescued != 1 {
		t.Errorf("totals %+v", tot)
	}
	if tot.ZeroResultRate != 0.5 || tot.CTR != 0.3 || tot.FirstPageCTR != 0.2 || tot.RescueRate != 0.1667 || tot.MeetsTarget {
		t.Errorf("rates %+v", tot)
	}
	if z := Summarize(nil); z.MeetsTarget || z.ZeroResultRate != 0 || z.ZeroResultGoal != ZeroResultTarget {
		t.Errorf("empty totals %+v", z)
	}
	if ok := Summarize([]DailyRow{{Searches: 100, ZeroResults: 4}}); !ok.MeetsTarget {
		t.Errorf("4%% should meet the < 5%% target: %+v", ok)
	}
}

func TestRankHot(t *testing.T) {
	in := []HotInput{
		{Query: "夜航的人", Sessions: 10, Searches: 12},
		{Query: "夜航的人 ", Sessions: 2, Searches: 2},                 // merges into 夜航的人 -> 12
		{Query: "三上悠亚", Sessions: 12, Searches: 30},                // top by sessions
		{Query: "波多野结衣", Sessions: 12, Searches: 20},               // tie on sessions, fewer searches
		{Query: "rare", Sessions: 2, Searches: 2},                  // below min sessions
		{Query: "没有的片", Sessions: 9, Searches: 10, ZeroResults: 8}, // mostly zero results
		{Query: "怎么下载", Sessions: 50, Searches: 50},                // help query
		{Query: "blocked term", Sessions: 50, Searches: 50},
		{Query: "加微信看片", Sessions: 50, Searches: 50}, // moderation word
		{Query: "x", Sessions: 99, Searches: 99},     // too short
	}
	ex := Exclusions{Blocked: []string{"blocked term"}, ModerationWords: []string{"加微信"}}
	got := RankHot(in, ex, HotConfig{})
	want := []HotRow{{1, "三上悠亚", 12}, {2, "波多野结衣", 12}, {3, "夜航的人", 12}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RankHot = %+v, want %+v", got, want)
	}
	if got := RankHot(in, ex, HotConfig{Size: 1}); len(got) != 1 || got[0].QNorm != "三上悠亚" {
		t.Errorf("size 1: %+v", got)
	}
}

func TestMergeSuggestions(t *testing.T) {
	g := map[string][]string{
		SuggestRelease:   {"r1", "r2", "r3", "r4", "r5"},
		SuggestPerformer: {"p1", "p2", "p3"},
		SuggestTag:       {"t1"},
		SuggestQuery:     {"q1", "q2"},
	}
	got := MergeSuggestions(g, 8)
	want := []string{"r1", "r2", "r3", "r4", "p1", "p2", "t1", "q1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeSuggestions = %v, want %v", got, want)
	}
	if got := MergeSuggestions(map[string][]string{SuggestPerformer: {"p1", "p2", "p3", "p4"}}, 3); !reflect.DeepEqual(got, []string{"p1", "p2", "p3"}) {
		t.Errorf("leftovers: %v", got)
	}
	if got := MergeSuggestions(map[string][]string{}, 8); len(got) != 0 {
		t.Errorf("empty: %v", got)
	}
	if got := MergeSuggestions(g, 100); len(got) != SuggestLimit {
		t.Errorf("limit clamp: %d", len(got))
	}
}

func TestAggregationRange(t *testing.T) {
	now := time.Date(2026, 10, 15, 13, 5, 0, 0, time.UTC)
	from, to := AggregationRange(now, time.Time{}, false)
	if to != day("2026-10-15") || from != day("2026-07-18") {
		t.Errorf("empty table: %s..%s", from, to)
	}
	from, _ = AggregationRange(now, day("2026-10-15"), true)
	if from != day("2026-10-14") {
		t.Errorf("up to date: from %s", from)
	}
	from, _ = AggregationRange(now, day("2026-10-10"), true)
	if from != day("2026-10-10") {
		t.Errorf("lagging: from %s", from)
	}
	from, _ = AggregationRange(now, day("2025-01-01"), true)
	if from != day("2026-07-18") {
		t.Errorf("very old: from %s", from)
	}
}

type fakeStore struct {
	last     time.Time
	hasLast  bool
	raw      []RawCount
	hot      []HotInput
	ex       Exclusions
	failHot  error
	gotFrom  time.Time
	gotTo    time.Time
	daily    []DailyRow
	trending []HotRow
	runs     []audit.Run
}

func (f *fakeStore) LastSearchDay(context.Context) (time.Time, bool, error) {
	return f.last, f.hasLast, nil
}
func (f *fakeStore) SearchEventCounts(_ context.Context, from, to time.Time) ([]RawCount, error) {
	f.gotFrom, f.gotTo = from, to
	return f.raw, nil
}
func (f *fakeStore) ReplaceSearchDaily(_ context.Context, _, _ time.Time, rows []DailyRow) error {
	f.daily = rows
	return nil
}
func (f *fakeStore) SearchHotInputs(context.Context, time.Time, time.Time) ([]HotInput, error) {
	return f.hot, f.failHot
}
func (f *fakeStore) SearchExclusions(context.Context) (Exclusions, error) { return f.ex, nil }
func (f *fakeStore) ReplaceSearchTrending(_ context.Context, _ string, rows []HotRow, _ time.Time) error {
	f.trending = rows
	return nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func TestRunner(t *testing.T) {
	now := time.Date(2026, 10, 15, 13, 0, 0, 0, time.UTC)
	f := &fakeStore{last: day("2026-10-15"), hasLast: true,
		raw: []RawCount{{Day: day("2026-10-15"), Query: "夜航", Searches: 2}},
		hot: []HotInput{{Query: "夜航", Sessions: 5, Searches: 5}}}
	rep, err := (&Runner{Store: f, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.gotFrom != day("2026-10-14") || f.gotTo != day("2026-10-16") {
		t.Errorf("event window %s..%s", f.gotFrom, f.gotTo)
	}
	if rep.DailyRows != 1 || rep.HotRows != 1 || len(f.daily) != 1 || len(f.trending) != 1 {
		t.Errorf("report %+v", rep)
	}
	if len(f.runs) != 2 || f.runs[0].Job != "search.daily" || f.runs[1].Job != "search.hot" || f.runs[1].Result != audit.ResultOK {
		t.Errorf("runs %+v", f.runs)
	}

	f2 := &fakeStore{failHot: errors.New("boom")}
	if _, err := (&Runner{Store: f2, Now: func() time.Time { return now }}).Run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if len(f2.runs) != 2 || f2.runs[1].Result != audit.ResultFailed || f2.runs[0].Result != audit.ResultOK {
		t.Errorf("failed runs %+v", f2.runs)
	}
}

func TestDisplayQueryKeepsSpelling(t *testing.T) {
	for in, want := range map[string]string{
		"  Summer   Night ": "Summer Night",
		"ＪＵＱ　743":           "ＪＵＱ 743",
		"夜航":                "夜航",
	} {
		if got := DisplayQuery(in); got != want {
			t.Errorf("DisplayQuery(%q) = %q, want %q", in, got, want)
		}
		if NormalizeQuery(DisplayQuery(in)) != NormalizeQuery(in) {
			t.Errorf("%q: display and input normalize differently", in)
		}
	}
	if got := DisplayQuery(strings.Repeat("A", MaxQueryRunes+5)); utf8.RuneCountInString(got) != MaxQueryRunes {
		t.Errorf("not capped: %d runes", utf8.RuneCountInString(got))
	}
}
