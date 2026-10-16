package main

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/obs"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "fetch one discovery page with -adapter, print the mapped records as JSON and exit without touching the database")
	dryAdapter := flag.String("adapter", "", "adapter name for -dry-run (e.g. dmm)")
	dryURL := flag.String("url", "", "discovery URL for -dry-run (default: the adapter's seeded start URL, if it has one)")
	flag.Parse()
	obs.SetupLogging("scraper")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	registry := newRegistry()
	if *dryRun {
		if e := dryRunCatalog(ctx, os.Stdout, registry, *dryAdapter, *dryURL, os.Getenv); e != nil {
			obs.Fatal("dry run", e)
		}
		return
	}
	s, e := store.Open(ctx)
	if e != nil {
		obs.Fatal("open store", e)
	}
	defer s.Close()
	obs.RegisterPool(s.Stat)
	if e := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9092")); e != nil {
		obs.Fatal("metrics listener", e)
	}
	// The crawler identifies itself honestly, with a contact operators can
	// use (CATALOG_CONTACT or CATALOG_USER_AGENT). Without one, catalog jobs
	// fail with config_error; RSS feeds still run.
	ua, uaErr := catalog.UserAgentFromEnv(os.Getenv)
	if uaErr != nil {
		slog.Warn("catalog crawls disabled", "err", uaErr)
	}
	robots := catalog.NewRobots(&http.Client{Timeout: 20 * time.Second}, ua)
	for ctx.Err() == nil {
		if e := s.ReapExpired(ctx); e != nil {
			obs.JobClaimErrors.WithLabelValues("crawl", "reap").Inc()
			slog.Error("reap expired jobs", "err", e)
		}
		job, e := s.Claim(ctx)
		id, source := job.ID, job.Source
		if errors.Is(e, pgx.ErrNoRows) {
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		}
		if e != nil {
			obs.JobClaimErrors.WithLabelValues("crawl", "claim").Inc()
			slog.Error("claim crawl job", "err", e)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			continue
		}
		start := time.Now()
		jl := slog.With("job_id", id, "kind", "crawl", "phase", job.Phase, "source_id", source.ID, "adapter", source.Adapter)
		fetchCtx, stopHeartbeat := context.WithCancel(obs.WithLogger(ctx, jl))
		doneHeartbeat := make(chan struct{})
		go func() {
			t := time.NewTicker(20 * time.Second)
			defer t.Stop()
			defer close(doneHeartbeat)
			for {
				select {
				case <-fetchCtx.Done():
					return
				case <-t.C:
					if e := s.Heartbeat(fetchCtx, id, s.WorkerID(), 0); e != nil {
						obs.JobLeaseLost.WithLabelValues("crawl").Inc()
						jl.Warn("job heartbeat failed", "err", e)
						return
					}
				}
			}
		}()
		var items []model.Item
		var cause error
		if source.Adapter != "" && source.Adapter != "rss" {
			if uaErr != nil {
				cause = &catalog.GateError{Code: catalog.CodeConfig, Reason: uaErr.Error()}
			} else {
				cause = runCatalog(fetchCtx, s, registry, job, ua, robots)
			}
		} else {
			items, cause = scraper.Fetch(fetchCtx, source)
			obs.CrawlRecords.WithLabelValues(strconv.FormatInt(source.ID, 10)).Add(float64(len(items)))
		}
		stopHeartbeat()
		<-doneHeartbeat
		finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		e = s.Finish(finish, id, items, cause)
		cancel()
		if e != nil {
			obs.JobClaimErrors.WithLabelValues("crawl", "finish").Inc()
			jl.Error("finish job", "err", e)
		}
		obs.ObserveJob("crawl", time.Since(start), cause)
		lvl := slog.LevelInfo
		if cause != nil {
			lvl = slog.LevelWarn
		}
		jl.Log(ctx, lvl, "job finished", "items", len(items), "duration_ms", time.Since(start).Milliseconds(), "err", cause)
	}
}

// newRegistry registers every catalog adapter (catalog.Adapters). Adapters
// that need credentials (dmm) are registered even when unconfigured; their
// jobs then fail with a clear "not configured" config_error instead of
// crashing the worker.
func newRegistry() *catalog.Registry {
	registry := catalog.NewRegistry()
	for _, a := range catalog.Adapters() {
		if e := registry.Register(a); e != nil {
			obs.Fatal("register adapter", e, "adapter", a.Name())
		}
	}
	return registry
}

func runCatalog(ctx context.Context, st *store.Store, registry *catalog.Registry, job store.ClaimedJob, ua string, robots *catalog.Robots) error {
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
	policy.Fetch.MaxRetryWait = maxInlineRetryWait
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

// maxInlineRetryWait is the longest 429 Retry-After a worker sleeps through
// before parking the source as blocked.
const maxInlineRetryWait = 30 * time.Second
