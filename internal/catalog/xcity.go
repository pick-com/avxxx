package catalog

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// XCITYAdapter handles the stable public idol/title URL patterns. Selectors
// are deliberately tolerant because the site has changed presentation markup.
type XCITYAdapter struct{}

func NewXCITYAdapter() Adapter    { return XCITYAdapter{} }
func (XCITYAdapter) Name() string { return "xcity" }

func (a XCITYAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
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
		kind, key := xcityClassify(u)
		if key == "" {
			return
		}
		k := string(kind) + ":" + key
		if seen[k] {
			return
		}
		seen[k] = true
		text := strings.TrimSpace(s.Text())
		if text == "" {
			text = key
		}
		fields := map[string]any{}
		if kind == Performer {
			fields["name"] = text
		}
		out = append(out, Record{EntityType: kind, SourceKey: key, URL: u, Fields: fields})
	})
	return out, nil
}

func (a XCITYAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	kind, key := xcityClassify(doc.FinalURL)
	if kind != entity || key == "" {
		return Record{}, fmt.Errorf("xcity: unexpected page %s", doc.FinalURL)
	}
	f := map[string]any{}
	text := strings.TrimSpace(d.Find("body").Text())
	title := strings.TrimSpace(d.Find("h1").First().Text())
	if title == "" {
		title = strings.TrimSpace(d.Find("title").First().Text())
	}
	if entity == Performer {
		f["name"] = title
		f["birth_date"] = xcityLabel(text, "生年月日")
		f["birth_place"] = xcityLabel(text, "出身地")
		f["height_cm"] = number(xcityLabel(text, "身長"))
		f["measurements"] = xcityLabel(text, "スリーサイズ")
		f["cup_size"] = xcityLabel(text, "バスト")
		f["release_refs"] = xcityLinks(d, "/title/")
	} else {
		f["title"] = title
		f["release_date"] = xcityLabel(text, "発売日")
		f["maker"] = xcityLabel(text, "メーカー")
		f["label"] = xcityLabel(text, "レーベル")
		f["genres"] = xcityLinks(d, "/genre/")
		f["tags"] = f["genres"]
		f["performers"] = xcityLinks(d, "/idol/")
	}
	return Record{EntityType: entity, SourceKey: key, URL: doc.FinalURL, Fields: f}, nil
}

// NextPage follows XCITY list pagination (?page=N, labelled 次へ).
func (XCITYAdapter) NextPage(doc Document) string {
	return nextPageLink(doc, []string{".pageScroll a.next", ".pager a.next", ".pager li.next a"}, []string{"次へ", "次へ »", "次へ>", "次のページ", "Next", "»"})
}

func xcityClassify(raw string) (EntityType, string) {
	u, _ := url.Parse(raw)
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if p == "idol" && i+1 < len(parts) && parts[i+1] != "" {
			return Performer, parts[i+1]
		}
		if p == "title" && i+1 < len(parts) && parts[i+1] != "" {
			return Release, parts[i+1]
		}
	}
	return "", ""
}
func xcityLabel(text, label string) string {
	i := strings.Index(text, label)
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(text[i+len(label):])
	for _, n := range []string{"生年月日", "出身地", "身長", "スリーサイズ", "バスト", "発売日", "メーカー", "レーベル"} {
		if j := strings.Index(v, n); j > 0 {
			v = v[:j]
		}
	}
	return strings.TrimSpace(v)
}
func xcityLinks(d *goquery.Document, part string) []string {
	out := []string{}
	d.Find("a[href*='" + part + "']").Each(func(_ int, s *goquery.Selection) {
		if v := strings.TrimSpace(s.Text()); v != "" {
			out = append(out, v)
		}
	})
	return out
}
