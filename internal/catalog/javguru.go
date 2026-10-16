package catalog

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

type JavGuruAdapter struct{}

func NewJavGuruAdapter() Adapter    { return JavGuruAdapter{} }
func (JavGuruAdapter) Name() string { return "javguru" }

func (a JavGuruAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
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
		kind, key := guruClassify(u)
		if key == "" {
			return
		}
		k := string(kind) + ":" + key
		if seen[k] {
			return
		}
		seen[k] = true
		name := strings.TrimSpace(s.Text())
		if name == "" {
			name = key
		}
		f := map[string]any{}
		if kind == Performer {
			f["name"] = name
		}
		out = append(out, Record{EntityType: kind, SourceKey: key, URL: u, Fields: f})
	})
	return out, nil
}

func (a JavGuruAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	kind, key := guruClassify(doc.FinalURL)
	if kind != entity || key == "" {
		return Record{}, fmt.Errorf("javguru: unexpected page %s", doc.FinalURL)
	}
	text := strings.TrimSpace(d.Find("body").Text())
	// Entity heading only: a logo <h1> or the bare <title> is the site's own
	// name, never a performer or release.
	title := entityHeading(d, "h1.entry-title", ".entry-header h1")
	if title == "" && entity == Release {
		title = pageTitle(d)
	}
	f := map[string]any{}
	if entity == Performer {
		name := performerName(d, "h1.entry-title", ".entry-header h1")
		if name == "" {
			return Record{}, fmt.Errorf("javguru: no performer name on %s: %w", doc.FinalURL, ErrSkipRecord)
		}
		f["name"] = name
		f[FieldPageTitle] = pageTitle(d)
		f["name_ja"] = guruLabel(text, "Japanese Name")
		f["cup_size"] = guruLabel(text, "Cup")
		f["height_cm"] = number(guruLabel(text, "Height"))
		f["birth_date"] = guruLabel(text, "Birthday")
		f["release_refs"] = guruLinks(d, "/jav/")
	} else {
		f["title"] = title
		f["dvd_code"] = guruLabel(text, "Code")
		f["release_date"] = guruLabel(text, "Release Date")
		f["maker"] = guruLabel(text, "Maker")
		f["genres"] = guruLinks(d, "/genre/")
		f["tags"] = f["genres"]
		f["performers"] = performerLinks(d.Find("a[href]"), doc.FinalURL, func(u string) bool {
			k, key := guruClassify(u)
			return k == Performer && key != ""
		})
	}
	return Record{EntityType: entity, SourceKey: key, URL: doc.FinalURL, Fields: f}, nil
}

// NextPage follows the WordPress archive pagination (/page/N/).
func (JavGuruAdapter) NextPage(doc Document) string {
	return nextPageLink(doc, []string{".wp-pagenavi a.nextpostslink"}, []string{"Next", "Next »", "»"})
}

func guruClassify(raw string) (EntityType, string) {
	u, _ := url.Parse(raw)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if i+1 < len(parts) && parts[i+1] == "page" {
			continue // archive pagination, not an entity
		}
		if p == "actress" && i+1 < len(parts) && parts[i+1] != "" {
			return Performer, parts[i+1]
		}
		if (p == "jav" || p == "video") && i+1 < len(parts) && parts[i+1] != "" {
			return Release, parts[i+1]
		}
	}
	return "", ""
}
func guruLabel(text, label string) string {
	lower := strings.ToLower(text)
	i := strings.Index(lower, strings.ToLower(label))
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(text[i+len(label):])
	// The value ends where the next "Label:" starts. Only a label followed by
	// a colon counts, so a value that contains a label word ("Example
	// Maker") is kept whole.
	for _, n := range guruLabels {
		if j := guruLabelAt(v, n); j > 0 {
			v = v[:j]
		}
	}
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(v), ":-"))
}

var guruLabels = []string{"Japanese Name", "Cup", "Height", "Birthday", "Code", "Release Date", "Maker", "Tags", "Actress", "Genre"}

// guruLabelAt returns the index in v of label followed by optional spaces and
// a colon (case-insensitive), or -1.
func guruLabelAt(v, label string) int {
	lv, ll := strings.ToLower(v), strings.ToLower(label)
	for from := 0; from < len(lv); {
		j := strings.Index(lv[from:], ll)
		if j < 0 {
			return -1
		}
		j += from
		if rest := strings.TrimLeft(lv[j+len(ll):], " \t"); strings.HasPrefix(rest, ":") {
			return j
		}
		from = j + len(ll)
	}
	return -1
}
func guruLinks(d *goquery.Document, part string) []string {
	out := []string{}
	d.Find("a[href*='" + part + "']").Each(func(_ int, s *goquery.Selection) {
		if v := strings.TrimSpace(s.Text()); v != "" {
			out = append(out, v)
		}
	})
	return out
}
