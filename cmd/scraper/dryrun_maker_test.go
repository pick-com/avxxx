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
	"testing"
)

// TestDryRunMakerProfile runs `-dry-run -adapter maker:<profile>` without
// -url: the profile's listing_url is fetched (once, with robots.txt checked)
// and the discovered records are printed. The profile points at a local
// server serving the synthetic s1 listing fixture.
func TestDryRunMakerProfile(t *testing.T) {
	listing, err := os.ReadFile("../../internal/catalog/testdata/maker-s1/listing_page1.html")
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.RequestURI())
		if r.URL.Path == "/works/date" {
			_, _ = w.Write(listing)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	var p catalog.MakerProfile
	for _, b := range catalog.BuiltinMakerProfiles() {
		if b.Slug == "s1" {
			p = b
		}
	}
	p.ListingURL = strings.Replace(p.ListingURL, p.BaseURL, srv.URL, 1)
	p.BaseURL = srv.URL
	a, err := catalog.NewMakerAdapter(p)
	if err != nil {
		t.Fatal(err)
	}
	r := catalog.NewRegistry()
	if err := r.Register(a); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := dryRunCatalog(context.Background(), &out, r, "maker:s1", "", testEnv); err != nil {
		t.Fatal(err)
	}
	var got dryRunOutput
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Adapter != "maker:s1" || got.URL != srv.URL+"/works/date" || len(got.Records) != 3 || got.NextPage != srv.URL+"/works/date?page=2" {
		t.Fatalf("output %+v", got)
	}
	if strings.Join(hits, ",") != "/robots.txt,/works/date" {
		t.Fatalf("requests %v", hits)
	}
}

// The worker registry carries the built-in maker profiles.
func TestNewRegistryHasMakers(t *testing.T) {
	t.Setenv(catalog.MakerProfilesEnv, "")
	r := newRegistry()
	for _, name := range []string{"maker:s1", "maker:ideapocket"} {
		if _, ok := r.Get(name); !ok {
			t.Errorf("%s not registered", name)
		}
	}
}
