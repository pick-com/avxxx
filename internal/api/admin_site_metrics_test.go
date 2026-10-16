package api

import (
	"avxxx/internal/sitemetrics"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSiteMetrics struct {
	from, to time.Time
	rows     []sitemetrics.Day
}

func (f *fakeSiteMetrics) SiteDailyMetrics(_ context.Context, from, to time.Time) ([]sitemetrics.Day, error) {
	f.from, f.to = from, to
	return f.rows, nil
}

func TestSiteMetricsHandler(t *testing.T) {
	now := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return time.Date(2026, 10, 16-n, 0, 0, 0, 0, time.UTC) }
	st := &fakeSiteMetrics{rows: []sitemetrics.Day{
		{Day: day(9), Plays: 1000},
		{Day: day(2), Plays: 100, DAU: 10, CompletedPlays: 50},
		{Day: day(1), Plays: 150, DAU: 12, CompletedPlays: 30},
		{Day: day(0), Plays: 7},
	}}
	get := func(q string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		serveSiteMetrics(rec, httptest.NewRequest("GET", "/api/admin/site-metrics"+q, nil), st, now)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := get("?days=7")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	// The query reaches back to the 7-day base of yesterday.
	if !st.from.Equal(day(8)) || !st.to.Equal(day(0)) {
		t.Errorf("range %v..%v", st.from, st.to)
	}
	if out["latest_day"] != "2026-10-15" {
		t.Errorf("latest %v", out["latest_day"])
	}
	series := out["series"].([]any)
	if len(series) != 3 {
		t.Fatalf("series %v", series)
	}
	last := series[2].(map[string]any)
	if last["partial"] != true || last["completion_rate"] != 0.0 {
		t.Errorf("today %v", last)
	}
	plays := out["kpis"].(map[string]any)["plays"].(map[string]any)
	if plays["value"] != 150.0 || plays["dod"] != 0.5 {
		t.Errorf("plays kpi %v", plays)
	}
	if c, _ := get("?days=3"); c != http.StatusBadRequest {
		t.Errorf("days=3: %d", c)
	}
	if c, _ := get("?days=x"); c != http.StatusBadRequest {
		t.Errorf("days=x: %d", c)
	}
}

func TestSiteMetricsRoutePermission(t *testing.T) {
	srv := &Server{adminAuth: newFakeAdmins(), siteMetricsSt: &fakeSiteMetrics{}}
	mux := http.NewServeMux()
	for _, rt := range srv.adminSiteMetricsRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	for role, want := range map[string]int{"ad_sales": 200, "translator": 403} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, asAdmin(httptest.NewRequest("GET", "/api/admin/site-metrics", nil), "u-"+role, role))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", role, rec.Code, want)
		}
	}
}
