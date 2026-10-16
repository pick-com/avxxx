package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Fixture acceptance (FINAL_DATA_DESIGN 10.6, ROADMAP "每个来源 20 个演员、
// 100 个作品 fixture 验收"): every adapter's parser runs over pages saved under
// testdata/<source>/, listed in testdata/<source>/manifest.json. Fixtures are
// never fetched by tests or by the harness.
//
// origin "recorded" is a page an operator saved from the live site with its
// URL; "synthetic" is hand-written markup that mirrors the site's structure.
// Only recorded fixtures count toward the acceptance target.

// Acceptance targets per source.
const (
	FixtureTargetPerformers = 20
	FixtureTargetReleases   = 100
)

// FixtureEntry is one manifest row.
type FixtureEntry struct {
	File       string `json:"file"`
	URL        string `json:"url"`
	Entity     string `json:"entity"` // performer | release | discovery
	Origin     string `json:"origin"` // recorded | synthetic
	CapturedAt string `json:"captured_at,omitempty"`
}

// FixtureManifest is testdata/<source>/manifest.json.
type FixtureManifest struct {
	Source   string         `json:"source"`
	Fixtures []FixtureEntry `json:"fixtures"`
}

// FixtureResult is the acceptance outcome for one source.
type FixtureResult struct {
	Source string `json:"source"`
	// Counts of fixtures by entity, split by origin.
	Recorded  map[string]int `json:"recorded"`
	Synthetic map[string]int `json:"synthetic"`
	// Discovery fixtures: records found and pages that failed.
	Discovered      int          `json:"discovered"`
	DiscoveryFailed int          `json:"discovery_failed"`
	Report          *ParseReport `json:"report"`
}

// MeetsTarget reports whether the recorded fixtures reach the target and
// every recorded detail fixture parsed.
func (r FixtureResult) MeetsTarget() bool {
	return r.Recorded["performer"] >= FixtureTargetPerformers && r.Recorded["release"] >= FixtureTargetReleases &&
		r.Report.Entities[Performer].Failed == 0 && r.Report.Entities[Release].Failed == 0
}

// LoadFixtureManifest reads dir/manifest.json. A missing manifest is an
// empty manifest (the source has no fixtures yet).
func LoadFixtureManifest(dir, source string) (FixtureManifest, error) {
	m := FixtureManifest{Source: source}
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s/manifest.json: %w", source, err)
	}
	for _, f := range m.Fixtures {
		if f.Origin != "recorded" && f.Origin != "synthetic" {
			return m, fmt.Errorf("%s: %s: origin must be recorded or synthetic", source, f.File)
		}
		if f.Entity != "performer" && f.Entity != "release" && f.Entity != "discovery" {
			return m, fmt.Errorf("%s: %s: entity must be performer, release or discovery", source, f.File)
		}
		if !filepath.IsLocal(f.File) {
			return m, fmt.Errorf("%s: %s: fixture path must stay inside the source directory", source, f.File)
		}
		if f.URL == "" {
			return m, fmt.Errorf("%s: %s: url is required (adapters classify pages by URL)", source, f.File)
		}
	}
	return m, nil
}

// RunFixtures parses every fixture of one adapter from root/<adapter name>/.
func RunFixtures(ctx context.Context, a Adapter, root string) (FixtureResult, error) {
	name := a.Name()
	dir := filepath.Join(root, name)
	res := FixtureResult{Source: name, Recorded: map[string]int{}, Synthetic: map[string]int{}, Report: NewParseReport(name, 5)}
	m, err := LoadFixtureManifest(dir, name)
	if err != nil {
		return res, err
	}
	for _, f := range m.Fixtures {
		if f.Origin == "recorded" {
			res.Recorded[f.Entity]++
		} else {
			res.Synthetic[f.Entity]++
		}
		body, err := os.ReadFile(filepath.Join(dir, f.File))
		if err != nil {
			return res, fmt.Errorf("%s: %w", name, err)
		}
		doc := Document{URL: f.URL, FinalURL: f.URL, Status: 200, ContentType: "text/html", Body: body}
		if f.Entity == "discovery" {
			recs, err := a.Discover(ctx, doc)
			if err != nil {
				res.DiscoveryFailed++
				res.Report.Failures = append(res.Report.Failures, fmt.Sprintf("discover %s: %v", f.File, err))
				continue
			}
			res.Discovered += len(recs)
			continue
		}
		entity := EntityType(f.Entity)
		rec, err := a.Parse(ctx, doc, entity)
		if err != nil {
			res.Report.Fail(entity, f.File, err)
			continue
		}
		res.Report.Add(rec)
	}
	return res, nil
}

// FormatFixtureResults renders a plain-text acceptance table.
func FormatFixtureResults(results []FixtureResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-12s %-17s %-17s %-10s %-22s %-22s %s\n", "source", "performers rec/syn", "releases rec/syn", "discovered", "performer req%/norm%", "release req%/norm%", "target")
	for _, r := range results {
		p, rel := r.Report.Entities[Performer], r.Report.Entities[Release]
		status := "MET"
		if !r.MeetsTarget() {
			status = fmt.Sprintf("NOT MET (need %d/%d recorded)", FixtureTargetPerformers, FixtureTargetReleases)
		}
		fmt.Fprintf(&b, "%-12s %-17s %-17s %-10d %-22s %-22s %s\n", r.Source,
			fmt.Sprintf("%d/%d", r.Recorded["performer"], r.Synthetic["performer"]),
			fmt.Sprintf("%d/%d", r.Recorded["release"], r.Synthetic["release"]),
			r.Discovered,
			fmt.Sprintf("%.1f/%.1f (%d ok,%d fail)", p.RequiredPct(Performer), p.NormalizedPct(), p.Parsed, p.Failed),
			fmt.Sprintf("%.1f/%.1f (%d ok,%d fail)", rel.RequiredPct(Release), rel.NormalizedPct(), rel.Parsed, rel.Failed),
			status)
		for _, f := range r.Report.Failures {
			fmt.Fprintf(&b, "    failure: %s\n", f)
		}
	}
	return b.String()
}
