package api

import (
	"avxxx/internal/medialink"
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
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
		// Each call can fetch a third-party provider page, so they are
		// limited per client address (mediaLimited).
		{"GET /api/search", srv.mediaLimited("media-lookup", limitMediaLookupIP, srv.media.Search)},
		{"GET /api/detail/{source}/{id...}", srv.mediaLimited("media-lookup", limitMediaLookupIP, srv.media.Detail)},
		{"POST /api/resolve/{source}/{id...}", srv.mediaLimited("media-resolve", limitMediaResolveIP, srv.media.Resolve)},
	}
}

// Per-address limits on the provider routes. The resolve cache absorbs
// repeated plays of one video, but any uncached (source, id), and any
// ?refresh=1 (once per DefaultResolveRefreshInterval per id, and ids are
// the caller's choice), is a live request to the provider; without a limit
// one client could make the server hammer a provider (and get its address
// blocked there). A player resolves once per play and refreshes only when a
// stream URL fails, far below these.
var (
	limitMediaResolveIP = rateRule{120, 10 * time.Minute}
	limitMediaRefreshIP = rateRule{20, 10 * time.Minute}
	limitMediaLookupIP  = rateRule{60, 10 * time.Minute}
)

// mediaLimited applies rule per client address before h; a resolve with
// ?refresh=1 also counts against limitMediaRefreshIP.
func (srv *Server) mediaLimited(name string, rule rateRule, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := srv.ip(r)
		ok := srv.allow(r, name+":"+ip, rule)
		if ok && r.Method == http.MethodPost && r.URL.Query().Get("refresh") == "1" {
			ok = srv.allow(r, "media-refresh:"+ip, limitMediaRefreshIP)
		}
		if !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(rule.window/time.Second)))
			reply(w, nil, tooManyRequests("too many requests"))
			return
		}
		h(w, r)
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
// media_link_attempts backoff. With false, it stays unlinked until the job
// links it.
//
// A published release without a mapping is an expected state, not an error:
// it answers 200 {"source":…,"available":false} so no-source release pages
// do not log a console 404 (the web player reads it as 暂无视频资源). A found
// mapping carries "available":true. 404 is left for a slug that names no
// published release.
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
	if errors.Is(e, pgx.ErrNoRows) {
		// Tell "no mapping yet" from "no such release".
		if _, _, err := srv.playback.ReleaseCodesForPlayback(r.Context(), slug); err != nil {
			reply(w, nil, err)
			return
		}
		reply(w, map[string]any{"source": source, "available": false}, nil)
		return
	}
	if e == nil && v != nil {
		v["available"] = true
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
