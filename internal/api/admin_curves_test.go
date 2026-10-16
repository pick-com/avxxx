package api

import (
	"avxxx/internal/playcurve"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func curveOf(v int16, at ...int) []int16 {
	c := make([]int16, playcurve.Buckets)
	for _, b := range at {
		c[b] = v
	}
	return c
}

func TestPlayConfigReplayAndPeakCurves(t *testing.T) {
	cases := []struct {
		name            string
		danmaku, replay map[string][]int16
		wantReplay      bool
		wantPeak        map[int]float64 // bucket → value, nil = peak_curve null
	}{
		{"neither", nil, nil, false, nil},
		{"replay only", nil, map[string][]int16{"main": curveOf(255, 20)}, true, map[int]float64{20: 255}},
		{"danmaku only", map[string][]int16{"main": curveOf(255, 10)}, nil, false, map[int]float64{10: 255}},
		{"both", map[string][]int16{"main": curveOf(255, 10)}, map[string][]int16{"main": curveOf(255, 20)}, true, map[int]float64{10: 255, 20: 109}},
		{"alt replay only is not main", nil, map[string][]int16{"alt:13": curveOf(255, 20)}, false, nil},
	}
	for _, c := range cases {
		st := newStub()
		st.curves, st.replay = c.danmaku, c.replay
		rec, body := doPlayConfig(t, st, "/api/releases/juq-743/play-config", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", c.name, rec.Code)
		}
		for _, k := range []string{"replay_curve", "peak_curve", "danmaku_curve"} {
			if _, ok := body[k]; !ok {
				t.Errorf("%s: %s missing", c.name, k)
			}
		}
		rc, _ := body["replay_curve"].([]any)
		if (rc != nil) != c.wantReplay || (rc != nil && len(rc) != playcurve.Buckets) {
			t.Errorf("%s: replay_curve = %v", c.name, body["replay_curve"])
		}
		pc, _ := body["peak_curve"].([]any)
		if c.wantPeak == nil {
			if body["peak_curve"] != nil {
				t.Errorf("%s: peak_curve = %v, want null", c.name, body["peak_curve"])
			}
			continue
		}
		if len(pc) != playcurve.Buckets {
			t.Fatalf("%s: peak_curve = %v", c.name, body["peak_curve"])
		}
		for b, want := range c.wantPeak {
			if pc[b].(float64) != want {
				t.Errorf("%s: peak_curve[%d] = %v, want %v", c.name, b, pc[b], want)
			}
		}
	}
}

type stubCurves struct {
	sets []store.ReleaseCurveSet
	err  error
}

func (s stubCurves) ReleaseCurves(context.Context, int64) ([]store.ReleaseCurveSet, error) {
	return s.sets, s.err
}

func doCurves(t *testing.T, cs releaseCurvesStore, target string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/admin/releases/{id}/curves", func(w http.ResponseWriter, r *http.Request) { serveReleaseCurves(w, r, cs) })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func ints(v int, at ...int) []int {
	c := make([]int, playcurve.Buckets)
	for _, b := range at {
		c[b] = v
	}
	return c
}

func TestAdminReleaseCurvesShape(t *testing.T) {
	dur := 600_000
	viewers := ints(2000)
	for b := range viewers {
		viewers[b] = 2000 - b*5
	}
	skips := ints(600, 3, 4, 5)
	main := store.ReleaseCurveSet{VersionKey: "main", ValidPlays: 2000, DurationMS: &dur, ComputedAt: time.Unix(1760000000, 0).UTC(),
		Data: map[string][]int{
			playcurve.KindViewers: viewers, playcurve.KindReplays: ints(500, 100), playcurve.KindSkips: skips,
			playcurve.KindRetention: ints(0), playcurve.KindReplayPeak: ints(255, 100),
		}}
	alt := store.ReleaseCurveSet{VersionKey: "alt:9", ValidPlays: 10, Data: map[string][]int{
		playcurve.KindViewers: ints(10, 0), playcurve.KindReplays: ints(0), playcurve.KindReplayPeak: nil,
	}}
	rec, body := doCurves(t, stubCurves{sets: []store.ReleaseCurveSet{main, alt}}, "/api/admin/releases/7/curves")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if body["release_id"].(float64) != 7 || body["min_valid_plays"].(float64) != 1000 || body["intro_skip_suggested"] != true {
		t.Fatalf("top level: %v", body)
	}
	vs := body["versions"].([]any)
	if len(vs) != 2 {
		t.Fatalf("versions: %d", len(vs))
	}
	m := vs[0].(map[string]any)
	for _, k := range []string{"version_key", "valid_plays", "duration_ms", "computed_at", "sufficient", "retention", "viewers",
		"replays", "replay_rate", "replay_peak", "skips", "skip_rate", "intro_skip_suggested"} {
		if _, ok := m[k]; !ok {
			t.Errorf("main: %s missing", k)
		}
	}
	ret := m["retention"].([]any)
	if len(ret) != 200 || ret[0].(float64) != 1 || ret[100].(float64) != 0.75 {
		t.Errorf("retention: %v %v", ret[0], ret[100])
	}
	if rr := m["replay_rate"].([]any); rr[100].(float64) != 0.3333 {
		t.Errorf("replay_rate[100] = %v", rr[100])
	}
	if sr := m["skip_rate"].([]any); sr[3].(float64) != 0.3 {
		t.Errorf("skip_rate[3] = %v", sr[3])
	}
	if m["sufficient"] != true || m["intro_skip_suggested"] != true || m["duration_ms"].(float64) != 600000 {
		t.Errorf("main = %v", m)
	}
	a := vs[1].(map[string]any)
	if a["sufficient"] != false || a["replay_peak"] != nil || a["skips"] != nil || a["skip_rate"] != nil || a["duration_ms"] != nil {
		t.Errorf("alt = %v", a)
	}
	if v := a["viewers"].([]any); len(v) != 200 || v[0].(float64) != 10 {
		t.Errorf("alt viewers = %v", a["viewers"])
	}

	// No curves yet: empty versions, not null.
	rec, body = doCurves(t, stubCurves{}, "/api/admin/releases/7/curves")
	if rec.Code != http.StatusOK || body["versions"] == nil || len(body["versions"].([]any)) != 0 || body["intro_skip_suggested"] != false {
		t.Fatalf("empty: %d %v", rec.Code, body)
	}
	if rec, _ = doCurves(t, stubCurves{err: pgx.ErrNoRows}, "/api/admin/releases/7/curves"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown release: %d", rec.Code)
	}
	if rec, _ = doCurves(t, stubCurves{}, "/api/admin/releases/x/curves"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: %d", rec.Code)
	}
	if rec, _ = doCurves(t, stubCurves{err: errors.New("boom")}, "/api/admin/releases/7/curves"); rec.Code != http.StatusInternalServerError {
		t.Errorf("store error: %d", rec.Code)
	}
}
