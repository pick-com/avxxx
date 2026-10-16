package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// Catalog egress (FINAL_DATA_DESIGN 8 "来源出口"): which network path a
// catalog crawl takes. A source may need to leave through a VPN or another
// egress the operator runs; the crawler reaches it through an HTTP(S) or
// SOCKS5 proxy URL. The proxy only changes the egress: robots.txt, the
// license gate, budgets and block detection apply exactly as on a direct
// connection, and a challenge served through the proxy still blocks the
// source.
//
// Resolution, first match wins:
//
//  1. the source's crawl policy proxy_url (source_crawl_policies.proxy_url):
//     "direct" (no proxy, ignore everything below), "env:CATALOG_PROXY_<X>"
//     (the URL is read from that environment variable, so credentials stay
//     in deployment secrets), or a proxy URL without credentials;
//  2. CATALOG_PROXY_URL;
//  3. the standard HTTPS_PROXY / HTTP_PROXY / NO_PROXY variables (and their
//     lower-case forms), as net/http's ProxyFromEnvironment reads them;
//  4. no proxy.
//
// A configured proxy that cannot be resolved (missing env variable, invalid
// URL) is an error, never a silent fall back to the direct route.

// ProxyEnv is the global catalog proxy variable.
const ProxyEnv = "CATALOG_PROXY_URL"

// ProxyDirect is the source proxy_url value that forces a direct connection.
const ProxyDirect = "direct"

// proxyEnvPrefix limits "env:NAME" references to proxy variables, so an
// admin-editable value can never point the crawler at another secret.
const proxyEnvPrefix = "CATALOG_PROXY_"

var proxyEnvName = regexp.MustCompile(`^CATALOG_PROXY_[A-Z0-9_]+$`)

// maxProxySetting bounds the stored proxy_url value.
const maxProxySetting = 512

// Egress modes (Egress.Mode).
const (
	EgressSource      = "source"      // the source's own proxy_url
	EgressGlobal      = "global"      // CATALOG_PROXY_URL
	EgressEnvironment = "environment" // HTTPS_PROXY / HTTP_PROXY
	EgressDirect      = "direct"      // proxy_url "direct": explicitly no proxy
	EgressNone        = "none"        // nothing configured
)

// Egress is a resolved network path for one source's crawl.
type Egress struct {
	Mode string
	// Desc is safe to log: proxy credentials are redacted.
	Desc string
	// proxy is the explicit proxy (source/global); env is the environment
	// proxy function. Both nil means a direct connection.
	proxy *url.URL
	env   func(*url.URL) (*url.URL, error)
}

// Proxied reports whether requests may go through a proxy.
func (e Egress) Proxied() bool { return e.proxy != nil || e.env != nil }

// Key identifies the egress for caching per-egress state (robots.txt). It
// may contain credentials: never log it.
func (e Egress) Key() string {
	if e.proxy != nil {
		return e.Mode + "|" + e.proxy.String()
	}
	return e.Mode
}

// ParseProxyURL parses and validates a proxy URL: http, https, socks5 or
// socks5h, with a host and nothing else (no path, query or fragment). Errors
// never echo the URL, which may carry credentials.
func ParseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("proxy URL is not a valid URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("proxy URL scheme must be http, https, socks5 or socks5h")
	}
	if u.Hostname() == "" {
		return nil, errors.New("proxy URL needs a host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("proxy URL must be scheme://[user:password@]host:port")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Path = ""
	return u, nil
}

// ValidateSourceProxy checks an admin-supplied proxy_url: "" (inherit),
// "direct", "env:CATALOG_PROXY_<X>", or a proxy URL without credentials.
// Credentials are refused here (and by a database CHECK) so they never land
// in the database, API responses or audit rows.
func ValidateSourceProxy(v string) error {
	v = strings.TrimSpace(v)
	switch {
	case v == "" || v == ProxyDirect:
		return nil
	case len(v) > maxProxySetting:
		return errors.New("proxy_url too long")
	case strings.HasPrefix(v, "env:"):
		if !proxyEnvName.MatchString(strings.TrimPrefix(v, "env:")) {
			return fmt.Errorf("proxy_url env reference must name a %s* variable (A-Z, 0-9, _)", proxyEnvPrefix)
		}
		return nil
	}
	u, err := ParseProxyURL(v)
	if err != nil {
		return err
	}
	if u.User != nil {
		return fmt.Errorf("proxy_url must not contain credentials: put the full URL in a %s* environment variable and set proxy_url to env:<NAME>", proxyEnvPrefix)
	}
	return nil
}

// ResolveEgress resolves a source's egress from its proxy_url setting and the
// environment (getenv; nil = no environment). See the package comment above
// for the order.
func ResolveEgress(sourceProxy string, getenv func(string) string) (Egress, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	sourceProxy = strings.TrimSpace(sourceProxy)
	if sourceProxy != "" {
		if err := ValidateSourceProxy(sourceProxy); err != nil {
			return Egress{}, fmt.Errorf("source proxy_url: %w", err)
		}
		if sourceProxy == ProxyDirect {
			return Egress{Mode: EgressDirect, Desc: "direct (source proxy_url=direct)"}, nil
		}
		raw, from := sourceProxy, "source proxy_url"
		if name, ok := strings.CutPrefix(sourceProxy, "env:"); ok {
			raw, from = strings.TrimSpace(getenv(name)), "source proxy_url env:"+name
			if raw == "" {
				return Egress{}, fmt.Errorf("%s: %w: variable is not set", from, ErrNotConfigured)
			}
		}
		u, err := ParseProxyURL(raw)
		if err != nil {
			return Egress{}, fmt.Errorf("%s: %w", from, err)
		}
		return Egress{Mode: EgressSource, Desc: "proxy " + RedactProxyURL(u.String()) + " (" + from + ")", proxy: u}, nil
	}
	if raw := strings.TrimSpace(getenv(ProxyEnv)); raw != "" {
		u, err := ParseProxyURL(raw)
		if err != nil {
			return Egress{}, fmt.Errorf("%s: %w", ProxyEnv, err)
		}
		return Egress{Mode: EgressGlobal, Desc: "proxy " + RedactProxyURL(u.String()) + " (" + ProxyEnv + ")", proxy: u}, nil
	}
	cfg := httpproxy.Config{
		HTTPProxy:  firstEnv(getenv, "HTTP_PROXY", "http_proxy"),
		HTTPSProxy: firstEnv(getenv, "HTTPS_PROXY", "https_proxy"),
		NoProxy:    firstEnv(getenv, "NO_PROXY", "no_proxy"),
	}
	if cfg.HTTPProxy == "" && cfg.HTTPSProxy == "" {
		return Egress{Mode: EgressNone, Desc: "direct"}, nil
	}
	parts := []string{}
	if cfg.HTTPSProxy != "" {
		parts = append(parts, "https via "+RedactProxyURL(cfg.HTTPSProxy))
	}
	if cfg.HTTPProxy != "" {
		parts = append(parts, "http via "+RedactProxyURL(cfg.HTTPProxy))
	}
	return Egress{Mode: EgressEnvironment, Desc: "proxy from environment (" + strings.Join(parts, ", ") + ")", env: cfg.ProxyFunc()}, nil
}

func firstEnv(getenv func(string) string, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

// proxyFunc is the http.Transport.Proxy function of e (nil = direct).
func (e Egress) proxyFunc() func(*http.Request) (*url.URL, error) {
	switch {
	case e.proxy != nil:
		return http.ProxyURL(e.proxy)
	case e.env != nil:
		return func(r *http.Request) (*url.URL, error) { return e.env(r.URL) }
	}
	return nil
}

// NewHTTPClient returns a client whose transport leaves through e. Errors it
// returns have the proxy credentials scrubbed.
func NewHTTPClient(timeout time.Duration, e Egress) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = e.proxyFunc()
	var rt http.RoundTripper = tr
	if s := proxySecrets(e.proxy); len(s) > 0 {
		rt = &proxyRedactingTransport{base: tr, secrets: s}
	}
	return &http.Client{Timeout: timeout, Transport: rt}
}

// proxySecrets are the substrings of u that must never be printed.
func proxySecrets(u *url.URL) []string {
	if u == nil || u.User == nil {
		return nil
	}
	out := []string{u.User.String()}
	if p, ok := u.User.Password(); ok && p != "" {
		out = append(out, p, url.QueryEscape(p))
	}
	if n := u.User.Username(); len(n) >= 3 {
		out = append(out, n)
	}
	return out
}

// proxyRedactingTransport scrubs proxy credentials from transport errors
// (defence in depth: net/http does not print proxy userinfo today).
type proxyRedactingTransport struct {
	base    http.RoundTripper
	secrets []string
}

func (t *proxyRedactingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err != nil {
		msg := err.Error()
		clean := msg
		for _, s := range t.secrets {
			clean = strings.ReplaceAll(clean, s, "***")
		}
		if clean != msg {
			err = &redactedError{msg: clean, err: err}
		}
	}
	return resp, err
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the base.
func (t *proxyRedactingTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}

// proxyUserinfo matches the userinfo of every scheme://user:pass@ URL in s.
var proxyUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/@\s]+@`)

// RedactProxyURL replaces the userinfo of every URL in s with "***", so a
// proxy URL (or an error or log line that contains one) can be printed.
func RedactProxyURL(s string) string {
	return proxyUserinfo.ReplaceAllString(s, "${1}***@")
}
