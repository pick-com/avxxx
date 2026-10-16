package api

// AdminDashboard site KPIs (PRD 11 A1):
//
//	GET /api/admin/site-metrics?days=30   plays, DAU, watch time and completion
//	                                      rate per UTC day (days 7-365, default
//	                                      30) with the latest closed day's
//	                                      day-over-day and 7-day comparisons
//
// Rows come from site_daily_metrics, rebuilt by the 'site_metrics' job
// (internal/sitemetrics). Needs dashboard:read (admin_perms.go).

import (
	"avxxx/internal/sitemetrics"
	"context"
	"net/http"
	"strconv"
	"time"
)

// siteMetricsStore backs the route; *store.Store implements it.
type siteMetricsStore interface {
	SiteDailyMetrics(ctx context.Context, from, to time.Time) ([]sitemetrics.Day, error)
}

func (srv *Server) adminSiteMetricsRoutes() []route {
	return []route{
		{"GET /api/admin/site-metrics", srv.admin(srv.siteMetrics)},
	}
}

// siteMetricsDay is one day of the response.
type siteMetricsDay struct {
	Day            string    `json:"day"`
	Plays          int64     `json:"plays"`
	DAU            int64     `json:"dau"`
	SignedIn       int64     `json:"signed_in"`
	WatchSeconds   int64     `json:"watch_seconds"`
	CompletedPlays int64     `json:"completed_plays"`
	CompletionRate *float64  `json:"completion_rate"`
	ComputedAt     time.Time `json:"computed_at"`
	// Partial: today (UTC), still being counted.
	Partial bool `json:"partial"`
}

type siteMetricsResponse struct {
	Days int `json:"days"`
	// LatestDay is the day the KPIs describe: yesterday (UTC), the last
	// closed day.
	LatestDay     string            `json:"latest_day"`
	KPIs          sitemetrics.KPIs  `json:"kpis"`
	Series        []siteMetricsDay  `json:"series"`
	RetentionDays int               `json:"retention_days"`
	Job           string            `json:"job"`
	Definitions   map[string]string `json:"definitions"`
}

var siteMetricDefinitions = map[string]string{
	"plays":           "distinct play_request plays per UTC day",
	"dau":             "distinct signed-in users, else devices, else sessions (no analytics consent) with any event",
	"watch_seconds":   "play_end watched_seconds, else request to last heartbeat, capped at 6 h, on the day the play was requested",
	"completion_rate": "plays ended with reason=completed or a 100 % progress milestone / plays",
}

func (srv *Server) siteMetrics(w http.ResponseWriter, r *http.Request) {
	if srv.siteMetricsSt == nil {
		reply(w, nil, errUnavailable)
		return
	}
	serveSiteMetrics(w, r, srv.siteMetricsSt, time.Now())
}

func serveSiteMetrics(w http.ResponseWriter, r *http.Request, st siteMetricsStore, now time.Time) {
	days := 30
	if v := r.URL.Query().Get("days"); v != "" {
		d, err := strconv.Atoi(v)
		if err != nil || d < 7 || d > 365 {
			reply(w, nil, badRequest("days must be 7 to 365", nil))
			return
		}
		days = d
	}
	today := sitemetrics.UTCDate(now)
	latest := today.AddDate(0, 0, -1)
	// The 7-day base reaches 8 days before latest.
	from := today.AddDate(0, 0, -(days - 1))
	if base := latest.AddDate(0, 0, -7); base.Before(from) {
		from = base
	}
	rows, err := st.SiteDailyMetrics(r.Context(), from, today)
	if err != nil {
		reply(w, nil, err)
		return
	}
	resp := siteMetricsResponse{Days: days, LatestDay: latest.Format(time.DateOnly), KPIs: sitemetrics.Summarize(rows, latest),
		Series: []siteMetricsDay{}, RetentionDays: sitemetrics.RetentionDays, Job: sitemetrics.JobKind, Definitions: siteMetricDefinitions}
	first := today.AddDate(0, 0, -(days - 1))
	for _, d := range rows {
		if d.Day.Before(first) {
			continue
		}
		out := siteMetricsDay{Day: d.Day.Format(time.DateOnly), Plays: d.Plays, DAU: d.DAU, SignedIn: d.SignedIn,
			WatchSeconds: d.WatchSeconds, CompletedPlays: d.CompletedPlays, ComputedAt: d.ComputedAt, Partial: d.Day.Equal(today)}
		if v, ok := d.CompletionRate(); ok {
			out.CompletionRate = &v
		}
		resp.Series = append(resp.Series, out)
	}
	reply(w, resp, nil)
}
