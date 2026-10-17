package store

// Indexes of migration 058 (docs/PERFORMANCE.md): each test checks that the
// query it serves can be answered from the index without a Sort, by
// planning it with sorts and sequential / bitmap scans disabled. A plan
// that still sorts means the index no longer matches the query's ORDER BY.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// openTestStore opens a store on a fresh database (skips without
// AVXXX_TEST_DATABASE_URL).
func openTestStore(t *testing.T) *Store {
	t.Helper()
	openIsolatedTestDB(t, "fullscale")
	s, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// planWithoutSort returns the JSON plan of sql under settings that make a
// Sort or a full scan as unattractive as possible.
func planWithoutSort(t *testing.T, s *Store, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	var plan string
	err := pgx.BeginFunc(ctx, s, func(tx pgx.Tx) error {
		for _, set := range []string{"enable_sort", "enable_seqscan", "enable_bitmapscan", "enable_incremental_sort"} {
			if _, err := tx.Exec(ctx, "SET LOCAL "+set+" = off"); err != nil {
				return err
			}
		}
		var raw json.RawMessage
		if err := tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
			return err
		}
		plan = string(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestCommentsTopUsesIndexIntegration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	var rel, user int64
	if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,'Comments top','published') RETURNING id`, "ctop-"+sfx).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	if err := s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "ctop-"+sfx+"@example.test").Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO comments(release_id,user_id,body,likes,status,created_at)
		SELECT $1,$2,'c'||g,g%7,CASE WHEN g%11=0 THEN 'deleted' ELSE 'visible' END,now()-g*interval '1 minute' FROM generate_series(1,60) g`, rel, user); err != nil {
		t.Fatal(err)
	}

	where := `c.release_id=$2 AND c.parent_id IS NULL AND ` + commentListed
	plan := planWithoutSort(t, s, commentListSQL(where, commentTopOrder, 4), user, rel, 21, 0)
	if !strings.Contains(plan, `"comments_top"`) || strings.Contains(plan, `"Sort"`) {
		t.Errorf("top comments should read comments_top without a Sort:\n%s", plan)
	}

	// The page itself: likes desc (deleted count as 0), newest first on ties.
	page, _, err := s.Comments(ctx, CommentFilter{ReleaseID: rel, Sort: "top", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(page); i++ {
		a, b := page[i-1], page[i]
		if a.Likes < b.Likes || (a.Likes == b.Likes && a.CreatedAt.Before(b.CreatedAt)) {
			t.Fatalf("not in top order at %d: %+v then %+v", i, a, b)
		}
	}
	if len(page) != 10 {
		t.Errorf("page of %d", len(page))
	}
}
