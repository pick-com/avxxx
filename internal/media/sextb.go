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
	"strconv"
	"strings"
	"time"
)

// SextbAdapter ports the public search/detail flow. Playback is limited to
// direct public media URLs present in the page; it does not decrypt player
// payloads or invoke third-party resolvers.
type SextbAdapter struct {
	client             *http.Client
	baseURL, userAgent string
}

func NewSextbAdapter() *SextbAdapter {
	return &SextbAdapter{client: &http.Client{Timeout: 15 * time.Second}, baseURL: "https://sextb.net", userAgent: "Mozilla/5.0 (compatible; avxxx/0.1)"}
}
func (a *SextbAdapter) Name() string { return "sextb" }

func (a *SextbAdapter) Search(ctx context.Context, query string, page int) ([]SearchResult, bool, error) {
	form := url.Values{"q": {query}, "page": {strconv.Itoa(page)}}
	body, err := a.post(ctx, "/ajax/search", form, a.baseURL+"/")
	if err != nil {
		return nil, false, err
	}
	var response struct {
		Hits []struct {
			Code, Name, Poster string
			Runtimes           any `json:"runtimes"`
		} `json:"hits"`
		TotalPages int `json:"totalPages"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		return nil, false, fmt.Errorf("invalid sextb search response: %w", err)
	}
	items := make([]SearchResult, 0, len(response.Hits))
	for _, hit := range response.Hits {
		if hit.Code == "" || hit.Name == "" {
			continue
		}
		id := "/" + strings.TrimPrefix(hit.Code, "/")
		title := hit.Name
		if runtime := fmt.Sprint(hit.Runtimes); runtime != "" && runtime != "<nil>" {
			title = "[" + runtime + " min] " + title
		}
		items = append(items, SearchResult{ID: id, Source: a.Name(), Title: title + " (" + strings.ToUpper(hit.Code) + ")", Cover: absoluteURL(a.baseURL, hit.Poster), Type: "video"})
	}
	return items, response.TotalPages > page, nil
}

func (a *SextbAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, err := normalizeSextbID(id, a.baseURL)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(body, `<h1[^>]*>([\s\S]*?)</h1>`))
	if title == "" {
		title = cleanHTML(firstMatch(body, `<title[^>]*>([\s\S]*?)</title>`))
	}
	embed := firstMatch(body, `(?is)<iframe[^>]+src=["'](https?://sextb\.net/e/[^"']+)["']`)
	return &Detail{ID: id, Source: a.Name(), Title: title, EmbedURL: embed, Episodes: []Episode{{ID: id, Title: title}}}, nil
}

func (a *SextbAdapter) Resolve(ctx context.Context, id string) (*ResolveResult, error) {
	u, err := normalizeSextbID(id, a.baseURL)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.baseURL+"/")
	if err != nil {
		return nil, err
	}
	var streams []Stream
	for _, m := range regexp.MustCompile(`(?i)(https?:\\?/\\?/[^"'<>\s]+\.(?:m3u8|mp4)(?:\?[^"'<>\s]*)?)`).FindAllStringSubmatch(body, -1) {
		raw := strings.ReplaceAll(html.UnescapeString(m[1]), `\/`, `/`)
		streams = append(streams, Stream{URL: raw, Protocol: mediaProtocol(raw)})
	}
	streams = dedupeStreams(streams)
	if len(streams) == 0 {
		return nil, errors.New("sextb has no direct public stream; third-party resolver is not enabled")
	}
	return &ResolveResult{Streams: streams}, nil
}

func (a *SextbAdapter) get(ctx context.Context, path, referer string) (string, error) {
	return a.request(ctx, http.MethodGet, path, nil, referer)
}
func (a *SextbAdapter) post(ctx context.Context, path string, form url.Values, referer string) (string, error) {
	return a.request(ctx, http.MethodPost, a.baseURL+path, strings.NewReader(form.Encode()), referer)
}
func (a *SextbAdapter) request(ctx context.Context, method, raw string, body io.Reader, referer string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, method, raw, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", a.userAgent)
	req.Header.Set("Referer", referer)
	req.Header.Set("Accept", "text/html,application/json")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("sextb returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(data), err
}

func normalizeSextbID(id, base string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, err := url.Parse(id)
		if err != nil || u.Host != "sextb.net" {
			return "", errors.New("invalid sextb resource")
		}
		return u.String(), nil
	}
	if strings.Contains(id, "..") || strings.ContainsAny(id, "?#") {
		return "", errors.New("invalid sextb resource id")
	}
	return base + "/" + strings.TrimPrefix(id, "/"), nil
}
