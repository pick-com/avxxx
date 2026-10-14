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
	out := []SearchResult{}
	seen := map[string]bool{}
	// KVS installations can return both video cards and album cards. Parse
	// the anchors directly instead of relying on nested card div boundaries,
	// which vary between themes and can contain nested divs.
	anchors := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*/(?:videos|albums|watch)/[^"']*)["'][^>]*>(.*?)</a>`).FindAllStringSubmatch(body, -1)
	for _, m := range anchors {
		u := absoluteURL(a.base, html.UnescapeString(m[1]))
		if seen[u] {
			continue
		}
		anchor := m[0]
		t := cleanHTML(firstMatch(anchor, `(?is)title=["']([^"']+)`))
		if t == "" {
			t = cleanHTML(m[2])
		}
		if t == "" {
			continue
		}
		cover := firstMatch(m[2], `(?is)(?:data-original|src)=["']([^"']+)`)
		if strings.HasPrefix(cover, "data:") {
			cover = firstMatch(m[2], `(?is)data-original=["']([^"']+)`)
		}
		kind := "video"
		if strings.Contains(strings.ToLower(u), "/albums/") {
			kind = "album"
		}
		id := strings.TrimPrefix(strings.TrimPrefix(u, a.base), "/")
		out = append(out, SearchResult{ID: id, Source: a.name, Title: t, Cover: absoluteURL(a.base, html.UnescapeString(cover)), Type: kind})
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
