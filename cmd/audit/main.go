// Command audit runs the catalog data-quality audit (internal/audit).
//
// Default mode is a worker: it claims jobs of kind 'audit' from the shared
// jobs table (enqueued nightly by cmd/scheduler or by POST
// /api/admin/audit/run), keeps the 90 s lease alive with heartbeats, and
// marks the job succeeded/failed like the crawl worker does. Expired leases
// are reaped by the same ReapExpired as crawl jobs.
//
//	audit              worker loop
//	audit -once        run every rule now, print the report, exit
//	audit -release 42  inbound check (R-AGE + R-META) for one release
package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	once := flag.Bool("once", false, "run the full audit once and exit")
	release := flag.Int64("release", 0, "run the inbound check for one release id and exit")
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
		id, version, err := s.ClaimJob(ctx, store.JobKindAudit)
		if errors.Is(err, pgx.ErrNoRows) {
			sleep(ctx, 10*time.Second)
			continue
		}
		if err != nil {
			log.Printf("claim audit job: %v", err)
			sleep(ctx, 30*time.Second)
			continue
		}
		runJob(ctx, s, id, version)
	}
}

func runJob(ctx context.Context, s *store.Store, id, version int64) {
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
					log.Printf("audit job %d heartbeat: %v", id, err)
					cancel()
					return
				}
			}
		}
	}()
	start := time.Now()
	rep, cause := (&audit.Runner{Store: s, JobID: id}).Run(runCtx)
	cancel()
	<-done
	counts := map[string]any{}
	for _, r := range rep.Runs {
		counts[r.Job] = map[string]any{"result": r.Result, "checked": r.Checked, "fixed": r.Fixed, "issues": r.Issues}
	}
	fin, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := s.FinishJob(fin, id, version, counts, cause); err != nil {
		log.Printf("finish audit job %d: %v", id, err)
	}
	log.Printf("audit job=%d duration=%s error=%v", id, time.Since(start).Round(time.Millisecond), cause)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}
