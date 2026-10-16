package main

import (
	"avxxx/internal/catalog"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// testEnv supplies the crawler contact a dry run requires.
func testEnv(k string) string {
	if k == "CATALOG_CONTACT" {
		return "ops@example.test"
	}
	return ""
}

func TestDryRunNotConfigured(t *testing.T) {
	t.Setenv("DMM_API_ID", "")
	t.Setenv("DMM_AFFILIATE_ID", "")
	var out bytes.Buffer
	err := dryRunCatalog(context.Background(), &out, newRegistry(), "dmm", "", testEnv)
	if !errors.Is(err, catalog.ErrNotConfigured) || out.Len() != 0 {
		t.Fatalf("err = %v, out = %q", err, out.String())
	}
}

func TestDryRunNeedsAdapterAndURL(t *testing.T) {
	r := newRegistry()
	if err := dryRunCatalog(context.Background(), &bytes.Buffer{}, r, "", "", testEnv); err == nil {
		t.Fatal("missing -adapter accepted")
	}
	if err := dryRunCatalog(context.Background(), &bytes.Buffer{}, r, "javdatabase", "", testEnv); err == nil {
		t.Fatal("adapter without default URL accepted without -url")
	}
	if err := dryRunCatalog(context.Background(), &bytes.Buffer{}, r, "nope", "http://x", testEnv); err == nil {
		t.Fatal("unknown adapter accepted")
	}
}

// TestDryRunPrintsOnePageWithoutCredentials fetches one page from a fake API
// that echoes credentials (like the real one) and checks the printed JSON.
func TestDryRunPrintsOnePageWithoutCredentials(t *testing.T) {
	creds := catalog.DMMCredentials{APIID: "DRYRUNAPIIDx1", AffiliateID: "dryrun-990"}
	requests, robotsRequests := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			robotsRequests++
			http.NotFound(w, r) // 404: no restrictions
			return
		}
		requests++
		if ua := r.Header.Get("User-Agent"); !strings.Contains(ua, "ops@example.test") {
			t.Errorf("User-Agent %q lacks the crawler contact", ua)
		}
		q := r.URL.Query()
		fmt.Fprintf(w, `{"request":{"parameters":{"api_id":%q,"affiliate_id":%q}},"result":{"status":200,"result_count":1,"total_count":5,"first_position":1,
"items":[{"content_id":"sone00156","title":"t","URL":"https://video.dmm.co.jp/av/content/?id=sone00156","affiliateURL":"https://al.fanza.co.jp/?lurl=x&af_id=%s&ch=api","date":"2024-04-23 10:00:00","maker_product":"SONE-156"}]}}`,
			q.Get("api_id"), q.Get("affiliate_id"), q.Get("affiliate_id"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	r := catalog.NewRegistry()
	if err := r.Register(catalog.NewDMMAdapterWith(creds, u.Host)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := dryRunCatalog(context.Background(), &out, r, "dmm", srv.URL+"/affiliate/v3/ItemList?hits=1&sort=date&site=FANZA&output=json", testEnv); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || robotsRequests != 1 {
		t.Fatalf("requests = %d, robots.txt requests = %d, want exactly one each", requests, robotsRequests)
	}
	s := out.String()
	if strings.Contains(s, creds.APIID) || strings.Contains(s, creds.AffiliateID) {
		t.Fatalf("credentials printed:\n%s", s)
	}
	var got dryRunOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0].Fields["dvd_code"] != "SONE-156" || !strings.Contains(got.NextPage, "offset=2") {
		t.Fatalf("output = %s", s)
	}
}

// A dry run is a manual verify: like verify jobs it needs the crawler
// contact, and fails before any request without it.
func TestDryRunNeedsContact(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	r := catalog.NewRegistry()
	if err := r.Register(catalog.NewDMMAdapterWith(catalog.DMMCredentials{APIID: "x", AffiliateID: "y-990"}, u.Host)); err != nil {
		t.Fatal(err)
	}
	err := dryRunCatalog(context.Background(), &bytes.Buffer{}, r, "dmm", srv.URL+"/affiliate/v3/ItemList?hits=1", func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "CATALOG_CONTACT") || requests != 0 {
		t.Fatalf("err = %v, requests = %d", err, requests)
	}
}
