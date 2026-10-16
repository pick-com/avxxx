package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"avxxx/internal/identity"

	"github.com/PuerkitoBio/goquery"
	"github.com/andybalholm/cascadia"
	"golang.org/x/text/unicode/norm"
)

// Official maker catalogs (FINAL_DATA_DESIGN 2.4 tier official_maker: S1,
// MOODYZ, IDEA POCKET, Prestige, ...). One configuration-driven adapter
// serves every studio: a MakerProfile names the site, its listing and detail
// URL shapes, CSS selectors per field, the date layouts, the product-code
// prefixes the maker owns and so the maker scope. The adapter is registered
// as "maker:<slug>" and each studio is its own sources row (own robots.txt,
// crawl policy and request spacing, license review).
//
// Like every HTML adapter it only parses documents the Fetcher hands it.
// Records carry the profile's maker name, whose identifier scope is always
// "maker:<Slug(maker)>" (MakerScope, slug rules of #82): Validate rejects a
// maker name without an ASCII slug.

// MakerAdapterPrefix prefixes every maker adapter name ("maker:s1").
const MakerAdapterPrefix = "maker:"

// MakerSourceKind is the tier of every maker catalog record.
const MakerSourceKind = "official_maker"

// MakerProfilesEnv names a JSON file ({"profiles": [...]}) whose profiles
// replace built-in ones of the same slug or add new makers.
const MakerProfilesEnv = "MAKER_PROFILES_FILE"

// MakerReleaseSelectors are the release detail page selectors. Each is a CSS
// selector, optionally suffixed "@attr" to read an attribute instead of the
// text. Single-value fields take the first match; performers and genres take
// every match.
type MakerReleaseSelectors struct {
	Title       string `json:"title"`
	Code        string `json:"code,omitempty"`
	ReleaseDate string `json:"release_date,omitempty"`
	Runtime     string `json:"runtime,omitempty"`
	Label       string `json:"label,omitempty"`
	Series      string `json:"series,omitempty"`
	Director    string `json:"director,omitempty"`
	Performers  string `json:"performers,omitempty"`
	Genres      string `json:"genres,omitempty"`
	Cover       string `json:"cover,omitempty"`
}

// MakerPerformerSelectors are the actress profile page selectors.
type MakerPerformerSelectors struct {
	Name         string `json:"name"`
	NameRomaji   string `json:"name_romaji,omitempty"`
	NameKana     string `json:"name_kana,omitempty"`
	BirthDate    string `json:"birth_date,omitempty"`
	Height       string `json:"height,omitempty"`
	Measurements string `json:"measurements,omitempty"`
	Cup          string `json:"cup,omitempty"`
	BirthPlace   string `json:"birth_place,omitempty"`
}

// MakerProfile configures one studio's official catalog.
type MakerProfile struct {
	// Slug names the adapter ("maker:<slug>") and the fixture directory
	// ("maker-<slug>"): lower-case letters, digits and hyphens.
	Slug string `json:"slug"`
	// Maker is the maker name stored on every release; it must have an
	// ASCII slug so the identifier scope is "maker:<slug>". Use the name the
	// other sources use (JAV Database "Studio:") so scopes line up.
	Maker string `json:"maker"`
	// BaseURL is the site origin; discovered links on other hosts are ignored.
	BaseURL string `json:"base_url"`
	// ListingURL is the first page of the newest-first release listing (the
	// seeded sources.url). Incremental runs stop at the previous head, so the
	// listing must be sorted by release date, newest first.
	ListingURL string `json:"listing_url"`
	// ListingPattern matches the path?query of listing pages; a next-page
	// link that does not match is ignored.
	ListingPattern string `json:"listing_pattern"`
	// DetailPattern matches the path?query of a release detail page; its
	// first capture group is the source record key.
	DetailPattern string `json:"detail_pattern"`
	// PerformerPattern (optional) does the same for actress pages.
	PerformerPattern string `json:"performer_pattern,omitempty"`
	// ItemSelector selects the listing's entry anchors (default "a[href]").
	ItemSelector string `json:"item_selector,omitempty"`
	// NextSelector selects the next-page anchor; rel=next and common
	// pagination markup are tried after it.
	NextSelector string `json:"next_selector,omitempty"`
	// CodePattern matches the normalized product code ("SONE-156") of the
	// maker's own titles. Other codes (guest or partner titles on the
	// maker's site) are skipped: the maker tier speaks only for its own.
	CodePattern string `json:"code_pattern"`
	// CodeFromKey derives the code from the detail key ("sone00156") when
	// the page carries none, and lets discovery skip foreign keys early.
	CodeFromKey bool `json:"code_from_key,omitempty"`
	// DateLayouts are Go time layouts tried in order (after NFKC).
	DateLayouts []string                `json:"date_layouts,omitempty"`
	Release     MakerReleaseSelectors   `json:"release"`
	Performer   MakerPerformerSelectors `json:"performer"`
}

// MakerProfileSet is the JSON shape of maker_profiles.json / MAKER_PROFILES_FILE.
type MakerProfileSet struct {
	Comment  string         `json:"_comment,omitempty"`
	Profiles []MakerProfile `json:"profiles"`
}

var profileSlug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// DefaultMakerDateLayouts apply when a profile lists none.
var DefaultMakerDateLayouts = []string{"2006年1月2日", "2006/1/2", "2006-01-02", "2006.1.2"}

// MakerAdapter is the adapter for one MakerProfile.
type MakerAdapter struct {
	p         MakerProfile
	base      *url.URL
	listing   *regexp.Regexp
	detail    *regexp.Regexp
	performer *regexp.Regexp
	code      *regexp.Regexp
}

// NewMakerAdapter validates p and returns its adapter.
func NewMakerAdapter(p MakerProfile) (*MakerAdapter, error) {
	a := &MakerAdapter{p: p}
	if !profileSlug.MatchString(p.Slug) {
		return nil, fmt.Errorf("maker profile %q: slug must be lower-case letters, digits and hyphens", p.Slug)
	}
	fail := func(format string, args ...any) (*MakerAdapter, error) {
		return nil, fmt.Errorf("maker profile %s: "+format, append([]any{p.Slug}, args...)...)
	}
	if scope := MakerScope(p.Maker, ""); !strings.HasPrefix(scope, "maker:") {
		return fail("maker %q has no ASCII slug (scope would be %q, want maker:<slug>)", p.Maker, scope)
	}
	var err error
	if a.base, err = url.Parse(p.BaseURL); err != nil || (a.base.Scheme != "https" && a.base.Scheme != "http") || a.base.Host == "" {
		return fail("base_url must be an absolute http(s) URL")
	}
	if p.ListingURL == "" || !SameSite(p.BaseURL, p.ListingURL) {
		return fail("listing_url must be on the base_url host")
	}
	compile := func(name, expr string, required, capture bool) (*regexp.Regexp, error) {
		if expr == "" {
			if required {
				return nil, fmt.Errorf("maker profile %s: %s is required", p.Slug, name)
			}
			return nil, nil
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("maker profile %s: %s: %w", p.Slug, name, err)
		}
		if capture && re.NumSubexp() < 1 {
			return nil, fmt.Errorf("maker profile %s: %s needs a capture group for the record key", p.Slug, name)
		}
		return re, nil
	}
	if a.listing, err = compile("listing_pattern", p.ListingPattern, true, false); err != nil {
		return nil, err
	}
	if a.detail, err = compile("detail_pattern", p.DetailPattern, true, true); err != nil {
		return nil, err
	}
	if a.performer, err = compile("performer_pattern", p.PerformerPattern, false, true); err != nil {
		return nil, err
	}
	if a.code, err = compile("code_pattern", p.CodePattern, true, false); err != nil {
		return nil, err
	}
	if !a.listing.MatchString(requestURI(p.ListingURL)) {
		return fail("listing_url does not match listing_pattern")
	}
	if p.Release.Title == "" {
		return fail("release.title selector is required")
	}
	if p.PerformerPattern != "" && p.Performer.Name == "" {
		return fail("performer.name selector is required with performer_pattern")
	}
	for name, sel := range a.selectors() {
		css, _ := splitSelector(sel)
		if css == "" {
			continue
		}
		if _, err := cascadia.Compile(css); err != nil {
			return fail("selector %s: %v", name, err)
		}
	}
	return a, nil
}

func (a *MakerAdapter) selectors() map[string]string {
	r, pf := a.p.Release, a.p.Performer
	return map[string]string{
		"item_selector": a.p.ItemSelector, "next_selector": a.p.NextSelector,
		"release.title": r.Title, "release.code": r.Code, "release.release_date": r.ReleaseDate, "release.runtime": r.Runtime,
		"release.label": r.Label, "release.series": r.Series, "release.director": r.Director, "release.performers": r.Performers,
		"release.genres": r.Genres, "release.cover": r.Cover,
		"performer.name": pf.Name, "performer.name_romaji": pf.NameRomaji, "performer.name_kana": pf.NameKana,
		"performer.birth_date": pf.BirthDate, "performer.height": pf.Height, "performer.measurements": pf.Measurements,
		"performer.cup": pf.Cup, "performer.birth_place": pf.BirthPlace,
	}
}

// Profile returns the adapter's profile.
func (a *MakerAdapter) Profile() MakerProfile { return a.p }

func (a *MakerAdapter) Name() string { return MakerAdapterPrefix + a.p.Slug }

// SourceKind is the tier of every record (the seeded source row agrees).
func (a *MakerAdapter) SourceKind() string { return MakerSourceKind }

// MakerScope is the identifier scope of the profile's releases ("maker:<slug>").
func (a *MakerAdapter) MakerScope() string { return MakerScope(a.p.Maker, "") }

// FixtureDir keeps the fixture directory name free of ':' ("maker-s1").
func (a *MakerAdapter) FixtureDir() string { return "maker-" + a.p.Slug }

// StartURL is the default -dry-run discovery URL.
func (a *MakerAdapter) StartURL() string { return a.p.ListingURL }

func requestURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.RequestURI()
}

// classify maps an absolute URL on the maker's host to an entity and key.
func (a *MakerAdapter) classify(raw string) (EntityType, string) {
	if !SameSite(a.p.BaseURL, raw) {
		return "", ""
	}
	path := requestURI(raw)
	if m := a.detail.FindStringSubmatch(path); m != nil && m[1] != "" {
		return Release, m[1]
	}
	if a.performer != nil {
		if m := a.performer.FindStringSubmatch(path); m != nil && m[1] != "" {
			return Performer, m[1]
		}
	}
	return "", ""
}

// NormalizeMakerCode returns the canonical product code in raw ("sone00156",
// "品番：SONE-156", "ＳＯＮＥ１５６" -> "SONE-156"), or "" when raw holds no
// letters+digits code. Numeric label prefixes (DMM content ids) are dropped
// and digits de-padded to three places, as identity.CodeKeys does.
func NormalizeMakerCode(raw string) string {
	s := norm.NFKC.String(raw)
	m := makerCodeInText.FindString(s)
	if m == "" {
		return ""
	}
	keys := identity.CodeKeys(m)
	if len(keys) == 0 {
		return ""
	}
	if k := keys[len(keys)-1]; dvdCodeShape.MatchString(k) {
		return strings.ToUpper(k)
	}
	return ""
}

var makerCodeInText = regexp.MustCompile(`(?i)\d*[a-z]{2,}[-_ ]?\d{2,}[a-z]?`)

// OwnsCode reports whether a normalized code belongs to this maker.
func (a *MakerAdapter) OwnsCode(code string) bool { return code != "" && a.code.MatchString(code) }

func (a *MakerAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return nil, err
	}
	base := doc.FinalURL
	if base == "" {
		base = doc.URL
	}
	sel := a.p.ItemSelector
	if sel == "" {
		sel = "a[href]"
	}
	seen := map[string]bool{}
	out := []Record{}
	d.Find(sel).Each(func(_ int, s *goquery.Selection) {
		h, ok := s.Attr("href")
		if !ok {
			return
		}
		u := absolute(base, h)
		kind, key := a.classify(u)
		if key == "" || seen[string(kind)+":"+key] {
			return
		}
		fields := map[string]any{}
		if kind == Release && a.p.CodeFromKey {
			// Foreign titles listed on the maker's site are not fetched.
			code := NormalizeMakerCode(key)
			if !a.OwnsCode(code) {
				return
			}
			fields["dvd_code"] = code
		}
		if kind == Performer {
			name := clean(s.Text())
			if name == "" {
				return
			}
			fields["name"] = name
		}
		seen[string(kind)+":"+key] = true
		out = append(out, Record{EntityType: kind, SourceKey: key, URL: u, Fields: fields})
	})
	return out, nil
}

// NextPage follows the listing pagination; only listing pages on the maker's
// host qualify.
func (a *MakerAdapter) NextPage(doc Document) string {
	var sels []string
	if a.p.NextSelector != "" {
		sels = append(sels, a.p.NextSelector)
	}
	next := nextPageLink(doc, sels, []string{"次へ", "次へ »", "次のページ", "Next", "»"})
	if next == "" || !SameSite(a.p.BaseURL, next) || !a.listing.MatchString(requestURI(next)) {
		return ""
	}
	return next
}

// ErrMakerNoTitle is a release page without a title: the selectors no longer
// fit the site, so the run fails loudly instead of skipping every page.
var ErrMakerNoTitle = errors.New("maker: release page has no title (check the profile's release.title selector)")

func (a *MakerAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	_ = ctx
	d, err := goquery.NewDocumentFromReader(strings.NewReader(string(doc.Body)))
	if err != nil {
		return Record{}, err
	}
	pageURL := doc.FinalURL
	if pageURL == "" {
		pageURL = doc.URL
	}
	kind, key := a.classify(pageURL)
	if kind != entity || key == "" {
		return Record{}, fmt.Errorf("%s: unexpected page %s", a.Name(), pageURL)
	}
	f := map[string]any{"maker_profile": a.p.Slug, "maker_scope": a.MakerScope()}
	if entity == Performer {
		pf := a.p.Performer
		name := one(d, pf.Name)
		if name == "" || BoilerplateName(name, pageTitle(d)) != "" {
			return Record{}, fmt.Errorf("%s: no performer name on %s: %w", a.Name(), pageURL, ErrSkipRecord)
		}
		f["name"] = name
		f[FieldPageTitle] = pageTitle(d)
		if HasKana(name) || hasHan(name) {
			f["name_ja"] = name
		}
		setStr(f, "name_romaji", one(d, pf.NameRomaji))
		setStr(f, "name_kana", one(d, pf.NameKana))
		setStr(f, "birth_date", a.date(one(d, pf.BirthDate)))
		if h := number(norm.NFKC.String(one(d, pf.Height))); h != "" {
			f["height_cm"] = h
		}
		meas := one(d, pf.Measurements)
		setStr(f, "measurements", meas)
		cup := one(d, pf.Cup)
		if cup == "" {
			if m := cupInText.FindStringSubmatch(norm.NFKC.String(meas)); m != nil {
				cup = m[1]
			}
		}
		setStr(f, "cup_size", strings.TrimSuffix(strings.ToUpper(norm.NFKC.String(cup)), "カップ"))
		setStr(f, "birth_place", one(d, pf.BirthPlace))
		return Record{EntityType: Performer, SourceKey: key, URL: pageURL, Fields: f}, nil
	}
	r := a.p.Release
	title := one(d, r.Title)
	if title == "" {
		return Record{}, fmt.Errorf("%s: %s: %w", a.Name(), pageURL, ErrMakerNoTitle)
	}
	code := NormalizeMakerCode(one(d, r.Code))
	if code == "" && a.p.CodeFromKey {
		code = NormalizeMakerCode(key)
	}
	if code == "" {
		return Record{}, fmt.Errorf("%s: no product code on %s: %w", a.Name(), pageURL, ErrSkipRecord)
	}
	if !a.OwnsCode(code) {
		return Record{}, fmt.Errorf("%s: %s is not a %s title (code_pattern): %w", a.Name(), code, a.p.Maker, ErrSkipRecord)
	}
	f["title"] = title
	f["dvd_code"] = code
	f["maker"] = a.p.Maker
	setStr(f, "release_date", a.date(one(d, r.ReleaseDate)))
	if m := number(norm.NFKC.String(one(d, r.Runtime))); m != "" {
		f["runtime_minutes"] = m
	}
	setStr(f, "label", one(d, r.Label))
	setStr(f, "series", one(d, r.Series))
	setStr(f, "director", one(d, r.Director))
	if r.Performers != "" {
		f["performers"] = all(d, r.Performers, true)
	}
	if r.Genres != "" {
		f["genres"] = all(d, r.Genres, false)
		f["tags"] = f["genres"]
	}
	if c := one(d, r.Cover); c != "" {
		f["cover_url"] = absolute(pageURL, c)
	}
	return Record{EntityType: Release, SourceKey: key, URL: pageURL, Fields: f}, nil
}

var cupInText = regexp.MustCompile(`([A-Z])\s*カップ`)

func hasHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func setStr(f map[string]any, k, v string) {
	if v != "" {
		f[k] = v
	}
}

func clean(s string) string { return strings.Join(strings.Fields(s), " ") }

// splitSelector splits "css@attr" into its parts (attr "" = text).
func splitSelector(sel string) (css, attr string) {
	sel = strings.TrimSpace(sel)
	if i := strings.LastIndex(sel, "@"); i > 0 && !strings.ContainsAny(sel[i:], " ]\"'") {
		return strings.TrimSpace(sel[:i]), sel[i+1:]
	}
	return sel, ""
}

func selValue(s *goquery.Selection, attr string) string {
	if attr != "" {
		v, _ := s.Attr(attr)
		return strings.TrimSpace(v)
	}
	return clean(s.Text())
}

// one returns the first non-empty value of sel ("" when sel is empty).
func one(d *goquery.Document, sel string) string {
	css, attr := splitSelector(sel)
	if css == "" {
		return ""
	}
	out := ""
	d.Find(css).EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if inChrome(s) {
			return true
		}
		out = selValue(s, attr)
		return out == ""
	})
	return out
}

// all returns every distinct non-empty value of sel, in page order; names
// drops boilerplate (site chrome text) as performer lists do.
func all(d *goquery.Document, sel string, names bool) []string {
	css, attr := splitSelector(sel)
	out := []string{}
	seen := map[string]bool{}
	d.Find(css).Each(func(_ int, s *goquery.Selection) {
		if inChrome(s) {
			return
		}
		v := selValue(s, attr)
		if v == "" || seen[v] || (names && BoilerplateName(v) != "") {
			return
		}
		seen[v] = true
		out = append(out, v)
	})
	return out
}

var dateInText = regexp.MustCompile(`\d{4}\s*[年/.\-]\s*\d{1,2}\s*[月/.\-]\s*\d{1,2}\s*日?`)

// date parses a date with the profile's layouts and returns YYYY-MM-DD, or ""
// when nothing parses (never a raw string the date columns cannot take).
func (a *MakerAdapter) date(raw string) string {
	s := strings.TrimSpace(norm.NFKC.String(raw))
	if s == "" {
		return ""
	}
	layouts := a.p.DateLayouts
	if len(layouts) == 0 {
		layouts = DefaultMakerDateLayouts
	}
	cands := []string{s}
	if m := dateInText.FindString(s); m != "" && m != s {
		cands = append(cands, strings.Join(strings.Fields(m), ""))
	}
	for _, c := range cands {
		for _, l := range layouts {
			if t, err := time.Parse(l, c); err == nil {
				return t.Format(time.DateOnly)
			}
		}
	}
	return ""
}

//go:embed maker_profiles.json
var builtinMakerProfilesJSON []byte

// ParseMakerProfiles decodes a profile set.
func ParseMakerProfiles(b []byte) ([]MakerProfile, error) {
	var set MakerProfileSet
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // a misspelt selector key must not be silently ignored
	if err := dec.Decode(&set); err != nil {
		return nil, fmt.Errorf("maker profiles: %w", err)
	}
	return set.Profiles, nil
}

// BuiltinMakerProfiles returns the shipped profiles (maker_profiles.json).
func BuiltinMakerProfiles() []MakerProfile {
	ps, err := ParseMakerProfiles(builtinMakerProfilesJSON)
	if err != nil {
		panic(err) // embedded file; covered by tests
	}
	return ps
}

// MakerAdapters builds adapters for profiles, in order; a later profile with
// the same slug replaces an earlier one.
func MakerAdapters(profiles []MakerProfile) ([]Adapter, error) {
	idx := map[string]int{}
	var out []Adapter
	for _, p := range profiles {
		a, err := NewMakerAdapter(p)
		if err != nil {
			return nil, err
		}
		if i, ok := idx[p.Slug]; ok {
			out[i] = a
			continue
		}
		idx[p.Slug] = len(out)
		out = append(out, a)
	}
	return out, nil
}

func builtinMakerAdapters() []Adapter {
	out, err := MakerAdapters(BuiltinMakerProfiles())
	if err != nil {
		panic(err) // embedded file; covered by tests
	}
	return out
}

// AdaptersFromEnv is Adapters with the built-in maker profiles merged with
// the file named by MAKER_PROFILES_FILE (same slug replaces, new slug adds).
func AdaptersFromEnv(getenv func(string) string) ([]Adapter, error) {
	path := strings.TrimSpace(getenv(MakerProfilesEnv))
	if path == "" {
		return Adapters(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MakerProfilesEnv, err)
	}
	extra, err := ParseMakerProfiles(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MakerProfilesEnv, err)
	}
	makers, err := MakerAdapters(append(BuiltinMakerProfiles(), extra...))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MakerProfilesEnv, err)
	}
	var out []Adapter
	for _, a := range Adapters() {
		if !strings.HasPrefix(a.Name(), MakerAdapterPrefix) {
			out = append(out, a)
		}
	}
	return append(out, makers...), nil
}
