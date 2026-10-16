package api

import (
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type stubRecs struct {
	gotQuery   store.RecQuery
	gotIDs     []int64
	recall     store.RecallResult
	infos      map[int64]recs.Info
	weights    store.SimilarityWeightsRow
	setWeights *recs.Weights
	enqueued   []string
	perfRecs   []store.PerformerRec
	heat       []store.BoardRow
	gotTag     string
}

func (s *stubRecs) ReleaseIDBySlug(_ context.Context, slug string) (int64, error) {
	if slug == "known" {
		return 1, nil
	}
	return 0, pgx.ErrNoRows
}
func (s *stubRecs) PerformerIDBySlug(_ context.Context, slug string) (int64, error) {
	if slug == "known" {
		return 7, nil
	}
	return 0, pgx.ErrNoRows
}
func (s *stubRecs) Recall(_ context.Context, q store.RecQuery) (store.RecallResult, error) {
	s.gotQuery = q
	return s.recall, nil
}
func (s *stubRecs) RecInfos(_ context.Context, ids []int64, _ int64, _ string) (map[int64]recs.Info, error) {
	s.gotIDs = ids
	out := map[int64]recs.Info{}
	for _, id := range ids {
		if x, ok := s.infos[id]; ok {
			out[id] = x
		}
	}
	return out, nil
}
func (s *stubRecs) TrendingTags(context.Context, string, int) ([]store.TagCount, error) {
	return []store.TagCount{{Slug: "drama", Name: "剧情", Count: 3}}, nil
}
func (s *stubRecs) TrendingBoard(_ context.Context, _, tag string, _, _ int) ([]store.BoardRow, error) {
	s.gotTag = tag
	prev := 5
	return []store.BoardRow{{Rank: 1, PrevRank: &prev, ID: 2, Score: 3, ComputedAt: time.Now()}, {Rank: 2, ID: 99}}, nil
}
func (s *stubRecs) PerformerHeatBoard(context.Context, string, int, int) ([]store.BoardRow, error) {
	return s.heat, nil
}
func (s *stubRecs) SimilarPerformers(context.Context, int64, int, int) ([]store.PerformerRec, error) {
	return []store.PerformerRec{{ID: 8, Slug: "b", Name: "B", Score: 0.92, Match: 92, Evidence: &recs.Evidence{CoStar: 6, CoWatch: 0.5}}}, nil
}
func (s *stubRecs) RecommendedPerformers(context.Context, int64, int, int) ([]store.PerformerRec, error) {
	return s.perfRecs, nil
}
func (s *stubRecs) SimilarityWeightsRow(context.Context) (store.SimilarityWeightsRow, error) {
	return s.weights, nil
}
func (s *stubRecs) SetSimilarityWeights(_ context.Context, w recs.Weights, _ string) (store.SimilarityWeightsRow, error) {
	s.setWeights = &w
	return store.SimilarityWeightsRow{Weights: w}, nil
}
func (s *stubRecs) EnqueueSourceless(_ context.Context, kind string) (int64, bool, error) {
	s.enqueued = append(s.enqueued, kind)
	return int64(len(s.enqueued)), true, nil
}

func newStubRecs() *stubRecs {
	st := &stubRecs{weights: store.SimilarityWeightsRow{Weights: recs.DefaultWeights}, infos: map[int64]recs.Info{}}
	for id := int64(2); id <= 8; id++ {
		st.infos[id] = recs.Info{ReleaseID: id, Slug: "r" + idText(id), Title: "T" + idText(id), Playable: true, PerformerID: 50 + id%2, PerformerName: "P", RuntimeMinutes: 20}
	}
	st.recall.Candidates = []recs.Candidate{
		{ReleaseID: 2, Channel: recs.ReasonCoWatch, Raw: 1},
		{ReleaseID: 3, Channel: recs.ChanTrending, Raw: 1, Label: "week", BoardRank: 1},
		{ReleaseID: 4, Channel: recs.ChanNew, Raw: 1},
		{ReleaseID: 5, Channel: recs.ChanNew, Raw: 0.9},
		{ReleaseID: 6, Channel: recs.ChanNew, Raw: 0.8},
		{ReleaseID: 99, Channel: recs.ChanNew, Raw: 0.7}, // not published: dropped
	}
	st.recall.Watched = map[int64]bool{}
	return st
}

func recsMux(st *stubRecs) http.Handler {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins(), recs: st, recsAdmin: st, accounts: &stubAccounts{}}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestRecsStatusCodes(t *testing.T) {
	for _, c := range []struct {
		method, path, token, body string
		want                      int
	}{
		{"GET", "/api/recs", "", "", 400},
		{"GET", "/api/recs?slot=nope", "", "", 400},
		{"GET", "/api/recs?slot=related", "", "", 400},
		{"GET", "/api/recs?slot=related&release=missing", "", "", 404},
		{"GET", "/api/recs?slot=related&release=known", "", "", 200},
		{"GET", "/api/recs?slot=similar_performers", "", "", 400},
		{"GET", "/api/recs?slot=similar_performers&performer=missing", "", "", 404},
		{"GET", "/api/recs?slot=similar_performers&performer=known", "", "", 200},
		{"GET", "/api/recs?slot=performers", "", "", 200},
		{"GET", "/api/recs?slot=home", "", "", 200},
		{"GET", "/api/recs?slot=home", "bad-token", "", 200}, // invalid token = visitor
		{"GET", "/api/recs?slot=home&limit=-1", "", "", 400},
		{"GET", "/api/recs?slot=home&cursor=zzz", "", "", 400},
		{"GET", "/api/recs?slot=home&period=decade", "", "", 400},
		{"GET", "/api/recs?slot=home&lang=xx", "", "", 400},
		{"GET", "/api/recs?slot=tonight&duration=45", "", "", 400},
		{"GET", "/api/recs?slot=tonight&exclude=1,x", "", "", 400},
		{"GET", "/api/recs?slot=tonight&exclude=2,3&duration=30", "good", "", 200},
		{"GET", "/api/trending", "", "", 200},
		{"GET", "/api/trending?period=year", "", "", 200},
		{"GET", "/api/trending?period=decade", "", "", 400},
		{"GET", "/api/trending/performers?period=today", "", "", 200},
		{"GET", "/api/admin/similarity-weights", "", "", 401},
		{"GET", "/api/admin/similarity-weights", "secret", "", 200},
		{"PUT", "/api/admin/similarity-weights", "secret", "", 400},
		{"PUT", "/api/admin/similarity-weights", "secret", `{"metadata":2}`, 400},
		{"PUT", "/api/admin/similarity-weights", "secret", `{"metadata":0,"co_star":0,"co_watch":0}`, 400},
		{"PUT", "/api/admin/similarity-weights", "secret", `{"bogus":1}`, 400},
		{"PUT", "/api/admin/similarity-weights", "secret", `{"co_watch":0.7}`, 200},
		{"POST", "/api/admin/recs/rebuild", "secret", "", 202},
		{"POST", "/api/admin/recs/rebuild?job=trending", "secret", "", 202},
		{"POST", "/api/admin/recs/rebuild?job=bogus", "secret", "", 400},
	} {
		rec := do(recsMux(newStubRecs()), c.method, c.path, c.token, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s (token %q): %d, want %d: %s", c.method, c.path, c.token, rec.Code, c.want, rec.Body)
		}
	}
}

func TestRecsUnavailableWithoutStore(t *testing.T) {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	for _, p := range []string{"/api/recs?slot=home", "/api/trending"} {
		if rec := do(mux, "GET", p, "", ""); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", p, rec.Code)
		}
	}
	if rec := do(mux, "POST", "/api/admin/recs/rebuild", "secret", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("rebuild: %d, want 503", rec.Code)
	}
}

func TestRecsHomeResponse(t *testing.T) {
	st := newStubRecs()
	rec := do(recsMux(st), "GET", "/api/recs?slot=home&limit=2", "good", "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if st.gotQuery.UserID != 1 || st.gotQuery.Slot != "home" || st.gotQuery.Period != "week" {
		t.Errorf("query = %+v", st.gotQuery)
	}
	var body struct {
		Items []struct {
			ID     int64       `json:"id"`
			Slug   string      `json:"slug"`
			Reason recs.Reason `json:"reason"`
			Perf   *struct {
				ID int64 `json:"id"`
			} `json:"performer"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
		Slot string  `json:"slot"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 2 || body.Items[0].ID != 2 || body.Items[0].Reason.Text != "看过此视频的人也看了" || body.Items[0].Perf == nil || body.Next == nil {
		t.Fatalf("body = %s", rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("logged-in recs must not be cached publicly")
	}
	// Second page via the cursor; 99 (unpublished) never appears.
	rec = do(recsMux(st), "GET", "/api/recs?slot=home&limit=2&cursor="+*body.Next, "", "")
	if !strings.Contains(rec.Body.String(), `"id":4`) || strings.Contains(rec.Body.String(), `"id":99`) {
		t.Errorf("page 2 = %s", rec.Body)
	}
}

func TestRecsWatchedFilterAndTonight(t *testing.T) {
	st := newStubRecs()
	st.recall.Watched = map[int64]bool{2: true}
	st.infos[4] = recs.Info{ReleaseID: 4, Playable: true, RuntimeMinutes: 120}
	rec := do(recsMux(st), "GET", "/api/recs?slot=tonight&duration=30&exclude=3", "good", "")
	var body struct {
		Item *struct {
			ID int64 `json:"id"`
		} `json:"item"`
		Alternates []struct {
			ID int64 `json:"id"`
		} `json:"alternates"`
		Exclude []int64 `json:"exclude"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// 2 watched, 3 excluded, 4 too long for "30", 99 unpublished; 5 shares
	// the excluded 3's performer so 换一个 demotes it below 6.
	if body.Item == nil || body.Item.ID != 6 || len(body.Alternates) != 1 || body.Alternates[0].ID != 5 || len(body.Exclude) != 1 {
		t.Fatalf("tonight = %s", rec.Body)
	}
	if !containsID(st.gotIDs, 3) {
		t.Errorf("excluded ids must be hydrated for demotion: %v", st.gotIDs)
	}
	// Nothing left: item null, alternates [].
	rec = do(recsMux(st), "GET", "/api/recs?slot=tonight&exclude=3,4,5,6,7,8", "good", "")
	if !strings.Contains(rec.Body.String(), `"item":null`) || !strings.Contains(rec.Body.String(), `"alternates":[]`) {
		t.Errorf("empty tonight = %s", rec.Body)
	}
}

func containsID(xs []int64, id int64) bool {
	for _, x := range xs {
		if x == id {
			return true
		}
	}
	return false
}

func TestRecsPerformerSlots(t *testing.T) {
	st := newStubRecs()
	rec := do(recsMux(st), "GET", "/api/recs?slot=similar_performers&performer=known", "", "")
	if !strings.Contains(rec.Body.String(), `"match_percent":92`) || !strings.Contains(rec.Body.String(), "共演 6 部、观众高度重合") {
		t.Errorf("similar = %s", rec.Body)
	}
	// Visitor: heat board fallback.
	st.heat = []store.BoardRow{{Rank: 1, ID: 9, Slug: "h", Name: "H", Score: 1}}
	rec = do(recsMux(st), "GET", "/api/recs?slot=performers", "", "")
	if !strings.Contains(rec.Body.String(), "hot_performer") || !strings.Contains(rec.Body.String(), "本周热门演员 · 第 1 名") {
		t.Errorf("visitor performers = %s", rec.Body)
	}
	st.perfRecs = []store.PerformerRec{{ID: 3, Slug: "c", Name: "C", ViaID: 4, ViaName: "D", ViaFollow: true}}
	rec = do(recsMux(st), "GET", "/api/recs?slot=performers", "good", "")
	if !strings.Contains(rec.Body.String(), "与你关注的 D 相似") {
		t.Errorf("user performers = %s", rec.Body)
	}
}

func TestTrendingResponse(t *testing.T) {
	st := newStubRecs()
	st.heat = []store.BoardRow{{Rank: 1, ID: 9, Slug: "h", Name: "H"}}
	rec := do(recsMux(st), "GET", "/api/trending?period=today", "", "")
	s := rec.Body.String()
	// 99 is not published: skipped. Movement up 4 for rank 1 from 5.
	if !strings.Contains(s, `"movement":"up"`) || !strings.Contains(s, `"delta":4`) || strings.Contains(s, `"id":99`) ||
		!strings.Contains(s, "今日热门 · 第 1 名") || !strings.Contains(s, `"performers":[`) || !strings.Contains(s, `"movement":"new"`) {
		t.Errorf("trending = %s", s)
	}
}

func TestSimilarityWeightsPut(t *testing.T) {
	st := newStubRecs()
	rec := do(recsMux(st), "PUT", "/api/admin/similarity-weights", "secret", `{"co_watch":0.7,"visual":0.1}`)
	if rec.Code != 200 || st.setWeights == nil {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	want := recs.Weights{Metadata: 0.35, CoStar: 0.25, CoWatch: 0.7, Visual: 0.1}
	if *st.setWeights != want {
		t.Errorf("saved %+v, want %+v (omitted fields kept)", *st.setWeights, want)
	}
	if !strings.Contains(rec.Body.String(), `"effective_from":"next_nightly_run"`) {
		t.Errorf("body = %s", rec.Body)
	}
	st = newStubRecs()
	do(recsMux(st), "POST", "/api/admin/recs/rebuild", "secret", "")
	if strings.Join(st.enqueued, ",") != "recs_similarity,recs_trending" {
		t.Errorf("enqueued %v", st.enqueued)
	}
}
