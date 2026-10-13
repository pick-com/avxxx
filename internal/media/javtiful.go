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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// JavtifulAdapter ports the public listing/detail/recording flow from the
// installed AdultHideout adapter. It deliberately does not implement any
// third-party resolver or access-control bypass.
type JavtifulAdapter struct {
	client    *http.Client
	baseURL   string
	userAgent string
}

func NewJavtifulAdapter() *JavtifulAdapter {
	return &JavtifulAdapter{
		client:    &http.Client{Timeout: 15 * time.Second},
		baseURL:   "https://javtiful.com",
		userAgent: "Mozilla/5.0 (compatible; avxxx/0.1)",
	}
}

func (a *JavtifulAdapter) Name() string { return "javtiful" }

func (a *JavtifulAdapter) Search(ctx context.Context, query string, page int) ([]SearchResult, bool, error) {
	u := a.baseURL + "/search?q=" + url.QueryEscape(query)
	if page > 1 {
		u += "&page=" + strconv.Itoa(page)
	}
	body, err := a.get(ctx, u, a.baseURL+"/")
	if err != nil {
		return nil, false, err
	}
	items := extractJavtifulVideos(body, a.baseURL)
	return items, hasJavtifulNextPage(body, page), nil
}

func (a *JavtifulAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	pageURL, err := normalizeJavtifulID(id, a.baseURL)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, pageURL, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	title := firstMatch(body, `<h1[^>]*>([\s\S]*?)</h1>`)
	if title == "" {
		title = firstMatch(body, `<title[^>]*>([\s\S]*?)</title>`)
	}
	return &Detail{ID: id, Source: a.Name(), Title: cleanHTML(title), Description: cleanHTML(firstMatch(body, `<meta[^>]+name=["']description["'][^>]+content=["']([^"']*)`)), Episodes: []Episode{{ID: id, Title: cleanHTML(title)}}}, nil
}

func (a *JavtifulAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	pageURL, err := normalizeJavtifulID(id, a.baseURL)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, pageURL, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	streams := extractJavtifulStreams(body)
	if len(streams) == 0 {
		return nil, errors.New("no public video stream found")
	}
	return &ResolveResult{Streams: streams}, nil
}

func (a *JavtifulAdapter) get(ctx context.Context, rawURL, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("javtiful returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func normalizeJavtifulID(id, base string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, err := url.Parse(id)
		if err != nil || u.Host != "javtiful.com" {
			return "", errors.New("invalid javtiful resource")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/video/") {
		return "", errors.New("invalid javtiful video id")
	}
	return base + id, nil
}

func extractJavtifulVideos(body, base string) []SearchResult {
	blockRE := regexp.MustCompile(`(?is)<article\b[^>]*class=["'][^"']*\bfront-video-card\b[^>]*>.*?</article>`)
	hrefRE := regexp.MustCompile(`(?is)href=["']([^"']*/video/\d+/[^"']+)["']`)
	titleRE := regexp.MustCompile(`(?is)class=["'][^"']*\bfront-video-title\b[^"']*["'][^>]*>(.*?)</a>`)
	imgRE := regexp.MustCompile(`(?i)\s(?:data-front-lazy-src|data-src|src)=["']([^"']+)`)
	seen := map[string]bool{}
	out := []SearchResult{}
	for _, block := range blockRE.FindAllString(body, -1) {
		if strings.Contains(strings.ToLower(block), "front-partner-card") {
			continue
		}
		m := hrefRE.FindStringSubmatch(block)
		if len(m) < 2 {
			continue
		}
		raw := html.UnescapeString(m[1])
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		full := base + u.Path
		if u.RawQuery != "" {
			full += "?" + u.RawQuery
		}
		if seen[full] {
			continue
		}
		title := ""
		if t := titleRE.FindStringSubmatch(block); len(t) > 1 {
			title = cleanHTML(t[1])
		}
		if title == "" {
			continue
		}
		cover := ""
		if im := imgRE.FindStringSubmatch(block); len(im) > 1 {
			cover = absoluteURL(base, im[1])
		}
		seen[full] = true
		out = append(out, SearchResult{ID: strings.TrimPrefix(strings.TrimPrefix(full, base), "/"), Source: "javtiful", Title: title, Cover: cover, Type: "video"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

func extractJavtifulStreams(body string) []Stream {
	var out []Stream
	if m := regexp.MustCompile(`(?s)"playerSources"\s*:\s*(\[.*?\])\s*,\s*"videoTitle"`).FindStringSubmatch(body); len(m) > 1 {
		var sources []struct {
			Src  string `json:"src"`
			Size int    `json:"size"`
		}
		if json.Unmarshal([]byte(m[1]), &sources) == nil {
			sort.Slice(sources, func(i, j int) bool { return sources[i].Size > sources[j].Size })
			for _, s := range sources {
				if isHTTPMediaURL(s.Src) {
					out = append(out, Stream{URL: html.UnescapeString(strings.ReplaceAll(s.Src, `\/`, `/`)), Protocol: mediaProtocol(s.Src), Quality: strconv.Itoa(s.Size)})
				}
			}
		}
	}
	for _, m := range regexp.MustCompile(`(?i)<source\b[^>]+src=["']([^"']+)["']`).FindAllStringSubmatch(body, -1) {
		if len(m) > 1 && isHTTPMediaURL(m[1]) {
			out = append(out, Stream{URL: html.UnescapeString(m[1]), Protocol: mediaProtocol(m[1])})
		}
	}
	return dedupeStreams(out)
}

func dedupeStreams(in []Stream) []Stream {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if !seen[s.URL] {
			seen[s.URL] = true
			out = append(out, s)
		}
	}
	return out
}
func mediaProtocol(raw string) string {
	if strings.Contains(strings.ToLower(raw), ".m3u8") {
		return "hls"
	}
	return "mp4"
}
func isHTTPMediaURL(raw string) bool {
	u, err := url.Parse(html.UnescapeString(strings.ReplaceAll(raw, `\/`, `/`)))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
func absoluteURL(base, raw string) string {
	u, err := url.Parse(html.UnescapeString(raw))
	if err != nil {
		return ""
	}
	b, _ := url.Parse(base)
	return b.ResolveReference(u).String()
}
func firstMatch(body, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(body)
	if len(m) > 1 {
		return m[1]
	}
	return ""
}
func cleanHTML(s string) string {
	s = regexp.MustCompile(`(?s)<[^>]+>`).ReplaceAllString(s, " ")
	return strings.TrimSpace(html.UnescapeString(strings.Join(strings.Fields(s), " ")))
}
func hasJavtifulNextPage(body string, page int) bool {
	return strings.Contains(strings.ToLower(body), `front-pagination-link`) && strings.Contains(body, strconv.Itoa(page+1))
}
