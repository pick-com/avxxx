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
	"sort"
	"strings"
	"time"
)

// kvsAdapter covers the public KVS-style HTML pages used by CamStreams and
// CamWhores. It intentionally returns direct public MP4 sources only.
type kvsAdapter struct {
	name, base, marker string
	client             *http.Client
}

func NewKVSAdapter(name, base, marker string) Adapter {
	return &kvsAdapter{name: name, base: strings.TrimRight(base, "/"), marker: marker, client: &http.Client{Timeout: 15 * time.Second}}
}
func (a *kvsAdapter) Name() string { return a.name }
func (a *kvsAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/search/" + url.PathEscape(q) + "/"
	if page > 1 {
		raw += fmt.Sprintf("?page=%d", page)
	}
	body, err := a.get(ctx, raw, a.base+"/")
	if err != nil {
		return nil, false, err
	}
	return a.parse(body), strings.Contains(strings.ToLower(body), "next"), nil
}
func (a *kvsAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, err := a.idURL(id)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.base+"/")
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(body, `(?is)<title[^>]*>(.*?)</title>`))
	return &Detail{ID: id, Source: a.name, Title: title, Episodes: []Episode{{ID: id, Title: title}}}, nil
}
func (a *kvsAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	u, err := a.idURL(id)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.base+"/")
	if err != nil {
		return nil, err
	}
	var out []Stream
	for _, m := range regexp.MustCompile(`(?i)(https?://[^"'<>\s]+\.mp4[^"'<>\s]*)`).FindAllStringSubmatch(body, -1) {
		out = append(out, Stream{URL: html.UnescapeString(m[1]), Protocol: "mp4"})
	}
	out = dedupeStreams(out)
	if len(out) == 0 {
		return nil, errors.New(a.name + " has no direct public stream")
	}
	return &ResolveResult{Streams: out}, nil
}
func (a *kvsAdapter) parse(body string) []SearchResult {
	blocks := regexp.MustCompile(`(?is)<(?:div|article)\b[^>]*class=["'][^"']*(?:item|thumb|video)[^"']*["'][^>]*>.*?</(?:div|article)>`).FindAllString(body, -1)
	out := []SearchResult{}
	seen := map[string]bool{}
	for _, b := range blocks {
		h := regexp.MustCompile(`(?i)href=["']([^"']*` + regexp.QuoteMeta(a.marker) + `[^"']*)`).FindStringSubmatch(b)
		if len(h) < 2 {
			continue
		}
		u := absoluteURL(a.base, h[1])
		if seen[u] {
			continue
		}
		t := cleanHTML(firstMatch(b, `(?is)(?:title|alt)=["']([^"']+)`))
		if t == "" {
			t = cleanHTML(firstMatch(b, `(?is)<a[^>]*>(.*?)</a>`))
		}
		if t == "" {
			continue
		}
		out = append(out, SearchResult{ID: strings.TrimPrefix(u, a.base), Source: a.name, Title: t, Type: "video"})
		seen[u] = true
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}
func (a *kvsAdapter) idURL(id string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, e := url.Parse(id)
		if e != nil || u.Host == "" || !strings.HasSuffix(u.Host, a.base[8:]) {
			return "", errors.New("invalid " + a.name + " video id")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	return a.base + id, nil
}
func (a *kvsAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "", e
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; avxxx/0.1)")
	req.Header.Set("Referer", ref)
	resp, e := a.client.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("%s returned HTTP %d", a.name, resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), e
}
