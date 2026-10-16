package obs

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// ---------------------------------------------------------------- pool

// poolCollector exposes pgxpool.Stat for one process's pool.
type poolCollector struct {
	stat func() *pgxpool.Stat
}

var (
	poolConnsDesc    = desc("avxxx_db_pool_conns", "Connections in the pgx pool by state (acquired|idle|constructing|total).", "state")
	poolMaxDesc      = desc("avxxx_db_pool_max_conns", "Configured maximum pool size.")
	poolAcquiresDesc = desc("avxxx_db_pool_acquires_total", "Successful connection acquires.")
	poolWaitDesc     = desc("avxxx_db_pool_acquire_wait_seconds_total", "Total time spent waiting to acquire a connection.")
	poolEmptyDesc    = desc("avxxx_db_pool_empty_acquires_total", "Acquires that had to wait because the pool was empty.")
	poolCancelDesc   = desc("avxxx_db_pool_canceled_acquires_total", "Acquires cancelled by their context.")
)

func desc(name, help string, labels ...string) *prometheus.Desc {
	note(name, labels)
	return prometheus.NewDesc(name, help, labels, nil)
}

// RegisterPool exposes a pgx pool's statistics (pass pool.Stat). Call once
// per process.
func RegisterPool(stat func() *pgxpool.Stat) {
	Registry.MustRegister(&poolCollector{stat: stat})
}

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{poolConnsDesc, poolMaxDesc, poolAcquiresDesc, poolWaitDesc, poolEmptyDesc, poolCancelDesc} {
		ch <- d
	}
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stat()
	if s == nil {
		return
	}
	g := prometheus.GaugeValue
	ch <- prometheus.MustNewConstMetric(poolConnsDesc, g, float64(s.AcquiredConns()), "acquired")
	ch <- prometheus.MustNewConstMetric(poolConnsDesc, g, float64(s.IdleConns()), "idle")
	ch <- prometheus.MustNewConstMetric(poolConnsDesc, g, float64(s.ConstructingConns()), "constructing")
	ch <- prometheus.MustNewConstMetric(poolConnsDesc, g, float64(s.TotalConns()), "total")
	ch <- prometheus.MustNewConstMetric(poolMaxDesc, g, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquiresDesc, prometheus.CounterValue, float64(s.AcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolWaitDesc, prometheus.CounterValue, s.AcquireDuration().Seconds())
	ch <- prometheus.MustNewConstMetric(poolEmptyDesc, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
	ch <- prometheus.MustNewConstMetric(poolCancelDesc, prometheus.CounterValue, float64(s.CanceledAcquireCount()))
}

// ---------------------------------------------------------------- health

var healthUpDesc = desc("avxxx_health_up", "1 if the /api/health check (database ping) passes at scrape time.")

type healthCollector struct{ check func(context.Context) error }

// RegisterHealth exposes check (the same check as /api/health) as
// avxxx_health_up, evaluated on every scrape with a 3 s budget.
func RegisterHealth(check func(context.Context) error) {
	Registry.MustRegister(&healthCollector{check: check})
}

func (c *healthCollector) Describe(ch chan<- *prometheus.Desc) { ch <- healthUpDesc }

func (c *healthCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v := 1.0
	if err := c.check(ctx); err != nil {
		v = 0
	}
	ch <- prometheus.MustNewConstMetric(healthUpDesc, prometheus.GaugeValue, v)
}

// ---------------------------------------------------------------- database state

// DBSnapshot is the queue / catalog / data-quality state read from
// PostgreSQL (store.MetricsSnapshot). Only one process exposes it (the
// scheduler, a singleton) so replicas never double-report.
type DBSnapshot struct {
	Jobs             []JobCount         // pending (due / scheduled) and running jobs
	ExpiredLeases    map[string]int64   // kind → running jobs whose lease is in the past
	OldestDueAge     map[string]float64 // kind → seconds the oldest due pending job has waited
	FailedLastHour   map[string]int64   // kind → jobs that finished failed in the last hour
	CheckpointLastOK map[int64]time.Time
	BlockedSources   int64 // crawl sources currently parked by a block
	OpenIssues       []IssueCount
	Holds            map[string]int64 // rule → releases ever held (review_events action 'hold')
	PlaybackSources  []BandCount
	Health           HealthPool
}

// JobCount is one (kind, state) queue bucket; state is due|scheduled|running.
type JobCount struct {
	Kind, State string
	N           int64
}

// IssueCount is one (rule, severity) bucket of open audit issues.
type IssueCount struct {
	Rule, Severity string
	N              int64
}

// BandCount is one (band, status) bucket of playback sources.
type BandCount struct {
	Band, Status string
	N            int64
}

// HealthPool is the pooled playback health over every source's latest run in
// the last 24 hours (the AdminDashboard 片源健康分).
type HealthPool struct {
	Score     *float64 // 0–100, nil without attempts
	Attempts  int64
	Playable  int64
	LastRunAt *time.Time
}

// Pairing is the PRD 14.5 play_request → play_end pairing over a window.
type Pairing struct {
	Requests int64 // distinct play_id with a play_request
	Paired   int64 // of those, with a play_end
}

// DBSource reads the snapshot; *store.Store implements it.
type DBSource interface {
	MetricsSnapshot(ctx context.Context) (DBSnapshot, error)
	PlayPairing(ctx context.Context, from, to time.Time) (Pairing, error)
}

var (
	jobsDesc        = desc("avxxx_jobs_queued", "Jobs by kind and state (due = pending and available, scheduled = pending in backoff, running).", "kind", "state")
	expiredDesc     = desc("avxxx_jobs_expired_leases", "Running jobs whose lease expired (not yet reaped) by kind.", "kind")
	oldestDueDesc   = desc("avxxx_jobs_oldest_due_age_seconds", "Age of the oldest due pending job by kind.", "kind")
	failedDesc      = desc("avxxx_jobs_failed_last_hour", "Jobs that ended failed in the last hour by kind.", "kind")
	checkpointDesc  = desc("avxxx_crawl_checkpoint_last_success_timestamp_seconds", "Latest checkpoint success of an enabled crawl source.", "source_id")
	blockedDesc     = desc("avxxx_crawl_sources_blocked", "Crawl sources currently parked by a block (Retry-After).")
	issuesDesc      = desc("avxxx_audit_open_issues", "Open data-quality audit issues by rule and severity.", "rule", "severity")
	holdsDesc       = desc("avxxx_audit_holds_total", "Releases held by the audit (review_events 'hold') by rule; R-AGE pages someone.", "rule")
	sourcesDesc     = desc("avxxx_playback_sources", "Playback sources by health band and status.", "band", "status")
	healthDesc      = desc("avxxx_source_health_score", "Pooled playback source health score (0-100) over the latest 24 h runs.")
	attemptsDesc    = desc("avxxx_source_health_attempts", "Play attempts behind the pooled source health score.")
	playableDesc    = desc("avxxx_source_health_playable_ratio", "Playable attempts / attempts over the latest 24 h source health runs.")
	healthRunDesc   = desc("avxxx_source_health_last_run_timestamp_seconds", "End of the newest source health window.")
	pairingDesc     = desc("avxxx_play_pairing_ratio", "Share of play_request play_ids with a play_end (PRD 14.5), 24 h window ending 1 h ago.")
	pairingReqsDesc = desc("avxxx_play_pairing_requests", "play_request play_ids in the pairing window.")
	dbUpDesc        = desc("avxxx_dbstats_up", "1 if the last database snapshot succeeded.")
	dbDurDesc       = desc("avxxx_dbstats_duration_seconds", "Time taken by the last database snapshot.")
)

// dbCollector serves DBSnapshot with a TTL cache so a fast scrape interval
// never turns into a query storm; the pairing query (a scan of a day of
// playback events) is cached longer.
type dbCollector struct {
	src        DBSource
	ttl        time.Duration
	pairingTTL time.Duration
	timeout    time.Duration
	now        func() time.Time

	mu       sync.Mutex
	snap     DBSnapshot
	snapErr  error
	snapAt   time.Time
	snapDur  time.Duration
	pair     Pairing
	pairErr  error
	pairAt   time.Time
	havePair bool
	haveSnap bool
}

// RegisterDB exposes src's database state. Only one process should call it.
func RegisterDB(src DBSource) {
	Registry.MustRegister(newDBCollector(src))
}

func newDBCollector(src DBSource) *dbCollector {
	return &dbCollector{src: src, ttl: 30 * time.Second, pairingTTL: 5 * time.Minute, timeout: 10 * time.Second, now: time.Now}
}

func (c *dbCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{jobsDesc, expiredDesc, oldestDueDesc, failedDesc, checkpointDesc, blockedDesc,
		issuesDesc, holdsDesc, sourcesDesc, healthDesc, attemptsDesc, playableDesc, healthRunDesc, pairingDesc,
		pairingReqsDesc, dbUpDesc, dbDurDesc} {
		ch <- d
	}
}

func (c *dbCollector) refresh() {
	now := c.now()
	if !c.haveSnap || now.Sub(c.snapAt) >= c.ttl {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		start := time.Now()
		c.snap, c.snapErr = c.src.MetricsSnapshot(ctx)
		c.snapDur = time.Since(start)
		cancel()
		c.snapAt, c.haveSnap = now, true
		if c.snapErr != nil {
			slog.Warn("metrics database snapshot failed", "err", c.snapErr)
		}
	}
	if !c.havePair || now.Sub(c.pairAt) >= c.pairingTTL {
		ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
		to := now.Add(-time.Hour) // let late play_end batches arrive
		c.pair, c.pairErr = c.src.PlayPairing(ctx, to.Add(-24*time.Hour), to)
		cancel()
		c.pairAt, c.havePair = now, true
		if c.pairErr != nil {
			slog.Warn("metrics play pairing query failed", "err", c.pairErr)
		}
	}
}

func (c *dbCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.refresh()
	g, ctr := prometheus.GaugeValue, prometheus.CounterValue
	up := 1.0
	if c.snapErr != nil {
		up = 0
	}
	ch <- prometheus.MustNewConstMetric(dbUpDesc, g, up)
	ch <- prometheus.MustNewConstMetric(dbDurDesc, g, c.snapDur.Seconds())
	if c.pairErr == nil {
		ch <- prometheus.MustNewConstMetric(pairingReqsDesc, g, float64(c.pair.Requests))
		if c.pair.Requests > 0 {
			ch <- prometheus.MustNewConstMetric(pairingDesc, g, float64(c.pair.Paired)/float64(c.pair.Requests))
		}
	}
	if c.snapErr != nil {
		return
	}
	s := c.snap
	for _, j := range s.Jobs {
		ch <- prometheus.MustNewConstMetric(jobsDesc, g, float64(j.N), j.Kind, j.State)
	}
	for k, n := range s.ExpiredLeases {
		ch <- prometheus.MustNewConstMetric(expiredDesc, g, float64(n), k)
	}
	for k, v := range s.OldestDueAge {
		ch <- prometheus.MustNewConstMetric(oldestDueDesc, g, v, k)
	}
	for k, n := range s.FailedLastHour {
		ch <- prometheus.MustNewConstMetric(failedDesc, g, float64(n), k)
	}
	for id, t := range s.CheckpointLastOK {
		ch <- prometheus.MustNewConstMetric(checkpointDesc, g, float64(t.Unix()), strconv.FormatInt(id, 10))
	}
	ch <- prometheus.MustNewConstMetric(blockedDesc, g, float64(s.BlockedSources))
	for _, is := range s.OpenIssues {
		ch <- prometheus.MustNewConstMetric(issuesDesc, g, float64(is.N), is.Rule, is.Severity)
	}
	for r, n := range s.Holds {
		ch <- prometheus.MustNewConstMetric(holdsDesc, ctr, float64(n), r)
	}
	for _, b := range s.PlaybackSources {
		ch <- prometheus.MustNewConstMetric(sourcesDesc, g, float64(b.N), b.Band, b.Status)
	}
	ch <- prometheus.MustNewConstMetric(attemptsDesc, g, float64(s.Health.Attempts))
	if s.Health.Score != nil {
		ch <- prometheus.MustNewConstMetric(healthDesc, g, *s.Health.Score)
	}
	if s.Health.Attempts > 0 {
		ch <- prometheus.MustNewConstMetric(playableDesc, g, float64(s.Health.Playable)/float64(s.Health.Attempts))
	}
	if s.Health.LastRunAt != nil {
		ch <- prometheus.MustNewConstMetric(healthRunDesc, g, float64(s.Health.LastRunAt.Unix()))
	}
}
