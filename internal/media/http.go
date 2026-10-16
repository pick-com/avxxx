package media

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Handler struct {
	service *Service
	cache   *ResultCache
	// resolves caches successful resolve results per (source, id)
	// (resolve_cache.go); MEDIA_RESOLVE_TTL 0 disables it.
	resolves *resolveCache
}

// DefaultResolveTTL is MEDIA_RESOLVE_TTL's default.
const DefaultResolveTTL = 5 * time.Minute

// NewHandler builds the HTTP handler. MEDIA_RESOLVE_TTL (a Go duration,
// default 5m, 0 disables) is the sliding reuse window of a successful
// resolve; MEDIA_RESOLVE_MAX_AGE (default 30m) caps a result's total age
// when its streams' expiry is unknown.
func NewHandler(service *Service) *Handler {
	ttl := ResolveTTLFromEnv(os.Getenv("MEDIA_RESOLVE_TTL"))
	maxAge := ResolveMaxAgeFromEnv(os.Getenv("MEDIA_RESOLVE_MAX_AGE"))
	return &Handler{service: service, cache: NewResultCache(), resolves: newResolveCache(ttl, maxAge)}
}

// UseResolveStore shares resolve results through st (Postgres
// media_resolve_cache) behind the in-process cache. Call before serving.
func (h *Handler) UseResolveStore(st ResolveStore) {
	if h != nil && h.resolves != nil {
		h.resolves.store = st
	}
}

// ResolveTTLFromEnv parses MEDIA_RESOLVE_TTL: empty or invalid gives
// DefaultResolveTTL, zero or negative disables the cache.
func ResolveTTLFromEnv(v string) time.Duration {
	return max(durationFromEnv("MEDIA_RESOLVE_TTL", v, DefaultResolveTTL), 0)
}

// ResolveMaxAgeFromEnv parses MEDIA_RESOLVE_MAX_AGE: empty, invalid, zero
// or negative gives DefaultResolveMaxAge.
func ResolveMaxAgeFromEnv(v string) time.Duration {
	if d := durationFromEnv("MEDIA_RESOLVE_MAX_AGE", v, DefaultResolveMaxAge); d > 0 {
		return d
	}
	return DefaultResolveMaxAge
}

func durationFromEnv(name, v string, def time.Duration) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid %s %q, using %s", name, v, def)
		return def
	}
	return d
}

// Service is the media service behind the handler (nil-safe).
func (h *Handler) Service() *Service {
	if h == nil {
		return nil
	}
	return h.service
}

func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q, source := r.URL.Query().Get("q"), r.URL.Query().Get("source")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page == 0 {
		page = 1
	}
	key := "search:" + source + ":" + q + ":" + strconv.Itoa(page)
	if v, ok := h.cache.Get(key); ok {
		writeJSON(w, v)
		return
	}
	items, more, err := h.service.Search(r.Context(), source, q, page)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	v := map[string]any{"items": items, "page": page, "has_more": more}
	h.cache.Set(key, v, timeMinute)
	writeJSON(w, v)
}

func (h *Handler) Detail(w http.ResponseWriter, r *http.Request) {
	v, err := h.service.Detail(r.Context(), r.PathValue("source"), r.PathValue("id"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, v)
}

// Resolve serves POST /api/resolve/{source}/{id...}. ?refresh=1 asks for a
// fresh provider read because a cached stream URL failed to play.
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	v, err := h.resolve(r.Context(), r.PathValue("source"), r.PathValue("id"), refresh)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, v)
}

// resolve returns a cached result for (source, id) while it is fresh, so
// repeated plays don't refetch the provider page (resolve_cache.go). Errors
// are never cached. refresh evicts the entry and re-resolves, at most once
// per DefaultResolveRefreshInterval per (source, id); a refused refresh is
// an ordinary lookup.
func (h *Handler) resolve(ctx context.Context, source, id string, refresh bool) (*ResolveResult, error) {
	c := h.resolves
	if !c.enabled() {
		return h.service.Resolve(ctx, source, id)
	}
	key := "resolve:" + strings.ToLower(source) + "\x00" + id
	if refresh && c.allowRefresh(key) {
		c.evict(ctx, key)
	} else if v, ok := c.get(ctx, key); ok {
		return v, nil
	}
	v, err := h.service.Resolve(ctx, source, id)
	if err != nil {
		return nil, err
	}
	c.put(ctx, key, v)
	return v, nil
}

const timeMinute = 60 * 1e9

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func writeMediaError(w http.ResponseWriter, err error) {
	code, status := "media_error", http.StatusBadGateway
	if errors.Is(err, ErrAdapterNotFound) {
		code, status = "source_not_configured", http.StatusNotImplemented
	}
	if err.Error() == "query is required" {
		code, status = "invalid_query", http.StatusBadRequest
	}
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": map[string]any{"code": code, "message": err.Error()}})
}

// ExactCodeCandidates returns the distinct ids of items whose last path
// segment equals code ("video/59813/juq-743" for JUQ-743), so variants such
// as "snos-397-reducing-mosaic" never match. More than one id is ambiguous.
func ExactCodeCandidates(items []SearchResult, code string) []string {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, it := range items {
		id := strings.ToLower(strings.TrimSuffix(it.ID, "/"))
		if (id == code || strings.HasSuffix(id, "/"+code)) && !seen[it.ID] {
			seen[it.ID] = true
			out = append(out, it.ID)
		}
	}
	return out
}

// ExactCodeMatch picks the single item whose last path segment equals code.
// ok is false when there is no exact match or more than one (ambiguous:
// left for review).
func ExactCodeMatch(items []SearchResult, code string) (SearchResult, bool, error) {
	ids := ExactCodeCandidates(items, code)
	if len(ids) != 1 {
		return SearchResult{}, false, nil
	}
	for _, it := range items {
		if it.ID == ids[0] {
			return it, true, nil
		}
	}
	return SearchResult{}, false, nil
}
