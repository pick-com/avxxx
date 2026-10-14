package media

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type catalogAdapter struct {
	name, base, searchPath, itemPath string
	client                           *http.Client
}

func NewJavguruAdapter() Adapter {
	return newCatalogAdapter("javguru", "https://jav.guru", "/?s=%s", "/")
}
func NewJavhdpornAdapter() Adapter {
	return newCatalogAdapter("javhdporn", "https://www.javhdporn.net", "/?s=%s", "/video/")
}
func NewMissavAdapter() Adapter {
	return newCatalogAdapter("missav", "https://missav123.com", "/en/search/%s", "/en/view/")
}
func newCatalogAdapter(name, base, searchPath, itemPath string) Adapter {
	return &catalogAdapter{name: name, base: base, searchPath: searchPath, itemPath: itemPath, client: &http.Client{Timeout: 25 * time.Second}}
}
func (a *catalogAdapter) Name() string { return a.name }
func (a *catalogAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	u := a.base + fmt.Sprintf(a.searchPath, url.PathEscape(q))
	if page > 1 {
		u += fmt.Sprintf("&paged=%d", page)
	}
	b, err := a.get(ctx, u, a.base+"/")
	if err != nil {
		return nil, false, err
	}
	seen, out := map[string]bool{}, []SearchResult{}
	re := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']+` + regexp.QuoteMeta(a.itemPath) + `[^"']*)["'][^>]*>(.*?)</a>`)
	for _, m := range re.FindAllStringSubmatch(b, -1) {
		link := absoluteURL(a.base, html.UnescapeString(m[1]))
		if seen[link] {
			continue
		}
		title := cleanHTML(firstMatch(m[0], `(?is)(?:title|alt)=["']([^"']+)`))
		if title == "" {
			title = cleanHTML(m[2])
		}
		if title == "" {
			continue
		}
		seen[link] = true
		out = append(out, SearchResult{ID: strings.TrimPrefix(link, a.base), Source: a.name, Title: title, Type: "video"})
	}
	return out, strings.Contains(strings.ToLower(b), "next"), nil
}
func (a *catalogAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u := absoluteURL(a.base, id)
	b, err := a.get(ctx, u, a.base+"/")
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(b, `(?is)<title[^>]*>(.*?)</title>`))
	embed := firstMatch(b, `(?i)<iframe[^>]+src=["']([^"']+)`)
	return &Detail{ID: id, Source: a.name, Title: title, EmbedURL: absoluteURL(u, html.UnescapeString(embed)), Episodes: []Episode{{ID: id, Title: title}}}, nil
}
func (a *catalogAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New(a.name + " direct resolver is not enabled; use the official page or embed")
}
func (a *catalogAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", ref)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s returned HTTP %d", a.name, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(body), err
}
