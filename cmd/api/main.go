package main

import (
	"avxxx/internal/api"
	"avxxx/internal/media"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	logger := obs.SetupLogging("api")
	ctx := context.Background()
	s, e := store.Open(ctx)
	if e != nil {
		obs.Fatal("open store", e)
	}
	defer s.Close()
	obs.RegisterPool(s.Stat)
	obs.RegisterHealth(s.Ping)
	// /metrics on an internal listener, never the public API port.
	if e := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9090")); e != nil {
		obs.Fatal("metrics listener", e)
	}
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
	handler := obs.Middleware(api.New(s, mediaHandler), logger)
	addr := os.Getenv("API_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	slog.Info("api listening", "listen", addr)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
		ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn)}
	obs.Fatal("api server stopped", srv.ListenAndServe())
}
