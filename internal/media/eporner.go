package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type EpornerAdapter struct {
	base   string
	client *http.Client
}

func NewEpornerAdapter() Adapter {
	return &EpornerAdapter{base: "https://www.eporner.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *EpornerAdapter) Name() string { return "eporner" }

func (a *EpornerAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	v := url.Values{"query": {q}, "page": {strconv.Itoa(page)}, "per_page": {"30"}, "order": {"most-popular"}, "gay": {"0"}, "hd": {"0"}, "thumbsize": {"medium"}, "format": {"json"}}
	body, err := a.get(ctx, "/api/v2/video/search/?"+v.Encode(), a.base+"/")
	if err != nil {
		return nil, false, err
	}
	var data struct {
		TotalPages int `json:"total_pages"`
		Videos     []struct {
			ID, Title, URL, Embed string
			Thumb                 struct {
				Src string `json:"src"`
			} `json:"default_thumb"`
			LengthSec int `json:"length_sec"`
		} `json:"videos"`
	}
	if err := json.Unmarshal([]byte(body), &data); err != nil {
		return nil, false, fmt.Errorf("invalid eporner search response: %w", err)
	}
	out := make([]SearchResult, 0, len(data.Videos))
	for _, v := range data.Videos {
		if v.ID == "" || v.Title == "" {
			continue
		}
		id := v.URL
		if id == "" {
			id = "/video-" + v.ID + "/"
		}
		out = append(out, SearchResult{ID: strings.TrimPrefix(id, a.base), Source: a.Name(), Title: v.Title, Cover: v.Thumb.Src, Type: "video"})
	}
	return out, data.TotalPages > page, nil
}

func (a *EpornerAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
	u, err := a.idURL(id)
	if err != nil {
		return nil, err
	}
	body, err := a.get(ctx, u, a.base+"/")
	if err != nil {
		return nil, err
	}
	title := cleanHTML(firstMatch(body, `(?is)<title[^>]*>(.*?)</title>`))
	embed := firstMatch(body, `(?i)(https?://www\.eporner\.com/embed/[A-Za-z0-9]+/?)`)
	return &Detail{ID: id, Source: a.Name(), Title: title, EmbedURL: embed, Episodes: []Episode{{ID: id, Title: title}}}, nil
}

func (a *EpornerAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("eporner direct resolver is not enabled; use the official embed player")
}
func (a *EpornerAdapter) idURL(id string) (string, error) {
	if strings.HasPrefix(id, "http://") || strings.HasPrefix(id, "https://") {
		u, e := url.Parse(id)
		if e != nil || u.Host != "www.eporner.com" {
			return "", errors.New("invalid eporner video id")
		}
		return u.String(), nil
	}
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	if !strings.HasPrefix(id, "/video-") && !strings.HasPrefix(id, "/video/") {
		return "", errors.New("invalid eporner video id")
	}
	return a.base + id, nil
}
func (a *EpornerAdapter) get(ctx context.Context, path, ref string) (string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, a.base+path, nil)
	if strings.HasPrefix(path, "http") {
		req, e = http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
	}
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
		return "", fmt.Errorf("eporner returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 12<<20))
	return string(b), e
}
