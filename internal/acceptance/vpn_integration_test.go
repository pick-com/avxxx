package acceptance

import (
	"avxxx/internal/catalog"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// forwardingProxy is an HTTP forward proxy in front of the fixture site: it
// checks Proxy-Authorization and forwards every request it gets.
type forwardingProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	n    int
	auth string
}

func newForwardingProxy(t *testing.T, user, pass string) *forwardingProxy {
	p := &forwardingProxy{auth: "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != p.auth {
			http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
			return
		}
		p.mu.Lock()
		p.n++
		p.mu.Unlock()
		out, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		out.Header = r.Header.Clone()
		out.Header.Del("Proxy-Authorization")
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *forwardingProxy) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.n
}

func (s *site) totalHits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, h := range s.hits {
		n += h
	}
	return n
}

func xslistFixture(t *testing.T, file, base string) string {
	t.Helper()
	b, err := os.ReadFile("../catalog/testdata/xslist/" + file)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(b), "https://xslist.org", base)
}

// TestVPNEgressIntegration runs an XSList source end to end through
// store.Claim and crawlrun (exactly as cmd/scraper):
//
//   - egress_policy vpn without any proxy is a config_error and nothing
//     reaches the site;
//   - with proxy_url env:CATALOG_PROXY_ACCEPT every request, robots.txt
//     included, goes through the authenticated proxy, records are saved, and
//     the proxy credentials appear nowhere in the job;
//   - when the site turns to a Cloudflare challenge the source is parked
//     blocked (reason challenge) and the saved data is untouched.
func TestVPNEgressIntegration(t *testing.T) {
	e := newEnv(t)
	if err := e.registry.Register(catalog.NewXSListAdapter()); err != nil {
		t.Fatal(err)
	}
	s := newSite(t)
	s.set("/en/ranking", xslistFixture(t, "discovery/ranking_page2.html", s.url("")))
	s.set("/en/model/90003.html", xslistFixture(t, "performers/model_90001.html", s.url("")))
	proxy := newForwardingProxy(t, "vpnuser", "acceptance-secret")
	proxyURL := strings.Replace(proxy.srv.URL, "http://", "http://vpnuser:acceptance-secret@", 1)
	env := map[string]string{}
	e.runner.Getenv = func(k string) string { return env[k] }

	src := e.addSource("XSList acceptance", catalog.XSListAdapterName, s.url("/en/ranking"), "aggregator")
	e.exec(`UPDATE sources SET egress_policy='vpn' WHERE id=$1`, src)

	o := e.run(src, catalog.PhaseIncremental)
	if o.Status != "failed" || o.ErrorCode != catalog.CodeConfig || !strings.Contains(o.Error, "egress_policy vpn") || s.totalHits() != 0 {
		t.Fatalf("vpn source without a proxy: %s (site hits %d)", o, s.totalHits())
	}

	env["CATALOG_PROXY_ACCEPT"] = proxyURL
	e.exec(`UPDATE source_crawl_policies SET proxy_url='env:CATALOG_PROXY_ACCEPT' WHERE source_id=$1`, src)
	o = e.mustSucceed(e.run(src, catalog.PhaseIncremental), "crawl through the proxy")
	if got := e.int64(`SELECT count(*) FROM source_records WHERE source_id=$1`, src); got != 1 {
		t.Fatalf("records %d (%s)", got, o)
	}
	if s.totalHits() == 0 || proxy.count() != s.totalHits() || s.hitCount("/robots.txt") != 1 {
		t.Fatalf("site hits %d, proxied %d, robots %d: every request must use the proxy", s.totalHits(), proxy.count(), s.hitCount("/robots.txt"))
	}
	if n := e.int64(`SELECT count(*) FROM jobs WHERE source_id=$1 AND (COALESCE(error,'') LIKE '%acceptance-secret%' OR result_counts::text LIKE '%acceptance-secret%')`, src); n != 0 {
		t.Fatal("proxy credentials reached a job row")
	}

	// The site starts challenging this egress.
	challenge := page{status: http.StatusForbidden, header: map[string]string{"cf-mitigated": "challenge"},
		body: `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body></body></html>`}
	s.setPage("/en/ranking", challenge)
	o = e.run(src, catalog.PhaseIncremental)
	if o.ErrorCode != "source_blocked" || e.str(`SELECT blocked_reason FROM sources WHERE id=$1`, src) != "challenge" {
		t.Fatalf("challenged run: %s, blocked_reason %s", o, e.str(`SELECT blocked_reason FROM sources WHERE id=$1`, src))
	}
	if got := e.int64(`SELECT count(*) FROM source_records WHERE source_id=$1`, src); got != 1 {
		t.Fatalf("a challenge changed saved records: %d", got)
	}
	if e.int64(`SELECT count(*) FROM performers WHERE canonical_name='Hanako Gousei' AND birth_date='2000-04-01'`) != 1 {
		t.Fatal("performer data lost behind a challenge")
	}
}
