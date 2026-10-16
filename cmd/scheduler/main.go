package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/playcurve"
	"avxxx/internal/recs"
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
	// Playback source health (tech plan 3.3) is enqueued once per
	// SOURCE_HEALTH_EVERY slot (default 10m, aligned to the clock); the
	// cmd/audit worker runs it.
	healthEvery := 10 * time.Minute
	if v := os.Getenv("SOURCE_HEALTH_EVERY"); v != "" {
		if healthEvery, err = time.ParseDuration(v); err != nil || healthEvery < time.Minute {
			log.Fatalf("invalid SOURCE_HEALTH_EVERY %q: want a duration of at least 1m", v)
		}
	}
	// Recommendations (internal/recs, run by cmd/audit): similarity nightly at
	// RECS_NIGHTLY_AT (default 04:30, PRD 10), trending boards once per
	// RECS_TRENDING_EVERY slot (default 1h, PRD 4.2 每小时更新).
	recsAt := os.Getenv("RECS_NIGHTLY_AT")
	if recsAt == "" {
		recsAt = "04:30"
	}
	recsHour, recsMinute, err := audit.ParseClock(recsAt)
	if err != nil {
		log.Fatal(err)
	}
	trendingEvery := time.Hour
	if v := os.Getenv("RECS_TRENDING_EVERY"); v != "" {
		if trendingEvery, err = time.ParseDuration(v); err != nil || trendingEvery < time.Minute {
			log.Fatalf("invalid RECS_TRENDING_EVERY %q: want a duration of at least 1m", v)
		}
	}
	interval := 60 * time.Second
	for ctx.Err() == nil {
		if ok, err := s.EnqueueKindSince(ctx, recs.JobKindSimilarity, audit.LastSlot(time.Now(), recsHour, recsMinute)); err != nil {
			log.Printf("enqueue recs similarity: %v", err)
		} else if ok {
			log.Printf("enqueued nightly recs similarity")
		}
		if _, err := s.EnqueueKindSince(ctx, recs.JobKindTrending, time.Now().Truncate(trendingEvery)); err != nil {
			log.Printf("enqueue recs trending: %v", err)
		}
		slot := audit.LastSlot(time.Now(), auditHour, auditMinute)
		if ok, err := s.EnqueueAuditSince(ctx, slot); err != nil {
			log.Printf("enqueue nightly audit: %v", err)
		} else if ok {
			log.Printf("enqueued nightly audit for %s", slot.Format(time.RFC3339))
		}
		if _, err := s.EnqueueKindSince(ctx, audit.JobKindSourceHealth, time.Now().Truncate(healthEvery)); err != nil {
			log.Printf("enqueue source health: %v", err)
		}
		// Replay peak / retention curves (PRD 14.3): hourly, run by cmd/audit.
		if _, err := s.EnqueueKindSince(ctx, playcurve.JobKind, time.Now().Truncate(time.Hour)); err != nil {
			log.Printf("enqueue release curves: %v", err)
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
		// Danmaku peak curves (PRD 5.5.1). Runs inline like the fan-out: a
		// bounded, DB-only pass over releases queued by new danmaku, moderation
		// or mutes; the store applies the cadence (1 min for releases younger
		// than 7 days, 10 min otherwise), so this 60 s tick is the 1 min clock.
		if res, err := s.RecomputeDanmakuCurves(ctx, 200); err != nil {
			log.Printf("danmaku curves: %v", err)
		} else if res.Releases > 0 {
			log.Printf("danmaku curves: releases=%d curves=%d", res.Releases, res.Curves)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
