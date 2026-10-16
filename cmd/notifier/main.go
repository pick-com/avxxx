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
	"log"
	"os"
	"os/signal"
	"syscall"

	"avxxx/internal/notify"
	"avxxx/internal/store"
	"avxxx/internal/webpush"
)

func main() {
	once := flag.Bool("once", false, "deliver one batch and exit")
	gen := flag.Bool("gen-vapid", false, "print a new VAPID key pair and exit")
	flag.Parse()

	if *gen {
		pub, priv, err := webpush.GenerateVAPID()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("VAPID_PUBLIC_KEY=%s\nVAPID_PRIVATE_KEY=%s\n", pub, priv)
		return
	}

	cfg, err := notify.LoadConfig(os.Getenv)
	if err != nil {
		log.Fatalf("notifier config: %v", err)
	}
	r, err := notify.NewRenderer(cfg.BaseURL)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	s, err := store.Open(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	w := &notify.Worker{Store: s, Renderer: r, Opts: cfg.Opts}
	if cfg.Push != nil {
		w.Push = cfg.Push
	}
	if cfg.Mail != nil {
		w.Mail = cfg.Mail
	}
	log.Printf("notifier: channels=%v", w.Channels())
	if len(w.Channels()) == 0 {
		log.Print("notifier: no channel configured (VAPID_* / SMTP_*); idling")
	}
	if *once {
		n, err := w.Tick(ctx)
		log.Printf("notifier: processed %d rows", n)
		if err != nil {
			log.Fatal(err)
		}
		return
	}
	w.Run(ctx)
}
