package catalog

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// forwardProxy is an httptest HTTP forward proxy: it answers absolute-URI
// requests itself and records what it saw.
type forwardProxy struct {
	*httptest.Server
	mu    sync.Mutex
	urls  []string
	auths []string
}

func newForwardProxy(t *testing.T) *forwardProxy {
	t.Helper()
	p := &forwardProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.urls = append(p.urls, r.URL.String())
		p.auths = append(p.auths, r.Header.Get("Proxy-Authorization"))
		p.mu.Unlock()
		fmt.Fprint(w, "<html><body>via proxy</body></html>")
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *forwardProxy) seen() ([]string, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string{}, p.urls...), append([]string{}, p.auths...)
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func fetchVia(t *testing.T, e Egress, target string) Document {
	t.Helper()
	doc, err := NewFetcher(NewHTTPClient(5*time.Second, e), FetchPolicy{}).Fetch(context.Background(), target)
	if err != nil {
		t.Fatalf("fetch %s: %v", target, err)
	}
	return doc
}

func TestGlobalProxyCarriesRequestsAndRedacts(t *testing.T) {
	p := newForwardProxy(t)
	proxyURL := strings.Replace(p.URL, "http://", "http://vpnuser:s3cret-pass@", 1)
	e, err := ResolveEgress("", envOf(map[string]string{ProxyEnv: proxyURL, "HTTPS_PROXY": "http://ignored.example:1"}))
	if err != nil {
		t.Fatal(err)
	}
	if e.Mode != EgressGlobal || !e.Proxied() {
		t.Fatalf("egress %+v", e)
	}
	if strings.Contains(e.Desc, "s3cret") || strings.Contains(e.Desc, "vpnuser") || !strings.Contains(e.Desc, "***@") {
		t.Fatalf("desc leaks credentials: %q", e.Desc)
	}
	doc := fetchVia(t, e, "http://catalog.example.test/en/ranking")
	if !strings.Contains(string(doc.Body), "via proxy") {
		t.Fatalf("body %q", doc.Body)
	}
	urls, auths := p.seen()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("vpnuser:s3cret-pass"))
	if len(urls) != 1 || urls[0] != "http://catalog.example.test/en/ranking" || auths[0] != want {
		t.Fatalf("proxy saw %v %v", urls, auths)
	}
}

func TestSourceProxyOverridesAndDirect(t *testing.T) {
	global, vpn := newForwardProxy(t), newForwardProxy(t)
	env := envOf(map[string]string{ProxyEnv: global.URL, "CATALOG_PROXY_VPN": vpn.URL})

	e, err := ResolveEgress("env:CATALOG_PROXY_VPN", env)
	if err != nil || e.Mode != EgressSource {
		t.Fatalf("%+v %v", e, err)
	}
	fetchVia(t, e, "http://xslist.example.test/en/model/1.html")
	if g, _ := global.seen(); len(g) != 0 {
		t.Fatalf("source proxy must win over %s: global saw %v", ProxyEnv, g)
	}
	if v, _ := vpn.seen(); len(v) != 1 {
		t.Fatalf("vpn proxy saw %v", v)
	}

	// A credential-free URL is accepted inline.
	if e, err = ResolveEgress(vpn.URL, env); err != nil || e.Mode != EgressSource {
		t.Fatalf("%+v %v", e, err)
	}

	// "direct" ignores every proxy setting.
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "direct") }))
	t.Cleanup(site.Close)
	if e, err = ResolveEgress(ProxyDirect, env); err != nil || e.Mode != EgressDirect || e.Proxied() {
		t.Fatalf("%+v %v", e, err)
	}
	if doc := fetchVia(t, e, site.URL+"/x"); string(doc.Body) != "direct" {
		t.Fatalf("body %q", doc.Body)
	}
	if g, _ := global.seen(); len(g) != 0 {
		t.Fatalf("direct went through the proxy: %v", g)
	}
}

func TestStandardProxyEnvironmentFallback(t *testing.T) {
	p := newForwardProxy(t)
	e, err := ResolveEgress("", envOf(map[string]string{"HTTP_PROXY": p.URL, "NO_PROXY": "skip.example.test"}))
	if err != nil || e.Mode != EgressEnvironment || !e.Proxied() {
		t.Fatalf("%+v %v", e, err)
	}
	fetchVia(t, e, "http://catalog.example.test/a")
	if u, _ := p.seen(); len(u) != 1 {
		t.Fatalf("HTTP_PROXY not used: %v", u)
	}
	// NO_PROXY is honoured: the request goes direct (and fails to resolve).
	if _, err := NewFetcher(NewHTTPClient(2*time.Second, e), FetchPolicy{}).Fetch(context.Background(), "http://skip.example.test/a"); err == nil {
		t.Fatal("NO_PROXY host reached the proxy")
	}
	if u, _ := p.seen(); len(u) != 1 {
		t.Fatalf("NO_PROXY host went through the proxy: %v", u)
	}
	// Lower-case forms count too.
	if e, err = ResolveEgress("", envOf(map[string]string{"https_proxy": "socks5://127.0.0.1:1"})); err != nil || e.Mode != EgressEnvironment {
		t.Fatalf("%+v %v", e, err)
	}
	if e, err = ResolveEgress("", nil); err != nil || e.Mode != EgressNone || e.Proxied() {
		t.Fatalf("%+v %v", e, err)
	}
}

// socks5Server is a minimal RFC 1928/1929 server (CONNECT, user/pass auth)
// that dials the requested target.
func socks5Server(t *testing.T, user, pass string) (addr string, connects *[]string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	seen := []string{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 262)
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				if _, err := io.ReadFull(c, buf[:buf[1]]); err != nil {
					return
				}
				c.Write([]byte{5, 2}) // username/password
				if _, err := io.ReadFull(c, buf[:2]); err != nil {
					return
				}
				u := make([]byte, buf[1])
				io.ReadFull(c, u)
				io.ReadFull(c, buf[:1])
				pw := make([]byte, buf[0])
				io.ReadFull(c, pw)
				if string(u) != user || string(pw) != pass {
					c.Write([]byte{1, 1})
					return
				}
				c.Write([]byte{1, 0})
				if _, err := io.ReadFull(c, buf[:4]); err != nil {
					return
				}
				var host string
				switch buf[3] {
				case 1:
					io.ReadFull(c, buf[:4])
					host = net.IP(buf[:4]).String()
				case 3:
					io.ReadFull(c, buf[:1])
					h := make([]byte, buf[0])
					io.ReadFull(c, h)
					host = string(h)
				default:
					return
				}
				io.ReadFull(c, buf[:2])
				target := net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(buf[:2])))
				mu.Lock()
				seen = append(seen, target)
				mu.Unlock()
				up, err := net.Dial("tcp", target)
				if err != nil {
					c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
					return
				}
				defer up.Close()
				c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				go io.Copy(up, c)
				io.Copy(c, up)
			}(c)
		}
	}()
	return ln.Addr().String(), &seen
}

func TestSOCKS5Proxy(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "socks ok") }))
	t.Cleanup(site.Close)
	addr, seen := socks5Server(t, "vpn", "pw-123")
	e, err := ResolveEgress("env:CATALOG_PROXY_SOCKS", envOf(map[string]string{"CATALOG_PROXY_SOCKS": "socks5://vpn:pw-123@" + addr}))
	if err != nil {
		t.Fatal(err)
	}
	if doc := fetchVia(t, e, site.URL+"/x"); string(doc.Body) != "socks ok" {
		t.Fatalf("body %q", doc.Body)
	}
	if len(*seen) != 1 || (*seen)[0] != strings.TrimPrefix(site.URL, "http://") {
		t.Fatalf("socks connects %v", *seen)
	}
	// Wrong credentials fail without echoing them.
	bad, _ := ResolveEgress("socks5://"+addr, nil)
	if _, err := NewFetcher(NewHTTPClient(2*time.Second, bad), FetchPolicy{}).Fetch(context.Background(), site.URL+"/x"); err == nil {
		t.Fatal("unauthenticated socks request succeeded")
	}
}

func TestProxyErrorsNeverLeakCredentials(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close() // nothing listens: the proxy connect fails
	for _, raw := range []string{"http://agent007:hunter2-secret@" + dead, "socks5://agent007:hunter2-secret@" + dead} {
		e, err := ResolveEgress("", envOf(map[string]string{ProxyEnv: raw}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewFetcher(NewHTTPClient(2*time.Second, e), FetchPolicy{}).Fetch(context.Background(), "http://catalog.example.test/x")
		if err == nil {
			t.Fatal("dead proxy succeeded")
		}
		if msg := err.Error() + e.Desc; strings.Contains(msg, "hunter2") || strings.Contains(msg, "agent007") {
			t.Fatalf("credentials leaked: %s", msg)
		}
	}
	// Invalid values are rejected without echoing them.
	if _, err := ResolveEgress("", envOf(map[string]string{ProxyEnv: "ftp://agent007:hunter2-secret@host:21"})); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("invalid scheme: %v", err)
	}
	if got := RedactProxyURL("proxyconnect via socks5h://u:p@h:1080 and http://x:y@z/ ok"); got != "proxyconnect via socks5h://***@h:1080 and http://***@z/ ok" {
		t.Fatalf("redact %q", got)
	}
}

func TestValidateSourceProxy(t *testing.T) {
	for v, ok := range map[string]bool{
		"":                               true,
		"direct":                         true,
		"env:CATALOG_PROXY_VPN":          true,
		"socks5://10.8.0.1:1080":         true,
		"socks5h://vpn-gw:1080":          true,
		"http://10.8.0.1:3128":           true,
		"https://proxy.example:443":      true,
		"http://user:pass@10.8.0.1:3128": false, // credentials never stored
		"socks5://user@10.8.0.1:1080":    false,
		"env:DATABASE_URL":               false, // only CATALOG_PROXY_* variables
		"env:catalog_proxy_vpn":          false,
		"ftp://10.8.0.1:21":              false,
		"http://10.8.0.1:3128/path":      false,
		"10.8.0.1:3128":                  false,
		"socks5://":                      false,
	} {
		if err := ValidateSourceProxy(v); (err == nil) != ok {
			t.Errorf("%q: err %v, want ok=%v", v, err, ok)
		}
	}
	if _, err := ResolveEgress("env:CATALOG_PROXY_MISSING", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("missing variable must be a config error, never direct: %v", err)
	}
	if _, err := ResolveEgress("", envOf(map[string]string{ProxyEnv: "not a url"})); err == nil {
		t.Fatal("an invalid CATALOG_PROXY_URL must not fall back to direct")
	}
}
