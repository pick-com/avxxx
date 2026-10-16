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
	for ctx.Err() == nil {
		if e := s.ReapExpired(ctx); e != nil {
			log.Printf("reap expired jobs: %v", e)
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
			cause = runCatalog(fetchCtx, s, registry, source)
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

func runCatalog(ctx context.Context, st *store.Store, registry *catalog.Registry, source model.Source) error {
	adapter, ok := registry.Get(source.Adapter)
	if !ok {
		return errors.New("unknown catalog adapter: " + source.Adapter)
	}
	policy, exhausted, err := st.FetchPolicy(ctx, source.ID)
	if err != nil {
		return err
	}
	if exhausted {
		log.Printf("source=%d daily request budget exhausted; skipping run", source.ID)
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
	if uerr := st.RecordCrawlUsage(bg, source.ID, pages, bytes); uerr != nil {
		log.Printf("source=%d record usage: %v", source.ID, uerr)
	}
	if b, blocked := catalog.IsBlocked(err); blocked {
		if berr := st.BlockSource(bg, source.ID, b.RetryAfter, b.Reason); berr != nil {
			log.Printf("source=%d mark blocked: %v", source.ID, berr)
		}
	} else if err == nil {
		if cerr := st.ClearSourceBlock(bg, source.ID); cerr != nil {
			log.Printf("source=%d clear block: %v", source.ID, cerr)
		}
	}
	log.Printf("source=%d catalog pages=%d records=%d stop=%s cursor=%q requests=%d bytes=%d err=%v", source.ID, res.Pages, res.Records, res.Stop, res.Cursor, pages, bytes, err)
	return err
}

// maxInlineRetryWait is the longest 429 Retry-After a worker sleeps through
// before parking the source as blocked.
const maxInlineRetryWait = 30 * time.Second
