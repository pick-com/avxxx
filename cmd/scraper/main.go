package main

import (
	"avxxx/internal/catalog"
	"avxxx/internal/model"
	"avxxx/internal/obs"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log/slog"
	"net/http"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	obs.SetupLogging("scraper")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, e := store.Open(ctx)
	if e != nil {
		obs.Fatal("open store", e)
	}
	defer s.Close()
	obs.RegisterPool(s.Stat)
	if e := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9092")); e != nil {
		obs.Fatal("metrics listener", e)
	}
	registry := catalog.NewRegistry()
	if e := registry.Register(catalog.NewJAVDatabaseAdapter()); e != nil {
		obs.Fatal("register adapter", e)
	}
	if e := registry.Register(catalog.NewXCITYAdapter()); e != nil {
		obs.Fatal("register adapter", e)
	}
	if e := registry.Register(catalog.NewJavGuruAdapter()); e != nil {
		obs.Fatal("register adapter", e)
	}
	for ctx.Err() == nil {
		if e := s.ReapExpired(ctx); e != nil {
			obs.JobClaimErrors.WithLabelValues("crawl", "reap").Inc()
			slog.Error("reap expired jobs", "err", e)
		}
		id, source, e := s.Claim(ctx)
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
		jl := slog.With("job_id", id, "kind", "crawl", "source_id", source.ID, "adapter", source.Adapter)
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
			cause = runCatalog(fetchCtx, s, registry, source)
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

func runCatalog(ctx context.Context, st *store.Store, registry *catalog.Registry, source model.Source) error {
	adapter, ok := registry.Get(source.Adapter)
	if !ok {
		return errors.New("unknown catalog adapter: " + source.Adapter)
	}
	policy, exhausted, err := st.FetchPolicy(ctx, source.ID)
	if err != nil {
		return err
	}
	sid := strconv.FormatInt(source.ID, 10)
	jl := obs.L(ctx)
	if exhausted {
		obs.CrawlBudgetExhausted.WithLabelValues(sid).Inc()
		jl.Info("daily request budget exhausted; skipping run")
		return nil
	}
	policy.MaxRetryWait = maxInlineRetryWait
	fetcher := catalog.NewFetcher(&http.Client{Timeout: 30 * time.Second}, policy)
	crawl := &catalog.Crawl{
		SourceID:    source.ID,
		StartURL:    source.URL,
		Adapter:     adapter,
		Fetcher:     fetcher,
		Checkpoints: store.CatalogCheckpoints{S: st},
		Sink:        st,
	}
	res, err := crawl.Run(ctx)
	// Usage and block state are recorded even if the job context was cancelled.
	bg, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pages, bytes := fetcher.Usage()
	obs.CrawlPages.WithLabelValues(sid).Add(float64(pages))
	obs.CrawlBytes.WithLabelValues(sid).Add(float64(bytes))
	obs.CrawlRecords.WithLabelValues(sid).Add(float64(res.Records))
	if uerr := st.RecordCrawlUsage(bg, source.ID, pages, bytes); uerr != nil {
		jl.Error("record crawl usage", "err", uerr)
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
	}
	jl.Info("catalog crawl", "pages", res.Pages, "records", res.Records, "stop", res.Stop, "cursor", res.Cursor, "requests", pages, "bytes", bytes, "err", err)
	return err
}

// maxInlineRetryWait is the longest 429 Retry-After a worker sleeps through
// before parking the source as blocked.
const maxInlineRetryWait = 30 * time.Second
