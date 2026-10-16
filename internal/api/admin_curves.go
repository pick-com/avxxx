package api

import (
	"avxxx/internal/playcurve"
	"avxxx/internal/store"
	"context"
	"math"
	"net/http"
	"strconv"
	"time"
)

// releaseCurvesStore is the subset of *store.Store the admin curves handler
// needs, so it can be tested without a database.
type releaseCurvesStore interface {
	ReleaseCurves(ctx context.Context, releaseID int64) ([]store.ReleaseCurveSet, error)
}

func (srv *Server) adminCurveRoutes() []route {
	return []route{
		{"GET /api/admin/releases/{id}/curves", srv.admin(srv.releaseCurves)},
	}
}

func (srv *Server) releaseCurves(w http.ResponseWriter, r *http.Request) {
	serveReleaseCurves(w, r, srv.store)
}

// curveVersion is one timeline of GET /api/admin/releases/{id}/curves.
// Arrays have 200 buckets (bucket width = duration_ms / 200).
type curveVersion struct {
	VersionKey string    `json:"version_key"`
	ValidPlays int       `json:"valid_plays"`
	DurationMS *int      `json:"duration_ms"`
	ComputedAt time.Time `json:"computed_at"`
	// Sufficient is false below 1000 valid plays: replay_peak is null and
	// the front end shows no replay curve (PRD 14.3).
	Sufficient bool `json:"sufficient"`
	// Retention is viewers[b] / valid plays (0-1): the retention curve.
	Retention []float64 `json:"retention"`
	Viewers   []int     `json:"viewers"`
	Replays   []int     `json:"replays"`
	// ReplayRate is replays[b] / viewers[b] (0-1, untrimmed and unsmoothed);
	// ReplayPeak is the trimmed, smoothed 0-255 curve the player gets.
	ReplayRate []float64 `json:"replay_rate"`
	ReplayPeak []int     `json:"replay_peak"`
	// Skips / SkipRate (skips / valid plays) are null when the duration of
	// the timeline is unknown.
	Skips              []int     `json:"skips"`
	SkipRate           []float64 `json:"skip_rate"`
	IntroSkipSuggested bool      `json:"intro_skip_suggested"`
}

type releaseCurvesResponse struct {
	ReleaseID     int64 `json:"release_id"`
	MinValidPlays int   `json:"min_valid_plays"`
	// IntroSkipSuggested (main timeline): skips are large and concentrated
	// in the first 10%, so operators should mark the intro (PRD 14.3).
	IntroSkipSuggested bool           `json:"intro_skip_suggested"`
	Versions           []curveVersion `json:"versions"`
}

// serveReleaseCurves implements GET /api/admin/releases/{id}/curves for the
// AdminVideo 数据 tab (PRD 14.3): retention, replay and skip curves per
// timeline, main first. A release without computed curves has no versions.
func serveReleaseCurves(w http.ResponseWriter, r *http.Request, cs releaseCurvesStore) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		reply(w, nil, badRequest("invalid id", err))
		return
	}
	sets, err := cs.ReleaseCurves(r.Context(), id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	resp := releaseCurvesResponse{ReleaseID: id, MinValidPlays: playcurve.MinValidPlays, Versions: []curveVersion{}}
	for _, set := range sets {
		v := buildCurveVersion(set)
		if v.VersionKey == "main" {
			resp.IntroSkipSuggested = v.IntroSkipSuggested
		}
		resp.Versions = append(resp.Versions, v)
	}
	reply(w, resp, nil)
}

func buildCurveVersion(set store.ReleaseCurveSet) curveVersion {
	v := curveVersion{VersionKey: set.VersionKey, ValidPlays: set.ValidPlays, DurationMS: set.DurationMS, ComputedAt: set.ComputedAt,
		Sufficient: set.ValidPlays >= playcurve.MinValidPlays}
	v.Viewers = fullCurve(set.Data[playcurve.KindViewers])
	v.Replays = fullCurve(set.Data[playcurve.KindReplays])
	v.ReplayPeak = curveOrNil(set.Data[playcurve.KindReplayPeak])
	v.Retention = make([]float64, playcurve.Buckets)
	v.ReplayRate = make([]float64, playcurve.Buckets)
	for b := range v.Retention {
		v.Retention[b] = ratio(v.Viewers[b], set.ValidPlays)
		v.ReplayRate[b] = ratio(v.Replays[b], v.Viewers[b])
	}
	if skips := curveOrNil(set.Data[playcurve.KindSkips]); skips != nil {
		v.Skips = skips
		v.SkipRate = make([]float64, playcurve.Buckets)
		for b, s := range skips {
			v.SkipRate[b] = ratio(s, set.ValidPlays)
		}
		v.IntroSkipSuggested = playcurve.IntroSkipSuggested(skips, set.ValidPlays)
	}
	return v
}

// fullCurve returns data, or 200 zeros when it is missing or malformed.
func fullCurve(data []int) []int {
	if len(data) == playcurve.Buckets {
		return data
	}
	return make([]int, playcurve.Buckets)
}

func curveOrNil(data []int) []int {
	if len(data) == playcurve.Buckets {
		return data
	}
	return nil
}

// ratio is n/d rounded to 4 decimals, 0 when d is 0.
func ratio(n, d int) float64 {
	if d <= 0 {
		return 0
	}
	return math.Round(float64(n)/float64(d)*10000) / 10000
}
