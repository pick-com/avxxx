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
	mux.HandleFunc("GET /api/items", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.Items(r.Context(), r.URL.Query().Get("q"))
		reply(w, v, e)
	})
	// Source-specific search/detail/resolve routes are ready for Kodi adapters.
	// Adapters are intentionally not registered until their source implementation
	// and authorization requirements are reviewed.
	mux.HandleFunc("GET /api/search", mediaHandler.Search)
	mux.HandleFunc("GET /api/detail/{source}/{id...}", mediaHandler.Detail)
	mux.HandleFunc("POST /api/resolve/{source}/{id...}", mediaHandler.Resolve)
	mux.HandleFunc("GET /api/admin/sources", admin(func(w http.ResponseWriter, r *http.Request) { v, e := s.Sources(r.Context()); reply(w, v, e) }))
	mux.HandleFunc("GET /api/admin/jobs", admin(func(w http.ResponseWriter, r *http.Request) { v, e := s.Jobs(r.Context()); reply(w, v, e) }))
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
