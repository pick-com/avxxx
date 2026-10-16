package recs

import (
	"cmp"
	"math"
	"slices"
	"time"
)

// Trending periods (PRD 4.2: 今日 / 本周 / 本月 / 年度), rolling windows.
const (
	PeriodToday = "today"
	PeriodWeek  = "week"
	PeriodMonth = "month"
	PeriodYear  = "year"
)

// Periods is the hourly job's order.
var Periods = []string{PeriodToday, PeriodWeek, PeriodMonth, PeriodYear}

// PeriodWindow is the rolling window of a period; growth compares it with
// the window just before it.
func PeriodWindow(period string) (time.Duration, bool) {
	switch period {
	case PeriodToday:
		return 24 * time.Hour, true
	case PeriodWeek:
		return 7 * 24 * time.Hour, true
	case PeriodMonth:
		return 30 * 24 * time.Hour, true
	case PeriodYear:
		return 365 * 24 * time.Hour, true
	}
	return 0, false
}

// PeriodLabel is the Chinese board name used in reasons.
func PeriodLabel(period string) string {
	switch period {
	case PeriodToday:
		return "今日热门"
	case PeriodMonth:
		return "本月热门"
	case PeriodYear:
		return "年度热门"
	}
	return "本周热门"
}

// Trending score parameters.
const (
	GrowthPrior  = 5.0 // plays added to the previous window before dividing
	GrowthWeight = 0.5
	BoardSize    = 200 // rows kept per period
)

// Growth is the relative change against the previous window, smoothed so a
// release going from 0 to 2 plays is not "infinitely" growing:
// (plays - prev) / (prev + GrowthPrior), clamped to [-1, 3].
func Growth(plays, prev int) float64 {
	g := float64(plays-prev) / (float64(prev) + GrowthPrior)
	return max(-1, min(3, g))
}

// TrendingScore combines PRD 4.2's three signals:
//
//	score = ln(1 + plays) · (0.5 + completion) · (1 + GrowthWeight · growth)
//
// plays dominate (log-damped), a high completion rate lifts a release up
// to 1.5×/0.5 = 3× over one nobody finishes, and growth adds up to +150% or
// takes up to -50%.
func TrendingScore(plays, prev int, completion float64) float64 {
	if plays <= 0 {
		return 0
	}
	completion = max(0, min(1, completion))
	return math.Log1p(float64(plays)) * (0.5 + completion) * (1 + GrowthWeight*Growth(plays, prev))
}

// TrendInput is one release's counts for a window.
type TrendInput struct {
	ID        int64
	Plays     int
	PrevPlays int
	Completed int
}

// TrendRow is one ranked board row.
type TrendRow struct {
	Rank       int
	ID         int64
	Score      float64
	Plays      int
	Completion float64
	Growth     float64
	NewFollows int // performer heat only
}

// RankTrending scores and ranks releases with at least one play in the
// window, best first (ties: more plays, then lower id), keeping limit rows.
func RankTrending(in []TrendInput, limit int) []TrendRow {
	var out []TrendRow
	for _, x := range in {
		if x.Plays <= 0 {
			continue
		}
		c := min(1, float64(x.Completed)/float64(x.Plays))
		out = append(out, TrendRow{ID: x.ID, Score: TrendingScore(x.Plays, x.PrevPlays, c), Plays: x.Plays, Completion: c, Growth: Growth(x.Plays, x.PrevPlays)})
	}
	return rank(out, limit)
}

// HeatInput is one performer's counts for a window.
type HeatInput struct {
	ID         int64
	Plays      int
	PrevPlays  int
	NewFollows int
}

// HeatScore ranks performers (PRD 4.2 演员热度榜): plays of their works with
// the same growth term as releases, plus new followers.
//
//	heat = ln(1 + plays) · (1 + GrowthWeight · growth) + 0.5 · ln(1 + new_follows)
func HeatScore(plays, prev, newFollows int) float64 {
	return math.Log1p(float64(max(plays, 0)))*(1+GrowthWeight*Growth(plays, prev)) + 0.5*math.Log1p(float64(max(newFollows, 0)))
}

// RankHeat scores and ranks performers with any plays or new follows.
func RankHeat(in []HeatInput, limit int) []TrendRow {
	var out []TrendRow
	for _, x := range in {
		if x.Plays <= 0 && x.NewFollows <= 0 {
			continue
		}
		out = append(out, TrendRow{ID: x.ID, Score: HeatScore(x.Plays, x.PrevPlays, x.NewFollows), Plays: x.Plays, Growth: Growth(x.Plays, x.PrevPlays), NewFollows: x.NewFollows})
	}
	return rank(out, limit)
}

func rank(out []TrendRow, limit int) []TrendRow {
	slices.SortFunc(out, func(a, b TrendRow) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		if c := cmp.Compare(b.Plays, a.Plays); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// Movement describes a rank change for the board (PRD 4.2 ▲ ▼ 新上榜):
// "new" when there was no previous rank, else "up" / "down" / "same" with
// delta = prev - rank (positive = climbed).
func Movement(rank int, prev *int) (string, int) {
	if prev == nil {
		return "new", 0
	}
	d := *prev - rank
	switch {
	case d > 0:
		return "up", d
	case d < 0:
		return "down", d
	}
	return "same", 0
}
