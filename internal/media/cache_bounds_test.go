package media

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResultCacheByteBudget(t *testing.T) {
	c := NewResultCache()
	big := strings.Repeat("x", 200<<10) // ~200 KB encoded
	for i := range 1000 {               // ~200 MB offered
		c.Set(fmt.Sprint("search:k:", i), map[string]any{"items": big}, time.Minute)
		if b := c.Bytes(); b > maxCacheBytes {
			t.Fatalf("after %d sets Bytes = %d > budget %d", i+1, b, maxCacheBytes)
		}
	}
	if _, ok := c.Get("search:k:999"); !ok {
		t.Error("the newest entry must be kept")
	}
	if _, ok := c.Get("search:k:0"); ok {
		t.Error("the oldest entry must have been evicted")
	}
	// A value bigger than its share of the budget is not cached at all.
	c.Set("search:huge", strings.Repeat("y", maxCacheValueBytes+1), time.Minute)
	if _, ok := c.Get("search:huge"); ok {
		t.Error("oversized value cached")
	}
}

func TestResultCacheEntryCapEvictsOldest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	c := NewResultCache()
	c.clock = func() time.Time { return now }
	for i := range maxCacheEntries + 500 {
		c.Set(fmt.Sprint("search:", i), i, time.Minute)
	}
	if n := c.Len(); n != maxCacheEntries {
		t.Fatalf("Len = %d, want %d", n, maxCacheEntries)
	}
	if _, ok := c.Get("search:499"); ok {
		t.Error("oldest entries should be evicted first")
	}
	if v, ok := c.Get("search:500"); !ok || string(v.(json.RawMessage)) != "500" {
		t.Errorf("search:500 = %v, %v", v, ok)
	}
	// Expired entries go first; replacing a key does not double count it.
	now = now.Add(2 * time.Minute)
	c.Set("search:a", "1", time.Minute)
	c.Set("search:a", "22", time.Minute)
	if n, b := c.Len(), c.Bytes(); n != 1 || b != len("search:a")+len(`"22"`) {
		t.Errorf("after expiry Len = %d Bytes = %d", n, b)
	}
}

func TestResultCacheHashesLongKeys(t *testing.T) {
	c := NewResultCache()
	k1 := "search:src:" + strings.Repeat("q", 1<<20) + ":1"
	k2 := "search:src:" + strings.Repeat("q", 1<<20) + ":2"
	c.Set(k1, "one", time.Minute)
	c.Set(k2, "two", time.Minute)
	if v, ok := c.Get(k1); !ok || string(v.(json.RawMessage)) != `"one"` {
		t.Errorf("k1 = %v %v", v, ok)
	}
	if v, ok := c.Get(k2); !ok || string(v.(json.RawMessage)) != `"two"` {
		t.Errorf("k2 = %v %v", v, ok)
	}
	if b := c.Bytes(); b > 2*(maxCacheKeyLen+16) {
		t.Errorf("Bytes = %d: long keys not hashed", b)
	}
	if k := cacheKey(k1); !strings.HasPrefix(k, "search:sha256:") || len(k) > maxCacheKeyLen {
		t.Errorf("cacheKey = %q", k)
	}
	if cacheKey("search:short") != "search:short" {
		t.Error("short keys are kept verbatim")
	}
}

type searchAdapter struct{ calls int }

func (a *searchAdapter) Name() string { return "fake" }
func (a *searchAdapter) Search(_ context.Context, q string, _ int) ([]SearchResult, bool, error) {
	a.calls++
	return []SearchResult{{ID: "video/1/" + q[:1]}}, false, nil
}
func (a *searchAdapter) Detail(context.Context, string) (*Detail, error) { return nil, nil }
func (a *searchAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, nil
}

func TestSearchRejectsOverlongQuery(t *testing.T) {
	a := &searchAdapter{}
	reg := NewRegistry()
	reg.Register(a)
	h := NewHandler(NewService(reg))
	get := func(q string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.Search(rec, httptest.NewRequest(http.MethodGet, "/api/search?source=fake&q="+q, nil))
		return rec
	}
	if rec := get(strings.Repeat("a", MaxSearchQueryBytes+1)); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "invalid_query") {
		t.Fatalf("overlong q: %d %s", rec.Code, rec.Body)
	}
	if a.calls != 0 || h.cache.Len() != 0 {
		t.Fatalf("provider called %d times, cache %d entries", a.calls, h.cache.Len())
	}
	for range 2 {
		if rec := get(strings.Repeat("a", MaxSearchQueryBytes)); rec.Code != http.StatusOK {
			t.Fatalf("max-length q: %d %s", rec.Code, rec.Body)
		}
	}
	if a.calls != 1 {
		t.Errorf("second search not served from the cache: %d calls", a.calls)
	}
}

// A caller-chosen, very long resolve id costs a bounded key in the L1
// cache and the refresh throttle, and is still cached and throttled.
func TestResolveLongIDKeysBounded(t *testing.T) {
	now := t0()
	a := &countingAdapter{}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	id := "video/" + strings.Repeat("z", 100<<10)
	for range 2 {
		if code := resolveOnce(t, h, id); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
	}
	resolveURL(t, h, "/api/resolve/fake/"+id+"?refresh=1")
	resolveURL(t, h, "/api/resolve/fake/"+id+"?refresh=1") // throttled
	if a.calls != 2 {
		t.Fatalf("calls = %d, want 2 (one resolve, one refresh)", a.calls)
	}
	c := h.resolves
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) > maxCacheKeyLen || !strings.HasPrefix(k, "resolve:sha256:") {
			t.Errorf("entry key %d bytes: %.40q", len(k), k)
		}
	}
	for k := range c.refreshes {
		if len(k) > maxCacheKeyLen {
			t.Errorf("refresh key %d bytes", len(k))
		}
	}
	if len(c.entries) != 1 || len(c.refreshes) != 1 {
		t.Errorf("entries %d refreshes %d", len(c.entries), len(c.refreshes))
	}
}
