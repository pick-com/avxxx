package main

import (
	"avxxx/internal/catalog"
	"avxxx/internal/crawlrun"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"context"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "fetch one discovery page with -adapter, print the mapped records as JSON and exit without touching the database")
	dryAdapter := flag.String("adapter", "", "adapter name for -dry-run (e.g. dmm, theporndb, maker:s1)")
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
	runner := &crawlrun.Runner{Store: s, Registry: registry, UserAgent: ua, UserAgentErr: uaErr, Robots: robots}
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
		items, cause := runner.Execute(fetchCtx, job)
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
//
// Official maker catalogs register as "maker:<slug>" from the built-in
// profiles, merged with MAKER_PROFILES_FILE when set (an unreadable or
// invalid file stops the worker: a typo must not silently drop a studio).
func newRegistry() *catalog.Registry {
	registry := catalog.NewRegistry()
	adapters, err := catalog.AdaptersFromEnv(os.Getenv)
	if err != nil {
		obs.Fatal("maker profiles", err)
	}
	for _, a := range adapters {
		if e := registry.Register(a); e != nil {
			obs.Fatal("register adapter", e, "adapter", a.Name())
		}
	}
	return registry
}
