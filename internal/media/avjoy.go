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

// AvjoyAdapter implements Avjoy's public search/detail pages. Kodi also has
// a local proxy fallback for this site; the WebApp intentionally exposes only
// the direct public MP4 URLs found in the page.
type AvjoyAdapter struct {
	baseURL string
	client  *http.Client
}

func NewAvjoyAdapter() Adapter {
	return &AvjoyAdapter{baseURL: "https://en.avjoy.me", client: &http.Client{Timeout: 20 * time.Second}}
}

func (a *AvjoyAdapter) Name() string { return "avjoy" }

func (a *AvjoyAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.baseURL + "/search/videos/" + url.PathEscape(q)
	if page > 1 {
		raw += fmt.Sprintf("?page=%d", page)
	}
	body, err := a.get(ctx, raw, a.baseURL+"/")
	if err != nil {
		return nil, false, err
	}
	items := a.parseSearch(body)
	return items, regexp.MustCompile(`(?i)aria-label=["']Next|class=["'][^"']*prevnext`).MatchString(body), nil
}

func (a *AvjoyAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, err := a.idURL(id)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(body, `(?is)<title[^>]*>(.*?)</title>`))
	if title == "" {
		title = cleanHTML(firstMatch(body, `(?is)<h1[^>]*>(.*?)</h1>`))
	}
	return &Detail{ID: id, Source: a.Name(), Title: title, Episodes: []Episode{{ID: id, Title: title}}}, nil
}

func (a *AvjoyAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	u, err := a.idURL(id)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	var streams []Stream
	for _, m := range regexp.MustCompile(`(?is)<source\b[^>]*src=["']([^"']+)["'][^>]*>`).FindAllStringSubmatch(body, -1) {
		src := absoluteURL(a.baseURL, html.UnescapeString(strings.TrimSpace(m[1])))
		if !strings.Contains(strings.ToLower(src), ".mp4") || strings.HasSuffix(strings.ToLower(strings.Split(src, "?")[0]), "/403.mp4") {
			continue
		}
		tag := m[0]
		quality := firstMatch(tag, `(?i)(?:res|label)=["']([^"']+)`)
		if !a.probe(ctx, src, u) {
			continue
		}
		streams = append(streams, Stream{URL: src, Protocol: "mp4", Quality: quality, Headers: map[string]string{"Referer": u, "User-Agent": "Mozilla/5.0 (compatible; avxxx/0.1)"}})
	}
	streams = dedupeStreams(streams)
	if len(streams) == 0 {
		return nil, errors.New("avjoy has no direct public stream")
	}
	return &ResolveResult{Streams: streams}, nil
}

func (a *AvjoyAdapter) probe(ctx context.Context, raw, ref string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Range", "bytes=0-1")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; avxxx/0.1)")
	req.Header.Set("Referer", ref)
	resp, err := a.client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300 && strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "video/")
}

func (a *AvjoyAdapter) parseSearch(body string) []SearchResult {
	var out []SearchResult
	seen := map[string]bool{}
	// The page header contains trending-search links to other videos. The
	// actual result grid starts after the search summary block.
	if i := strings.Index(strings.ToLower(body), `class="well-info"`); i >= 0 {
		body = body[i:]
	}
	anchors := regexp.MustCompile(`(?is)<a\b[^>]*href=["']([^"']*/video/[^"']*)["'][^>]*>(.*?)</a>`).FindAllStringSubmatch(body, -1)
	for _, m := range anchors {
		u := absoluteURL(a.baseURL, html.UnescapeString(m[1]))
		if seen[u] {
			continue
		}
		title := cleanHTML(firstMatch(m[0], `(?is)<(?:img|span)[^>]*(?:title|class=["'][^"']*content-title[^"']*["'])=["']([^"']+)`))
		if title == "" {
			title = cleanHTML(m[2])
		}
		if title == "" {
			continue
		}
		cover := firstMatch(m[2], `(?is)<img[^>]+(?:src|data-original)=["']([^"']+)`)
		out = append(out, SearchResult{ID: strings.TrimPrefix(u, a.baseURL), Source: a.Name(), Title: title, Cover: absoluteURL(a.baseURL, html.UnescapeString(cover)), Type: "video"})
		seen[u] = true
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

func (a *AvjoyAdapter) idURL(id string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, err := url.Parse(id)
		if err != nil || u.Host != "en.avjoy.me" {
			return "", errors.New("invalid avjoy video id")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	if !strings.HasPrefix(id, "/video/") {
		return "", errors.New("invalid avjoy video id")
	}
	return a.baseURL + id, nil
}

func (a *AvjoyAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; avxxx/0.1)")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Referer", ref)
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("avjoy returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	return string(b), err
}
