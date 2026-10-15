package main

import (
	"avxxx/internal/media"
	"avxxx/internal/model"
	"avxxx/internal/scraper"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	ctx := context.Background()
	s, e := store.Open(ctx)
	if e != nil {
		log.Fatal(e)
	}
	defer s.Close()
	mux := http.NewServeMux()
	mediaRegistry := media.NewRegistry()
	mediaRegistry.Register(media.NewJavtifulAdapter())
	mediaRegistry.Register(media.NewSextbAdapter())
	mediaRegistry.Register(media.NewABxxxAdapter())
	mediaRegistry.Register(media.NewKVSAdapter("camstreams", "https://camstreams.tv", "/videos/"))
	mediaRegistry.Register(media.NewKVSAdapter("camwhores", "https://www.camwhores.tv", "/videos/"))
	mediaRegistry.Register(media.NewAvjoyAdapter())
	mediaRegistry.Register(media.NewEpornerAdapter())
	mediaRegistry.Register(media.NewFreeomovieAdapter())
	mediaRegistry.Register(media.NewHclipsAdapter())
	mediaRegistry.Register(media.NewJavsubbedAdapter())
	mediaRegistry.Register(media.NewPornbusyAdapter())
	mediaRegistry.Register(media.NewPornflipAdapter())
	mediaRegistry.Register(media.NewPunishworldAdapter())
	mediaRegistry.Register(media.NewSextvxAdapter())
	mediaRegistry.Register(media.NewShooshTimeAdapter())
	mediaRegistry.Register(media.NewKVSAdapter("pornslash", "https://www.pornslash.com", "/videos/"))
	mediaRegistry.Register(media.NewJavguruAdapter())
	mediaRegistry.Register(media.NewJavhdpornAdapter())
	mediaRegistry.Register(media.NewMissavAdapter())
	mediaHandler := media.NewHandler(media.NewService(mediaRegistry))
	reply := func(w http.ResponseWriter, v any, e error) {
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			log.Print(e)
			code := http.StatusInternalServerError
			if errors.Is(e, pgx.ErrNoRows) {
				code = http.StatusNotFound
			}
			if strings.Contains(e.Error(), "source disabled") {
				code = http.StatusConflict
			}
			if strings.Contains(e.Error(), "duplicate key") {
				code = http.StatusConflict
			}
			http.Error(w, `{"error":"request failed"}`, code)
			return
		}
		json.NewEncoder(w).Encode(v)
	}
	admin := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := os.Getenv("ADMIN_TOKEN")
			if token == "" || r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", 401)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		e := s.Ping(r.Context())
		if e != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		reply(w, map[string]bool{"ok": e == nil}, e)
	})
	mux.HandleFunc("GET /api/live", func(w http.ResponseWriter, r *http.Request) { reply(w, map[string]bool{"ok": true}, nil) })
	mux.HandleFunc("POST /api/auth/register", func(w http.ResponseWriter, r *http.Request) { var in struct{ Email string `json:"email"`; Password string `json:"password"`; DisplayName string `json:"display_name"` }; _ = json.NewDecoder(r.Body).Decode(&in); v,e:=s.Register(r.Context(),in.Email,in.Password,in.DisplayName); reply(w,v,e) })
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) { var in struct{ Email string `json:"email"`; Password string `json:"password"` }; _ = json.NewDecoder(r.Body).Decode(&in); v,e:=s.Login(r.Context(),in.Email,in.Password); reply(w,v,e) })
	mux.HandleFunc("GET /api/auth/me", func(w http.ResponseWriter, r *http.Request) { v,e:=s.UserByToken(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer ")); reply(w,v,e) })
	userID := func(r *http.Request) (int64,error) { return s.UserIDByToken(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"),"Bearer ")) }
	mux.HandleFunc("GET /api/me/watch-history", func(w http.ResponseWriter, r *http.Request) { id,e:=userID(r); if e!=nil { reply(w,nil,errors.New("unauthorized")); return }; v,e:=s.WatchHistory(r.Context(),id); reply(w,map[string]any{"items":v},e) })
	mux.HandleFunc("POST /api/me/watch-history", func(w http.ResponseWriter, r *http.Request) { id,e:=userID(r); if e!=nil { reply(w,nil,errors.New("unauthorized")); return }; var in struct{ Slug string `json:"slug"`; Progress int `json:"progress_seconds"`; Duration int `json:"duration_seconds"` }; _=json.NewDecoder(r.Body).Decode(&in); e=s.SaveWatch(r.Context(),id,in.Slug,in.Progress,in.Duration); reply(w,map[string]bool{"saved":e==nil},e) })
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Items(r.Context(), r.URL.Query().Get("q"))
		reply(w, v, e)
	})
	mux.HandleFunc("GET /api/performers", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.Performers(r.Context(), r.URL.Query().Get("q"), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/performers/{slug}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.PerformerDetail(r.Context(), r.PathValue("slug"))
		reply(w, v, e)
	})
	mux.HandleFunc("GET /api/performers/{slug}/releases", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.PerformerReleases(r.Context(), r.PathValue("slug"), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/releases", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.Releases(r.Context(), r.URL.Query().Get("q"), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/releases/{slug}", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ReleaseBySlug(r.Context(), r.PathValue("slug"))
		reply(w, v, e)
	})
	mux.HandleFunc("GET /api/releases/{slug}/performers", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ReleasePerformers(r.Context(), r.PathValue("slug"))
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/releases/{slug}/sources", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.ReleaseSources(r.Context(), r.PathValue("slug"))
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/releases/{slug}/playback", func(w http.ResponseWriter, r *http.Request) {
		source := r.URL.Query().Get("source")
		if source == "" {
			source = "javtiful"
		}
		v, e := s.ReleasePlayback(r.Context(), r.PathValue("slug"), source)
		reply(w, v, e)
	})
	mux.HandleFunc("POST /api/admin/releases/{id}/media-link", admin(func(w http.ResponseWriter, r *http.Request) {
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
		err = s.LinkMediaToRelease(r.Context(), id, input.Source, input.MediaID, input.MediaURL)
		reply(w, map[string]any{"linked": err == nil}, err)
	}))
	mux.HandleFunc("POST /api/admin/media-links/by-identifier", admin(func(w http.ResponseWriter, r *http.Request) {
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
		linked, err := s.LinkMediaByIdentifier(r.Context(), input.Source, input.MediaID, input.MediaURL, input.Identifier)
		reply(w, map[string]any{"linked": linked}, err)
	}))
	mux.HandleFunc("GET /api/tags", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.Tags(r.Context(), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/makers", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.Makers(r.Context(), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	mux.HandleFunc("GET /api/catalog/search", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.CatalogSearch(r.Context(), r.URL.Query().Get("q"), limit)
		reply(w, map[string]any{"items": v}, e)
	})
	// Source-specific search/detail/resolve routes are ready for Kodi adapters.
	// Adapters are intentionally not registered until their source implementation
	// and authorization requirements are reviewed.
	mux.HandleFunc("GET /api/search", mediaHandler.Search)
	mux.HandleFunc("GET /api/detail/{source}/{id...}", mediaHandler.Detail)
	mux.HandleFunc("POST /api/resolve/{source}/{id...}", mediaHandler.Resolve)
	mux.HandleFunc("GET /api/admin/sources", admin(func(w http.ResponseWriter, r *http.Request) { v, e := s.Sources(r.Context()); reply(w, v, e) }))
	mux.HandleFunc("GET /api/admin/jobs", admin(func(w http.ResponseWriter, r *http.Request) { v, e := s.Jobs(r.Context()); reply(w, v, e) }))
	mux.HandleFunc("GET /api/admin/crawl-policies", admin(func(w http.ResponseWriter, r *http.Request) { v, e := s.CrawlPolicies(r.Context()); reply(w, v, e) }))
	mux.HandleFunc("POST /api/admin/media-assets", admin(func(w http.ResponseWriter, r *http.Request) {
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
		reply(w, map[string]bool{"ok": true}, s.SaveMediaAsset(r.Context(), asset))
	}))
	mux.HandleFunc("GET /api/admin/identity-candidates", admin(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.IdentityCandidates(r.Context(), r.URL.Query().Get("entity_type"), limit)
		reply(w, v, e)
	}))
	mux.HandleFunc("PUT /api/admin/sources/{id}/crawl-policy", admin(func(w http.ResponseWriter, r *http.Request) {
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
		reply(w, map[string]bool{"ok": true}, s.UpsertCrawlPolicy(r.Context(), id, req.DiscoveryInterval, req.DetailRefreshInterval, req.MinRequestInterval, req.DailyPageBudget, req.DailyByteBudget))
	}))
	mux.HandleFunc("GET /api/admin/conflicts", admin(func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		v, e := s.PendingConflicts(r.Context(), limit)
		reply(w, v, e)
	}))
	mux.HandleFunc("POST /api/admin/conflicts/{id}/resolve", admin(func(w http.ResponseWriter, r *http.Request) {
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
		reply(w, map[string]bool{"ok": true}, s.ResolveConflict(r.Context(), id, req.SourceRecordID, req.Value, req.Note))
	}))
	mux.HandleFunc("POST /api/admin/sources", admin(func(w http.ResponseWriter, r *http.Request) {
		var x model.Source
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		if json.NewDecoder(r.Body).Decode(&x) != nil || strings.TrimSpace(x.Name) == "" || !scraper.ValidURL(x.URL) {
			http.Error(w, "name and HTTP(S) feed URL required", 400)
			return
		}
		reply(w, map[string]bool{"ok": true}, s.AddSource(r.Context(), x))
	}))
	mux.HandleFunc("POST /api/admin/sources/{id}/jobs", admin(func(w http.ResponseWriter, r *http.Request) {
		id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if e != nil || id <= 0 {
			http.Error(w, "invalid id", 400)
			return
		}
		reply(w, map[string]bool{"ok": true}, s.Enqueue(r.Context(), id))
	}))
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	log.Printf("API on %s", addr)
	log.Fatal((&http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}).ListenAndServe())
}
