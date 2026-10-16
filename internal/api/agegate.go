package api

// Site-entry 18+ gate (DESIGN_ALIGNMENT #8, PRD 15; docs/COMPLIANCE.md).
//
// The site is adults-only. On a first visit the web app shows an
// interstitial with an 18+ checkbox; POST /api/age-gate {confirmed:true}
// answers ok (legacy clients may still send {year, month}, answered ok or
// denied) and sets a signed cookie the SSR server checks before it renders
// any page (web/app/lib/ageGate.server.ts verifies the same HMAC):
//
//	ag=v1.<ok|denied>.<expiry unix>.<base64url HMAC-SHA256(AGE_GATE_KEY, "v1.<verdict>.<expiry>")>
//
//   - ok lasts about a year; denied lasts 24 hours and cannot be replaced
//     while it is valid: the endpoint refuses new answers (403) until it
//     expires, so a visitor cannot simply pick another year.
//   - HttpOnly, SameSite=Lax, Path=/, Secure over https (BASE_URL).
//   - Nothing about the visitor is stored: no birth date, no log line with
//     it. Only the counter avxxx_age_gate_total{result} is kept.
//   - Per-address rate limit (limitAgeGateIP).
//
// Age is computed conservatively from year and month only: whoever is born
// in the current month counts as not yet having had the birthday.
//
// Accounts need the same confirmation (adultCheck): registration, an OAuth
// sign-up and the one-time prompt for older accounts (POST /api/me/adult-confirm).
//
// This is a self-declaration gate, not identity verification. The content
// API itself stays open (crawlers, API clients); the gate protects the web
// pages. See docs/COMPLIANCE.md for the known limitations and the legal
// follow-ups (region-based verification).

import (
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// AdultAge is the minimum age to use the site.
	AdultAge = 18

	ageGateCookie    = "ag"
	ageGateOKTTL     = 365 * 24 * time.Hour
	ageGateDeniedTTL = 24 * time.Hour

	// ageGateDevKey signs gate cookies when AGE_GATE_KEY is unset (local
	// development). It must equal DEV_KEY in web/app/lib/ageGate.server.ts.
	// Production sets AGE_GATE_KEY (deploy/ansible).
	ageGateDevKey = "avxxx-dev-age-gate-key"
)

// limitAgeGateIP bounds gate answers per client address.
var limitAgeGateIP = rateRule{20, 10 * time.Minute}

type ageVerdict string

const (
	verdictNone   ageVerdict = ""
	verdictOK     ageVerdict = "ok"
	verdictDenied ageVerdict = "denied"
)

// ageGate signs and verifies gate cookies.
type ageGate struct {
	key []byte
}

// newAgeGate reads AGE_GATE_KEY; unset falls back to the development key
// (logged), so a local api and web agree without configuration. On a public
// https deployment (BASE_URL / PUBLIC_BASE_URL https, not localhost) the
// well-known development key would let anyone forge an "ok" cookie, so it
// fails closed instead: a random per-process key that no other process
// shares, which keeps every visitor at the gate until AGE_GATE_KEY is set.
func newAgeGate(getenv func(string) string) ageGate {
	key := strings.TrimSpace(getenv("AGE_GATE_KEY"))
	if key == "" {
		if publicHTTPS(getenv("PUBLIC_BASE_URL")) || publicHTTPS(getenv("BASE_URL")) {
			slog.Error("AGE_GATE_KEY is not set on a public https deployment: the age gate refuses every cookie until it is configured")
			b := make([]byte, 32)
			rand.Read(b)
			return ageGate{key: b}
		}
		slog.Warn("AGE_GATE_KEY is not set: age gate cookies use the development key")
		key = ageGateDevKey
	}
	return ageGate{key: []byte(key)}
}

// publicHTTPS reports whether base is an https URL for a non-local host.
func publicHTTPS(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Scheme != "https" {
		return false
	}
	h := u.Hostname()
	return h != "" && h != "localhost" && h != "127.0.0.1" && h != "::1" && !strings.HasSuffix(h, ".localhost") && !strings.HasSuffix(h, ".test")
}

func (g ageGate) mac(payload string) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// sign returns the cookie value for verdict v valid until exp.
func (g ageGate) sign(v ageVerdict, exp time.Time) string {
	payload := "v1." + string(v) + "." + strconv.FormatInt(exp.Unix(), 10)
	return payload + "." + g.mac(payload)
}

// parse verifies a cookie value: the verdict and its expiry, or verdictNone
// for a missing, malformed, tampered or expired value.
func (g ageGate) parse(value string, now time.Time) (ageVerdict, time.Time) {
	parts := strings.Split(value, ".")
	if len(parts) != 4 || parts[0] != "v1" {
		return verdictNone, time.Time{}
	}
	v := ageVerdict(parts[1])
	if v != verdictOK && v != verdictDenied {
		return verdictNone, time.Time{}
	}
	sec, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return verdictNone, time.Time{}
	}
	if !hmac.Equal([]byte(parts[3]), []byte(g.mac(strings.Join(parts[:3], ".")))) {
		return verdictNone, time.Time{}
	}
	exp := time.Unix(sec, 0)
	if !now.Before(exp) {
		return verdictNone, time.Time{}
	}
	return v, exp
}

// ageFromBirth is the age in whole years at now from a birth year and month.
// The day is unknown, so the birth month itself counts as not yet reached.
// Months outside 1-12, years more than 120 years back and future dates are
// invalid.
func ageFromBirth(year, month int, now time.Time) (int, error) {
	now = now.UTC()
	if month < 1 || month > 12 || year < now.Year()-120 || year > now.Year() || (year == now.Year() && month > int(now.Month())) {
		return 0, badRequest("invalid birth year or month", nil)
	}
	age := now.Year() - year
	if int(now.Month()) <= month {
		age--
	}
	return age, nil
}

func (srv *Server) gateSecure(r *http.Request) bool { return r.TLS != nil || srv.adminSecureCookie }

// gateState reads the visitor's gate cookie.
func (srv *Server) gateState(r *http.Request) (ageVerdict, time.Time) {
	c, err := r.Cookie(ageGateCookie)
	if err != nil {
		return verdictNone, time.Time{}
	}
	return srv.ageGate.parse(c.Value, srv.now())
}

// setGateCookie stores verdict v and returns its expiry.
func (srv *Server) setGateCookie(w http.ResponseWriter, r *http.Request, v ageVerdict) time.Time {
	ttl := ageGateOKTTL
	if v == verdictDenied {
		ttl = ageGateDeniedTTL
	}
	exp := srv.now().Add(ttl).Truncate(time.Second)
	http.SetCookie(w, &http.Cookie{Name: ageGateCookie, Value: srv.ageGate.sign(v, exp), Path: "/", Expires: exp, MaxAge: int(ttl / time.Second),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: srv.gateSecure(r)})
	return exp
}

func (srv *Server) now() time.Time {
	if srv.clock != nil {
		return srv.clock()
	}
	return time.Now()
}

func (srv *Server) ageGateRoutes() []route {
	return []route{
		{"POST /api/age-gate", srv.postAgeGate},
	}
}

type ageGateReply struct {
	Status      string     `json:"status"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
}

func deniedReply(until time.Time) ageGateReply {
	u := until.UTC()
	return ageGateReply{Status: string(verdictDenied), LockedUntil: &u}
}

// postAgeGate answers the site-entry gate: {"year": 1990, "month": 4}.
func (srv *Server) postAgeGate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if err := requireJSON(r); err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.allow(r, "agegate:"+srv.ip(r), limitAgeGateIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	// A valid denial is locked for its 24 hours.
	if v, until := srv.gateState(r); v == verdictDenied {
		obs.AgeGate.WithLabelValues("locked").Inc()
		replyStatus(w, http.StatusForbidden, deniedReply(until))
		return
	}
	var in struct {
		Confirmed bool `json:"confirmed"`
		Year      int  `json:"year"`
		Month     int  `json:"month"`
	}
	if err := readJSON(r, &in, false); err != nil {
		obs.AgeGate.WithLabelValues("invalid").Inc()
		reply(w, nil, err)
		return
	}
	if in.Confirmed {
		obs.AgeGate.WithLabelValues("ok").Inc()
		srv.setGateCookie(w, r, verdictOK)
		reply(w, ageGateReply{Status: string(verdictOK)}, nil)
		return
	}
	age, err := ageFromBirth(in.Year, in.Month, srv.now())
	if err != nil {
		obs.AgeGate.WithLabelValues("invalid").Inc()
		reply(w, nil, err)
		return
	}
	if age < AdultAge {
		obs.AgeGate.WithLabelValues("denied").Inc()
		reply(w, deniedReply(srv.setGateCookie(w, r, verdictDenied)), nil)
		return
	}
	obs.AgeGate.WithLabelValues("ok").Inc()
	srv.setGateCookie(w, r, verdictOK)
	reply(w, ageGateReply{Status: string(verdictOK)}, nil)
}

// adultInput is the 18+ confirmation an account route accepts: the checkbox,
// plus birth year and month unless the gate cookie is already ok.
type adultInput struct {
	AdultConfirmed bool `json:"adult_confirmed"`
	BirthYear      int  `json:"birth_year"`
	BirthMonth     int  `json:"birth_month"`
}

var (
	errAdultRequired = &httpError{status: http.StatusBadRequest, msg: "adult confirmation required"}
	errBirthRequired = &httpError{status: http.StatusBadRequest, msg: "birth year and month are required"}
	errAgeRestricted = &httpError{status: http.StatusForbidden, msg: "age restricted"}
)

// adultCheck turns an adultInput into the confirmation to store, or refuses:
// 400 without the checkbox (or without a birth date when the gate cookie is
// not ok), 403 while the gate cookie is denied or for an under-18 birth date
// (which also sets the 24-hour denied cookie, as the gate would).
func (srv *Server) adultCheck(w http.ResponseWriter, r *http.Request, in adultInput) (store.Adult, error) {
	if !in.AdultConfirmed {
		return store.Adult{}, errAdultRequired
	}
	v, _ := srv.gateState(r)
	if v == verdictDenied {
		return store.Adult{}, errAgeRestricted
	}
	if in.BirthYear != 0 || in.BirthMonth != 0 {
		age, err := ageFromBirth(in.BirthYear, in.BirthMonth, srv.now())
		if err != nil {
			return store.Adult{}, err
		}
		if age < AdultAge {
			obs.AgeGate.WithLabelValues("denied").Inc()
			srv.setGateCookie(w, r, verdictDenied)
			return store.Adult{}, errAgeRestricted
		}
		return store.Adult{Confirmed: true, BirthYM: fmt.Sprintf("%04d-%02d", in.BirthYear, in.BirthMonth)}, nil
	}
	// Checkbox-only confirmation is the current product flow. Legacy clients
	// may still send birth year/month and are handled above for compatibility.
	return store.Adult{Confirmed: true}, nil
}
