package images

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"avxxx/internal/objstore"
)

// Candidate is one entity whose source evidence names an image and which
// has no visible stored image of AssetType yet.
type Candidate struct {
	EntityType     string
	EntityID       int64
	AssetType      string
	SourceID       int64
	SourceRecordID int64
	// Evidence holds the record's image fields (cover_url, cover,
	// image_urls) from source_records.raw_data.
	Evidence map[string]any
	Attempts int
}

// StoredImage is what SaveImage records: the original row and its derived
// rows, all pointing at objects already uploaded.
type StoredImage struct {
	Candidate Candidate
	SourceURL string
	Processed Processed
	Keys      map[string]string // variant -> object key
}

// FetchStore is everything the media_fetch job needs from the database.
type FetchStore interface {
	// ImageSources lists every source with its image policy fields.
	ImageSources(ctx context.Context) ([]SourcePolicy, error)
	// ImageCandidates selects up to limit public, unmerged entities with
	// evidence from one of sourceIDs that have no visible original of their
	// stored asset type, no media_blocks row, no approved remove_photo
	// request (performers) and no fetch attempt due after now; one row per
	// entity, newest entities first.
	ImageCandidates(ctx context.Context, sourceIDs []int64, now time.Time, limit int) ([]Candidate, error)
	// ObjectKeyStored reports whether a media_assets row already owns key.
	ObjectKeyStored(ctx context.Context, key string) (bool, error)
	// SaveImage inserts the asset rows and marks the attempt stored.
	SaveImage(ctx context.Context, img StoredImage) error
	// RecordFetchAttempt upserts the attempt row for c.
	RecordFetchAttempt(ctx context.Context, c Candidate, sourceURL, status, errText string, next time.Time) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// FetchConfig is the job's politeness budget.
type FetchConfig struct {
	MinInterval time.Duration // between requests to one source (IMAGE_FETCH_MIN_INTERVAL, >= 2s)
	MaxPerRun   int           // downloads per run (IMAGE_FETCH_MAX_PER_RUN)
	BatchSize   int
	Limits      Limits
}

// Fetch job defaults.
const (
	DefaultFetchMinInterval = 2 * time.Second
	DefaultFetchMaxPerRun   = 200
	DefaultFetchBatch       = 50
)

// FetchConfigFromEnv reads IMAGE_FETCH_MIN_INTERVAL, IMAGE_FETCH_MAX_PER_RUN,
// IMAGE_MAX_BYTES and IMAGE_MAX_DIMENSION; invalid values keep the defaults.
func FetchConfigFromEnv(getenv func(string) string) (FetchConfig, []string) {
	c := FetchConfig{MinInterval: DefaultFetchMinInterval, MaxPerRun: DefaultFetchMaxPerRun, BatchSize: DefaultFetchBatch, Limits: DefaultLimits()}
	var warn []string
	if v := strings.TrimSpace(getenv("IMAGE_FETCH_MIN_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err != nil || d < DefaultFetchMinInterval {
			warn = append(warn, fmt.Sprintf("IMAGE_FETCH_MIN_INTERVAL %q invalid or below %s; using %s", v, DefaultFetchMinInterval, c.MinInterval))
		} else {
			c.MinInterval = d
		}
	}
	posInt := func(name string, dst *int) {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			if n, err := strconv.Atoi(v); err != nil || n <= 0 {
				warn = append(warn, fmt.Sprintf("invalid %s %q; using %d", name, v, *dst))
			} else {
				*dst = n
			}
		}
	}
	posInt("IMAGE_FETCH_MAX_PER_RUN", &c.MaxPerRun)
	posInt("IMAGE_MAX_DIMENSION", &c.Limits.MaxDimension)
	maxBytes := int(c.Limits.MaxBytes)
	posInt("IMAGE_MAX_BYTES", &maxBytes)
	// The catalog Fetcher refuses bodies above 10 MiB anyway.
	c.Limits.MaxBytes = int64(min(maxBytes, 10<<20))
	return c, warn
}

// EvidenceURL picks the image URL from a source record's fields: a
// release's cover_url / cover, else the largest image_urls entry; a
// performer's image_urls large / small. Only absolute http(s) URLs without
// credentials qualify (the runner also refuses internal hosts).
func EvidenceURL(entityType string, f map[string]any) string {
	var order []string
	if entityType == EntityRelease {
		for _, k := range []string{"cover_url", "cover"} {
			if s, ok := f[k].(string); ok && validImageURL(s) {
				return s
			}
		}
		order = []string{"large", "small", "list"}
	} else {
		order = []string{"large", "small"}
	}
	m, _ := f["image_urls"].(map[string]any)
	for _, k := range order {
		if s, ok := m[k].(string); ok && validImageURL(s) {
			return s
		}
	}
	return ""
}

func validImageURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil
}

// PublicURL reports whether raw is an http(s) URL whose host is not a
// literal internal address or a local name.
func PublicURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && validImageURL(raw) && !blockedHost(u.Hostname())
}

// blockedHost refuses literal internal addresses in evidence URLs; the
// default client's dialer re-checks every resolved address (PublicClient).
func blockedHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
		return true
	}
	if ip := net.ParseIP(h); ip != nil {
		return !publicIP(ip)
	}
	return false
}

func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast())
}

// PublicClient is an HTTP client whose dialer refuses non-public addresses,
// so a source cannot point the image job at internal services.
func PublicClient(timeout time.Duration) *http.Client {
	d := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
			return fmt.Errorf("refusing non-public address %s", host)
		}
		return nil
	}}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = d.DialContext
	tr.Proxy = nil
	return &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("too many redirects")
		}
		if !PublicURL(req.URL.String()) {
			return errors.New("redirect to a refused URL")
		}
		return nil
	}}
}

// Backoff for failed attempts.
const (
	firstRetry   = 24 * time.Hour
	maxRetry     = 30 * 24 * time.Hour
	robotsRetry  = 7 * 24 * time.Hour
	rejectRetry  = 30 * 24 * time.Hour
	attemptError = 500 // stored error text cap
)

// NextTry is the retry time after attempts failed attempts (1, 2, 4 ... days, capped at 30).
func NextTry(now time.Time, attempts int) time.Time {
	d := firstRetry
	for i := 1; i < attempts && d < maxRetry; i++ {
		d *= 2
	}
	return now.Add(min(d, maxRetry))
}

// FetchRunner executes the media_fetch job.
type FetchRunner struct {
	Store   FetchStore
	Objects objstore.Storage
	Config  FetchConfig
	// UserAgent is the crawler UA with contact (catalog.UserAgentFromEnv).
	UserAgent string
	// Client defaults to PublicClient; Robots defaults to a cache on it.
	Client *http.Client
	Robots catalog.RobotsChecker
	JobID  int64
	Now    func() time.Time
	// NewFetcher overrides the per-source fetcher (tests).
	NewFetcher func(src SourcePolicy) *catalog.Fetcher
	// AllowPrivateHosts lets evidence URLs name internal hosts (tests with
	// httptest servers only; production keeps the SSRF guard).
	AllowPrivateHosts bool
}

// SourceSkip names a source the run did not download from.
type SourceSkip struct {
	SourceID int64  `json:"source_id"`
	Reason   string `json:"reason"`
}

// FetchReport summarizes one run (also nightly_runs.detail).
type FetchReport struct {
	Allowed  []int64      `json:"allowed_sources"`
	Skipped  []SourceSkip `json:"skipped_sources,omitempty"`
	Checked  int          `json:"checked"`
	Stored   int          `json:"stored"`
	Objects  int          `json:"objects_written"`
	Deduped  int          `json:"objects_deduped"`
	Rejected int          `json:"rejected"`
	Failed   int          `json:"failed"`
	Blocked  []SourceSkip `json:"blocked_sources,omitempty"`
	Requests int64        `json:"requests"`
	Stopped  string       `json:"stopped,omitempty"`
}

func (r *FetchRunner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run downloads images for due candidates from image-permitted sources.
func (r *FetchRunner) Run(ctx context.Context) (rep FetchReport, err error) {
	start := r.now()
	defer func() { r.record(ctx, start, rep, err) }()
	if r.Objects == nil {
		rep.Stopped = "storage_not_configured"
		return rep, nil
	}
	if r.UserAgent == "" {
		rep.Stopped = "user_agent_not_configured"
		return rep, nil
	}
	cfg := r.Config
	if cfg.MinInterval < DefaultFetchMinInterval {
		cfg.MinInterval = DefaultFetchMinInterval
	}
	if cfg.MaxPerRun <= 0 {
		cfg.MaxPerRun = DefaultFetchMaxPerRun
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultFetchBatch
	}
	if cfg.Limits.MaxBytes == 0 {
		cfg.Limits = DefaultLimits()
	}
	srcs, err := r.Store.ImageSources(ctx)
	if err != nil {
		return rep, fmt.Errorf("image sources: %w", err)
	}
	policy := map[int64]SourcePolicy{}
	for _, s := range srcs {
		if d := SourceAllowed(s); d.Allowed {
			policy[s.ID] = s
			rep.Allowed = append(rep.Allowed, s.ID)
		} else if s.ImagesAllowed {
			// Flag set but the license no longer qualifies: say why.
			rep.Skipped = append(rep.Skipped, SourceSkip{SourceID: s.ID, Reason: d.Reason})
		}
	}
	if len(rep.Allowed) == 0 {
		rep.Stopped = "no_image_sources"
		return rep, nil
	}
	client := r.Client
	if client == nil {
		client = PublicClient(30 * time.Second)
	}
	robots := r.Robots
	if robots == nil {
		robots = catalog.NewRobots(client, r.UserAgent)
	}
	fetchers := map[int64]*catalog.Fetcher{}
	blocked := map[int64]bool{}
	fetcher := func(src SourcePolicy) *catalog.Fetcher {
		if f := fetchers[src.ID]; f != nil {
			return f
		}
		var f *catalog.Fetcher
		if r.NewFetcher != nil {
			f = r.NewFetcher(src)
		} else {
			f = catalog.NewFetcher(client, catalog.FetchPolicy{MinInterval: cfg.MinInterval, MaxPages: int64(cfg.MaxPerRun)})
			f.UserAgent, f.Robots, f.Automatic = r.UserAgent, robots, true
		}
		fetchers[src.ID] = f
		return f
	}
	defer func() {
		for _, f := range fetchers {
			p, _ := f.Usage()
			rep.Requests += p
		}
	}()
	allowedIDs := rep.Allowed
	for rep.Checked < cfg.MaxPerRun {
		batch, err := r.Store.ImageCandidates(ctx, allowedIDs, r.now(), min(cfg.BatchSize, cfg.MaxPerRun-rep.Checked))
		if err != nil {
			return rep, fmt.Errorf("select candidates: %w", err)
		}
		if len(batch) == 0 {
			return rep, nil
		}
		progressed := false
		for _, c := range batch {
			src, ok := policy[c.SourceID]
			if !ok || blocked[c.SourceID] || !AssetAllowed(c.EntityType, c.AssetType) {
				continue // defense in depth: the query is already restricted
			}
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			progressed = true
			rep.Checked++
			if err := r.fetchOne(ctx, fetcher(src), c, cfg.Limits, &rep); err != nil {
				var b *catalog.BlockedError
				if errors.As(err, &b) || errors.Is(err, catalog.ErrBudgetExhausted) {
					blocked[c.SourceID] = true
					rep.Blocked = append(rep.Blocked, SourceSkip{SourceID: c.SourceID, Reason: err.Error()})
					continue
				}
				return rep, err
			}
			if rep.Checked >= cfg.MaxPerRun {
				break
			}
		}
		if !progressed {
			return rep, nil // every remaining candidate's source is blocked
		}
		remaining := allowedIDs[:0:0]
		for _, id := range allowedIDs {
			if !blocked[id] {
				remaining = append(remaining, id)
			}
		}
		if allowedIDs = remaining; len(allowedIDs) == 0 {
			return rep, nil
		}
	}
	rep.Stopped = "max_per_run"
	return rep, nil
}

// fetchOne downloads, validates and stores one candidate. Download and
// validation failures are recorded as attempts (nil error); a block or
// budget stop is returned so the source stops; storage and database
// failures fail the job.
func (r *FetchRunner) fetchOne(ctx context.Context, f *catalog.Fetcher, c Candidate, lim Limits, rep *FetchReport) error {
	now := r.now()
	src := EvidenceURL(c.EntityType, c.Evidence)
	fail := func(status string, cause error, next time.Time) error {
		msg := cause.Error()
		if len(msg) > attemptError {
			msg = msg[:attemptError]
		}
		return r.Store.RecordFetchAttempt(ctx, c, src, status, msg, next)
	}
	if src == "" {
		rep.Rejected++
		return fail("skipped", errors.New("no usable image URL in evidence"), now.Add(rejectRetry))
	}
	if !r.AllowPrivateHosts && !PublicURL(src) {
		rep.Rejected++
		return fail("skipped", errors.New("image URL names an internal host"), now.Add(rejectRetry))
	}
	doc, err := f.Fetch(ctx, src)
	if err != nil {
		if b, ok := catalog.IsBlocked(err); ok {
			rep.Failed++
			if ferr := fail("failed", err, now.Add(max(b.RetryAfter, firstRetry))); ferr != nil {
				return ferr
			}
			return err
		}
		if errors.Is(err, catalog.ErrBudgetExhausted) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rep.Failed++
		if _, ok := catalog.IsRobots(err); ok {
			return fail("skipped", err, now.Add(robotsRetry))
		}
		return fail("failed", err, NextTry(now, c.Attempts+1))
	}
	p, err := Process(doc.Body, doc.ContentType, lim)
	if err != nil {
		rep.Rejected++
		return fail("failed", err, now.Add(rejectRetry))
	}
	img := StoredImage{Candidate: c, SourceURL: src, Processed: p, Keys: map[string]string{}}
	img.Keys[VariantOriginal] = ObjectKey(c.EntityType, c.EntityID, c.AssetType, p.SourceSHA256, p.Original.Ext)
	for _, d := range p.Derived {
		img.Keys[d.Variant] = VariantKey(c.EntityType, c.EntityID, c.AssetType, p.SourceSHA256, d.Variant, d.Ext)
	}
	for _, e := range append([]Encoded{p.Original}, p.Derived...) {
		key := img.Keys[e.Variant]
		stored, err := r.Store.ObjectKeyStored(ctx, key)
		if err != nil {
			return err
		}
		if stored {
			rep.Deduped++
			continue
		}
		if err := r.Objects.Put(ctx, key, e.Body, e.MIME); err != nil {
			return fmt.Errorf("store %s: %w", key, err)
		}
		rep.Objects++
	}
	if err := r.Store.SaveImage(ctx, img); err != nil {
		return fmt.Errorf("save image rows: %w", err)
	}
	rep.Stored++
	return nil
}

func (r *FetchRunner) record(ctx context.Context, start time.Time, rep FetchReport, cause error) {
	if r.Store == nil {
		return
	}
	detail := map[string]any{"stored": rep.Stored, "objects_written": rep.Objects, "objects_deduped": rep.Deduped,
		"rejected": rep.Rejected, "failed": rep.Failed, "requests": rep.Requests, "allowed_sources": rep.Allowed}
	if rep.Stopped != "" {
		detail["stopped"] = rep.Stopped
	}
	if len(rep.Blocked) > 0 {
		detail["blocked_sources"] = rep.Blocked
	}
	run := audit.Run{Job: JobKindFetch, JobID: r.JobID, StartedAt: start, DurationMS: r.now().Sub(start).Milliseconds(),
		Checked: rep.Checked, Fixed: rep.Stored, Issues: rep.Rejected + rep.Failed, Result: audit.ResultOK, Detail: detail}
	switch {
	case cause != nil:
		run.Result = audit.ResultFailed
		detail["error"] = cause.Error()
	case rep.Rejected+rep.Failed > 0 || len(rep.Blocked) > 0:
		run.Result = audit.ResultIssues
	}
	if err := r.Store.RecordRun(context.WithoutCancel(ctx), run); err != nil {
		slog.Warn("media_fetch: record run", "err", err)
	}
}
