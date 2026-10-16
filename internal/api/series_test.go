package api

import (
	"avxxx/internal/completeness"
	"avxxx/internal/evidence"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type fakeSeries struct {
	rels   []store.SeriesRelease
	filter store.SeriesFilter
}

func (f *fakeSeries) SeriesPage(_ context.Context, sf store.SeriesFilter) ([]store.SeriesListItem, string, error) {
	f.filter = sf
	return []store.SeriesListItem{{ID: 1, Slug: "best-of", Name: "Best Of", ReleaseCount: 3}}, "", nil
}

func (f *fakeSeries) SeriesDetailBySlug(_ context.Context, slug string, _ int) (store.SeriesDetail, error) {
	if slug != "best-of" {
		return store.SeriesDetail{}, pgx.ErrNoRows
	}
	return store.SeriesDetail{SeriesListItem: store.SeriesListItem{ID: 1, Slug: slug, Name: "Best Of"},
		Makers: []store.SeriesMaker{{Name: "M", ReleaseCount: 3}}, TopPerformers: []store.SeriesPerformer{{ID: 5, Slug: "p", Name: "P", ReleaseCount: 3}}}, nil
}

func (f *fakeSeries) SeriesReleases(_ context.Context, slug string) ([]store.SeriesRelease, error) {
	if slug != "best-of" {
		return nil, pgx.ErrNoRows
	}
	return f.rels, nil
}

type fakeEvidence struct{}

func (fakeEvidence) ReleaseEvidence(_ context.Context, slug string) (evidence.Evidence, error) {
	if slug != "abc-001" {
		return evidence.Evidence{}, pgx.ErrNoRows
	}
	return evidence.Build(evidence.Input{EntityType: "release", Slug: slug,
		Canonical: map[string]json.RawMessage{"canonical_title": json.RawMessage(`"T"`)},
		Records:   []evidence.Record{{ID: 1, SourceName: "src", SourceKind: "aggregator"}},
		Values:    []evidence.Value{{RecordID: 1, Field: "title", JSON: json.RawMessage(`"T"`)}}}), nil
}

func (fakeEvidence) PerformerEvidence(context.Context, string) (evidence.Evidence, error) {
	return evidence.Evidence{}, pgx.ErrNoRows
}

type fakeReports struct{ opts completeness.Options }

func (f *fakeReports) CompletenessReport(_ context.Context, o completeness.Options) (completeness.Report, error) {
	f.opts = o
	if o.Source == "nope" {
		return completeness.Report{}, completeness.ErrUnknownSource
	}
	b := completeness.NewBuilder(o, nil)
	b.Add(completeness.Facts{ID: 1, Slug: "a", Maker: "M"})
	return b.Report(time.Unix(0, 0)), nil
}

func seriesServer() (*Server, *fakeSeries, *fakeReports) {
	day := func(s string) *time.Time { t, _ := time.Parse(time.DateOnly, s); return &t }
	fs := &fakeSeries{}
	for i := 1; i <= 5; i++ {
		fs.rels = append(fs.rels, store.SeriesRelease{
			Release:     model.Release{ID: int64(i), Slug: fmt.Sprintf("abc-%03d", i), CanonicalTitle: fmt.Sprintf("Best Of Vol.%d", 6-i), ReleaseDate: day(fmt.Sprintf("2024-0%d-01", i)), Status: "published"},
			ProductCode: fmt.Sprintf("ABC-%03d", i),
		})
	}
	fr := &fakeReports{}
	return &Server{seriesSt: fs, evidence: fakeEvidence{}, reports: fr, adminToken: "tok", adminAuth: newFakeAdmins()}, fs, fr
}

func serve(srv *Server, method, target string, admin bool) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	req := httptest.NewRequest(method, target, nil)
	if admin {
		req.Header.Set("Authorization", "Bearer tok")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSeriesRoutesStatus(t *testing.T) {
	srv, fs, _ := seriesServer()
	cases := []struct {
		target string
		want   int
	}{
		{"/api/series", 200},
		{"/api/series?sort=works&q=best", 200},
		{"/api/series?sort=bogus", 400},
		{"/api/series?limit=-1", 400},
		{"/api/series?cursor=not-a-cursor!", 400},
		{"/api/series?lang=xx-invalid", 400},
		{"/api/series/best-of", 200},
		{"/api/series/best-of?lang=en", 200},
		{"/api/series/missing", 404},
		{"/api/series/best-of/releases", 200},
		{"/api/series/best-of/releases?sort=oldest", 200},
		{"/api/series/best-of/releases?sort=random", 400},
		{"/api/series/best-of/releases?cursor=" + store.EncodeCursor(store.Cursor{Sort: "newest", Key: "x", ID: 1}), 400},
		{"/api/series/missing/releases", 404},
		{"/api/releases/abc-001/evidence", 200},
		{"/api/releases/zzz/evidence", 404},
		{"/api/performers/p/evidence", 404},
	}
	for _, c := range cases {
		if rec := serve(srv, "GET", c.target, false); rec.Code != c.want {
			t.Errorf("GET %s = %d, want %d (%s)", c.target, rec.Code, c.want, rec.Body)
		}
	}
	serve(srv, "GET", "/api/series?sort=works&q=best", false)
	if fs.filter.Sort != "works" || fs.filter.Q != "best" {
		t.Errorf("filter: %+v", fs.filter)
	}
	// Without stores (no database) the routes answer 503 instead of panicking.
	empty := &Server{}
	for _, p := range []string{"/api/series", "/api/series/x", "/api/series/x/releases", "/api/releases/x/evidence", "/api/performers/x/evidence"} {
		if rec := serve(empty, "GET", p, false); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s without store = %d", p, rec.Code)
		}
	}
}

func TestSeriesReleasesEpisodeOrderAndPaging(t *testing.T) {
	srv, _, _ := seriesServer()
	var got []int64
	target := "/api/series/best-of/releases?limit=2"
	for pages := 0; pages < 5; pages++ {
		rec := serve(srv, "GET", target, false)
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var body struct {
			Items []struct {
				ID      int64 `json:"id"`
				Episode *int  `json:"episode"`
			} `json:"items"`
			Next  *string `json:"next_cursor"`
			Basis string  `json:"episode_basis"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Basis != "title" {
			t.Errorf("basis %q", body.Basis)
		}
		for _, it := range body.Items {
			got = append(got, it.ID)
		}
		if body.Next == nil {
			break
		}
		target = "/api/series/best-of/releases?limit=2&cursor=" + *body.Next
	}
	// Titles are Vol.5 … Vol.1 for ids 1 … 5: episode order is 5,4,3,2,1.
	if fmt.Sprint(got) != "[5 4 3 2 1]" {
		t.Errorf("episode order across pages: %v", got)
	}
}

func TestCompletenessReportHandler(t *testing.T) {
	srv, _, fr := seriesServer()
	if rec := serve(srv, "GET", "/api/admin/reports/completeness?entity=release", false); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	bad := []string{"", "?entity=video", "?entity=performer&group_by=maker", "?entity=release&group_by=tag",
		"?entity=release&format=xml", "?entity=release&runs=-1", "?entity=release&top=500", "?entity=release&source=nope"}
	for _, q := range bad {
		if rec := serve(srv, "GET", "/api/admin/reports/completeness"+q, true); rec.Code != 400 {
			t.Errorf("%s: %d, want 400 (%s)", q, rec.Code, rec.Body)
		}
	}
	rec := serve(srv, "GET", "/api/admin/reports/completeness?entity=release&group_by=maker&runs=30&source=official", true)
	if rec.Code != 200 || fr.opts.TrendRuns != 30 || fr.opts.GroupBy != "maker" || fr.opts.Source != "official" {
		t.Fatalf("json: %d %+v %s", rec.Code, fr.opts, rec.Body)
	}
	var rep completeness.Report
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil || rep.Total != 1 || len(rep.Groups) != 1 {
		t.Errorf("report: %+v %v", rep, err)
	}
	rec = serve(srv, "GET", "/api/admin/reports/completeness?entity=performer&format=csv", true)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "completeness-performer-field.csv") ||
		!strings.HasPrefix(rec.Body.String(), strings.Join(completeness.CSVHeader, ",")) {
		t.Errorf("csv: %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	if rec := serve(&Server{adminToken: "tok", adminAuth: newFakeAdmins()}, "GET", "/api/admin/reports/completeness?entity=release", true); rec.Code != 503 {
		t.Errorf("no store: %d", rec.Code)
	}
}
