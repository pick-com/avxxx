// Package clientip derives a request's client address.
//
// By default the client is the TCP peer: forwarding headers are ignored, so a
// client cannot choose its own address. Behind a reverse proxy (Caddy on the
// same host) every request then comes from the proxy, so TRUSTED_PROXIES lists
// the proxies' networks: when the peer is inside one of them, the client is
// the right-most X-Forwarded-For entry that is not itself a trusted proxy.
// Entries left of it were written by the client and are never used.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// EnvVar is the environment variable holding the trusted proxy networks.
const EnvVar = "TRUSTED_PROXIES"

// Resolver derives client addresses. The zero value trusts no proxy and
// returns the TCP peer.
type Resolver struct {
	trusted []netip.Prefix
}

// Parse builds a Resolver from a comma-separated list of CIDRs (a bare
// address is a single-host network). Empty entries are ignored; "" trusts no
// proxy.
func Parse(spec string) (Resolver, error) {
	var r Resolver
	for _, f := range strings.Split(spec, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		var p netip.Prefix
		var err error
		if strings.Contains(f, "/") {
			p, err = netip.ParsePrefix(f)
		} else {
			var a netip.Addr
			if a, err = netip.ParseAddr(f); err == nil && a.Zone() != "" {
				err = fmt.Errorf("zoned address")
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if err != nil || !p.IsValid() {
			return Resolver{}, fmt.Errorf("%s: invalid network %q", EnvVar, f)
		}
		p = p.Masked()
		if p.Addr().Is4In6() { // ::ffff:a.b.c.d/n+96 is the IPv4 network a.b.c.d/n
			if p.Bits() < 96 {
				return Resolver{}, fmt.Errorf("%s: invalid network %q", EnvVar, f)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		r.trusted = append(r.trusted, p)
	}
	return r, nil
}

// FromEnv reads TRUSTED_PROXIES through getenv.
func FromEnv(getenv func(string) string) (Resolver, error) {
	return Parse(getenv(EnvVar))
}

// Trusts reports whether any proxy is trusted.
func (r Resolver) Trusts() bool { return len(r.trusted) > 0 }

func (r Resolver) trustedAddr(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseAddr parses "1.2.3.4", "1.2.3.4:80", "::1" or "[::1]:80", without zone
// and with IPv4-mapped IPv6 addresses unmapped.
func parseAddr(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	a, err := netip.ParseAddr(s)
	if err != nil {
		h, _, serr := net.SplitHostPort(s)
		if serr != nil {
			return netip.Addr{}, false
		}
		if a, err = netip.ParseAddr(h); err != nil {
			return netip.Addr{}, false
		}
	}
	return a.WithZone("").Unmap(), true
}

// IP returns the client address of req as a bare IP string. Without trusted
// proxies, or when the peer is not one, it is the TCP peer (RemoteAddr as is
// when it cannot be parsed). When the peer is trusted, X-Forwarded-For is read
// right to left, skipping trusted proxies: the first other address is the
// client. A malformed entry stops the walk (nothing left of it can be
// trusted) and the last trusted hop is returned; so is the left-most entry
// when every hop is trusted.
func (r Resolver) IP(req *http.Request) string {
	peer, ok := parseAddr(req.RemoteAddr)
	if !ok {
		if h, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			return h
		}
		return req.RemoteAddr
	}
	if !r.trustedAddr(peer) {
		return peer.String()
	}
	client := peer
	values := req.Header.Values("X-Forwarded-For")
	for i := len(values) - 1; i >= 0; i-- {
		hops := strings.Split(values[i], ",")
		for j := len(hops) - 1; j >= 0; j-- {
			if strings.TrimSpace(hops[j]) == "" {
				continue
			}
			a, ok := parseAddr(hops[j])
			if !ok {
				return client.String()
			}
			if !r.trustedAddr(a) {
				return a.String()
			}
			client = a
		}
	}
	return client.String()
}
