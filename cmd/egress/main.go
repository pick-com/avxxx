// The egress service owns this host's shared VPN tunnel.
//
// Several scrapers — in different languages, in different compose projects,
// on one VPS — send their outbound fetches through one gluetun sidecar, and
// exactly one process has to decide when that tunnel reconnects. A rotation is
// global: an unserialized one yanks the address out from under everybody
// else's in-flight requests. That serialization is the reason this exists.
//
// Two endpoints are the whole contract:
//
//	GET  /status  → where to send traffic, and what the current exit is
//	POST /rotate  → "I can't reach <probe>" → a usable exit, or why not
//
// The proxy address is returned by /status rather than configured into each
// consumer, so growing the pool to several tunnels later stays a change here
// instead of a change in every repo.
//
// It deliberately has no database: rotation must not depend on any one
// consumer's storage being up, and a service that needed this project's
// Postgres could not serve the others at all.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/cnc224/shared/egress"
)

// exit is os.Exit, swappable so main's failure path can be tested.
var exit = os.Exit

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !run(ctx) {
		stop()
		exit(1)
	}
}

// run serves until ctx is done; false means it failed to start or serve.
func run(ctx context.Context) bool {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	eg, err := egress.New(egress.Options{
		ControlURL:        os.Getenv("VPN_CONTROL_URL"),
		ProxyURL:          os.Getenv("VPN_PROXY_URL"),
		ProbeURL:          os.Getenv("VPN_PROBE_URL"),
		Attempts:          envInt("VPN_ROTATE_ATTEMPTS", 3),
		MinRotateInterval: envDuration("VPN_MIN_ROTATE_INTERVAL", 5*time.Minute),
		Log:               log,
	})
	if err != nil {
		log.Error("egress config", "err", err)
		return false
	}

	addr := os.Getenv("EGRESS_ADDR")
	if addr == "" {
		addr = ":9000"
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: (&server{eg: eg, log: log}).routes(),
		// A rotation is minutes of reconnecting and probing, so the write
		// deadline has to outlast it or the caller gets a truncated reply for
		// work that actually succeeded.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Info("egress service listening", "addr", addr,
		"tunnel", eg.Enabled(), "proxy", proxyString(eg))
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("listen", "err", err)
		return false
	}
	return true
}

type server struct {
	eg  *egress.Egress
	log *slog.Logger
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /status", s.handleStatus)
	mux.HandleFunc("POST /rotate", s.handleRotate)
	return mux
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.eg.Status(r.Context()))
}

type rotateRequest struct {
	// Consumer names the asker, for the log. Several scrapers share one
	// tunnel, so "who wanted this rotation" is the first question afterwards.
	Consumer string `json:"consumer"`
	// Probe is the URL that must be reachable. The caller names it because
	// "can this exit reach my source?" has no single answer once the tunnel
	// is shared; empty falls back to the service's own default.
	Probe string `json:"probe"`
}

type rotateResponse struct {
	Rotated    bool   `json:"rotated"`
	ExitIP     string `json:"exit_ip,omitempty"`
	Country    string `json:"country,omitempty"`
	Generation uint64 `json:"generation"`
	Reason     string `json:"reason"`
}

func (s *server) handleRotate(w http.ResponseWriter, r *http.Request) {
	var req rotateRequest
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, rotateResponse{Reason: "bad json: " + err.Error()})
			return
		}
	}
	if req.Probe != "" {
		if err := validProbe(req.Probe); err != nil {
			writeJSON(w, http.StatusBadRequest, rotateResponse{Reason: err.Error()})
			return
		}
	}
	who := req.Consumer
	if who == "" {
		who = "anonymous"
	}
	s.log.Info("rotation requested", "consumer", who, "probe", req.Probe)

	res, err := s.eg.Provide(r.Context(), req.Probe)
	resp := rotateResponse{
		Rotated:    res.Rotated,
		ExitIP:     res.Exit.IP,
		Country:    res.Exit.Country,
		Generation: s.eg.Generation(),
		Reason:     res.Reason,
	}
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, resp)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The caller hung up mid-rotation; there is nobody to answer.
		s.log.Warn("rotation abandoned", "consumer", who, "err", err)
	default:
		// 503, not 500: nothing is broken here — no exit can currently reach
		// what the caller asked for, and it should back off and retry.
		s.log.Warn("no usable exit", "consumer", who, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, resp)
	}
}

// validProbe rejects anything that is not an absolute http(s) URL. The service
// fetches whatever it is handed, so the shape is checked even though the
// listener is only reachable from the host's own compose network.
func validProbe(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("probe is not a url")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("probe must be http or https")
	}
	if u.Host == "" {
		return errors.New("probe has no host")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func proxyString(eg *egress.Egress) string {
	if u := eg.ProxyURL(); u != nil {
		return u.String()
	}
	return ""
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}
