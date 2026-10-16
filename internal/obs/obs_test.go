package obs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// logLines decodes JSON log output into one map per line.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("not JSON: %q: %v", sc.Text(), err)
		}
		out = append(out, m)
	}
	return out
}

func accessLine(t *testing.T, lines []map[string]any) map[string]any {
	t.Helper()
	for _, l := range lines {
		if l["msg"] == "http_request" {
			return l
		}
	}
	t.Fatalf("no http_request line in %v", lines)
	return nil
}

func TestMiddlewareFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "api", LogConfig{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/releases/{slug}", func(w http.ResponseWriter, r *http.Request) {
		if RequestID(r.Context()) == "" {
			t.Error("request id missing from context")
		}
		io.WriteString(w, "hello")
	})
	h := Middleware(mux, logger)

	req := httptest.NewRequest("GET", "/api/releases/abc-123?lang=en", nil)
	req.RemoteAddr = "203.0.113.77:51234"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	id := rec.Header().Get(RequestIDHeader)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
		t.Fatalf("generated request id = %q", id)
	}
	l := accessLine(t, logLines(t, &buf))
	want := map[string]any{"service": "api", "request_id": id, "method": "GET", "route": "/api/releases/{slug}",
		"status": float64(200), "bytes": float64(5), "client_ip": "203.0.113.0/24", "level": "INFO"}
	for k, v := range want {
		if l[k] != v {
			t.Errorf("%s = %v, want %v", k, l[k], v)
		}
	}
	if _, ok := l["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms missing: %v", l)
	}
	if strings.Contains(buf.String(), "abc-123") || strings.Contains(buf.String(), "lang=en") {
		t.Errorf("raw path or query logged: %s", buf.String())
	}

	// A plain client id is echoed back; a hostile one is replaced.
	buf.Reset()
	req = httptest.NewRequest("GET", "/api/releases/x", nil)
	req.Header.Set(RequestIDHeader, "client-req-0001")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); got != "client-req-0001" {
		t.Errorf("echoed id = %q", got)
	}
	req = httptest.NewRequest("GET", "/nope", nil)
	req.Header.Set(RequestIDHeader, "bad id\n{\"level\":\"ERROR\"}")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); strings.Contains(got, " ") || len(got) != 32 {
		t.Errorf("hostile id kept: %q", got)
	}
	l = accessLine(t, logLines(t, &buf)[1:])
	if l["route"] != "unmatched" || l["status"] != float64(404) {
		t.Errorf("unmatched request logged as %v", l)
	}
}

// TestMiddlewareRedaction: a request carrying an Authorization header, a
// cookie, a token query parameter and an email produces no secret in the log
// output, whether the handler logs through slog, through log.Printf or by
// returning an error.
func TestMiddlewareRedaction(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "api", LogConfig{Level: "debug"})
	prev := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(prev)

	const (
		bearer = "s3cr3t-bearer-token-abc"
		query  = "qs-secret-xyz-789"
		cookie = "sess-cookie-value-456"
		email  = "alice@example.com"
		push   = "https://fcm.googleapis.com/fcm/send/dEvIcE-CaPaBiLiTy"
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/me/push", func(w http.ResponseWriter, r *http.Request) {
		L(r.Context()).Info("handler debug",
			"authorization", r.Header.Get("Authorization"),
			"cookie", r.Header.Get("Cookie"),
			"url", r.URL.String(),
			"email", email,
			"endpoint", push,
			"ip", r.RemoteAddr)
		log.Printf("legacy %s %v %s %s %s", r.URL.String(), r.Header, email, push, r.RemoteAddr)
		RecordError(w, errors.New("user "+email+" sent Bearer "+bearer))
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	req := httptest.NewRequest("POST", "/api/me/push?token="+query+"&lang=en", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Cookie", "session="+cookie)
	req.RemoteAddr = "198.51.100.23:4444"
	Middleware(mux, logger).ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, secret := range []string{bearer, query, cookie, email, "dEvIcE-CaPaBiLiTy", "198.51.100.23"} {
		if strings.Contains(out, secret) {
			t.Errorf("log output contains %q:\n%s", secret, out)
		}
	}
	l := accessLine(t, logLines(t, &buf))
	if l["level"] != "ERROR" || l["status"] != float64(500) || l["err"] == nil {
		t.Errorf("5xx access line = %v", l)
	}
	if !strings.Contains(out, "198.51.100.0/24") {
		t.Errorf("client network prefix missing:\n%s", out)
	}
}

func TestScrubString(t *testing.T) {
	cases := map[string]string{
		"GET /x?token=abc&lang=en": "GET /x?token=[REDACTED]&lang=en",
		"GET https://api.dmm.com/affiliate/v3/ItemList?api_id=K1&affiliate_id=a-990&hits=1": "GET https://api.dmm.com/affiliate/v3/ItemList?api_id=[REDACTED]&affiliate_id=[REDACTED]&hits=1",
		"Authorization: Bearer abc.def":                                  "Authorization: Bearer [REDACTED]",
		"mail bob.smith+x@mail.example.org now":                          "mail [EMAIL] now",
		"from 203.0.113.9 via 10.0.0.5":                                  "from 203.0.113.0/24 via 10.0.0.5",
		"listen 127.0.0.1:9090":                                          "listen 127.0.0.1:9090",
		"push https://updates.push.services.mozilla.com/wpush/v2/gAAA x": "push https://updates.push.services.mozilla.com/[REDACTED] x",
	}
	for in, want := range cases {
		if got := ScrubString(in); got != want {
			t.Errorf("ScrubString(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"203.0.113.9:443": "203.0.113.0/24", "[2001:db8:1:2::1]:80": "2001:db8:1::/48",
		"203.0.113.9, 10.0.0.1": "203.0.113.0/24", "nonsense": Redacted,
	} {
		if got := IPPrefix(in); got != want {
			t.Errorf("IPPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextFormatAndLevel(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf, "scraper", LogConfig{Format: "text", Level: "warn"})
	l.Info("hidden")
	l.Warn("shown", "job_id", 7, "password", "hunter2")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "job_id=7") || strings.Contains(out, "hunter2") {
		t.Errorf("text output = %q", out)
	}
}

func TestHashID(t *testing.T) {
	a, b := HashID("user", 42), HashID("user", 43)
	if len(a) != 12 || a == b || a != HashID("user", 42) || strings.Contains(a, "42") && len(a) < 3 {
		t.Errorf("HashID = %q, %q", a, b)
	}
}

// ---------------------------------------------------------------- metrics

// Label names allowed on avxxx metrics: all bounded by code or by the
// admin-provisioned crawl source table. Anything else (slug, user id, path,
// release id, ip ...) would be unbounded.
var allowedLabels = map[string]bool{
	"method": true, "route": true, "code": true, "status_class": true, "kind": true, "result": true,
	"op": true, "source_id": true, "outcome": true, "channel": true, "status": true, "state": true,
	"rule": true, "severity": true, "band": true,
}

func TestMetricLabelsBounded(t *testing.T) {
	cat := Catalog()
	if len(cat) < 30 {
		t.Fatalf("catalog has %d metrics", len(cat))
	}
	seen := map[string]bool{}
	for _, m := range cat {
		if !strings.HasPrefix(m.Name, "avxxx_") {
			t.Errorf("%s: missing avxxx_ prefix", m.Name)
		}
		if seen[m.Name] {
			t.Errorf("%s registered twice", m.Name)
		}
		seen[m.Name] = true
		for _, l := range m.Labels {
			if !allowedLabels[l] {
				t.Errorf("%s: label %q is not in the bounded allowlist", m.Name, l)
			}
		}
	}
}

func TestHTTPMetricsUseRoutePattern(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/performers/{slug}", func(w http.ResponseWriter, r *http.Request) {})
	h := Middleware(mux, NewLogger(io.Discard, "", LogConfig{}))
	for _, p := range []string{"/api/performers/a", "/api/performers/b", "/random/1", "/random/2"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", p, nil))
	}
	if got := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "/api/performers/{slug}", "200")); got != 2 {
		t.Errorf("pattern counter = %v", got)
	}
	if got := testutil.ToFloat64(HTTPRequests.WithLabelValues("GET", "unmatched", "404")); got < 2 {
		t.Errorf("unmatched counter = %v", got)
	}
	mfs, err := Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if v := lp.GetValue(); strings.Contains(v, "/random") || strings.HasSuffix(v, "/a") || strings.HasSuffix(v, "/b") {
					t.Errorf("%s: raw path %q leaked into label %s", mf.GetName(), v, lp.GetName())
				}
			}
		}
	}
	problems, err := testutil.GatherAndLint(Registry)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if strings.HasPrefix(p.Metric, "avxxx_") {
			t.Errorf("lint %s: %s", p.Metric, p.Text)
		}
	}
}

type fakeDB struct {
	snaps, pairs int
}

func (f *fakeDB) MetricsSnapshot(context.Context) (DBSnapshot, error) {
	f.snaps++
	score := 91.5
	now := time.Unix(1_700_000_000, 0)
	return DBSnapshot{
		Jobs:             []JobCount{{"crawl", "due", 4}, {"audit", "running", 1}},
		ExpiredLeases:    map[string]int64{"audit": 1},
		OldestDueAge:     map[string]float64{"crawl": 120},
		FailedLastHour:   map[string]int64{"crawl": 2},
		CheckpointLastOK: map[int64]time.Time{3: now},
		BlockedSources:   1,
		OpenIssues:       []IssueCount{{"R-AGE", "blocking", 2}},
		Holds:            map[string]int64{"R-AGE": 5},
		PlaybackSources:  []BandCount{{"excellent", "active", 10}},
		Health:           HealthPool{Score: &score, Attempts: 200, Playable: 190, LastRunAt: &now},
	}, nil
}

func (f *fakeDB) PlayPairing(context.Context, time.Time, time.Time) (Pairing, error) {
	f.pairs++
	return Pairing{Requests: 200, Paired: 180}, nil
}

func TestDBCollector(t *testing.T) {
	f := &fakeDB{}
	c := newDBCollector(f)
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }
	want := `
# HELP avxxx_play_pairing_ratio Share of play_request play_ids with a play_end (PRD 14.5), 24 h window ending 1 h ago.
# TYPE avxxx_play_pairing_ratio gauge
avxxx_play_pairing_ratio 0.9
# HELP avxxx_audit_holds_total Releases held by the audit (review_events 'hold') by rule; R-AGE pages someone.
# TYPE avxxx_audit_holds_total counter
avxxx_audit_holds_total{rule="R-AGE"} 5
# HELP avxxx_jobs_queued Jobs by kind and state (due = pending and available, scheduled = pending in backoff, running).
# TYPE avxxx_jobs_queued gauge
avxxx_jobs_queued{kind="audit",state="running"} 1
avxxx_jobs_queued{kind="crawl",state="due"} 4
# HELP avxxx_source_health_playable_ratio Playable attempts / attempts over the latest 24 h source health runs.
# TYPE avxxx_source_health_playable_ratio gauge
avxxx_source_health_playable_ratio 0.95
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(want), "avxxx_play_pairing_ratio", "avxxx_audit_holds_total",
		"avxxx_jobs_queued", "avxxx_source_health_playable_ratio"); err != nil {
		t.Fatal(err)
	}
	testutil.CollectAndCount(c)
	if f.snaps != 1 || f.pairs != 1 {
		t.Errorf("cache: snaps=%d pairs=%d, want 1/1 within the TTL", f.snaps, f.pairs)
	}
	clock = clock.Add(time.Minute)
	testutil.CollectAndCount(c)
	if f.snaps != 2 || f.pairs != 1 {
		t.Errorf("after 1m: snaps=%d pairs=%d, want 2/1", f.snaps, f.pairs)
	}
}

// TestAlertRulesReferenceKnownMetrics keeps deploy/monitoring/alerts.yml in
// step with the metrics this package registers (promtool checks the syntax:
// scripts/check-alerts.sh).
func TestAlertRulesReferenceKnownMetrics(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/monitoring/alerts.yml")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, m := range Catalog() {
		known[m.Name] = true
	}
	// Metrics produced outside the Go binaries: scripts/backup-db.sh writes
	// avxxx_backup.prom for node_exporter's textfile collector. Each name must
	// still appear in that script, so a rename there fails this test too.
	backupScript, err := os.ReadFile("../../scripts/backup-db.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"avxxx_backup", "avxxx_backup_last_success_timestamp_seconds",
		"avxxx_backup_last_size_bytes", "avxxx_backup_last_duration_seconds"} {
		if !strings.Contains(string(backupScript), n) {
			t.Errorf("textfile metric %s is not written by scripts/backup-db.sh", n)
		}
		known[n] = true
	}
	names := regexp.MustCompile(`avxxx_[a-z0-9_]+`).FindAllString(string(raw), -1)
	if len(names) == 0 {
		t.Fatal("no avxxx metrics referenced")
	}
	alerts := regexp.MustCompile(`(?m)^\s+- alert: (\w+)`).FindAllStringSubmatch(string(raw), -1)
	if len(alerts) < 15 {
		t.Errorf("only %d alerts", len(alerts))
	}
	for _, n := range names {
		base := n
		for _, suf := range []string{"_bucket", "_count", "_sum"} {
			base = strings.TrimSuffix(base, suf)
		}
		if !known[base] {
			t.Errorf("alerts.yml references unknown metric %s", n)
		}
	}
	for _, want := range []string{"ApiHealthProbeFailing", "JobsPendingGrowing", "JobsExpiredLeases", "CrawlCheckpointStale",
		"JobsFailed", "ApiHigh5xxRate", "ReleaseDetailP99High", "RecsP99High", "SourceHealthBelowAlphaGate",
		"EventsQuarantineRateHigh", "PlayPairingLow", "NotifierFailures", "RAgeHold"} {
		if !strings.Contains(string(raw), "alert: "+want+"\n") {
			t.Errorf("alert %s missing", want)
		}
	}
}
