// Package obs is the observability layer shared by the five binaries
// (tech plan 3.12): structured logging with log/slog, request ids, and
// Prometheus metrics served on a separate internal listener.
//
// Logging: SetupLogging installs a JSON (LOG_FORMAT=json, the default) or
// text (LOG_FORMAT=text, for development) slog handler at LOG_LEVEL
// (debug|info|warn|error, default info) as the process default, so the
// remaining log.Printf calls are routed through it too. Every record passes
// through Redact: secrets, email addresses, push endpoints and raw IPs never
// reach the output (see redact.go).
package obs

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// LogConfig selects the handler; zero values take the defaults.
type LogConfig struct {
	Format string // "json" (default) or "text"
	Level  string // "debug", "info" (default), "warn", "error"
}

// LogConfigFromEnv reads LOG_FORMAT and LOG_LEVEL.
func LogConfigFromEnv(getenv func(string) string) LogConfig {
	return LogConfig{Format: getenv("LOG_FORMAT"), Level: getenv("LOG_LEVEL")}
}

// ParseLevel maps a LOG_LEVEL value to a slog level (unknown: info).
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// NewLogger builds a redacting logger writing to w. service is attached to
// every record so the five binaries' logs can share one sink.
func NewLogger(w io.Writer, service string, c LogConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: ParseLevel(c.Level), ReplaceAttr: Redact}
	var h slog.Handler
	if strings.EqualFold(strings.TrimSpace(c.Format), "text") {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	l := slog.New(h)
	if service != "" {
		l = l.With("service", service)
	}
	return l
}

// SetupLogging installs the process-wide logger for service from the
// environment and returns it. log.Printf output goes through it at INFO.
func SetupLogging(service string) *slog.Logger {
	l := NewLogger(os.Stderr, service, LogConfigFromEnv(os.Getenv))
	slog.SetDefault(l)
	return l
}

// Fatal logs msg at ERROR with err and exits 1 (the slog form of log.Fatal).
func Fatal(msg string, err error, args ...any) {
	slog.Error(msg, append([]any{"err", err}, args...)...)
	os.Exit(1)
}

type ctxKey struct{}

// WithLogger returns ctx carrying l (see L).
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// L returns the logger carried by ctx (with request_id / job_id attached),
// or the default logger.
func L(ctx context.Context) *slog.Logger {
	if ctx != nil {
		if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok {
			return l
		}
	}
	return slog.Default()
}

// hashKey keys HashID. LOG_HASH_KEY makes hashes stable across processes and
// restarts (set it in production to correlate a user's lines); without it a
// random per-process key still prevents reversing the hash.
var hashKey = func() []byte {
	if k := os.Getenv("LOG_HASH_KEY"); k != "" {
		return []byte(k)
	}
	b := make([]byte, 32)
	rand.Read(b)
	return b
}()

// HashID returns a keyed, truncated hash of an identifier (user id, device
// id) for logs: the same input gives the same 12 hex chars within a key, and
// the raw id is never written.
func HashID(kind string, id int64) string {
	return HashString(kind + ":" + strconv.FormatInt(id, 10))
}

// HashKeyed is the full-length (32 hex chars) keyed hash of kind:s, for
// stored pseudonymous ids (consent_records.device_hash) where the 12-char
// log hash would collide too often. Same key as HashID: stable across
// restarts only when LOG_HASH_KEY is set.
func HashKeyed(kind, s string) string {
	m := hmac.New(sha256.New, hashKey)
	m.Write([]byte(kind + ":" + s))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// HashString is HashID for a string identifier.
func HashString(s string) string {
	m := hmac.New(sha256.New, hashKey)
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))[:12]
}
