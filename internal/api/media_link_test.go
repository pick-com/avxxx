package api

import (
	"avxxx/internal/media"
	"avxxx/internal/medialink"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// fakePlayback is an in-memory playbackLinks for one release "rel" (id 7).
type fakePlayback struct {
	codes    []string
	link     string
	attempt  *medialink.Attempt
	backoff  time.Time
	recorded int
}

func (f *fakePlayback) ReleasePlayback(_ context.Context, slug, _ string) (map[string]any, error) {
	if f.link == "" {
		return nil, pgx.ErrNoRows
	}
	return map[string]any{"source": "javtiful", "source_key": f.link}, nil
}
func (f *fakePlayback) ReleaseCodesForPlayback(context.Context, string) (int64, []string, error) {
	return 7, f.codes, nil
}
func (f *fakePlayback) MediaLinkAttempt(context.Context, int64, string) (*medialink.Attempt, error) {
	return f.attempt, nil
}
func (f *fakePlayback) MediaLinkProviderBackoff(context.Context, string) (time.Time, error) {
	return f.backoff, nil
}
func (f *fakePlayback) ClaimMediaLink(_ context.Context, releaseID int64, _, mediaID, _ string) (int64, error) {
	f.link = mediaID
	return releaseID, nil
}
func (f *fakePlayback) RecordMediaLinkAttempt(_ context.Context, a medialink.Attempt) error {
	f.recorded++
	f.attempt = &a
	return nil
}

type fakeProvider struct {
	items []media.SearchResult
	err   error
	calls int
}

func (p *fakeProvider) Search(context.Context, string, string, int) ([]media.SearchResult, bool, error) {
	p.calls++
	return p.items, false, p.err
}

func playbackServer(fp *fakePlayback, prov *fakeProvider, onDemand bool, now time.Time) http.Handler {
	srv := &Server{playback: fp, linkOnDemand: onDemand,
		linker: &medialink.Linker{Store: fp, Search: prov, Now: func() time.Time { return now }}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/releases/{slug}/playback", srv.releasePlayback)
	return mux
}

func getPlayback(h http.Handler) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/releases/rel/playback", nil))
	return rec.Code
}

func TestPlaybackOnDemandLinks(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fp := &fakePlayback{codes: []string{"juq-743"}}
	prov := &fakeProvider{items: []media.SearchResult{{ID: "video/59813/juq-743"}}}
	h := playbackServer(fp, prov, true, now)
	if code := getPlayback(h); code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	if fp.link != "video/59813/juq-743" || prov.calls != 1 || fp.attempt.Status != medialink.StatusFound {
		t.Fatalf("link %q calls %d attempt %+v", fp.link, prov.calls, fp.attempt)
	}
	getPlayback(h)
	if prov.calls != 1 {
		t.Fatal("a linked release must not search again")
	}
}

func TestPlaybackOnDemandHonorsAttempts(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fp := &fakePlayback{codes: []string{"abc-001"}}
	prov := &fakeProvider{}
	h := playbackServer(fp, prov, true, now)

	// Miss: recorded as not_found for 7 days; later views don't search.
	if code := getPlayback(h); code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", code)
	}
	if prov.calls != 1 || fp.attempt.Status != medialink.StatusNotFound || fp.attempt.NextTryAt.Sub(now) != medialink.NotFoundRetry {
		t.Fatalf("calls %d attempt %+v", prov.calls, fp.attempt)
	}
	for range 3 {
		getPlayback(h)
	}
	if prov.calls != 1 {
		t.Fatalf("not-due attempt searched again: %d calls", prov.calls)
	}

	// Ambiguous (never auto-retried) also blocks on-demand searches.
	fp.attempt = &medialink.Attempt{Status: medialink.StatusAmbiguous}
	getPlayback(h)
	if prov.calls != 1 {
		t.Fatal("ambiguous attempt must not be searched on demand")
	}

	// Due again: one search, and a provider-wide backoff blocks it.
	past := now.Add(-time.Minute)
	fp.attempt = &medialink.Attempt{Status: medialink.StatusError, Attempts: 2, NextTryAt: &past}
	fp.backoff = now.Add(time.Hour)
	getPlayback(h)
	if prov.calls != 1 {
		t.Fatal("provider backoff must block on-demand searches")
	}
	fp.backoff = time.Time{}
	prov.err = &media.StatusError{Source: "javtiful", Code: 503}
	getPlayback(h)
	if prov.calls != 2 || fp.attempt.Status != medialink.StatusError || fp.attempt.Attempts != 3 || fp.attempt.NextTryAt.Sub(now) != 40*time.Minute {
		t.Fatalf("calls %d attempt %+v", prov.calls, fp.attempt)
	}
}

func TestPlaybackOnDemandDisabled(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fp := &fakePlayback{codes: []string{"juq-743"}}
	prov := &fakeProvider{items: []media.SearchResult{{ID: "video/59813/juq-743"}}}
	h := playbackServer(fp, prov, false, now)
	if code := getPlayback(h); code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 (offline only)", code)
	}
	if prov.calls != 0 || fp.recorded != 0 {
		t.Fatal("MEDIA_LINK_ON_DEMAND=false must not search or record")
	}
	fp.link = "video/59813/juq-743" // the job linked it
	if code := getPlayback(h); code != http.StatusOK {
		t.Fatalf("status %d after offline link, want 200", code)
	}
}

func TestPlaybackOnDemandRateLimited(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	fp := &fakePlayback{codes: []string{"juq-743"}}
	prov := &fakeProvider{}
	srv := &Server{playback: fp, linkOnDemand: true, linker: &medialink.Linker{Store: fp, Search: prov,
		Pacer:   &medialink.Pacer{Interval: time.Hour, Now: func() time.Time { return now }, Sleep: func(context.Context, time.Duration) error { return nil }},
		MaxWait: onDemandMaxWait, Now: func() time.Time { return now }}}
	if srv.autoLinkPlayback(context.Background(), "rel", "javtiful") || prov.calls != 1 {
		t.Fatal("first on-demand search should run")
	}
	fp.attempt = nil // pretend it is due again
	if srv.autoLinkPlayback(context.Background(), "rel", "javtiful") || prov.calls != 1 || fp.recorded != 1 {
		t.Fatal("per-process rate limit must skip the search without recording an attempt")
	}
	if srv.autoLinkPlayback(context.Background(), "rel", "nope") {
		t.Fatal("unknown providers are never auto-linked")
	}
}
