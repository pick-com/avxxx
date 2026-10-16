// Package crawlrun executes one claimed crawl job: a catalog adapter run
// (budgeted Fetcher, robots.txt, checkpoints, block parking, verify reports)
// or an RSS feed fetch. cmd/scraper's worker loop and the end-to-end
// acceptance suite (internal/acceptance) share it, so the suite exercises
// exactly the production path.
package crawlrun

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/obs"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// MaxInlineRetryWait is the longest 429 Retry-After a worker sleeps through
// before parking the source as blocked.
const MaxInlineRetryWait = 30 * time.Second

// Runner holds what a worker needs to run jobs.
type Runner struct {
	Store    *store.Store
	Registry *catalog.Registry
	// UserAgent is the contact User-Agent (catalog.UserAgentFromEnv); when
	// UserAgentErr is set catalog jobs fail with config_error.
	UserAgent    string
	UserAgentErr error
	Robots       *catalog.Robots
	// FetchFeed fetches an RSS/Atom source (default scraper.Fetch).
	FetchFeed func(context.Context, model.Source) ([]model.Item, error)
}

// Execute runs one claimed job and returns the feed items (RSS only) and the
// run error; the caller passes both to store.Finish.
func (r *Runner) Execute(ctx context.Context, job store.ClaimedJob) ([]model.Item, error) {
	source := job.Source
	if source.Adapter != "" && source.Adapter != "rss" {
		if r.UserAgentErr != nil {
			return nil, &catalog.GateError{Code: catalog.CodeConfig, Reason: r.UserAgentErr.Error()}
		}
		return nil, RunCatalog(ctx, r.Store, r.Registry, job, r.UserAgent, r.Robots)
	}
	fetch := r.FetchFeed
	if fetch == nil {
		fetch = scraper.Fetch
	}
	items, err := fetch(ctx, source)
	obs.CrawlRecords.WithLabelValues(strconv.FormatInt(source.ID, 10)).Add(float64(len(items)))
	return items, err
}

// RunCatalog runs one catalog job for its phase: budget and robots-checked
// fetches, checkpoint advance only after fully persisted batches, verify
// reports instead of catalog writes, and block parking for 403/429/challenge.
func RunCatalog(ctx context.Context, st *store.Store, registry *catalog.Registry, job store.ClaimedJob, ua string, robots *catalog.Robots) error {
	source := job.Source
	adapter, ok := registry.Get(source.Adapter)
	if !ok {
		return errors.New("unknown catalog adapter: " + source.Adapter)
	}
	policy, err := st.FetchPolicy(ctx, source.ID, job.Phase)
	if err != nil {
		return err
	}
	sid := strconv.FormatInt(source.ID, 10)
	jl := obs.L(ctx)
	if policy.Exhausted {
		obs.CrawlBudgetExhausted.WithLabelValues(sid).Inc()
		jl.Info("daily request budget exhausted; skipping run")
		return nil
	}
	redactor, _ := adapter.(catalog.Redactor)
	client, err := catalog.HTTPClientFor(adapter, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		// Not configured (e.g. DMM credentials missing): a config_error that
		// the queue does not retry; nothing was fetched.
		return &catalog.GateError{Code: catalog.CodeConfig, Reason: fmt.Sprintf("source %d (%s): %v", source.ID, source.Adapter, err)}
	}
	policy.Fetch.MaxRetryWait = MaxInlineRetryWait
	fetcher := catalog.NewFetcher(client, policy.Fetch)
	fetcher.UserAgent, fetcher.Robots, fetcher.Automatic = ua, robots, job.Phase.Automatic()
	crawl := &catalog.Crawl{
		SourceID:    source.ID,
		StartURL:    source.URL,
		Phase:       job.Phase,
		Adapter:     adapter,
		Fetcher:     fetcher,
		Checkpoints: store.CatalogCheckpoints{S: st},
		Sink:        st,
		MaxPages:    policy.MaxPages,
	}
	var report *catalog.ParseReport
	if job.Phase == catalog.PhaseVerify {
		// Verify jobs never write to the catalog: they parse into a report
		// stored on the job for the admin to review.
		report = catalog.NewParseReport(adapter.Name(), 10)
		crawl.Sink, crawl.MaxPages = report, job.PageCap
	}
	if redactor != nil {
		// Defence in depth: records are scrubbed before the catalog or the
		// verify report sees them.
		crawl.Sink = catalog.RedactingSink{Sink: crawl.Sink, Redactor: redactor}
	}
	res, err := crawl.Run(ctx)
	// Credentials must never reach job errors, block reasons or logs.
	err = catalog.SanitizeError(err, redactor)
	// Usage, results and block state are recorded even if the job context was cancelled.
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pages, bytes := fetcher.Usage()
	obs.CrawlPages.WithLabelValues(sid).Add(float64(pages))
	obs.CrawlBytes.WithLabelValues(sid).Add(float64(bytes))
	if report == nil {
		// Verify runs parse into the report and save nothing.
		obs.CrawlRecords.WithLabelValues(sid).Add(float64(res.Records))
	}
	if uerr := st.RecordCrawlUsage(bg, source.ID, job.Phase, pages, bytes); uerr != nil {
		jl.Error("record crawl usage", "err", uerr)
	}
	counts := map[string]any{"phase": job.Phase, "pages": res.Pages, "records": res.Records, "robots_skipped": res.RobotsSkipped, "parse_skipped": res.Skipped,
		"stop": res.Stop, "watermark_gap": res.WatermarkGap, "requests": pages, "bytes": bytes}
	if report != nil {
		counts["verify"] = report.Summary()
	}
	if rerr := st.SetJobResult(bg, job.ID, counts); rerr != nil {
		jl.Error("record job result", "err", rerr)
	}
	if b, blocked := catalog.IsBlocked(err); blocked {
		obs.CrawlBlocked.WithLabelValues(sid).Inc()
		if berr := st.BlockSource(bg, source.ID, b.RetryAfter, b.Reason); berr != nil {
			jl.Error("mark source blocked", "err", berr)
		}
	} else if err == nil {
		if cerr := st.ClearSourceBlock(bg, source.ID); cerr != nil {
			jl.Error("clear source block", "err", cerr)
		}
		if job.Phase == catalog.PhaseBaseline && res.Stop == catalog.StopComplete {
			if merr := st.MarkBaselineComplete(bg, source.ID); merr != nil {
				jl.Error("mark baseline complete", "err", merr)
			}
		}
	}
	cursor := res.Cursor
	if redactor != nil {
		cursor = redactor.Redact(cursor)
	}
	jl.Info("catalog crawl", "pages", res.Pages, "records", res.Records, "parse_skipped", res.Skipped, "robots_skipped", res.RobotsSkipped,
		"stop", res.Stop, "watermark_gap", res.WatermarkGap, "cursor", cursor, "requests", pages, "bytes", bytes, "err", err)
	return err
}
