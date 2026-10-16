package obs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry holds every avxxx metric plus the Go runtime and process
// collectors. Each binary serves it on its own internal listener
// (ServeMetrics); metric vectors a binary never touches are simply absent
// from its exposition.
var Registry = prometheus.NewRegistry()

// MetricInfo describes one registered metric family (see Catalog).
type MetricInfo struct {
	Name   string
	Labels []string
}

// catalog lists every metric registered through this package, so tests can
// check label sets for cardinality.
var catalog []MetricInfo

// Catalog returns the metrics this package registers and their label names.
func Catalog() []MetricInfo { return append([]MetricInfo(nil), catalog...) }

func note(name string, labels []string) { catalog = append(catalog, MetricInfo{name, labels}) }

func counterVec(name, help string, labels ...string) *prometheus.CounterVec {
	note(name, labels)
	v := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	Registry.MustRegister(v)
	return v
}

func histogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	note(name, labels)
	v := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: buckets}, labels)
	Registry.MustRegister(v)
	return v
}

// LatencyBuckets resolve the PRD targets: recs P99 ≤ 50 ms, release detail
// P99 ≤ 150 ms (tech plan 3.12).
var LatencyBuckets = []float64{.005, .01, .025, .05, .075, .1, .15, .25, .5, 1, 2.5, 5, 10}

// JobBuckets span a 1 s source-health pass to an hour-long nightly audit.
var JobBuckets = []float64{.1, .5, 1, 5, 15, 30, 60, 120, 300, 600, 1800, 3600}

// Metrics. Labels are bounded: route is the ServeMux pattern (never the raw
// path), kind/channel/status/outcome are code constants, and source_id is a
// crawl source (admin-provisioned, tens of rows) — never a release, slug,
// user, device or playback source.
var (
	HTTPRequests = counterVec("avxxx_http_requests_total",
		"HTTP requests by route pattern, method and status code.", "method", "route", "code")
	HTTPDuration = histogramVec("avxxx_http_request_duration_seconds",
		"HTTP request latency by route pattern, method and status class.", LatencyBuckets, "method", "route", "status_class")
	HTTPResponseBytes = counterVec("avxxx_http_response_bytes_total",
		"HTTP response body bytes by route pattern.", "route")

	// AgeGate counts site-entry 18+ gate answers by result
	// (ok|denied|locked|invalid); no birth date is ever recorded.
	AgeGate = counterVec("avxxx_age_gate_total",
		"Site-entry age gate answers by result (ok|denied|locked|invalid).", "result")

	JobDuration = histogramVec("avxxx_job_duration_seconds",
		"Duration of finished jobs by kind and result (ok|error).", JobBuckets, "kind", "result")
	JobLeaseLost = counterVec("avxxx_job_lease_lost_total",
		"Jobs whose heartbeat failed (lease lost) by kind.", "kind")
	JobClaimErrors = counterVec("avxxx_job_claim_errors_total",
		"Job claim / finish / reap database errors by kind and op.", "kind", "op")

	CrawlPages = counterVec("avxxx_crawl_pages_total",
		"Pages fetched by catalog crawls by crawl source.", "source_id")
	CrawlBytes = counterVec("avxxx_crawl_bytes_total",
		"Bytes fetched by catalog crawls by crawl source.", "source_id")
	CrawlRecords = counterVec("avxxx_crawl_records_total",
		"Catalog records or feed items saved by crawl source.", "source_id")
	CrawlBlocked = counterVec("avxxx_crawl_blocked_total",
		"Crawl runs that ended with the source blocked (429/403, Retry-After) by crawl source.", "source_id")
	CrawlBudgetExhausted = counterVec("avxxx_crawl_budget_exhausted_total",
		"Crawl runs skipped because the daily request budget was exhausted.", "source_id")

	Events = counterVec("avxxx_events_total",
		"Analytics events received by POST /api/events by outcome (accepted|duplicate|quarantined).", "outcome")

	NotifierDeliveries = counterVec("avxxx_notifier_deliveries_total",
		"Outbox rows finished by channel and status (sent|pending|failed|cancelled|lease_lost|error).", "channel", "status")
	NotifierDuration = histogramVec("avxxx_notifier_delivery_duration_seconds",
		"Outbox delivery latency by channel.", LatencyBuckets, "channel")
	NotifierPushSends = counterVec("avxxx_notifier_push_sends_total",
		"Web Push sends per subscription by outcome (delivered|gone|retry|rejected).", "outcome")
)

func init() {
	Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// StatusClass maps 404 to "4xx".
func StatusClass(code int) string {
	if code < 100 || code > 599 {
		return "other"
	}
	return string(rune('0'+code/100)) + "xx"
}

// ObserveJob records one finished job.
func ObserveJob(kind string, d time.Duration, err error) {
	result := "ok"
	if err != nil {
		result = "error"
	}
	JobDuration.WithLabelValues(kind, result).Observe(d.Seconds())
}

// MetricsAddr reads METRICS_ADDR: unset uses def, "off" (or "none", "-")
// disables the listener ("" return).
func MetricsAddr(def string) string {
	v := strings.TrimSpace(os.Getenv("METRICS_ADDR"))
	switch strings.ToLower(v) {
	case "":
		return def
	case "off", "none", "-":
		return ""
	}
	return v
}

// MetricsHandler serves Registry in the Prometheus text format.
func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
}

// ServeMetrics serves /metrics on addr (an internal address: 127.0.0.1 or
// the compose network, never published) until ctx is done. It returns once
// the listener is bound, so a port clash fails fast; an empty addr is a
// no-op.
func ServeMetrics(ctx context.Context, addr string) error {
	if addr == "" {
		slog.Info("metrics listener disabled")
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler())
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics listener stopped", "err", err)
		}
	}()
	slog.Info("metrics listening", "listen", ln.Addr().String())
	return nil
}
