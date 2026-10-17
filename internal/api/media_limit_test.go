package api

import (
	"avxxx/internal/community"
	"avxxx/internal/media"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingAdapter counts provider calls (every one would be a request to a
// third-party site).
type countingAdapter struct{ calls atomic.Int64 }

func (a *countingAdapter) Name() string { return "fake" }
func (a *countingAdapter) Search(context.Context, string, int) ([]media.SearchResult, bool, error) {
	a.calls.Add(1)
	return nil, false, nil
}
func (a *countingAdapter) Detail(context.Context, string) (*media.Detail, error) {
	a.calls.Add(1)
	return &media.Detail{}, nil
}
func (a *countingAdapter) Resolve(context.Context, string) (*media.ResolveResult, error) {
	a.calls.Add(1)
	return &media.ResolveResult{Streams: []media.Stream{{URL: "https://cdn.example/v.m3u8"}}}, nil
}

// The provider routes are public and each uncached id (or ?refresh=1) is a
// live provider request, so one client address must not be able to drive
// an unbounded number of them.
func TestMediaProviderRoutesAreRateLimitedPerAddress(t *testing.T) {
	a := &countingAdapter{}
	reg := media.NewRegistry()
	reg.Register(a)
	srv := &Server{media: media.NewHandler(media.NewService(reg)), limiter: community.NewMemoryLimiter()}
	mux := http.NewServeMux()
	for _, rt := range srv.mediaRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	do := func(method, path, addr string) int {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = addr + ":1234"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	// Distinct ids: every one reaches the provider until the limit.
	for i := 0; i < limitMediaResolveIP.limit; i++ {
		if code := do("POST", fmt.Sprintf("/api/resolve/fake/id-%d", i), "203.0.113.7"); code != 200 {
			t.Fatalf("resolve %d: %d", i, code)
		}
	}
	before := a.calls.Load()
	if code := do("POST", "/api/resolve/fake/one-more", "203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("resolve past the limit: %d", code)
	}
	if a.calls.Load() != before {
		t.Fatal("a limited resolve still reached the provider")
	}
	// Another address is not affected.
	if code := do("POST", "/api/resolve/fake/id-0", "198.51.100.9"); code != 200 {
		t.Fatalf("other address: %d", code)
	}

	// ?refresh=1 has its own, tighter budget.
	for i := 0; i < limitMediaRefreshIP.limit; i++ {
		if code := do("POST", fmt.Sprintf("/api/resolve/fake/r-%d?refresh=1", i), "192.0.2.5"); code != 200 {
			t.Fatalf("refresh %d: %d", i, code)
		}
	}
	if code := do("POST", "/api/resolve/fake/r-x?refresh=1", "192.0.2.5"); code != http.StatusTooManyRequests {
		t.Fatalf("refresh past the limit: %d", code)
	}

	// Search and detail share the lookup budget.
	for i := 0; i < limitMediaLookupIP.limit; i++ {
		path := fmt.Sprintf("/api/detail/fake/d-%d", i)
		if i%2 == 0 {
			path = fmt.Sprintf("/api/search?source=fake&q=q%d", i)
		}
		if code := do("GET", path, "192.0.2.77"); code != 200 {
			t.Fatalf("lookup %d (%s): %d", i, path, code)
		}
	}
	if code := do("GET", "/api/search?source=fake&q=again", "192.0.2.77"); code != http.StatusTooManyRequests {
		t.Fatalf("lookup past the limit: %d", code)
	}
}
