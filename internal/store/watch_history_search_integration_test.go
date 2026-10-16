package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWatchHistorySearchIntegration covers GET /api/me/watch-history?q=
// (store.WatchHistory with q): title, Japanese title, performer name and
// product code (prefix and code_match_key) matches, only the user's own rows,
// newest first. Skipped unless AVXXX_TEST_DATABASE_URL is set.
func TestWatchHistorySearchIntegration(t *testing.T) {
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
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var uid, other, perf int64
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "hs"+sfx+"@t").Scan(&uid)
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "ho"+sfx+"@t").Scan(&other)
	s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,canonical_name_ja) VALUES($1,'Hana Aoi','葵はな') RETURNING id`, "hana-"+sfx).Scan(&perf)

	// Codes unique per run (the identifier table is shared across tests),
	// letters only so the code keeps a "<letters>-<number>" shape.
	prefix := "hs" + strings.Map(func(r rune) rune { return 'a' + r - '0' }, sfx[len(sfx)-8:])
	newRelease := func(slug, title, titleJa, code string, withPerformer bool, watchedAgo time.Duration) {
		t.Helper()
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,title_ja,status) VALUES($1,$2,$3,'published') RETURNING id`, slug, title, titleJa).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if code != "" {
			mustExec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'dvd_code',upper($2),$2)`, id, code)
		}
		if withPerformer {
			mustExec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,0,true)`, id, perf)
		}
		mustExec(`INSERT INTO watch_history(user_id,release_id,progress_seconds,duration_seconds,watched_at) VALUES($1,$2,60,600,now()-$3::interval)`, uid, id, fmt.Sprintf("%d seconds", int(watchedAgo.Seconds())))
	}
	a, b, c := "hsa-"+sfx, "hsb-"+sfx, "hsc-"+sfx
	newRelease(a, "Summer Beach Story", "夏の海物語", prefix+"-00123", false, 3*time.Hour)
	newRelease(b, "Office Night", "", prefix+"-456", true, 2*time.Hour)
	newRelease(c, "Beach Volley 100%_off", "", "", true, time.Hour)
	// Another user's row never shows.
	var foreign int64
	s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, a).Scan(&foreign)
	mustExec(`INSERT INTO watch_history(user_id,release_id,progress_seconds) VALUES($1,$2,5)`, other, foreign)

	slugs := func(q string) []string {
		t.Helper()
		items, err := s.WatchHistory(ctx, uid, q)
		if err != nil {
			t.Fatalf("WatchHistory(%q): %v", q, err)
		}
		out := []string{}
		for _, it := range items {
			out = append(out, it["slug"].(string))
		}
		return out
	}
	eq := func(q string, want ...string) {
		t.Helper()
		got := slugs(q)
		if fmt.Sprint(got) != fmt.Sprint(append([]string{}, want...)) {
			t.Errorf("q=%q: got %v, want %v", q, got, want)
		}
	}

	eq("", c, b, a)                       // no q: everything, newest first
	eq("beach", c, a)                     // title, case-insensitive
	eq("  BEACH  ", c, a)                 // normalized query
	eq("海物語", a)                          // Japanese title
	eq("hana", c, b)                      // performer name
	eq("葵", c, b)                         // performer Japanese name
	eq(prefix+"-001", a)                  // code prefix
	eq(prefix+"123", a)                   // code_match_key: no dash, no zero padding
	eq(strings.ToUpper(prefix)+"-456", b) // code, upper case
	eq("100%_", c)                        // LIKE metacharacters are literal
	eq("100%x")                           // ... so this matches nothing
	eq("nothing-like-this")

	if items, err := s.WatchHistory(ctx, other, "beach"); err != nil || len(items) != 1 {
		t.Errorf("other user: %v %v", items, err)
	}
}
