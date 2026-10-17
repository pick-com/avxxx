package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxDocumentBytes = 10 << 20
	// defaultUserAgent is used only when a Fetcher has no UserAgent (tests,
	// FetchDocument). Production crawls set UserAgentFromEnv's contact UA.
	defaultUserAgent = RobotsToken + "/" + crawlerVersion
	// DefaultBlockBackoff is used when a source signals a block without a
	// usable Retry-After header.
	DefaultBlockBackoff = time.Hour
	// MaxBlockBackoff caps hostile or malformed Retry-After values.
	MaxBlockBackoff = 7 * 24 * time.Hour
)

// ErrBudgetExhausted is returned by Fetcher.Fetch when the source's page or
// byte budget for this run has been spent. It is a clean stop, not a failure.
var ErrBudgetExhausted = errors.New("catalog: source request budget exhausted")

// BlockedError reports that a source refused automated access (HTTP 403/429,
// a 503 challenge, or a challenge page). The source's run must stop; other
// sources are unaffected.
type BlockedError struct {
	URL        string
	Status     int
	Reason     string
	RetryAfter time.Duration
}

func (e *BlockedError) Error() string {
	return fmt.Sprintf("source blocked (%s, HTTP %d) at %s; retry after %s", e.Reason, e.Status, e.URL, e.RetryAfter)
}

// IsBlocked reports whether err (or a wrapped error) is a BlockedError.
func IsBlocked(err error) (*BlockedError, bool) {
	var b *BlockedError
	if errors.As(err, &b) {
		return b, true
	}
	return nil, false
}

// FetchPolicy is the per-run request budget and rate limit for one source.
// Zero values mean unlimited / no delay.
type FetchPolicy struct {
	MinInterval time.Duration
	MaxPages    int64
	MaxBytes    int64
	// MaxRetryWait is the longest Retry-After the fetcher will sleep through
	// inline for a 429 before giving up and reporting the source as blocked.
	MaxRetryWait time.Duration
}

// Fetcher is the only path catalog crawls use to reach the network. It
// enforces the source policy (spacing, page/byte budget) and detects block
// signals so adapters never have to.
type Fetcher struct {
	Client *http.Client
	Policy FetchPolicy
	// UserAgent is sent with every request ("" = defaultUserAgent).
	UserAgent string
	// Robots, when set, is consulted before every request: disallowed URLs
	// are refused with a RobotsError and Crawl-delay raises MinInterval.
	// Automatic marks an automatic (baseline/incremental) crawl, for which an
	// unreachable robots.txt counts as a disallow; manual verify jobs proceed.
	Robots    RobotsChecker
	Automatic bool

	now   func() time.Time
	sleep func(context.Context, time.Duration) error

	mu      sync.Mutex
	last    time.Time
	pages   int64
	bytes   int64
	blocked *BlockedError
}

func NewFetcher(client *http.Client, policy FetchPolicy) *Fetcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Fetcher{Client: client, Policy: policy, now: time.Now, sleep: sleepCtx}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Usage returns the pages and bytes consumed by this fetcher so far.
func (f *Fetcher) Usage() (pages, bytes int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pages, f.bytes
}

// Fetch performs one budgeted, rate-limited GET.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.blocked != nil {
		return Document{}, f.blocked
	}
	interval := f.Policy.MinInterval
	if f.Robots != nil {
		v := f.Robots.Check(ctx, rawURL)
		if v.Challenge {
			// The host challenges this egress before we can even read its
			// rules: a block, for verify runs too. Never attempt the page.
			f.blocked = &BlockedError{URL: robotsURL(rawURL), Status: v.ChallengeStatus, Reason: "challenge", RetryAfter: DefaultBlockBackoff}
			return Document{}, f.blocked
		}
		if v.Unavailable && f.Automatic {
			return Document{}, &RobotsError{URL: rawURL, Reason: v.Reason + "; automatic crawls treat an unavailable robots.txt as disallow"}
		}
		if !v.Unavailable && !v.Allowed {
			return Document{}, &RobotsError{URL: rawURL, Reason: v.Reason}
		}
		if v.CrawlDelay > interval {
			interval = v.CrawlDelay // Crawl-delay is a lower bound on spacing
		}
	}
	retried := false
	for {
		if err := f.reserve(ctx, interval); err != nil {
			return Document{}, err
		}
		doc, resp, err := f.do(ctx, rawURL)
		if err != nil {
			return doc, err
		}
		if b := detectBlock(rawURL, resp, doc.Body, f.now()); b != nil {
			if b.Status == http.StatusTooManyRequests && !retried && f.Policy.MaxRetryWait > 0 && b.RetryAfter <= f.Policy.MaxRetryWait {
				retried = true
				if err := f.sleep(ctx, b.RetryAfter); err != nil {
					return Document{}, err
				}
				// The wait already spaces the retry; don't stack MinInterval on top.
				f.last = f.now().Add(-interval)
				continue
			}
			f.blocked = b
			return doc, b
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return doc, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
		}
		return doc, nil
	}
}

// reserve checks the budget and waits for the minimum request interval.
func (f *Fetcher) reserve(ctx context.Context, interval time.Duration) error {
	if f.Policy.MaxPages > 0 && f.pages >= f.Policy.MaxPages {
		return ErrBudgetExhausted
	}
	if f.Policy.MaxBytes > 0 && f.bytes >= f.Policy.MaxBytes {
		return ErrBudgetExhausted
	}
	if !f.last.IsZero() && interval > 0 {
		if wait := f.last.Add(interval).Sub(f.now()); wait > 0 {
			if err := f.sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (f *Fetcher) do(ctx context.Context, rawURL string) (Document, *http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Document{}, nil, err
	}
	ua := f.UserAgent
	if ua == "" {
		ua = defaultUserAgent
	}
	req.Header.Set("User-Agent", ua)
	f.last = f.now()
	f.pages++
	resp, err := f.Client.Do(req)
	if err != nil {
		return Document{}, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	f.bytes += int64(len(body))
	if err != nil {
		return Document{}, nil, err
	}
	if len(body) > maxDocumentBytes {
		return Document{}, nil, fmt.Errorf("document exceeds 10 MiB")
	}
	doc := Document{URL: rawURL, FinalURL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}
	return doc, resp, nil
}

// challengeMarkers are deliberately specific: generic words like "captcha"
// appear in ordinary comment forms and must not trip a block.
var challengeMarkers = [][]byte{
	[]byte("/cdn-cgi/challenge-platform/"),
	[]byte("cf_chl_opt"),
	[]byte(`id="challenge-form"`),
	[]byte("<title>Just a moment...</title>"),
	[]byte("Attention Required! | Cloudflare"),
	[]byte("DDoS-Guard</title>"),
}

func isChallenge(resp *http.Response, body []byte) bool {
	if strings.EqualFold(resp.Header.Get("cf-mitigated"), "challenge") {
		return true
	}
	for _, m := range challengeMarkers {
		if bytes.Contains(body, m) {
			return true
		}
	}
	return false
}

func detectBlock(rawURL string, resp *http.Response, body []byte, now time.Time) *BlockedError {
	retry, hasRetry := parseRetryAfter(resp.Header.Get("Retry-After"), now)
	b := &BlockedError{URL: rawURL, Status: resp.StatusCode, RetryAfter: retry}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		b.Reason = "rate_limited"
	case resp.StatusCode == http.StatusForbidden:
		b.Reason = "forbidden"
		if isChallenge(resp, body) {
			// Cloudflare managed challenges answer 403: name the cause so an
			// operator knows a different egress or the site's permission,
			// not a retry, is what unblocks it.
			b.Reason = "challenge"
		}
	case resp.StatusCode == http.StatusServiceUnavailable && (hasRetry || isChallenge(resp, body)):
		// A bare 503 is usually an outage; only treat it as a block when the
		// server asks us to back off or serves a challenge.
		b.Reason = "unavailable"
		if isChallenge(resp, body) {
			b.Reason = "challenge"
		}
	case resp.StatusCode >= 200 && resp.StatusCode < 300 && isChallenge(resp, body):
		b.Reason = "challenge"
	default:
		return nil
	}
	if !hasRetry {
		b.RetryAfter = DefaultBlockBackoff
	}
	return b
}

// parseRetryAfter accepts delta-seconds or an HTTP-date (RFC 9110 §10.2.3).
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		if n < 0 {
			return 0, false
		}
		if n > int64(MaxBlockBackoff/time.Second) {
			return MaxBlockBackoff, true
		}
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
		if d < 0 {
			d = 0
		}
	} else {
		return 0, false
	}
	if d > MaxBlockBackoff {
		d = MaxBlockBackoff
	}
	return d, true
}

// robotsURL is the robots.txt URL of rawURL's origin.
func robotsURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return u.Scheme + "://" + u.Host + "/robots.txt"
}

// FetchDocument performs a single unbudgeted fetch. Catalog crawls should use
// a Fetcher built from the source policy instead.
func FetchDocument(ctx context.Context, client *http.Client, rawURL string) (Document, error) {
	return NewFetcher(client, FetchPolicy{}).Fetch(ctx, rawURL)
}
