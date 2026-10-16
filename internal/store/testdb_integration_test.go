package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// openIsolatedTestDB creates a fresh database next to AVXXX_TEST_DATABASE_URL,
// points DATABASE_URL at it for the test, and drops it when the test ends.
// Tests that assert on seeded rows or global state (sources seeded by
// migrations, external ids, run logs) use it so they do not depend on what
// other integration tests left behind in the shared database. Skips when
// AVXXX_TEST_DATABASE_URL is not set.
func openIsolatedTestDB(t *testing.T, prefix string) {
	t.Helper()
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("avxxx_%s_%d", prefix, time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	t.Setenv("DATABASE_URL", u.String())
}

// OpenIsolatedTestDB is openIsolatedTestDB for the external store_test
// package (search_integration_test.go).
func OpenIsolatedTestDB(t *testing.T, prefix string) { openIsolatedTestDB(t, prefix) }
