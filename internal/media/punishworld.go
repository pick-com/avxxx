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

type PunishworldAdapter struct {
	base   string
	client *http.Client
}

func NewPunishworldAdapter() Adapter {
	return &PunishworldAdapter{base: "https://punishworld.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *PunishworldAdapter) Name() string { return "punishworld" }
func (a *PunishworldAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/?s=" + url.QueryEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("&paged=%d", page)
	}
	b, e := a.get(ctx, raw, a.base+"/")
	if e != nil {
		return nil, false, e
	}
	out := []SearchResult{}
	for _, m := range regexp.MustCompile(`(?is)<div\s+class=["'][^"']*video-block[^"']*["'][^>]*>.*?</div>\s*</div>`).FindAllStringSubmatch(b, -1) {
		x := m[0]
		u := firstMatch(x, `(?i)<a\s+class=["']thumb[^>]+href=["']([^"']+)`)
		t := firstMatch(x, `(?is)<span\s+class=["']title["']>([^<]+)`)
		if u == "" || t == "" {
			continue
		}
		cover := firstMatch(x, `(?i)<img[^>]+(?:data-src|src)=["']([^"']+)`)
		out = append(out, SearchResult{ID: strings.TrimPrefix(absoluteURL(a.base, u), a.base), Source: a.Name(), Title: html.UnescapeString(strings.TrimSpace(t)), Cover: absoluteURL(a.base, cover), Type: "video"})
	}
	return out, strings.Contains(strings.ToLower(b), "next"), nil
}
func (a *PunishworldAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
func (a *PunishworldAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("punishworld direct MP4 requires source headers; use the source player")
}
func (a *PunishworldAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *PunishworldAdapter) get(ctx context.Context, raw, ref string) (string, error) {
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
		return "", fmt.Errorf("punishworld returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
