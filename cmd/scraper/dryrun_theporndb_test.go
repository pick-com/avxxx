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

func TestDryRunThePornDBNotConfigured(t *testing.T) {
	t.Setenv("THEPORNDB_API_TOKEN", "")
	var out bytes.Buffer
	err := dryRunCatalog(context.Background(), &out, newRegistry(), "theporndb", "", testEnv)
	if !errors.Is(err, catalog.ErrNotConfigured) || out.Len() != 0 {
		t.Fatalf("err = %v, out = %q", err, out.String())
	}
}

// TestDryRunThePornDBOnePage fetches one GET /jav page from a fake API that
// echoes the bearer token and checks the printed JSON carries no token.
func TestDryRunThePornDBOnePage(t *testing.T) {
	const token = "dryRunTPDBtoken42"
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		requests++
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		fmt.Fprintf(w, `{"data":[{"id":"5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01","title":"t %s","sku":"SONE-156","date":"2024-04-23"}],
"meta":{"total":3,"per_page":1,"current_page":1,"last":3}}`, r.Header.Get("Authorization"))
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	r := catalog.NewRegistry()
	if err := r.Register(catalog.NewThePornDBAdapterWith(token, u.Host)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := dryRunCatalog(context.Background(), &out, r, "theporndb", srv.URL+"/jav?orderBy=recently_released&per_page=1", testEnv); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d", requests)
	}
	if strings.Contains(out.String(), token) {
		t.Fatalf("token printed: %s", out.String())
	}
	var got dryRunOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 1 || got.Records[0].Fields["dvd_code"] != "SONE-156" || !strings.Contains(got.NextPage, "page=2") {
		t.Fatalf("output %+v", got)
	}
	if dryRunStartURLs["theporndb"] != catalog.ThePornDBJAVListURL {
		t.Fatal("theporndb has no default dry-run URL")
	}
}
