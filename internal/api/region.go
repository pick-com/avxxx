package api

// Visitor region (ISO 3166-1 alpha-2) for region-targeted home slots and
// announcements and play-config's region_blocked / region_allow filtering.
//
// Order: an explicit ?region= (the SSR web server passes it, see
// web/app/lib/region.server.ts), else the CDN's CF-IPCountry header, else the
// header named by GEOIP_HEADER (a country header a trusted reverse proxy or
// CDN sets and overwrites, e.g. X-Country-Code). Without any of them the
// region is unknown ("") and region features stay inactive: untargeted
// content only, and region_allow sources are not offered. There is no GeoIP
// database lookup.
//
// The value is advisory, not an access control: a client can send ?region=
// itself, so region_allow is a licensing presentation rule, not DRM.

import (
	"avxxx/internal/homeops"
	"net/http"
	"strings"
	"sync/atomic"
)

// GeoIPHeaderEnv names the optional fallback country header.
const GeoIPHeaderEnv = "GEOIP_HEADER"

var geoipHeader atomic.Pointer[string]

// setGeoIPHeader configures the fallback country header ("" = none).
func setGeoIPHeader(name string) {
	name = strings.TrimSpace(name)
	if name != "" {
		name = http.CanonicalHeaderKey(name)
	}
	geoipHeader.Store(&name)
}

func geoipHeaderName() string {
	if p := geoipHeader.Load(); p != nil && *p != http.CanonicalHeaderKey("CF-IPCountry") {
		return *p
	}
	return ""
}

// headerRegion is the visitor's country from CF-IPCountry or GEOIP_HEADER
// ("" when unknown; Cloudflare's "XX" unknown and "T1" Tor count as unknown).
func headerRegion(r *http.Request) string {
	names := []string{"CF-IPCountry"}
	if h := geoipHeaderName(); h != "" {
		names = append(names, h)
	}
	for _, name := range names {
		reg, ok := homeops.NormalizeRegion(r.Header.Get(name))
		if ok && reg != "" && reg != "XX" && reg != "T1" {
			return reg
		}
	}
	return ""
}

// varyRegion marks a response as depending on the region headers.
func varyRegion(w http.ResponseWriter) {
	w.Header().Add("Vary", "CF-IPCountry")
	if h := geoipHeaderName(); h != "" {
		w.Header().Add("Vary", h)
	}
}
