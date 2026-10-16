// Command audit runs the catalog data-quality audit, the playback source
// health job (internal/audit) and the recommendation jobs (internal/recs).
//
// Default mode is a worker: it claims sourceless jobs of kind
// 'source_health' (every 10 minutes, cmd/scheduler) and 'audit' (nightly, or
// POST /api/admin/audit/run) from the shared jobs table, source health
// first since it is short and frequent. It also runs 'recs_trending' (hourly)
// and 'recs_similarity' (nightly at 04:30, PRD 10, or POST
// /api/admin/recs/rebuild). It keeps the 90 s lease alive with
// heartbeats and marks the job succeeded/failed like the crawl worker does.
// Expired leases are reaped by the same ReapExpired as crawl jobs.
//
//	audit              worker loop
//	audit -once        run every rule now, print the report, exit
//	audit -health      run the source health job now, print the report, exit
//	audit -release 42  inbound check (R-AGE + R-META) for one release
//	audit -recs        run the recommendation similarity + trending jobs now, print, exit
package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	once := flag.Bool("once", false, "run the full audit once and exit")
	release := flag.Int64("release", 0, "run the inbound check for one release id and exit")
	health := flag.Bool("health", false, "run the source health job once and exit")
	recsNow := flag.Bool("recs", false, "run the recommendation similarity and trending jobs once and exit")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	switch {
	case *release > 0:
		res, err := audit.CheckRelease(ctx, s, *release, audit.DefaultAgeConfig, time.Now())
		printJSON(res)
		if err != nil {
			log.Fatal(err)
		}
	case *health:
		rep, err := (&audit.HealthRunner{Store: s, Config: healthConfig()}).Run(ctx)
		printJSON(rep)
		if err != nil {
			log.Fatal(err)
		}
	case *recsNow:
		rn := &recs.Runner{Store: s}
		sim, err := rn.RunSimilarity(ctx)
		printJSON(sim)
		if err != nil {
			log.Fatal(err)
		}
		tr, err := rn.RunTrending(ctx)
		printJSON(tr)
		if err != nil {
			log.Fatal(err)
		}
	case *once:
		rep, err := (&audit.Runner{Store: s}).Run(ctx)
		printJSON(rep)
		if err != nil {
			log.Fatal(err)
		}
	default:
		work(ctx, s)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func work(ctx context.Context, s *store.Store) {
	for ctx.Err() == nil {
		if err := s.ReapExpired(ctx); err != nil {
			log.Printf("reap expired jobs: %v", err)
		}
		claimed := false
		for _, kind := range []string{store.JobKindSourceHealth, store.JobKindRecsTrending, store.JobKindAudit, store.JobKindRecsSimilarity} {
			id, version, err := s.ClaimJob(ctx, kind)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				log.Printf("claim %s job: %v", kind, err)
				continue
			}
			claimed = true
			runJob(ctx, s, kind, id, version)
			break
		}
		if !claimed {
			sleep(ctx, 10*time.Second)
		}
	}
}

// healthConfig reads SOURCE_HEALTH_FIRST_FRAME_MS and
// SOURCE_HEALTH_MIN_ATTEMPTS; unset or invalid values keep the defaults.
func healthConfig() audit.HealthConfig {
	var c audit.HealthConfig
	if n, err := strconv.ParseInt(os.Getenv("SOURCE_HEALTH_FIRST_FRAME_MS"), 10, 64); err == nil && n > 0 {
		c.FirstFrameOKMS = n
	}
	if n, err := strconv.Atoi(os.Getenv("SOURCE_HEALTH_MIN_ATTEMPTS")); err == nil && n > 0 {
		c.MinAttempts = n
	}
	return c
}

// execute runs one job of kind and returns its result counts.
func execute(ctx context.Context, s *store.Store, kind string, id int64) (any, error) {
	switch kind {
	case store.JobKindRecsSimilarity:
		return (&recs.Runner{Store: s, JobID: id}).RunSimilarity(ctx)
	case store.JobKindRecsTrending:
		return (&recs.Runner{Store: s, JobID: id}).RunTrending(ctx)
	}
	if kind == store.JobKindSourceHealth {
		rep, err := (&audit.HealthRunner{Store: s, Config: healthConfig(), JobID: id}).Run(ctx)
		return map[string]any{"sources": rep.Sources, "recorded": rep.Recorded, "scored": rep.Scored,
			"insufficient_data": rep.Insufficient, "status_changes": len(rep.Changes), "pruned": rep.Pruned}, err
	}
	rep, err := (&audit.Runner{Store: s, JobID: id}).Run(ctx)
	counts := map[string]any{}
	for _, r := range rep.Runs {
		counts[r.Job] = map[string]any{"result": r.Result, "checked": r.Checked, "fixed": r.Fixed, "issues": r.Issues}
	}
	return counts, err
}

func runJob(ctx context.Context, s *store.Store, kind string, id, version int64) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { // keep the lease; losing it cancels the run
		defer close(done)
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				if err := s.Heartbeat(runCtx, id, s.WorkerID(), version); err != nil {
					log.Printf("%s job %d heartbeat: %v", kind, id, err)
					cancel()
					return
				}
			}
		}
	}()
	start := time.Now()
	counts, cause := execute(runCtx, s, kind, id)
	cancel()
	<-done
	fin, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := s.FinishJob(fin, id, version, counts, cause); err != nil {
		log.Printf("finish %s job %d: %v", kind, id, err)
	}
	log.Printf("%s job=%d duration=%s error=%v", kind, id, time.Since(start).Round(time.Millisecond), cause)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
