package main

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, e := store.Open(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	registry := catalog.NewRegistry()
	if e := registry.Register(catalog.NewJAVDatabaseAdapter()); e != nil {
		log.Fatal(e)
	}
	if e := registry.Register(catalog.NewXCITYAdapter()); e != nil {
		log.Fatal(e)
	}
	if e := registry.Register(catalog.NewJavGuruAdapter()); e != nil {
		log.Fatal(e)
	}
	// The crawler identifies itself honestly, with a contact operators can
	// use (CATALOG_CONTACT or CATALOG_USER_AGENT). Without one, catalog jobs
	// fail with config_error; RSS feeds still run.
	ua, uaErr := catalog.UserAgentFromEnv(os.Getenv)
	if uaErr != nil {
		log.Printf("catalog crawls disabled: %v", uaErr)
	}
	robots := catalog.NewRobots(&http.Client{Timeout: 20 * time.Second}, ua)
	for ctx.Err() == nil {
		if e := s.ReapExpired(ctx); e != nil {
			log.Printf("reap expired jobs: %v", e)
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
			log.Printf("claim: %v", e)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			continue
		}
		fetchCtx, stopHeartbeat := context.WithCancel(ctx)
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
						log.Printf("job %d heartbeat: %v", id, e)
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
		}
		stopHeartbeat()
		<-doneHeartbeat
		finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		e = s.Finish(finish, id, items, cause)
		cancel()
		if e != nil {
			log.Printf("finish job %d: %v", id, e)
		}
		log.Printf("job=%d items=%d error=%v", id, len(items), cause)
	}
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
	if policy.Exhausted {
		log.Printf("source=%d phase=%s daily request budget exhausted; skipping run", source.ID, job.Phase)
		return nil
	}
	policy.Fetch.MaxRetryWait = maxInlineRetryWait
	fetcher := catalog.NewFetcher(&http.Client{Timeout: 30 * time.Second}, policy.Fetch)
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
	res, err := crawl.Run(ctx)
	// Usage, results and block state are recorded even if the job context was cancelled.
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pages, bytes := fetcher.Usage()
	if uerr := st.RecordCrawlUsage(bg, source.ID, job.Phase, pages, bytes); uerr != nil {
		log.Printf("source=%d record usage: %v", source.ID, uerr)
	}
	counts := map[string]any{"phase": job.Phase, "pages": res.Pages, "records": res.Records, "robots_skipped": res.Skipped,
		"stop": res.Stop, "watermark_gap": res.WatermarkGap, "requests": pages, "bytes": bytes}
	if report != nil {
		counts["verify"] = report.Summary()
	}
	if rerr := st.SetJobResult(bg, job.ID, counts); rerr != nil {
		log.Printf("job=%d record result: %v", job.ID, rerr)
	}
	if b, blocked := catalog.IsBlocked(err); blocked {
		if berr := st.BlockSource(bg, source.ID, b.RetryAfter, b.Reason); berr != nil {
			log.Printf("source=%d mark blocked: %v", source.ID, berr)
		}
	} else if err == nil {
		if cerr := st.ClearSourceBlock(bg, source.ID); cerr != nil {
			log.Printf("source=%d clear block: %v", source.ID, cerr)
		}
		if job.Phase == catalog.PhaseBaseline && res.Stop == catalog.StopComplete {
			if merr := st.MarkBaselineComplete(bg, source.ID); merr != nil {
				log.Printf("source=%d mark baseline complete: %v", source.ID, merr)
			}
		}
	}
	log.Printf("source=%d phase=%s catalog pages=%d records=%d skipped=%d stop=%s gap=%v cursor=%q requests=%d bytes=%d err=%v",
		source.ID, job.Phase, res.Pages, res.Records, res.Skipped, res.Stop, res.WatermarkGap, res.Cursor, pages, bytes, err)
	return err
}

// maxInlineRetryWait is the longest 429 Retry-After a worker sleeps through
// before parking the source as blocked.
const maxInlineRetryWait = 30 * time.Second
