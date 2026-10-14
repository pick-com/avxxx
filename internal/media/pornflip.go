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

type PornflipAdapter struct {
	base   string
	client *http.Client
}

func NewPornflipAdapter() Adapter {
	return &PornflipAdapter{base: "https://www.pornflip.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *PornflipAdapter) Name() string { return "pornflip" }
func (a *PornflipAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/search?q=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&page=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	for _, m := range regexp.MustCompile(`(?is)<div class=["'][^"']*card[^"']*itemVideo[^"']*["'][^>]*>.*?</div>\s*</div>`).FindAllStringSubmatch(b, -1) {
		x := m[0]
		u := firstMatch(x, `(?i)<a href=["']([^"']+)["']`)
		t := cleanHTML(firstMatch(x, `(?is)<h5[^>]*>(.*?)</h5>`))
		if u == "" || t == "" {
			continue
		}
		cover := firstMatch(x, `(?i)<img[^>]+src=["']([^"']+)`)
		out = append(out, SearchResult{ID: strings.TrimPrefix(absoluteURL(a.base, u), a.base), Source: a.Name(), Title: html.UnescapeString(t), Cover: absoluteURL(a.base, cover), Type: "video"})
	}
	return out, strings.Contains(strings.ToLower(b), "rel=\"next\""), nil
}
func (a *PornflipAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
func (a *PornflipAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("pornflip HLS requires source headers; use the source player")
}
func (a *PornflipAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *PornflipAdapter) get(ctx context.Context, raw, ref string) (string, error) {
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
		return "", fmt.Errorf("pornflip returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
