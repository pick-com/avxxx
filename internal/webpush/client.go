package webpush

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Subscription is one browser push subscription.
type Subscription struct {
	Endpoint string
	Keys     Keys
}

// Outcome classifies a push service response (RFC 8030 section 5, 8.3).
type Outcome int

const (
	// Delivered: 2xx, the push service accepted the message.
	Delivered Outcome = iota
	// Gone: 404/410, the subscription expired or was revoked; delete it.
	Gone
	// Retry: 429, 5xx or a transport error; try again later.
	Retry
	// Rejected: any other 4xx (bad VAPID, payload too large, ...); retrying
	// the same request will not help.
	Rejected
)

func (o Outcome) String() string {
	return [...]string{"delivered", "gone", "retry", "rejected"}[o]
}

// Result is the outcome of one Send.
type Result struct {
	Outcome    Outcome
	Status     int           // HTTP status, 0 on transport errors
	RetryAfter time.Duration // from Retry-After, when present
}

// Client sends encrypted notifications with VAPID authentication.
type Client struct {
	VAPID *VAPID
	HTTP  *http.Client
	// TTL is how long the push service may hold an undelivered message.
	TTL time.Duration
	// Urgency is the RFC 8030 Urgency header ("very-low", "low", "normal", "high").
	Urgency string
	Now     func() time.Time
}

// maxErrBody bounds how much of an error response is read (and discarded).
const maxErrBody = 4 << 10

// Send encrypts payload for sub and POSTs it to the push service. err is only
// set for problems on our side (bad keys, payload too large) or transport
// failures; HTTP-level failures are reported through Result.
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte) (Result, error) {
	body, err := Encrypt(payload, sub.Keys)
	if err != nil {
		return Result{Outcome: Rejected}, err
	}
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	// RFC 8292: exp no more than 24 hours ahead.
	auth, err := c.VAPID.Authorization(sub.Endpoint, now().Add(12*time.Hour))
	if err != nil {
		return Result{Outcome: Rejected}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{Outcome: Rejected}, err
	}
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl/time.Second)))
	if c.Urgency != "" {
		req.Header.Set("Urgency", c.Urgency)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the url.Error text repeats the full endpoint
		}
		return Result{Outcome: Retry}, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrBody))
	return Classify(resp.StatusCode, resp.Header.Get("Retry-After"), now()), nil
}

// Classify maps a push service status code to an Outcome.
func Classify(status int, retryAfter string, now time.Time) Result {
	r := Result{Status: status, RetryAfter: parseRetryAfter(retryAfter, now)}
	switch {
	case status >= 200 && status < 300:
		r.Outcome = Delivered
	case status == http.StatusNotFound || status == http.StatusGone:
		r.Outcome = Gone
	case status == http.StatusTooManyRequests || status >= 500 || status == http.StatusRequestTimeout:
		r.Outcome = Retry
	default:
		r.Outcome = Rejected
	}
	return r
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// EndpointHost returns only the host of an endpoint, for logs: the full
// endpoint is a bearer capability and must not be logged.
func EndpointHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return "invalid-endpoint"
}
