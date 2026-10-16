package media

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory ResolveStore.
type memStore struct {
	mu      sync.Mutex
	rows    map[string]StoredResolve
	deletes int
	extends int
}

func newMemStore() *memStore { return &memStore{rows: map[string]StoredResolve{}} }

func (m *memStore) LoadResolve(_ context.Context, key string, now time.Time) (*StoredResolve, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key]
	if !ok || !now.Before(r.ExpiresAt) {
		return nil, nil
	}
	return &r, nil
}
func (m *memStore) SaveResolve(_ context.Context, key string, e StoredResolve) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows[key] = e
	return nil
}
func (m *memStore) ExtendResolve(_ context.Context, key string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key]
	if !ok {
		return nil
	}
	m.extends++
	if at.After(r.MaxExpiresAt) {
		at = r.MaxExpiresAt
	}
	if at.After(r.ExpiresAt) {
		r.ExpiresAt = at
	}
	m.rows[key] = r
	return nil
}
func (m *memStore) DeleteResolve(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	delete(m.rows, key)
	return nil
}
func (m *memStore) PurgeResolves(_ context.Context, now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, r := range m.rows {
		if !now.Before(r.ExpiresAt) {
			delete(m.rows, k)
			n++
		}
	}
	return n, nil
}
func (m *memStore) row(key string) (StoredResolve, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[key]
	return r, ok
}

func t0() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }

func TestSlidingTTLMath(t *testing.T) {
	now := t0()
	noExpiry := &ResolveResult{Streams: []Stream{{URL: "https://x/1"}}}
	if got := resolveHardCap(noExpiry, now, 30*time.Minute); !got.Equal(now.Add(30 * time.Minute)) {
		t.Errorf("unknown expiry cap = %s, want resolved + max age", got)
	}
	a, b := now.Add(10*time.Minute), now.Add(4*time.Minute)
	known := &ResolveResult{Streams: []Stream{{URL: "https://x/1", ExpiresAt: &a}}, Subtitles: []Stream{{URL: "https://x/s", ExpiresAt: &b}}}
	if got := resolveHardCap(known, now, 30*time.Minute); !got.Equal(b.Add(-30 * time.Second)) {
		t.Errorf("known expiry cap = %s, want earliest expiry - 30s", got)
	}
	capAt := now.Add(7 * time.Minute)
	if got := slideExpiry(now, 5*time.Minute, capAt); !got.Equal(now.Add(5 * time.Minute)) {
		t.Errorf("slide below cap = %s", got)
	}
	if got := slideExpiry(now.Add(4*time.Minute), 5*time.Minute, capAt); !got.Equal(capAt) {
		t.Errorf("slide past cap = %s, want the cap", got)
	}
}

func TestSlidingTTLUntilMaxAge(t *testing.T) {
	now := t0()
	a := &countingAdapter{}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	resolveOnce(t, h, "video/1/a-1")
	// A view every 4 min keeps the entry alive past the 5 min TTL …
	for range 7 {
		now = now.Add(4 * time.Minute)
		resolveOnce(t, h, "video/1/a-1")
	}
	if a.calls != 1 {
		t.Fatalf("sliding hits refetched: calls = %d", a.calls)
	}
	// … but never past MEDIA_RESOLVE_MAX_AGE (30 min) after the resolve.
	now = t0().Add(30 * time.Minute)
	resolveOnce(t, h, "video/1/a-1")
	if a.calls != 2 {
		t.Fatalf("max age must force a refetch: calls = %d", a.calls)
	}
}

func TestSlidingTTLStopsAtStreamExpiry(t *testing.T) {
	now := t0()
	exp := now.Add(10 * time.Minute)
	a := &countingAdapter{expires: &exp}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	for _, at := range []time.Duration{0, 4 * time.Minute, 8 * time.Minute, 9*time.Minute + 29*time.Second} {
		now = t0().Add(at)
		resolveOnce(t, h, "video/1/a-1")
	}
	if a.calls != 1 {
		t.Fatalf("hits before expiry-30s refetched: calls = %d", a.calls)
	}
	now = t0().Add(9*time.Minute + 30*time.Second)
	resolveOnce(t, h, "video/1/a-1")
	if a.calls != 2 {
		t.Fatalf("a hit at expiry-30s must refetch: calls = %d", a.calls)
	}
}

func TestRefreshEvictsAndIsRateLimited(t *testing.T) {
	now := t0()
	a := &countingAdapter{}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	st := newMemStore()
	h.UseResolveStore(st)
	key := "resolve:fake:video/1/a-1"

	_, first := resolveURL(t, h, "/api/resolve/fake/video/1/a-1")
	if _, ok := st.row(key); !ok {
		t.Fatal("result not saved to the store")
	}
	_, fresh := resolveURL(t, h, "/api/resolve/fake/video/1/a-1?refresh=1")
	if fresh == first || a.calls != 2 || st.deletes != 1 {
		t.Fatalf("refresh must evict and re-resolve: %q -> %q, calls %d, deletes %d", first, fresh, a.calls, st.deletes)
	}
	if r, _ := st.row(key); !strings.Contains(string(r.Result), "t=2") {
		t.Fatalf("store keeps the old result: %s", r.Result)
	}
	// A second refresh within 60 s gets the cached (fresh) result.
	now = now.Add(59 * time.Second)
	if _, got := resolveURL(t, h, "/api/resolve/fake/video/1/a-1?refresh=1"); got != fresh || a.calls != 2 {
		t.Fatalf("rate-limited refresh = %q, calls %d", got, a.calls)
	}
	// Other ids are limited separately.
	resolveURL(t, h, "/api/resolve/fake/video/2/b-2?refresh=1")
	if a.calls != 3 {
		t.Fatalf("other id: calls = %d", a.calls)
	}
	now = now.Add(time.Second)
	if _, got := resolveURL(t, h, "/api/resolve/fake/video/1/a-1?refresh=1"); got == fresh || a.calls != 4 {
		t.Fatalf("refresh after 60 s = %q, calls %d", got, a.calls)
	}
}

func TestRefreshWithoutCacheEntry(t *testing.T) {
	now := t0()
	a := &countingAdapter{}
	h := newTestHandler(a, 5*time.Minute, func() time.Time { return now })
	resolveURL(t, h, "/api/resolve/fake/video/1/a-1?refresh=1")
	resolveURL(t, h, "/api/resolve/fake/video/1/a-1?refresh=1") // limited: served from the cache
	if a.calls != 1 {
		t.Fatalf("calls = %d, want 1", a.calls)
	}
}

func TestAllowRefreshBounded(t *testing.T) {
	now := t0()
	c := newResolveCache(time.Minute, time.Hour)
	c.clock = func() time.Time { return now }
	if !c.allowRefresh("k") || c.allowRefresh("k") {
		t.Fatal("one refresh per interval")
	}
	now = now.Add(DefaultResolveRefreshInterval)
	if !c.allowRefresh("k") {
		t.Fatal("allowed again after the interval")
	}
	for i := range maxResolveEntries {
		c.refreshes[strings.Repeat("x", 1)+time.Duration(i).String()] = now
	}
	if c.allowRefresh("new") {
		t.Fatal("a full guard refuses new keys")
	}
}

func TestSharedStoreAcrossInstances(t *testing.T) {
	now := t0()
	st := newMemStore()
	a1, a2 := &countingAdapter{}, &countingAdapter{}
	h1 := newTestHandler(a1, 5*time.Minute, func() time.Time { return now })
	h2 := newTestHandler(a2, 5*time.Minute, func() time.Time { return now })
	h1.UseResolveStore(st)
	h2.UseResolveStore(st)
	_, u1 := resolveURL(t, h1, "/api/resolve/fake/video/1/a-1")
	now = now.Add(4 * time.Minute)
	_, u2 := resolveURL(t, h2, "/api/resolve/fake/video/1/a-1")
	if u1 != u2 || a2.calls != 0 {
		t.Fatalf("second instance must reuse the stored result: %q vs %q, calls %d", u1, u2, a2.calls)
	}
	r, _ := st.row("resolve:fake:video/1/a-1")
	if !r.ExpiresAt.Equal(now.Add(5*time.Minute)) || !r.MaxExpiresAt.Equal(t0().Add(DefaultResolveMaxAge)) {
		t.Fatalf("store row not slid: expires %s max %s", r.ExpiresAt, r.MaxExpiresAt)
	}
	// h1's own L1 hit 4 min later writes the slide through (>= ttl/4).
	now = now.Add(4 * time.Minute)
	before := st.extends
	resolveURL(t, h1, "/api/resolve/fake/video/1/a-1")
	if st.extends != before+1 || a1.calls != 1 {
		t.Fatalf("L1 slide not written through: extends %d->%d calls %d", before, st.extends, a1.calls)
	}
}

func TestStoreSkipsSecretsAndHugeRows(t *testing.T) {
	now := t0()
	c := newResolveCache(time.Minute, time.Hour)
	c.clock = func() time.Time { return now }
	st := newMemStore()
	c.store = st
	c.put(context.Background(), "secret", &ResolveResult{Streams: []Stream{{URL: "https://x/1", Headers: map[string]string{"Cookie": "sid=1"}}}})
	c.put(context.Background(), "huge", &ResolveResult{Streams: []Stream{{URL: "https://x/" + strings.Repeat("a", MaxStoredResolveBytes)}}})
	c.put(context.Background(), "ok", &ResolveResult{Streams: []Stream{{URL: "https://x/1", Headers: map[string]string{"Referer": "https://x/"}}}})
	for key, want := range map[string]bool{"secret": false, "huge": false, "ok": true} {
		if _, ok := st.row(key); ok != want {
			t.Errorf("%s stored = %v, want %v", key, ok, want)
		}
		if _, ok := c.get(context.Background(), key); !ok {
			t.Errorf("%s missing from L1", key)
		}
	}
}
