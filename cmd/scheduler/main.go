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
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
