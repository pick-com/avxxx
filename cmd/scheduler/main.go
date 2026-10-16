package main

import (
	"avxxx/internal/store"
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	interval := 60 * time.Second
	for ctx.Err() == nil {
		n, err := s.EnqueueDue(ctx)
		if err != nil {
			log.Printf("enqueue due sources: %v", err)
		} else if n > 0 {
			log.Printf("enqueued %d source jobs", n)
		}
		// New-work notifications: publish due upcoming releases, then fan out
		// each newly published release once (no network I/O; DB writes only).
		if res, err := s.NotifyFanout(ctx, 200); err != nil {
			log.Printf("notify fan-out: %v", err)
		} else if res.Promoted > 0 || res.Releases > 0 {
			log.Printf("notify fan-out: promoted=%d releases=%d notifications=%d outbox=%d", res.Promoted, res.Releases, res.Notifications, res.Outbox)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
