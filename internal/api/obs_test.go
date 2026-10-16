package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"avxxx/internal/obs"
)

// TestAccessLogRedaction runs the real route table behind obs.Middleware: a
// request with an Authorization header and a token query parameter is logged
// once, by route pattern, with the handler error attached and no secret.
func TestAccessLogRedaction(t *testing.T) {
	var buf bytes.Buffer
	h := obs.Middleware(New(nil, nil), obs.NewLogger(&buf, "api", obs.LogConfig{}))
	const bearer, query = "admin-bearer-SECRET-1", "query-token-SECRET-2"
	req := httptest.NewRequest("POST", "/api/events?token="+query, strings.NewReader("[]"))
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set(obs.RequestIDHeader, "req-from-edge-01")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get(obs.RequestIDHeader) != "req-from-edge-01" {
		t.Errorf("request id not echoed: %q", rec.Header().Get(obs.RequestIDHeader))
	}
	out := buf.String()
	if strings.Contains(out, bearer) || strings.Contains(out, query) || strings.Contains(out, "SECRET") {
		t.Fatalf("secret in log output:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("want one access line, got:\n%s", out)
	}
	var l map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &l); err != nil {
		t.Fatal(err)
	}
	if l["route"] != "/api/events" || l["method"] != "POST" || l["status"] != float64(503) ||
		l["request_id"] != "req-from-edge-01" || l["err"] == nil || l["level"] != "ERROR" {
		t.Errorf("access line = %v", l)
	}
}
