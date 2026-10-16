package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubPlaybackStore struct {
	rel     store.PlaybackRelease
	sources []store.PlaybackSource
	err     error
	tokens  map[string]int64
	resume  map[int64]int
	curves  map[string][]int16
}

func (s *stubPlaybackStore) PlaybackForRelease(_ context.Context, slug string) (store.PlaybackRelease, []store.PlaybackSource, error) {
	if s.err != nil {
		return store.PlaybackRelease{}, nil, s.err
	}
	if slug != s.rel.Slug {
		return store.PlaybackRelease{}, nil, pgx.ErrNoRows
	}
	return s.rel, s.sources, nil
}

func (s *stubPlaybackStore) ResumePosition(_ context.Context, userID, _ int64) (int, bool, error) {
	p, ok := s.resume[userID]
	return p, ok && p > 0, nil
}

func (s *stubPlaybackStore) UserIDByToken(_ context.Context, token string) (int64, error) {
	if id, ok := s.tokens[token]; ok {
		return id, nil
	}
	return 0, pgx.ErrNoRows
}

func (s *stubPlaybackStore) DanmakuCurves(context.Context, int64) (map[string][]int16, error) {
	return s.curves, nil
}

func ptr[T any](v T) *T { return &v }

func newStub() *stubPlaybackStore {
	return &stubPlaybackStore{
		rel: store.PlaybackRelease{ID: 1, Slug: "juq-743", Title: "Title", RuntimeMinutes: 120},
		sources: []store.PlaybackSource{
			{ID: 12, Kind: store.KindOfficialEmbed, Provider: "p", Label: "main", URL: ptr("https://e/12"), MaxHeight: ptr(1080),
				DurationSeconds: ptr(7200), VersionKind: store.VersionMain, Priority: 1, HealthScore: 1, Status: "active"},
			{ID: 13, Kind: store.KindOfficialEmbed, Provider: "p", Label: "cut", URL: ptr("https://e/13"), MaxHeight: ptr(1080),
				DurationSeconds: ptr(8000), VersionKind: store.VersionMain, Priority: 2, HealthScore: 1, Status: "active"},
			{ID: 14, Kind: store.KindExternalLink, Provider: "q", Label: "jp only", URL: ptr("https://e/14"),
				VersionKind: store.VersionMain, Priority: 0, HealthScore: 1, Status: "active", RegionAllow: []string{"JP"},
				LicenseRef: ptr("SECRET-1")},
		},
		tokens: map[string]int64{"good": 7},
		resume: map[int64]int{7: 1834},
	}
}

func doPlayConfig(t *testing.T, ps playbackStore, target, auth string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/releases/{slug}/play-config", func(w http.ResponseWriter, r *http.Request) { servePlayConfig(w, r, ps) })
	req := httptest.NewRequest("GET", target, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestPlayConfigShape(t *testing.T) {
	rec, body := doPlayConfig(t, newStub(), "/api/releases/juq-743/play-config?region=de&quality=1080", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type %q", ct)
	}
	rel := body["release"].(map[string]any)
	if rel["slug"] != "juq-743" || rel["title"] != "Title" || rel["duration_seconds"] != float64(7200) {
		t.Errorf("release %v", rel)
	}
	srcs := body["sources"].([]any)
	if len(srcs) != 2 {
		t.Fatalf("sources %v, want 12 and 13 (14 is JP-only)", srcs)
	}
	first, second := srcs[0].(map[string]any), srcs[1].(map[string]any)
	if first["id"] != float64(12) || first["is_alt_version"] != false || second["is_alt_version"] != true || second["version_kind"] != "alt" {
		t.Errorf("sources %v", srcs)
	}
	for _, hidden := range []string{"license_ref", "region_allow", "health_score", "status", "storage_prefix"} {
		if _, ok := first[hidden]; ok {
			t.Errorf("source exposes %s", hidden)
		}
	}
	if body["resume"] != nil || body["ads"] != nil {
		t.Errorf("resume/ads should be null: %v %v", body["resume"], body["ads"])
	}
	if d := body["danmaku"].(map[string]any); d["enabled"] != true {
		t.Errorf("danmaku %v", d)
	}
	if s, ok := body["subtitles"].([]any); !ok || len(s) != 0 {
		t.Errorf("subtitles %v", body["subtitles"])
	}
	if c, ok := body["chapters"].([]any); !ok || len(c) != 0 {
		t.Errorf("chapters %v", body["chapters"])
	}
}

func TestPlayConfigRegionAndResume(t *testing.T) {
	_, body := doPlayConfig(t, newStub(), "/api/releases/juq-743/play-config?region=JP", "Bearer good")
	srcs := body["sources"].([]any)
	if len(srcs) != 3 || srcs[0].(map[string]any)["id"] != float64(14) {
		t.Fatalf("JP sources %v", srcs)
	}
	res, ok := body["resume"].(map[string]any)
	if !ok || res["source_id"] != float64(14) || res["position_seconds"] != float64(1834) {
		t.Errorf("resume %v", body["resume"])
	}

	_, body = doPlayConfig(t, newStub(), "/api/releases/juq-743/play-config", "Bearer bad")
	if body["resume"] != nil {
		t.Errorf("invalid token should give null resume, got %v", body["resume"])
	}
}

func TestPlayConfigErrors(t *testing.T) {
	cases := []struct {
		name, target string
		stub         *stubPlaybackStore
		want         int
	}{
		{"unknown slug", "/api/releases/nope/play-config", newStub(), 404},
		{"bad region", "/api/releases/juq-743/play-config?region=JPN", newStub(), 400},
		{"bad quality", "/api/releases/juq-743/play-config?quality=hd", newStub(), 400},
		{"quality with p", "/api/releases/juq-743/play-config?quality=720p", newStub(), 200},
		{"store failure", "/api/releases/juq-743/play-config", &stubPlaybackStore{err: errors.New("boom")}, 500},
	}
	for _, c := range cases {
		rec, _ := doPlayConfig(t, c.stub, c.target, "")
		if rec.Code != c.want {
			t.Errorf("%s: status %d want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestPlayConfigEmptySources(t *testing.T) {
	stub := newStub()
	stub.sources = nil
	stub.rel.RuntimeMinutes = 0
	_, body := doPlayConfig(t, stub, "/api/releases/juq-743/play-config", "Bearer good")
	if s, ok := body["sources"].([]any); !ok || len(s) != 0 {
		t.Errorf("sources %v, want []", body["sources"])
	}
	if body["release"].(map[string]any)["duration_seconds"] != nil || body["resume"] != nil {
		t.Errorf("body %v", body)
	}
}
