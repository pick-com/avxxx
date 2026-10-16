package catalog

// Performer-name hygiene shared by the parsers and the R-PERFORMER-NAME audit
// rule. Crawled pages carry site chrome (logo headings, <title> suffixes,
// navigation links to the idol index) whose text is the site's own title, e.g.
// "JAV Japanese Porn Actress & Idol Database". A parser that takes "the first
// <h1>" or falls back to <title> can save that text as a person; these helpers
// pick the entity heading and recognise boilerplate so it is never stored.

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/PuerkitoBio/goquery"
)

// FieldPageTitle is the Record field holding the raw <title> of a performer
// page. It is evidence for R-PERFORMER-NAME (a name equal to the source's own
// title is boilerplate), not a reviewable performer field.
const FieldPageTitle = "page_title"

// ErrSkipRecord marks a detail page that parsed but carries no usable entity
// (for example a performer page whose only heading is the site title). The
// crawl records a detail checkpoint failure and moves on instead of failing
// the whole run.
var ErrSkipRecord = errors.New("catalog: page has no usable entity")

// Name rejection reasons returned by BoilerplateName.
const (
	NameBoilerplateWords = "boilerplate_words"
	NameURLLike          = "url_like"
	NameTooLong          = "too_long"
	NameTooManyWords     = "too_many_words"
	NameMatchesPageTitle = "matches_page_title"
	NameMatchesSiteName  = "matches_site_name"
)

// MaxNameRunes and MaxNameWords bound a plausible performer name. Stage names
// in any script stay well below them; site titles and taglines do not.
const (
	MaxNameRunes = 40
	MaxNameWords = 5
)

var (
	boilerplateWords = regexp.MustCompile(`(?i)\b(database|porn|pornstars?|actress(es)?\s*&\s*idols?|idols?\s+(list|database|ranking)|official\s+(site|website|page)|homepage|home\s+page|all\s+(idols|actresses)|jav\s+(profile|database|guru|idols?|actress(es)?))\b|\bjav\b|公式サイト|オフィシャルサイト|女優一覧|女優検索|アイドル一覧|一覧|ランキング`)
	urlLike          = regexp.MustCompile(`(?i)(https?://|www\.|\b[a-z0-9-]+\.(com|net|org|jp|co|tv|xyz|info|io|me|cc|to)\b)`)
	titleSeparators  = regexp.MustCompile(`\s+[|｜\-–—:]\s+|\s*[｜|]\s*`)
)

// BoilerplateName reports why name looks like a site title or page
// boilerplate rather than a person's name ("" when it looks fine).
// pageTitles are the <title> texts of the source pages the name came from:
// a name equal to a whole title, or to its site-name part (the last
// " - " / " | " segment), is the source's own title.
func BoilerplateName(name string, pageTitles ...string) string {
	n := strings.Join(strings.Fields(name), " ")
	if n == "" {
		return ""
	}
	key := nameKey(n)
	for _, t := range pageTitles {
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if nameKey(t) == key {
			return NameMatchesPageTitle
		}
		if parts := titleSeparators.Split(t, -1); len(parts) > 1 && nameKey(parts[len(parts)-1]) == key {
			return NameMatchesSiteName
		}
	}
	switch {
	case urlLike.MatchString(n):
		return NameURLLike
	case boilerplateWords.MatchString(n):
		return NameBoilerplateWords
	case utf8.RuneCountInString(n) > MaxNameRunes:
		return NameTooLong
	case len(strings.Fields(n)) > MaxNameWords:
		return NameTooManyWords
	}
	return ""
}

// nameKey folds case and drops everything but letters and digits.
func nameKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// chromeSelector matches site chrome: a heading inside it is the logo or a
// widget title, never the entity on the page.
const chromeSelector = "header, nav, footer, aside, .site-header, .site-branding, .site-title, .site-description, .logo, #logo, .header, #header, .navbar, .menu, .widget, .sidebar, #sidebar"

// pageTitle returns the document <title>, whitespace collapsed.
func pageTitle(d *goquery.Document) string {
	return strings.Join(strings.Fields(d.Find("head title").First().Text()), " ")
}

// inChrome reports whether s sits inside site chrome.
func inChrome(s *goquery.Selection) bool {
	return s.Is(chromeSelector) || s.ParentsFiltered(chromeSelector).Length() > 0
}

// entityHeading returns the text of the page's entity heading: the first of
// preferred that exists, else the first <h1> outside site chrome. "" when the
// page has none (only a logo heading, say).
func entityHeading(d *goquery.Document, preferred ...string) string {
	for _, sel := range preferred {
		if t := strings.TrimSpace(d.Find(sel).First().Text()); t != "" {
			return t
		}
	}
	out := ""
	d.Find("h1").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if inChrome(s) {
			return true
		}
		out = strings.TrimSpace(s.Text())
		return out == ""
	})
	return out
}

// titleSubject returns the entity part of a "<entity> - <site>" page title:
// the first segment when the title has a separator, else "" (a bare title is
// the site's own name).
func titleSubject(title string) string {
	parts := titleSeparators.Split(strings.TrimSpace(title), -1)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

// performerName picks a performer name from a detail page: the entity
// heading (with a " - Profile"-style suffix cut), else the subject of the
// <title>. Any candidate that looks like boilerplate is rejected; "" means
// the page has no usable name.
func performerName(d *goquery.Document, preferred ...string) string {
	title := pageTitle(d)
	for _, c := range []string{entityHeading(d, preferred...), titleSubject(title)} {
		c = strings.Join(strings.Fields(c), " ")
		if i := strings.Index(c, " - "); i > 0 {
			c = c[:i]
		}
		if c != "" && BoilerplateName(c, title) == "" {
			return c
		}
	}
	return ""
}

// performerLinks returns the text of links to performer detail pages
// (isPerformer decides from the absolute URL), skipping site chrome and
// boilerplate text, de-duplicated in page order.
func performerLinks(sel *goquery.Selection, base string, isPerformer func(string) bool) []string {
	out := []string{}
	seen := map[string]bool{}
	sel.Each(func(_ int, s *goquery.Selection) {
		if inChrome(s) {
			return
		}
		if h, ok := s.Attr("href"); ok && !isPerformer(absolute(base, h)) {
			return
		}
		v := strings.Join(strings.Fields(s.Text()), " ")
		if v == "" || seen[v] || BoilerplateName(v) != "" {
			return
		}
		seen[v] = true
		out = append(out, v)
	})
	return out
}
