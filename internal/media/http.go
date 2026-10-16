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
	// resolveTTL caches successful resolve results per (source, id); 0
	// disables it. Stream URLs carry expiring tokens, so keep it short.
	resolveTTL time.Duration
}

// DefaultResolveTTL is MEDIA_RESOLVE_TTL's default.
const DefaultResolveTTL = 5 * time.Minute

// NewHandler builds the HTTP handler. MEDIA_RESOLVE_TTL (a Go duration,
// default 5m, 0 disables) sets how long a successful resolve is reused.
func NewHandler(service *Service) *Handler {
	return &Handler{service: service, cache: NewResultCache(), resolveTTL: ResolveTTLFromEnv(os.Getenv("MEDIA_RESOLVE_TTL"))}
}

// ResolveTTLFromEnv parses MEDIA_RESOLVE_TTL: empty or invalid gives
// DefaultResolveTTL, zero or negative disables the cache.
func ResolveTTLFromEnv(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return DefaultResolveTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("invalid MEDIA_RESOLVE_TTL %q, using %s", v, DefaultResolveTTL)
		return DefaultResolveTTL
	}
	return max(d, 0)
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

func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	v, err := h.resolve(r.Context(), r.PathValue("source"), r.PathValue("id"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, v)
}

// resolve returns a cached result for (source, id) while it is fresh, so
// repeated plays don't refetch the provider page. Errors are never cached,
// and a result never outlives its streams' earliest expires_at.
func (h *Handler) resolve(ctx context.Context, source, id string) (*ResolveResult, error) {
	key := "resolve:" + strings.ToLower(source) + "\x00" + id
	if h.resolveTTL > 0 {
		if v, ok := h.cache.Get(key); ok {
			return v.(*ResolveResult), nil
		}
	}
	v, err := h.service.Resolve(ctx, source, id)
	if err != nil {
		return nil, err
	}
	if ttl := resolveCacheTTL(v, h.resolveTTL, h.cache.now()); ttl > 0 {
		h.cache.Set(key, v, ttl)
	}
	return v, nil
}

// resolveCacheTTL caps ttl so a cached result expires at least 30 s before
// the earliest stream expiry.
func resolveCacheTTL(v *ResolveResult, ttl time.Duration, now time.Time) time.Duration {
	if v == nil || ttl <= 0 {
		return 0
	}
	for _, list := range [][]Stream{v.Streams, v.Subtitles} {
		for _, st := range list {
			if st.ExpiresAt != nil {
				ttl = min(ttl, st.ExpiresAt.Sub(now)-30*time.Second)
			}
		}
	}
	return max(ttl, 0)
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
