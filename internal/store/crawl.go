package store

import (
	"avxxx/internal/catalog"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// defaultBaselinePagesPerRun matches the migration 024 column default: a
// baseline job yields after this many discovery pages so queued incremental
// jobs for the source run in between.
const defaultBaselinePagesPerRun = 20

// CrawlPolicy is one source's crawl schedule and per-phase budgets.
type CrawlPolicy struct {
	SourceID                int64      `json:"source_id"`
	DiscoveryInterval       string     `json:"discovery_interval"`
	DetailRefreshInterval   string     `json:"detail_refresh_interval"`
	MinRequestInterval      string     `json:"min_request_interval"`
	DailyPageBudget         int64      `json:"daily_page_budget"`
	DailyByteBudget         int64      `json:"daily_byte_budget"`
	BaselineDailyPageBudget int64      `json:"baseline_daily_page_budget"`
	BaselineDailyByteBudget int64      `json:"baseline_daily_byte_budget"`
	BaselinePagesPerRun     int64      `json:"baseline_pages_per_run"`
	EnabledAt               *time.Time `json:"enabled_at"`
	BaselineCompletedAt     *time.Time `json:"baseline_completed_at"`
	// ProxyURL is the source's egress (catalog.ValidateSourceProxy): "" =
	// inherit CATALOG_PROXY_URL / HTTPS_PROXY, "direct", "env:CATALOG_PROXY_<X>"
	// or a proxy URL without credentials. On upsert, nil keeps the stored
	// value (older clients do not send it) and "" clears it.
	ProxyURL *string `json:"proxy_url"`
}

func (s *Store) CrawlPolicies(ctx context.Context) ([]CrawlPolicy, error) {
	r, err := s.Query(ctx, `SELECT source_id,COALESCE(discovery_interval::text,''),COALESCE(detail_refresh_interval::text,''),min_request_interval::text,
		daily_page_budget,daily_byte_budget,baseline_daily_page_budget,baseline_daily_byte_budget,baseline_pages_per_run,enabled_at,baseline_completed_at,
		COALESCE(proxy_url,'') FROM source_crawl_policies ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []CrawlPolicy{}
	for r.Next() {
		var p CrawlPolicy
		var proxy string
		if err = r.Scan(&p.SourceID, &p.DiscoveryInterval, &p.DetailRefreshInterval, &p.MinRequestInterval, &p.DailyPageBudget, &p.DailyByteBudget,
			&p.BaselineDailyPageBudget, &p.BaselineDailyByteBudget, &p.BaselinePagesPerRun, &p.EnabledAt, &p.BaselineCompletedAt, &proxy); err != nil {
			return nil, err
		}
		p.ProxyURL = &proxy
		out = append(out, p)
	}
	return out, r.Err()
}

// UpsertCrawlPolicy writes a source's schedule and budgets. Empty intervals
// are NULL (no schedule); BaselinePagesPerRun <= 0 keeps the default.
// Validation messages are matched by the back office
// (web/app/lib/crawlPolicyErrors.ts): keep them in step.
func (s *Store) UpsertCrawlPolicy(ctx context.Context, p CrawlPolicy) error {
	if p.BaselinePagesPerRun <= 0 {
		p.BaselinePagesPerRun = defaultBaselinePagesPerRun
	}
	if p.DailyPageBudget < 0 || p.DailyByteBudget < 0 || p.BaselineDailyPageBudget < 0 || p.BaselineDailyByteBudget < 0 {
		return invalid("budgets must not be negative")
	}
	if p.ProxyURL != nil {
		v := strings.TrimSpace(*p.ProxyURL)
		if err := catalog.ValidateSourceProxy(v); err != nil {
			return invalid("%s", err.Error())
		}
		p.ProxyURL = &v
	}
	_, err := s.Exec(ctx, `INSERT INTO source_crawl_policies(source_id,discovery_interval,detail_refresh_interval,min_request_interval,daily_page_budget,daily_byte_budget,
		baseline_daily_page_budget,baseline_daily_byte_budget,baseline_pages_per_run,proxy_url)
		VALUES($1,NULLIF($2,'')::interval,NULLIF($3,'')::interval,$4::interval,$5,$6,$7,$8,$9,NULLIF($11,''))
		ON CONFLICT(source_id) DO UPDATE SET discovery_interval=EXCLUDED.discovery_interval,detail_refresh_interval=EXCLUDED.detail_refresh_interval,
		min_request_interval=EXCLUDED.min_request_interval,daily_page_budget=EXCLUDED.daily_page_budget,daily_byte_budget=EXCLUDED.daily_byte_budget,
		baseline_daily_page_budget=EXCLUDED.baseline_daily_page_budget,baseline_daily_byte_budget=EXCLUDED.baseline_daily_byte_budget,
		baseline_pages_per_run=EXCLUDED.baseline_pages_per_run,
		proxy_url=CASE WHEN $10::boolean THEN EXCLUDED.proxy_url ELSE source_crawl_policies.proxy_url END`,
		p.SourceID, p.DiscoveryInterval, p.DetailRefreshInterval, p.MinRequestInterval, p.DailyPageBudget, p.DailyByteBudget,
		p.BaselineDailyPageBudget, p.BaselineDailyByteBudget, p.BaselinePagesPerRun, p.ProxyURL != nil, strOr(p.ProxyURL))
	return crawlPolicyError(err)
}

// crawlPolicyError turns the admin's input errors that PostgreSQL catches
// (a malformed interval, an unknown source) into 400s with a stable message
// the back office translates, instead of 500 "request failed".
func crawlPolicyError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "22007", "22008", "22015", "22P02": // invalid_datetime_format, datetime_field_overflow, interval_field_overflow, invalid_text_representation
		return invalid("invalid interval")
	case "23503": // foreign_key_violation: no such source
		return invalid("unknown source")
	}
	return err
}

// RunPolicy is the fetch policy and discovery page cap for one run.
type RunPolicy struct {
	Fetch    catalog.FetchPolicy
	MaxPages int
	// Exhausted is true when the phase's daily budget is already spent.
	Exhausted bool
	// ProxyURL is the source's proxy_url setting ("" = inherit); resolve it
	// with catalog.ResolveEgress.
	ProxyURL string
}

// FetchPolicy builds today's remaining request budget for one phase of a
// source from source_crawl_policies and source_crawl_usage. Budgets are
// separate per phase: incremental spends daily_*_budget, baseline spends
// baseline_daily_*_budget and yields after baseline_pages_per_run pages, so
// a baseline can never starve the incremental pass. Verify runs are capped by
// catalog.VerifyMaxRequests and count against neither.
func (s *Store) FetchPolicy(ctx context.Context, sourceID int64, phase catalog.Phase) (RunPolicy, error) {
	var out RunPolicy
	var minSeconds *float64
	var pageBudget, byteBudget, basePages, baseBytes, perRun, pagesUsed, bytesUsed int64
	err := s.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM p.min_request_interval)::float8, COALESCE(p.daily_page_budget,0), COALESCE(p.daily_byte_budget,0),
		COALESCE(p.baseline_daily_page_budget,0), COALESCE(p.baseline_daily_byte_budget,0), COALESCE(p.baseline_pages_per_run,$3),
		COALESCE(u.pages,0), COALESCE(u.bytes,0), COALESCE(p.proxy_url,'')
FROM sources s
LEFT JOIN source_crawl_policies p ON p.source_id=s.id
LEFT JOIN source_crawl_usage u ON u.source_id=s.id AND u.usage_date=current_date AND u.phase=$2
WHERE s.id=$1`, sourceID, string(phase), defaultBaselinePagesPerRun).Scan(&minSeconds, &pageBudget, &byteBudget, &basePages, &baseBytes, &perRun, &pagesUsed, &bytesUsed, &out.ProxyURL)
	if err != nil {
		return out, err
	}
	out.Fetch.MinInterval = defaultMinRequestInterval
	if minSeconds != nil {
		out.Fetch.MinInterval = time.Duration(*minSeconds * float64(time.Second))
	}
	switch phase {
	case catalog.PhaseVerify:
		out.Fetch.MaxPages = catalog.VerifyMaxRequests
		return out, nil
	case catalog.PhaseBaseline:
		pageBudget, byteBudget = basePages, baseBytes
		out.MaxPages = int(perRun)
	}
	if pageBudget > 0 {
		out.Fetch.MaxPages = pageBudget - pagesUsed
		if out.Fetch.MaxPages <= 0 {
			out.Exhausted = true
			return out, nil
		}
	}
	if byteBudget > 0 {
		out.Fetch.MaxBytes = byteBudget - bytesUsed
		if out.Fetch.MaxBytes <= 0 {
			out.Exhausted = true
			return out, nil
		}
	}
	return out, nil
}

// RecordCrawlUsage adds a run's request usage to today's counters for phase.
func (s *Store) RecordCrawlUsage(ctx context.Context, sourceID int64, phase catalog.Phase, pages, bytes int64) error {
	if pages == 0 && bytes == 0 {
		return nil
	}
	_, err := s.Exec(ctx, `INSERT INTO source_crawl_usage(source_id,usage_date,phase,pages,bytes) VALUES($1,current_date,$2,$3,$4)
ON CONFLICT(source_id,usage_date,phase) DO UPDATE SET pages=source_crawl_usage.pages+EXCLUDED.pages,bytes=source_crawl_usage.bytes+EXCLUDED.bytes,updated_at=now()`, sourceID, string(phase), pages, bytes)
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
