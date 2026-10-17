package main

import (
	"avxxx/internal/catalog"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

// TestDryRunXSListAndJAVLibraryViaProxy runs `-dry-run -adapter xslist|javlibrary
// -proxy env:CATALOG_PROXY_VPN`: the page and robots.txt are fetched through
// the proxy (an httptest forward proxy serving the synthetic fixtures for
// the sites' hosts), the output names the egress with the credentials
// redacted, and the records are mapped.
func TestDryRunXSListAndJAVLibraryViaProxy(t *testing.T) {
	read := func(p string) []byte {
		b, err := os.ReadFile("../../internal/catalog/testdata/" + p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	pages := map[string][]byte{
		"http://xslist.org/en/ranking":                     read("xslist/discovery/ranking_page1.html"),
		"http://www.javlibrary.com/en/vl_newrelease.php":   read("javlibrary/discovery/newrelease_page1.html"),
		"http://xslist.org/robots.txt":                     []byte("User-agent: *\nAllow: /\n"),
		"http://www.javlibrary.com/robots.txt":             []byte("User-agent: *\nAllow: /\n"),
		"http://challenged.javlibrary.com/en/?v=javli4abc": nil,
	}
	var mu sync.Mutex
	var seen []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.String())
		mu.Unlock()
		if r.Header.Get("Proxy-Authorization") == "" {
			http.Error(w, "auth", http.StatusProxyAuthRequired)
			return
		}
		if r.URL.Host == "challenged.javlibrary.com" {
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("<title>Just a moment...</title>"))
			return
		}
		b, ok := pages[r.URL.String()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(proxy.Close)
	env := map[string]string{"CATALOG_CONTACT": "ops@example.com",
		"CATALOG_PROXY_VPN": strings.Replace(proxy.URL, "http://", "http://vpn:dry-run-secret@", 1)}
	getenv := func(k string) string { return env[k] }

	for adapter, u := range map[string]string{catalog.XSListAdapterName: "http://xslist.org/en/ranking", catalog.JAVLibraryAdapterName: "http://www.javlibrary.com/en/vl_newrelease.php"} {
		var out bytes.Buffer
		if err := dryRunCatalogVia(context.Background(), &out, newRegistry(), adapter, u, "env:CATALOG_PROXY_VPN", getenv); err != nil {
			t.Fatalf("%s: %v", adapter, err)
		}
		if strings.Contains(out.String(), "dry-run-secret") {
			t.Fatalf("%s: output leaks proxy credentials", adapter)
		}
		var got dryRunOutput
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got.Egress, "***@") || len(got.Records) != 2 || got.NextPage == "" {
			t.Fatalf("%s: egress %q records %d next %q", adapter, got.Egress, len(got.Records), got.NextPage)
		}
	}
	mu.Lock()
	n := len(seen)
	mu.Unlock()
	if n != 4 { // two robots.txt + two pages, all through the proxy
		t.Fatalf("proxied requests %d: %v", n, seen)
	}

	// A challenged egress: the dry run reports the block and maps nothing.
	err := dryRunCatalogVia(context.Background(), &bytes.Buffer{}, newRegistry(), catalog.JAVLibraryAdapterName, "http://challenged.javlibrary.com/en/?v=javli4abc", "env:CATALOG_PROXY_VPN", getenv)
	if b, ok := catalog.IsBlocked(err); !ok || b.Reason != "challenge" {
		t.Fatalf("challenge: %v", err)
	}
	// A missing proxy variable never falls back to the direct route.
	if err := dryRunCatalogVia(context.Background(), &bytes.Buffer{}, newRegistry(), catalog.XSListAdapterName, "", "env:CATALOG_PROXY_MISSING", getenv); err == nil || !strings.Contains(err.Error(), catalog.CodeConfig) {
		t.Fatalf("missing proxy variable: %v", err)
	}
}
