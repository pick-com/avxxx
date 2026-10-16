package obs

import (
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
)

// Redacted replaces a value that must never be logged.
const Redacted = "[REDACTED]"

// sensitiveKeys are attribute-key substrings whose values are dropped
// whatever they contain (tokens, passwords, Authorization / Cookie headers,
// push subscription endpoints and keys, email addresses, sessions).
var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie",
	"api_key", "apikey", "private_key", "p256dh", "auth", "credential",
	"email", "endpoint", "session", "signature",
}

// ipKeys hold a client address; their values are reduced to a network prefix.
var ipKeys = map[string]bool{
	"ip": true, "client_ip": true, "remote_addr": true, "remote_ip": true,
	"x_forwarded_for": true, "x-forwarded-for": true, "forwarded_for": true,
}

// Free-text scrubbers, applied to the message and every string value (the
// remaining log.Printf calls arrive as a single message string).
var (
	bearerRe = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]+`)
	// A secret-looking query parameter: keep the name, drop the value
	// (api_id / affiliate_id: DMM Affiliate API credentials).
	querySecretRe = regexp.MustCompile(`(?i)([?&;](?:access_token|refresh_token|id_token|token|key|api_key|apikey|password|passwd|secret|sig|signature|auth|code|session|share_token|api_id|affiliate_id)=)[^&\s"';]+`)
	// Header dumps (fmt of an http.Header, raw header lines).
	cookieRe = regexp.MustCompile(`(?i)\b((?:set-)?cookie|x-api-key|proxy-authorization)(["']?\s*[:=]\s*\[?)[^\]\r\n]*`)
	emailRe  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// Web Push endpoints are capability URLs; keep only the host.
	pushEndpointRe = regexp.MustCompile(`(?i)(https://[A-Za-z0-9.-]*(?:fcm\.googleapis\.com|push\.services\.mozilla\.com|notify\.windows\.com|push\.apple\.com|updates\.push\.services\.mozilla\.com))/[^\s"']*`)
	ipv4Re         = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3})\.\d{1,3}\b`)
)

// ScrubString removes secrets, email addresses, push endpoints and the last
// octet of IPv4 addresses from free text.
func ScrubString(s string) string {
	if s == "" {
		return s
	}
	s = bearerRe.ReplaceAllString(s, "$1 "+Redacted)
	s = cookieRe.ReplaceAllString(s, "${1}${2}"+Redacted)
	s = pushEndpointRe.ReplaceAllString(s, "$1/"+Redacted)
	s = querySecretRe.ReplaceAllString(s, "${1}"+Redacted)
	s = emailRe.ReplaceAllString(s, "[EMAIL]")
	s = ipv4Re.ReplaceAllStringFunc(s, func(m string) string {
		if ip, err := netip.ParseAddr(m); err == nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
			return m // listen / compose-network addresses are not personal data
		}
		return ipv4Re.ReplaceAllString(m, "$1.0/24")
	})
	return s
}

// IPPrefix reduces a client address ("1.2.3.4", "1.2.3.4:5678", "[::1]:80")
// to its /24 (IPv4) or /48 (IPv6) network, which is enough to spot abuse
// from one network without logging a personal address.
func IPPrefix(addr string) string {
	addr = strings.TrimSpace(addr)
	if i := strings.IndexByte(addr, ','); i >= 0 { // X-Forwarded-For list: first hop
		addr = strings.TrimSpace(addr[:i])
	}
	if h, _, err := net.SplitHostPort(addr); err == nil {
		addr = h
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return Redacted
	}
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	p, err := ip.Prefix(bits)
	if err != nil {
		return Redacted
	}
	return p.String()
}

func sensitiveKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range sensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Redact is the slog ReplaceAttr hook every handler from NewLogger uses.
func Redact(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
		return a
	}
	if a.Key != slog.MessageKey && sensitiveKey(a.Key) {
		return slog.String(a.Key, Redacted)
	}
	if ipKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, IPPrefix(a.Value.Resolve().String()))
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, ScrubString(v.String()))
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, ScrubString(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, ScrubString(x.String()))
		case []byte:
			return slog.String(a.Key, ScrubString(string(x)))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
