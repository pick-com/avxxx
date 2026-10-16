package catalog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFixtureAcceptance runs every adapter over its recorded fixtures. It
// fails on any unparsable fixture; the 20/100 target itself is reported by
// cmd/fixturecheck (-strict to enforce), not asserted here, because the
// recorded corpus is still being collected.
func TestFixtureAcceptance(t *testing.T) {
	for _, a := range Adapters() {
		res, err := RunFixtures(context.Background(), a, "testdata")
		if err != nil {
			t.Fatalf("%s: %v", a.Name(), err)
		}
		if len(res.Report.Failures) > 0 || res.DiscoveryFailed > 0 {
			t.Errorf("%s: failures %v", a.Name(), res.Report.Failures)
		}
		if res.Synthetic["discovery"]+res.Recorded["discovery"] == 0 || res.Discovered == 0 {
			t.Errorf("%s: no discovery fixtures parsed: %+v", a.Name(), res)
		}
		t.Logf("\n%s", FormatFixtureResults([]FixtureResult{res}))
	}
}

func TestFixtureReportFields(t *testing.T) {
	res, err := RunFixtures(context.Background(), NewJAVDatabaseAdapter(), "testdata")
	if err != nil {
		t.Fatal(err)
	}
	rel := res.Report.Entities[Release]
	if rel.Parsed != 1 || rel.Complete != 1 || rel.RequiredPct(Release) != 100 || rel.NormalizedPct() != 100 {
		t.Fatalf("release report %+v", rel)
	}
	perf := res.Report.Entities[Performer]
	// Two performer samples: sena-hashimoto has no JP name (3 of 4 required
	// fields), the site-title regression page has all 4. The logo-only page
	// is an expected ErrSkipRecord, not a parse.
	if perf.Parsed != 2 || perf.Complete != 1 || perf.RequiredPct(Performer) != 87.5 || perf.Present["name_ja"] != 1 || res.Skipped != 1 {
		t.Fatalf("performer report %+v skipped %d", perf, res.Skipped)
	}
	if res.MeetsTarget() || res.Recorded["release"] != 0 {
		t.Fatal("synthetic fixtures must not count toward the target")
	}
}

func TestFixtureManifestValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if m, err := LoadFixtureManifest(filepath.Join(dir, "missing"), "x"); err != nil || len(m.Fixtures) != 0 {
		t.Fatalf("missing manifest: %v", err)
	}
	for _, bad := range []string{
		`{"fixtures":[{"file":"../../etc/passwd","url":"https://x/","entity":"release","origin":"recorded"}]}`,
		`{"fixtures":[{"file":"a.html","url":"https://x/","entity":"release","origin":"live"}]}`,
		`{"fixtures":[{"file":"a.html","url":"","entity":"release","origin":"recorded"}]}`,
		`{"fixtures":[{"file":"a.html","url":"https://x/","entity":"studio","origin":"recorded"}]}`,
		`{"fixtures":[{"file":"a.html","url":"https://x/","entity":"release","origin":"recorded","expect":"fail"}]}`,
		`{"fixtures":[{"file":"a.html","url":"https://x/","entity":"discovery","origin":"recorded","expect":"skip"}]}`,
	} {
		write(bad)
		if _, err := LoadFixtureManifest(dir, "x"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if s := FormatFixtureResults([]FixtureResult{{Source: "x", Recorded: map[string]int{}, Synthetic: map[string]int{}, Report: NewParseReport("x", 1)}}); !strings.Contains(s, "NOT MET") {
		t.Fatalf("format %q", s)
	}
}

func TestParseReportNormalization(t *testing.T) {
	r := NewParseReport("x", 2)
	r.Add(Record{EntityType: Release, SourceKey: "a", Fields: map[string]any{"title": "T", "dvd_code": "ABC-123", "release_date": "2024-01-02", "maker": "M", "performers": []string{"P"}}})
	r.Add(Record{EntityType: Release, SourceKey: "b", Fields: map[string]any{"title": "T", "dvd_code": "ABC123", "release_date": "2024/01/02", "content_id": "abc00123"}})
	e := r.Entities[Release]
	if e.Parsed != 2 || e.Complete != 1 || e.Checked != 5 || e.Normalized != 3 {
		t.Fatalf("report %+v", e)
	}
	if sum := r.Summary(); sum["dry_run"] != true || sum["release"] == nil {
		t.Fatalf("summary %v", sum)
	}
}
