package store

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestPerformerWorkSortsIntegration checks that the works / newest_work
// sorts of GET /api/performers, which read performer_stats, return the same
// order (across keyset pages) as ordering by the live counts, including
// performers missing from performer_stats (live fallback: new, reactivated
// and unmerged ones), that performer_stats_pending always lists those, and
// that under EXPLAIN ANALYZE the live per-performer counts run only for the
// page's rows and the fallback branch reads only the pending performers.
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
	// pm is merged away (with works) before the first stats run.
	exec(`INSERT INTO performers(slug,canonical_name) VALUES('pm','PM')`)
	exec(`INSERT INTO releases(slug,canonical_title,status,release_date) SELECT 'pm-'||j,'M','published',DATE '2028-01-01'+j FROM generate_series(1,4) j`)
	exec(`INSERT INTO release_performers(release_id,performer_id) SELECT r.id,p.id FROM releases r JOIN performers p ON r.slug LIKE p.slug||'-%' WHERE p.slug='pm'`)
	exec(`UPDATE performers SET status='merged',merged_into_id=(SELECT id FROM performers WHERE slug='p1') WHERE slug='pm'`)
	if _, err = s.RefreshPerformerStats(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// pending lists the slugs in performer_stats_pending, and checks the
	// invariant the missing-stats branch relies on: every listed performer
	// without a stats row is pending.
	pending := func() []string {
		t.Helper()
		var orphans int
		if err := s.QueryRow(ctx, `SELECT count(*) FROM performers p WHERE p.status='active' AND p.merged_into_id IS NULL
			AND NOT EXISTS (SELECT 1 FROM performer_stats ps WHERE ps.performer_id=p.id)
			AND NOT EXISTS (SELECT 1 FROM performer_stats_pending q WHERE q.performer_id=p.id)`).Scan(&orphans); err != nil {
			t.Fatal(err)
		}
		if orphans != 0 {
			t.Errorf("%d listed performers without stats are not in performer_stats_pending", orphans)
		}
		rows, err := s.Query(ctx, `SELECT p.slug FROM performer_stats_pending q JOIN performers p ON p.id=q.performer_id ORDER BY 1`)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The run cleared every performer with a stats row, and p3 / pm, which
	// are not listed (the trigger marked both when they were inserted).
	if got := pending(); len(got) != 0 {
		t.Errorf("pending after the stats run: %v, want none", got)
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
	// Reactivated and unmerged performers have no stats row either.
	exec(`UPDATE performers SET status='active' WHERE slug='p3'`)
	exec(`UPDATE performers SET status='active',merged_into_id=NULL WHERE slug='pm'`)
	// Updates of performers that are already listed mark nothing.
	exec(`UPDATE performers SET status='active',canonical_name=canonical_name||'' WHERE slug IN ('p5','p6')`)
	if got, want := pending(), []string{"new1", "new2", "p3", "pm"}; !slices.Equal(got, want) {
		t.Errorf("pending = %v, want %v", got, want)
	}
	check("performers missing from performer_stats")
	if got := paged("works"); len(got) < 2 || got[0] != "new1" {
		t.Errorf("new1 (9 live works, no stats row) should lead works: %v", got)
	}
	// A performer hidden again before the next run stays pending (it is
	// filtered out by status) until that run drops it.
	exec(`UPDATE performers SET status='hidden' WHERE slug='new2'`)
	check("a pending performer hidden again")
	exec(`UPDATE performers SET status='active' WHERE slug='new2'`)

	// 5000 more performers, so the planner sees a real performers table: the
	// missing-stats branch must start from performer_stats_pending, not
	// anti-join every performer against performer_stats.
	exec(`INSERT INTO performers(slug,canonical_name) SELECT 'bulk'||i,'Bulk '||i FROM generate_series(1,5000) i`)

	// EXPLAIN ANALYZE: once every performer has a stats row, the live
	// counts run once per page row (limit+1), not once per performer.
	if _, err = s.RefreshPerformerStats(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if got := pending(); len(got) != 0 {
		t.Errorf("pending after the second stats run: %v, want none", got)
	}
	// One performer added since: the only pending row, leading works.
	exec(`INSERT INTO performers(slug,canonical_name) VALUES('late','Late')`)
	exec(`INSERT INTO releases(slug,canonical_title,status,release_date) SELECT 'late-'||j,'L','published',DATE '2030-01-01'+j FROM generate_series(1,12) j`)
	exec(`INSERT INTO release_performers(release_id,performer_id) SELECT r.id,p.id FROM releases r JOIN performers p ON r.slug LIKE p.slug||'-%' WHERE p.slug='late'`)
	exec(`ANALYZE performers`)
	exec(`ANALYZE performer_stats`)
	exec(`ANALYZE performer_stats_pending`)
	for sort := range live {
		page, _, err := s.PerformerPage(ctx, PerformerFilter{Sort: sort, Limit: 3})
		if err != nil || len(page) == 0 || page[0].Slug != "late" {
			t.Errorf("%s: late (12 live works from 2030, no stats row) should lead: %v %v", sort, page, err)
		}
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
		// limit+1 page rows, plus the live sort key of the one pending
		// performer.
		loops := workCountLoops(plan[0]["Plan"].(map[string]any))
		if loops == 0 || loops > float64(limit+2) {
			b, _ := json.MarshalIndent(plan, "", " ")
			t.Errorf("%s: work counts ran %v times, want 1..%d\n%s", sort, loops, limit+2, b)
		}
		// The missing-stats branch starts from performer_stats_pending and
		// reads only its performers, not all 5000. (The stats branch is the
		// planner's choice: at this size a hash join, at 50k the
		// performer_stats_works index.)
		branch := missingBranch(plan[0]["Plan"].(map[string]any))
		if branch == nil {
			b, _ := json.MarshalIndent(plan, "", " ")
			t.Fatalf("%s: no branch reads performer_stats_pending\n%s", sort, b)
		}
		if n := planRelationRows(branch, "performers"); n > 2 {
			b, _ := json.MarshalIndent(branch, "", " ")
			t.Errorf("%s: the missing-stats branch read %v performers rows, want at most 2 (the pending one)\n%s", sort, n, b)
		}
	}
}

// missingBranch is the child of the UNION ALL's (Merge) Append that reads
// performer_stats_pending.
func missingBranch(n map[string]any) map[string]any {
	if t := n["Node Type"]; t == "Append" || t == "Merge Append" {
		for _, k := range planChildren(n) {
			if planRelationReads(k, "performer_stats_pending") {
				return k
			}
		}
	}
	for _, k := range planChildren(n) {
		if b := missingBranch(k); b != nil {
			return b
		}
	}
	return nil
}

func planRelationReads(n map[string]any, rel string) bool {
	return n["Relation Name"] == rel || slices.ContainsFunc(planChildren(n), func(k map[string]any) bool { return planRelationReads(k, rel) })
}

// planRelationRows sums the rows the scans of rel returned (actual rows times
// loops).
func planRelationRows(n map[string]any, rel string) float64 {
	var sum float64
	if n["Relation Name"] == rel {
		sum += n["Actual Rows"].(float64) * n["Actual Loops"].(float64)
	}
	for _, k := range planChildren(n) {
		sum += planRelationRows(k, rel)
	}
	return sum
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
