package api

import (
	"avxxx/internal/store"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// skipPublicRoutes are public GET routes the walk does not call: the media
// provider routes need live third-party adapters (none in tests).
var skipPublicRoutes = map[string]bool{
	"GET /api/search":                  true,
	"GET /api/detail/{source}/{id...}": true,
}

// TestPublicAPINoRawDataIntegration walks every public GET route of the
// route table against fixtures whose source records carry a canary in
// raw_data and in a non-reported source field, and asserts no response
// contains raw_data, the canary, a hidden birthday or override reasons
// (FINAL_DATA_DESIGN 9 / 10, ROADMAP 全量验收: 公开 API 不泄露 raw_data).
//
// It runs in its own throw-away database created next to
// AVXXX_TEST_DATABASE_URL (make test-integration), because other packages'
// integration tests reset that database's schema concurrently.
func TestPublicAPINoRawDataIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	dbName := fmt.Sprintf("avxxx_api_rawdata_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	t.Setenv("DATABASE_URL", u.String())
	t.Setenv("ADMIN_TOKEN", "")
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	id := func(q string, args ...any) int64 {
		t.Helper()
		var v int64
		if err := s.QueryRow(ctx, q, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return v
	}
	const canary = "RAWCANARY-7f3a"
	const hiddenBirthday = "1998-07-06"
	payload := fmt.Sprintf(`{"secret":%q,"name_ja":"ノ","title":"Raw Title","raw_html":"<b>%s</b>"}`, canary, canary)
	src := id(`INSERT INTO sources(name,url,source_kind) VALUES('walk-src','https://walk.test/','aggregator') RETURNING id`)
	rel := id(`INSERT INTO releases(slug,canonical_title,series_name,maker_name,status,release_date) VALUES('walk-001','Walk Vol.1','Walk Series','Walk Maker','published','2024-01-01') RETURNING id`)
	perf := id(`INSERT INTO performers(slug,canonical_name,birth_date,birth_date_hidden) VALUES('walk-star','Walk Star',$1,true) RETURNING id`, hiddenBirthday)
	exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, rel, perf)
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,'Walk Tag','walk-tag')`, rel)
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'dvd_code','WALK-001','walk-001')`, rel)
	exec(`INSERT INTO release_overrides(release_id,field_name,value,reason,actor) VALUES($1,'label_name','"L"','OVERRIDE-REASON-x','OVERRIDE-ACTOR-x')`, rel)
	for _, r := range []struct {
		col string
		id  int64
		et  string
	}{{"release_id", rel, "release"}, {"performer_id", perf, "performer"}} {
		rec := id(`INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,`+r.col+`,raw_data,parser_version,resolution_status)
			VALUES($1,$2,$3,'https://walk.test/'||$3,$4,$5::jsonb,'t','resolved') RETURNING id`, src, r.et, "k-"+r.et, r.id, payload)
		for field, v := range map[string]string{"secret": canary, "birth_date": hiddenBirthday, "title": "Walk Vol.1", "name": "Walk Star"} {
			exec(`INSERT INTO source_field_values(source_record_id,field_name,raw_value,normalized_value,captured_at) VALUES($1,$2,to_jsonb($3::text),to_jsonb($3::text),now())`, rec, field, v)
		}
	}
	seriesSlug := ""
	if err := s.QueryRow(ctx, `SELECT slug FROM series WHERE name='Walk Series'`).Scan(&seriesSlug); err != nil {
		t.Fatal(err)
	}

	h := New(s, nil)
	paths := func(pattern string) string {
		_, p, _ := strings.Cut(pattern, " ")
		switch {
		case strings.HasPrefix(p, "/api/series/"):
			p = strings.ReplaceAll(p, "{slug}", seriesSlug)
		case strings.HasPrefix(p, "/api/performers/"):
			p = strings.ReplaceAll(p, "{slug}", "walk-star")
		default:
			p = strings.ReplaceAll(p, "{slug}", "walk-001")
		}
		p = strings.ReplaceAll(p, "{lang}", "en")
		return samplePath(p)
	}
	queries := []string{"", "?q=walk&lang=en"}
	walked := 0
	for _, rt := range (&Server{}).routes() {
		method, p, _ := strings.Cut(rt.pattern, " ")
		if method != http.MethodGet || strings.HasPrefix(p, "/api/admin/") || skipPublicRoutes[rt.pattern] {
			continue
		}
		for _, q := range queries {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(method, paths(rt.pattern)+q, nil))
			body, _ := io.ReadAll(rec.Body)
			walked++
			if rec.Code >= 500 {
				t.Errorf("%s%s: status %d", rt.pattern, q, rec.Code)
			}
			for _, leak := range []string{"raw_data", canary, "raw_html", hiddenBirthday, "OVERRIDE-REASON", "OVERRIDE-ACTOR"} {
				if strings.Contains(string(body), leak) {
					t.Errorf("%s%s leaks %q: %s", rt.pattern, q, leak, body)
				}
			}
		}
	}
	if walked < 20 {
		t.Errorf("walked only %d requests", walked)
	}
	// The fixtures are visible where they should be (the walk is not vacuous).
	for p, want := range map[string]string{
		"/api/releases/walk-001/evidence":    `"walk-src"`,
		"/api/performers/walk-star/evidence": `"Walk Star"`,
		"/api/series/" + seriesSlug:          `"Walk Series"`,
		"/api/releases/walk-001":             `"series"`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d, want %s in %s", p, rec.Code, want, rec.Body)
		}
	}
}
