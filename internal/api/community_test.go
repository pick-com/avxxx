package api

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// stubCommunity is an in-memory communityStore.
type stubCommunity struct {
	poster    store.Poster
	words     []community.Keyword
	created   []store.NewDanmaku
	reviews   []string // statuses passed to CreateReview
	votes     []community.Voter
	rangeArgs []int64 // viewer ids passed to DanmakuRange
	reviewErr error
	calls     []string
}

func (s *stubCommunity) rec(n string) { s.calls = append(s.calls, n) }

func (s *stubCommunity) ReleaseIDBySlug(_ context.Context, slug string) (int64, error) {
	if slug != "abc-123" {
		return 0, pgx.ErrNoRows
	}
	return 10, nil
}
func (s *stubCommunity) PosterInfo(context.Context, int64) (store.Poster, error) {
	return s.poster, nil
}
func (s *stubCommunity) ModerationMatcher(context.Context) (*community.Matcher, error) {
	return community.NewMatcher(s.words), nil
}
func (s *stubCommunity) DanmakuTimelineFor(_ context.Context, slug string, sourceID int64) (store.DanmakuTimeline, error) {
	if slug != "abc-123" {
		return store.DanmakuTimeline{}, pgx.ErrNoRows
	}
	if sourceID == 99 {
		return store.DanmakuTimeline{}, store.ErrUnknownSource
	}
	return store.DanmakuTimeline{ReleaseID: 10, VersionKey: "main", DurationMS: 600_000}, nil
}
func (s *stubCommunity) DanmakuRange(_ context.Context, _ store.DanmakuTimeline, from, to int, viewer int64) ([]store.Danmaku, error) {
	s.rangeArgs = append(s.rangeArgs, viewer)
	return []store.Danmaku{{ID: 1, TMs: from, Body: "hi"}}, nil
}
func (s *stubCommunity) CreateDanmaku(_ context.Context, d store.NewDanmaku) (store.Danmaku, error) {
	s.created = append(s.created, d)
	return store.Danmaku{ID: 5, TMs: d.TMs, Body: d.Body, Mine: true}, nil
}
func (s *stubCommunity) RatingSummary(_ context.Context, _ int64, voterKey string) (store.RatingSummary, error) {
	out := store.RatingSummary{Count: 3, Insufficient: true}
	if voterKey != "" {
		v := 4
		out.MyScore = &v
	}
	return out, nil
}
func (s *stubCommunity) RateRelease(_ context.Context, _ int64, v community.Voter, _ int) error {
	s.votes = append(s.votes, v)
	return nil
}
func (s *stubCommunity) Reviews(context.Context, store.ReviewFilter) ([]store.Review, string, error) {
	s.rec("Reviews")
	return []store.Review{}, "", nil
}
func (s *stubCommunity) CreateReview(_ context.Context, _, _ int64, _ string, _ bool, status string) (store.Review, error) {
	s.reviews = append(s.reviews, status)
	return store.Review{ID: 3}, s.reviewErr
}
func (s *stubCommunity) UpdateReview(_ context.Context, id, _ int64, _ store.ReviewEdit) (store.Review, error) {
	if id == 404 {
		return store.Review{}, pgx.ErrNoRows
	}
	if id == 403 {
		return store.Review{}, store.ErrNotOwner
	}
	return store.Review{ID: id}, nil
}
func (s *stubCommunity) DeleteReview(context.Context, int64, int64) error { return nil }
func (s *stubCommunity) SetReviewLike(context.Context, int64, int64, bool) (int, error) {
	return 1, nil
}
func (s *stubCommunity) CreateReport(_ context.Context, tt string, id, _ int64, _, _ string) (store.Report, bool, error) {
	s.rec("CreateReport:" + tt)
	if id == 404 {
		return store.Report{}, false, store.ErrTargetNotFound
	}
	return store.Report{ID: 1, Status: "open"}, id != 2, nil
}

// stubModeration is an in-memory moderationStore.
type stubModeration struct {
	actions []store.ModerationAction
	err     error
}

func (s *stubModeration) ModerationCounts(context.Context) (store.QueueCounts, error) {
	return store.QueueCounts{Reports: 1}, nil
}
func (s *stubModeration) ModerationQueue(context.Context, store.QueueFilter) (any, string, error) {
	return []any{}, "", nil
}
func (s *stubModeration) Moderate(_ context.Context, a store.ModerationAction) (store.ModerationResult, error) {
	s.actions = append(s.actions, a)
	return store.ModerationResult{ID: a.ID}, s.err
}
func (s *stubModeration) ResolveReport(_ context.Context, id int64, _, _, _ string) (store.Report, error) {
	if id == 9 {
		return store.Report{}, store.ErrReportClosed
	}
	return store.Report{ID: id}, nil
}
func (s *stubModeration) RemoveRatingFlood(context.Context, int64, int, time.Time, string, string) (int64, error) {
	return 20, nil
}
func (s *stubModeration) ModerationWords(context.Context) ([]community.Keyword, error) {
	return []community.Keyword{}, nil
}
func (s *stubModeration) AddModerationWord(_ context.Context, w, c, _ string) (community.Keyword, error) {
	return community.Keyword{Word: w, Category: c}, nil
}
func (s *stubModeration) DeleteModerationWord(context.Context, string, string) error { return nil }
func (s *stubModeration) Unmute(context.Context, int64, string, string) error        { return nil }

func newCommunityServer(c *stubCommunity, m *stubModeration, lim community.Limiter) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, community: c, moderation: m, limiter: lim, adminToken: "admintok"}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func doH(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var oldPoster = store.Poster{UserID: 1, AccountAge: 30 * 24 * time.Hour}

func TestDanmakuList(t *testing.T) {
	c := &stubCommunity{}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	rec := doH(h, "GET", "/api/releases/abc-123/danmaku?from_ms=61000&to_ms=150000", "", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["from_ms"] != 60000.0 || body["to_ms"] != 180000.0 || body["segment_ms"] != 60000.0 {
		t.Errorf("window not segment-aligned: %v", body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("anonymous read must be cacheable: %q", cc)
	}
	rec = doH(h, "GET", "/api/releases/abc-123/danmaku", "", map[string]string{"Authorization": "Bearer good"})
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("signed-in read must be private: %q", cc)
	}
	if c.rangeArgs[0] != 0 || c.rangeArgs[1] != 1 {
		t.Errorf("viewer ids %v", c.rangeArgs)
	}
	for path, want := range map[string]int{
		"/api/releases/nope/danmaku":                           404,
		"/api/releases/abc-123/danmaku?from_ms=-1":             400,
		"/api/releases/abc-123/danmaku?from_ms=0&to_ms=0":      400,
		"/api/releases/abc-123/danmaku?from_ms=0&to_ms=9e9":    400,
		"/api/releases/abc-123/danmaku?to_ms=600000":           400, // > 5 segments
		"/api/releases/abc-123/danmaku?source_id=99":           400,
		"/api/releases/abc-123/danmaku?source=x":               400,
		"/api/releases/abc-123/danmaku?from_ms=0&to_ms=300000": 200,
	} {
		if rec := doH(h, "GET", path, "", nil); rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}
}

func TestDanmakuPost(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer good"}
	c := &stubCommunity{poster: oldPoster, words: []community.Keyword{{Word: "加微信", Category: "ad"}}}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	path := "/api/releases/abc-123/danmaku"
	if rec := doH(h, "POST", path, `{"t_ms":1000,"body":"hi"}`, nil); rec.Code != 401 {
		t.Errorf("anonymous post: %d", rec.Code)
	}
	for body, want := range map[string]int{
		`{"body":"hi"}`:           400, // t_ms missing
		`{"t_ms":1000,"body":""}`: 400,
		`{"t_ms":1000,"body":"` + strings.Repeat("弹", 51) + `"}`: 400,
		`{"t_ms":1000,"body":"hi","color":"red"}`:                400,
		`{"t_ms":1000,"body":"hi","mode":"left"}`:                400,
		`{"t_ms":9999999,"body":"hi"}`:                           400, // past the end
		`{"t_ms":1000,"body":"hi","source_id":99}`:               400,
		`{"t_ms":1000,"body":"hi","extra":1}`:                    400,
		`{"t_ms":1000,"body":"` + strings.Repeat("弹", 50) + `"}`: 201,
	} {
		if rec := doH(h, "POST", path, body, auth); rec.Code != want {
			t.Errorf("POST %s: %d, want %d (%s)", body, rec.Code, want, rec.Body)
		}
	}
	rec := doH(h, "POST", path, `{"t_ms":1000,"body":"快来 加 微 信","color":"#ff0000"}`, auth)
	if rec.Code != 201 {
		t.Fatalf("keyword hit still looks posted: %d", rec.Code)
	}
	last := c.created[len(c.created)-1]
	if last.Status != community.StatusShadow || last.Color != "#FF0000" || last.Mode != "scroll" {
		t.Errorf("keyword hit must be shadow with normalized fields: %+v", last)
	}
	if c.created[0].Status != community.StatusVisible {
		t.Errorf("clean post must be visible: %+v", c.created[0])
	}
}

func TestDanmakuPostLimitsAndMutes(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer good"}
	path := "/api/releases/abc-123/danmaku"
	// New account (< 3 days): 3 per minute.
	c := &stubCommunity{poster: store.Poster{UserID: 1, AccountAge: time.Hour}}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	for i := range 3 {
		if rec := doH(h, "POST", path, `{"t_ms":1000,"body":"hi"}`, auth); rec.Code != 201 {
			t.Fatalf("post %d: %d", i, rec.Code)
		}
	}
	if rec := doH(h, "POST", path, `{"t_ms":1000,"body":"hi"}`, auth); rec.Code != 429 {
		t.Fatalf("4th post by a new account: %d, want 429", rec.Code)
	}
	// Older accounts get more.
	c = &stubCommunity{poster: oldPoster}
	h = newCommunityServer(c, nil, community.NewMemoryLimiter())
	for i := range 4 {
		if rec := doH(h, "POST", path, `{"t_ms":1000,"body":"hi"}`, auth); rec.Code != 201 {
			t.Fatalf("post %d by an old account: %d", i, rec.Code)
		}
	}
	// Muted: 403, nothing stored.
	until := time.Now().Add(time.Hour)
	c = &stubCommunity{poster: store.Poster{UserID: 1, AccountAge: 30 * 24 * time.Hour, MutedUntil: &until}}
	h = newCommunityServer(c, nil, community.NewMemoryLimiter())
	if rec := doH(h, "POST", path, `{"t_ms":1000,"body":"hi"}`, auth); rec.Code != 403 || len(c.created) != 0 {
		t.Fatalf("muted post: %d created=%d", rec.Code, len(c.created))
	}
	if rec := doH(h, "POST", "/api/releases/abc-123/reviews", `{"body":"good"}`, auth); rec.Code != 403 {
		t.Fatalf("muted review: %d", rec.Code)
	}
}

func TestRatingRoutes(t *testing.T) {
	c := &stubCommunity{poster: oldPoster}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	path := "/api/releases/abc-123/rating"
	if rec := doH(h, "PUT", path, `{"score":4}`, nil); rec.Code != 400 {
		t.Errorf("no identity: %d", rec.Code)
	}
	if rec := doH(h, "PUT", path, `{"score":4}`, map[string]string{"X-Device-ID": "bad id"}); rec.Code != 400 {
		t.Errorf("invalid device: %d", rec.Code)
	}
	if rec := doH(h, "PUT", path, `{"score":4}`, map[string]string{"Authorization": "Bearer bad", "X-Device-ID": "device-123456"}); rec.Code != 401 {
		t.Errorf("bad token must not fall back to the device: %d", rec.Code)
	}
	for _, body := range []string{`{"score":0}`, `{"score":6}`, `{}`, `{"score":"5"}`} {
		if rec := doH(h, "PUT", path, body, map[string]string{"X-Device-ID": "device-123456"}); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	if rec := doH(h, "PUT", "/api/releases/nope/rating", `{"score":4}`, map[string]string{"X-Device-ID": "device-123456"}); rec.Code != 404 {
		t.Errorf("unknown release: %d", rec.Code)
	}
	rec := doH(h, "PUT", path, `{"score":4}`, map[string]string{"X-Device-ID": "device-123456"})
	if rec.Code != 200 || c.votes[0].UserID != 0 || c.votes[0].DeviceID != "device-123456" {
		t.Fatalf("device vote: %d %+v", rec.Code, c.votes)
	}
	rec = doH(h, "PUT", path, `{"score":5}`, map[string]string{"Authorization": "Bearer good"})
	if rec.Code != 200 || c.votes[1].UserID != 1 || community.RatingWeight(c.votes[1]) != community.WeightFull {
		t.Fatalf("user vote: %d %+v", rec.Code, c.votes)
	}
	var sum map[string]any
	json.Unmarshal(rec.Body.Bytes(), &sum)
	if sum["score"] != nil || sum["insufficient"] != true || sum["my_score"] != 4.0 {
		t.Errorf("summary %v", sum)
	}
	rec = doH(h, "GET", path, "", nil)
	json.Unmarshal(rec.Body.Bytes(), &sum)
	if rec.Code != 200 || sum["my_score"] != nil {
		t.Errorf("anonymous GET: %d %v", rec.Code, sum)
	}
}

func TestReviewRoutes(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer good"}
	c := &stubCommunity{poster: oldPoster, words: []community.Keyword{{Word: "剧透", Category: "spoiler"}}}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	for path, want := range map[string]int{
		"/api/releases/abc-123/reviews":                200,
		"/api/releases/abc-123/reviews?sort=newest":    200,
		"/api/releases/abc-123/reviews?sort=finished":  200,
		"/api/releases/abc-123/reviews?sort=hot":       400,
		"/api/releases/abc-123/reviews?cursor=garbage": 400,
		"/api/releases/abc-123/reviews?limit=-1":       400,
		"/api/releases/nope/reviews":                   404,
	} {
		if rec := doH(h, "GET", path, "", nil); rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}
	if rec := doH(h, "POST", "/api/releases/abc-123/reviews", `{"body":"`+strings.Repeat("好", 141)+`"}`, auth); rec.Code != 400 {
		t.Errorf("141 chars: %d", rec.Code)
	}
	if rec := doH(h, "POST", "/api/releases/abc-123/reviews", `{"body":"`+strings.Repeat("好", 140)+`","spoiler":true}`, auth); rec.Code != 201 {
		t.Errorf("140 chars: %d", rec.Code)
	}
	if rec := doH(h, "POST", "/api/releases/abc-123/reviews", `{"body":"结局剧透了"}`, auth); rec.Code != 201 || c.reviews[1] != community.StatusShadow {
		t.Errorf("keyword review: %d %v", rec.Code, c.reviews)
	}
	c.reviewErr = &pgconn.PgError{Code: "23505"}
	if rec := doH(h, "POST", "/api/releases/abc-123/reviews", `{"body":"again"}`, auth); rec.Code != 409 {
		t.Errorf("second review: %d", rec.Code)
	}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"PATCH", "/api/reviews/5", `{}`, 400},
		{"PATCH", "/api/reviews/5", `{"spoiler":true}`, 200},
		{"PATCH", "/api/reviews/404", `{"spoiler":true}`, 404},
		{"PATCH", "/api/reviews/403", `{"body":"x"}`, 403},
		{"PATCH", "/api/reviews/abc", `{"body":"x"}`, 400},
		{"DELETE", "/api/reviews/5", "", 200},
		{"PUT", "/api/reviews/5/like", "", 200},
		{"DELETE", "/api/reviews/5/like", "", 200},
		{"POST", "/api/reviews/5/report", `{"reason":"spoiler"}`, 201},
		{"POST", "/api/reviews/2/report", `{"reason":"spoiler"}`, 200}, // already reported
		{"POST", "/api/reviews/404/report", `{"reason":"spoiler"}`, 404},
		{"POST", "/api/reviews/5/report", `{"reason":"meh"}`, 400},
		{"POST", "/api/reviews/5/report", `{"reason":"spam","target_type":"danmaku"}`, 400},
		{"POST", "/api/danmaku/5/report", `{"reason":"spam"}`, 201},
		{"POST", "/api/reports", `{"target_type":"source","target_id":3,"reason":"broken"}`, 201},
		{"POST", "/api/reports", `{"target_type":"user","target_id":3,"reason":"spam"}`, 400},
		{"POST", "/api/reports", `{"target_type":"release","target_id":3,"reason":"spam","note":"` + strings.Repeat("x", 501) + `"}`, 400},
	} {
		if rec := doH(h, tc.method, tc.path, tc.body, auth); rec.Code != tc.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", tc.method, tc.path, tc.body, rec.Code, tc.want, rec.Body)
		}
	}
}

func TestCommunityWritesRequireUser(t *testing.T) {
	c := &stubCommunity{poster: oldPoster}
	h := newCommunityServer(c, nil, community.NewMemoryLimiter())
	nilStore := New(nil, nil)
	srv := &Server{}
	for _, rt := range srv.communityRoutes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if method == "GET" || strings.HasSuffix(path, "/rating") {
			continue // public
		}
		path = samplePath(path)
		for _, tok := range []string{"", "bad"} {
			hdr := map[string]string{}
			if tok != "" {
				hdr["Authorization"] = "Bearer " + tok
			}
			if rec := doH(h, method, path, `{"t_ms":1,"body":"x","reason":"spam"}`, hdr); rec.Code != 401 {
				t.Errorf("%s token=%q: %d", rt.pattern, tok, rec.Code)
			}
		}
		if rec := doH(nilStore, method, path, "{}", map[string]string{"Authorization": "Bearer good"}); rec.Code != 401 {
			t.Errorf("%s without store: %d", rt.pattern, rec.Code)
		}
	}
	if len(c.created) != 0 || len(c.calls) != 0 {
		t.Errorf("store called without auth: %v", c.calls)
	}
	// Public routes without a store: 503, never a panic.
	for _, p := range []string{"/api/releases/x/danmaku", "/api/releases/x/rating", "/api/releases/x/reviews"} {
		if rec := doH(nilStore, "GET", p, "", nil); rec.Code != 503 {
			t.Errorf("GET %s without store: %d", p, rec.Code)
		}
	}
}

func TestModerationRoutes(t *testing.T) {
	m := &stubModeration{}
	h := newCommunityServer(&stubCommunity{}, m, nil)
	admin := map[string]string{"Authorization": "Bearer admintok", "X-Admin-User": "alice"}
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/api/admin/moderation/queue", "", 200},
		{"GET", "/api/admin/moderation/queue?kind=reports", "", 200},
		{"GET", "/api/admin/moderation/queue?kind=danmaku&status=shadow", "", 200},
		{"GET", "/api/admin/moderation/queue?kind=nope", "", 400},
		{"POST", "/api/admin/moderation/danmaku/7/action", `{"action":"fold"}`, 200},
		{"POST", "/api/admin/moderation/danmaku/7/action", `{"action":"delete_mute","mute_days":3,"reason":"ads"}`, 200},
		{"POST", "/api/admin/moderation/danmaku/7/action", `{"action":"delete","mute_days":3}`, 400},
		{"POST", "/api/admin/moderation/danmaku/7/action", `{"action":"delete_mute","mute_days":999}`, 400},
		{"POST", "/api/admin/moderation/danmaku/7/action", `{"action":"ban"}`, 400},
		{"POST", "/api/admin/moderation/danmaku/x/action", `{"action":"fold"}`, 400},
		{"POST", "/api/admin/moderation/reviews/8/action", `{"action":"approve"}`, 200},
		{"POST", "/api/admin/moderation/ratings/9/action", `{"action":"fold"}`, 400},
		{"POST", "/api/admin/moderation/ratings/9/action", `{"action":"delete"}`, 200},
		{"POST", "/api/admin/moderation/reports/1/resolve", `{"status":"dismissed"}`, 200},
		{"POST", "/api/admin/moderation/reports/9/resolve", `{"status":"resolved"}`, 409},
		{"POST", "/api/admin/moderation/reports/1/resolve", `{"status":"open"}`, 400},
		{"POST", "/api/admin/moderation/rating-floods/remove", `{"release_id":1,"score":5,"window_start":"2026-10-01T10:00:00Z"}`, 200},
		{"POST", "/api/admin/moderation/rating-floods/remove", `{"release_id":1,"score":5}`, 400},
		{"GET", "/api/admin/moderation/words", "", 200},
		{"POST", "/api/admin/moderation/words", `{"word":"加微信","category":"ad"}`, 200},
		{"POST", "/api/admin/moderation/words", `{"word":"!!!","category":"ad"}`, 400},
		{"POST", "/api/admin/moderation/words", `{"word":"x","category":"weird"}`, 400},
		{"DELETE", "/api/admin/moderation/words/abc", "", 200},
		{"DELETE", "/api/admin/moderation/mutes/5", "", 200},
		{"DELETE", "/api/admin/moderation/mutes/x", "", 400},
	} {
		if rec := doH(h, tc.method, tc.path, tc.body, admin); rec.Code != tc.want {
			t.Errorf("%s %s %s: %d, want %d (%s)", tc.method, tc.path, tc.body, rec.Code, tc.want, rec.Body)
		}
	}
	if len(m.actions) != 4 {
		t.Fatalf("actions %d", len(m.actions))
	}
	if a := m.actions[1]; a.Actor != "alice" || a.MuteDays != 3 || a.Action != "delete_mute" || a.TargetType != "danmaku" || a.Reason != "ads" {
		t.Errorf("action %+v", a)
	}
	if m.actions[2].TargetType != "review" || m.actions[3].TargetType != "rating" {
		t.Errorf("target types %+v", m.actions)
	}
	// Default actor and token check.
	h2 := newCommunityServer(&stubCommunity{}, m, nil)
	doH(h2, "POST", "/api/admin/moderation/danmaku/7/action", `{"action":"fold"}`, map[string]string{"Authorization": "Bearer admintok"})
	if m.actions[len(m.actions)-1].Actor != "admin" {
		t.Error("default actor")
	}
	if rec := doH(h2, "GET", "/api/admin/moderation/queue", "", nil); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	m.err = pgx.ErrNoRows
	if rec := doH(h2, "POST", "/api/admin/moderation/danmaku/7/action", `{"action":"fold"}`, admin); rec.Code != 404 {
		t.Errorf("missing target: %d", rec.Code)
	}
}

func TestPlayConfigCurve(t *testing.T) {
	st := newStub()
	curve := make([]int16, 200)
	curve[10] = 255
	st.curves = map[string][]int16{"main": curve, "alt:13": curve}
	rec, body := doPlayConfig(t, st, "/api/releases/juq-743/play-config", "")
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	c, ok := body["danmaku_curve"].([]any)
	if !ok || len(c) != 200 || c[10] != 255.0 {
		t.Fatalf("danmaku_curve %v", body["danmaku_curve"])
	}
	alt := body["danmaku"].(map[string]any)["alt_curves"].(map[string]any)
	if _, ok := alt["13"]; !ok {
		t.Errorf("alt curve for source 13 (8000 s vs 7200 s main) missing: %v", alt)
	}
	st.curves = nil
	_, body = doPlayConfig(t, st, "/api/releases/juq-743/play-config", "")
	if v, present := body["danmaku_curve"]; !present || v != nil {
		t.Errorf("no curve must be null, got %v", v)
	}
}
