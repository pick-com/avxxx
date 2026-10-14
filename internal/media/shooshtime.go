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

type ShooshTimeAdapter struct {
	base   string
	client *http.Client
}

func NewShooshTimeAdapter() Adapter {
	return &ShooshTimeAdapter{base: "https://shooshtime.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *ShooshTimeAdapter) Name() string { return "shooshtime" }
func (a *ShooshTimeAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/search/?q=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&page=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	for _, m := range regexp.MustCompile(`(?is)<a[^>]+href=["']([^"']*/videos?/[^"']+)["'][^>]*>.*?</a>`).FindAllStringSubmatch(b, -1) {
		u := m[1]
		t := cleanHTML(m[0])
		if t == "" {
			continue
		}
		out = append(out, SearchResult{ID: strings.TrimPrefix(absoluteURL(a.base, html.UnescapeString(u)), a.base), Source: a.Name(), Title: t, Type: "video"})
	}
	return out, strings.Contains(strings.ToLower(b), "next"), nil
}
func (a *ShooshTimeAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, e := a.idURL(id)
	if e != nil {
		return nil, e
	}
	b, e := a.get(ctx, u, a.base+"/")
	if e != nil {
		return nil, e
	}
	t := cleanHTML(firstMatch(b, `(?is)<title[^>]*>(.*?)</title>`))
	return &Detail{ID: id, Source: a.Name(), Title: t, Episodes: []Episode{{ID: id, Title: t}}}, nil
}
func (a *ShooshTimeAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	u, e := a.idURL(id)
	if e != nil {
		return nil, e
	}
	b, e := a.get(ctx, u, a.base+"/")
	if e != nil {
		return nil, e
	}
	out := []Stream{}
	for _, m := range regexp.MustCompile(`(?i)(https?://[^"'\s]+\.mp4[^"'\s]*)`).FindAllStringSubmatch(b, -1) {
		out = append(out, Stream{URL: html.UnescapeString(m[1]), Protocol: "mp4"})
	}
	out = dedupeStreams(out)
	if len(out) == 0 {
		return nil, errors.New("shooshtime has no direct public stream")
	}
	return &ResolveResult{Streams: out}, nil
}
func (a *ShooshTimeAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *ShooshTimeAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "", e
	}
	r.Header.Set("User-Agent", "Mozilla/5.0")
	r.Header.Set("Referer", ref)
	x, e := a.client.Do(r)
	if e != nil {
		return "", e
	}
	defer x.Body.Close()
	if x.StatusCode != 200 {
		return "", fmt.Errorf("shooshtime returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
