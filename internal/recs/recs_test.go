package recs

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"
	"time"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestCompletionWeight(t *testing.T) {
	for _, c := range []struct{ ratio, want float64 }{
		{0, 0.2}, {1, 1}, {2, 1}, {0.5, 0.6}, {-1, 0.6}, {math.NaN(), 0.6},
	} {
		if got := CompletionWeight(c.ratio); !near(got, c.want) {
			t.Errorf("CompletionWeight(%v) = %v, want %v", c.ratio, got, c.want)
		}
	}
}

func neighborsOf(ns []Neighbor, item int64) map[int64]Neighbor {
	out := map[int64]Neighbor{}
	for _, n := range ns {
		if n.ItemID == item {
			out[n.NeighborID] = n
		}
	}
	return out
}

func TestCoWatchMath(t *testing.T) {
	// Users 1..3 watched A(10) and B(20) to the end; users 1..2 also C(30).
	var in []Interaction
	for u := int64(1); u <= 3; u++ {
		in = append(in, Interaction{u, 10, 1}, Interaction{u, 20, 1})
	}
	in = append(in, Interaction{1, 30, 1}, Interaction{2, 30, 1})
	got := CoWatch(in, CoWatchConfig{MinSupport: 2, Shrink: 1})

	// f_u: users 1,2 have 3 items -> 1/log2(5); user 3 has 2 -> 1/2.
	f3, f2 := 1/math.Log2(5), 0.5
	nA := 2*f3 + f2
	nC := 2 * f3
	wantAB := nA / math.Sqrt(nA*nA) * 3.0 / 4.0
	wantAC := 2 * f3 / math.Sqrt(nA*nC) * 2.0 / 3.0
	a := neighborsOf(got, 10)
	if !near(a[20].Score, wantAB) || a[20].Support != 3 {
		t.Errorf("A~B = %+v, want score %v support 3", a[20], wantAB)
	}
	if !near(a[30].Score, wantAC) || a[30].Support != 2 {
		t.Errorf("A~C = %+v, want score %v support 2", a[30], wantAC)
	}
	// Symmetric.
	if b := neighborsOf(got, 20); !near(b[10].Score, a[20].Score) {
		t.Errorf("B~A %v != A~B %v", b[10].Score, a[20].Score)
	}
	// Scores are below 1 and ordered best first per item.
	for i, n := range got {
		if n.Score <= 0 || n.Score >= 1 {
			t.Errorf("score out of (0,1): %+v", n)
		}
		if i > 0 && got[i-1].ItemID == n.ItemID && got[i-1].Score < n.Score {
			t.Errorf("not sorted: %+v before %+v", got[i-1], n)
		}
	}
}

func TestCoWatchMinSupportAndTopK(t *testing.T) {
	in := []Interaction{{1, 10, 1}, {1, 20, 1}, {2, 10, 1}, {2, 20, 1}}
	if got := CoWatch(in, CoWatchConfig{MinSupport: 3}); len(got) != 0 {
		t.Errorf("support 2 < 3 should drop the pair, got %+v", got)
	}
	if got := CoWatch(in, CoWatchConfig{MinSupport: 2}); len(got) != 2 {
		t.Errorf("want both directions, got %+v", got)
	}
	// One item co-watched with 5 others by the same 3 users: TopK 2 keeps 2.
	in = nil
	for u := int64(1); u <= 3; u++ {
		for it := int64(1); it <= 6; it++ {
			in = append(in, Interaction{u, it, 1})
		}
	}
	got := CoWatch(in, CoWatchConfig{MinSupport: 3, TopK: 2})
	if n := len(neighborsOf(got, 1)); n != 2 {
		t.Errorf("TopK 2: item 1 has %d neighbours", n)
	}
}

func TestCoWatchCompletionWeighting(t *testing.T) {
	// B is finished by A's viewers, C only started: B must rank above C.
	var in []Interaction
	for u := int64(1); u <= 4; u++ {
		in = append(in, Interaction{u, 1, 1}, Interaction{u, 2, CompletionWeight(1)}, Interaction{u, 3, CompletionWeight(0.05)})
	}
	nb := neighborsOf(CoWatch(in, CoWatchConfig{MinSupport: 3}), 1)
	if nb[2].Score <= nb[3].Score {
		t.Errorf("finished B %v should beat started C %v", nb[2].Score, nb[3].Score)
	}
	// Duplicate rows keep the max weight; zero weights and ids are ignored.
	in = append(in, Interaction{1, 2, 0.1}, Interaction{0, 2, 1}, Interaction{1, 0, 1}, Interaction{1, 9, 0})
	nb2 := neighborsOf(CoWatch(in, CoWatchConfig{MinSupport: 3}), 1)
	if !near(nb2[2].Score, nb[2].Score) {
		t.Errorf("duplicates changed score %v -> %v", nb[2].Score, nb2[2].Score)
	}
}

func TestCoWatchHeavyUserCap(t *testing.T) {
	var in []Interaction
	for it := int64(1); it <= 50; it++ {
		in = append(in, Interaction{1, it, float64(it) / 50})
	}
	got := CoWatch(in, CoWatchConfig{MinSupport: 1, MaxItemsPerUser: 5})
	for _, n := range got {
		if n.ItemID < 46 || n.NeighborID < 46 {
			t.Fatalf("only the 5 heaviest items should pair, got %+v", n)
		}
	}
	if len(got) != 5*4 {
		t.Errorf("want 20 directed pairs, got %d", len(got))
	}
}

func TestContentNeighbors(t *testing.T) {
	docs := []ReleaseDoc{
		{ID: 1, Order: 1, Tags: []string{"drama"}, Maker: "M1", Series: "S1"},
		{ID: 2, Order: 2, Tags: []string{"comedy"}, Maker: "M2", Series: "S1"},
		{ID: 3, Order: 3, Tags: []string{"drama", "office"}, Maker: "M1"},
		{ID: 4, Order: 4, Tags: []string{"office", "drama"}, Maker: "M3"},
		{ID: 5, Order: 5, Tags: []string{"other"}, Maker: "M4"},
	}
	got := ContentNeighbors(ReleaseFeatureSets(docs), ReleaseContentConfig)
	byID := func(id int64) map[int64]ContentMatch {
		m := map[int64]ContentMatch{}
		for _, x := range got[id] {
			m[x.ID] = x
		}
		return m
	}
	n1 := byID(1)
	if n1[2].Kind != "s" || ContentReason(n1[2].Kind) != ReasonSeries {
		t.Errorf("1~2 share only the series: %+v", n1[2])
	}
	if n1[3].Kind != "m" || ContentReason(n1[3].Kind) != ReasonMaker {
		t.Errorf("1~3 dominated by maker: %+v", n1[3])
	}
	if n4 := byID(4); n4[3].Kind != "t" || ContentReason("t") != ReasonTags || !slices.Contains(n4[3].Shared, "t:office") {
		t.Errorf("4~3 share tags: %+v", n4[3])
	}
	if len(got[5]) != 0 {
		t.Errorf("5 shares nothing: %+v", got[5])
	}
	for id, ms := range got {
		for _, m := range ms {
			if m.ID == id || m.Score <= 0 || m.Score > 1 {
				t.Errorf("bad match %d -> %+v", id, m)
			}
		}
	}
}

func TestContentNeighborsPostingCap(t *testing.T) {
	// 100 releases of one maker: with cap 10 each only meets its ~10
	// nearest by Order.
	var sets []FeatureSet
	for i := int64(1); i <= 100; i++ {
		sets = append(sets, FeatureSet{ID: i, Order: i, Features: map[string]float64{"m:big": 1}})
	}
	got := ContentNeighbors(sets, ContentConfig{PostingCap: 10, TopK: 100})
	for _, m := range got[50] {
		if m.ID < 44 || m.ID > 56 {
			t.Errorf("50 compared with far release %d", m.ID)
		}
	}
	if n := len(got[50]); n != 10 {
		t.Errorf("want 10 neighbours in the window, got %d", n)
	}
	if n := len(got[1]); n != 10 {
		t.Errorf("edge window: want 10, got %d", n)
	}
}

func TestPerformerSimilarity(t *testing.T) {
	// 1 and 2 co-star in three releases; 3 shares tags/maker with 1 only.
	docs := []ReleaseDoc{
		{ID: 10, Tags: []string{"a"}, Maker: "M", Performers: []int64{1, 2}},
		{ID: 11, Tags: []string{"a"}, Maker: "M", Performers: []int64{1, 2}},
		{ID: 12, Tags: []string{"b"}, Maker: "N", Performers: []int64{2, 1}},
		{ID: 13, Tags: []string{"a"}, Maker: "M", Performers: []int64{3}},
		{ID: 14, Tags: []string{"z"}, Maker: "Z", Performers: []int64{4}},
	}
	if n := CoStarCounts(docs)[[2]int64{1, 2}]; n != 3 {
		t.Errorf("co-star 1-2 = %d, want 3", n)
	}
	got := PerformerSimilarity(docs, nil, Weights{Metadata: 0.5, CoStar: 0.5}, PerformerConfig{})
	find := func(a, b int64) *PerformerMatch {
		for i := range got {
			if got[i].PerformerID == a && got[i].SimilarID == b {
				return &got[i]
			}
		}
		return nil
	}
	m12 := find(1, 2)
	if m12 == nil || m12.Evidence.CoStar != 3 || !near(m12.Evidence.CoStarScore, 0.6) {
		t.Fatalf("1~2 = %+v", m12)
	}
	m13 := find(1, 3)
	if m13 == nil || m13.Evidence.CoStar != 0 || m13.Evidence.Metadata <= 0 || len(m13.Evidence.SharedMakers)+len(m13.Evidence.SharedTags) == 0 {
		t.Fatalf("1~3 = %+v", m13)
	}
	if find(1, 4) != nil {
		t.Errorf("4 shares nothing with 1")
	}
	// Co-star-only weights: 1~3 disappears, 1~2 = CoStarScore(3).
	got = PerformerSimilarity(docs, nil, Weights{CoStar: 1}, PerformerConfig{})
	if find(1, 3) != nil {
		t.Errorf("metadata weight 0 should drop 1~3")
	}
	if m := find(1, 2); m == nil || !near(m.Score, 0.6) {
		t.Errorf("co-star only 1~2 = %+v, want 0.6", m)
	}
	// Audience co-watch: users watching 3's and 4's works.
	var aud []Interaction
	for u := int64(1); u <= 5; u++ {
		aud = append(aud, Interaction{u, 13, 1}, Interaction{u, 14, 1})
	}
	got = PerformerSimilarity(docs, PerformerInteractions(aud, docs), Weights{CoWatch: 1}, PerformerConfig{})
	if m := find(3, 4); m == nil || m.Evidence.CoWatch <= 0.3 {
		t.Errorf("audience overlap 3~4 = %+v", m)
	}
	// Invalid weights fall back to the defaults instead of dividing by 0.
	if got = PerformerSimilarity(docs, nil, Weights{}, PerformerConfig{}); len(got) == 0 {
		t.Errorf("zero weights should use defaults")
	}
}

func TestCoStarSkipsCompilations(t *testing.T) {
	ps := make([]int64, MaxCastForCoStar+1)
	for i := range ps {
		ps[i] = int64(i + 1)
	}
	if n := len(CoStarCounts([]ReleaseDoc{{ID: 1, Performers: ps}})); n != 0 {
		t.Errorf("compilation produced %d pairs", n)
	}
}

func TestWeightsValidate(t *testing.T) {
	for _, c := range []struct {
		w  Weights
		ok bool
	}{
		{DefaultWeights, true},
		{Weights{Metadata: 1}, true},
		{Weights{Visual: 1}, false},
		{Weights{}, false},
		{Weights{Metadata: 1.5}, false},
		{Weights{Metadata: -0.1, CoStar: 1}, false},
		{Weights{Metadata: math.NaN(), CoStar: 1}, false},
	} {
		if got := c.w.Validate() == ""; got != c.ok {
			t.Errorf("Validate(%+v) ok=%v, want %v", c.w, got, c.ok)
		}
	}
}

func TestPerformerReason(t *testing.T) {
	for _, c := range []struct {
		ev   Evidence
		want string
	}{
		{Evidence{CoStar: 6, CoWatch: 0.5}, "共演 6 部、观众高度重合"},
		{Evidence{CoStar: 2, Metadata: 0.5, SharedTags: []string{"剧情"}}, "共演 2 部、风格相近 · 剧情"},
		{Evidence{CoWatch: 0.15}, "观众重合"},
		{Evidence{Metadata: 0.4, SharedMakers: []string{"S1"}}, "同厂商 · S1"},
		{Evidence{CoStar: 1}, "共演 1 部"},
		{Evidence{Metadata: 0.1}, "相似演员"},
	} {
		if got := PerformerReason(c.ev); got != c.want {
			t.Errorf("PerformerReason(%+v) = %q, want %q", c.ev, got, c.want)
		}
	}
}

func TestTrendingScore(t *testing.T) {
	if TrendingScore(0, 10, 1) != 0 {
		t.Error("no plays scores 0")
	}
	base := TrendingScore(100, 100, 0.5)
	if !near(base, math.Log1p(100)*1.0*(1+GrowthWeight*0)) {
		t.Errorf("flat growth score = %v", base)
	}
	if TrendingScore(100, 100, 0.9) <= base {
		t.Error("completion should lift the score")
	}
	if TrendingScore(100, 20, 0.5) <= base || TrendingScore(100, 400, 0.5) >= base {
		t.Error("growth should lift, decline should lower")
	}
	if g := Growth(1000, 0); g != 3 {
		t.Errorf("growth clamps at 3, got %v", g)
	}
	if g := Growth(0, 1000); g < -1 || g > -0.99 {
		t.Errorf("full decline approaches -1, got %v", g)
	}
	// More plays beat fewer at equal shape; tie-breaks are deterministic.
	rows := RankTrending([]TrendInput{
		{ID: 3, Plays: 10, PrevPlays: 10, Completed: 5},
		{ID: 1, Plays: 50, PrevPlays: 50, Completed: 25},
		{ID: 2, Plays: 10, PrevPlays: 10, Completed: 5},
		{ID: 9, Plays: 0, PrevPlays: 30},
	}, 0)
	if len(rows) != 3 || rows[0].ID != 1 || rows[1].ID != 2 || rows[2].ID != 3 || rows[2].Rank != 3 {
		t.Errorf("RankTrending = %+v", rows)
	}
	if !near(rows[0].Completion, 0.5) {
		t.Errorf("completion = %v", rows[0].Completion)
	}
	if got := RankTrending([]TrendInput{{ID: 1, Plays: 2}, {ID: 2, Plays: 1}}, 1); len(got) != 1 {
		t.Errorf("limit not applied: %+v", got)
	}
	heat := RankHeat([]HeatInput{{ID: 1, Plays: 10}, {ID: 2, NewFollows: 50}, {ID: 3}}, 0)
	if len(heat) != 2 || heat[0].NewFollows+heat[1].NewFollows != 50 {
		t.Errorf("RankHeat = %+v", heat)
	}
}

func TestMovement(t *testing.T) {
	p := func(n int) *int { return &n }
	for _, c := range []struct {
		rank  int
		prev  *int
		mv    string
		delta int
	}{{1, nil, "new", 0}, {1, p(4), "up", 3}, {5, p(2), "down", -3}, {3, p(3), "same", 0}} {
		if mv, d := Movement(c.rank, c.prev); mv != c.mv || d != c.delta {
			t.Errorf("Movement(%d,%v) = %s %d, want %s %d", c.rank, c.prev, mv, d, c.mv, c.delta)
		}
	}
}

// ---------------------------------------------------------------- online

func infos(xs ...Info) map[int64]Info {
	m := map[int64]Info{}
	for _, x := range xs {
		if !x.Playable {
			x.Playable = true
		}
		m[x.ReleaseID] = x
	}
	return m
}

func ids(rs []Rec) []int64 {
	out := make([]int64, len(rs))
	for i, r := range rs {
		out[i] = r.ReleaseID
	}
	return out
}

func TestRankMergesAndScores(t *testing.T) {
	cands := []Candidate{
		{ReleaseID: 1, Channel: ChanNew, Raw: 1},
		{ReleaseID: 2, Channel: ReasonCoWatch, Raw: 0.8},
		{ReleaseID: 2, Channel: ChanTrending, Raw: 5, Label: PeriodWeek, BoardRank: 1}, // duplicate release
		{ReleaseID: 3, Channel: ReasonCoWatch, Raw: 0.4},
		{ReleaseID: 4, Channel: "unknown", Raw: 9},
	}
	got := Rank(cands, infos(Info{ReleaseID: 1}, Info{ReleaseID: 2}, Info{ReleaseID: 3}, Info{ReleaseID: 4}), Options{})
	if !slices.Equal(ids(got), []int64{2, 3, 1}) {
		t.Fatalf("order = %v", ids(got))
	}
	// 2: cowatch 1.0·(0.5+0.5) = 1, plus 0.15 · trending 0.5 = 1.075.
	if !near(got[0].Score, 1.075) || got[0].Reason.Code != "cowatch" || !slices.Equal(got[0].Channels, []string{ReasonCoWatch, ChanTrending}) {
		t.Errorf("merged = %+v", got[0])
	}
	// 3: cowatch normalized 0.4/0.8 -> 1·0.75.
	if !near(got[1].Score, 0.75) {
		t.Errorf("3 score = %v", got[1].Score)
	}
}

func TestRankFilters(t *testing.T) {
	var cands []Candidate
	for id := int64(1); id <= 7; id++ {
		cands = append(cands, Candidate{ReleaseID: id, Channel: ChanNew, Raw: float64(10 - id)})
	}
	in := infos(Info{ReleaseID: 1}, Info{ReleaseID: 2}, Info{ReleaseID: 3}, Info{ReleaseID: 4}, Info{ReleaseID: 6}, Info{ReleaseID: 7})
	x := in[4]
	x.Playable = false
	in[4] = x
	// 5 is missing from infos (held_review / merged / draft).
	opt := Options{Seed: 1, Watched: map[int64]bool{2: true}, Exclude: map[int64]bool{3: true}, RequirePlayable: true,
		Keep: func(i Info) bool { return i.ReleaseID != 7 }}
	if got := ids(Rank(cands, in, opt)); !slices.Equal(got, []int64{6}) {
		t.Errorf("filtered = %v, want [6]", got)
	}
	// Without any playback sources, unplayable releases are allowed.
	opt.RequirePlayable = false
	if got := ids(Rank(cands, in, opt)); !slices.Equal(got, []int64{4, 6}) {
		t.Errorf("no playable requirement = %v, want [4 6]", got)
	}
}

func TestDiversifyCapsPerformerRuns(t *testing.T) {
	mk := func(id, perf int64) Rec { return Rec{Info: Info{ReleaseID: id, PerformerID: perf}} }
	in := []Rec{mk(1, 7), mk(2, 7), mk(3, 7), mk(4, 7), mk(5, 8), mk(6, 0), mk(7, 0), mk(8, 0)}
	got := Diversify(in, 2)
	if !slices.Equal(ids(got), []int64{1, 2, 5, 3, 4, 6, 7, 8}) {
		t.Errorf("Diversify = %v", ids(got))
	}
	for i := 2; i < len(got); i++ {
		p := got[i].PerformerID
		if p != 0 && got[i-1].PerformerID == p && got[i-2].PerformerID == p {
			t.Errorf("run of 3 for performer %d at %d", p, i)
		}
	}
	// Nothing else left: the tail keeps its order rather than vanishing.
	if got := Diversify([]Rec{mk(1, 7), mk(2, 7), mk(3, 7)}, 2); len(got) != 3 {
		t.Errorf("items lost: %v", ids(got))
	}
	// Performer-less items never collide.
	if got := Diversify([]Rec{mk(1, 0), mk(2, 0), mk(3, 0)}, 2); !slices.Equal(ids(got), []int64{1, 2, 3}) {
		t.Errorf("zero performer reordered: %v", ids(got))
	}
}

func TestRankAppliesDiversity(t *testing.T) {
	var cands []Candidate
	in := map[int64]Info{}
	for id := int64(1); id <= 6; id++ {
		perf := int64(1)
		if id == 6 {
			perf = 2
		}
		cands = append(cands, Candidate{ReleaseID: id, Channel: ChanSamePerformer, Raw: float64(10 - id), PerformerName: "A"})
		in[id] = Info{ReleaseID: id, PerformerID: perf, Playable: true}
	}
	got := ids(Rank(cands, in, Options{}))
	if !slices.Equal(got[:3], []int64{1, 2, 6}) {
		t.Errorf("rank = %v, want performer 2 third", got)
	}
}

func TestReasons(t *testing.T) {
	info := Info{Series: "S", Maker: "M", PerformerName: "林遥", SharedTag: "剧情"}
	for _, c := range []struct {
		cand Candidate
		code string
		text string
	}{
		{Candidate{Channel: ReasonCoWatch}, "cowatch", "看过此视频的人也看了"},
		{Candidate{Channel: ReasonCoWatch, SeedTitle: "夏日"}, "because_watched", "因为你看过《夏日》"},
		{Candidate{Channel: ReasonSeries}, "same_series", "同系列 · S"},
		{Candidate{Channel: ReasonMaker}, "same_maker", "同厂商 · M"},
		{Candidate{Channel: ReasonTags}, "similar_tags", "相似标签 · 剧情"},
		{Candidate{Channel: ChanSamePerformer, PerformerName: "林遥"}, "same_performer", "同演员 · 林遥"},
		{Candidate{Channel: ChanSamePerformer, PerformerName: "林遥", SeedTitle: "x"}, "same_performer", "因为你看过 林遥 的作品"},
		{Candidate{Channel: ChanFollowedNew, PerformerName: "林遥"}, "followed_new", "你关注的 林遥 有新作"},
		{Candidate{Channel: ChanTrending, Label: PeriodToday, BoardRank: 3}, "trending", "今日热门 · 第 3 名"},
		{Candidate{Channel: ChanNew}, "new", "新上线"},
	} {
		r := ReasonFor(c.cand, info)
		if r.Code != c.code || r.Text != c.text {
			t.Errorf("ReasonFor(%+v) = %+v, want %s %q", c.cand, r, c.code, c.text)
		}
	}
	if r := ReasonFor(Candidate{Channel: ReasonTags}, Info{}); r.Text != "内容相似" {
		t.Errorf("tags without shared tag: %q", r.Text)
	}
}

func TestTonight(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	for _, c := range []struct {
		bucket  string
		runtime int
		series  string
		want    bool
	}{
		{"", 0, "", true}, {"any", 999, "", true},
		{"30", 25, "", true}, {"30", 31, "", false}, {"30", 0, "", false},
		{"60", 60, "", true}, {"60", 44, "", false}, {"60", 91, "", false},
		{"120", 120, "", true}, {"120", 119, "", false},
		{"series", 0, "S", true}, {"series", 100, "", false},
	} {
		if got := DurationMatch(c.bucket, c.runtime, c.series); got != c.want {
			t.Errorf("DurationMatch(%q,%d,%q) = %v", c.bucket, c.runtime, c.series, got)
		}
	}
	keep := TonightKeep("")
	if keep(Info{Rating: f(7.4)}) || !keep(Info{Rating: f(7.5)}) || !keep(Info{}) {
		t.Error("rating filter: < 7.5 dropped, unrated kept")
	}
	// Demote: a candidate sharing the excluded one's maker drops below.
	cands := []Candidate{{ReleaseID: 1, Channel: ChanNew, Raw: 3}, {ReleaseID: 2, Channel: ChanNew, Raw: 2.9}, {ReleaseID: 3, Channel: ChanNew, Raw: 1}}
	in := infos(Info{ReleaseID: 1, Maker: "X"}, Info{ReleaseID: 2}, Info{ReleaseID: 3}, Info{ReleaseID: 9, Maker: "X"})
	got := ids(Rank(cands, in, Options{Exclude: map[int64]bool{9: true}, Demote: []Info{in[9]}}))
	if !slices.Equal(got, []int64{2, 1, 3}) {
		t.Errorf("demoted order = %v", got)
	}
	pick, alts := Tonight(Rank(append(cands, Candidate{ReleaseID: 4, Channel: ChanNew}, Candidate{ReleaseID: 5, Channel: ChanNew}),
		infos(Info{ReleaseID: 1}, Info{ReleaseID: 2}, Info{ReleaseID: 3}, Info{ReleaseID: 4}, Info{ReleaseID: 5}), Options{}))
	if pick == nil || pick.ReleaseID != 1 || len(alts) != 3 {
		t.Errorf("Tonight = %v + %v", pick, ids(alts))
	}
	if pick, alts := Tonight(nil); pick != nil || alts == nil || len(alts) != 0 {
		t.Error("empty tonight")
	}
}

// ---------------------------------------------------------------- runner

type fakeStore struct {
	docs     []ReleaseDoc
	inter    []Interaction
	weights  Weights
	release  []ReleaseSimilarRow
	perf     []PerformerMatch
	trending map[string][]TrendRow
	heat     map[string][]TrendRow
	runs     []audit.Run
	windows  [][3]time.Time
}

func (f *fakeStore) SimilarityWeights(context.Context) (Weights, error) { return f.weights, nil }
func (f *fakeStore) ReleaseDocs(context.Context) ([]ReleaseDoc, error)  { return f.docs, nil }
func (f *fakeStore) WatchInteractions(context.Context, time.Time, time.Time) ([]Interaction, error) {
	return f.inter, nil
}
func (f *fakeStore) ReplaceReleaseSimilar(_ context.Context, rows []ReleaseSimilarRow, _ time.Time) error {
	f.release = rows
	return nil
}
func (f *fakeStore) ReplacePerformerSimilar(_ context.Context, rows []PerformerMatch, _ time.Time) error {
	f.perf = rows
	return nil
}
func (f *fakeStore) TrendingInputs(_ context.Context, a, b, c time.Time) ([]TrendInput, error) {
	f.windows = append(f.windows, [3]time.Time{a, b, c})
	return []TrendInput{{ID: 1, Plays: 3}, {ID: 2, Plays: 5}}, nil
}
func (f *fakeStore) PerformerHeatInputs(context.Context, time.Time, time.Time, time.Time) ([]HeatInput, error) {
	return []HeatInput{{ID: 7, Plays: 1}}, nil
}
func (f *fakeStore) ReplaceTrending(_ context.Context, p string, rows []TrendRow, _ time.Time) error {
	f.trending[p] = rows
	return nil
}
func (f *fakeStore) ReplacePerformerHeat(_ context.Context, p string, rows []TrendRow, _ time.Time) error {
	f.heat[p] = rows
	return nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func TestRunnerSimilarity(t *testing.T) {
	f := &fakeStore{weights: DefaultWeights, trending: map[string][]TrendRow{}, heat: map[string][]TrendRow{}}
	for id := int64(1); id <= 3; id++ {
		f.docs = append(f.docs, ReleaseDoc{ID: id, Tags: []string{"t"}, Maker: "M", Performers: []int64{100 + id}})
	}
	for u := int64(1); u <= 3; u++ {
		f.inter = append(f.inter, Interaction{u, 1, 1}, Interaction{u, 2, -1})
	}
	rep, err := (&Runner{Store: f, JobID: 5}).RunSimilarity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.CoWatchRows != 2 || rep.ContentRows == 0 || rep.PerformerRows == 0 || rep.Releases != 3 || rep.Interactions != 6 {
		t.Errorf("report = %+v", rep)
	}
	reasons := map[string]int{}
	for _, r := range f.release {
		reasons[r.Reason]++
	}
	if reasons[ReasonCoWatch] != 2 || reasons[ReasonMaker]+reasons[ReasonTags] == 0 {
		t.Errorf("reasons = %v", reasons)
	}
	if len(f.runs) != 1 || f.runs[0].Job != "recs.similarity" || f.runs[0].Result != audit.ResultOK || f.runs[0].JobID != 5 {
		t.Errorf("runs = %+v", f.runs)
	}
}

func TestRunnerTrending(t *testing.T) {
	f := &fakeStore{trending: map[string][]TrendRow{}, heat: map[string][]TrendRow{}}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	rep, err := (&Runner{Store: f, Now: func() time.Time { return now }}).RunTrending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Periods {
		if rows := f.trending[p]; len(rows) != 2 || rows[0].ID != 2 || rep.Releases[p] != 2 || len(f.heat[p]) != 1 {
			t.Errorf("%s: rows %+v heat %+v", p, rows, f.heat[p])
		}
	}
	if w := f.windows[1]; !w[2].Equal(now) || !w[1].Equal(now.Add(-7*24*time.Hour)) || !w[0].Equal(now.Add(-14*24*time.Hour)) {
		t.Errorf("week window = %v", w)
	}
	if len(f.runs) != 1 || f.runs[0].Job != "recs.trending" {
		t.Errorf("runs = %+v", f.runs)
	}
}

// ---------------------------------------------------------------- benchmarks

// synthetic builds a request-sized candidate set: ~360 candidates over 300
// releases from every channel, 1 000 watched ids.
func synthetic(r *rand.Rand) ([]Candidate, map[int64]Info, Options) {
	chans := []string{ReasonCoWatch, ReasonSeries, ReasonTags, ChanSamePerformer, ChanFollowedNew, ChanTrending, ChanNew}
	var cands []Candidate
	in := map[int64]Info{}
	for i := 0; i < 360; i++ {
		id := int64(r.IntN(300) + 1)
		cands = append(cands, Candidate{ReleaseID: id, Channel: chans[r.IntN(len(chans))], Raw: r.Float64(), PerformerName: "P", BoardRank: i})
		in[id] = Info{ReleaseID: id, PerformerID: int64(r.IntN(20)), Playable: r.IntN(10) > 0, Title: fmt.Sprint(id)}
	}
	watched := map[int64]bool{}
	for i := 0; i < 1000; i++ {
		watched[int64(r.IntN(5000))] = true
	}
	return cands, in, Options{Watched: watched, RequirePlayable: true}
}

func BenchmarkRank(b *testing.B) {
	cands, in, opt := synthetic(rand.New(rand.NewPCG(1, 2)))
	b.ReportAllocs()
	for b.Loop() {
		Rank(cands, in, opt)
	}
}

func BenchmarkCoWatch(b *testing.B) {
	// 2 000 users × 30 watches over 5 000 releases.
	r := rand.New(rand.NewPCG(3, 4))
	var in []Interaction
	for u := int64(1); u <= 2000; u++ {
		for k := 0; k < 30; k++ {
			in = append(in, Interaction{u, int64(r.IntN(5000) + 1), r.Float64()})
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		CoWatch(in, CoWatchConfig{})
	}
}
