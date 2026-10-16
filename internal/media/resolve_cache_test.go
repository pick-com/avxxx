package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// countingAdapter resolves to a fresh stream URL on every call and fails
// while fail is set.
type countingAdapter struct {
	calls   int
	fail    bool
	expires *time.Time
}

func (a *countingAdapter) Name() string { return "fake" }
func (a *countingAdapter) Search(context.Context, string, int) ([]SearchResult, bool, error) {
	return nil, false, nil
}
func (a *countingAdapter) Detail(context.Context, string) (*Detail, error) { return nil, nil }
func (a *countingAdapter) Resolve(_ context.Context, id string) (*ResolveResult, error) {
	a.calls++
	if a.fail {
		return nil, errors.New("upstream down")
	}
	return &ResolveResult{Streams: []Stream{{URL: fmt.Sprintf("https://cdn.example/%s.mp4?t=%d", id, a.calls), Protocol: "mp4", ExpiresAt: a.expires}}}, nil
}

func newTestHandler(a Adapter, ttl time.Duration, clock func() time.Time) *Handler {
	reg := NewRegistry()
	reg.Register(a)
	h := &Handler{service: NewService(reg), cache: NewResultCache(), resolves: newResolveCache(ttl, DefaultResolveMaxAge)}
	h.cache.clock = clock
	h.resolves.clock = clock
	return h
}

func resolveOnce(t *testing.T, h *Handler, id string) int {
	t.Helper()
	code, _ := resolveURL(t, h, "/api/resolve/fake/"+id)
	return code
}

// resolveURL posts to path and returns the status and the first stream URL.
func resolveURL(t *testing.T, h *Handler, path string) (int, string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/resolve/{source}/{id...}", h.Resolve)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	var v ResolveResult
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if len(v.Streams) == 0 {
		return rec.Code, ""
	}
	return rec.Code, v.Streams[0].URL
}

func TestResolveCacheTTL(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	a := &countingAdapter{}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })

	for range 3 {
		if code := resolveOnce(t, h, "video/1/abc-1"); code != 200 {
			t.Fatalf("status %d", code)
		}
	}
	if a.calls != 1 {
		t.Fatalf("calls within TTL = %d, want 1", a.calls)
	}
	resolveOnce(t, h, "video/2/abc-2") // per (source, id)
	if a.calls != 2 {
		t.Fatalf("other id must not hit the cache: calls = %d", a.calls)
	}
	now = now.Add(5*time.Minute + time.Second)
	resolveOnce(t, h, "video/1/abc-1")
	if a.calls != 3 {
		t.Fatalf("expired entry must refetch: calls = %d", a.calls)
	}
}

func TestResolveCacheNeverCachesErrors(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	a := &countingAdapter{fail: true}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	if code := resolveOnce(t, h, "video/1/x-1"); code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", code)
	}
	resolveOnce(t, h, "video/1/x-1")
	if a.calls != 2 {
		t.Fatalf("errors must not be cached: calls = %d", a.calls)
	}
	a.fail = false
	resolveOnce(t, h, "video/1/x-1")
	resolveOnce(t, h, "video/1/x-1")
	if a.calls != 3 {
		t.Fatalf("success after errors should be cached: calls = %d", a.calls)
	}
}

func TestResolveCacheDisabledAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	a := &countingAdapter{}
	h := newTestHandler(a, 0, func() time.Time { return now })
	resolveOnce(t, h, "video/1/x-1")
	resolveOnce(t, h, "video/1/x-1")
	if a.calls != 2 {
		t.Fatalf("TTL 0 disables the cache: calls = %d", a.calls)
	}

	// A stream expiring in 20 s is never cached (30 s safety margin).
	exp := now.Add(20 * time.Second)
	b := &countingAdapter{expires: &exp}
	h = newTestHandler(b, 5*time.Minute, func() time.Time { return now })
	resolveOnce(t, h, "video/1/x-1")
	resolveOnce(t, h, "video/1/x-1")
	if b.calls != 2 {
		t.Fatalf("near-expiry stream must not be cached: calls = %d", b.calls)
	}
}

func TestResolveCacheTTLHelpers(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	exp := now.Add(2 * time.Minute)
	v := &ResolveResult{Streams: []Stream{{URL: "https://x/1", ExpiresAt: &exp}}}
	if got := slideExpiry(now, 5*time.Minute, resolveHardCap(v, now, DefaultResolveMaxAge)); got.Sub(now) != 90*time.Second {
		t.Errorf("capped ttl = %s, want 1m30s", got.Sub(now))
	}
	for in, want := range map[string]time.Duration{"": DefaultResolveTTL, "bogus": DefaultResolveTTL, "30s": 30 * time.Second, "0": 0, "-1m": 0} {
		if got := ResolveTTLFromEnv(in); got != want {
			t.Errorf("ResolveTTLFromEnv(%q) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]time.Duration{"": DefaultResolveMaxAge, "bogus": DefaultResolveMaxAge, "1h": time.Hour, "0": DefaultResolveMaxAge, "-1m": DefaultResolveMaxAge} {
		if got := ResolveMaxAgeFromEnv(in); got != want {
			t.Errorf("ResolveMaxAgeFromEnv(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestExactCodeCandidates(t *testing.T) {
	items := []SearchResult{{ID: "video/1/xyz-1"}, {ID: "video/2/xyz-1"}, {ID: "video/1/xyz-1"}, {ID: "video/3/xyz-10"}}
	got := ExactCodeCandidates(items, "XYZ-1")
	if len(got) != 2 || got[0] != "video/1/xyz-1" || got[1] != "video/2/xyz-1" {
		t.Fatalf("candidates = %v", got)
	}
	if ExactCodeCandidates(items, " ") != nil {
		t.Error("blank code has no candidates")
	}
}

func TestStatusError(t *testing.T) {
	err := fmt.Errorf("search: %w", &StatusError{Source: "javtiful", Code: 429})
	if HTTPStatus(err) != 429 || !IsRateLimited(err) {
		t.Fatal("429 must be detected through wrapping")
	}
	if IsRateLimited(&StatusError{Code: 500}) || IsRateLimited(errors.New("x")) {
		t.Fatal("only 403/429 are rate limits")
	}
}
