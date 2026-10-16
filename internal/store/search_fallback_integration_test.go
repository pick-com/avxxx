package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestSearchILIKEFallbackIntegration runs the search queries with pg_trgm
// treated as missing, the path used when the extension cannot be created.
func TestSearchILIKEFallbackIntegration(t *testing.T) {
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
	trgmByPool.Store(s.Pool, false)
	defer trgmByPool.Delete(s.Pool)

	sfx := fmt.Sprint(time.Now().UnixNano() % 1_000_000_000)
	title := "Fallback Title " + sfx
	var id int64
	if err = s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,$2,'published') RETURNING id`, "fallback-"+sfx, title).Scan(&id); err != nil {
		t.Fatal(err)
	}
	res, err := s.Search(ctx, "fallback title "+sfx+" 1080p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Rescued || len(res.Items) != 1 || res.Items[0]["id"] != id {
		t.Errorf("fallback search: %+v", res)
	}
	sug, err := s.Suggest(ctx, "fallback title "+sfx, 8)
	if err != nil || len(sug) != 1 {
		t.Errorf("fallback suggest: %v %v", sug, err)
	}
	nm, err := s.NearMatches(ctx, "other "+sfx, 3)
	if err != nil || len(nm) != 1 || nm[0].ID != id {
		t.Errorf("fallback near matches: %+v %v", nm, err)
	}
	// LIKE metacharacters in the query are literal.
	if res, err = s.Search(ctx, "%_%", 10); err != nil || len(res.Items) != 0 {
		t.Errorf("metacharacters: %d items, %v", len(res.Items), err)
	}
}
