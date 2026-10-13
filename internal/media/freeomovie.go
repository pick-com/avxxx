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

type FreeomovieAdapter struct {
	base   string
	client *http.Client
}

func NewFreeomovieAdapter() Adapter {
	return &FreeomovieAdapter{base: "https://www.freeomovie.to", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *FreeomovieAdapter) Name() string { return "freeomovie" }
func (a *FreeomovieAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/?s=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&paged=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	seen := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?is)<li[^>]+id=["']post-[^>]+>.*?</li>`).FindAllStringSubmatch(b, -1) {
		x := m[0]
		h := firstMatch(x, `(?i)<a[^>]+href=["']([^"']+)["']`)
		if h == "" || !strings.Contains(h, "freeomovie") || seen[h] {
			continue
		}
		t := firstMatch(x, `(?is)<img[^>]+(?:alt|title)=["']([^"']+)`)
		if t == "" {
			t = cleanHTML(firstMatch(x, `(?is)<h[12][^>]*>(.*?)</h[12]>`))
		}
		if t == "" {
			continue
		}
		cover := firstMatch(x, `(?is)<img[^>]+(?:data-src|src)=["']([^"']+)`)
		u := absoluteURL(a.base, html.UnescapeString(h))
		out = append(out, SearchResult{ID: strings.TrimPrefix(u, a.base), Source: a.Name(), Title: html.UnescapeString(t), Cover: absoluteURL(a.base, cover), Type: "video"})
		seen[h] = true
	}
	return out, strings.Contains(strings.ToLower(b), "next page"), nil
}
func (a *FreeomovieAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, e := a.idURL(id)
	if e != nil {
		return nil, e
	}
	b, e := a.get(ctx, u, a.base+"/")
	if e != nil {
		return nil, e
	}
	t := cleanHTML(firstMatch(b, `(?is)<title[^>]*>(.*?)</title>`))
	embed := ""
	for _, m := range regexp.MustCompile(`(?is)<iframe[^>]+src=["']([^"']+)["']`).FindAllStringSubmatch(b, -1) {
		s := absoluteURL(a.base, html.UnescapeString(m[1]))
		if !strings.Contains(s, "freeomovie.to") {
			embed = s
			break
		}
	}
	return &Detail{ID: id, Source: a.Name(), Title: t, EmbedURL: embed, Episodes: []Episode{{ID: id, Title: t}}}, nil
}
func (a *FreeomovieAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("freeomovie direct resolver is not enabled; use the official host player")
}
func (a *FreeomovieAdapter) idURL(id string) (string, error) {
	if strings.HasPrefix(id, "http") {
		u, e := url.Parse(id)
		if e != nil || u.Host != "www.freeomovie.to" {
			return "", errors.New("invalid freeomovie video id")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *FreeomovieAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "", e
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (compatible; avxxx/0.1)")
	r.Header.Set("Referer", ref)
	resp, e := a.client.Do(r)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("freeomovie returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	return string(b), e
}
