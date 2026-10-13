package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ABxxxAdapter uses the site's JSON listing and video-file endpoints. It
// returns the upstream URL directly; Kodi's local playback proxy is omitted.
type ABxxxAdapter struct {
	client             *http.Client
	baseURL, userAgent string
}

func NewABxxxAdapter() *ABxxxAdapter {
	return &ABxxxAdapter{client: &http.Client{Timeout: 20 * time.Second}, baseURL: "https://abxxx.com/", userAgent: "Mozilla/5.0 (compatible; avxxx/0.1)"}
}
func (a *ABxxxAdapter) Name() string { return "abxxx" }

func (a *ABxxxAdapter) Search(ctx context.Context, query string, page int) ([]SearchResult, bool, error) {
	params := fmt.Sprintf("0/str/relevance/60/search..%d.all...", page)
	raw := a.baseURL + "api/videos2.php?" + url.Values{"params": {params}, "s": {query}}.Encode()
	body, err := a.request(ctx, http.MethodGet, raw, nil, a.baseURL+"search/?s="+url.QueryEscape(query))
	if err != nil {
		return nil, false, err
	}
	var data struct {
		Videos []struct {
			ID     json.Number `json:"video_id"`
			Dir    string      `json:"dir"`
			Title  string      `json:"title"`
			Poster string      `json:"scr"`
		} `json:"videos"`
		Pages int `json:"pages"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, false, fmt.Errorf("invalid abxxx search response: %w", err)
	}
	out := make([]SearchResult, 0, len(data.Videos))
	for _, v := range data.Videos {
		id := v.ID.String()
		if id == "" || v.Dir == "" {
			continue
		}
		out = append(out, SearchResult{ID: "/video/" + id + "/" + v.Dir + "/", Source: a.Name(), Title: cleanHTML(html.UnescapeString(v.Title)), Cover: absoluteURL(a.baseURL, v.Poster), Type: "video"})
	}
	return out, page < data.Pages, nil
}

func (a *ABxxxAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, err := a.normalizeID(id)
	if err != nil {
		return nil, err
	}
	body, err := a.request(ctx, http.MethodGet, u, nil, a.baseURL)
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(body, `<title[^>]*>([\s\S]*?)</title>`))
	return &Detail{ID: id, Source: a.Name(), Title: title, Episodes: []Episode{{ID: id, Title: title}}}, nil
}

func (a *ABxxxAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	u, err := a.normalizeID(id)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 {
		return nil, errors.New("invalid abxxx video id")
	}
	raw, err := a.request(ctx, http.MethodGet, a.baseURL+"api/videofile.php?video_id="+url.QueryEscape(parts[1]), nil, u)
	if err != nil {
		return nil, err
	}
	var sources []struct {
		VideoURL, Format string
		Default          int `json:"is_default"`
	}
	if err := json.Unmarshal([]byte(raw), &sources); err != nil {
		return nil, fmt.Errorf("invalid abxxx stream response: %w", err)
	}
	var streams []Stream
	for _, s := range sources {
		if s.VideoURL == "" || s.Format == "_tr.mp4" {
			continue
		}
		decoded, err := decodeABxxxURL(s.VideoURL, a.baseURL)
		if err == nil && isHTTPMediaURL(decoded) {
			streams = append(streams, Stream{URL: decoded, Protocol: "mp4", Quality: s.Format})
		}
	}
	streams = dedupeStreams(streams)
	if len(streams) == 0 {
		return nil, errors.New("abxxx has no public direct stream")
	}
	return &ResolveResult{Streams: streams}, nil
}

func (a *ABxxxAdapter) normalizeID(id string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, err := url.Parse(id)
		if err != nil || u.Host != "abxxx.com" {
			return "", errors.New("invalid abxxx resource")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/video/") && !strings.HasPrefix(id, "video/") {
		return "", errors.New("invalid abxxx video id")
	}
	return a.baseURL + strings.TrimPrefix(id, "/"), nil
}
func (a *ABxxxAdapter) request(ctx context.Context, method, raw string, body io.Reader, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("abxxx returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}
func decodeABxxxURL(value, base string) (string, error) {
	value = strings.NewReplacer(",", "+", "~", "=").Replace(value)
	value += strings.Repeat("=", (4-len(value)%4)%4)
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	return absoluteURL(base, strings.ReplaceAll(string(b), "/>", "/?")), nil
}
