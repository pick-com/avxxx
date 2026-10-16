package api

import (
	"avxxx/internal/homeops"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// stubHome is an in-memory homeStore recording the calls it gets.
type stubHome struct {
	slots      []store.HomeSlot
	anns       []store.Announcement
	liveCalls  int
	lastLang   string
	lastRegion string
	lastAt     time.Time
	actor      string
	created    store.HomeSlotInput
	reordered  []int64
	dismissed  map[int64]bool
}

func (s *stubHome) LiveHomeSlots(_ context.Context, at time.Time, lang, region string) ([]store.HomeSlot, error) {
	s.liveCalls++
	s.lastAt, s.lastLang, s.lastRegion = at, lang, region
	return s.slots, nil
}
func (s *stubHome) HomeSlots(context.Context, store.HomeSlotFilter) ([]store.HomeSlot, error) {
	return s.slots, nil
}
func (s *stubHome) CreateHomeSlot(_ context.Context, in store.HomeSlotInput, actor string) (store.HomeSlot, error) {
	s.created, s.actor = in, actor
	return store.HomeSlot{ID: 9, SlotType: in.SlotType}, nil
}
func (s *stubHome) UpdateHomeSlot(_ context.Context, id int64, _ store.HomeSlotInput, actor string) (store.HomeSlot, error) {
	s.actor = actor
	if id != 1 {
		return store.HomeSlot{}, pgx.ErrNoRows
	}
	return store.HomeSlot{ID: id}, nil
}
func (s *stubHome) DeleteHomeSlot(_ context.Context, _ int64, actor string) error {
	s.actor = actor
	return nil
}
func (s *stubHome) ReorderHomeSlots(_ context.Context, _ string, ids []int64, actor string) (int, error) {
	s.reordered, s.actor = ids, actor
	return len(ids), nil
}
func (s *stubHome) LiveAnnouncements(_ context.Context, at time.Time, lang, region string) ([]store.Announcement, error) {
	s.lastAt, s.lastLang, s.lastRegion = at, lang, region
	return s.anns, nil
}
func (s *stubHome) Announcements(context.Context) ([]store.Announcement, error) { return s.anns, nil }
func (s *stubHome) CreateAnnouncement(_ context.Context, in store.AnnouncementInput, actor string) (store.Announcement, error) {
	s.actor = actor
	if !homeops.HasAny(in.Texts, homeops.Body) {
		return store.Announcement{}, &store.ValidationError{Msg: "an announcement needs a body"}
	}
	return store.Announcement{ID: 3}, nil
}
func (s *stubHome) UpdateAnnouncement(context.Context, int64, store.AnnouncementInput, string) (store.Announcement, error) {
	return store.Announcement{}, nil
}
func (s *stubHome) DeleteAnnouncement(context.Context, int64, string) error { return nil }
func (s *stubHome) DismissAnnouncement(_ context.Context, userID, id int64) error {
	if id == 404 {
		return pgx.ErrNoRows
	}
	if s.dismissed == nil {
		s.dismissed = map[int64]bool{}
	}
	s.dismissed[id] = true
	return nil
}
func (s *stubHome) DismissedAnnouncements(context.Context, int64) ([]int64, error) {
	out := []int64{}
	for id := range s.dismissed {
		out = append(out, id)
	}
	return out, nil
}
func (s *stubHome) TopRatedShort(_ context.Context, maxMinutes, limit int) ([]store.RatedRelease, string, error) {
	score := 8.6
	return []store.RatedRelease{{Release: model.Release{ID: 5, Slug: "abc-005", CanonicalTitle: "Short", RuntimeMinutes: maxMinutes}, Score: &score, Votes: 60}}, store.ShortBasisRated, nil
}

func newHomeServer(h *stubHome) (*Server, http.Handler) {
	srv := &Server{accounts: &stubAccounts{}, home: h, homeCache: newRespCache(), adminToken: "admintok"}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return srv, mux
}

func homeFixture() *stubHome {
	starts := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &stubHome{slots: []store.HomeSlot{
		{ID: 1, SlotType: "hero", TargetType: "release", Texts: homeops.Texts{"en": {Title: "Pick of the week", Subtitle: "Editors' choice"}},
			StartsAt: starts, Release: &model.Release{ID: 11, Slug: "abc-011", CanonicalTitle: "原題"}},
		{ID: 2, SlotType: "hero", TargetType: "series", Texts: homeops.Texts{}, StartsAt: starts,
			Series: &store.HomeSeries{ID: 4, Slug: "best-series", Name: "Best Series", ReleaseCount: 3}, Release: &model.Release{ID: 12, Slug: "abc-012"}},
		{ID: 3, SlotType: "shelf", TargetType: "link", LinkURL: "https://example.com/x", Texts: homeops.Texts{"zh-CN": {Title: "活动"}}, StartsAt: starts},
	}, anns: []store.Announcement{
		{ID: 7, Severity: "warning", Texts: homeops.Texts{"zh-CN": {Body: "维护中"}, "en": {Body: "Maintenance", LinkLabel: "Details"}}, LinkURL: "/legal/terms", Dismissible: true, StartsAt: starts, UpdatedAt: starts},
		{ID: 8, Severity: "info", Texts: homeops.Texts{}, StartsAt: starts, UpdatedAt: starts}, // no body: not served
	}}
}

func TestPublicHomeSlots(t *testing.T) {
	h := homeFixture()
	_, mux := newHomeServer(h)
	rec := doH(mux, "GET", "/api/home/slots?lang=en", "", map[string]string{"CF-IPCountry": "jp"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("Cache-Control %q", cc)
	}
	if h.lastLang != "en" || h.lastRegion != "JP" {
		t.Errorf("store got lang %q region %q", h.lastLang, h.lastRegion)
	}
	var body struct {
		Hero  []homeSlotView `json:"hero"`
		Shelf []homeSlotView `json:"shelf"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Hero) != 2 || len(body.Shelf) != 1 {
		t.Fatalf("hero %d shelf %d", len(body.Hero), len(body.Shelf))
	}
	if v := body.Hero[0]; v.Title != "Pick of the week" || v.Subtitle != "Editors' choice" || v.Href != "/releases/abc-011" || v.Release == nil {
		t.Errorf("release slot %+v", v)
	}
	if v := body.Hero[1]; v.Title != "Best Series" || v.Href != "/series/best-series" || v.Series == nil {
		t.Errorf("series slot falls back to the series name: %+v", v)
	}
	if v := body.Shelf[0]; v.Title != "活动" || v.Href != "https://example.com/x" || !v.External {
		t.Errorf("link slot %+v", v)
	}

	// Cached: a second read does not reach the store; an admin write clears it.
	doH(mux, "GET", "/api/home/slots?lang=en", "", map[string]string{"CF-IPCountry": "JP"})
	if h.liveCalls != 1 {
		t.Errorf("store called %d times, want 1 (cached)", h.liveCalls)
	}
	rec = doH(mux, "POST", "/api/admin/home-slots", `{"slot_type":"hero","target_type":"release","release_slug":"abc-011","starts_at":"2026-10-20T00:00:00Z"}`,
		map[string]string{"Authorization": "Bearer admintok", "X-Admin-User": "ops1"})
	if rec.Code != http.StatusOK || h.actor != "ops1" || h.created.ReleaseSlug != "abc-011" {
		t.Fatalf("create: %d %s actor %q", rec.Code, rec.Body, h.actor)
	}
	doH(mux, "GET", "/api/home/slots?lang=en", "", map[string]string{"CF-IPCountry": "JP"})
	if h.liveCalls != 2 {
		t.Errorf("store called %d times after a write, want 2", h.liveCalls)
	}

	if rec := doH(mux, "GET", "/api/home/slots?region=USA", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad region: %d", rec.Code)
	}
	if rec := doH(mux, "GET", "/api/home/slots?lang=xx", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad lang: %d", rec.Code)
	}
}

func TestAnnouncementsAndDismissals(t *testing.T) {
	h := homeFixture()
	_, mux := newHomeServer(h)
	rec := doH(mux, "GET", "/api/announcements?lang=en", "", nil)
	var body struct {
		Items []announcementView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(body.Items) != 1 || body.Items[0].Body != "Maintenance" || body.Items[0].LinkLabel != "Details" || body.Items[0].Version == 0 {
		t.Errorf("items %+v", body.Items)
	}
	if h.lastRegion != "" {
		t.Errorf("no region header: got %q", h.lastRegion)
	}
	// zh-TW falls back to the zh-CN body; the link label never mixes in English.
	rec = doH(mux, "GET", "/api/announcements?lang=zh-TW", "", nil)
	var tw struct {
		Items []announcementView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tw); err != nil || len(tw.Items) != 1 || tw.Items[0].Body != "维护中" || tw.Items[0].LinkLabel != "" {
		t.Errorf("zh-TW items %+v (%v)", tw.Items, err)
	}

	if rec := doH(mux, "PUT", "/api/me/announcement-dismissals/7", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous dismiss: %d", rec.Code)
	}
	auth := map[string]string{"Authorization": "Bearer good"}
	if rec := doH(mux, "PUT", "/api/me/announcement-dismissals/7", "", auth); rec.Code != 200 {
		t.Errorf("dismiss: %d %s", rec.Code, rec.Body)
	}
	if rec := doH(mux, "PUT", "/api/me/announcement-dismissals/404", "", auth); rec.Code != http.StatusNotFound {
		t.Errorf("dismiss missing: %d", rec.Code)
	}
	rec = doH(mux, "GET", "/api/me/announcement-dismissals", "", auth)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"ids":[7]`) || !strings.Contains(rec.Header().Get("Cache-Control"), "no-store") {
		t.Errorf("dismissed list: %d %s", rec.Code, rec.Body)
	}

	adm := map[string]string{"Authorization": "Bearer admintok"}
	if rec := doH(mux, "POST", "/api/admin/announcements", `{"texts":{}}`, adm); rec.Code != http.StatusBadRequest {
		t.Errorf("announcement without body: %d", rec.Code)
	}
	if rec := doH(mux, "POST", "/api/admin/announcements", `{"bogus":1}`, adm); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: %d", rec.Code)
	}
}

func TestAdminOpsRoutes(t *testing.T) {
	h := homeFixture()
	_, mux := newHomeServer(h)
	adm := map[string]string{"Authorization": "Bearer admintok", "X-Admin-User": "ops2"}
	if rec := doH(mux, "GET", "/api/admin/home-slots", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := doH(mux, "GET", "/api/admin/home-slots?from=2026-10-01&to=2026-11-01&slot_type=hero", "", adm); rec.Code != 200 {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec := doH(mux, "GET", "/api/admin/home-slots?from=yesterday", "", adm); rec.Code != http.StatusBadRequest {
		t.Errorf("bad from: %d", rec.Code)
	}
	if rec := doH(mux, "PUT", "/api/admin/home-slots/2", `{}`, adm); rec.Code != http.StatusNotFound {
		t.Errorf("update missing: %d", rec.Code)
	}
	rec := doH(mux, "POST", "/api/admin/home-slots/reorder", `{"slot_type":"hero","ids":[2,1]}`, adm)
	if rec.Code != 200 || len(h.reordered) != 2 || h.reordered[0] != 2 || h.actor != "ops2" {
		t.Errorf("reorder: %d %v %q", rec.Code, h.reordered, h.actor)
	}
	rec = doH(mux, "GET", "/api/admin/home-slots/preview?at=2026-12-24T20:00:00Z&lang=en&region=us", "", adm)
	if rec.Code != 200 || !h.lastAt.Equal(time.Date(2026, 12, 24, 20, 0, 0, 0, time.UTC)) || h.lastRegion != "US" || h.lastLang != "en" {
		t.Fatalf("preview: %d %s at %v region %q", rec.Code, rec.Body, h.lastAt, h.lastRegion)
	}
	if !strings.Contains(rec.Body.String(), `"announcements"`) || !strings.Contains(rec.Body.String(), `"hero"`) {
		t.Errorf("preview body %s", rec.Body)
	}
	if rec := doH(mux, "DELETE", "/api/admin/announcements/7", "", adm); rec.Code != 200 {
		t.Errorf("delete: %d", rec.Code)
	}
}

func TestTopRatedShort(t *testing.T) {
	_, mux := newHomeServer(homeFixture())
	rec := doH(mux, "GET", "/api/home/top-rated-short?limit=5&max_minutes=45", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"basis":"rated"`) || !strings.Contains(rec.Body.String(), `"runtime_minutes":45`) ||
		!strings.Contains(rec.Body.String(), `"score":8.6`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := doH(mux, "GET", "/api/home/top-rated-short?limit=500", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("limit over max: %d", rec.Code)
	}
}

func TestHomeRoutesWithoutStore(t *testing.T) {
	srv := &Server{}
	mux := http.NewServeMux()
	for _, rt := range srv.homeRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	if rec := doH(mux, "GET", "/api/home/slots", "", nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no store: %d", rec.Code)
	}
}
