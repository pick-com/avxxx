package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubSocial is an in-memory socialStore. err is returned by every mutating
// call; paused drives HistoryPaused.
type stubSocial struct {
	err    error
	paused bool
	calls  []string
}

func (s *stubSocial) rec(name string) { s.calls = append(s.calls, name) }

func (s *stubSocial) Follow(_ context.Context, _ int64, slug, mode string) (map[string]any, error) {
	s.rec("Follow:" + mode)
	if s.err != nil {
		return nil, s.err
	}
	return map[string]any{"performer_slug": slug, "notify_mode": mode}, nil
}
func (s *stubSocial) Unfollow(context.Context, int64, string) error { s.rec("Unfollow"); return s.err }
func (s *stubSocial) Follows(context.Context, int64) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
func (s *stubSocial) FollowingFeedFor(context.Context, int64, string, *store.Cursor, int) ([]map[string]any, string, error) {
	s.rec("FollowingFeed")
	return []map[string]any{}, "", nil
}
func (s *stubSocial) Reserve(context.Context, int64, string) error { s.rec("Reserve"); return s.err }
func (s *stubSocial) Unreserve(context.Context, int64, string) error {
	s.rec("Unreserve")
	return s.err
}
func (s *stubSocial) Reservations(context.Context, int64) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
func (s *stubSocial) WatchLater(context.Context, int64) ([]map[string]any, error) {
	return []map[string]any{}, nil
}
func (s *stubSocial) AddWatchLater(context.Context, int64, string) error { return s.err }
func (s *stubSocial) RemoveWatchLater(context.Context, int64, string) error {
	return s.err
}
func (s *stubSocial) ReorderWatchLater(_ context.Context, _ int64, ordered []string) error {
	s.rec("Reorder")
	if s.err != nil {
		return s.err
	}
	_, err := store.ReorderPositions([]string{"a", "b"}, ordered)
	return err
}
func (s *stubSocial) Settings(context.Context, int64) (store.UserSettings, error) {
	return store.DefaultSettings(), nil
}
func (s *stubSocial) PatchSettings(_ context.Context, _ int64, body []byte) (store.UserSettings, error) {
	s.rec("PatchSettings")
	return store.ApplySettingsPatch(store.DefaultSettings(), body)
}
func (s *stubSocial) HistoryPaused(context.Context, int64) (bool, error) { return s.paused, nil }
func (s *stubSocial) DeleteWatch(context.Context, int64, string) error   { return s.err }
func (s *stubSocial) ClearWatchHistory(context.Context, int64) (int64, error) {
	return 3, s.err
}
func (s *stubSocial) Notifications(context.Context, int64, *store.Cursor, int) ([]map[string]any, string, error) {
	return []map[string]any{}, "", nil
}
func (s *stubSocial) UnreadNotifications(context.Context, int64) (int64, error) { return 2, nil }
func (s *stubSocial) MarkNotificationsRead(_ context.Context, _ int64, ids []int64, all bool) (int64, error) {
	s.rec("MarkRead")
	if all {
		return 5, nil
	}
	return int64(len(ids)), nil
}

func newSocialServer(soc *stubSocial) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, social: soc}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func do(h http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// socialRoutePatterns are the routes added by socialRoutes and notificationRoutes.
func socialRoutePatterns() []string {
	srv := &Server{}
	var out []string
	for _, rt := range append(srv.socialRoutes(), srv.notificationRoutes()...) {
		out = append(out, rt.pattern)
	}
	return out
}

func TestSocialRoutesRequireUser(t *testing.T) {
	soc := &stubSocial{}
	h := newSocialServer(soc)
	nilStore := New(nil, nil) // no store at all: still 401, never a panic
	for _, p := range socialRoutePatterns() {
		method, path, _ := strings.Cut(p, " ")
		path = samplePath(path)
		for _, tok := range []string{"", "bad"} {
			if rec := do(h, method, path, tok, "{}"); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s token=%q: status %d, want 401", p, tok, rec.Code)
			}
		}
		if rec := do(nilStore, method, path, "good", "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without store: status %d, want 401", p, rec.Code)
		}
	}
	if len(soc.calls) != 0 {
		t.Errorf("store called without auth: %v", soc.calls)
	}
}

func TestSocialHandlerStatus(t *testing.T) {
	feedCursor := store.EncodeCursor(store.Cursor{Sort: store.FeedSort, Key: "2026-10-01", ID: 4})
	notifCursor := store.EncodeCursor(store.Cursor{Sort: store.NotificationSort, ID: 9})
	tests := []struct {
		name         string
		err          error
		method, path string
		body         string
		want         int
	}{
		{"follow default mode", nil, "PUT", "/api/me/follows/aoi", "", 200},
		{"follow lead_only", nil, "PUT", "/api/me/follows/aoi", `{"notify_mode":"lead_only"}`, 200},
		{"follow bad mode", nil, "PUT", "/api/me/follows/aoi", `{"notify_mode":"sometimes"}`, 400},
		{"follow unknown field", nil, "PUT", "/api/me/follows/aoi", `{"mode":"all"}`, 400},
		{"follow bad json", nil, "PUT", "/api/me/follows/aoi", `{`, 400},
		{"follow unknown performer", pgx.ErrNoRows, "PUT", "/api/me/follows/nobody", "", 404},
		{"unfollow", nil, "DELETE", "/api/me/follows/aoi", "", 200},
		{"follows list", nil, "GET", "/api/me/follows", "", 200},
		{"feed", nil, "GET", "/api/me/following/feed", "", 200},
		{"feed cursor", nil, "GET", "/api/me/following/feed?cursor=" + feedCursor, "", 200},
		{"feed wrong cursor", nil, "GET", "/api/me/following/feed?cursor=" + notifCursor, "", 400},
		{"feed bad limit", nil, "GET", "/api/me/following/feed?limit=-1", "", 400},
		{"feed one performer", nil, "GET", "/api/me/following/feed?performer=aoi", "", 200},

		{"reserve upcoming", nil, "PUT", "/api/me/reservations/abc-123", "", 200},
		{"reserve published", store.ErrReleaseNotUpcoming, "PUT", "/api/me/reservations/abc-123", "", 409},
		{"unreserve published", store.ErrReleaseNotUpcoming, "DELETE", "/api/me/reservations/abc-123", "", 409},
		{"reserve unknown", pgx.ErrNoRows, "PUT", "/api/me/reservations/nope", "", 404},
		{"reservations list", nil, "GET", "/api/me/reservations", "", 200},

		{"watch later list", nil, "GET", "/api/me/watch-later", "", 200},
		{"watch later add", nil, "PUT", "/api/me/watch-later/abc-123", "", 200},
		{"watch later add unknown", pgx.ErrNoRows, "PUT", "/api/me/watch-later/nope", "", 404},
		{"watch later remove", nil, "DELETE", "/api/me/watch-later/abc-123", "", 200},
		{"reorder", nil, "POST", "/api/me/watch-later/reorder", `{"slugs":["b","a"]}`, 200},
		{"reorder empty", nil, "POST", "/api/me/watch-later/reorder", `{"slugs":[]}`, 400},
		{"reorder no body", nil, "POST", "/api/me/watch-later/reorder", "", 400},
		{"reorder unknown slug", nil, "POST", "/api/me/watch-later/reorder", `{"slugs":["z"]}`, 400},

		{"settings get", nil, "GET", "/api/me/settings", "", 200},
		{"settings patch", nil, "PATCH", "/api/me/settings", `{"theme":"light","history_paused":true}`, 200},
		{"settings unknown key", nil, "PATCH", "/api/me/settings", `{"colour":"red"}`, 400},
		{"settings bad enum", nil, "PATCH", "/api/me/settings", `{"theme":"neon"}`, 400},
		{"settings not object", nil, "PATCH", "/api/me/settings", `[1]`, 400},

		{"history delete one", nil, "DELETE", "/api/me/watch-history/abc-123", "", 200},
		{"history delete unknown", pgx.ErrNoRows, "DELETE", "/api/me/watch-history/nope", "", 404},
		{"history clear", nil, "DELETE", "/api/me/watch-history", "", 200},

		{"notifications", nil, "GET", "/api/me/notifications", "", 200},
		{"notifications cursor", nil, "GET", "/api/me/notifications?cursor=" + notifCursor, "", 200},
		{"notifications bad cursor", nil, "GET", "/api/me/notifications?cursor=not-a-cursor", "", 400},
		{"notifications feed cursor", nil, "GET", "/api/me/notifications?cursor=" + feedCursor, "", 400},
		{"unread count", nil, "GET", "/api/me/notifications/unread-count", "", 200},
		{"read ids", nil, "POST", "/api/me/notifications/read", `{"ids":[1,2]}`, 200},
		{"read all", nil, "POST", "/api/me/notifications/read", `{"all":true}`, 200},
		{"read neither", nil, "POST", "/api/me/notifications/read", `{}`, 400},
		{"read both", nil, "POST", "/api/me/notifications/read", `{"ids":[1],"all":true}`, 400},
		{"read bad ids", nil, "POST", "/api/me/notifications/read", `{"ids":["x"]}`, 400},
	}
	for _, tc := range tests {
		h := newSocialServer(&stubSocial{err: tc.err})
		rec := do(h, tc.method, tc.path, "good", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: %s %s -> %d (%s), want %d", tc.name, tc.method, tc.path, rec.Code, strings.TrimSpace(rec.Body.String()), tc.want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: content-type %q", tc.name, ct)
		}
	}
}

func TestSaveWatchRespectsHistoryPaused(t *testing.T) {
	for _, paused := range []bool{false, true} {
		h := newSocialServer(&stubSocial{paused: paused})
		rec := do(h, "POST", "/api/me/watch-history", "good", `{"slug":"abc-123","progress_seconds":10}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("paused=%v: status %d", paused, rec.Code)
		}
		var out map[string]bool
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if out["saved"] == paused || out["paused"] != paused {
			t.Errorf("paused=%v: body %v", paused, out)
		}
	}
}

func TestMarkReadPassesAll(t *testing.T) {
	soc := &stubSocial{}
	rec := do(newSocialServer(soc), "POST", "/api/me/notifications/read", "good", `{"all":true}`)
	var out map[string]int64
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["updated"] != 5 {
		t.Errorf("updated = %v", out)
	}
}
