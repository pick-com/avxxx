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

type HclipsAdapter struct {
	base   string
	client *http.Client
}

func NewHclipsAdapter() Adapter {
	return &HclipsAdapter{base: "https://hclips.com", client: &http.Client{Timeout: 20 * time.Second}}
}
func (a *HclipsAdapter) Name() string { return "hclips" }
func (a *HclipsAdapter) Search(ctx context.Context, q string, page int) ([]SearchResult, bool, error) {
	raw := a.base + "/api/videos2.php?params=86400/str/relevance/60/search.0." + strconv.Itoa(page) + ".all...&s=" + url.QueryEscape(q)
	b, e := a.get(ctx, raw, a.base+"/search/1/?s="+url.QueryEscape(q))
	if e != nil {
		return nil, false, e
	}
	var d struct {
		Pages  int `json:"pages"`
		Videos []struct {
			ID              string `json:"video_id"`
			Dir, Title, Scr string
		} `json:"videos"`
	}
	if e = json.Unmarshal([]byte(b), &d); e != nil {
		return nil, false, fmt.Errorf("invalid hclips response: %w", e)
	}
	out := []SearchResult{}
	for _, v := range d.Videos {
		if v.ID == "" || v.Dir == "" {
			continue
		}
		out = append(out, SearchResult{ID: "/videos/" + v.ID + "/" + v.Dir + "/", Source: a.Name(), Title: strings.TrimSpace(v.Title), Cover: v.Scr, Type: "video"})
	}
	return out, d.Pages > page, nil
}
func (a *HclipsAdapter) Detail(ctx context.Context, id string) (*Detail, error) {
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
func (a *HclipsAdapter) Resolve(context.Context, string) (*ResolveResult, error) {
	return nil, errors.New("hclips direct resolver requires source headers; use Kodi or an authorized proxy")
}
func (a *HclipsAdapter) idURL(id string) (string, error) {
	if !strings.HasPrefix(id, "/") {
		id = "/" + id
	}
	if !strings.HasPrefix(id, "/videos/") {
		return "", errors.New("invalid hclips video id")
	}
	return a.base + id, nil
}
func (a *HclipsAdapter) get(ctx context.Context, raw, ref string) (string, error) {
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "", e
	}
	r.Header.Set("User-Agent", "Mozilla/5.0")
	r.Header.Set("Referer", ref)
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	x, e := a.client.Do(r)
	if e != nil {
		return "", e
	}
	defer x.Body.Close()
	if x.StatusCode != 200 {
		return "", fmt.Errorf("hclips returned HTTP %d", x.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(x.Body, 12<<20))
	return string(b), e
}
