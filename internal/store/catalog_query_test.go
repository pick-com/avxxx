package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	for _, c := range []Cursor{
		{Sort: "newest", Key: "2024-05-01", ID: 42},
		{Sort: "name", Key: "Ai Uehara / 上原亜衣", ID: 7},
		{Sort: "works", Key: "0", ID: 1},
	} {
		raw := EncodeCursor(c)
		if strings.ContainsAny(raw, "+/=") {
			t.Errorf("cursor %q is not URL-safe", raw)
		}
		got, err := DecodeCursor(raw)
		if err != nil || got != c {
			t.Errorf("DecodeCursor(EncodeCursor(%+v)) = %+v, %v", c, got, err)
		}
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"!!!", "bm90IGpzb24", EncodeCursor(Cursor{Sort: "newest"}), EncodeCursor(Cursor{Key: "x", ID: 1})} {
		if _, err := DecodeCursor(raw); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("DecodeCursor(%q) err = %v, want ErrInvalidQuery", raw, err)
		}
	}
}

func TestBuildReleaseQueryDefault(t *testing.T) {
	sql, args, limit, err := buildReleaseQuery(ReleaseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if limit != 20 {
		t.Errorf("limit = %d, want 20", limit)
	}
	for _, want := range []string{
		"r.status='published'", "r.merged_into_id IS NULL",
		"ORDER BY COALESCE(r.release_date,DATE '0001-01-01') DESC,r.id DESC LIMIT $1",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if !reflect.DeepEqual(args, []any{21}) {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildReleaseQueryAllFilters(t *testing.T) {
	c := &Cursor{Sort: "runtime", Key: "120", ID: 9}
	sql, args, limit, err := buildReleaseQuery(ReleaseFilter{
		Q: "juq", Tag: "drama", Maker: "Madonna", Performer: "ai-uehara", Year: 2023,
		MinRuntime: 60, MaxRuntime: 180, Sort: "runtime", Cursor: c, Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if limit != 100 {
		t.Errorf("limit = %d, want clamp to 100", limit)
	}
	for _, want := range []string{
		"r.status='published'",
		"r.canonical_title ILIKE '%'||$1||'%'",
		"normalized_value=lower($1)",
		"normalized_tag=$2",
		"r.maker_name=$3",
		"p.slug=$4",
		"r.release_date>=$5 AND r.release_date<$6",
		"r.runtime_minutes>=$7",
		"r.runtime_minutes<=$8",
		"(COALESCE(r.runtime_minutes,0) < $9 OR (COALESCE(r.runtime_minutes,0) = $9 AND r.id < $10))",
		"ORDER BY COALESCE(r.runtime_minutes,0) DESC,r.id DESC LIMIT $11",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	want := []any{"juq", "drama", "Madonna", "ai-uehara",
		time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
		60, 180, int64(120), int64(9), 101}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("args = %#v\nwant %#v", args, want)
	}
	// User input never reaches the SQL text.
	for _, v := range []string{"juq", "drama", "Madonna", "ai-uehara"} {
		if strings.Contains(sql, v) {
			t.Errorf("sql contains literal %q", v)
		}
	}
}

func TestBuildReleaseQueryOldestCursor(t *testing.T) {
	sql, args, _, err := buildReleaseQuery(ReleaseFilter{Sort: "oldest", Cursor: &Cursor{Sort: "oldest", Key: "2020-02-03", ID: 5}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "(COALESCE(r.release_date,DATE '9999-12-31') > $1 OR (COALESCE(r.release_date,DATE '9999-12-31') = $1 AND r.id > $2))") {
		t.Errorf("bad keyset predicate:\n%s", sql)
	}
	if !strings.Contains(sql, "to_char(COALESCE(r.release_date,DATE '9999-12-31'),'YYYY-MM-DD')") {
		t.Errorf("missing cursor key column:\n%s", sql)
	}
	if args[0] != time.Date(2020, 2, 3, 0, 0, 0, 0, time.UTC) || args[1] != int64(5) {
		t.Errorf("args = %#v", args)
	}
}

func TestBuildReleaseQueryErrors(t *testing.T) {
	for name, f := range map[string]ReleaseFilter{
		"unknown sort":    {Sort: "views"},
		"cursor mismatch": {Sort: "newest", Cursor: &Cursor{Sort: "runtime", Key: "1", ID: 1}},
		"bad date key":    {Sort: "newest", Cursor: &Cursor{Sort: "newest", Key: "yesterday", ID: 1}},
		"bad int key":     {Sort: "runtime", Cursor: &Cursor{Sort: "runtime", Key: "long", ID: 1}},
	} {
		if _, _, _, err := buildReleaseQuery(f); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%s: err = %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestBuildPerformerQuery(t *testing.T) {
	sql, args, _, err := buildPerformerQuery(PerformerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"p.status='active'", "p.merged_into_id IS NULL", "r.status='published'", "ORDER BY p.canonical_name ASC,p.id ASC LIMIT $1"} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if !reflect.DeepEqual(args, []any{21}) {
		t.Errorf("args = %#v", args)
	}

	// works sorts on performer_stats; performers the hourly run has not
	// seen yet sort on a live count in a branch of their own.
	live := "(SELECT count(*) FROM release_performers lrp JOIN releases lr ON lr.id=lrp.release_id WHERE lrp.performer_id=p.id AND lr.status='published' AND lr.merged_into_id IS NULL)"
	sql, args, _, err = buildPerformerQuery(PerformerFilter{Q: "ai", Sort: "works", Cursor: &Cursor{Sort: "works", Key: "12", ID: 3}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"p.canonical_name ILIKE '%'||$1||'%'",
		"ps.performer_id IS NOT NULL AND (ps.works_count < $2 OR (ps.works_count = $2 AND p.id > $3)) ORDER BY ps.works_count DESC,p.id ASC LIMIT $4",
		"ps.performer_id IS NULL AND (" + live + " < $2 OR (" + live + " = $2 AND p.id > $3)) ORDER BY " + live + " DESC,p.id ASC LIMIT $4",
		"ORDER BY u.sk DESC,u.id ASC LIMIT $4) pg",
		// The live counts are only computed for the page's rows.
		"LIMIT $4) pg\n\tCROSS JOIN LATERAL (SELECT count(*) works",
		"ORDER BY pg.sk DESC,pg.id ASC",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if !reflect.DeepEqual(args, []any{"ai", int64(12), int64(3), 11}) {
		t.Errorf("args = %#v", args)
	}

	sql, _, _, err = buildPerformerQuery(PerformerFilter{Sort: "newest_work"})
	if err != nil || !strings.Contains(sql, "ORDER BY COALESCE(ps.latest_work_date,DATE '0001-01-01') DESC,p.id DESC LIMIT") ||
		!strings.Contains(sql, "ORDER BY COALESCE((SELECT max(lr.release_date) FROM") || !strings.Contains(sql, "ORDER BY pg.sk DESC,pg.id DESC") {
		t.Errorf("newest_work: %v\n%s", err, sql)
	}
	if _, _, _, err = buildPerformerQuery(PerformerFilter{Sort: "popular"}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("unknown sort err = %v", err)
	}
}

func TestSearchItemFields(t *testing.T) {
	d := time.Date(2024, 3, 9, 0, 0, 0, 0, time.UTC)
	maker, ja := "S1", "上原亜衣"
	rt := int32(120)
	rel := searchItem("release", 1, "abc-123", "Title", &d, &maker, &rt, nil, nil)
	want := map[string]any{"type": "release", "id": int64(1), "slug": "abc-123", "name": "Title",
		"release_date": "2024-03-09", "maker_name": "S1", "runtime_minutes": int32(120)}
	if !reflect.DeepEqual(rel, want) {
		t.Errorf("release item = %#v", rel)
	}
	perf := searchItem("performer", 2, "ai", "Ai", nil, nil, nil, &ja, nil)
	want = map[string]any{"type": "performer", "id": int64(2), "slug": "ai", "name": "Ai",
		"canonical_name_ja": "上原亜衣", "birth_date": nil}
	if !reflect.DeepEqual(perf, want) {
		t.Errorf("performer item = %#v", perf)
	}
}
