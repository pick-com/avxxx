package catalog

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/text/unicode/norm"
)

// XSList (https://xslist.org) adapter: performer profiles (aliases, birth
// date, birthplace, height, measurements, cup) for lookup and cross-checking.
// FINAL_DATA_DESIGN 2.4 tier "aggregator"; the filmography on a profile is
// kept as evidence only (release_refs, a list of product codes) and never
// creates releases or credits.
//
// Access status (2026-10-16): xslist.org answers every request from our
// egress, robots.txt included, with a Cloudflare managed challenge (HTTP 403,
// cf-mitigated: challenge). The Fetcher reports that as a block and the
// source is parked; nothing here tries to get past a challenge. The source is
// seeded disabled with license_status review_required (migrations/*_vpn_sources.sql) and
// may run through a configured VPN egress (proxy_url) once an operator has
// confirmed the site serves real pages there and its terms permit it.
//
// The selectors were written against synthetic fixtures modelled on the
// site's documented URL scheme and schema.org Person microdata
// (testdata/xslist/README.md). They read microdata first and fall back to
// "Label: value" text, so they degrade to missing fields, not wrong ones.
type XSListAdapter struct{}

// XSListAdapterName is the adapter / sources.adapter name.
const XSListAdapterName = "xslist"

// XSListStartURL is the default discovery page (the performer ranking).
const XSListStartURL = "https://xslist.org/en/ranking"

func NewXSListAdapter() Adapter          { return XSListAdapter{} }
func (XSListAdapter) Name() string       { return XSListAdapterName }
func (XSListAdapter) SourceKind() string { return "aggregator" }
func (XSListAdapter) StartURL() string   { return XSListStartURL }

// XSListLookupURL is the site search for one performer name: the lookup
// entry point (a verify job or dry run with -url). Its result page is a
// discovery page like the ranking.
func XSListLookupURL(name string) string {
	return "https://xslist.org/search?" + url.Values{"query": {strings.TrimSpace(name)}, "lg": {"en"}}.Encode()
}

// xslistModelPath matches /{lang}/model/{id}.html (and the language-less
// /model/{id}.html form).
var xslistModelPath = regexp.MustCompile(`^/(?:[a-z]{2}(?:-[a-z]{2})?/)?model/(\d+)(?:\.html?)?/?$`)

func xslistClassify(raw string) (EntityType, string) {
	u, err := url.Parse(raw)
	if err != nil || !siteHost(u.Hostname(), "xslist.org") {
		return "", ""
	}
	if m := xslistModelPath.FindStringSubmatch(u.Path); m != nil {
		return Performer, m[1]
	}
	return "", ""
}

// siteHost reports whether host is domain or one of its subdomains, or a
// local test host.
func siteHost(host, domain string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == domain || strings.HasSuffix(h, "."+domain) || isTestHost(h)
}

// isTestHost lets httptest servers (127.0.0.1 / localhost) stand in for the
// site in tests and dry runs against a local mirror.
func isTestHost(h string) bool {
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || strings.HasSuffix(h, ".test")
}

func (a XSListAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
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
		h, _ := s.Attr("href")
		u := absolute(base, h)
		kind, key := xslistClassify(u)
		if kind != Performer || seen[key] {
			return
		}
		name := clean(s.Text())
		if name == "" {
			name = clean(s.AttrOr("title", ""))
		}
		if name == "" || BoilerplateName(name) != "" {
			return
		}
		seen[key] = true
		out = append(out, Record{EntityType: Performer, SourceKey: key, URL: u, Fields: map[string]any{"name": name}})
	})
	return out, nil
}

// NextPage follows the ranking / search pagination (rel=next or "Next").
func (XSListAdapter) NextPage(doc Document) string {
	return nextPageLink(doc, []string{".pagination a[rel='next']", ".pager a.next"}, []string{"Next", "Next »", "»", "下一页", "次へ"})
}

// xslistLabels are the profile labels of the text fallback, longest first
// within a family so "Birth Place" is not read as "Birth".
var xslistLabels = []string{
	"Japanese Name", "Also Known As", "Aliases", "Alias", "AKA",
	"Date of Birth", "Birthday", "Birth Date", "Birthplace", "Birth Place", "Place of Birth",
	"Height", "Measurements", "Body Measurements", "Cup Size", "Cup", "Blood Type",
	"AV Activity", "AV Debut", "Debut", "Retired", "Filmography",
}

func (a XSListAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	base := docBase(doc)
	kind, key := xslistClassify(base)
	if entity != Performer || kind != Performer {
		return Record{}, fmt.Errorf("xslist: unexpected %s page %s (only performer profiles are parsed)", entity, base)
	}
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	person := d.Find("[itemtype*='schema.org/Person']").First()
	if person.Length() == 0 {
		person = d.Selection
	}
	name := clean(person.Find("[itemprop='name']").First().Text())
	if name == "" || BoilerplateName(name, pageTitle(d)) != "" {
		name = performerName(d, "#layout h1", "main h1")
	}
	// "Yua Mikami (三上悠亜)": the parenthesised part is the Japanese name.
	nameJA := ""
	if i := strings.Index(name, "("); i > 0 && strings.HasSuffix(name, ")") {
		nameJA = clean(name[i+1 : len(name)-1])
		name = clean(name[:i])
	}
	if heading := clean(person.Find("h1").First().Text()); nameJA == "" && strings.HasSuffix(heading, ")") {
		if i := strings.LastIndex(heading, "("); i > 0 {
			nameJA = clean(heading[i+1 : len(heading)-1])
		}
	}
	if name == "" || BoilerplateName(name, pageTitle(d)) != "" {
		return Record{}, fmt.Errorf("xslist: no performer name on %s: %w", base, ErrSkipRecord)
	}
	labels := labeledTexts(d, xslistLabels)
	f := map[string]any{"name": name, FieldPageTitle: pageTitle(d)}
	if v := firstNonEmpty(labels["Japanese Name"], nameJA); v != "" && hasJapanese(v) {
		f["name_ja"] = v
	}
	aliases := []string{}
	person.Find("[itemprop='alternateName'], [itemprop='additionalName']").Each(func(_ int, s *goquery.Selection) {
		aliases = append(aliases, splitNames(s.Text())...)
	})
	aliases = append(aliases, splitNames(firstNonEmpty(labels["Also Known As"], labels["Aliases"], labels["Alias"], labels["AKA"]))...)
	f["aliases"] = uniqueNames(aliases, name, nameJA)
	birth := firstNonEmpty(person.Find("[itemprop='birthDate']").First().AttrOr("content", ""), person.Find("[itemprop='birthDate']").First().Text(),
		labels["Date of Birth"], labels["Birthday"], labels["Birth Date"])
	if v := looseDate(birth); v != "" {
		f["birth_date"] = v
	}
	if v := clean(firstNonEmpty(person.Find("[itemprop='birthPlace']").First().Text(), labels["Birthplace"], labels["Birth Place"], labels["Place of Birth"])); v != "" {
		f["birth_place"] = v
	}
	if v := number(firstNonEmpty(person.Find("[itemprop='height']").First().Text(), labels["Height"])); v != "" {
		f["height_cm"] = v
	}
	meas := clean(firstNonEmpty(labels["Measurements"], labels["Body Measurements"]))
	cup := ""
	if meas != "" {
		f["measurements"] = meas
		b, w, h, c := splitMeasurements(meas)
		setStr(f, "bust_cm", b)
		setStr(f, "waist_cm", w)
		setStr(f, "hip_cm", h)
		cup = c
	}
	if c := cupLetter(firstNonEmpty(labels["Cup Size"], labels["Cup"])); c != "" {
		cup = c
	}
	setStr(f, "cup_size", cup)
	setStr(f, "blood_type", strings.ToUpper(strings.TrimSuffix(clean(labels["Blood Type"]), "型")))
	if v := clean(firstNonEmpty(labels["AV Activity"])); v != "" {
		f["activity"] = v
	}
	if v := looseDate(firstNonEmpty(labels["AV Debut"], labels["Debut"])); v != "" {
		f["debut_date"] = v
	}
	// Filmography: product codes only, as evidence (list fields merge as
	// unions and never create releases or credits).
	f["release_refs"] = filmographyCodes(d)
	return Record{EntityType: Performer, SourceKey: key, URL: base, Fields: f}, nil
}

// productCode matches a JAV product code such as ABP-123 or SSIS-001.
var productCode = regexp.MustCompile(`\b([A-Z]{2,6})-?(\d{2,5})\b`)

// filmographyCodes collects the product codes of a profile's filmography
// table(s) (#filmography / a table under a "Filmography" heading / any table
// whose rows hold codes), de-duplicated in page order.
func filmographyCodes(d *goquery.Document) []string {
	out := []string{}
	seen := map[string]bool{}
	d.Find("#filmography, .filmography, table").Each(func(_ int, t *goquery.Selection) {
		if inChrome(t) {
			return
		}
		t.Find("td, li").Each(func(_ int, c *goquery.Selection) {
			for _, m := range productCode.FindAllStringSubmatch(norm.NFKC.String(c.Text()), -1) {
				code := m[1] + "-" + m[2]
				if !seen[code] {
					seen[code] = true
					out = append(out, code)
				}
			}
		})
	})
	return out
}

// labeledTexts finds "Label: value" pairs: an element whose own text starts
// with the label (the smallest such element wins, so a container does not
// swallow its children), or a dt/th label followed by its dd/td value.
func labeledTexts(d *goquery.Document, labels []string) map[string]string {
	out := map[string]string{}
	for _, label := range labels {
		best, bestLen := "", -1
		d.Find("p, li, dt, th, td, span, div, b, strong, label").Each(func(_ int, s *goquery.Selection) {
			if inChrome(s) {
				return
			}
			text := clean(s.Text())
			rest, ok := cutLabel(text, label)
			if !ok {
				return
			}
			if rest == "" && s.Is("dt, th, b, strong, span, label") {
				rest = clean(s.NextFiltered("dd, td").First().Text())
				if rest == "" && s.Is("b, strong, span, label") && s.Parent().Length() > 0 {
					rest, _ = cutLabel(clean(s.Parent().Text()), label)
				}
			}
			if bestLen < 0 || len(text) < bestLen {
				best, bestLen = rest, len(text)
			}
		})
		if bestLen >= 0 {
			out[label] = cutAtLabel(best, labels)
		}
	}
	return out
}

// cutLabel returns the text after "Label:" (or "Label：") when text starts
// with it, case-insensitively.
func cutLabel(text, label string) (string, bool) {
	if len(text) < len(label) || !strings.EqualFold(text[:len(label)], label) {
		return "", false
	}
	rest := strings.TrimSpace(text[len(label):])
	switch {
	case strings.HasPrefix(rest, ":"):
		rest = rest[1:]
	case strings.HasPrefix(rest, "："):
		rest = rest[len("："):]
	default:
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// cutAtLabel ends a value where the next "Label:" starts.
func cutAtLabel(v string, labels []string) string {
	lv := strings.ToLower(v)
	end := len(v)
	for _, l := range labels {
		ll := strings.ToLower(l)
		for from := 0; from < len(lv); {
			j := strings.Index(lv[from:], ll)
			if j < 0 {
				break
			}
			j += from
			if rest := strings.TrimLeft(lv[j+len(ll):], " \t"); j > 0 && (strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：")) {
				if j < end {
					end = j
				}
				break
			}
			from = j + len(ll)
		}
	}
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(v[:end]), "|/,;"))
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

var nameListSep = regexp.MustCompile(`\s*[,，、/／;；]\s*`)

// splitNames splits an alias list on commas, slashes and the Japanese list
// separators.
func splitNames(s string) []string {
	out := []string{}
	for _, p := range nameListSep.Split(clean(s), -1) {
		if p = clean(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// uniqueNames drops blanks, boilerplate and the excluded names (the
// performer's own name), de-duplicated ignoring case and spacing.
func uniqueNames(names []string, exclude ...string) []string {
	seen := map[string]bool{}
	for _, e := range exclude {
		if k := nameKey(e); k != "" {
			seen[k] = true
		}
	}
	out := []string{}
	for _, n := range names {
		k := nameKey(n)
		if k == "" || seen[k] || BoilerplateName(n) != "" {
			continue
		}
		seen[k] = true
		out = append(out, n)
	}
	return out
}

func hasJapanese(s string) bool {
	for _, r := range s {
		if (r >= 0x3040 && r <= 0x30ff) || (r >= 0x4e00 && r <= 0x9fff) {
			return true
		}
	}
	return false
}

// looseDateLayouts are the date shapes profile and release pages print.
var looseDateLayouts = []string{"2006-01-02", "2006/01/02", "2006.01.02", "2006-1-2", "2006/1/2", "2006年1月2日",
	"January 2, 2006", "Jan 2, 2006", "2 January 2006", "02 Jan 2006"}

// looseDate returns s as YYYY-MM-DD, or "" when it is not a full date (a
// year or "unknown" is never stored in a date column).
func looseDate(s string) string {
	s = clean(norm.NFKC.String(s))
	if s == "" {
		return ""
	}
	cands := []string{s}
	if m := dateInText.FindString(s); m != "" && m != s {
		cands = append(cands, strings.Join(strings.Fields(m), ""))
	}
	if i := strings.IndexAny(s, "(（"); i > 0 {
		cands = append(cands, clean(s[:i]))
	}
	for _, c := range cands {
		for _, l := range looseDateLayouts {
			if t, err := time.Parse(l, c); err == nil {
				return t.Format(time.DateOnly)
			}
		}
	}
	return ""
}

var (
	measB   = regexp.MustCompile(`(?i)\bB\s*(\d{2,3})(?:\s*\(\s*([A-Z])\s*\))?`)
	measW   = regexp.MustCompile(`(?i)\bW\s*(\d{2,3})`)
	measH   = regexp.MustCompile(`(?i)\bH\s*(\d{2,3})`)
	measBWH = regexp.MustCompile(`(\d{2,3})\s*([A-K])?\s*[-/]\s*(\d{2,3})\s*[-/]\s*(\d{2,3})`)
	cupRe   = regexp.MustCompile(`(?i)^\s*([A-K])(?:\s*cup|\s*カップ)?\s*$`)
)

// splitMeasurements reads "B83(F) / W57 / H85" or "83F-57-85" into bust,
// waist, hip (cm) and the cup letter when it is printed with the bust.
func splitMeasurements(s string) (bust, waist, hip, cup string) {
	s = norm.NFKC.String(s)
	if m := measB.FindStringSubmatch(s); m != nil {
		bust, cup = m[1], strings.ToUpper(m[2])
		if m := measW.FindStringSubmatch(s); m != nil {
			waist = m[1]
		}
		if m := measH.FindStringSubmatch(s); m != nil {
			hip = m[1]
		}
		return
	}
	if m := measBWH.FindStringSubmatch(s); m != nil {
		return m[1], m[3], m[4], strings.ToUpper(m[2])
	}
	return
}

func cupLetter(s string) string {
	if m := cupRe.FindStringSubmatch(norm.NFKC.String(s)); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// docBase is the URL a document is resolved against.
func docBase(doc Document) string {
	if doc.FinalURL != "" {
		return doc.FinalURL
	}
	return doc.URL
}
