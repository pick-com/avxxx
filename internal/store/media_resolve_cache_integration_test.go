package store

// media_resolve_cache (shared resolve cache). Runs only under
// `make test-integration` (AVXXX_TEST_DATABASE_URL).

import (
	"avxxx/internal/media"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolveCountAdapter struct{ calls atomic.Int32 }

func (a *resolveCountAdapter) Name() string { return "fakecache" }
func (a *resolveCountAdapter) Search(context.Context, string, int) ([]media.SearchResult, bool, error) {
	return nil, false, nil
}
func (a *resolveCountAdapter) Detail(context.Context, string) (*media.Detail, error) { return nil, nil }
func (a *resolveCountAdapter) Resolve(_ context.Context, id string) (*media.ResolveResult, error) {
	n := a.calls.Add(1)
	return &media.ResolveResult{Streams: []media.Stream{{URL: fmt.Sprintf("https://cdn.example/%s.mp4?n=%d", id, n), Protocol: "mp4"}}}, nil
}

func TestMediaResolveCacheIntegration(t *testing.T) {
	openIsolatedTestDB(t, "resolvecache")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Store primitives.
	now := time.Now().UTC().Truncate(time.Microsecond)
	row := media.StoredResolve{Result: json.RawMessage(`{"streams":[{"url":"https://x/1","protocol":"mp4"}]}`), ExpiresAt: now.Add(time.Minute), MaxExpiresAt: now.Add(3 * time.Minute), RefreshedAt: now}
	if err := s.SaveResolve(ctx, "k1", row); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadResolve(ctx, "k1", now)
	if err != nil || got == nil || !got.ExpiresAt.Equal(row.ExpiresAt) || !strings.Contains(string(got.Result), "https://x/1") {
		t.Fatalf("load = %+v, %v", got, err)
	}
	if got, _ := s.LoadResolve(ctx, "k1", now.Add(time.Minute)); got != nil {
		t.Fatal("an expired row must not load")
	}
	if err := s.ExtendResolve(ctx, "k1", now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoadResolve(ctx, "k1", now); got == nil || !got.ExpiresAt.Equal(row.MaxExpiresAt) {
		t.Fatalf("extend must stop at max_expires_at: %+v", got)
	}
	if err := s.ExtendResolve(ctx, "k1", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoadResolve(ctx, "k1", now); got == nil || !got.ExpiresAt.Equal(row.MaxExpiresAt) {
		t.Fatal("extend must never move expires_at backwards")
	}
	big := media.StoredResolve{Result: json.RawMessage(`{"u":"` + strings.Repeat("a", media.MaxStoredResolveBytes) + `"}`), ExpiresAt: now.Add(time.Minute), MaxExpiresAt: now.Add(time.Minute), RefreshedAt: now}
	if err := s.SaveResolve(ctx, "big", big); err == nil {
		t.Fatal("oversized rows must be rejected")
	}
	if _, err := s.Exec(ctx, `INSERT INTO media_resolve_cache(key,result,expires_at,max_expires_at) VALUES('raw', jsonb_build_object('u', repeat('a', 70000)), now(), now())`); err == nil {
		t.Fatal("the table CHECK must cap row size")
	}
	old := row
	old.ExpiresAt, old.MaxExpiresAt = now.Add(-time.Minute), now.Add(-time.Minute)
	if err := s.SaveResolve(ctx, "old", old); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PurgeResolves(ctx, now); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if err := s.DeleteResolve(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoadResolve(ctx, "k1", now); got != nil {
		t.Fatal("deleted row still loads")
	}

	// Two API instances (handlers) share results through the table, and a
	// refresh on one replaces the shared row.
	t.Setenv("MEDIA_RESOLVE_TTL", "5m")
	a1, a2 := &resolveCountAdapter{}, &resolveCountAdapter{}
	h1, h2 := newCacheHandler(a1, s), newCacheHandler(a2, s)
	u1 := postResolve(t, h1, "/api/resolve/fakecache/video/1/a-1")
	if u2 := postResolve(t, h2, "/api/resolve/fakecache/video/1/a-1"); u2 != u1 || a2.calls.Load() != 0 {
		t.Fatalf("second instance did not reuse the row: %q vs %q (calls %d)", u1, u2, a2.calls.Load())
	}
	fresh := postResolve(t, h1, "/api/resolve/fakecache/video/1/a-1?refresh=1")
	if fresh == u1 || a1.calls.Load() != 2 {
		t.Fatalf("refresh = %q (calls %d)", fresh, a1.calls.Load())
	}
	// A third instance (fresh process) sees the refreshed URL.
	h3 := newCacheHandler(&resolveCountAdapter{}, s)
	if got := postResolve(t, h3, "/api/resolve/fakecache/video/1/a-1"); got != fresh {
		t.Fatalf("restart got %q, want refreshed %q", got, fresh)
	}
	var refreshedAt, maxExp time.Time
	if err := s.QueryRow(ctx, `SELECT refreshed_at, max_expires_at FROM media_resolve_cache WHERE key=$1`, "resolve:fakecache:video/1/a-1").Scan(&refreshedAt, &maxExp); err != nil {
		t.Fatal(err)
	}
	if d := maxExp.Sub(refreshedAt); d != media.DefaultResolveMaxAge {
		t.Fatalf("max_expires_at - refreshed_at = %s, want the max age", d)
	}
}

func newCacheHandler(a media.Adapter, s *Store) *media.Handler {
	reg := media.NewRegistry()
	reg.Register(a)
	h := media.NewHandler(media.NewService(reg))
	h.UseResolveStore(s)
	return h
}

func postResolve(t *testing.T, h *media.Handler, path string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/resolve/{source}/{id...}", h.Resolve)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	var v media.ResolveResult
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &v) != nil || len(v.Streams) == 0 {
		t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
	}
	return v.Streams[0].URL
}
