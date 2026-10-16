package recs

import (
	"cmp"
	"slices"
	"strconv"
	"time"
)

// Slots served by GET /api/recs (tech plan 3.5).
const (
	SlotHome              = "home"
	SlotRelated           = "related"
	SlotSimilarPerformers = "similar_performers"
	SlotPerformers        = "performers"
	SlotTonight           = "tonight"
)

// ValidSlot reports whether s is a known slot.
func ValidSlot(s string) bool {
	switch s {
	case SlotHome, SlotRelated, SlotSimilarPerformers, SlotPerformers, SlotTonight:
		return true
	}
	return false
}

// Recall channels. release_similar.reason values double as channels.
const (
	ReasonCoWatch = "cowatch"
	ReasonSeries  = "series"
	ReasonMaker   = "maker"
	ReasonTags    = "tags"

	ChanSamePerformer = "same_performer"
	ChanFollowedNew   = "followed_new"
	ChanTrending      = "trending"
	ChanNew           = "new"
)

// ChannelWeights is the prior of each channel: roughly how likely a click
// that turns into a long watch is (PRD 10 预估点击 × 预估观看时长), tuned by hand
// until V1.1 learns it.
var ChannelWeights = map[string]float64{
	ReasonCoWatch:     1.0,
	ChanFollowedNew:   0.95,
	ReasonSeries:      0.85,
	ChanSamePerformer: 0.8,
	ChanInterests:     0.7,
	ReasonMaker:       0.6,
	ReasonTags:        0.6,
	ChanTrending:      0.5,
	ChanNew:           0.4,
}

// Candidate is one recall hit. Raw is the channel's own score (bigger is
// better; normalized per channel by Rank). The other fields feed the reason.
type Candidate struct {
	ReleaseID     int64
	Channel       string
	Raw           float64
	SeedID        int64  // co-watch from history: the watched release
	SeedTitle     string // ...and its title
	PerformerID   int64  // same_performer / followed_new
	PerformerName string
	Label         string // trending: period; tags: the shared tag; interests: the matched tag
	BoardRank     int    // trending: rank on the board
}

// Info is the hydrated release a candidate points at.
type Info struct {
	ReleaseID      int64      `json:"id"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	RuntimeMinutes int        `json:"runtime_minutes,omitempty"`
	ReleaseDate    *time.Time `json:"release_date,omitempty"`
	Maker          string     `json:"maker_name,omitempty"`
	Series         string     `json:"series_name,omitempty"`
	PerformerID    int64      `json:"-"` // primary performer, 0 = none
	PerformerSlug  string     `json:"-"`
	PerformerName  string     `json:"-"`
	Playable       bool       `json:"playable"`
	HasLang        bool       `json:"-"`      // audio or subtitles in the request language
	Rating         *float64   `json:"rating"` // 10-point; nil below the vote minimum
	SharedTag      string     `json:"-"`      // related: first tag shared with the seed
	Tags           []string   `json:"-"`      // normalized tags (soft preferences)
}

// Reason is the explanation shown with every recommendation (PRD 10 可解释).
type Reason struct {
	Code string `json:"code"`
	Text string `json:"text"`
	// Name is the performer the reason refers to (same_performer,
	// followed_new). It is not always the item's primary performer, so
	// clients that localize the text or list followed names must use it.
	Name string `json:"name,omitempty"`
}

// Rec is one ranked recommendation.
type Rec struct {
	Info
	Score    float64   `json:"score"`
	Reason   Reason    `json:"reason"`
	Channels []string  `json:"channels"`
	Best     Candidate `json:"-"` // the channel hit the reason came from
}

// Options control filtering and re-ranking.
type Options struct {
	Seed            int64          // the release being watched (never recommended)
	Watched         map[int64]bool // logged-in user's history: filtered out
	Exclude         map[int64]bool // explicit exclusions (tonight 换一个)
	RequirePlayable bool           // true once any playback source exists
	MaxRun          int            // max consecutive recs with the same primary performer, default 2
	Keep            func(Info) bool
	// Demote lowers candidates that share a maker or primary performer with
	// these (tonight: 「换一个」会降低同类型权重).
	Demote []Info
	// Prefer are soft preferences (cold-start interests, Tonight mood /
	// company): they multiply the score, never filter (prefs.go).
	Prefer []Preference
	// Feedback is the viewer's 推荐反馈 (feedback.go): releases marked 不感兴趣
	// and those led by a performer marked 不推荐 are dropped, recall hits
	// through such a performer ignored, and marked tags demoted.
	Feedback Feedback
}

// DemoteFactor multiplies the score of a demoted candidate per match.
const DemoteFactor = 0.7

// Rank merges candidates per release, scores them, filters and re-ranks:
//
//	channel score = ChannelWeights[ch] · (0.5 + 0.5 · raw / max raw in ch)
//	merged        = best channel + 0.15 · Σ other channels
//	× 1.1 when the release has audio / subtitles in the request language
//	× (0.9 + 0.02 · rating) when a rating is shown (8.0 → ×1.06)
//	× each soft preference's Factor (matching tags / runtime)
//	× TagFeedbackFactor when a tag is marked 不感兴趣 (推荐反馈)
//
// Releases missing from infos (not published, held, merged) are dropped,
// as are the seed, watched and excluded ones, those dropped by Feedback
// (marked 不感兴趣, or led by a performer marked 不推荐), unplayable ones when
// RequirePlayable, and those Keep rejects. The reason comes from the best
// channel. Finally Diversify caps runs of the same performer.
func Rank(cands []Candidate, infos map[int64]Info, opt Options) []Rec {
	maxRaw := map[string]float64{}
	for _, c := range cands {
		maxRaw[c.Channel] = max(maxRaw[c.Channel], c.Raw)
	}
	type acc struct {
		best      Candidate
		bestScore float64
		sum       float64
		channels  []string
	}
	byID := map[int64]*acc{}
	var order []int64
	for _, c := range cands {
		w, ok := ChannelWeights[c.Channel]
		if !ok || opt.Feedback.ignoresHit(c) {
			continue
		}
		norm := 1.0
		if m := maxRaw[c.Channel]; m > 0 {
			norm = max(0, c.Raw) / m
		}
		s := w * (0.5 + 0.5*norm)
		a := byID[c.ReleaseID]
		if a == nil {
			a = &acc{}
			byID[c.ReleaseID] = a
			order = append(order, c.ReleaseID)
		}
		a.sum += s
		if s > a.bestScore {
			a.best, a.bestScore = c, s
		}
		if !slices.Contains(a.channels, c.Channel) {
			a.channels = append(a.channels, c.Channel)
		}
	}
	out := make([]Rec, 0, len(order))
	for _, id := range order {
		a := byID[id]
		info, ok := infos[id]
		if !ok || id == opt.Seed || opt.Watched[id] || opt.Exclude[id] {
			continue
		}
		if opt.Feedback.drops(info) {
			continue
		}
		if opt.RequirePlayable && !info.Playable {
			continue
		}
		if opt.Keep != nil && !opt.Keep(info) {
			continue
		}
		score := a.bestScore + 0.15*(a.sum-a.bestScore)
		if info.HasLang {
			score *= 1.1
		}
		if info.Rating != nil {
			score *= 0.9 + 0.02**info.Rating
		}
		for _, p := range opt.Prefer {
			score *= p.Factor(info)
		}
		score *= opt.Feedback.factor(info)
		for _, d := range opt.Demote {
			if (d.Maker != "" && d.Maker == info.Maker) || (d.PerformerID != 0 && d.PerformerID == info.PerformerID) {
				score *= DemoteFactor
			}
		}
		out = append(out, Rec{Info: info, Score: round3(score), Reason: ReasonFor(a.best, info), Channels: a.channels, Best: a.best})
	}
	slices.SortStableFunc(out, func(x, y Rec) int {
		if c := cmp.Compare(y.Score, x.Score); c != 0 {
			return c
		}
		return cmp.Compare(x.ReleaseID, y.ReleaseID)
	})
	return Diversify(out, opt.MaxRun)
}

// Diversify reorders recs so no more than maxRun (default 2) consecutive
// items share a primary performer (PRD 10 同一演员连续不超过 2 个). It walks the
// list in order and, when the next item would extend a run past maxRun,
// takes the best later item that does not; when none is left the remaining
// items are appended as they are. Items without a performer never collide.
func Diversify(recs []Rec, maxRun int) []Rec {
	if maxRun <= 0 {
		maxRun = 2
	}
	rest := slices.Clone(recs)
	out := make([]Rec, 0, len(recs))
	runOf := func(p int64) int {
		if p == 0 {
			return 0
		}
		n := 0
		for i := len(out) - 1; i >= 0 && out[i].PerformerID == p; i-- {
			n++
		}
		return n
	}
	for len(rest) > 0 {
		pick := -1
		for i, r := range rest {
			if runOf(r.PerformerID) < maxRun {
				pick = i
				break
			}
		}
		if pick < 0 {
			out = append(out, rest...)
			break
		}
		out = append(out, rest[pick])
		rest = slices.Delete(rest, pick, pick+1)
	}
	return out
}

// ReasonFor builds the reason code and Chinese text for a candidate.
func ReasonFor(c Candidate, info Info) Reason {
	switch c.Channel {
	case ReasonCoWatch:
		if c.SeedTitle != "" {
			return Reason{Code: "because_watched", Text: "因为你看过《" + c.SeedTitle + "》"}
		}
		return Reason{Code: "cowatch", Text: "看过此视频的人也看了"}
	case ReasonSeries:
		if info.Series != "" {
			return Reason{Code: "same_series", Text: "同系列 · " + info.Series}
		}
		return Reason{Code: "same_series", Text: "同系列"}
	case ReasonMaker:
		if info.Maker != "" {
			return Reason{Code: "same_maker", Text: "同厂商 · " + info.Maker}
		}
		return Reason{Code: "same_maker", Text: "同厂商"}
	case ReasonTags:
		if t := firstNonEmpty(c.Label, info.SharedTag); t != "" {
			return Reason{Code: "similar_tags", Text: "相似标签 · " + t}
		}
		return Reason{Code: "similar_tags", Text: "内容相似"}
	case ChanSamePerformer:
		name := firstNonEmpty(c.PerformerName, info.PerformerName)
		if c.SeedTitle != "" {
			return Reason{Code: "same_performer", Text: "因为你看过 " + name + " 的作品", Name: name}
		}
		return Reason{Code: "same_performer", Text: "同演员 · " + name, Name: name}
	case ChanFollowedNew:
		return Reason{Code: "followed_new", Text: "你关注的 " + c.PerformerName + " 有新作", Name: c.PerformerName}
	case ChanInterests:
		if c.Label != "" {
			return Reason{Code: "interests", Text: "你选的兴趣 · " + c.Label}
		}
		return Reason{Code: "interests", Text: "符合你选的兴趣"}
	case ChanTrending:
		text := PeriodLabel(c.Label)
		if c.BoardRank > 0 {
			text += " · 第 " + strconv.Itoa(c.BoardRank) + " 名"
		}
		return Reason{Code: "trending", Text: text}
	}
	return Reason{Code: "new", Text: "新上线"}
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }

// Tonight (PRD 18) duration buckets.
const (
	DurationAny    = "any"
	DurationShort  = "30"     // 30 分钟内
	DurationHour   = "60"     // 1 小时左右: 45–90 min
	DurationLong   = "120"    // 2 小时以上
	DurationSeries = "series" // 剧集
)

// ValidDuration reports whether d is a tonight duration bucket ("" = any).
func ValidDuration(d string) bool {
	switch d {
	case "", DurationAny, DurationShort, DurationHour, DurationLong, DurationSeries:
		return true
	}
	return false
}

// DurationMatch reports whether a release fits the bucket. An unknown
// runtime only fits "any" (and "series" when the release has a series).
func DurationMatch(bucket string, runtimeMinutes int, series string) bool {
	switch bucket {
	case "", DurationAny:
		return true
	case DurationSeries:
		return series != ""
	}
	if runtimeMinutes <= 0 {
		return false
	}
	switch bucket {
	case DurationShort:
		return runtimeMinutes <= 30
	case DurationHour:
		return runtimeMinutes >= 45 && runtimeMinutes <= 90
	case DurationLong:
		return runtimeMinutes >= 120
	}
	return false
}

// TonightMinRating is PRD 18's 评分 ≥ 7.5, applied only when the release has a
// shown rating (enough votes); unrated releases stay eligible.
const TonightMinRating = 7.5

// TonightKeep is the tonight filter: duration bucket and minimum rating.
func TonightKeep(bucket string) func(Info) bool {
	return func(i Info) bool {
		if i.Rating != nil && *i.Rating < TonightMinRating {
			return false
		}
		return DurationMatch(bucket, i.RuntimeMinutes, i.Series)
	}
}

// TonightAlternates is how many alternates come with the pick.
const TonightAlternates = 3

// Tonight splits a ranked list into the pick and up to 3 alternates.
func Tonight(ranked []Rec) (*Rec, []Rec) {
	if len(ranked) == 0 {
		return nil, []Rec{}
	}
	alts := ranked[1:min(len(ranked), 1+TonightAlternates)]
	return &ranked[0], slices.Clone(alts)
}
