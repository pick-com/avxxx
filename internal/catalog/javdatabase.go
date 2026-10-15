package catalog

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// JAVDatabaseAdapter parses public JAV Database catalog pages. It only parses
// a bounded Document supplied by the fetcher and never follows links itself.
type JAVDatabaseAdapter struct{}

func NewJAVDatabaseAdapter() Adapter    { return JAVDatabaseAdapter{} }
func (JAVDatabaseAdapter) Name() string { return "javdatabase" }

func (a JAVDatabaseAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []Record{}
	d.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		h, _ := s.Attr("href")
		u := absolute(doc.FinalURL, h)
		kind, key := classify(u)
		if key == "" || seen[string(kind)+":"+key] {
			return
		}
		seen[string(kind)+":"+key] = true
		name := strings.TrimSpace(s.Text())
		if name == "" {
			return
		}
		out = append(out, Record{EntityType: kind, SourceKey: key, URL: u, Fields: map[string]any{"name": name}})
	})
	return out, nil
}

func (a JAVDatabaseAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	kind, key := classify(doc.FinalURL)
	if kind != entity || key == "" {
		return Record{}, fmt.Errorf("javdatabase: unexpected page %s", doc.FinalURL)
	}
	f := map[string]any{}
	text := strings.TrimSpace(d.Find("body").Text())
	if entity == Performer {
		name := strings.TrimSpace(d.Find("h1").First().Text())
		if i := strings.Index(name, " - "); i > 0 {
			name = name[:i]
		}
		f["name"] = name
		f["name_ja"] = labelValue(text, "JP:")
		f["birth_date"] = labelValue(text, "DOB:")
		f["debut_date"] = labelValue(text, "Debut:")
		f["height_cm"] = number(labelValue(text, "Height:"))
		f["cup_size"] = labelValue(text, "Cup:")
		f["shoe_size"] = number(labelValue(text, "Shoe Size:"))
		f["measurements"] = labelValue(text, "Measurements:")
		f["tags"] = linksAfter(d, "Tags:")
		f["release_refs"] = movieRefs(d)
		return Record{EntityType: Performer, SourceKey: key, URL: doc.FinalURL, Fields: f}, nil
	}
	f["title"] = strings.TrimSpace(d.Find("h1").First().Text())
	f["dvd_code"] = labelValue(text, "DVD ID:")
	f["content_id"] = labelValue(text, "Content ID:")
	f["release_date"] = labelValue(text, "Release Date:")
	f["runtime_minutes"] = number(labelValue(text, "Runtime:"))
	f["maker"] = linkedLabel(d, "Studio:")
	f["director"] = linkedLabel(d, "Director:")
	f["genres"] = linksAfter(d, "Genre(s):")
	f["tags"] = f["genres"]
	f["performers"] = linksAfter(d, "Idol(s)/Actress(es):")
	return Record{EntityType: Release, SourceKey: key, URL: doc.FinalURL, Fields: f}, nil
}

func classify(raw string) (EntityType, string) {
	u, _ := url.Parse(raw)
	p := strings.Trim(u.Path, "/")
	parts := strings.Split(p, "/")
	for i, x := range parts {
		if x == "idols" && i+1 < len(parts) && parts[i+1] != "" {
			return Performer, parts[i+1]
		}
		if x == "movies" && i+1 < len(parts) && parts[i+1] != "" {
			return Release, parts[i+1]
		}
	}
	return "", ""
}
func absolute(base, raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	b, e := url.Parse(base)
	if e != nil {
		return raw
	}
	return b.ResolveReference(u).String()
}
func labelValue(text, label string) string {
	i := strings.Index(text, label)
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(text[i+len(label):])
	for _, next := range []string{"Content ID:", "DVD ID:", "DOB:", "Debut:", "Measurements:", "Cup:", "Height:", "Shoe Size:", "Release Date:", "Runtime:", "Studio:", "Director:", "Genre(s):", "Idol(s)/Actress(es):", "Tags:", "JP:"} {
		if j := strings.Index(v, next); j >= 0 && j > 0 {
			v = v[:j]
		}
	}
	if j := strings.IndexAny(v, "\n|"); j >= 0 {
		v = v[:j]
	}
	return strings.TrimSpace(v)
}
func number(v string) any {
	re := regexp.MustCompile(`[-+]?\d+(?:\.\d+)?`)
	m := re.FindString(v)
	return m
}
func linkedLabel(d *goquery.Document, label string) string {
	var out string
	d.Find("body *").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.TrimSpace(s.Text()) == label {
			out = strings.TrimSpace(s.Next().Text())
			return false
		}
		return true
	})
	return out
}
func linksAfter(d *goquery.Document, label string) []string {
	out := []string{}
	d.Find("body *").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if strings.TrimSpace(s.Text()) == label {
			s.NextAll().Find("a").Each(func(_ int, a *goquery.Selection) {
				v := strings.TrimSpace(a.Text())
				if v != "" {
					out = append(out, v)
				}
			})
			return false
		}
		return true
	})
	return out
}
func movieRefs(d *goquery.Document) []string {
	out := []string{}
	d.Find("a[href*='/movies/']").Each(func(_ int, s *goquery.Selection) {
		if v := strings.TrimSpace(s.Text()); v != "" {
			out = append(out, v)
		}
	})
	return out
}
