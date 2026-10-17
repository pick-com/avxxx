package store

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// Tags lists are cached per limit for tagsTTL (the home page asks on every
// server render): same rows as the query, new counts only after the TTL.
func TestTagsCachedIntegration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO releases(slug,canonical_title,status) SELECT $1||g,'Tagged '||g,'published' FROM generate_series(1,12) g`, "tg-"+sfx+"-")
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT r.id,'Tag'||k,'tg-'||k FROM releases r, generate_series(1,4) k
		WHERE r.slug LIKE $1 AND (r.id%4)>=k-1`, "tg-"+sfx+"-%")

	got, err := s.Tags(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.tagsNow(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || len(got) != 3 {
		t.Fatalf("Tags = %v, query %v", got, want)
	}
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT id,'Tag9','tg-9' FROM releases WHERE slug LIKE $1`, "tg-"+sfx+"-%")
	if again, err := s.Tags(ctx, 3); err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("within the TTL: %v %v, want cached %v", again, err, got)
	}
	if other, err := s.Tags(ctx, 5); err != nil || len(other) != 5 || other[1]["slug"] != "tg-9" {
		t.Fatalf("another limit is its own entry: %v %v", other, err)
	}
}
