package audit

// R-PERFORMER-NAME: performers whose name looks like a crawled site title or
// page boilerplate ("JAV Japanese Porn Actress & Idol Database", a URL, a
// tagline, the source's own <title>). The rule only opens an issue; it never
// deletes, hides or renames anything. An admin hides the performer
// (POST /api/admin/performers/{id}/status) or merges it into the real one
// (identity review); a hidden or merged performer is no longer checked, so
// the next run resolves the issue.

import (
	srccatalog "avxxx/internal/catalog"
	"context"
	"sort"
	"time"
)

// RulePerformerName is the junk-performer-name rule.
const RulePerformerName Rule = "R-PERFORMER-NAME"

// PerformerNameFacts is what R-PERFORMER-NAME needs about one performer.
type PerformerNameFacts struct {
	ID     int64
	Name   string
	NameJA string
	// PageTitles are the <title> texts of the source pages the performer was
	// parsed from (source_records.raw_data.page_title).
	PageTitles []string
	// SiteTitles are page titles the same source also used on other
	// performers' pages: the site's own title, whatever its shape.
	SiteTitles []string
	// Releases counts published releases crediting the performer.
	Releases int
}

// NameFinding is one name field that looks like boilerplate.
type NameFinding struct {
	Field  string `json:"field"` // canonical_name | canonical_name_ja
	Value  string `json:"value"`
	Reason string `json:"reason"` // a srccatalog.Name* reason
}

// CheckPerformerName returns the name fields of p that look like a site
// title or page boilerplate (see srccatalog.BoilerplateName).
func CheckPerformerName(p PerformerNameFacts) []NameFinding {
	var out []NameFinding
	for _, f := range []struct{ field, value string }{{"canonical_name", p.Name}, {"canonical_name_ja", p.NameJA}} {
		reason := srccatalog.BoilerplateName(f.value, p.PageTitles...)
		for _, t := range p.SiteTitles {
			if reason == "" && srccatalog.SameName(f.value, t) {
				reason = srccatalog.NameMatchesSiteTitle
			}
		}
		if reason != "" {
			out = append(out, NameFinding{Field: f.field, Value: f.value, Reason: reason})
		}
	}
	return out
}

// PerformerNameIssue builds the issue for findings on p. Severity is high
// when published releases credit the performer (the junk name is visible),
// medium otherwise.
func PerformerNameIssue(p PerformerNameFacts, fs []NameFinding, seen time.Time) Issue {
	sev := SeverityMedium
	if p.Releases > 0 {
		sev = SeverityHigh
	}
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, f.Field+":"+f.Value+":"+f.Reason)
	}
	sort.Strings(parts)
	return Issue{Rule: RulePerformerName, EntityType: "performer", EntityID: p.ID, Severity: sev,
		Detail: map[string]any{"name": p.Name, "findings": fs, "published_releases": p.Releases,
			"suggested_actions": []string{"hide", "merge"}, "auto_action": "none"},
		Fingerprint: Fingerprint(parts...), Priority: int64(p.Releases), LastSeenAt: seen}
}

func (r *Runner) rulePerformerName(ctx context.Context, start time.Time) (ruleOutcome, error) {
	out := ruleOutcome{detail: map[string]any{}}
	byReason := map[string]int{}
	var after int64
	for {
		page, err := r.Store.PerformerNames(ctx, after, r.batch())
		if err != nil {
			return out, err
		}
		for _, p := range page {
			out.checked++
			after = p.ID
			fs := CheckPerformerName(p)
			if len(fs) == 0 {
				continue
			}
			res, err := r.Store.OpenIssue(ctx, PerformerNameIssue(p, fs, start))
			if err != nil {
				return out, err
			}
			if res != IssueSuppressed {
				out.issues++
				for _, f := range fs {
					byReason[f.Reason]++
				}
			}
		}
		if len(page) < r.batch() {
			break
		}
	}
	resolved, err := r.Store.ResolveStale(ctx, RulePerformerName, start)
	out.fixed = resolved
	out.detail["by_reason"] = byReason
	out.detail["resolved_stale"] = resolved
	return out, err
}
