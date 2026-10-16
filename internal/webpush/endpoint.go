package webpush

import (
	"errors"
	"net"
	"net/url"
	"strings"
)

// MaxEndpointLen bounds stored endpoints (real ones are ~150–500 bytes).
const MaxEndpointLen = 2048

// DefaultAllowedHosts are the push services of the major browsers. The
// server POSTs to user-supplied endpoints, so restricting hosts keeps the
// notifier from being used to reach arbitrary (internal) URLs.
var DefaultAllowedHosts = []string{
	"fcm.googleapis.com",        // Chrome, Edge (Chromium), Opera, Samsung
	"android.googleapis.com",    // legacy GCM endpoints
	"push.services.mozilla.com", // Firefox (updates.push.services.mozilla.com)
	"notify.windows.com",        // legacy Edge (*.notify.windows.com)
	"push.apple.com",            // Safari (web.push.apple.com)
}

// ValidateEndpoint checks a subscription endpoint: absolute https URL, no
// userinfo, no IP-literal host, and (unless allowed is nil) a host equal to
// or a subdomain of one of allowed.
func ValidateEndpoint(endpoint string, allowed []string) error {
	if len(endpoint) > MaxEndpointLen {
		return errors.New("endpoint too long")
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return errors.New("endpoint is not a URL")
	}
	if u.Scheme != "https" {
		return errors.New("endpoint must be https")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || u.User != nil || u.Opaque != "" {
		return errors.New("endpoint must be an absolute https URL")
	}
	if net.ParseIP(host) != nil {
		return errors.New("endpoint host must be a domain name")
	}
	if allowed == nil {
		return nil
	}
	for _, a := range allowed {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" && (host == a || strings.HasSuffix(host, "."+a)) {
			return nil
		}
	}
	return errors.New("endpoint host is not a known push service")
}
