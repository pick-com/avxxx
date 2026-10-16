package catalog

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	TaskDiscovery = "catalog_discovery"
	TaskDetail    = "catalog_detail"
)

// CheckpointKey identifies one checkpoint row (source, task type, partition).
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
)

// Crawl runs paginated discovery for one source: resume from the stored
// cursor, fetch each discovery page and its detail pages through Fetcher,
// persist every record, then advance the checkpoint to the next page.
type Crawl struct {
	SourceID    int64
	StartURL    string
	Adapter     Adapter
	Fetcher     *Fetcher
	Checkpoints CheckpointStore
	Sink        RecordSink
	// MaxPages caps discovery pages per run (0 = unlimited).
	MaxPages int
	Now      func() time.Time
}

type CrawlResult struct {
	Pages   int
	Records int
	// Skipped counts detail pages without a usable entity (ErrSkipRecord).
	Skipped int
	Cursor  string
	Stop    StopReason
}

func (c *Crawl) Run(ctx context.Context) (CrawlResult, error) {
	if c.Adapter == nil || c.Fetcher == nil || c.Checkpoints == nil || c.Sink == nil {
		return CrawlResult{}, errors.New("catalog: crawl is missing adapter, fetcher, checkpoints or sink")
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	parser := c.Adapter.Name()
	key := CheckpointKey{SourceID: c.SourceID, TaskType: TaskDiscovery, ParserVersion: parser}
	cp, _, err := c.Checkpoints.LoadCheckpoint(ctx, key)
	if err != nil {
		return CrawlResult{}, fmt.Errorf("load checkpoint: %w", err)
	}
	page := c.StartURL
	if cp.Cursor != "" && SameSite(c.StartURL, cp.Cursor) {
		page = cp.Cursor
	}
	res := CrawlResult{Cursor: page}
	seen := map[string]bool{}
	fail := func(k CheckpointKey, code string, cause error) (CrawlResult, error) {
		if b, ok := IsBlocked(cause); ok && b != nil {
			code = "source_blocked"
		}
		if ferr := c.Checkpoints.FailCheckpoint(ctx, k, code); ferr != nil {
			return res, errors.Join(cause, fmt.Errorf("record checkpoint failure: %w", ferr))
		}
		return res, cause
	}
	for {
		if c.MaxPages > 0 && res.Pages >= c.MaxPages {
			res.Stop = StopPageLimit
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
		for _, discovered := range records {
			detailKey := CheckpointKey{SourceID: c.SourceID, TaskType: TaskDetail, Partition: discovered.SourceKey, ParserVersion: parser}
			doc, err := c.Fetcher.Fetch(ctx, discovered.URL)
			if errors.Is(err, ErrBudgetExhausted) {
				// Batch incomplete: leave the cursor on this page so the next
				// run re-reads it. Saves are idempotent by source key.
				res.Stop = StopBudget
				return res, nil
			}
			if err != nil {
				if _, blocked := IsBlocked(err); !blocked {
					_ = c.Checkpoints.FailCheckpoint(ctx, detailKey, "detail_fetch_failed")
				}
				return fail(key, "detail_fetch_failed", err)
			}
			record, err := c.Adapter.Parse(ctx, doc, discovered.EntityType)
			if errors.Is(err, ErrSkipRecord) {
				// Nothing to save (e.g. only a site-title heading): note it on
				// the detail checkpoint and keep crawling.
				_ = c.Checkpoints.FailCheckpoint(ctx, detailKey, "parse_skipped")
				res.Skipped++
				continue
			}
			if err != nil {
				_ = c.Checkpoints.FailCheckpoint(ctx, detailKey, "parse_failed")
				return fail(key, "parse_failed", err)
			}
			if record.SourceKey == "" {
				record.SourceKey = discovered.SourceKey
			}
			if record.URL == "" {
				record.URL = discovered.URL
			}
			if _, err := c.Sink.SaveCatalogRecord(ctx, c.SourceID, parser, record); err != nil {
				_ = c.Checkpoints.FailCheckpoint(ctx, detailKey, "record_save_failed")
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
		watermark := cp.Watermark
		if next == "" {
			// Full pass finished: the next run starts again from the head.
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
		page = next
	}
}
