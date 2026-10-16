package search

import (
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"avxxx/internal/community"
)

// FirstPage is the number of results on the first page; a click at rank
// <= FirstPage counts toward first-page CTR.
const FirstPage = 20

// ZeroResultTarget is the PRD target for the zero-result rate (< 5%).
const ZeroResultTarget = 0.05

// RawCount is one group of `search` events as the database returns it:
// the raw query text, never a user id. Searches counts every event;
// ZeroResults those with result_count = 0; Clicks those with a clicked_rank;
// FirstPageClicks those with clicked_rank <= FirstPage; Rescued those whose
// props.rescued is true (the server's modifier-stripped retry found results).
type RawCount struct {
	Day             time.Time
	Query           string
	Lang            string
	Searches        int
	ZeroResults     int
	Clicks          int
	FirstPageClicks int
	Rescued         int
}

// DailyRow is one search_query_daily row.
type DailyRow struct {
	Day             time.Time
	QNorm           string
	Lang            string
	Searches        int
	ZeroResults     int
	Clicks          int
	FirstPageClicks int
	Rescued         int
}

type dailyKey struct {
	day        string
	qNorm, lng string
}

// AggregateDaily folds raw groups into search_query_daily rows: queries are
// normalized (NormalizeQuery) and rows that normalize to the same (day,
// q_norm, lang) are summed; queries shorter than MinQueryRunes after
// normalization are dropped. Lang is cut to 16 bytes. Rows come back sorted
// by day, q_norm, lang.
func AggregateDaily(raw []RawCount) []DailyRow {
	acc := map[dailyKey]*DailyRow{}
	for _, r := range raw {
		q := NormalizeQuery(r.Query)
		if !Meaningful(q) || r.Searches <= 0 {
			continue
		}
		day := r.Day.UTC().Truncate(24 * time.Hour)
		lang := strings.TrimSpace(r.Lang)
		if len(lang) > 16 {
			lang = lang[:16]
		}
		k := dailyKey{day.Format(time.DateOnly), q, lang}
		d := acc[k]
		if d == nil {
			d = &DailyRow{Day: day, QNorm: q, Lang: lang}
			acc[k] = d
		}
		d.Searches += r.Searches
		d.ZeroResults += r.ZeroResults
		d.Clicks += r.Clicks
		d.FirstPageClicks += r.FirstPageClicks
		d.Rescued += r.Rescued
	}
	out := make([]DailyRow, 0, len(acc))
	for _, d := range acc {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if !a.Day.Equal(b.Day) {
			return a.Day.Before(b.Day)
		}
		if a.QNorm != b.QNorm {
			return a.QNorm < b.QNorm
		}
		return a.Lang < b.Lang
	})
	return out
}

// HotInput is one raw query over the hot-search window: Sessions is the
// number of distinct sessions that searched it (robust against one client
// repeating a query), Searches all events, ZeroResults the zero-result ones.
type HotInput struct {
	Query       string
	Sessions    int
	Searches    int
	ZeroResults int
}

// HotRow is one search_trending row.
type HotRow struct {
	Rank     int    `json:"rank"`
	QNorm    string `json:"q"`
	Searches int    `json:"searches"` // distinct sessions in the window
}

// HotConfig tunes RankHot.
type HotConfig struct {
	Size         int     // board size (default 20)
	MinSessions  int     // minimum distinct sessions (default 3)
	MaxZeroShare float64 // drop terms whose searches mostly found nothing (default 0.5)
}

func (c HotConfig) withDefaults() HotConfig {
	if c.Size <= 0 {
		c.Size = 20
	}
	if c.MinSessions <= 0 {
		c.MinSessions = 3
	}
	if c.MaxZeroShare <= 0 {
		c.MaxZeroShare = 0.5
	}
	return c
}

// Exclusions are the terms never shown as hot searches: the admin
// search_blocklist (exact q_norm) and the moderation word list (a term is
// excluded when its folded form contains a moderation word).
type Exclusions struct {
	Blocked         []string // NormalizeQuery form
	ModerationWords []string // community.NormalizeKeyword form
}

// Excluded reports whether q (normalized) is on a blocklist.
func (e Exclusions) Excluded(q string) bool {
	for _, b := range e.Blocked {
		if b != "" && q == b {
			return true
		}
	}
	folded := community.NormalizeKeyword(q)
	for _, w := range e.ModerationWords {
		if w != "" && strings.Contains(folded, w) {
			return true
		}
	}
	return false
}

// RankHot builds the hot-search board: inputs are merged by normalized
// query, short queries, help-like queries, excluded terms, terms below
// MinSessions and terms whose share of zero-result searches exceeds
// MaxZeroShare are dropped; the rest are ordered by sessions, then searches,
// then query text, and cut to Size.
func RankHot(in []HotInput, ex Exclusions, cfg HotConfig) []HotRow {
	cfg = cfg.withDefaults()
	type agg struct{ sessions, searches, zero int }
	acc := map[string]*agg{}
	for _, x := range in {
		q := NormalizeQuery(x.Query)
		if !Meaningful(q) || utf8.RuneCountInString(q) > 40 {
			continue
		}
		a := acc[q]
		if a == nil {
			a = &agg{}
			acc[q] = a
		}
		a.sessions += x.Sessions
		a.searches += x.Searches
		a.zero += x.ZeroResults
	}
	type cand struct {
		q string
		agg
	}
	var cs []cand
	for q, a := range acc {
		if a.sessions < cfg.MinSessions || a.searches == 0 {
			continue
		}
		if float64(a.zero)/float64(a.searches) > cfg.MaxZeroShare {
			continue
		}
		if IsHelpQuery(q) || ex.Excluded(q) {
			continue
		}
		cs = append(cs, cand{q, *a})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].sessions != cs[j].sessions {
			return cs[i].sessions > cs[j].sessions
		}
		if cs[i].searches != cs[j].searches {
			return cs[i].searches > cs[j].searches
		}
		return cs[i].q < cs[j].q
	})
	if len(cs) > cfg.Size {
		cs = cs[:cfg.Size]
	}
	out := make([]HotRow, len(cs))
	for i, c := range cs {
		out[i] = HotRow{Rank: i + 1, QNorm: c.q, Searches: c.sessions}
	}
	return out
}

// Totals are the AdminSearch headline numbers over a set of daily rows.
type Totals struct {
	Searches        int     `json:"searches"`
	ZeroResults     int     `json:"zero_results"`
	Clicks          int     `json:"clicks"`
	FirstPageClicks int     `json:"first_page_clicks"`
	Rescued         int     `json:"rescued"`
	ZeroResultRate  float64 `json:"zero_result_rate"`
	ZeroResultGoal  float64 `json:"zero_result_target"`
	MeetsTarget     bool    `json:"meets_target"`
	CTR             float64 `json:"ctr"`
	FirstPageCTR    float64 `json:"first_page_ctr"`
	// RescueRate is rescued / (rescued + zero_results): of the searches whose
	// original query matched nothing, the share the stripped retry saved.
	RescueRate float64 `json:"rescue_rate"`
}

// Summarize sums rows and derives the rates (0 when the denominator is 0).
// MeetsTarget is false without searches.
func Summarize(rows []DailyRow) Totals {
	var t Totals
	for _, r := range rows {
		t.Searches += r.Searches
		t.ZeroResults += r.ZeroResults
		t.Clicks += r.Clicks
		t.FirstPageClicks += r.FirstPageClicks
		t.Rescued += r.Rescued
	}
	t.ZeroResultGoal = ZeroResultTarget
	t.ZeroResultRate = rate(t.ZeroResults, t.Searches)
	t.CTR = rate(t.Clicks, t.Searches)
	t.FirstPageCTR = rate(t.FirstPageClicks, t.Searches)
	t.RescueRate = rate(t.Rescued, t.Rescued+t.ZeroResults)
	t.MeetsTarget = t.Searches > 0 && t.ZeroResultRate < ZeroResultTarget
	return t
}

func rate(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return float64(int64(float64(n)/float64(d)*10000+0.5)) / 10000
}
