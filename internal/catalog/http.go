package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

func FetchDocument(ctx context.Context, client *http.Client, rawURL string) (Document, error) {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Document{}, err
	}
	req.Header.Set("User-Agent", "avxxx-catalog/0.1")
	resp, err := client.Do(req)
	if err != nil {
		return Document{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil {
		return Document{}, err
	}
	if len(body) > 10<<20 {
		return Document{}, fmt.Errorf("document exceeds 10 MiB")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Document{URL: rawURL, Status: resp.StatusCode, Body: body}, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	return Document{URL: rawURL, FinalURL: resp.Request.URL.String(), Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: body}, nil
}
