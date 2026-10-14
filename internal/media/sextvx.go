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

type SextvxAdapter struct {
	base   string
	client *http.Client
}

func NewSextvxAdapter() Adapter {
	return &SextvxAdapter{base: "https://www.sextvx.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *SextvxAdapter) Name() string { return "sextvx" }
func (a *SextvxAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/results?search_query=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&page=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	for _, m := range regexp.MustCompile(`(?is)<div class=["']video rotate["'][^>]*>.*?</div>\s*</div>`).FindAllStringSubmatch(b, -1) {
		x := m[0]
		u := firstMatch(x, `(?i)href=["']((?:https?://www\.sextvx\.com)?/video/[^"']+)`)
		t := firstMatch(x, `(?i)<h3[^>]*>.*?title=["']([^"']+)`)
		if u == "" {
			continue
		}
		if t == "" {
			t = cleanHTML(firstMatch(x, `(?is)<img[^>]+alt=["']([^"']+)`))
		}
		out = append(out, SearchResult{ID: strings.TrimPrefix(absoluteURL(a.base, html.UnescapeString(u)), a.base), Source: a.Name(), Title: html.UnescapeString(t), Type: "video"})
	}
	return out, strings.Contains(strings.ToLower(b), "next"), nil
}
func (a *SextvxAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
func (a *SextvxAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("sextvx direct stream requires source headers")
}
func (a *SextvxAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	if !strings.HasPrefix(id, "/video/") {
		return "", errors.New("invalid sextvx video id")
	}
	return a.base + id, nil
}
func (a *SextvxAdapter) get(ctx context.Context, raw, ref string) (string, error) {
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
		return "", fmt.Errorf("sextvx returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
