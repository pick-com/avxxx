package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	TaskDiscovery = "catalog_discovery"
	TaskDetail    = "catalog_detail"
)

// CheckpointKey identifies one checkpoint row (source, task type, partition).
// Discovery checkpoints are partitioned by Phase, so a baseline pass and the
// daily incremental pass never share a cursor or watermark.
type CheckpointKey struct {
	SourceID      int64
	TaskType      string
	Partition     string
	ParserVersion string
}

// Checkpoint is the persisted resume position for a paginated task.
type Checkpoint struct {
	Cursor         string
	Watermark      *time.Time
	CompletedPages int64
	LastErrorCode  string
}

// CheckpointStore persists crawl checkpoints. Load returns found=false when no
// checkpoint exists yet. Advance must only be called after the batch it
// covers is fully persisted; Fail records an error without moving the cursor.
type CheckpointStore interface {
	LoadCheckpoint(ctx context.Context, key CheckpointKey) (cp Checkpoint, found bool, err error)
	AdvanceCheckpoint(ctx context.Context, key CheckpointKey, cursor string, watermark *time.Time, pages int64) error
	FailCheckpoint(ctx context.Context, key CheckpointKey, code string) error
}

// RecordSink persists one parsed catalog record.
type RecordSink interface {
	SaveCatalogRecord(ctx context.Context, sourceID int64, parserVersion string, rec Record) (int64, error)
}

// StopReason explains why a crawl run ended without error.
type StopReason string

const (
	StopComplete  StopReason = "complete"   // reached the last discovery page
	StopBudget    StopReason = "budget"     // page/byte budget spent
	StopPageLimit StopReason = "page_limit" // Crawl.MaxPages reached
	StopWatermark StopReason = "watermark"  // incremental pass reached already-seen records
)

// DefaultIncrementalPages is the incremental overlap window: how many head
// pages an incremental run reads when it does not meet its watermark first
// (FINAL_DATA_DESIGN 7 "最近 3 页或 7 天重叠窗口", with headroom).
const DefaultIncrementalPages = 5

// incrementalMarkKeys is how many head records an incremental watermark keeps,
// so one deleted or re-sorted record does not lose the mark.
const incrementalMarkKeys = 5

// Crawl runs paginated discovery for one source and one Phase:
//
//   - baseline resumes from its own cursor, walks the archive page by page and
//     advances the baseline checkpoint after each fully persisted page. When
//     a full pass ends, its cursor resets and its watermark is set; it never
//     touches the incremental checkpoint.
//   - incremental always starts at the newest-first head and stops at the
//     stored watermark: the set of head record keys from its last completed
//     run (cursor), stamped with that run's time (watermark). It advances only
//     when the run reached the mark, the end of the listing, or its page
//     window; a failed or budget-stopped run leaves the mark untouched, so the
//     next run re-reads the head (saves are idempotent).
//   - verify reads at most MaxPages pages from the head and never reads or
//     writes a checkpoint.
type Crawl struct {
	SourceID    int64
	StartURL    string
	Phase       Phase
	Adapter     Adapter
	Fetcher     *Fetcher
	Checkpoints CheckpointStore
	Sink        RecordSink
	// MaxPages caps discovery pages per run (0 = unlimited; incremental runs
	// default to DefaultIncrementalPages).
	MaxPages int
	Now      func() time.Time
}

// ErrEmptyListing is returned when an automatic crawl's discovery page lists
// no records at all. The run fails (and is retried) instead of completing.
var ErrEmptyListing = errors.New("catalog: discovery page lists no records")

type CrawlResult struct {
	Phase   Phase
	Pages   int
	Records int
	// Skipped counts detail pages without a usable entity (ErrSkipRecord).
	Skipped int
	// RobotsSkipped counts detail pages robots.txt disallowed.
	RobotsSkipped int
	Cursor        string
	Stop          StopReason
	// WatermarkGap is set when an incremental run filled its page window
	// without meeting the previous mark: records between the window and the
	// mark are left for the next baseline pass.
	WatermarkGap bool
}

func recordMark(r Record) string { return string(r.EntityType) + ":" + r.SourceKey }

func (c *Crawl) Run(ctx context.Context) (CrawlResult, error) {
	if c.Adapter == nil || c.Fetcher == nil || c.Sink == nil || (c.Checkpoints == nil && c.Phase != PhaseVerify) {
		return CrawlResult{}, errors.New("catalog: crawl is missing adapter, fetcher, checkpoints or sink")
	}
	phase := c.Phase
	if phase == "" {
		phase = PhaseBaseline
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	maxPages := c.MaxPages
	if phase == PhaseIncremental && maxPages <= 0 {
		maxPages = DefaultIncrementalPages
	}
	if phase == PhaseVerify && (maxPages <= 0 || maxPages > VerifyMaxPages) {
		maxPages = VerifyMaxPages
	}
	parser := c.Adapter.Name()
	key := CheckpointKey{SourceID: c.SourceID, TaskType: TaskDiscovery, Partition: string(phase), ParserVersion: parser}
	var cp Checkpoint
	if phase != PhaseVerify {
		var err error
		if cp, _, err = c.Checkpoints.LoadCheckpoint(ctx, key); err != nil {
			return CrawlResult{Phase: phase}, fmt.Errorf("load checkpoint: %w", err)
		}
	}
	page := c.StartURL
	narrowed := false // incremental head filtered to recent records (IncrementalStarter)
	if phase == PhaseBaseline && cp.Cursor != "" && SameSite(c.StartURL, cp.Cursor) {
		page = cp.Cursor
	} else if s, ok := c.Adapter.(IncrementalStarter); ok && phase == PhaseIncremental && cp.Watermark != nil {
		// Incremental only: the incremental checkpoint's watermark is the
		// time of the last completed incremental run, so an adapter that can
		// filter by date (DMM gte_date) narrows the head to what changed
		// since then. The head marks below still stop the run. Baseline and
		// verify always start from their own cursor / the start URL.
		if u := s.IncrementalStart(c.StartURL, *cp.Watermark); u != "" && SameSite(c.StartURL, u) {
			page, narrowed = u, true
		}
	}
	inline := false
	if d, ok := c.Adapter.(InlineDiscoverer); ok {
		inline = d.InlineRecords()
	}
	oldMarks := map[string]bool{}
	if phase == PhaseIncremental {
		for _, m := range strings.Split(cp.Cursor, "\n") {
			if m = strings.TrimSpace(m); m != "" {
				oldMarks[m] = true
			}
		}
	}
	var newMarks []string
	res := CrawlResult{Phase: phase, Cursor: page}
	seen := map[string]bool{}
	fail := func(k CheckpointKey, code string, cause error) (CrawlResult, error) {
		if b, ok := IsBlocked(cause); ok && b != nil {
			code = "source_blocked"
		}
		if _, ok := IsRobots(cause); ok {
			code = CodeRobotsDisallow
		}
		if phase == PhaseVerify {
			return res, cause
		}
		if ferr := c.Checkpoints.FailCheckpoint(ctx, k, code); ferr != nil {
			return res, errors.Join(cause, fmt.Errorf("record checkpoint failure: %w", ferr))
		}
		return res, cause
	}
	failDetail := func(k CheckpointKey, code string) {
		if phase != PhaseVerify {
			_ = c.Checkpoints.FailCheckpoint(ctx, k, code)
		}
	}
	for {
		if maxPages > 0 && res.Pages >= maxPages {
			res.Stop = StopPageLimit
			if phase == PhaseIncremental {
				// The window is spent without meeting the old mark (or there
				// was none): move the mark to this run's head so tomorrow's
				// run stops here; anything older is the baseline's job.
				res.WatermarkGap = len(oldMarks) > 0
				return res, c.advanceIncremental(ctx, key, newMarks, cp.Cursor, now)
			}
			return res, nil
		}
		seen[page] = true
		index, err := c.Fetcher.Fetch(ctx, page)
		if errors.Is(err, ErrBudgetExhausted) {
			res.Stop = StopBudget
			return res, nil
		}
		if err != nil {
			return fail(key, "index_fetch_failed", err)
		}
		records, err := c.Adapter.Discover(ctx, index)
		if err != nil {
			return fail(key, "discover_failed", err)
		}
		if len(records) == 0 && phase == PhaseIncremental && narrowed && res.Pages == 0 {
			// A date-filtered head (DMM gte_date) can legitimately be empty:
			// nothing new since the last pass. Keep the marks and their time.
			res.Stop = StopComplete
			return res, nil
		}
		if len(records) == 0 && phase != PhaseVerify {
			// A catalog listing page with nothing on it is a broken or
			// truncated response (or a redesign the adapter no longer
			// parses), not the end of the archive: a baseline would mark the
			// pass complete and an incremental run would move its watermark
			// past records it never saw. Fail without touching checkpoints;
			// verify runs report the empty page instead.
			return fail(key, "empty_listing", fmt.Errorf("%w: %s", ErrEmptyListing, page))
		}
		reachedMark := false
		for _, discovered := range records {
			if phase == PhaseIncremental {
				if oldMarks[recordMark(discovered)] {
					reachedMark = true
					break // newest-first: everything from here on was seen
				}
				if res.Pages == 0 && len(newMarks) < incrementalMarkKeys {
					newMarks = append(newMarks, recordMark(discovered))
				}
			}
			if inline {
				// Structured APIs: the discovery payload is the record. The
				// discovery page itself already went through the Fetcher
				// (robots.txt, budget); verify runs save into the report.
				if _, err := c.Sink.SaveCatalogRecord(ctx, c.SourceID, parser, discovered); err != nil {
					failDetail(CheckpointKey{SourceID: c.SourceID, TaskType: TaskDetail, Partition: discovered.SourceKey, ParserVersion: parser}, "record_save_failed")
					return fail(key, "record_save_failed", err)
				}
				res.Records++
				continue
			}
			detailKey := CheckpointKey{SourceID: c.SourceID, TaskType: TaskDetail, Partition: discovered.SourceKey, ParserVersion: parser}
			doc, err := c.Fetcher.Fetch(ctx, discovered.URL)
			if errors.Is(err, ErrBudgetExhausted) {
				// Batch incomplete: leave the cursor on this page so the next
				// run re-reads it. Saves are idempotent by source key.
				res.Stop = StopBudget
				return res, nil
			}
			if _, ok := IsRobots(err); ok {
				res.RobotsSkipped++ // robots.txt disallows this detail page: never fetch it
				continue
			}
			if err != nil {
				if _, blocked := IsBlocked(err); !blocked {
					failDetail(detailKey, "detail_fetch_failed")
				}
				return fail(key, "detail_fetch_failed", err)
			}
			record, err := c.Adapter.Parse(ctx, doc, discovered.EntityType)
			if errors.Is(err, ErrSkipRecord) {
				// Nothing to save (e.g. only a site-title heading): note it on
				// the detail checkpoint and keep crawling.
				failDetail(detailKey, "parse_skipped")
				res.Skipped++
				continue
			}
			if err != nil {
				failDetail(detailKey, "parse_failed")
				return fail(key, "parse_failed", err)
			}
			if record.SourceKey == "" {
				record.SourceKey = discovered.SourceKey
			}
			if record.URL == "" {
				record.URL = discovered.URL
			}
			if _, err := c.Sink.SaveCatalogRecord(ctx, c.SourceID, parser, record); err != nil {
				failDetail(detailKey, "record_save_failed")
				return fail(key, "record_save_failed", err)
			}
			res.Records++
		}
		res.Pages++
		next := ""
		if p, ok := c.Adapter.(Paginator); ok {
			next = p.NextPage(index)
		}
		if next != "" && (!SameSite(c.StartURL, next) || seen[next]) {
			next = ""
		}
		switch phase {
		case PhaseVerify:
			res.Cursor = next
			if next == "" {
				res.Stop = StopComplete
				return res, nil
			}
		case PhaseIncremental:
			if reachedMark || next == "" {
				res.Stop = StopComplete
				if reachedMark {
					res.Stop = StopWatermark
				}
				res.Cursor = ""
				return res, c.advanceIncremental(ctx, key, newMarks, cp.Cursor, now)
			}
			res.Cursor = next
		default: // baseline
			watermark := cp.Watermark
			if next == "" {
				// Full pass finished: the next baseline starts again from the head.
				t := now().UTC()
				watermark = &t
			}
			if err := c.Checkpoints.AdvanceCheckpoint(ctx, key, next, watermark, 1); err != nil {
				return res, fmt.Errorf("advance checkpoint: %w", err)
			}
			cp.Watermark = watermark
			res.Cursor = next
			if next == "" {
				res.Stop = StopComplete
				return res, nil
			}
		}
		page = next
	}
}

// advanceIncremental stores this run's head marks, topped up with the previous
// marks (newest first, at most incrementalMarkKeys), and the run time as the
// new incremental watermark.
func (c *Crawl) advanceIncremental(ctx context.Context, key CheckpointKey, marks []string, previous string, now func() time.Time) error {
	t := now().UTC()
	keep := append([]string{}, marks...)
	for _, m := range strings.Split(previous, "\n") {
		if len(keep) >= incrementalMarkKeys {
			break
		}
		if m = strings.TrimSpace(m); m != "" && !slices.Contains(keep, m) {
			keep = append(keep, m)
		}
	}
	if err := c.Checkpoints.AdvanceCheckpoint(ctx, key, strings.Join(keep, "\n"), &t, 1); err != nil {
		return fmt.Errorf("advance checkpoint: %w", err)
	}
	return nil
}
