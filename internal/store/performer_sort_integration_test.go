package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// TestPerformerWorkSortsIntegration checks that the works / newest_work
// sorts of GET /api/performers, which read performer_stats, return the same
// order (across keyset pages) as ordering by the live counts, including
// performers missing from performer_stats (live fallback), and that the
// live per-performer counts run only for the page's rows (EXPLAIN ANALYZE).
// Runs in its own database. Skipped unless AVXXX_TEST_DATABASE_URL is set.
func TestPerformerWorkSortsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "perfsort")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// 40 performers; performer i has i%7 published works (ties on purpose)
	// dated by i%5 (some undated), plus a draft and a merged release that
	// must not count.
	exec(`INSERT INTO performers(slug,canonical_name) SELECT 'p'||i,'P '||i FROM generate_series(1,40) i`)
	exec(`INSERT INTO releases(slug,canonical_title,status,release_date)
		SELECT 'r'||i||'-'||j,'R','published',CASE WHEN i%5=0 THEN NULL ELSE DATE '2020-01-01'+(i%5)*30+j END
		FROM generate_series(1,40) i, generate_series(1,i%7) j`)
	exec(`INSERT INTO release_performers(release_id,performer_id) SELECT r.id,p.id FROM releases r JOIN performers p ON r.slug LIKE p.slug||'-%'`)
	exec(`INSERT INTO releases(slug,canonical_title,status,release_date) VALUES('draft','D','draft','2030-01-01'),('gone','G','published','2031-01-01')`)
	exec(`INSERT INTO release_performers(release_id,performer_id) SELECT r.id,p.id FROM releases r, performers p WHERE r.slug IN ('draft','gone') AND p.slug IN ('p1','p2')`)
	exec(`UPDATE releases SET merged_into_id=(SELECT id FROM releases WHERE slug='r6-1') WHERE slug='gone'`)
	exec(`UPDATE performers SET status='hidden' WHERE slug='p3'`)
	if _, err = s.RefreshPerformerStats(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	live := map[string]string{
		"works":       "st.works DESC,p.id ASC",
		"newest_work": "COALESCE(st.newest,DATE '0001-01-01') DESC,p.id DESC",
	}
	reference := func(sort string) []string {
		t.Helper()
		rows, err := s.Query(ctx, `SELECT p.slug FROM performers p
			CROSS JOIN LATERAL (SELECT count(*) works,max(r.release_date) newest FROM release_performers rp
			 JOIN releases r ON r.id=rp.release_id WHERE rp.performer_id=p.id AND r.status='published' AND r.merged_into_id IS NULL) st
			WHERE p.status='active' AND p.merged_into_id IS NULL ORDER BY `+live[sort])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var slug string
			if err = rows.Scan(&slug); err != nil {
				t.Fatal(err)
			}
			out = append(out, slug)
		}
		return out
	}
	paged := func(sort string) []string {
		t.Helper()
		var out []string
		f := PerformerFilter{Sort: sort, Limit: 7}
		for range 20 {
			page, next, err := s.PerformerPage(ctx, f)
			if err != nil {
				t.Fatalf("%s: %v", sort, err)
			}
			for _, p := range page {
				out = append(out, p.Slug)
				var works int64
				if err = s.QueryRow(ctx, `SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
					JOIN performers p ON p.id=rp.performer_id WHERE p.slug=$1 AND r.status='published' AND r.merged_into_id IS NULL`, p.Slug).Scan(&works); err != nil {
					t.Fatal(err)
				}
				if p.WorksCount != works {
					t.Errorf("%s %s works_count = %d, want live %d", sort, p.Slug, p.WorksCount, works)
				}
			}
			if next == "" {
				return out
			}
			c, err := DecodeCursor(next)
			if err != nil {
				t.Fatal(err)
			}
			f.Cursor = &c
		}
		t.Fatalf("%s: paging did not end", sort)
		return nil
	}
	check := func(when string) {
		t.Helper()
		for sort := range live {
			want, got := reference(sort), paged(sort)
			if !slices.Equal(got, want) {
				t.Errorf("%s, sort %s:\n got %v\nwant %v", when, sort, got, want)
			}
		}
	}
	check("fresh stats")

	// Performers added since the last stats run (no performer_stats row)
	// sort by their live counts.
	exec(`INSERT INTO performers(slug,canonical_name) VALUES('new1','New 1'),('new2','New 2')`)
	exec(`INSERT INTO releases(slug,canonical_title,status,release_date) SELECT 'new1-'||j,'N','published',DATE '2029-01-01'+j FROM generate_series(1,9) j`)
	exec(`INSERT INTO release_performers(release_id,performer_id) SELECT r.id,p.id FROM releases r JOIN performers p ON r.slug LIKE p.slug||'-%' WHERE p.slug='new1'`)
	check("performers missing from performer_stats")
	if got := paged("works"); len(got) < 2 || got[0] != "new1" {
		t.Errorf("new1 (9 live works, no stats row) should lead works: %v", got)
	}

	// EXPLAIN ANALYZE: once every performer has a stats row, the live
	// counts run once per page row (limit+1), not once per performer.
	if _, err = s.RefreshPerformerStats(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for sort := range live {
		sql, args, limit, err := buildPerformerQuery(PerformerFilter{Sort: sort, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		var plan []map[string]any
		if err = s.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		loops := workCountLoops(plan[0]["Plan"].(map[string]any))
		if loops == 0 || loops > float64(limit+1) {
			b, _ := json.MarshalIndent(plan, "", " ")
			t.Errorf("%s: work counts ran %v times, want 1..%d\n%s", sort, loops, limit+1, b)
		}
	}
}

// workCountLoops sums the actual loops of the outermost Aggregate nodes
// that read release_performers: one loop is one performer's work count
// (the page lateral, or a fallback subplan, which never runs once every
// performer has a stats row).
func workCountLoops(n map[string]any) float64 {
	if n["Node Type"] == "Aggregate" && readsReleasePerformers(n) {
		return n["Actual Loops"].(float64)
	}
	var sum float64
	for _, k := range planChildren(n) {
		sum += workCountLoops(k)
	}
	return sum
}

func readsReleasePerformers(n map[string]any) bool {
	if n["Relation Name"] == "release_performers" {
		return true
	}
	return slices.ContainsFunc(planChildren(n), readsReleasePerformers)
}

func planChildren(n map[string]any) []map[string]any {
	kids, _ := n["Plans"].([]any)
	out := make([]map[string]any, 0, len(kids))
	for _, k := range kids {
		out = append(out, k.(map[string]any))
	}
	return out
}
