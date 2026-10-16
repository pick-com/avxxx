package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

func (srv *Server) mediaRoutes() []route {
	return []route{
		{"GET /api/releases/{slug}/sources", srv.releaseSources},
		{"GET /api/releases/{slug}/playback", srv.releasePlayback},
		// Source-specific search/detail/resolve routes are ready for Kodi adapters.
		// Adapters are intentionally not registered until their source implementation
		// and authorization requirements are reviewed.
		{"GET /api/search", srv.media.Search},
		{"GET /api/detail/{source}/{id...}", srv.media.Detail},
		{"POST /api/resolve/{source}/{id...}", srv.media.Resolve},
	}
}

func (srv *Server) releaseSources(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.ReleaseSources(r.Context(), r.PathValue("slug"))
	reply(w, map[string]any{"items": v}, e)
}

// autoLinkSources are playback providers whose search can be matched to a
// release by its DVD code when no release_media_sources row exists yet.
var autoLinkSources = map[string]string{"javtiful": "https://javtiful.com/"}

func (srv *Server) releasePlayback(w http.ResponseWriter, r *http.Request) {
	source := strings.ToLower(r.URL.Query().Get("source"))
	if source == "" {
		source = "javtiful"
	}
	slug := r.PathValue("slug")
	v, e := srv.store.ReleasePlayback(r.Context(), slug, source)
	if errors.Is(e, pgx.ErrNoRows) && srv.autoLinkPlayback(r.Context(), slug, source) {
		v, e = srv.store.ReleasePlayback(r.Context(), slug, source)
	}
	reply(w, v, e)
}

// playbackMisses remembers slugs a provider had no exact match for, so a
// popular release without a source doesn't trigger a provider search on
// every page view.
var playbackMisses = struct {
	sync.Mutex
	until map[string]time.Time
}{until: map[string]time.Time{}}

const playbackMissTTL = 6 * time.Hour

// autoLinkPlayback searches the provider for each of the release's DVD codes
// and stores the first exact match. It returns true when a link was stored.
// Ambiguous or missing matches are left alone (and remembered for a while).
func (srv *Server) autoLinkPlayback(ctx context.Context, slug, source string) bool {
	base, ok := autoLinkSources[source]
	if !ok || srv.media == nil || srv.store == nil {
		return false
	}
	key := source + "\x00" + slug
	playbackMisses.Lock()
	if t, miss := playbackMisses.until[key]; miss && time.Now().Before(t) {
		playbackMisses.Unlock()
		return false
	}
	playbackMisses.Unlock()

	releaseID, codes, err := srv.store.ReleaseCodesForPlayback(ctx, slug)
	if err != nil || len(codes) == 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, code := range codes {
		hit, found, err := srv.media.MatchByCode(ctx, source, code)
		if err != nil {
			return false // provider error: don't cache, try again next time
		}
		if found {
			return srv.store.LinkMediaToRelease(ctx, releaseID, source, hit.ID, base+strings.TrimPrefix(hit.ID, "/")) == nil
		}
	}
	playbackMisses.Lock()
	playbackMisses.until[key] = time.Now().Add(playbackMissTTL)
	playbackMisses.Unlock()
	return false
}
