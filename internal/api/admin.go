package api

import (
	"avxxx/internal/model"
	"avxxx/internal/scraper"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

func (srv *Server) adminRoutes() []route {
	return []route{
		{"POST /api/admin/releases/{id}/media-link", admin(srv.linkMediaToRelease)},
		{"POST /api/admin/media-links/by-identifier", admin(srv.linkMediaByIdentifier)},
		{"GET /api/admin/sources", admin(srv.sources)},
		{"GET /api/admin/jobs", admin(srv.jobs)},
		{"GET /api/admin/crawl-policies", admin(srv.crawlPolicies)},
		{"POST /api/admin/media-assets", admin(srv.saveMediaAsset)},
		{"GET /api/admin/identity-candidates", admin(srv.identityCandidates)},
		{"PUT /api/admin/sources/{id}/crawl-policy", admin(srv.upsertCrawlPolicy)},
		{"GET /api/admin/conflicts", admin(srv.conflicts)},
		{"POST /api/admin/conflicts/{id}/resolve", admin(srv.resolveConflict)},
		{"POST /api/admin/sources", admin(srv.addSource)},
		{"POST /api/admin/sources/{id}/jobs", admin(srv.enqueue)},
	}
}

func (srv *Server) linkMediaToRelease(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var input struct {
		Source   string `json:"source"`
		MediaID  string `json:"media_id"`
		MediaURL string `json:"media_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		reply(w, nil, err)
		return
	}
	if input.Source == "" || input.MediaID == "" {
		reply(w, nil, errors.New("source and media_id are required"))
		return
	}
	err = srv.store.LinkMediaToRelease(r.Context(), id, input.Source, input.MediaID, input.MediaURL)
	reply(w, map[string]any{"linked": err == nil}, err)
}

func (srv *Server) linkMediaByIdentifier(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Source     string `json:"source"`
		MediaID    string `json:"media_id"`
		MediaURL   string `json:"media_url"`
		Identifier string `json:"identifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		reply(w, nil, err)
		return
	}
	if input.Source == "" || input.MediaID == "" || input.Identifier == "" {
		reply(w, nil, errors.New("source, media_id and identifier are required"))
		return
	}
	linked, err := srv.store.LinkMediaByIdentifier(r.Context(), input.Source, input.MediaID, input.MediaURL, input.Identifier)
	reply(w, map[string]any{"linked": linked}, err)
}

func (srv *Server) sources(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Sources(r.Context())
	reply(w, v, e)
}

func (srv *Server) jobs(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.Jobs(r.Context())
	reply(w, v, e)
}

func (srv *Server) crawlPolicies(w http.ResponseWriter, r *http.Request) {
	v, e := srv.store.CrawlPolicies(r.Context())
	reply(w, v, e)
}

func (srv *Server) saveMediaAsset(w http.ResponseWriter, r *http.Request) {
	var asset map[string]any
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&asset) != nil || asset["entity_type"] == nil || asset["entity_id"] == nil || asset["asset_type"] == nil || asset["object_key"] == nil || asset["content_sha256"] == nil || asset["mime_type"] == nil {
		http.Error(w, "required asset fields are missing", 400)
		return
	}
	if asset["storage_provider"] == nil {
		asset["storage_provider"] = "s3"
	}
	if asset["is_original"] == nil {
		asset["is_original"] = true
	}
	reply(w, map[string]bool{"ok": true}, srv.store.SaveMediaAsset(r.Context(), asset))
}

func (srv *Server) identityCandidates(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.IdentityCandidates(r.Context(), r.URL.Query().Get("entity_type"), limit)
	reply(w, v, e)
}

func (srv *Server) upsertCrawlPolicy(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	var req struct {
		DiscoveryInterval     string `json:"discovery_interval"`
		DetailRefreshInterval string `json:"detail_refresh_interval"`
		MinRequestInterval    string `json:"min_request_interval"`
		DailyPageBudget       int64  `json:"daily_page_budget"`
		DailyByteBudget       int64  `json:"daily_byte_budget"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.MinRequestInterval == "" {
		http.Error(w, "min_request_interval is required", 400)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.store.UpsertCrawlPolicy(r.Context(), id, req.DiscoveryInterval, req.DetailRefreshInterval, req.MinRequestInterval, req.DailyPageBudget, req.DailyByteBudget))
}

func (srv *Server) conflicts(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	v, e := srv.store.PendingConflicts(r.Context(), limit)
	reply(w, v, e)
}

func (srv *Server) resolveConflict(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	var req struct {
		SourceRecordID int64           `json:"source_record_id"`
		Value          json.RawMessage `json:"value"`
		Note           string          `json:"note"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&req) != nil || len(req.Value) == 0 {
		http.Error(w, "value is required", 400)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.store.ResolveConflict(r.Context(), id, req.SourceRecordID, req.Value, req.Note))
}

func (srv *Server) addSource(w http.ResponseWriter, r *http.Request) {
	var x model.Source
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if json.NewDecoder(r.Body).Decode(&x) != nil || strings.TrimSpace(x.Name) == "" || !scraper.ValidURL(x.URL) {
		http.Error(w, "name and HTTP(S) feed URL required", 400)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.store.AddSource(r.Context(), x))
}

func (srv *Server) enqueue(w http.ResponseWriter, r *http.Request) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil || id <= 0 {
		http.Error(w, "invalid id", 400)
		return
	}
	reply(w, map[string]bool{"ok": true}, srv.store.Enqueue(r.Context(), id))
}
