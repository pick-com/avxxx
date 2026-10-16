package api

import (
	"avxxx/internal/medialink"
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// playbackLinks is the subset of *store.Store that GET
// /api/releases/{slug}/playback and its on-demand linking need.
type playbackLinks interface {
	ReleasePlayback(ctx context.Context, slug, source string) (map[string]any, error)
	ReleaseCodesForPlayback(ctx context.Context, slug string) (int64, []string, error)
	MediaLinkAttempt(ctx context.Context, releaseID int64, provider string) (*medialink.Attempt, error)
	MediaLinkProviderBackoff(ctx context.Context, provider string) (time.Time, error)
	medialink.LinkStore
}

// onDemandMaxWait bounds how long a playback request waits for the
// per-process provider rate limit before giving up (404, try again later).
const onDemandMaxWait = 3 * time.Second

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

// releasePlayback returns the release's playback mapping for ?source=
// (default javtiful). Mappings are normally created offline by the
// media_link job (internal/medialink); with MEDIA_LINK_ON_DEMAND=true (the
// default) a missing one is searched for here once, honoring the same
// media_link_attempts backoff. With false, it is 404 until the job links it.
func (srv *Server) releasePlayback(w http.ResponseWriter, r *http.Request) {
	if srv.playback == nil {
		reply(w, nil, errNotFound)
		return
	}
	source := strings.ToLower(r.URL.Query().Get("source"))
	if source == "" {
		source = "javtiful"
	}
	slug := r.PathValue("slug")
	v, e := srv.playback.ReleasePlayback(r.Context(), slug, source)
	if errors.Is(e, pgx.ErrNoRows) && srv.linkOnDemand && srv.autoLinkPlayback(r.Context(), slug, source) {
		v, e = srv.playback.ReleasePlayback(r.Context(), slug, source)
	}
	reply(w, v, e)
}

// linkInFlight dedupes concurrent on-demand searches for one release.
var linkInFlight sync.Map

// autoLinkPlayback links one release on demand, like one step of the
// media_link job: only when its attempt is missing or due, the provider is
// not backing off and the per-process rate limit allows a request now. It
// returns true when a link was stored.
func (srv *Server) autoLinkPlayback(ctx context.Context, slug, source string) bool {
	if _, ok := medialink.Providers[source]; !ok || srv.linker == nil {
		return false
	}
	releaseID, codes, err := srv.playback.ReleaseCodesForPlayback(ctx, slug)
	if err != nil || len(codes) == 0 {
		return false
	}
	prev, err := srv.playback.MediaLinkAttempt(ctx, releaseID, source)
	if err != nil {
		log.Printf("media link attempt %d/%s: %v", releaseID, source, err)
		return false
	}
	now := time.Now()
	if srv.linker.Now != nil {
		now = srv.linker.Now()
	}
	if !medialink.Due(prev, now) {
		return false
	}
	if until, err := srv.playback.MediaLinkProviderBackoff(ctx, source); err != nil || now.Before(until) {
		return false
	}
	key := source + "\x00" + slug
	if _, busy := linkInFlight.LoadOrStore(key, struct{}{}); busy {
		return false
	}
	defer linkInFlight.Delete(key)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := srv.linker.Link(ctx, source, medialink.Candidate{ReleaseID: releaseID, Codes: codes, Prev: prev})
	if err != nil {
		if !errors.Is(err, medialink.ErrBusy) {
			log.Printf("on-demand media link %s/%s: %v", source, slug, err)
		}
		return false
	}
	return out.Linked()
}
