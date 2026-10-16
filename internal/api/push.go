package api

// Web Push subscription routes (PRD 17, tech plan 3.7). Delivery is done by
// cmd/notifier; the API only hands out the VAPID public key and stores the
// browser's PushSubscription. Without VAPID_PUBLIC_KEY push is disabled:
// the key route answers 200 {"enabled":false} (so browsers log no error on
// every settings page), subscribe returns 404, and DELETE keeps working for
// cleanup.

import (
	"context"
	"crypto/ecdh"
	"log/slog"
	"net/http"
	"strings"

	"avxxx/internal/webpush"
)

// pushStore is the subset of *store.Store used by the push routes.
type pushStore interface {
	SavePushSubscription(ctx context.Context, userID int64, endpoint, p256dh, auth, userAgent string) error
	DeletePushSubscription(ctx context.Context, userID int64, endpoint string) (int64, error)
}

// pushConfig is read once by Register.
type pushConfig struct {
	vapidPublicKey string   // "" disables push
	allowedHosts   []string // nil accepts any https host
}

// pushConfigFromEnv reads VAPID_PUBLIC_KEY and PUSH_ALLOWED_HOSTS
// (comma-separated host suffixes; "*" allows any https host; default: the
// major browsers' push services).
func pushConfigFromEnv(getenv func(string) string) pushConfig {
	var c pushConfig
	if k := strings.TrimSpace(getenv("VAPID_PUBLIC_KEY")); k != "" {
		raw, err := webpush.DecodeB64(k)
		if err == nil {
			_, err = ecdh.P256().NewPublicKey(raw)
		}
		if err != nil {
			slog.Warn("VAPID_PUBLIC_KEY is not a P-256 public key; web push disabled")
		} else {
			c.vapidPublicKey = webpush.EncodeB64(raw)
		}
	}
	switch v := strings.TrimSpace(getenv("PUSH_ALLOWED_HOSTS")); v {
	case "":
		c.allowedHosts = webpush.DefaultAllowedHosts
	case "*":
		c.allowedHosts = nil
	default:
		for _, h := range strings.Split(v, ",") {
			if h = strings.TrimSpace(h); h != "" {
				c.allowedHosts = append(c.allowedHosts, h)
			}
		}
	}
	return c
}

func (srv *Server) pushRoutes() []route {
	return []route{
		{"GET /api/push/vapid-public-key", srv.vapidPublicKey},
		{"POST /api/me/push-subscriptions", srv.user(srv.subscribePush)},
		{"DELETE /api/me/push-subscriptions", srv.user(srv.unsubscribePush)},
	}
}

var errPushDisabled = &httpError{status: http.StatusNotFound, msg: "web push is not configured"}

func (srv *Server) vapidPublicKey(w http.ResponseWriter, r *http.Request) {
	if srv.pushCfg.vapidPublicKey == "" {
		w.Header().Set("Cache-Control", "no-cache")
		reply(w, map[string]any{"enabled": false}, nil)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	reply(w, map[string]any{"enabled": true, "public_key": srv.pushCfg.vapidPublicKey}, nil)
}

// pushSubscriptionJSON is PushSubscription.toJSON() from the browser.
type pushSubscriptionJSON struct {
	Endpoint       string       `json:"endpoint"`
	ExpirationTime *float64     `json:"expirationTime"`
	Keys           webpush.Keys `json:"keys"`
}

const maxUserAgent = 256

// subscribePush stores the caller's PushSubscription.toJSON() (201).
func (srv *Server) subscribePush(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.pushCfg.vapidPublicKey == "" || srv.push == nil {
		reply(w, nil, errPushDisabled)
		return
	}
	var in pushSubscriptionJSON
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := webpush.ValidateEndpoint(in.Endpoint, srv.pushCfg.allowedHosts); err != nil {
		reply(w, nil, badRequest(err.Error(), nil)) // the message never echoes the endpoint
		return
	}
	pub, auth, err := in.Keys.Decode()
	if err != nil {
		reply(w, nil, badRequest("keys.p256dh and keys.auth must be a P-256 public key and a 16-byte secret", nil))
		return
	}
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	e := srv.push.SavePushSubscription(r.Context(), uid, in.Endpoint,
		webpush.EncodeB64(pub.Bytes()), webpush.EncodeB64(auth), strings.ToValidUTF8(ua, ""))
	if e == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
	}
	reply(w, map[string]bool{"subscribed": e == nil}, e)
}

// unsubscribePush removes one subscription ({"endpoint": "..."}), or every
// subscription of the caller when the body is empty or has no endpoint.
func (srv *Server) unsubscribePush(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.push == nil {
		reply(w, nil, errPushDisabled)
		return
	}
	var in pushSubscriptionJSON // keys and expirationTime are accepted and ignored
	if err := readJSON(r, &in, true); err != nil {
		reply(w, nil, err)
		return
	}
	n, e := srv.push.DeletePushSubscription(r.Context(), uid, in.Endpoint)
	reply(w, map[string]int64{"deleted": n}, e)
}
