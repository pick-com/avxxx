package scraper

import (
	"avxxx/internal/model"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type entry struct {
	Title string `xml:"title"`

	Description string `xml:"description"`
	Summary     string `xml:"summary"`
	Links       []struct {
		Text string `xml:",chardata"`
		Href string `xml:"href,attr"`
		Rel  string `xml:"rel,attr"`
	} `xml:"link"`
}
type feed struct {
	XMLName xml.Name
	Channel struct {
		Items []entry `xml:"item"`
	} `xml:"channel"`
	Entries []entry `xml:"entry"`
}

func ValidURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.User == nil
}
func Parse(r io.Reader, source model.Source) ([]model.Item, error) {
	var f feed
	if e := xml.NewDecoder(r).Decode(&f); e != nil {
		return nil, e
	}
	if f.XMLName.Local != "rss" && f.XMLName.Local != "feed" {
		return nil, fmt.Errorf("unsupported feed: %s", f.XMLName.Local)
	}
	entries := append(f.Channel.Items, f.Entries...)
	out := []model.Item{}
	base, _ := url.Parse(source.URL)
	for _, x := range entries {
		link := ""
		for _, l := range x.Links {
			if l.Rel == "" || l.Rel == "alternate" {
				link = l.Href
				if link == "" {
					link = strings.TrimSpace(l.Text)
				}
				break
			}
		}
		u, e := url.Parse(link)
		if e != nil || link == "" || strings.TrimSpace(x.Title) == "" {
			continue
		}
		link = base.ResolveReference(u).String()
		if !ValidURL(link) {
			continue
		}
		summary := x.Description
		if summary == "" {
			summary = x.Summary
		}
		out = append(out, model.Item{SourceID: source.ID, Title: strings.TrimSpace(x.Title), URL: link, Summary: summary})
	}
	return out, nil
}
func Fetch(ctx context.Context, s model.Source) ([]model.Item, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "avxxx-feed-reader/0.1")
	client := http.Client{Timeout: 30 * time.Second}
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("source returned HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (5<<20)+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 5<<20 {
		return nil, fmt.Errorf("feed exceeds 5 MiB")
	}
	return Parse(strings.NewReader(string(b)), s)
}
