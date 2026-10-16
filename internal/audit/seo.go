package audit

// R-SEO (PRD 11 rule H 收录检查; PRD 13 SEO): the pages the sitemap lists
// should each be indexable on their own. The rule looks at data the system
// already has:
//
//   - every release and performer the sitemap lists (store/sitemap.go: playable
//     releases, active performers with a published release), for a blank title
//     or name, a fallback slug ("release-42"), a release description with no
//     facts to say, a missing stored cover (VideoObject thumbnail / og:image,
//     only once cover storage is in use) and titles shared with other listed
//     releases, per language (the canonical title, and reviewed title
//     translations);
//   - the latest `npm run seo:check` report (nightly_runs job "seo.check",
//     web/scripts/seo-check.mjs): sampled pages that failed SSR checks (status,
//     h1, canonical, hreflang, lang, noindex) become findings on their entity;
//     sitemap types listing fewer URLs than expected, languages without a
//     sitemap file (missing hreflang alternates) and a missing or stale report
//     are site findings in the run detail.
//
// One open issue per entity carries all of its findings. The rule never
// changes data: renaming a slug moves an indexed URL, and titles and
// descriptions need a person (R-TITLE already trims whitespace). An issue is
// resolved by the next run once its entity no longer has findings or leaves
// the sitemap.

import (
	"context"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SEOCheckJob is the nightly_runs job of the web SEO check (api.SEOCheckJob).
const SEOCheckJob = "seo.check"

// SEOReportMaxAge: an older seo.check report is reported as stale and its
// page failures are not turned into issues (the page may have been fixed).
const SEOReportMaxAge = 8 * 24 * time.Hour

// R-SEO checks.
const (
	SEOBlankTitle      = "blank_title"      // empty <h1> / <title>
	SEOFallbackSlug    = "fallback_slug"    // slug is the release-<id> / performer-<id> repair value
	SEOThinDescription = "thin_description" // no synopsis and no facts: the generic description
	SEONoImage         = "no_image"         // no stored cover: VideoObject without thumbnailUrl, no og:image
	SEODuplicateTitle  = "duplicate_title"  // another listed release has the same title in a language
	SEOSSRFailed       = "ssr_failed"       // the latest seo.check sampled this page and it failed
)

// Site-level findings (run detail only; audit_issues holds entities).
const (
	SEOSiteNoReport          = "no_seo_report"         // seo.check never recorded
	SEOSiteStaleReport       = "stale_seo_report"      // older than SEOReportMaxAge
	SEOSiteSitemapGap        = "sitemap_gap"           // a type lists fewer URLs than expected
	SEOSiteMissingLangs      = "sitemap_missing_langs" // languages without sitemap files (hreflang gaps)
	SEOSiteCountsUnavailable = "sitemap_counts_unavailable"
)

// SEOFacts is what R-SEO needs about one sitemap-listed release or performer.
type SEOFacts struct {
	EntityType string // "release" or "performer"
	ID         int64
	Slug       string
	Title      string // canonical_title / canonical_name
	// HasSynopsis: a non-blank synopsis (release) or biography (performer),
	// used as the meta description as is.
	HasSynopsis bool
	// HasFacts: a release has a maker, release date, runtime or a credited
	// performer for the generated description. Always true for performers
	// (their description lists their works).
	HasFacts bool
	// HasImage: a stored, visible cover (release) or avatar (performer).
	HasImage bool
	// Popularity orders the queue: watch count (release), published
	// releases (performer).
	Popularity int64
}

// TitleGroup is a set of sitemap-listed releases sharing one normalized
// title in a language. Lang "" is the canonical title, shown in every
// language without a reviewed translation.
type TitleGroup struct {
	Lang  string
	Title string
	IDs   []int64
}

// SEOSiteFacts are catalog-wide counts for the run detail.
type SEOSiteFacts struct {
	SitemapReleases   int64 `json:"sitemap_releases"`
	PublishedReleases int64 `json:"published_releases"`
	SitemapPerformers int64 `json:"sitemap_performers"`
	// ReleasesWithCover: sitemap releases with a stored, visible cover. Zero
	// means cover storage is not in use yet, so SEONoImage is not raised.
	ReleasesWithCover int64 `json:"releases_with_cover"`
	// TitleTranslations: sitemap releases with a reviewed (or stale, still
	// served) title translation, per language.
	TitleTranslations map[string]int64 `json:"title_translations"`
}

// SEOFinding is one problem with one page.
type SEOFinding struct {
	Check    string   `json:"check"`
	Severity Severity `json:"severity"`
	// SEODuplicateTitle: the languages and up to 10 other release ids.
	Langs  []string `json:"langs,omitempty"`
	Others []int64  `json:"others,omitempty"`
	// SEOSSRFailed: the page URL and the checker's reasons.
	URL     string   `json:"url,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

// seoInputs is the cross-entity context of one run.
type seoInputs struct {
	dupes    map[int64]*SEOFinding            // release id -> duplicate finding
	failures map[string]map[string]SEOFinding // entity type -> slug -> failure
	coversOn bool
}

var fallbackSlug = regexp.MustCompile(`^(release|performer)-[0-9]+$`)

// CheckSEO returns the findings for one listed page, worst first.
func CheckSEO(f SEOFacts, in seoInputs) []SEOFinding {
	var out []SEOFinding
	if strings.TrimSpace(f.Title) == "" {
		out = append(out, SEOFinding{Check: SEOBlankTitle, Severity: SeverityHigh})
	}
	if fs, ok := in.failures[f.EntityType][f.Slug]; ok {
		out = append(out, fs)
	}
	if f.EntityType == "release" {
		if d := in.dupes[f.ID]; d != nil {
			out = append(out, *d)
		}
		if !f.HasSynopsis && !f.HasFacts {
			out = append(out, SEOFinding{Check: SEOThinDescription, Severity: SeverityLow})
		}
		if in.coversOn && !f.HasImage {
			out = append(out, SEOFinding{Check: SEONoImage, Severity: SeverityLow})
		}
	}
	if fallbackSlug.MatchString(f.Slug) {
		out = append(out, SEOFinding{Check: SEOFallbackSlug, Severity: SeverityLow})
	}
	sort.SliceStable(out, func(i, j int) bool { return severityRank(out[i].Severity) < severityRank(out[j].Severity) })
	return out
}

func severityRank(s Severity) int {
	switch s {
	case SeverityBlocking:
		return 0
	case SeverityHigh:
		return 1
	case SeverityMedium:
		return 2
	}
	return 3
}

// seoSuggestions maps checks to what an admin can do about them.
var seoSuggestions = map[string]string{
	SEOBlankTitle:      "edit_title",
	SEOFallbackSlug:    "rename_slug_with_redirect",
	SEOThinDescription: "add_synopsis_or_metadata",
	SEONoImage:         "fetch_or_allow_cover",
	SEODuplicateTitle:  "disambiguate_title",
	SEOSSRFailed:       "inspect_page",
}

// SEOIssue builds the issue for findings on f. The fingerprint is the set of
// checks, so dismissing an issue holds until a new kind of finding appears.
func SEOIssue(f SEOFacts, fs []SEOFinding, seen time.Time) Issue {
	sev := SeverityLow
	checks := make([]string, 0, len(fs))
	var actions []string
	for _, x := range fs {
		if severityRank(x.Severity) < severityRank(sev) {
			sev = x.Severity
		}
		checks = append(checks, x.Check)
		actions = append(actions, seoSuggestions[x.Check])
	}
	sort.Strings(checks)
	key := "title"
	if f.EntityType == "performer" {
		key = "name"
	}
	return Issue{Rule: RuleSEO, EntityType: f.EntityType, EntityID: f.ID, Severity: sev,
		Detail: map[string]any{key: f.Title, "slug": f.Slug, "findings": fs,
			"suggested_actions": actions, "auto_action": "none"},
		Fingerprint: Fingerprint(append([]string{"seo"}, checks...)...), Priority: f.Popularity, LastSeenAt: seen}
}

// duplicateFindings folds title groups into one finding per release.
func duplicateFindings(groups []TitleGroup) map[int64]*SEOFinding {
	out := map[int64]*SEOFinding{}
	for _, g := range groups {
		lang := g.Lang
		if lang == "" {
			lang = "canonical"
		}
		for _, id := range g.IDs {
			d := out[id]
			if d == nil {
				d = &SEOFinding{Check: SEODuplicateTitle, Severity: SeverityLow}
				out[id] = d
			}
			if !containsStr(d.Langs, lang) {
				d.Langs = append(d.Langs, lang)
			}
			for _, o := range g.IDs {
				if o != id && len(d.Others) < 10 && !containsID(d.Others, o) {
					d.Others = append(d.Others, o)
				}
			}
		}
	}
	return out
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func containsID(xs []int64, v int64) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// seoPagePath matches the release and performer URLs of every language
// (/releases/x, /en/releases/x).
var seoPagePath = regexp.MustCompile(`^(?:/[A-Za-z-]+)?/(releases|performers)/([^/]+)/?$`)

// SEOPageFailures maps the "failures" of a seo.check report to entity type
// → slug → finding. URLs that are not release or performer pages (home,
// tags, series) are counted in other.
func SEOPageFailures(detail map[string]any) (out map[string]map[string]SEOFinding, other int) {
	out = map[string]map[string]SEOFinding{"release": {}, "performer": {}}
	list, _ := detail["failures"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		raw, _ := m["url"].(string)
		u, err := url.Parse(raw)
		if err != nil || raw == "" {
			other++
			continue
		}
		sm := seoPagePath.FindStringSubmatch(path.Clean(u.Path))
		if sm == nil {
			other++
			continue
		}
		slug, err := url.PathUnescape(sm[2])
		if err != nil {
			other++
			continue
		}
		var reasons []string
		rs, _ := m["reasons"].([]any)
		for _, r := range rs {
			if s, ok := r.(string); ok {
				reasons = append(reasons, s)
			}
		}
		out[strings.TrimSuffix(sm[1], "s")][slug] = SEOFinding{Check: SEOSSRFailed, Severity: SeverityHigh, URL: raw, Reasons: reasons}
	}
	return out, other
}

// SEOSiteFinding is one catalog-wide finding in the run detail.
type SEOSiteFinding struct {
	Check  string `json:"check"`
	Type   string `json:"type,omitempty"`
	Listed int64  `json:"listed,omitempty"`
	Want   int64  `json:"expected,omitempty"`
	Langs  []any  `json:"langs,omitempty"`
	Note   string `json:"note,omitempty"`
}

func num(v any) (int64, bool) {
	f, ok := v.(float64)
	if !ok {
		if i, ok := v.(int64); ok {
			return i, true
		}
		if i, ok := v.(int); ok {
			return int64(i), true
		}
		return 0, false
	}
	return int64(f), true
}

// SEOReportFindings checks one seo.check report detail for sitemap gaps.
func SEOReportFindings(detail map[string]any) []SEOSiteFinding {
	var out []SEOSiteFinding
	sm, _ := detail["sitemap"].(map[string]any)
	types := make([]string, 0, len(sm))
	for k := range sm {
		types = append(types, k)
	}
	sort.Strings(types)
	for _, typ := range types {
		m, _ := sm[typ].(map[string]any)
		listed, _ := num(m["listed"])
		want, ok := num(m["expected"])
		if ok && listed < want {
			out = append(out, SEOSiteFinding{Check: SEOSiteSitemapGap, Type: typ, Listed: listed, Want: want})
		}
	}
	if langs, _ := detail["sitemap_missing_langs"].([]any); len(langs) > 0 {
		out = append(out, SEOSiteFinding{Check: SEOSiteMissingLangs, Langs: langs})
	}
	if src, _ := detail["counts_source"].(string); src == "unavailable" {
		out = append(out, SEOSiteFinding{Check: SEOSiteCountsUnavailable, Note: "the check could not read /api/sitemap/counts, so coverage is unknown"})
	}
	return out
}

// ---------------------------------------------------------------- runner

func (r *Runner) ruleSEO(ctx context.Context, start time.Time) (ruleOutcome, error) {
	out := ruleOutcome{detail: map[string]any{}}
	site, err := r.Store.SEOSite(ctx)
	if err != nil {
		return out, err
	}
	out.detail["site"] = site
	in := seoInputs{coversOn: site.ReleasesWithCover > 0, failures: map[string]map[string]SEOFinding{}}

	// The latest seo.check report.
	var siteFindings []SEOSiteFinding
	report, ok, err := r.Store.LatestRun(ctx, SEOCheckJob)
	if err != nil {
		return out, err
	}
	switch {
	case !ok:
		out.detail["seo_check"] = map[string]any{"found": false}
		out.detail["note"] = "no seo.check report yet (npm run seo:check with ADMIN_TOKEN); page checks only"
	default:
		age := start.Sub(report.StartedAt)
		sc := map[string]any{"found": true, "started_at": report.StartedAt, "age_hours": int64(age.Hours()),
			"score": report.Detail["score"], "band": report.Detail["band"],
			"ssr_pass_rate": report.Detail["ssr_pass_rate"], "sitemap_coverage": report.Detail["sitemap_coverage"]}
		out.detail["seo_check"] = sc
		siteFindings = append(siteFindings, SEOReportFindings(report.Detail)...)
		if age > SEOReportMaxAge {
			siteFindings = append(siteFindings, SEOSiteFinding{Check: SEOSiteStaleReport,
				Note: "older than " + SEOReportMaxAge.String() + "; its page failures are not raised"})
		} else {
			failures, other := SEOPageFailures(report.Detail)
			in.failures = failures
			sc["page_failures_other"] = other
		}
	}
	if !ok {
		siteFindings = append(siteFindings, SEOSiteFinding{Check: SEOSiteNoReport})
	}

	groups, err := r.Store.SEODuplicateTitles(ctx)
	if err != nil {
		return out, err
	}
	in.dupes = duplicateFindings(groups)

	byCheck := map[string]int{}
	for _, typ := range []string{"release", "performer"} {
		var after int64
		for {
			page, err := r.Store.SEOEntities(ctx, typ, after, r.batch())
			if err != nil {
				return out, err
			}
			for _, f := range page {
				out.checked++
				after = f.ID
				fs := CheckSEO(f, in)
				if len(fs) == 0 {
					continue
				}
				res, err := r.Store.OpenIssue(ctx, SEOIssue(f, fs, start))
				if err != nil {
					return out, err
				}
				if res != IssueSuppressed {
					out.issues++
					for _, x := range fs {
						byCheck[x.Check]++
					}
				}
			}
			if len(page) < r.batch() {
				break
			}
		}
	}
	// A missing report is a note, not a problem with the catalog.
	for _, s := range siteFindings {
		if s.Check != SEOSiteNoReport {
			out.issues++
		}
	}
	resolved, err := r.Store.ResolveStale(ctx, RuleSEO, start)
	out.fixed = resolved
	out.detail["by_check"] = byCheck
	out.detail["site_findings"] = siteFindings
	out.detail["duplicate_title_groups"] = len(groups)
	out.detail["resolved_stale"] = resolved
	out.detail["auto_action"] = "none"
	return out, err
}
