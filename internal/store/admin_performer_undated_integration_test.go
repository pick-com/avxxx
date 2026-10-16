package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestAdminPerformerUndatedReleaseIntegration is the regression test for
// GET /api/admin/performers/{id} failing with "cannot scan NULL into *bool":
// a performer credited on a published or draft release with no release_date
// made upcomingReleaseSQL evaluate to NULL in performerWorks. Skipped unless
// AVXXX_TEST_DATABASE_URL is set (make test-integration).
func TestAdminPerformerUndatedReleaseIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sfx := fmt.Sprint(time.Now().UnixNano())
	var perfID int64
	if err := s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$2) RETURNING id`, "undated-"+sfx, "Undated "+sfx).Scan(&perfID); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, c := range []struct {
		status   string
		date     *string
		upcoming bool
	}{
		{"published", nil, false},
		{"draft", nil, false},
		{"upcoming", nil, true},
		{"published", ptr("2999-01-01"), true},
	} {
		slug := fmt.Sprintf("undated-%s-%s-%v", c.status, sfx, c.date != nil)
		var relID int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$1,$2,$3::date) RETURNING id`,
			slug, c.status, c.date).Scan(&relID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Exec(ctx, `INSERT INTO release_performers(release_id,performer_id,billing_order) VALUES($1,$2,1)`, relID, perfID); err != nil {
			t.Fatal(err)
		}
		want[slug] = c.upcoming
	}

	p, err := s.AdminPerformer(ctx, perfID)
	if err != nil {
		t.Fatalf("AdminPerformer with undated releases: %v", err)
	}
	if len(p.Works) != len(want) {
		t.Fatalf("works = %d, want %d", len(p.Works), len(want))
	}
	for _, w := range p.Works {
		if w.Upcoming != want[w.Slug] {
			t.Errorf("%s: upcoming = %v, want %v", w.Slug, w.Upcoming, want[w.Slug])
		}
	}
	if len(p.Upcoming) != 2 {
		t.Errorf("upcoming = %d, want 2", len(p.Upcoming))
	}

	list, _, err := s.AdminPerformers(ctx, AdminPerformerFilter{Q: "Undated " + sfx, Filters: []string{PerformerFilterUpcoming}})
	if err != nil {
		t.Fatalf("AdminPerformers upcoming filter: %v", err)
	}
	if len(list) != 1 || list[0].ID != perfID {
		t.Errorf("upcoming filter = %+v, want performer %d", list, perfID)
	}
}
