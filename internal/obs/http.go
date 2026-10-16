package obs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// RequestIDHeader carries the request id in both directions.
const RequestIDHeader = "X-Request-ID"

// A client-supplied id is reused only if it is short and plain, so a hostile
// header cannot inject into logs.
var requestIDRe = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,64}$`)

type requestIDKey struct{}

// RequestID returns the request id stored in ctx by Middleware, or "".
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(requestIDKey{}).(string)
	return s
}

// NewRequestID returns 16 random bytes in hex.
func NewRequestID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// recorder captures status, body size and a handler error for the access log.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	err    error
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Flush keeps streaming responses (HLS proxying) working through the wrapper.
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// RecordError attaches err to the access-log line of the request whose
// ResponseWriter is w, so a handler error is logged once, with the request
// id, route and status. It reports false when w is not wrapped by
// Middleware (the caller should log err itself).
func RecordError(w http.ResponseWriter, err error) bool {
	for {
		switch x := w.(type) {
		case *recorder:
			x.err = err
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = x.Unwrap()
		default:
			return false
		}
	}
}

// RouteLabel turns a ServeMux pattern ("GET /api/releases/{slug}") into the
// route label ("/api/releases/{slug}"). Requests no pattern matched (404 /
// 405 from the mux) share the label "unmatched", so arbitrary paths never
// become label values.
func RouteLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = strings.TrimSpace(pattern[i+1:])
	}
	if i := strings.IndexByte(pattern, '/'); i > 0 { // host-qualified pattern
		pattern = pattern[i:]
	}
	return pattern
}

func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions:
		return m
	}
	return "OTHER"
}

// Middleware wraps a ServeMux-based handler with request ids, one access-log
// line per request and the HTTP metrics. logger nil uses slog.Default().
//
// The line carries request_id, method, route (the matched pattern, never the
// raw path or query string), status, duration_ms, bytes and client_ip
// reduced to its /24 (or /48) network. Request headers are never logged.
func Middleware(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := r.Header.Get(RequestIDHeader)
		if !requestIDRe.MatchString(id) {
			id = NewRequestID()
		}
		w.Header().Set(RequestIDHeader, id)
		l := logger
		if l == nil {
			l = slog.Default()
		}
		l = l.With("request_id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		r = r.WithContext(WithLogger(ctx, l))
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		d := time.Since(start)
		route, method := RouteLabel(r.Pattern), methodLabel(r.Method)
		HTTPRequests.WithLabelValues(method, route, strconv.Itoa(rec.status)).Inc()
		HTTPDuration.WithLabelValues(method, route, StatusClass(rec.status)).Observe(d.Seconds())
		HTTPResponseBytes.WithLabelValues(route).Add(float64(rec.bytes))

		level := slog.LevelInfo
		switch {
		case rec.status >= 500:
			level = slog.LevelError
		case rec.status >= 400 && rec.err != nil:
			level = slog.LevelWarn
		}
		attrs := []slog.Attr{
			slog.String("method", method),
			slog.String("route", route),
			slog.Int("status", rec.status),
			slog.Float64("duration_ms", float64(d.Microseconds())/1000),
			slog.Int64("bytes", rec.bytes),
			slog.String("client_ip", r.RemoteAddr),
		}
		if rec.err != nil {
			attrs = append(attrs, slog.Any("err", rec.err))
		}
		l.LogAttrs(r.Context(), level, "http_request", attrs...)
	})
}
