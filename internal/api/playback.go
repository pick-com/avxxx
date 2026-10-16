package api

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (srv *Server) playbackRoutes() []route {
	return []route{
		{"GET /api/releases/{slug}/play-config", srv.playConfig},
	}
}

// playbackStore is the subset of *store.Store the play-config handler needs,
// so the handler can be tested without a database.
type playbackStore interface {
	PlaybackForRelease(ctx context.Context, slug string) (store.PlaybackRelease, []store.PlaybackSource, error)
	ResumePosition(ctx context.Context, userID, releaseID int64) (int, bool, error)
	UserIDByToken(ctx context.Context, token string) (int64, error)
	DanmakuCurves(ctx context.Context, releaseID int64) (map[string][]int16, error)
}

func (srv *Server) playConfig(w http.ResponseWriter, r *http.Request) {
	servePlayConfig(w, r, srv.store)
}

type playResume struct {
	SourceID        int64 `json:"source_id"`
	PositionSeconds int   `json:"position_seconds"`
}

type playConfigResponse struct {
	Release store.PlaybackRelease  `json:"release"`
	Sources []store.PlaybackSource `json:"sources"`
	Resume  *playResume            `json:"resume"`
	Ads     any                    `json:"ads"`
	Danmaku map[string]any         `json:"danmaku"`
	// DanmakuCurve is the peak curve of the main timeline (PRD 5.5.1: 200
	// values 0-255), null with fewer than 50 valid danmaku. Alternate
	// versions' curves are in danmaku.alt_curves keyed by source id.
	DanmakuCurve []int16 `json:"danmaku_curve"`
	Subtitles    []any   `json:"subtitles"`
	Chapters     []any   `json:"chapters"`
}

var regionCode = regexp.MustCompile(`^[A-Za-z]{2}$`)

// servePlayConfig implements GET /api/releases/{slug}/play-config
// (tech plan 3.3). Query: region (ISO 3166-1 alpha-2), quality (preferred
// max height, e.g. 1080). A valid bearer token adds resume information; a
// missing or invalid token is not an error.
func servePlayConfig(w http.ResponseWriter, r *http.Request, ps playbackStore) {
	q := r.URL.Query()
	region := strings.ToUpper(strings.TrimSpace(q.Get("region")))
	if region != "" && !regionCode.MatchString(region) {
		writePlayJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid region"})
		return
	}
	quality := 0
	if v := strings.TrimSuffix(strings.TrimSpace(q.Get("quality")), "p"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 || n > 10000 {
			writePlayJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid quality"})
			return
		}
		quality = n
	}

	rel, all, err := ps.PlaybackForRelease(r.Context(), r.PathValue("slug"))
	if errors.Is(err, pgx.ErrNoRows) {
		writePlayJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		log.Print(err)
		writePlayJSON(w, http.StatusInternalServerError, map[string]string{"error": "request failed"})
		return
	}

	plan := store.PlanPlayback(rel, all, region, quality)
	rel.DurationSeconds = plan.DurationSeconds
	resp := playConfigResponse{
		Release:   rel,
		Sources:   plan.Sources,
		Danmaku:   map[string]any{"enabled": true, "segment_ms": danmakuSegmentMS},
		Subtitles: []any{},
		Chapters:  []any{},
	}
	resp.DanmakuCurve, resp.Danmaku["alt_curves"] = danmakuCurves(r.Context(), ps, rel.ID, plan.Sources)
	if resp.Sources == nil {
		resp.Sources = []store.PlaybackSource{}
	}
	for i := range resp.Sources {
		if resp.Sources[i].AudioLangs == nil {
			resp.Sources[i].AudioLangs = []string{}
		}
		if resp.Sources[i].SubtitleLangs == nil {
			resp.Sources[i].SubtitleLangs = []string{}
		}
	}

	if token := bearerToken(r); token != "" && strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		if uid, err := ps.UserIDByToken(r.Context(), token); err == nil {
			if sourceID, ok := store.ResumeSource(plan.Sources); ok {
				pos, found, err := ps.ResumePosition(r.Context(), uid, rel.ID)
				if err != nil {
					log.Print(err)
				} else if found {
					resp.Resume = &playResume{SourceID: sourceID, PositionSeconds: pos}
				}
			}
		}
	}
	writePlayJSON(w, http.StatusOK, resp)
}

// danmakuCurves loads the release's peak curves: the main timeline's, and
// those of alternate-version sources in play order keyed by source id. A
// failure only drops the curves (logged); playback must not depend on them.
func danmakuCurves(ctx context.Context, ps playbackStore, releaseID int64, sources []store.PlaybackSource) ([]int16, map[string][]int16) {
	alt := map[string][]int16{}
	curves, err := ps.DanmakuCurves(ctx, releaseID)
	if err != nil {
		log.Print(err)
		return nil, alt
	}
	for _, x := range sources {
		if c, ok := curves[community.AltVersionKey(x.ID)]; ok && x.IsAltVersion {
			alt[strconv.FormatInt(x.ID, 10)] = c
		}
	}
	return curves[community.DefaultVersion], alt
}

func writePlayJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
