package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type PornbusyAdapter struct {
	base   string
	client *http.Client
}

func NewPornbusyAdapter() Adapter {
	return &PornbusyAdapter{base: "https://pornbusy.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *PornbusyAdapter) Name() string { return "pornbusy" }
func (a *PornbusyAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/wp-json/wp/v2/search?search=" + url.QueryEscape(q) + "&per_page=20&page=" + strconv.Itoa(page)
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	var hits []struct {
		ID    int
		Title string
		URL   string
	}
	if e = json.Unmarshal([]byte(b), &hits); e != nil {
		return nil, false, fmt.Errorf("invalid pornbusy response: %w", e)
	}
	out := []SearchResult{}
	for _, h := range hits {
		if h.URL == "" {
			continue
		}
		out = append(out, SearchResult{ID: strings.TrimPrefix(h.URL, a.base), Source: a.Name(), Title: cleanHTML(html.UnescapeString(h.Title)), Type: "video"})
	}
	return out, len(hits) >= 20, nil
}
func (a *PornbusyAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
	if m := firstMatch(b, `(?is)<iframe[^>]+src=["']([^"']+)["']`); m != "" {
		embed = absoluteURL(u, m)
	}
	return &Detail{ID: id, Source: a.Name(), Title: t, EmbedURL: embed, Episodes: []Episode{{ID: id, Title: t}}}, nil
}
func (a *PornbusyAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("pornbusy direct resolver requires third-party host resolution; use the official embed page")
}
func (a *PornbusyAdapter) idURL(id string) (string, error) {
	if strings.HasPrefix(id, "http") {
		u, e := url.Parse(id)
		if e != nil || u.Host != "pornbusy.com" {
			return "", errors.New("invalid pornbusy video id")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *PornbusyAdapter) get(ctx context.Context, raw, ref string) (string, error) {
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
		return "", fmt.Errorf("pornbusy returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
