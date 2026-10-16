package api

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// stubCompliance records what the compliance handlers pass to the store.
// Methods a test does not override panic through the nil interface.
type stubCompliance struct {
	complianceStore
	consents   []store.ConsentRecord
	feedback   []store.FeedbackInput
	lookups    []string // "ticket email"
	takenDown  map[string]bool
	legalCalls []string // "kind lang version"
}

func (s *stubCompliance) RecordConsent(_ context.Context, rec store.ConsentRecord) (store.ConsentState, error) {
	s.consents = append(s.consents, rec)
	return store.ConsentState{Categories: rec.Input.Categories(), PersonalizedAds: rec.Input.PersonalizedAds, GPC: rec.Input.GPC}, nil
}

func (s *stubCompliance) LatestConsent(_ context.Context, uid int64) (store.ConsentState, error) {
	if uid != 1 {
		return store.ConsentState{}, pgx.ErrNoRows
	}
	return store.ConsentState{Analytics: true, Categories: []string{"necessary", "analytics"}}, nil
}

func (s *stubCompliance) CreateFeedback(_ context.Context, in store.FeedbackInput) (store.FeedbackReceipt, error) {
	s.feedback = append(s.feedback, in)
	return store.FeedbackReceipt{ID: int64(len(s.feedback)), Status: "open"}, nil
}

func (s *stubCompliance) TakedownStatus(_ context.Context, ticket, email string) (store.TakedownPublicStatus, error) {
	s.lookups = append(s.lookups, ticket+" "+email)
	if email != "owner@example.com" {
		return store.TakedownPublicStatus{}, pgx.ErrNoRows
	}
	return store.TakedownPublicStatus{Ticket: ticket, Status: "received"}, nil
}

func (s *stubCompliance) ReleaseTakenDown(_ context.Context, slug string) (bool, error) {
	return s.takenDown[slug], nil
}

func (s *stubCompliance) LegalDocument(_ context.Context, kind, lang string, version int) (store.PublicLegalDocument, error) {
	s.legalCalls = append(s.legalCalls, strings.Join([]string{kind, lang, strconv.Itoa(version)}, " "))
	if !store.ValidLegalKind(kind) {
		return store.PublicLegalDocument{}, pgx.ErrNoRows
	}
	return store.PublicLegalDocument{Kind: kind, Lang: "zh-CN", RequestedLang: lang, Fallback: lang != "zh-CN", Version: 1}, nil
}

func (s *stubCompliance) CurrentLegalVersion(_ context.Context, kind string) (store.LegalVersion, error) {
	return store.LegalVersion{Kind: kind, Version: 3, EffectiveAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}, nil
}

func newComplianceServer(c *stubCompliance) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, compliance: c, limiter: community.NewMemoryLimiter()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestConsentRecordsGPCAndHashedDevice(t *testing.T) {
	c := &stubCompliance{}
	h := newComplianceServer(c)
	device := "d3f1c6a2-1b7e-4c55-9b2b-0f6e7a1c2d3e"
	body := `{"analytics":true,"personalized_ads":true,"personalized_recs":false,"source":"banner","device_id":"` + device + `"}`

	req := httptest.NewRequest("POST", "/api/consent", strings.NewReader(body))
	req.Header.Set("Sec-GPC", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	got := c.consents[0]
	if !got.Input.GPC || got.Input.PersonalizedAds || got.UserID != nil {
		t.Errorf("GPC header must force personalized_ads off: %+v", got)
	}
	if got.DeviceHash == "" || strings.Contains(got.DeviceHash, device) || len(got.DeviceHash) != 32 {
		t.Errorf("device id must be stored only as a keyed hash, got %q", got.DeviceHash)
	}
	if strings.Contains(rec.Body.String(), "personalized_ads\"]") {
		t.Errorf("categories must not include personalized_ads: %s", rec.Body)
	}

	// Logged in: linked to the user; an invalid token still records for the device.
	if rec := do(h, "POST", "/api/consent", "good", `{"analytics":false,"source":"sync"}`); rec.Code != http.StatusCreated || c.consents[1].UserID == nil || *c.consents[1].UserID != 1 {
		t.Errorf("logged-in consent: %d %+v", rec.Code, c.consents[1])
	}
	if rec := do(h, "POST", "/api/consent", "bad", `{"analytics":false,"device_id":"`+device+`"}`); rec.Code != http.StatusCreated || c.consents[2].UserID != nil {
		t.Errorf("invalid token must fall back to the device: %d", rec.Code)
	}
	// Anonymous without a device id, bad source, unknown field: 400.
	for _, b := range []string{`{"analytics":true}`, `{"source":"x","device_id":"` + device + `"}`, `{"ip":"1.2.3.4","device_id":"` + device + `"}`, `{"device_id":"short"}`} {
		if rec := do(h, "POST", "/api/consent", "", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", b, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/me/consent", "good", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"analytics":true`) {
		t.Errorf("me/consent: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/me/consent", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("me/consent without login: %d", rec.Code)
	}
}

func TestFeedbackValidationAndRateLimit(t *testing.T) {
	c := &stubCompliance{}
	h := newComplianceServer(c)
	ok := `{"category":"bug","message":"player stalls","email":"a@example.com","page_url":"https://reel.example/releases/x?token=secret#t=1"}`
	if rec := do(h, "POST", "/api/feedback", "", ok); rec.Code != http.StatusCreated {
		t.Fatalf("feedback: %d %s", rec.Code, rec.Body)
	}
	if got := c.feedback[0].PageURL; got != "https://reel.example/releases/x" {
		t.Errorf("page_url must drop query and fragment, got %q", got)
	}
	for _, b := range []string{`{"category":"spam","message":"x"}`, `{"category":"bug","message":""}`, `{"category":"bug","message":"x","email":"nope"}`, `{"category":"bug","message":"x","page_url":"javascript:alert(1)"}`} {
		if rec := do(h, "POST", "/api/feedback", "", b); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", b, rec.Code)
		}
	}
	limited := false
	for range feedbackLimit.perIP + 1 {
		if rec := do(h, "POST", "/api/feedback", "", ok); rec.Code == http.StatusTooManyRequests {
			limited = true
		}
	}
	if !limited {
		t.Error("feedback must be rate limited per address")
	}
}

func TestTakedownLookupUsesHeaderNotURL(t *testing.T) {
	c := &stubCompliance{}
	h := newComplianceServer(c)
	req := httptest.NewRequest("GET", "/api/takedowns/TD-ABCD-EFGH-JKMN-PQRS", nil)
	req.Header.Set(takedownEmailHeader, "owner@example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("lookup: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if strings.Contains(rec.Body.String(), "owner@example.com") {
		t.Errorf("lookup must not echo the email: %s", rec.Body)
	}
	if rec := do(h, "GET", "/api/takedowns/TD-ABCD-EFGH-JKMN-PQRS?email=owner@example.com", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("the email in the query string must not be read: %d", rec.Code)
	}
}

func TestTakedownIntakeValidation(t *testing.T) {
	h := newComplianceServer(&stubCompliance{})
	// Missing good-faith statement and signature: 400 before the store.
	b := `{"claimant_name":"A","claimant_email":"a@example.com","rights_owner":"Studio","work_description":"Film","infringing_urls":["https://reel.example/releases/x"],"good_faith":false,"accuracy":true,"signature":""}`
	if rec := do(h, "POST", "/api/takedowns", "", b); rec.Code != http.StatusBadRequest {
		t.Errorf("incomplete notice: %d", rec.Code)
	}
}

func TestReleaseTakenDownAnswers451(t *testing.T) {
	srv := &Server{compliance: &stubCompliance{takenDown: map[string]bool{"abc-001": true}}}
	for slug, want := range map[string]int{"abc-001": http.StatusUnavailableForLegalReasons, "abc-002": http.StatusNotFound} {
		req := httptest.NewRequest("GET", "/api/releases/"+slug, nil)
		req.SetPathValue("slug", slug)
		rec := httptest.NewRecorder()
		srv.replyMissingRelease(rec, req, pgx.ErrNoRows)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", slug, rec.Code, want)
		}
		if want == 451 && (strings.Contains(rec.Body.String(), slug) || !strings.Contains(rec.Body.String(), "copyright")) {
			t.Errorf("451 body: %s", rec.Body)
		}
	}
}

func TestLegalRoutes(t *testing.T) {
	c := &stubCompliance{}
	h := newComplianceServer(c)
	if rec := do(h, "GET", "/api/legal/terms/current-version", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"3"`) {
		t.Errorf("current-version: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, "GET", "/api/legal/privacy?lang=en&version=1", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"fallback":true`) {
		t.Errorf("privacy en: %d %s", rec.Code, rec.Body)
	}
	if got := c.legalCalls[len(c.legalCalls)-1]; got != "privacy en 1" {
		t.Errorf("store call %q", got)
	}
	for _, p := range []string{"/api/legal/privacy?lang=xx", "/api/legal/privacy?version=0"} {
		if rec := do(h, "GET", p, "", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", p, rec.Code)
		}
	}
	if rec := do(h, "GET", "/api/legal/nope", "", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown kind: %d", rec.Code)
	}
}
