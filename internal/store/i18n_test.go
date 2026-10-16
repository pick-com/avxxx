package store

import (
	"errors"
	"strings"
	"testing"
)

func TestBuildTaskQuery(t *testing.T) {
	if _, _, _, err := buildTaskQuery(TaskFilter{Missing: true}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("missing without lang: %v", err)
	}
	if _, _, _, err := buildTaskQuery(TaskFilter{Cursor: &Cursor{Sort: "newest", Key: "x", ID: 1}}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("foreign cursor: %v", err)
	}
	sql, args, limit, err := buildTaskQuery(TaskFilter{Lang: "en", Status: "stale", EntityType: "tag", Limit: 5})
	if err != nil || limit != 5 || len(args) != 4 || args[3] != 6 {
		t.Fatalf("filtered: %v %v %d", err, args, limit)
	}
	for _, want := range []string{"t.lang=$1", "t.status=$2", "t.entity_type=$3", "LIMIT $4", "ORDER BY t.entity_type,t.entity_id,t.field,t.lang"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q: %s", want, sql)
		}
	}
	sql, args, _, err = buildTaskQuery(TaskFilter{Lang: "ko", Missing: true, Status: "reviewed"})
	if err != nil || len(args) != 2 || strings.Contains(sql, "status") {
		t.Fatalf("missing: %v %v %s", err, args, sql)
	}
	if !strings.Contains(sql, "FROM i18n_sources s") || !strings.Contains(sql, "NOT EXISTS") {
		t.Errorf("missing sql: %s", sql)
	}
}

func TestTaskCursorRoundTrip(t *testing.T) {
	raw := EncodeCursor(Cursor{Sort: taskSort, Key: `{"EntityType":"release","EntityID":"10","Field":"title","Lang":"en"}`, ID: 1})
	c, err := DecodeTaskCursor(raw)
	if err != nil {
		t.Fatal(err)
	}
	sql, args, _, err := buildTaskQuery(TaskFilter{Cursor: c})
	if err != nil || !strings.Contains(sql, "(t.entity_type,t.entity_id,t.field,t.lang)>($1,$2,$3,$4)") || args[1] != "10" {
		t.Errorf("cursor sql %v %v %s", err, args, sql)
	}
	if _, err := DecodeTaskCursor(EncodeCursor(Cursor{Sort: "newest", Key: "2024-01-01", ID: 3})); err == nil {
		t.Error("release cursor accepted as task cursor")
	}
	if _, err := DecodeTaskCursor("%%"); err == nil {
		t.Error("garbage accepted")
	}
}
