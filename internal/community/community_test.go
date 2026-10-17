package community

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestBucketMS(t *testing.T) {
	for _, c := range []struct{ dur, want int }{
		{0, 1000}, {60_000, 1000}, {200_000, 1000}, {7_200_000, 36_000}, {7_200_123, 36_000},
	} {
		if got := BucketMS(c.dur); got != c.want {
			t.Errorf("BucketMS(%d) = %d, want %d", c.dur, got, c.want)
		}
	}
	if BucketOf(10_000_000, 36_000) != CurveBuckets-1 {
		t.Error("past the end must clamp to the last bucket")
	}
	if BucketOf(-5, 1000) != 0 || BucketOf(35_999, 36_000) != 0 || BucketOf(36_000, 36_000) != 1 {
		t.Error("BucketOf boundaries")
	}
}

func TestDedupeKey(t *testing.T) {
	same := [][]string{
		{"哈哈哈哈", "哈哈", "哈"},
		{"23333", "2333333333", "23"},
		{"hahaha", "HAHA", "ha ha ha!"},
		{"awsl awsl", "AWSL", "ａｗｓｌ"},
		{"前方高能", "前方高能！！", "前 方 高 能"},
	}
	for _, g := range same {
		for _, s := range g[1:] {
			if DedupeKey(s) != DedupeKey(g[0]) {
				t.Errorf("DedupeKey(%q)=%q != DedupeKey(%q)=%q", s, DedupeKey(s), g[0], DedupeKey(g[0]))
			}
		}
	}
	if DedupeKey("前方高能") == DedupeKey("后方高能") {
		t.Error("different text must not collide")
	}
	if DedupeKey("abca") == DedupeKey("abc") {
		t.Error("abca is not a repetition of abc")
	}
	if DedupeKey("😂😂") != DedupeKey("😂😂") || DedupeKey("😂") == DedupeKey("👍") {
		t.Error("symbol-only text keys on its raw form")
	}
}

func TestDedupeCounts(t *testing.T) {
	pts := []CurvePoint{
		{TMs: 100, UserID: 1, Body: "a"},
		{TMs: 200, UserID: 1, Body: "b"},     // same user, same bucket: dropped
		{TMs: 300, UserID: 2, Body: "哈哈哈"},   // counts
		{TMs: 400, UserID: 3, Body: "哈哈哈哈哈"}, // near-identical text, same bucket: dropped
		{TMs: 1500, UserID: 1, Body: "a"},    // same user, next bucket: counts
		{TMs: 1600, UserID: 4, Body: "哈"},    // text seen only in bucket 0: counts
	}
	counts, valid := DedupeCounts(pts, 1000)
	if valid != 4 || counts[0] != 2 || counts[1] != 2 {
		t.Fatalf("valid=%d counts[0..1]=%v", valid, counts[:2])
	}
}

func TestSmoothAndNormalize(t *testing.T) {
	var c [CurveBuckets]int
	c[0], c[1], c[100] = 3, 3, 9
	sm := Smooth3(c)
	if sm[0] != 3 || sm[1] != 2 || sm[2] != 1 || sm[99] != 3 || sm[100] != 3 || sm[101] != 3 || sm[199] != 0 {
		t.Fatalf("smooth: %v %v %v %v %v", sm[0], sm[1], sm[2], sm[99], sm[100])
	}
	n := Normalize(sm)
	if len(n) != CurveBuckets || n[0] != 255 || n[100] != 255 || n[1] != 170 || n[2] != 85 || n[150] != 0 {
		t.Fatalf("normalize: %v %v %v %v", n[0], n[1], n[2], n[100])
	}
	var zero [CurveBuckets]float64
	for _, v := range Normalize(zero) {
		if v != 0 {
			t.Fatal("all-zero input must stay zero")
		}
	}
}

func TestComputeCurveThreshold(t *testing.T) {
	var pts []CurvePoint
	for i := range MinCurveDanmaku - 1 {
		pts = append(pts, CurvePoint{TMs: i * 1000, UserID: int64(i), Body: "x" + strings.Repeat("y", i%3) + string(rune('a'+i%26))})
	}
	c := ComputeCurve(pts, 200_000)
	if c.Data != nil || c.Valid != MinCurveDanmaku-1 {
		t.Fatalf("49 valid danmaku must give no curve: %+v", c)
	}
	// 50 copies of the same text by one user count once.
	var spam []CurvePoint
	for range 200 {
		spam = append(spam, CurvePoint{TMs: 5000, UserID: 9, Body: "666"})
	}
	if c := ComputeCurve(spam, 200_000); c.Data != nil || c.Valid != 1 {
		t.Fatalf("spam must count once: %+v", c)
	}
	pts = append(pts, CurvePoint{TMs: 120_500, UserID: 999, Body: "高能"})
	c = ComputeCurve(pts, 200_000)
	if c.Valid != MinCurveDanmaku || len(c.Data) != CurveBuckets || c.BucketMS != 1000 {
		t.Fatalf("50 valid danmaku must give a curve: valid=%d len=%d", c.Valid, len(c.Data))
	}
	peak := int16(0)
	for _, v := range c.Data {
		peak = max(peak, v)
	}
	if peak != CurveMax {
		t.Fatalf("normalized peak %d", peak)
	}
	// Unknown duration: the latest danmaku bounds the timeline.
	if c := ComputeCurve(pts, 0); c.BucketMS != 1000 || c.Valid != MinCurveDanmaku {
		t.Fatalf("unknown duration: %+v", c.BucketMS)
	}
	if AltVersionKey(42) != "alt:42" {
		t.Error("AltVersionKey")
	}
}

func TestMatcher(t *testing.T) {
	m := NewMatcher([]Keyword{{Word: "加微信", Category: "ad"}, {Word: "  ", Category: "other"}, {Word: "FREE", Category: "ad"}})
	for _, s := range []string{"快来加微信", "加 微 信 abc", "加.微.信", "ｆｒｅｅ stuff", "Free!"} {
		if _, ok := m.Match(s); !ok {
			t.Errorf("%q should match", s)
		}
	}
	for _, s := range []string{"微信很好", "fre"} {
		if _, ok := m.Match(s); ok {
			t.Errorf("%q should not match", s)
		}
	}
	if w, _ := m.Match("加微信"); w.Category != "ad" {
		t.Error("category")
	}
	var nilM *Matcher
	if _, ok := nilM.Match("x"); ok {
		t.Error("nil matcher matches nothing")
	}
	if NormalizeKeyword(" A-b ") != "ab" || !ValidKeywordCategory("ad") || ValidKeywordCategory("x") {
		t.Error("keyword helpers")
	}
}

func TestCleanBody(t *testing.T) {
	if b, err := CleanBody("  hi  ", 50, false); err != nil || b != "hi" {
		t.Fatal(b, err)
	}
	if _, err := CleanBody(strings.Repeat("弹", 50), 50, false); err != nil {
		t.Error("50 runes is allowed")
	}
	if _, err := CleanBody(strings.Repeat("弹", 51), 50, false); err != ErrBodyTooLong {
		t.Error("51 runes is too long", err)
	}
	if _, err := CleanBody("   ", 50, false); err != ErrEmptyBody {
		t.Error("empty")
	}
	if _, err := CleanBody("a\nb", 50, false); err != ErrControlChar {
		t.Error("newline in danmaku")
	}
	if _, err := CleanBody("a\nb", 140, true); err != nil {
		t.Error("newline in review")
	}
	if _, err := CleanBody("a\x00b", 140, true); err != ErrControlChar {
		t.Error("NUL")
	}
}

func TestRatingWeights(t *testing.T) {
	cases := []struct {
		v    Voter
		want float64
	}{
		{Voter{DeviceID: "dev-12345678"}, WeightAnonymous},
		{Voter{UserID: 1, AccountAge: time.Hour}, WeightNewAccount},
		{Voter{UserID: 1, AccountAge: 8 * 24 * time.Hour}, WeightFull},
		{Voter{UserID: 1, AccountAge: 30 * 24 * time.Hour, Abnormal: true}, WeightAbnormal},
		{Voter{UserID: 1, AccountAge: time.Hour, Abnormal: true}, WeightAbnormal},
	}
	for _, c := range cases {
		if got := RatingWeight(c.v); got != c.want {
			t.Errorf("RatingWeight(%+v) = %v, want %v", c.v, got, c.want)
		}
	}
	if VoterKey(Voter{UserID: 7}) != "u:7" {
		t.Error("user voter key")
	}
	k := VoterKey(Voter{DeviceID: "dev-12345678"})
	if !strings.HasPrefix(k, "d:") || len(k) != 34 || strings.Contains(k, "dev") {
		t.Errorf("device voter key %q must be a hash", k)
	}
	if !ValidDeviceID("abcdEFGH-_12") || ValidDeviceID("short") || ValidDeviceID("has space here") {
		t.Error("ValidDeviceID")
	}
}

func TestBayesian(t *testing.T) {
	// No votes: the prior.
	if got := BayesianScore(RatingAggregate{}, 3.5); got != 3.5 {
		t.Errorf("empty = %v", got)
	}
	// 100 full-weight 5-star votes against a 3.0 prior: (25*3 + 500) / 125 = 4.6.
	a := RatingAggregate{Count: 100, WeightSum: 100, ScoreSum: 500}
	if got := BayesianScore(a, 3.0); math.Abs(got-4.6) > 1e-9 {
		t.Errorf("score = %v", got)
	}
	if s := DisplayScore(a, 3.0); s == nil || *s != 9.2 {
		t.Errorf("display = %v", s)
	}
	// Low-weight votes move the score less: 100 anonymous 5-star votes.
	anon := RatingAggregate{Count: 100, WeightSum: 30, ScoreSum: 150}
	if BayesianScore(anon, 3.0) >= BayesianScore(a, 3.0) {
		t.Error("anonymous votes must count less")
	}
	// Fewer than 50 votes: hidden.
	if DisplayScore(RatingAggregate{Count: 49, WeightSum: 49, ScoreSum: 245}, 3.0) != nil {
		t.Error("49 votes must hide the score")
	}
	if TenPoint(4.26) != 8.5 || TenPoint(1) != 2 || TenPoint(5) != 10 {
		t.Error("TenPoint")
	}
	// An out-of-range prior falls back to the default.
	if BayesianScore(RatingAggregate{}, 0) != DefaultPriorMean {
		t.Error("prior fallback")
	}
}

func TestMemoryLimiter(t *testing.T) {
	l := NewMemoryLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for i := range 3 {
		if ok, _ := l.Allow(ctx, "u1", 3, time.Minute); !ok {
			t.Fatalf("hit %d should pass", i)
		}
	}
	if ok, _ := l.Allow(ctx, "u1", 3, time.Minute); ok {
		t.Fatal("4th hit in a minute must be refused")
	}
	if ok, _ := l.Allow(ctx, "u2", 3, time.Minute); !ok {
		t.Fatal("other keys are independent")
	}
	now = now.Add(59 * time.Second)
	if ok, _ := l.Allow(ctx, "u1", 3, time.Minute); ok {
		t.Fatal("still inside the window")
	}
	now = now.Add(2 * time.Second)
	if ok, _ := l.Allow(ctx, "u1", 3, time.Minute); !ok {
		t.Fatal("window slid: should pass")
	}
	if ok, _ := l.Allow(ctx, "u1", 0, time.Minute); ok {
		t.Fatal("limit 0 refuses")
	}
	// Idle keys are swept.
	now = now.Add(time.Hour)
	l.Allow(ctx, "u3", 3, time.Minute)
	l.mu.Lock()
	_, kept := l.keys["u1"]
	l.mu.Unlock()
	if kept {
		t.Error("idle key not swept")
	}
	if PostingLimit("danmaku", time.Hour) != 3 || PostingLimit("review", time.Hour) != 3 ||
		PostingLimit("danmaku", 4*24*time.Hour) != DanmakuPerMinute || PostingLimit("review", 4*24*time.Hour) != ReviewPerMinute {
		t.Error("PostingLimit")
	}
}

func TestModerationHelpers(t *testing.T) {
	now := time.Unix(0, 0)
	if ReportDue("underage", now) != now.Add(2*time.Hour) || ReportDue("spam", now) != now.Add(48*time.Hour) ||
		ReportDue("copyright", now) != now.Add(24*time.Hour) {
		t.Error("ReportDue")
	}
	if !ValidReportTarget("performer_info") || ValidReportTarget("user") || !ValidReportReason("spoiler") || ValidReportReason("meh") {
		t.Error("report enums")
	}
	for action, want := range map[string]string{"fold": "folded", "delete": "deleted", "delete_mute": "deleted", "approve": "visible"} {
		if got, ok := ActionStatus(action); !ok || got != want {
			t.Errorf("ActionStatus(%s) = %s", action, got)
		}
	}
	if _, ok := ActionStatus("ban"); ok {
		t.Error("unknown action")
	}
	if !ValidColor("#ffAA00") || ValidColor("red") || !ValidMode("top") || ValidMode("left") || !ValidLang("") || !ValidLang("zh-CN") || ValidLang("zh_CN!") {
		t.Error("danmaku field validators")
	}
	// Helpful: likes matter, but age decays them.
	if HelpfulScore(10, 0) <= HelpfulScore(0, 0) || HelpfulScore(10, 0) <= HelpfulScore(10, 30*24*time.Hour) {
		t.Error("HelpfulScore ordering")
	}
	if math.Abs(HelpfulScore(1, HelpfulHalfLife)-1) > 1e-9 {
		t.Error("half-life")
	}
	for ratio, want := range map[float64]string{0: "none", 0.6: "partial", 0.95: "finished", 1: "finished"} {
		if b, _ := WatchBadge(ratio); b != want {
			t.Errorf("WatchBadge(%v) = %s", ratio, b)
		}
	}
	if _, p := WatchBadge(0.617); p != 61 {
		t.Errorf("percent %d", p)
	}
}
