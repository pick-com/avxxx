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
//	audit -once -rules R-SEO,R-META   run only these rules
//	audit -health      run the source health job now, print the report, exit
//	audit -curves      run the replay / retention curve job now, print the report, exit
//	audit -release 42  inbound check (R-AGE + R-META) for one release
//	audit -recs        run the recommendation similarity + trending jobs now, print, exit
//
// The worker also claims 'release_curves' jobs (hourly, cmd/scheduler): the
// PRD 14.3 replay peak and retention curves (internal/playcurve), and
// 'search_stats' jobs (hourly, cmd/scheduler): search_query_daily and the
// hot-search board (internal/search).
//
//	audit -search      run the search statistics job now, print the report, exit
//
// It also claims 'media_link' jobs (every MEDIA_LINK_EVERY, default 1h,
// cmd/scheduler; or POST /api/admin/media-links/run): offline release ->
// playback provider linking (internal/medialink). Politeness is set by
// MEDIA_LINK_MIN_INTERVAL (default 2s, never lower), MEDIA_LINK_MAX_PER_RUN
// (default 200) and MEDIA_LINK_DAILY_CAP (default 2000).
//
//	audit -media-link  run the media link job now, print the report, exit
//
// It also claims 'completeness_snapshot' jobs (nightly, cmd/scheduler): the
// daily field-completeness snapshot behind the admin completeness report's
// trend (internal/completeness).
//
//	audit -completeness  store today's completeness snapshot now, print it, exit
//
// It also claims 'crosscheck' jobs (every CROSSCHECK_EVERY, default 1h,
// cmd/scheduler): third-party lookups of releases that miss fields or have
// open field conflicts (internal/crosscheck; ThePornDB with
// THEPORNDB_API_TOKEN). The source row's license gate, crawl policy and the
// crawler contact (CATALOG_CONTACT) apply; CROSSCHECK_MAX_PER_RUN (default
// 100) and CROSSCHECK_DAILY_CAP (default 1000) bound each run.
//
//	audit -crosscheck  run the cross-check job now, print the report, exit
//
// It also claims 'media_fetch' and 'media_purge' jobs (every
// MEDIA_FETCH_EVERY / MEDIA_PURGE_EVERY, default 1h, cmd/scheduler; or POST
// /api/admin/media/jobs): catalog images from image-permitted sources into
// S3-compatible storage, and their removal after suppression or takedown
// (internal/images, docs/DATA_RETENTION_AND_IMAGES.md). Storage is S3_*;
// downloads need CATALOG_CONTACT like catalog crawls and are paced by
// IMAGE_FETCH_MIN_INTERVAL (default 2s) and IMAGE_FETCH_MAX_PER_RUN.
//
//	audit -media-fetch   run the image fetch job now, print the report, exit
//	audit -media-purge   run the image purge job now, print the report, exit
//
// It also claims 'watch_stats' (hourly) and 'watch_stats_nightly' (nightly,
// cmd/scheduler) jobs: 观看统计 user_watch_daily per user and local day
// (internal/watchstats, PRD 20). The hourly run rebuilds the dates that are
// "today" in some time zone, the nightly run the three dates before them.
//
//	audit -watch-stats                       rebuild the hourly dates now, print, exit
//	audit -watch-stats -watch-stats-days 90  backfill the last 90 dates (event retention)
//
// It also claims 'site_metrics' jobs (hourly, cmd/scheduler): the
// AdminDashboard plays / DAU / watch time / completion KPIs per UTC day in
// site_daily_metrics (internal/sitemetrics). A run rebuilds yesterday and
// today; the first run on an empty table backfills the 90-day retention.
//
//	audit -site-metrics                         run the job now (yesterday and today, or the backfill), print, exit
//	audit -site-metrics -site-metrics-days 90   rebuild the last N days (at most the event retention)
//
// It also claims 'i18n_machine' jobs (every I18N_MT_EVERY, default 1h,
// cmd/scheduler; or 机器翻译 in /admin/i18n): OpenRouter machine translation
// (internal/mtjob, internal/i18n/openrouter.go) of admin-selected fields and
// of missing release titles in I18N_MT_LANGS, saved as 'machine' drafts for
// human review. Without OPENROUTER_API_KEY the run records not_configured.
// OPENROUTER_DAILY_CAP (default 200 requests per UTC day, shared through
// mt_usage) and I18N_MT_MAX_PER_RUN (default 60 strings) bound it.
//
//	audit -mt          run the machine translation job now, print the report, exit
package main

import (
	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"avxxx/internal/completeness"
	"avxxx/internal/crosscheck"
	"avxxx/internal/i18n"
	"avxxx/internal/images"
	"avxxx/internal/media"
	"avxxx/internal/medialink"
	"avxxx/internal/mtjob"
	"avxxx/internal/objstore"
	"avxxx/internal/obs"
	"avxxx/internal/playcurve"
	"avxxx/internal/recs"
	"avxxx/internal/search"
	"avxxx/internal/sitemetrics"
	"avxxx/internal/store"
	"avxxx/internal/watchstats"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	once := flag.Bool("once", false, "run the full audit once and exit")
	rules := flag.String("rules", "", "with -once: comma-separated rules to run (default all)")
	release := flag.Int64("release", 0, "run the inbound check for one release id and exit")
	health := flag.Bool("health", false, "run the source health job once and exit")
	curves := flag.Bool("curves", false, "run the replay / retention curve job once and exit")
	recsNow := flag.Bool("recs", false, "run the recommendation similarity and trending jobs once and exit")
	searchNow := flag.Bool("search", false, "run the search statistics job once and exit")
	mediaLinkNow := flag.Bool("media-link", false, "run the offline media link job once and exit")
	completenessNow := flag.Bool("completeness", false, "store today's completeness snapshot and exit")
	crossCheckNow := flag.Bool("crosscheck", false, "run the third-party cross-check job once and exit")
	mediaFetchNow := flag.Bool("media-fetch", false, "run the image fetch job once and exit")
	mediaPurgeNow := flag.Bool("media-purge", false, "run the image purge job once and exit")
	watchStatsNow := flag.Bool("watch-stats", false, "rebuild user_watch_daily (WatchStats) once and exit")
	watchStatsDays := flag.Int("watch-stats-days", 0, "with -watch-stats: rebuild the last N dates instead of today's (at most the 90-day event retention)")
	siteMetricsNow := flag.Bool("site-metrics", false, "rebuild site_daily_metrics (AdminDashboard KPIs) once and exit")
	siteMetricsDays := flag.Int("site-metrics-days", 0, "with -site-metrics: rebuild the last N days (at most the 90-day event retention)")
	mtNow := flag.Bool("mt", false, "run the machine translation job once and exit")
	flag.Parse()

	obs.SetupLogging("audit")
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		obs.Fatal("open store", err)
	}
	defer s.Close()

	switch {
	case *release > 0:
		res, err := audit.CheckRelease(ctx, s, *release, audit.DefaultAgeConfig, time.Now())
		printJSON(res)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *health:
		rep, err := (&audit.HealthRunner{Store: s, Config: healthConfig()}).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *curves:
		rep, err := (&playcurve.Runner{Store: s}).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *recsNow:
		rn := &recs.Runner{Store: s}
		sim, err := rn.RunSimilarity(ctx)
		printJSON(sim)
		if err != nil {
			obs.Fatal("run failed", err)
		}
		tr, err := rn.RunTrending(ctx)
		printJSON(tr)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *searchNow:
		rep, err := (&search.Runner{Store: s}).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *mediaLinkNow:
		rep, err := mediaLinkRunner(s, 0).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *crossCheckNow:
		rep, err := crossCheckRunner(s, 0).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *mediaFetchNow:
		rn, err := mediaFetchRunner(ctx, s, 0)
		if err != nil {
			obs.Fatal("image storage", err)
		}
		rep, err := rn.Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *mediaPurgeNow:
		rn, err := mediaPurgeRunner(ctx, s, 0)
		if err != nil {
			obs.Fatal("image storage", err)
		}
		rep, err := rn.Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *watchStatsNow:
		rn := &watchstats.Runner{Store: s}
		if *watchStatsDays > 0 {
			rn.Days = watchstats.BackfillDays(time.Now(), *watchStatsDays)
		}
		rep, err := rn.Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *siteMetricsNow:
		rn := &sitemetrics.Runner{Store: s}
		if *siteMetricsDays > 0 {
			rn.Days = sitemetrics.BackfillDays(time.Now(), *siteMetricsDays)
		}
		rep, err := rn.Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *mtNow:
		rep, err := mtRunner(s, 0).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *completenessNow:
		rep, err := runCompleteness(ctx, s, 0)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	case *once:
		var only []audit.Rule
		for _, x := range strings.Split(*rules, ",") {
			if x = strings.TrimSpace(x); x == "" {
				continue
			}
			if !audit.KnownRule(audit.Rule(x)) {
				obs.Fatal("unknown rule", fmt.Errorf("%q", x))
			}
			only = append(only, audit.Rule(x))
		}
		rep, err := (&audit.Runner{Store: s, Rules: only}).Run(ctx)
		printJSON(rep)
		if err != nil {
			obs.Fatal("run failed", err)
		}
	default:
		obs.RegisterPool(s.Stat)
		if err := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9093")); err != nil {
			obs.Fatal("metrics listener", err)
		}
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
			obs.JobClaimErrors.WithLabelValues("audit", "reap").Inc()
			slog.Error("reap expired jobs", "err", err)
		}
		claimed := false
		for _, kind := range []string{store.JobKindSourceHealth, store.JobKindRecsTrending, store.JobKindReleaseCurves, store.JobKindSearchStats, store.JobKindAudit, store.JobKindRecsSimilarity, store.JobKindMediaLink, store.JobKindCompletenessSnapshot, store.JobKindCrossCheck, store.JobKindMediaPurge, store.JobKindMediaFetch, store.JobKindWatchStats, store.JobKindWatchStatsNightly, store.JobKindSiteMetrics, store.JobKindMachineTranslation} {
			id, version, err := s.ClaimJob(ctx, kind)
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			if err != nil {
				obs.JobClaimErrors.WithLabelValues(kind, "claim").Inc()
				slog.Error("claim job", "kind", kind, "err", err)
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

// mediaLinkRunner builds the media_link job with the providers it links
// (medialink.Providers) and the MEDIA_LINK_* politeness settings.
func mediaLinkRunner(s *store.Store, jobID int64) *medialink.Runner {
	reg := media.NewRegistry()
	reg.Register(media.NewJavtifulAdapter())
	cfg, warn := medialink.ConfigFromEnv(os.Getenv)
	for _, w := range warn {
		slog.Warn("media link config", "warning", w)
	}
	return &medialink.Runner{Store: s, Search: media.NewService(reg), Config: cfg, JobID: jobID}
}

// crossCheckRunner builds the crosscheck job over every lookup adapter
// (catalog.LookupAdapter) with the CROSSCHECK_* caps. Without the crawler
// contact or the adapter's credentials each adapter's run records why it
// did nothing instead of failing.
func crossCheckRunner(s *store.Store, jobID int64) *crosscheck.Runner {
	cfg, warn := crosscheck.ConfigFromEnv(os.Getenv)
	for _, w := range warn {
		slog.Warn("crosscheck config", "warning", w)
	}
	var adapters []catalog.LookupAdapter
	for _, a := range catalog.Adapters() {
		if l, ok := a.(catalog.LookupAdapter); ok {
			adapters = append(adapters, l)
		}
	}
	ua, err := catalog.UserAgentFromEnv(os.Getenv)
	if err != nil {
		slog.Warn("crosscheck disabled", "err", err)
	}
	return &crosscheck.Runner{Store: s, Adapters: adapters, Config: cfg, HTTPClient: &http.Client{Timeout: 30 * time.Second},
		UserAgent: ua, Robots: catalog.NewRobots(&http.Client{Timeout: 20 * time.Second}, ua), JobID: jobID}
}

// imageStorage opens the S3_* object storage (nil when S3_ENDPOINT is
// empty: the image jobs then report storage_not_configured). The bucket is
// created, private, when missing; an unreachable store fails the job.
func imageStorage(ctx context.Context) (objstore.Storage, error) {
	return objstore.FromEnv(ctx, os.Getenv, true)
}

// mediaFetchRunner builds the media_fetch job: storage, the crawler
// User-Agent (CATALOG_CONTACT; empty stops the job) and IMAGE_* limits.
func mediaFetchRunner(ctx context.Context, s *store.Store, jobID int64) (*images.FetchRunner, error) {
	objects, err := imageStorage(ctx)
	if err != nil {
		return nil, err
	}
	cfg, warn := images.FetchConfigFromEnv(os.Getenv)
	for _, w := range warn {
		slog.Warn("image fetch config", "warning", w)
	}
	ua, err := catalog.UserAgentFromEnv(os.Getenv)
	if err != nil {
		slog.Warn("image fetch disabled", "err", err)
	}
	return &images.FetchRunner{Store: s, Objects: objects, Config: cfg, UserAgent: ua, JobID: jobID}, nil
}

// mediaPurgeRunner builds the media_purge job (MEDIA_PURGE_AFTER, default 24h).
func mediaPurgeRunner(ctx context.Context, s *store.Store, jobID int64) (*images.PurgeRunner, error) {
	objects, err := imageStorage(ctx)
	if err != nil {
		return nil, err
	}
	cfg, warn := images.PurgeConfigFromEnv(os.Getenv)
	for _, w := range warn {
		slog.Warn("image purge config", "warning", w)
	}
	return &images.PurgeRunner{Store: s, Objects: objects, Config: cfg, JobID: jobID}, nil
}

// mtRunner builds the machine translation job: the OpenRouter translator
// (nil without OPENROUTER_API_KEY, so the run records not_configured) with
// the daily cap shared through mt_usage, and the I18N_MT_* bounds. Warnings
// never contain the key.
func mtRunner(s *store.Store, jobID int64) *mtjob.Runner {
	cfg, warn := mtjob.ConfigFromEnv(os.Getenv)
	orc, owarn := i18n.OpenRouterConfigFromEnv(os.Getenv)
	for _, w := range append(warn, owarn...) {
		slog.Warn("machine translation config", "warning", w)
	}
	cfg.BatchSize = orc.BatchSize
	rn := &mtjob.Runner{Store: s, Config: cfg, JobID: jobID}
	if tr, err := i18n.NewOpenRouter(orc, s.MTBudget(store.MTProviderOpenRouter), &http.Client{}); err == nil {
		rn.Translator = tr
	}
	return rn
}

// execute runs one job of kind and returns its result counts.
func execute(ctx context.Context, s *store.Store, kind string, id int64) (any, error) {
	switch kind {
	case store.JobKindRecsSimilarity:
		return (&recs.Runner{Store: s, JobID: id}).RunSimilarity(ctx)
	case store.JobKindRecsTrending:
		return (&recs.Runner{Store: s, JobID: id}).RunTrending(ctx)
	case store.JobKindSearchStats:
		return (&search.Runner{Store: s, JobID: id}).Run(ctx)
	case store.JobKindMediaLink:
		return mediaLinkRunner(s, id).Run(ctx)
	case store.JobKindCrossCheck:
		return crossCheckRunner(s, id).Run(ctx)
	case store.JobKindMediaFetch:
		rn, err := mediaFetchRunner(ctx, s, id)
		if err != nil {
			return nil, err
		}
		return rn.Run(ctx)
	case store.JobKindMediaPurge:
		rn, err := mediaPurgeRunner(ctx, s, id)
		if err != nil {
			return nil, err
		}
		return rn.Run(ctx)
	case store.JobKindWatchStats, store.JobKindWatchStatsNightly:
		return (&watchstats.Runner{Store: s, JobID: id, Kind: kind}).Run(ctx)
	case store.JobKindSiteMetrics:
		return (&sitemetrics.Runner{Store: s, JobID: id}).Run(ctx)
	case store.JobKindMachineTranslation:
		return mtRunner(s, id).Run(ctx)
	}
	if kind == store.JobKindSourceHealth {
		rep, err := (&audit.HealthRunner{Store: s, Config: healthConfig(), JobID: id}).Run(ctx)
		return map[string]any{"sources": rep.Sources, "recorded": rep.Recorded, "scored": rep.Scored,
			"insufficient_data": rep.Insufficient, "status_changes": len(rep.Changes), "pruned": rep.Pruned}, err
	}
	if kind == store.JobKindCompletenessSnapshot {
		return runCompleteness(ctx, s, id)
	}
	if kind == store.JobKindReleaseCurves {
		rep, err := (&playcurve.Runner{Store: s}).Run(ctx)
		return rep, err
	}
	rep, err := (&audit.Runner{Store: s, JobID: id}).Run(ctx)
	counts := map[string]any{}
	for _, r := range rep.Runs {
		counts[r.Job] = map[string]any{"result": r.Result, "checked": r.Checked, "fixed": r.Fixed, "issues": r.Issues}
	}
	return counts, err
}

func runJob(ctx context.Context, s *store.Store, kind string, id, version int64) {
	jl := slog.With("job_id", id, "kind", kind)
	runCtx, cancel := context.WithCancel(obs.WithLogger(ctx, jl))
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
					obs.JobLeaseLost.WithLabelValues(kind).Inc()
					jl.Warn("job heartbeat failed; cancelling", "err", err)
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
		obs.JobClaimErrors.WithLabelValues(kind, "finish").Inc()
		jl.Error("finish job", "err", err)
	}
	obs.ObserveJob(kind, time.Since(start), cause)
	lvl := slog.LevelInfo
	if cause != nil {
		lvl = slog.LevelWarn
	}
	jl.Log(ctx, lvl, "job finished", "duration_ms", time.Since(start).Milliseconds(), "err", cause)
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// runCompleteness stores today's completeness snapshot and records the run
// in nightly_runs (job 'completeness_snapshot', AdminDashboard).
func runCompleteness(ctx context.Context, s *store.Store, jobID int64) (completeness.RunReport, error) {
	start := time.Now()
	rep, err := (&completeness.Runner{Store: s, JobID: jobID}).Run(ctx)
	run := audit.Run{Job: completeness.JobKind, JobID: jobID, StartedAt: start, DurationMS: time.Since(start).Milliseconds(),
		Result: "ok", Detail: map[string]any{"entities": rep.Entities, "required_complete": rep.Complete}}
	for _, n := range rep.Entities {
		run.Checked += int(n)
	}
	if err != nil {
		run.Result = "failed"
	}
	if rerr := s.RecordRun(ctx, run); rerr != nil {
		slog.Warn("record completeness run", "err", rerr)
	}
	return rep, err
}
