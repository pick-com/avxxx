package main

import (
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// dryRunStartURLs are the default discovery URLs for -dry-run.
var dryRunStartURLs = map[string]string{
	catalog.DMMAdapterName: catalog.DMMItemListURL,
}

type dryRunOutput struct {
	Adapter  string           `json:"adapter"`
	URL      string           `json:"url"`
	NextPage string           `json:"next_page,omitempty"`
	Records  []catalog.Record `json:"records"`
}

// dryRunCatalog fetches exactly one discovery page through the adapter's
// configured client, maps it and writes the records as JSON to w. It never
// opens the database. Output passes through the adapter's redactor.
func dryRunCatalog(ctx context.Context, w io.Writer, registry *catalog.Registry, name, rawURL string) error {
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
	if rawURL == "" {
		return fmt.Errorf("adapter %q has no default start URL; pass -url", name)
	}
	redactor, _ := adapter.(catalog.Redactor)
	client, err := catalog.HTTPClientFor(adapter, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	fetcher := catalog.NewFetcher(client, catalog.FetchPolicy{MaxPages: 1})
	doc, err := fetcher.Fetch(ctx, rawURL)
	if err != nil {
		return catalog.SanitizeError(err, redactor)
	}
	records, err := adapter.Discover(ctx, doc)
	if err != nil {
		return catalog.SanitizeError(err, redactor)
	}
	out := dryRunOutput{Adapter: adapter.Name(), URL: rawURL, Records: records}
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
