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
	client := &http.Client{Timeout: 30 * time.Second}
	index, err := catalog.FetchDocument(ctx, client, source.URL)
	if err != nil {
		return err
	}
	records, err := adapter.Discover(ctx, index)
	if err != nil {
		return err
	}
	for _, discovered := range records {
		doc, fetchErr := catalog.FetchDocument(ctx, client, discovered.URL)
		if fetchErr != nil {
			return fetchErr
		}
		record, parseErr := adapter.Parse(ctx, doc, discovered.EntityType)
		if parseErr != nil {
			return parseErr
		}
		if record.SourceKey == "" {
			record.SourceKey = discovered.SourceKey
		}
		if record.URL == "" {
			record.URL = discovered.URL
		}
		if _, saveErr := st.SaveCatalogRecord(ctx, source.ID, adapter.Name(), record); saveErr != nil {
			return saveErr
		}
	}
	return nil
}
