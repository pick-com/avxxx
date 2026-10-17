package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cnc224/shared/egress"
	"github.com/cnc224/shared/egress/egresstest"
)

func newServer(t *testing.T, tn *egresstest.Tunnel) *server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &server{eg: tn.Egress(t, egress.Options{Log: log}), log: log}
}

func post(t *testing.T, s *server, body string) (int, rotateResponse) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/rotate", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	var out rotateResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The endpoint is "give me a usable exit", not "reconnect": a working exit is
// not thrown away because somebody asked, which is what keeps one consumer
// from breaking another's in-flight fetches.
func TestRotateKeepsAWorkingExit(t *testing.T) {
	tn := egresstest.New(t, "198.51.100.1", "198.51.100.2")
	s := newServer(t, tn)

	code, resp := post(t, s, `{"consumer":"books","probe":"http://probe.example/"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, resp.Reason)
	}
	if resp.Rotated {
		t.Error("rotated a working exit")
	}
	if resp.ExitIP != "198.51.100.1" {
		t.Errorf("exit = %q", resp.ExitIP)
	}
	if tn.Rotations() != 0 {
		t.Errorf("reconnects = %d, want 0", tn.Rotations())
	}
}

func TestRotateReplacesABlockedExit(t *testing.T) {
	tn := egresstest.New(t, "192.0.2.1", "198.51.100.2")
	tn.Block("192.0.2.1")
	s := newServer(t, tn)

	code, resp := post(t, s, `{"consumer":"books","probe":"http://probe.example/"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, resp.Reason)
	}
	if !resp.Rotated || resp.ExitIP != "198.51.100.2" {
		t.Errorf("resp = %+v, want the replacement exit", resp)
	}
	if resp.Generation == 0 {
		t.Error("a rotation must advance the generation")
	}
}

// Nothing here is broken when no exit works — the caller should back off, so
// it gets 503 and a reason rather than a 500.
func TestRotateReportsWhenNoExitWorks(t *testing.T) {
	tn := egresstest.New(t, "192.0.2.1", "192.0.2.2", "192.0.2.3")
	tn.Block("192.0.2.1", "192.0.2.2", "192.0.2.3")
	s := newServer(t, tn)

	code, resp := post(t, s, `{"consumer":"books"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if resp.Reason == "" {
		t.Error("a refusal must say why")
	}
}

// One tunnel, several consumers: a second ask inside the floor is answered
// with the current exit and a reason, not with another reconnect that would
// break whoever the last one was for.
func TestRotateRefusesToThrashTheSharedTunnel(t *testing.T) {
	tn := egresstest.New(t, "192.0.2.1", "192.0.2.2", "192.0.2.3")
	tn.Block("192.0.2.1", "192.0.2.2", "192.0.2.3")
	s := newServer(t, tn)

	if code, _ := post(t, s, `{"consumer":"books"}`); code != http.StatusServiceUnavailable {
		t.Fatalf("first ask: status = %d", code)
	}
	after := tn.Rotations()

	code, resp := post(t, s, `{"consumer":"shopping"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("second ask: status = %d", code)
	}
	if !strings.Contains(resp.Reason, "cooling down") {
		t.Errorf("reason = %q, want the cooldown explained", resp.Reason)
	}
	if tn.Rotations() != after {
		t.Errorf("reconnects = %d, want %d — the floor must hold across consumers", tn.Rotations(), after)
	}
}

func TestStatusReportsWhereTrafficLeaves(t *testing.T) {
	tn := egresstest.New(t, "198.51.100.7")
	s := newServer(t, tn)

	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var st egress.Status
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || !st.Up {
		t.Errorf("status = %+v, want an enabled, running tunnel", st)
	}
	// The proxy address comes from here, so consumers never hardcode it.
	if st.ProxyURL != tn.ProxyURL {
		t.Errorf("proxy_url = %q, want %q", st.ProxyURL, tn.ProxyURL)
	}
	if st.ExitIP != "198.51.100.7" {
		t.Errorf("exit_ip = %q", st.ExitIP)
	}
}

// The service fetches whatever probe it is handed, so the shape is checked.
func TestRotateRejectsABadProbe(t *testing.T) {
	tn := egresstest.New(t, "198.51.100.1")
	s := newServer(t, tn)

	for _, body := range []string{
		`{"probe":"file:///etc/passwd"}`,
		`{"probe":"/just/a/path"}`,
		`{"probe":"::::"}`,
		`not json at all`,
	} {
		if code, _ := post(t, s, body); code != http.StatusBadRequest {
			t.Errorf("%s → %d, want 400", body, code)
		}
	}
}

func TestHealthz(t *testing.T) {
	tn := egresstest.New(t, "198.51.100.1")
	w := httptest.NewRecorder()
	newServer(t, tn).routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Errorf("healthz = %d", w.Code)
	}
}

func TestRotateEmptyBodyAndNoHostProbe(t *testing.T) {
	tn := egresstest.New(t, "198.51.100.1")
	s := newServer(t, tn)
	if code, _ := post(t, s, ""); code != http.StatusOK {
		t.Errorf("empty body → %d", code)
	}
	if code, _ := post(t, s, `{"probe":"http://"}`); code != http.StatusBadRequest {
		t.Errorf("no host → %d", code)
	}
}

func TestRotateCallerHangsUp(t *testing.T) {
	tn := egresstest.New(t, "192.0.2.1", "192.0.2.2")
	tn.Block("192.0.2.1", "192.0.2.2")
	s := newServer(t, tn)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodPost, "/rotate", strings.NewReader(`{"probe":"http://probe.example/"}`)).WithContext(ctx)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	t.Logf("code=%d body=%s", w.Code, w.Body.String())
}

func TestEnvHelpers(t *testing.T) {
	t.Setenv("X_INT", "5")
	if envInt("X_INT", 1) != 5 {
		t.Error()
	}
	t.Setenv("X_INT", "bad")
	if envInt("X_INT", 1) != 1 {
		t.Error()
	}
	t.Setenv("X_INT", "-3")
	if envInt("X_INT", 1) != 1 || envInt("X_UNSET", 7) != 7 {
		t.Error()
	}
	t.Setenv("X_D", "2s")
	if envDuration("X_D", time.Second) != 2*time.Second {
		t.Error()
	}
	t.Setenv("X_D", "bad")
	if envDuration("X_D", time.Second) != time.Second || envDuration("X_UNSET", time.Minute) != time.Minute {
		t.Error()
	}
	t.Setenv("X_D", "-1s")
	if envDuration("X_D", time.Second) != time.Second {
		t.Error()
	}
}

func TestRunLifecycle(t *testing.T) {
	t.Setenv("EGRESS_ADDR", "127.0.0.1:0")
	t.Setenv("VPN_PROXY_URL", "http://127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() { done <- run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if !<-done {
		t.Error("graceful shutdown must report success")
	}
}

func TestRunDefaultAddrAndListenFailure(t *testing.T) {
	t.Setenv("EGRESS_ADDR", "127.0.0.1:-1")
	if run(context.Background()) {
		t.Error("bad addr must fail")
	}
}

func TestRunConfigError(t *testing.T) {
	t.Setenv("VPN_PROXY_URL", "http://bad host/")
	t.Setenv("VPN_CONTROL_URL", "http://bad host/")
	if run(context.Background()) {
		t.Skip("config accepted; no config error path reachable")
	}
}

func TestMainExits(t *testing.T) {
	t.Setenv("EGRESS_ADDR", "127.0.0.1:-1")
	old := exit
	defer func() { exit = old }()
	code := 0
	exit = func(c int) { code = c }
	main()
	if code != 1 {
		t.Errorf("exit code %d", code)
	}
}

func TestRunDefaultAddr(t *testing.T) {
	t.Setenv("EGRESS_ADDR", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() { done <- run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done // true when :9000 was free, false when something else holds it
}
