// Command notifier delivers notification_outbox rows (written by the
// scheduler's new-work fan-out) over Web Push and email (internal/notify).
//
// Channels are configured by environment (see notify.Config); with neither
// VAPID_* nor SMTP_* set it idles, so deploying it before a provider is
// chosen is harmless.
//
//	notifier             worker loop
//	notifier -once       deliver one batch and exit
//	notifier -gen-vapid  print a new VAPID key pair (env lines) and exit
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"avxxx/internal/notify"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"avxxx/internal/webpush"
)

func main() {
	once := flag.Bool("once", false, "deliver one batch and exit")
	gen := flag.Bool("gen-vapid", false, "print a new VAPID key pair and exit")
	flag.Parse()

	obs.SetupLogging("notifier")
	if *gen {
		pub, priv, err := webpush.GenerateVAPID()
		if err != nil {
			obs.Fatal("generate VAPID keys", err)
		}
		fmt.Printf("VAPID_PUBLIC_KEY=%s\nVAPID_PRIVATE_KEY=%s\n", pub, priv)
		return
	}

	cfg, err := notify.LoadConfig(os.Getenv)
	if err != nil {
		obs.Fatal("notifier config", err)
	}
	r, err := notify.NewRenderer(cfg.BaseURL)
	if err != nil {
		obs.Fatal("notifier templates", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		obs.Fatal("open store", err)
	}
	defer s.Close()

	w := &notify.Worker{Store: s, Renderer: r, Opts: cfg.Opts}
	if cfg.Push != nil {
		w.Push = cfg.Push
	}
	if cfg.Mail != nil {
		w.Mail = cfg.Mail
	}
	slog.Info("notifier: starting", "channels", w.Channels())
	if len(w.Channels()) == 0 {
		slog.Warn("notifier: no channel configured (VAPID_* / SMTP_*); idling")
	}
	if *once {
		n, err := w.Tick(ctx)
		slog.Info("notifier: processed rows", "rows", n)
		if err != nil {
			obs.Fatal("notifier tick", err)
		}
		return
	}
	obs.RegisterPool(s.Stat)
	if err := obs.ServeMetrics(ctx, obs.MetricsAddr("127.0.0.1:9094")); err != nil {
		obs.Fatal("metrics listener", err)
	}
	w.Run(ctx)
}
