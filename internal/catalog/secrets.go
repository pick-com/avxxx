package catalog

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrNotConfigured is returned (wrapped) by adapters that need deployment
// configuration, such as API credentials, which is missing. A worker that
// sees it skips the run with a clear job error instead of crashing.
var ErrNotConfigured = errors.New("catalog: adapter not configured")

// Configurable is implemented by adapters that need configuration before
// they can run. Configured returns an error wrapping ErrNotConfigured when it
// is missing.
type Configurable interface {
	Configured() error
}

// TransportWrapper is implemented by adapters that authenticate each request
// at the transport layer. Keeping credentials out of the URLs the Fetcher,
// the crawl and the checkpoints see means they never reach logs, job errors,
// cursors or source records.
type TransportWrapper interface {
	WrapTransport(base http.RoundTripper) http.RoundTripper
}

// Redactor scrubs adapter secrets from a string (URL, error text, field).
type Redactor interface {
	Redact(s string) string
}

// InlineDiscoverer is implemented by adapters whose discovery documents
// already carry complete records (structured APIs). When InlineRecords is
// true the crawl saves discovered records directly, without a detail fetch.
type InlineDiscoverer interface {
	InlineRecords() bool
}

// IncrementalStarter is implemented by adapters that can narrow the first
// discovery page of a run to what changed since the last completed pass.
// IncrementalStart returns the narrowed start URL, or "" to keep start.
type IncrementalStarter interface {
	IncrementalStart(start string, watermark time.Time) string
}

// HTTPClientFor returns the client a crawl for adapter a should use: base,
// with the adapter's authenticating transport when it has one. It returns an
// error wrapping ErrNotConfigured when the adapter is not configured.
func HTTPClientFor(a Adapter, base *http.Client) (*http.Client, error) {
	if base == nil {
		base = &http.Client{}
	}
	if c, ok := a.(Configurable); ok {
		if err := c.Configured(); err != nil {
			return nil, err
		}
	}
	w, ok := a.(TransportWrapper)
	if !ok {
		return base, nil
	}
	cl := *base
	rt := cl.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	cl.Transport = w.WrapTransport(rt)
	return &cl, nil
}

// redactedError carries a scrubbed message while keeping the original error
// reachable for errors.Is/As (budget, block and context checks).
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// SanitizeError scrubs r's secrets from err's message. A BlockedError inside
// err has its URL scrubbed in place, because job finishing prints it.
func SanitizeError(err error, r Redactor) error {
	if err == nil || r == nil {
		return err
	}
	if b, ok := IsBlocked(err); ok {
		b.URL = r.Redact(b.URL)
		b.Reason = r.Redact(b.Reason)
	}
	msg := err.Error()
	clean := r.Redact(msg)
	if clean == msg {
		return err
	}
	return &redactedError{msg: clean, err: err}
}

// RedactRecord returns rec with r applied to its URL, key and every string in
// its fields. It understands the value shapes adapters produce.
func RedactRecord(r Redactor, rec Record) Record {
	if r == nil {
		return rec
	}
	rec.URL = r.Redact(rec.URL)
	rec.SourceKey = r.Redact(rec.SourceKey)
	for i, l := range rec.Links {
		rec.Links[i] = r.Redact(l)
	}
	for k, v := range rec.Fields {
		rec.Fields[k] = redactValue(r, v)
	}
	return rec
}

func redactValue(r Redactor, v any) any {
	switch x := v.(type) {
	case string:
		return r.Redact(x)
	case []string:
		out := make([]string, len(x))
		for i, s := range x {
			out[i] = r.Redact(s)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, s := range x {
			out[k] = r.Redact(s)
		}
		return out
	case []map[string]string:
		out := make([]map[string]string, len(x))
		for i, m := range x {
			out[i] = redactValue(r, m).(map[string]string)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = redactValue(r, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = redactValue(r, e)
		}
		return out
	default:
		return v
	}
}

// RedactingSink scrubs every record before it reaches the underlying sink:
// defence in depth for adapters that already redact their own output.
type RedactingSink struct {
	Sink     RecordSink
	Redactor Redactor
}

func (s RedactingSink) SaveCatalogRecord(ctx context.Context, sourceID int64, parserVersion string, rec Record) (int64, error) {
	return s.Sink.SaveCatalogRecord(ctx, sourceID, parserVersion, RedactRecord(s.Redactor, rec))
}
