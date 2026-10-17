package recs

import (
	"avxxx/internal/audit"
	"context"
	"fmt"
	"time"
)

// Job kinds (jobs.kind) run by the cmd/audit worker and enqueued by
// cmd/scheduler: similarity nightly at RECS_NIGHTLY_AT (default 04:30, PRD
// 10), trending every RECS_TRENDING_EVERY (default 1h, PRD 4.2).
const (
	JobKindSimilarity = "recs_similarity"
	JobKindTrending   = "recs_trending"
)

// Lookback windows for co-watch inputs. Raw events are kept 90 days.
const (
	HistoryLookback = 365 * 24 * time.Hour
	EventLookback   = 90 * 24 * time.Hour
)

// ReleaseSimilarRow is one release_similar row.
type ReleaseSimilarRow struct {
	ReleaseID int64
	SimilarID int64
	Score     float64
	Reason    string
}

// ReleaseSimilarEpsilon is how far a stored release_similar score may drift
// from the newly computed one before the nightly job rewrites the row
// (SimilarDiff). Each night's new releases and plays move idf and the
// co-watch norms, so most scores change by 1e-5 to 1e-3 every night: with
// no tolerance the job rewrites almost every row, with 1e-3 about 1%
// (docs/PERFORMANCE.md). Scores are cosines in [0, 1], and 1e-3 is a tenth
// of a match percent. Each release's neighbour order stays exact.
const ReleaseSimilarEpsilon = 1e-3

// ReleaseSimilarWrite counts the rows one ReplaceReleaseSimilar changed.
type ReleaseSimilarWrite struct {
	Inserted int64 `json:"inserted"`
	Updated  int64 `json:"updated"`
	Deleted  int64 `json:"deleted"`
	// Exact counts releases written with exact scores because no stored
	// value within the tolerance kept their order (float rounding edges).
	Exact   int64 `json:"exact_releases"`
	WriteMS int64 `json:"write_ms"`
}

// Store is everything the jobs need from the database (internal/store/recs.go).
type Store interface {
	SimilarityWeights(ctx context.Context) (Weights, error)
	// ReleaseDocs returns every published, unmerged release with its tags,
	// maker, series and active performers.
	ReleaseDocs(ctx context.Context) ([]ReleaseDoc, error)
	// WatchInteractions returns (user, release, ratio) for published
	// releases from watch_history since historySince and progress /
	// completed play events since eventSince; ratio is the best watch ratio
	// in [0, 1], or -1 when the duration is unknown.
	WatchInteractions(ctx context.Context, historySince, eventSince time.Time) ([]Interaction, error)
	// ReplaceReleaseSimilar makes release_similar equivalent to rows (see
	// SimilarDiff, with ReleaseSimilarEpsilon), writing only the rows that
	// differ, and reports how many it inserted, updated and deleted. It may
	// reorder rows.
	ReplaceReleaseSimilar(ctx context.Context, rows []ReleaseSimilarRow, at time.Time) (ReleaseSimilarWrite, error)
	ReplacePerformerSimilar(ctx context.Context, rows []PerformerMatch, at time.Time) error

	// TrendingInputs counts plays (distinct play_request plays, or
	// watch_history users if more), completed plays and previous-window
	// plays per published release: window [from, to), previous [prevFrom, from).
	TrendingInputs(ctx context.Context, prevFrom, from, to time.Time) ([]TrendInput, error)
	PerformerHeatInputs(ctx context.Context, prevFrom, from, to time.Time) ([]HeatInput, error)
	// ReplaceTrending / ReplacePerformerHeat swap a period's board and set
	// prev_rank from the board they replace.
	ReplaceTrending(ctx context.Context, period string, rows []TrendRow, at time.Time) error
	ReplacePerformerHeat(ctx context.Context, period string, rows []TrendRow, at time.Time) error
	// RefreshPerformerStats rebuilds performer_stats (Actor 统计, NavActors
	// sorts and filters) after the heat boards it reads; it returns the
	// number of performers written.
	RefreshPerformerStats(ctx context.Context, at time.Time) (int64, error)

	RecordRun(ctx context.Context, r audit.Run) error
}

// Runner executes the recommendation jobs.
type Runner struct {
	Store     Store
	Now       func() time.Time
	JobID     int64
	CoWatch   CoWatchConfig
	Performer PerformerConfig
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// SimilarityReport summarizes a similarity run.
type SimilarityReport struct {
	StartedAt     time.Time `json:"started_at"`
	Releases      int       `json:"releases"`
	Interactions  int       `json:"interactions"`
	CoWatchRows   int       `json:"cowatch_rows"`
	ContentRows   int       `json:"content_rows"`
	PerformerRows int       `json:"performer_rows"`
	// ReleaseWrite is what the release_similar write changed.
	ReleaseWrite ReleaseSimilarWrite `json:"release_write"`
	Weights      Weights             `json:"weights"`
	DurationMS   int64               `json:"duration_ms"`
}

// RunSimilarity recomputes release_similar (co-watch + content) and
// performer_similar from the current weights, and records one nightly_runs
// row 'recs.similarity'.
func (r *Runner) RunSimilarity(ctx context.Context) (rep SimilarityReport, err error) {
	start := r.now()
	rep.StartedAt = start
	defer func() {
		r.record(ctx, "recs.similarity", start, rep.Releases, rep.CoWatchRows+rep.ContentRows+rep.PerformerRows, rep, err)
	}()

	w, err := r.Store.SimilarityWeights(ctx)
	if err != nil {
		return rep, fmt.Errorf("weights: %w", err)
	}
	rep.Weights = w
	docs, err := r.Store.ReleaseDocs(ctx)
	if err != nil {
		return rep, fmt.Errorf("release docs: %w", err)
	}
	rep.Releases = len(docs)
	raw, err := r.Store.WatchInteractions(ctx, start.Add(-HistoryLookback), start.Add(-EventLookback))
	if err != nil {
		return rep, fmt.Errorf("interactions: %w", err)
	}
	inter := make([]Interaction, len(raw))
	for i, x := range raw {
		inter[i] = Interaction{UserID: x.UserID, ItemID: x.ItemID, Weight: CompletionWeight(x.Weight)}
	}
	rep.Interactions = len(inter)

	// Performers first: their working set is freed before the release rows
	// (about 15M at 300k releases) are built, which lowers the peak.
	perf := PerformerSimilarity(docs, PerformerInteractions(inter, docs), w, r.Performer)
	rep.PerformerRows = len(perf)
	if err = ctx.Err(); err != nil {
		return rep, err
	}

	cw := CoWatch(inter, r.CoWatch)
	content := ReleaseContentConfig
	content.KindOnly = true // release_similar stores the reason only
	rows := make([]ReleaseSimilarRow, 0, len(cw)+len(docs)*content.withDefaults().TopK)
	for _, nb := range cw {
		rows = append(rows, ReleaseSimilarRow{nb.ItemID, nb.NeighborID, nb.Score, ReasonCoWatch})
	}
	rep.CoWatchRows = len(rows)
	if err = ctx.Err(); err != nil {
		return rep, err
	}
	// Streamed straight into rows: holding every release's matches as well
	// doubled the peak memory.
	EachContentNeighbors(ReleaseFeatureSets(docs), content, func(id int64, ms []ContentMatch) {
		for _, m := range ms {
			rows = append(rows, ReleaseSimilarRow{id, m.ID, m.Score, ContentReason(m.Kind)})
		}
	})
	rep.ContentRows = len(rows) - rep.CoWatchRows
	if err = ctx.Err(); err != nil {
		return rep, err
	}

	at := r.now()
	if rep.ReleaseWrite, err = r.Store.ReplaceReleaseSimilar(ctx, rows, at); err != nil {
		return rep, fmt.Errorf("write release_similar: %w", err)
	}
	rep.ReleaseWrite.WriteMS = r.now().Sub(at).Milliseconds()
	if err = r.Store.ReplacePerformerSimilar(ctx, perf, at); err != nil {
		return rep, fmt.Errorf("write performer_similar: %w", err)
	}
	rep.DurationMS = r.now().Sub(start).Milliseconds()
	return rep, nil
}

// TrendingReport summarizes a trending run.
type TrendingReport struct {
	StartedAt  time.Time      `json:"started_at"`
	Releases   map[string]int `json:"releases"`   // board rows per period
	Performers map[string]int `json:"performers"` // heat rows per period
	// PerformerStats is the number of performer_stats rows rewritten.
	PerformerStats int64 `json:"performer_stats"`
	DurationMS     int64 `json:"duration_ms"`
}

// RunTrending recomputes every period's trending board and performer heat
// ranking, then performer_stats (which reads the month heat board), and
// records one nightly_runs row 'recs.trending'.
func (r *Runner) RunTrending(ctx context.Context) (rep TrendingReport, err error) {
	start := r.now()
	rep = TrendingReport{StartedAt: start, Releases: map[string]int{}, Performers: map[string]int{}}
	defer func() {
		n := 0
		for _, v := range rep.Releases {
			n += v
		}
		r.record(ctx, "recs.trending", start, n, n, rep, err)
	}()
	for _, p := range Periods {
		d, _ := PeriodWindow(p)
		from := start.Add(-d)
		prevFrom := from.Add(-d)
		in, err := r.Store.TrendingInputs(ctx, prevFrom, from, start)
		if err != nil {
			return rep, fmt.Errorf("trending inputs %s: %w", p, err)
		}
		rows := RankTrending(in, BoardSize)
		if err = r.Store.ReplaceTrending(ctx, p, rows, start); err != nil {
			return rep, fmt.Errorf("write trending %s: %w", p, err)
		}
		rep.Releases[p] = len(rows)
		heat, err := r.Store.PerformerHeatInputs(ctx, prevFrom, from, start)
		if err != nil {
			return rep, fmt.Errorf("heat inputs %s: %w", p, err)
		}
		hrows := RankHeat(heat, BoardSize)
		if err = r.Store.ReplacePerformerHeat(ctx, p, hrows, start); err != nil {
			return rep, fmt.Errorf("write heat %s: %w", p, err)
		}
		rep.Performers[p] = len(hrows)
	}
	if rep.PerformerStats, err = r.Store.RefreshPerformerStats(ctx, start); err != nil {
		return rep, fmt.Errorf("performer stats: %w", err)
	}
	rep.DurationMS = r.now().Sub(start).Milliseconds()
	return rep, nil
}

func (r *Runner) record(ctx context.Context, job string, start time.Time, checked, written int, detail any, cause error) {
	run := audit.Run{Job: job, JobID: r.JobID, StartedAt: start, DurationMS: r.now().Sub(start).Milliseconds(),
		Checked: checked, Fixed: written, Result: audit.ResultOK, Detail: map[string]any{"report": detail}}
	if cause != nil {
		run.Result = audit.ResultFailed
		run.Detail["error"] = cause.Error()
	}
	rc, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_ = r.Store.RecordRun(rc, run)
}
