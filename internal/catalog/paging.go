package catalog

import (
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Paginator is implemented by adapters whose discovery pages link to a next
// page. NextPage returns the absolute URL of the following discovery page, or
// "" when doc is the last page. It must not perform I/O.
type Paginator interface {
	NextPage(doc Document) string
}

// commonNextSelectors covers rel=next and WordPress-style pagination.
var commonNextSelectors = []string{
	"link[rel='next']",
	"a[rel='next']",
	"a.next.page-numbers",
	".pagination a.next",
	".pagination li.next a",
	"li.next a",
	"a.next",
}

// nextPageLink finds the next discovery page in doc using the given selectors
// first, then commonNextSelectors, then anchors whose text is one of texts.
// Only same-host http(s) links that differ from the current page are returned.
func nextPageLink(doc Document, selectors []string, texts []string) string {
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return ""
	}
	base := doc.FinalURL
	if base == "" {
		base = doc.URL
	}
	accept := func(href string) string {
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") {
			return ""
		}
		u := absolute(base, href)
		if !SameSite(base, u) || u == base {
			return ""
		}
		return u
	}
	for _, sel := range append(append([]string{}, selectors...), commonNextSelectors...) {
		var found string
		d.Find(sel).EachWithBreak(func(_ int, s *goquery.Selection) bool {
			h, _ := s.Attr("href")
			found = accept(h)
			return found == ""
		})
		if found != "" {
			return found
		}
	}
	if len(texts) == 0 {
		return ""
	}
	var found string
	d.Find("a[href]").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		t := strings.TrimSpace(s.Text())
		for _, want := range texts {
			if strings.EqualFold(t, want) {
				h, _ := s.Attr("href")
				found = accept(h)
				return found == ""
			}
		}
		return true
	})
	return found
}

// SameSite reports whether two absolute http(s) URLs share a host.
func SameSite(a, b string) bool {
	ua, err := url.Parse(a)
	if err != nil {
		return false
	}
	ub, err := url.Parse(b)
	if err != nil {
		return false
	}
	if ub.Scheme != "http" && ub.Scheme != "https" {
		return false
	}
	return strings.EqualFold(ua.Hostname(), ub.Hostname())
}
