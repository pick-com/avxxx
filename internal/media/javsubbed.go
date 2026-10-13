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
	"strconv"
	"strings"
	"time"
)

type JavsubbedAdapter struct {
	base   string
	client *http.Client
}

func NewJavsubbedAdapter() Adapter {
	return &JavsubbedAdapter{base: "https://javsubbed.net", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *JavsubbedAdapter) Name() string { return "javsubbed" }
func (a *JavsubbedAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/?s=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&paged=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	for _, m := range regexp.MustCompile(`(?is)<article[^>]+class=["'][^"']*post[^"']*["'][^>]*>.*?</article>`).FindAllStringSubmatch(b, -1) {
		x := m[0]
		u := firstMatch(x, `(?i)href=["']([^"']+)["']`)
		t := firstMatch(x, `(?i)title=["']([^"']+)["']`)
		if u == "" || t == "" {
			continue
		}
		cover := firstMatch(x, `(?i)(?:data-src|src)=["']([^"']+)`)
		out = append(out, SearchResult{ID: strings.TrimPrefix(absoluteURL(a.base, html.UnescapeString(u)), a.base), Source: a.Name(), Title: html.UnescapeString(t), Cover: absoluteURL(a.base, cover), Type: "video"})
	}
	return out, strings.Contains(b, "next"), nil
}
func (a *JavsubbedAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
func (a *JavsubbedAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("javsubbed direct resolver requires third-party host resolution")
}
func (a *JavsubbedAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *JavsubbedAdapter) get(ctx context.Context, raw, ref string) (string, error) {
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
		return "", fmt.Errorf("javsubbed returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}

var _ = strconv.Itoa
