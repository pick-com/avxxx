package api

// Cookie consent records (CookieBanner / MobileConsent; PRD 15, 14.5):
// POST /api/consent appends a choice; GET /api/me/consent returns a
// logged-in user's latest choice so it follows them across devices.

import (
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"errors"
	"net/http"
)

func (srv *Server) consentRoutes() []route {
	return []route{
		{"POST /api/consent", srv.recordConsent},
		{"GET /api/me/consent", srv.myConsent},
	}
}

// recordConsent: POST /api/consent {"analytics", "personalized_ads",
// "personalized_recs", "gpc", "source": banner|preferences|sync|gpc,
// "device_id"}. A valid user token links the record to the account; without
// one device_id (a random per-browser id) is required. Only a keyed hash of
// device_id is stored (LOG_HASH_KEY), never the id itself or the IP. A GPC
// signal (body gpc or the Sec-GPC: 1 header) forces personalized_ads off.
// 201 with the stored choice; 429 when rate limited.
func (srv *Server) recordConsent(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	var in store.ConsentInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if r.Header.Get("Sec-GPC") == "1" {
		in.GPC = true
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowIntake(r, consentLimit); err != nil {
		reply(w, nil, err)
		return
	}
	rec := store.ConsentRecord{Input: in}
	if in.DeviceID != "" {
		rec.DeviceHash = obs.HashKeyed("consent-device", in.DeviceID)
	}
	// An invalid or expired token is not an error: the choice is still
	// recorded for the device.
	if bearerToken(r) != "" && srv.accounts != nil {
		if uid, err := srv.userID(r); err == nil {
			rec.UserID = &uid
		} else if !errors.Is(err, errUnauthorized) {
			reply(w, nil, err)
			return
		}
	}
	if rec.UserID == nil && rec.DeviceHash == "" {
		reply(w, nil, badRequest("device_id is required without a login", nil))
		return
	}
	v, err := srv.compliance.RecordConsent(r.Context(), rec)
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// myConsent: GET /api/me/consent, the user's latest choice on any device
// (404 before the first one).
func (srv *Server) myConsent(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	uid, err := srv.userID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.LatestConsent(r.Context(), uid)
	reply(w, v, err)
}
