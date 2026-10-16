package media

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	service *Service
	cache   *ResultCache
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service, cache: NewResultCache()}
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
	v, err := h.service.Resolve(r.Context(), r.PathValue("source"), r.PathValue("id"))
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, v)
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

// MatchByCode searches a source for a product code (e.g. JUQ-743) and returns
// the result whose id ends in exactly that code ("video/59813/juq-743"), so
// variants such as "snos-397-reducing-mosaic" never match. ok is false when
// the source has no exact match.
func (h *Handler) MatchByCode(ctx context.Context, source, code string) (SearchResult, bool, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" {
		return SearchResult{}, false, nil
	}
	items, _, err := h.service.Search(ctx, source, code, 1)
	if err != nil {
		return SearchResult{}, false, err
	}
	return ExactCodeMatch(items, code)
}

// ExactCodeMatch picks the single item whose last path segment equals code.
func ExactCodeMatch(items []SearchResult, code string) (SearchResult, bool, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	var hit *SearchResult
	for i := range items {
		id := strings.ToLower(strings.TrimSuffix(items[i].ID, "/"))
		if id == code || strings.HasSuffix(id, "/"+code) {
			if hit != nil && hit.ID != items[i].ID {
				return SearchResult{}, false, nil // ambiguous: leave it for review
			}
			hit = &items[i]
		}
	}
	if hit == nil {
		return SearchResult{}, false, nil
	}
	return *hit, true, nil
}
