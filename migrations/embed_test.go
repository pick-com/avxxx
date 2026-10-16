package migrations

import (
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// The glob-based Schema must equal the explicit 001…014 concatenation it
// replaced, byte for byte.
func TestSchemaMatchesExplicitOrder(t *testing.T) {
	names := []string{
		"001_init.sql", "002_catalog.sql", "003_media_assets.sql", "004_auth.sql",
		"005_watch_history.sql", "006_favorites.sql", "007_auth_hardening.sql",
		"008_playback_sources.sql", "009_crawl_budget.sql", "010_events.sql",
		"011_catalog_indexes.sql", "012_identity_merge.sql", "013_audit.sql", "014_social.sql",
	}
	parts := make([]string, len(names))
	for i, n := range names {
		b, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		parts[i] = string(b)
	}
	want := strings.Join(parts, "\n")
	// Later migrations are appended after 014; the first 14 files must keep
	// their position and content.
	if !strings.HasPrefix(Schema, want) {
		t.Fatal("Schema does not start with the explicit 001…014 concatenation")
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	sqlFiles := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			sqlFiles++
		}
	}
	if sqlFiles == len(names) && Schema != want {
		t.Error("Schema differs from the explicit 001…014 concatenation")
	}
}

func TestJoinSortsByFilename(t *testing.T) {
	got, err := join(fstest.MapFS{
		"010_c.sql": {Data: []byte("C")},
		"002_b.sql": {Data: []byte("B")},
		"001_a.sql": {Data: []byte("A")},
		"README.md": {Data: []byte("ignored")},
	})
	if err != nil || got != "A\nB\nC" {
		t.Errorf("join = %q %v, want %q", got, err, "A\nB\nC")
	}
}
