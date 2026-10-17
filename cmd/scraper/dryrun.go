package main

import (
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

// dryRunStartURLs are the default discovery URLs for -dry-run.
var dryRunStartURLs = map[string]string{
	catalog.DMMAdapterName:       catalog.DMMItemListURL,
	catalog.ThePornDBAdapterName: catalog.ThePornDBJAVListURL,
}

type dryRunOutput struct {
	Adapter string `json:"adapter"`
	URL     string `json:"url"`
	// Egress is the network path the fetch took (proxy credentials
	// redacted), so an operator can confirm the VPN proxy was used.
	Egress   string           `json:"egress"`
	NextPage string           `json:"next_page,omitempty"`
	Records  []catalog.Record `json:"records"`
}

// dryRunCatalog fetches exactly one discovery page through the adapter's
// configured client, maps it and writes the records as JSON to w. It never
// opens the database. Output passes through the adapter's redactor.
//
// A dry run is a manual check, so it follows the verify-phase rules: one
// page, the contact User-Agent (CATALOG_CONTACT / CATALOG_USER_AGENT from
// getenv) is required, and robots.txt is honoured with an unreachable
// robots.txt tolerated (Fetcher.Automatic=false); a 404 means no
// restrictions. Without the database it cannot read license_status; the
// license gate applies when the source runs as a job.
//
// Egress: the -proxy value when given (same syntax as a source's crawl
// policy proxy_url: direct, env:CATALOG_PROXY_<X> or a credential-free
// proxy URL), else CATALOG_PROXY_URL, else HTTPS_PROXY / HTTP_PROXY. robots.txt
// is read through the same egress as the page.
func dryRunCatalog(ctx context.Context, w io.Writer, registry *catalog.Registry, name, rawURL string, getenv func(string) string) error {
	return dryRunCatalogVia(ctx, w, registry, name, rawURL, "", getenv)
}

func dryRunCatalogVia(ctx context.Context, w io.Writer, registry *catalog.Registry, name, rawURL, proxy string, getenv func(string) string) error {
	if name == "" {
		return errors.New("-dry-run needs -adapter")
	}
	adapter, ok := registry.Get(name)
	if !ok {
		return fmt.Errorf("unknown catalog adapter %q", name)
	}
	if rawURL == "" {
		rawURL = dryRunStartURLs[adapter.Name()]
	}
	if s, ok := adapter.(interface{ StartURL() string }); ok && rawURL == "" {
		rawURL = s.StartURL() // maker:<slug>: the profile's listing_url
	}
	if rawURL == "" {
		return fmt.Errorf("adapter %q has no default start URL; pass -url", name)
	}
	redactor, _ := adapter.(catalog.Redactor)
	egress, err := catalog.ResolveEgress(proxy, getenv)
	if err != nil {
		return fmt.Errorf("%s: egress: %s", catalog.CodeConfig, catalog.RedactProxyURL(err.Error()))
	}
	client, err := catalog.HTTPClientFor(adapter, catalog.NewHTTPClient(30*time.Second, egress))
	if err != nil {
		return err
	}
	ua, err := catalog.UserAgentFromEnv(getenv)
	if err != nil {
		return fmt.Errorf("%s: %w", catalog.CodeConfig, err)
	}
	fetcher := catalog.NewFetcher(client, catalog.FetchPolicy{MaxPages: 1})
	fetcher.UserAgent = ua
	fetcher.Robots = catalog.NewRobots(catalog.NewHTTPClient(20*time.Second, egress), ua)
	fetcher.Automatic = false // manual verify: an unreachable robots.txt does not block
	doc, err := fetcher.Fetch(ctx, rawURL)
	if err != nil {
		return catalog.SanitizeError(err, redactor)
	}
	records, err := adapter.Discover(ctx, doc)
	if err != nil {
		return catalog.SanitizeError(err, redactor)
	}
	out := dryRunOutput{Adapter: adapter.Name(), URL: rawURL, Egress: egress.Desc, Records: records}
	if p, ok := adapter.(catalog.Paginator); ok {
		out.NextPage = p.NextPage(doc)
	}
	for i := range out.Records {
		out.Records[i] = catalog.RedactRecord(redactor, out.Records[i])
	}
	if redactor != nil {
		out.URL, out.NextPage = redactor.Redact(out.URL), redactor.Redact(out.NextPage)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(out)
}
