package catalog

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/unicode/norm"
)

// JAVLibrary (https://www.javlibrary.com) adapter: release pages (code,
// title, release date, runtime, director, maker, label, genres, cast) as
// community/aggregator evidence (FINAL_DATA_DESIGN 2.4 tier "aggregator",
// below XSList and Jav.Guru by field policy, migrations/*_vpn_sources.sql).
//
// Deliberately conservative mapping:
//   - the cast is stored as evidence ("cast", "cast_ids"), not as
//     "performers": the English site prints names family-name first
//     ("Mikami Yua"), which would create duplicate performers under swapped
//     slugs. Cast is cross-check evidence for identity review only;
//   - genres are evidence ("genres"), not public tags ("tags");
//   - the user rating is community evidence ("javlibrary_user_rating",
//     "javlibrary_user_rating_votes" when printed): source-namespaced so it is
//     never compared with another source's rating, no canonical column takes
//     it, and it never overrides a field;
//   - no cover_url: covers come from official sources, never from here.
//
// Access status (2026-10-16): www.javlibrary.com answers every request from
// our egress, robots.txt included, with a Cloudflare managed challenge (HTTP
// 403, cf-mitigated: challenge). The Fetcher reports a challenge as a block
// and the source is parked; nothing here tries to get past a challenge (no
// cookie reuse, no headless browser, no TLS fingerprinting). Seeded disabled,
// license_status review_required.
//
// The selectors follow the site's long-standing release page layout
// (#video_id, #video_date, #video_length, #video_maker, ...) and were checked
// against synthetic fixtures only (testdata/javlibrary/README.md).
type JAVLibraryAdapter struct{}

// JAVLibraryAdapterName is the adapter / sources.adapter name.
const JAVLibraryAdapterName = "javlibrary"

// JAVLibraryStartURL is the default discovery page (newest releases first).
const JAVLibraryStartURL = "https://www.javlibrary.com/en/vl_newrelease.php"

func NewJAVLibraryAdapter() Adapter          { return JAVLibraryAdapter{} }
func (JAVLibraryAdapter) Name() string       { return JAVLibraryAdapterName }
func (JAVLibraryAdapter) SourceKind() string { return "aggregator" }
func (JAVLibraryAdapter) StartURL() string   { return JAVLibraryStartURL }

// JAVLibraryLookupURL is the site search for one product code (a verify job
// or dry run with -url); the result is a discovery page.
func JAVLibraryLookupURL(code string) string {
	return "https://www.javlibrary.com/en/vl_searchbyid.php?" + url.Values{"keyword": {strings.TrimSpace(code)}}.Encode()
}

// javlibraryKey matches the release id of ?v=<id> (e.g. javli4abcd).
var javlibraryKey = regexp.MustCompile(`^[a-z0-9]{4,20}$`)

// javlibraryLang matches the language path segment (en, ja, cn, tw).
var javlibraryLang = regexp.MustCompile(`^[a-z]{2}$`)

func javlibraryClassify(raw string) (EntityType, string) {
	u, err := url.Parse(raw)
	if err != nil || !siteHost(u.Hostname(), "javlibrary.com") {
		return "", ""
	}
	// Release pages: /{lang}/?v=<id> or /{lang}/index.php?v=<id>; every
	// other path (vl_star.php, vl_newrelease.php, ...) is a listing.
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) == 1 && (parts[0] == "" || parts[0] == "index.php" || javlibraryLang.MatchString(parts[0])):
	case len(parts) == 2 && javlibraryLang.MatchString(parts[0]) && parts[1] == "index.php":
	default:
		return "", ""
	}
	if v := strings.ToLower(u.Query().Get("v")); javlibraryKey.MatchString(v) {
		return Release, v
	}
	return "", ""
}

func (a JAVLibraryAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return nil, err
	}
	base := docBase(doc)
	seen := map[string]bool{}
	out := []Record{}
	d.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		if inChrome(s) {
			return
		}
		u := absolute(base, s.AttrOr("href", ""))
		kind, key := javlibraryClassify(u)
		if kind != Release || seen[key] {
			return
		}
		seen[key] = true
		f := map[string]any{}
		code := clean(s.Find(".id").First().Text())
		title := clean(s.Find(".title").First().Text())
		if title == "" {
			title = clean(s.AttrOr("title", ""))
		}
		if code == "" {
			code, title = splitCodeTitle(title)
		}
		setStr(f, "dvd_code", code)
		setStr(f, "title", title)
		out = append(out, Record{EntityType: Release, SourceKey: key, URL: u, Fields: f})
	})
	return out, nil
}

// NextPage follows the listing pagination (.page_selector a.page.next).
func (JAVLibraryAdapter) NextPage(doc Document) string {
	return nextPageLink(doc, []string{".page_selector a.page.next", ".page_selector a[rel='next']"}, []string{"Next", "›", "»", ">"})
}

var javlibraryLabels = []string{"ID", "Release Date", "Length", "Director", "Maker", "Label", "User Rating", "Genre(s)", "Cast"}

func (a JAVLibraryAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	base := docBase(doc)
	kind, key := javlibraryClassify(base)
	if entity != Release || kind != Release {
		return Record{}, fmt.Errorf("javlibrary: unexpected %s page %s (only release pages are parsed)", entity, base)
	}
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	labels := labeledTexts(d, javlibraryLabels)
	code := clean(firstNonEmpty(d.Find("#video_id td.text, #video_id .text").First().Text(), labels["ID"]))
	heading := clean(firstNonEmpty(d.Find("#video_title h3 a, #video_title h3, #video_title a").First().Text(), entityHeading(d, "h3.post-title")))
	title := heading
	if code != "" && len(title) > len(code) && strings.EqualFold(title[:len(code)], code) {
		title = clean(title[len(code):])
	} else if c, t := splitCodeTitle(heading); code == "" && c != "" {
		code, title = c, t
	}
	if title == "" {
		return Record{}, fmt.Errorf("javlibrary: no release title on %s: %w", base, ErrSkipRecord)
	}
	f := map[string]any{"title": title}
	setStr(f, "dvd_code", strings.ToUpper(code))
	if v := looseDate(firstNonEmpty(d.Find("#video_date td.text, #video_date .text").First().Text(), labels["Release Date"])); v != "" {
		f["release_date"] = v
	}
	if v := number(firstNonEmpty(d.Find("#video_length span.text, #video_length .text").First().Text(), labels["Length"])); v != "" {
		f["runtime_minutes"] = v
	}
	setStr(f, "director", firstNonEmpty(clean(d.Find("#video_director .director a").First().Text()), firstLinkedLabel(labels["Director"])))
	setStr(f, "maker", firstNonEmpty(clean(d.Find("#video_maker .maker a").First().Text()), firstLinkedLabel(labels["Maker"])))
	setStr(f, "label", firstNonEmpty(clean(d.Find("#video_label .label a").First().Text()), firstLinkedLabel(labels["Label"])))
	f["genres"] = texts(d.Find("#video_genres .genre a"))
	cast, ids := []string{}, []string{}
	seen := map[string]bool{}
	d.Find("#video_cast .star a, #video_cast a[href*='vl_star.php']").Each(func(_ int, s *goquery.Selection) {
		n := clean(s.Text())
		if n == "" || seen[n] || BoilerplateName(n) != "" {
			return
		}
		seen[n] = true
		cast = append(cast, n)
		if u, err := url.Parse(absolute(base, s.AttrOr("href", ""))); err == nil {
			if id := u.Query().Get("s"); id != "" {
				ids = append(ids, id)
			}
		}
	})
	f["cast"], f["cast_ids"] = cast, ids
	score := firstNonEmpty(d.Find("#video_review .score").First().Text(), labels["User Rating"])
	if m := ratingRe.FindStringSubmatch(norm.NFKC.String(score)); m != nil {
		f["javlibrary_user_rating"] = m[1]
	}
	if m := votesRe.FindStringSubmatch(norm.NFKC.String(clean(d.Find("#video_review").Text()))); m != nil {
		f["javlibrary_user_rating_votes"] = m[1]
	}
	return Record{EntityType: Release, SourceKey: key, URL: base, Fields: f}, nil
}

var (
	ratingRe = regexp.MustCompile(`(\d{1,2}(?:\.\d{1,2})?)`)
	votesRe  = regexp.MustCompile(`(?i)(\d[\d,]*)\s*(?:votes?|users?)`)
	// codePrefix is a code at the start of a title: "ABP-123 Title".
	codePrefix = regexp.MustCompile(`^([A-Za-z]{2,6}-\d{2,5})\s+(.+)$`)
)

// splitCodeTitle splits "ABP-123 Title" into code and title.
func splitCodeTitle(s string) (string, string) {
	if m := codePrefix.FindStringSubmatch(clean(s)); m != nil {
		return strings.ToUpper(m[1]), clean(m[2])
	}
	return "", clean(s)
}

// firstLinkedLabel is the first comma-separated value of a label text.
func firstLinkedLabel(v string) string {
	if p := splitNames(v); len(p) > 0 {
		return p[0]
	}
	return ""
}

func texts(sel *goquery.Selection) []string {
	out := []string{}
	seen := map[string]bool{}
	sel.Each(func(_ int, s *goquery.Selection) {
		if v := clean(s.Text()); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	})
	return out
}
