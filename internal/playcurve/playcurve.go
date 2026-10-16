// Package playcurve computes the playback replay peak and retention curves
// of PRD 14.3 ("最多重看" 曲线) from play events. It is database-free:
// internal/store loads the events and saves the results, cmd/audit runs the
// hourly job (kind JobKind, enqueued by cmd/scheduler).
//
// Input, per play_id (PRD 14.2 / 14.3): the play_request (release, source,
// user / device / session), the r1 replay tables carried by heartbeat
// (deltas) and play_end (remainder) whose sum is the play's 200-bucket table
// (events.DecodeBuckets), forward seeks, and interaction events.
//
// Algorithm, per release and timeline (version_key: "main" shared by the
// main, high-quality and dub sources; "alt:<source id>" for an alternate
// version, as in the danmaku curve):
//
//  1. Dedupe: one play per play_id; its table is the sum of every reported
//     delta (each bucket capped at 255).
//  2. Bot filter: a play with no interaction (pause, resume, seek, rate
//     change, manual quality / source change, peak click) and more than
//     2 h of continuous play is dropped, and so is every play of its device.
//  3. Plays that never reported a table (no play_end, no heartbeat with
//     buckets) are not counted: their table is unknown.
//  4. Daily cap: per release, at most 3 counted plays per user and per device
//     per UTC day (anonymous plays without either fall back to the session),
//     in play_request order.
//  5. viewers[b] = plays with count ≥ 1, replays[b] = count ≥ 2, skips[b] =
//     plays with a forward seek crossing b (strictly between the buckets of
//     from and to) whose final count for b is 0 (never came back).
//  6. Replay peak: rate[b] = replays[b] / viewers[b] (0 where fewer than
//     MinBucketViewers viewers), first 3% (6 buckets) and last 5%
//     (10 buckets) zeroed, 3-bucket moving average inside the kept range,
//     normalized to 0–255. Fewer than MinValidPlays valid plays (or no
//     replay at all): no curve.
//  7. Retention[b] = viewers[b] / valid plays, in basis points.
package playcurve

import (
	"avxxx/internal/community"
	"avxxx/internal/events"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Constants of PRD 14.3.
const (
	Buckets          = events.ReplayBuckets // 200, as the danmaku curve
	CurveMax         = community.CurveMax   // normalized curves are 0..255
	MinValidPlays    = 1000                 // fewer: no replay peak
	MaxPlaysPerDay   = 3                    // per user / device / release / day
	BotContinuous    = 2 * time.Hour        // no interaction and longer: bot
	TrimHead         = Buckets * 3 / 100    // 6 buckets (3%)
	TrimTail         = Buckets * 5 / 100    // 10 buckets (5%)
	MinBucketViewers = 20                   // a rate needs this many viewers
	CountCap         = events.ReplayCountMax
	RetentionScale   = 10000 // retention is stored in basis points
	DanmakuWeight    = 0.7   // peak curve = 70% danmaku + 30% replay rate
	ReplayWeight     = 0.3
	DefaultWindow    = 90 * 24 * time.Hour // raw events are kept 90 days (PRD 14.5)
	JobKind          = "release_curves"
)

// Curve kinds stored in release_curves.kind.
const (
	KindViewers    = "viewers"
	KindReplays    = "replays"
	KindSkips      = "skips"
	KindReplayPeak = "replay_peak"
	KindRetention  = "retention"
)

// Seek is one forward or backward seek of a play.
type Seek struct{ FromMS, ToMS int64 }

// Play is one play_id as seen by the aggregation.
type Play struct {
	PlayID     string
	VersionKey string
	UserID     int64  // 0 when unknown (no analytics consent / anonymous)
	DeviceID   string // "" when unknown
	SessionID  string
	StartedAt  time.Time // play_request ts_server
	// Reported is true once a play_end or a heartbeat with buckets arrived.
	Reported   bool
	Table      [Buckets]int
	Interacted bool
	Continuous time.Duration // longest of the heartbeat span and play_end watched_seconds
	Seeks      []Seek
}

// Row is one event of a play, with the props the aggregation reads.
type Row struct {
	PlayID         string
	Event          string
	TSClient       time.Time
	TSServer       time.Time
	UserID         int64
	DeviceID       string
	SessionID      string
	SourceID       int64  // play_request
	Buckets        string // heartbeat / play_end
	FromMS, ToMS   int64  // seek
	Mode           string // quality_change / source_switch
	WatchedSeconds int64  // play_end
}

// Builder assembles Plays from event rows in any order.
type Builder struct {
	// Version maps a play_request source id to its version key.
	Version func(sourceID int64) string
	plays   map[string]*building
	order   []string
	// BadTables counts buckets props that failed to decode (rows stored
	// before validation existed); they are ignored.
	BadTables int
}

type building struct {
	Play
	hasRequest      bool
	hbFirst, hbLast time.Time
}

// Add folds one event row into its play.
func (b *Builder) Add(r Row) {
	if r.PlayID == "" {
		return
	}
	if b.plays == nil {
		b.plays = map[string]*building{}
	}
	p := b.plays[r.PlayID]
	if p == nil {
		p = &building{Play: Play{PlayID: r.PlayID}}
		b.plays[r.PlayID] = p
		b.order = append(b.order, r.PlayID)
	}
	switch r.Event {
	case "play_request":
		// A resent play_request with another event_id: keep the earliest.
		if p.hasRequest && !r.TSServer.Before(p.StartedAt) {
			return
		}
		p.hasRequest = true
		p.StartedAt = r.TSServer
		p.UserID, p.DeviceID, p.SessionID = r.UserID, r.DeviceID, r.SessionID
		p.VersionKey = community.DefaultVersion
		if b.Version != nil {
			p.VersionKey = b.Version(r.SourceID)
		}
	case "heartbeat":
		if p.hbFirst.IsZero() || r.TSClient.Before(p.hbFirst) {
			p.hbFirst = r.TSClient
		}
		if r.TSClient.After(p.hbLast) {
			p.hbLast = r.TSClient
		}
		p.Continuous = max(p.Continuous, p.hbLast.Sub(p.hbFirst))
		b.addTable(p, r.Buckets)
	case "play_end":
		p.Reported = true
		p.Continuous = max(p.Continuous, time.Duration(r.WatchedSeconds)*time.Second)
		b.addTable(p, r.Buckets)
	case "seek":
		p.Interacted = true
		p.Seeks = append(p.Seeks, Seek{FromMS: r.FromMS, ToMS: r.ToMS})
	case "pause", "resume", "rate_change", "peak_click":
		p.Interacted = true
	case "quality_change", "source_switch":
		if r.Mode == "manual" {
			p.Interacted = true
		}
	}
}

func (b *Builder) addTable(p *building, s string) {
	if s == "" {
		return
	}
	t, err := events.DecodeBuckets(s)
	if err != nil {
		b.BadTables++
		return
	}
	p.Reported = true
	for i, v := range t {
		p.Table[i] = min(CountCap, p.Table[i]+int(v))
	}
}

// Plays returns every play that has a play_request, in first-seen order.
func (b *Builder) Plays() []Play {
	out := make([]Play, 0, len(b.order))
	for _, id := range b.order {
		if p := b.plays[id]; p.hasRequest {
			out = append(out, p.Play)
		}
	}
	return out
}

// Stats counts what the filters did.
type Stats struct {
	Plays      int `json:"plays"`       // distinct play_ids
	Bot        int `json:"bot"`         // dropped by the bot filter
	Unreported int `json:"unreported"`  // never reported a table
	Capped     int `json:"capped"`      // over the daily cap
	Valid      int `json:"valid_plays"` // counted
}

// Result is the aggregation of one timeline.
type Result struct {
	VersionKey string
	DurationMS int // 0 when unknown
	ValidPlays int
	Viewers    [Buckets]int
	Replays    [Buckets]int
	// Skips is nil when the duration is unknown (seeks cannot be bucketed).
	Skips *[Buckets]int
	// ReplayPeak is nil below MinValidPlays or without any replay.
	ReplayPeak []int16
	Retention  [Buckets]int // basis points of ValidPlays
}

// Filter applies the dedupe, bot, reported and daily-cap rules and returns
// the counted plays in play_request order.
func Filter(plays []Play) ([]Play, Stats) {
	var st Stats
	seen := make(map[string]bool, len(plays))
	uniq := make([]Play, 0, len(plays))
	for _, p := range plays {
		if seen[p.PlayID] {
			continue
		}
		seen[p.PlayID] = true
		uniq = append(uniq, p)
	}
	st.Plays = len(uniq)

	botDevices := map[string]bool{}
	for _, p := range uniq {
		if isBot(p) && p.DeviceID != "" {
			botDevices[p.DeviceID] = true
		}
	}
	slices.SortStableFunc(uniq, func(a, b Play) int {
		if c := a.StartedAt.Compare(b.StartedAt); c != 0 {
			return c
		}
		return strings.Compare(a.PlayID, b.PlayID)
	})
	perDay := map[string]int{}
	out := uniq[:0]
	for _, p := range uniq {
		if isBot(p) || (p.DeviceID != "" && botDevices[p.DeviceID]) {
			st.Bot++
			continue
		}
		if !p.Reported {
			st.Unreported++
			continue
		}
		keys := capKeys(p)
		over := false
		for _, k := range keys {
			if perDay[k] >= MaxPlaysPerDay {
				over = true
			}
		}
		if over {
			st.Capped++
			continue
		}
		for _, k := range keys {
			perDay[k]++
		}
		out = append(out, p)
	}
	st.Valid = len(out)
	return out, st
}

func isBot(p Play) bool { return !p.Interacted && p.Continuous > BotContinuous }

// capKeys are the daily-cap counters a play uses: user and device, or the
// session when neither is known.
func capKeys(p Play) []string {
	day := p.StartedAt.UTC().Format(time.DateOnly)
	var keys []string
	if p.UserID != 0 {
		keys = append(keys, "u:"+strconv.FormatInt(p.UserID, 10)+"|"+day)
	}
	if p.DeviceID != "" {
		keys = append(keys, "d:"+p.DeviceID+"|"+day)
	}
	if len(keys) == 0 {
		keys = append(keys, "s:"+p.SessionID+"|"+day)
	}
	return keys
}

// Aggregate filters plays and computes one Result per timeline that has at
// least one valid play. durationsMS maps version keys to their durations
// (missing or 0: unknown, no skip curve). Results are sorted by version key.
func Aggregate(plays []Play, durationsMS map[string]int) ([]Result, Stats) {
	counted, st := Filter(plays)
	byVersion := map[string]*Result{}
	var keys []string
	for _, p := range counted {
		r := byVersion[p.VersionKey]
		if r == nil {
			r = &Result{VersionKey: p.VersionKey, DurationMS: durationsMS[p.VersionKey]}
			if r.DurationMS > 0 {
				r.Skips = new([Buckets]int)
			}
			byVersion[p.VersionKey] = r
			keys = append(keys, p.VersionKey)
		}
		r.ValidPlays++
		for b, c := range p.Table {
			if c >= 1 {
				r.Viewers[b]++
			}
			if c >= 2 {
				r.Replays[b]++
			}
		}
		if r.Skips != nil {
			skipped := SkippedBuckets(p.Seeks, p.Table, r.DurationMS)
			for b, s := range skipped {
				if s {
					r.Skips[b]++
				}
			}
		}
	}
	slices.Sort(keys)
	out := make([]Result, 0, len(keys))
	for _, k := range keys {
		r := byVersion[k]
		for b := range r.Retention {
			r.Retention[b] = int(math.Round(float64(r.Viewers[b]) * RetentionScale / float64(r.ValidPlays)))
		}
		if r.ValidPlays >= MinValidPlays {
			r.ReplayPeak = ReplayPeak(r.Viewers, r.Replays)
		}
		out = append(out, *r)
	}
	return out, st
}

// BucketOf maps a position to its bucket for a timeline of durationMS.
func BucketOf(ms int64, durationMS int) int {
	if ms <= 0 || durationMS <= 0 {
		return 0
	}
	b := int(ms * Buckets / int64(durationMS))
	return min(b, Buckets-1)
}

// SkippedBuckets marks the buckets a play skipped: crossed by a forward
// seek (strictly between the from and to buckets) and never played (final
// count 0).
func SkippedBuckets(seeks []Seek, table [Buckets]int, durationMS int) [Buckets]bool {
	var out [Buckets]bool
	if durationMS <= 0 {
		return out
	}
	for _, s := range seeks {
		if s.ToMS <= s.FromMS {
			continue
		}
		for b := BucketOf(s.FromMS, durationMS) + 1; b < BucketOf(s.ToMS, durationMS); b++ {
			if table[b] == 0 {
				out[b] = true
			}
		}
	}
	return out
}

// ReplayRates is replays/viewers per bucket, 0 where viewers are below
// MinBucketViewers.
func ReplayRates(viewers, replays [Buckets]int) [Buckets]float64 {
	var out [Buckets]float64
	for b := range out {
		if viewers[b] >= MinBucketViewers {
			out[b] = float64(replays[b]) / float64(viewers[b])
		}
	}
	return out
}

// Trim zeroes the first TrimHead and last TrimTail buckets.
func Trim(v [Buckets]float64) [Buckets]float64 {
	for b := range v {
		if b < TrimHead || b >= Buckets-TrimTail {
			v[b] = 0
		}
	}
	return v
}

// SmoothKept is the 3-bucket moving average over the kept range
// [TrimHead, Buckets-TrimTail): edge buckets of the range average only the
// neighbours inside it, and trimmed buckets stay 0.
func SmoothKept(v [Buckets]float64) [Buckets]float64 {
	var out [Buckets]float64
	lo, hi := TrimHead, Buckets-TrimTail
	for b := lo; b < hi; b++ {
		sum, n := 0.0, 0
		for j := b - 1; j <= b+1; j++ {
			if j >= lo && j < hi {
				sum += v[j]
				n++
			}
		}
		out[b] = sum / float64(n)
	}
	return out
}

// ReplayPeak runs rates → trim → smoothing → 0–255 normalization; nil when
// no bucket has a replay rate.
func ReplayPeak(viewers, replays [Buckets]int) []int16 {
	s := SmoothKept(Trim(ReplayRates(viewers, replays)))
	if slices.Max(s[:]) <= 0 {
		return nil
	}
	return community.Normalize(s)
}

// PeakCurve is the one curve the player shows with danmaku on (PRD 14.3):
// 70% danmaku density + 30% replay rate when both exist, renormalized to
// 0–255; otherwise whichever exists; nil when neither does.
func PeakCurve(danmaku, replay []int16) []int16 {
	okD, okR := len(danmaku) == Buckets, len(replay) == Buckets
	switch {
	case okD && okR:
		var mix [Buckets]float64
		for b := range mix {
			mix[b] = DanmakuWeight*float64(danmaku[b]) + ReplayWeight*float64(replay[b])
		}
		return community.Normalize(mix)
	case okD:
		return slices.Clone(danmaku)
	case okR:
		return slices.Clone(replay)
	}
	return nil
}

// Intro skip hint (PRD 14.3 last bullet): skips are large and concentrated
// at the start.
const (
	IntroBuckets      = Buckets / 10 // first 10%
	IntroSkipMinRate  = 0.2          // some intro bucket skipped by ≥ 20% of valid plays
	IntroSkipMinShare = 0.5          // ≥ 50% of all skip mass in the first 10%
	IntroSkipMinPlays = 100
)

// IntroSkipSuggested reports whether operators should mark an intro.
func IntroSkipSuggested(skips []int, validPlays int) bool {
	if len(skips) != Buckets || validPlays < IntroSkipMinPlays {
		return false
	}
	head, total, peak := 0, 0, 0
	for b, s := range skips {
		total += s
		if b < IntroBuckets {
			head += s
			peak = max(peak, s)
		}
	}
	if total == 0 {
		return false
	}
	return float64(peak) >= IntroSkipMinRate*float64(validPlays) && float64(head) >= IntroSkipMinShare*float64(total)
}
