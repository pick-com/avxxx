package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"log"
	"os"
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
	// The nightly audit is enqueued once per day at AUDIT_NIGHTLY_AT (HH:MM in
	// the process time zone, TZ; default 04:30 per PRD 10). cmd/audit runs it.
	auditAt := os.Getenv("AUDIT_NIGHTLY_AT")
	if auditAt == "" {
		auditAt = "04:30"
	}
	auditHour, auditMinute, err := audit.ParseClock(auditAt)
	if err != nil {
		log.Fatal(err)
	}
	interval := 60 * time.Second
	for ctx.Err() == nil {
		slot := audit.LastSlot(time.Now(), auditHour, auditMinute)
		if ok, err := s.EnqueueAuditSince(ctx, slot); err != nil {
			log.Printf("enqueue nightly audit: %v", err)
		} else if ok {
			log.Printf("enqueued nightly audit for %s", slot.Format(time.RFC3339))
		}
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
