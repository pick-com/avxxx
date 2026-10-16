package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/completeness"
	"avxxx/internal/crosscheck"
	"avxxx/internal/images"
	"avxxx/internal/obs"
	"avxxx/internal/playcurve"
	"avxxx/internal/recs"
	"avxxx/internal/search"
	"avxxx/internal/store"
	"avxxx/internal/watchstats"
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	obs.SetupLogging("scheduler") // log.Printf below goes through the redacting slog handler
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	// The scheduler is the singleton, so it alone exposes the database-derived
	// gauges (queue depth, leases, audit, source health, play pairing).
	obs.RegisterPool(s.Stat)
	obs.RegisterDB(s)
	if err := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9091")); err != nil {
		log.Fatal(err)
	}
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
	// Search statistics (internal/search, run by cmd/audit): search events
	// into search_query_daily and the 24 h hot-search board, once per
	// SEARCH_STATS_EVERY slot (default 1h, tech plan 3.13 每小时聚合).
	searchEvery := time.Hour
	if v := os.Getenv("SEARCH_STATS_EVERY"); v != "" {
		if searchEvery, err = time.ParseDuration(v); err != nil || searchEvery < time.Minute {
			log.Fatalf("invalid SEARCH_STATS_EVERY %q: want a duration of at least 1m", v)
		}
	}
	// Offline release -> playback provider linking (internal/medialink, run
	// by cmd/audit): once per MEDIA_LINK_EVERY slot (default 1h). Newly
	// ingested releases are picked up by the next run.
	mediaLinkEvery := time.Hour
	if v := os.Getenv("MEDIA_LINK_EVERY"); v != "" {
		if mediaLinkEvery, err = time.ParseDuration(v); err != nil || mediaLinkEvery < time.Minute {
			log.Fatalf("invalid MEDIA_LINK_EVERY %q: want a duration of at least 1m", v)
		}
	}
	// Third-party cross-check (internal/crosscheck, run by cmd/audit): once
	// per CROSSCHECK_EVERY slot (default 1h). The job itself applies each
	// source's license gate, crawl policy and daily caps, so an unconfigured
	// or disabled source costs one cheap no-op run.
	crossCheckEvery := time.Hour
	if v := os.Getenv("CROSSCHECK_EVERY"); v != "" {
		if crossCheckEvery, err = time.ParseDuration(v); err != nil || crossCheckEvery < time.Minute {
			log.Fatalf("invalid CROSSCHECK_EVERY %q: want a duration of at least 1m", v)
		}
	}
	// Catalog images (internal/images, run by cmd/audit): downloads from
	// image-permitted sources once per MEDIA_FETCH_EVERY slot and the purge
	// of suppressed / taken-down images once per MEDIA_PURGE_EVERY slot
	// (default 1h each; the purge must run well within the 24 h policy).
	mediaFetchEvery, mediaPurgeEvery := time.Hour, time.Hour
	for _, e := range []struct {
		name string
		dst  *time.Duration
		max  time.Duration
	}{{"MEDIA_FETCH_EVERY", &mediaFetchEvery, 24 * time.Hour}, {"MEDIA_PURGE_EVERY", &mediaPurgeEvery, 6 * time.Hour}} {
		if v := os.Getenv(e.name); v != "" {
			if *e.dst, err = time.ParseDuration(v); err != nil || *e.dst < time.Minute || *e.dst > e.max {
				log.Fatalf("invalid %s %q: want a duration between 1m and %s", e.name, v, e.max)
			}
		}
	}
	// Completeness snapshot (internal/completeness, run by cmd/audit): once
	// per day at COMPLETENESS_NIGHTLY_AT (default 05:00, after the audit), the
	// trend behind GET /api/admin/reports/completeness.
	completenessAt := os.Getenv("COMPLETENESS_NIGHTLY_AT")
	if completenessAt == "" {
		completenessAt = "05:00"
	}
	completenessHour, completenessMinute, err := audit.ParseClock(completenessAt)
	if err != nil {
		log.Fatal(err)
	}
	// 观看统计 (internal/watchstats, run by cmd/audit): user_watch_daily for
	// the dates that are "today" somewhere once per WATCH_STATS_EVERY slot
	// (default 1h), and the three dates before once per day at
	// WATCH_STATS_NIGHTLY_AT (default 03:30).
	watchStatsEvery := time.Hour
	if v := os.Getenv("WATCH_STATS_EVERY"); v != "" {
		if watchStatsEvery, err = time.ParseDuration(v); err != nil || watchStatsEvery < 5*time.Minute {
			log.Fatalf("invalid WATCH_STATS_EVERY %q: want a duration of at least 5m", v)
		}
	}
	watchStatsAt := os.Getenv("WATCH_STATS_NIGHTLY_AT")
	if watchStatsAt == "" {
		watchStatsAt = "03:30"
	}
	watchStatsHour, watchStatsMinute, err := audit.ParseClock(watchStatsAt)
	if err != nil {
		log.Fatal(err)
	}
	interval := 60 * time.Second
	var lastAuthPrune time.Time
	for ctx.Err() == nil {
		if ok, err := s.EnqueueKindSince(ctx, recs.JobKindSimilarity, audit.LastSlot(time.Now(), recsHour, recsMinute)); err != nil {
			log.Printf("enqueue recs similarity: %v", err)
		} else if ok {
			log.Printf("enqueued nightly recs similarity")
		}
		if _, err := s.EnqueueKindSince(ctx, recs.JobKindTrending, time.Now().Truncate(trendingEvery)); err != nil {
			log.Printf("enqueue recs trending: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, search.JobKind, time.Now().Truncate(searchEvery)); err != nil {
			log.Printf("enqueue search stats: %v", err)
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
		if ok, err := s.EnqueueKindSince(ctx, completeness.JobKind, audit.LastSlot(time.Now(), completenessHour, completenessMinute)); err != nil {
			log.Printf("enqueue completeness snapshot: %v", err)
		} else if ok {
			log.Printf("enqueued nightly completeness snapshot")
		}
		// Replay peak / retention curves (PRD 14.3): hourly, run by cmd/audit.
		if _, err := s.EnqueueKindSince(ctx, playcurve.JobKind, time.Now().Truncate(time.Hour)); err != nil {
			log.Printf("enqueue release curves: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, store.JobKindMediaLink, time.Now().Truncate(mediaLinkEvery)); err != nil {
			log.Printf("enqueue media link: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, crosscheck.JobKind, time.Now().Truncate(crossCheckEvery)); err != nil {
			log.Printf("enqueue crosscheck: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, images.JobKindFetch, time.Now().Truncate(mediaFetchEvery)); err != nil {
			log.Printf("enqueue media fetch: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, images.JobKindPurge, time.Now().Truncate(mediaPurgeEvery)); err != nil {
			log.Printf("enqueue media purge: %v", err)
		}
		if _, err := s.EnqueueKindSince(ctx, watchstats.JobKind, time.Now().Truncate(watchStatsEvery)); err != nil {
			log.Printf("enqueue watch stats: %v", err)
		}
		if ok, err := s.EnqueueKindSince(ctx, watchstats.JobKindNightly, audit.LastSlot(time.Now(), watchStatsHour, watchStatsMinute)); err != nil {
			log.Printf("enqueue nightly watch stats: %v", err)
		} else if ok {
			log.Printf("enqueued nightly watch stats")
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
		// Account deletion (PRD 14.5, tech plan 3.6): accounts marked for
		// deletion more than 30 days ago are hard-deleted; logging in during
		// the grace period cancels. Expired sessions / OAuth requests / reset
		// tokens are pruned hourly. DB-only and bounded, like the fan-out.
		if n, err := s.PurgeDeletedAccounts(ctx, store.AccountDeletionGrace, 100); err != nil {
			log.Printf("account purge: %v", err)
		} else if n > 0 {
			log.Printf("account purge: deleted %d accounts", n)
		}
		if now := time.Now(); now.Sub(lastAuthPrune) >= time.Hour {
			lastAuthPrune = now
			if n, err := s.PruneExpiredAuth(ctx); err != nil {
				log.Printf("auth prune: %v", err)
			} else if n > 0 {
				log.Printf("auth prune: removed %d expired rows", n)
			}
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
