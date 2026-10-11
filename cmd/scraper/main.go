package main

import (
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"log"
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
	for ctx.Err() == nil {
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
		items, cause := scraper.Fetch(ctx, source)
		finish, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		e = s.Finish(finish, id, items, cause)
		cancel()
		if e != nil {
			log.Printf("finish job %d: %v", id, e)
		}
		log.Printf("job=%d items=%d error=%v", id, len(items), cause)
	}
}
