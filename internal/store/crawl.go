package store

import (
	"avxxx/internal/catalog"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// CatalogCheckpoints adapts the checkpoint data-access layer to
// catalog.CheckpointStore.
type CatalogCheckpoints struct{ S *Store }

var _ catalog.CheckpointStore = CatalogCheckpoints{}

func (c CatalogCheckpoints) key(k catalog.CheckpointKey) CrawlCheckpoint {
	return CrawlCheckpoint{SourceID: k.SourceID, TaskType: k.TaskType, PartitionKey: k.Partition, ParserVersion: k.ParserVersion}
}

func (c CatalogCheckpoints) LoadCheckpoint(ctx context.Context, k catalog.CheckpointKey) (catalog.Checkpoint, bool, error) {
	cp, err := c.S.Checkpoint(ctx, k.SourceID, k.TaskType, k.Partition, k.ParserVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Checkpoint{}, false, nil
	}
	if err != nil {
		return catalog.Checkpoint{}, false, err
	}
	return catalog.Checkpoint{Cursor: cp.Cursor, Watermark: cp.Watermark, CompletedPages: cp.CompletedPages, LastErrorCode: cp.LastErrorCode}, true, nil
}

func (c CatalogCheckpoints) AdvanceCheckpoint(ctx context.Context, k catalog.CheckpointKey, cursor string, watermark *time.Time, pages int64) error {
	return c.S.AdvanceCheckpoint(ctx, c.key(k), cursor, watermark, pages)
}

func (c CatalogCheckpoints) FailCheckpoint(ctx context.Context, k catalog.CheckpointKey, code string) error {
	return c.S.FailCheckpoint(ctx, c.key(k), code)
}

// defaultMinRequestInterval matches the source_crawl_policies column default
// and applies to sources without a policy row.
const defaultMinRequestInterval = 5 * time.Second

// FetchPolicy builds today's remaining request budget for a source from
// source_crawl_policies and source_crawl_usage. exhausted is true when a
// configured daily budget has already been spent.
func (s *Store) FetchPolicy(ctx context.Context, sourceID int64) (policy catalog.FetchPolicy, exhausted bool, err error) {
	var minSeconds *float64
	var pageBudget, byteBudget, pagesUsed, bytesUsed int64
	err = s.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM p.min_request_interval)::float8, COALESCE(p.daily_page_budget,0), COALESCE(p.daily_byte_budget,0), COALESCE(u.pages,0), COALESCE(u.bytes,0)
FROM sources s
LEFT JOIN source_crawl_policies p ON p.source_id=s.id
LEFT JOIN source_crawl_usage u ON u.source_id=s.id AND u.usage_date=current_date
WHERE s.id=$1`, sourceID).Scan(&minSeconds, &pageBudget, &byteBudget, &pagesUsed, &bytesUsed)
	if err != nil {
		return policy, false, err
	}
	policy.MinInterval = defaultMinRequestInterval
	if minSeconds != nil {
		policy.MinInterval = time.Duration(*minSeconds * float64(time.Second))
	}
	if pageBudget > 0 {
		policy.MaxPages = pageBudget - pagesUsed
		if policy.MaxPages <= 0 {
			return policy, true, nil
		}
	}
	if byteBudget > 0 {
		policy.MaxBytes = byteBudget - bytesUsed
		if policy.MaxBytes <= 0 {
			return policy, true, nil
		}
	}
	return policy, false, nil
}

// RecordCrawlUsage adds a run's request usage to today's counters.
func (s *Store) RecordCrawlUsage(ctx context.Context, sourceID, pages, bytes int64) error {
	if pages == 0 && bytes == 0 {
		return nil
	}
	_, err := s.Exec(ctx, `INSERT INTO source_crawl_usage(source_id,usage_date,pages,bytes) VALUES($1,current_date,$2,$3)
ON CONFLICT(source_id,usage_date) DO UPDATE SET pages=source_crawl_usage.pages+EXCLUDED.pages,bytes=source_crawl_usage.bytes+EXCLUDED.bytes,updated_at=now()`, sourceID, pages, bytes)
	return err
}

// BlockSource parks one source until now()+retryAfter. Claim and EnqueueDue
// skip it until then; other sources' jobs are untouched.
func (s *Store) BlockSource(ctx context.Context, sourceID int64, retryAfter time.Duration, reason string) error {
	if retryAfter <= 0 {
		retryAfter = catalog.DefaultBlockBackoff
	}
	_, err := s.Exec(ctx, `UPDATE sources SET blocked_until=now()+make_interval(secs => $2::float8),blocked_reason=$3,
next_run_at=GREATEST(COALESCE(next_run_at,now()),now()+make_interval(secs => $2::float8)) WHERE id=$1`, sourceID, retryAfter.Seconds(), reason)
	return err
}

// ClearSourceBlock removes a block after a successful run.
func (s *Store) ClearSourceBlock(ctx context.Context, sourceID int64) error {
	_, err := s.Exec(ctx, `UPDATE sources SET blocked_until=NULL,blocked_reason=NULL WHERE id=$1 AND (blocked_until IS NOT NULL OR blocked_reason IS NOT NULL)`, sourceID)
	return err
}
