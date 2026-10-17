package api

import (
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type stubFeedback struct {
	owners []store.RecFeedbackOwner
	added  []string
	undone []string
	items  []store.RecFeedback
	claims []string
}

func (s *stubFeedback) AddRecFeedback(_ context.Context, o store.RecFeedbackOwner, kind, target string) (store.RecFeedback, error) {
	s.owners = append(s.owners, o)
	if target == "missing" {
		return store.RecFeedback{}, pgx.ErrNoRows
	}
	if kind == recs.FeedbackTag && strings.Contains(target, "/") {
		return store.RecFeedback{}, store.ErrBadFeedback
	}
	s.added = append(s.added, kind+":"+target)
	return store.RecFeedback{Kind: kind, Target: target, Name: "N", CreatedAt: time.Unix(0, 0)}, nil
}

func (s *stubFeedback) UndoRecFeedback(_ context.Context, o store.RecFeedbackOwner, kind, target string) (bool, error) {
	s.owners = append(s.owners, o)
	s.undone = append(s.undone, kind+":"+target)
	return target != "none", nil
}

func (s *stubFeedback) ListRecFeedback(_ context.Context, o store.RecFeedbackOwner) ([]store.RecFeedback, error) {
	s.owners = append(s.owners, o)
	return s.items, nil
}

func (s *stubFeedback) ClaimRecFeedback(_ context.Context, userID int64, deviceID string) (int64, error) {
	s.claims = append(s.claims, fmt.Sprintf("%d:%s", userID, deviceID))
	return 3, nil
}

const testDevice = "device-abcdef123456"

func feedbackMux(st *stubRecs, fb *stubFeedback) http.Handler {
	srv := &Server{recs: st, recsAdmin: st, recFeedback: fb, accounts: &stubAccounts{}, limiter: community.NewMemoryLimiter()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func doDevice(h http.Handler, method, path, token, device, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if device != "" {
		req.Header.Set(deviceHeader, device)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRecFeedbackStatusCodes(t *testing.T) {
	rel := `{"kind":"not_interested_release","target":"r2"}`
	for _, c := range []struct {
		method, path, token, device, body string
		want                              int
	}{
		{"POST", "/api/recs/feedback", "good", "", rel, 201},
		{"POST", "/api/recs/feedback", "", testDevice, rel, 201},
		{"POST", "/api/recs/feedback", "", "", rel, 400},                  // nobody
		{"POST", "/api/recs/feedback", "", "short", rel, 400},             // invalid device id
		{"POST", "/api/recs/feedback", "bad-token", testDevice, rel, 401}, // a sent token must be valid
		{"POST", "/api/recs/feedback", "good", "", `{"kind":"dislike","target":"r2"}`, 400},
		{"POST", "/api/recs/feedback", "good", "", `{"kind":"not_interested_release"}`, 400},
		{"POST", "/api/recs/feedback", "good", "", `{"kind":"not_interested_release","target":"missing"}`, 404},
		{"POST", "/api/recs/feedback", "good", "", `{"kind":"not_interested_tag","target":"a/b"}`, 400},
		{"POST", "/api/recs/feedback", "good", "", `{"kind":"not_interested_performer","target":"p","x":1}`, 400},
		{"POST", "/api/recs/feedback", "good", "", "", 400},
		{"DELETE", "/api/recs/feedback?kind=not_interested_release&target=r2", "good", "", "", 200},
		{"DELETE", "/api/recs/feedback?kind=not_interested_release&target=r2", "", testDevice, "", 200},
		{"DELETE", "/api/recs/feedback?kind=bogus&target=r2", "good", "", "", 400},
		{"DELETE", "/api/recs/feedback?kind=not_interested_release&target=r2", "", "", "", 400},
		{"GET", "/api/me/rec-feedback", "good", "", "", 200},
		{"GET", "/api/me/rec-feedback", "", testDevice, "", 200},
		{"GET", "/api/me/rec-feedback", "", "", "", 400},
		{"GET", "/api/me/rec-feedback", "bad-token", "", "", 401},
	} {
		rec := doDevice(feedbackMux(newStubRecs(), &stubFeedback{}), c.method, c.path, c.token, c.device, c.body)
		if rec.Code != c.want {
			t.Errorf("%s %s (token %q device %q): %d, want %d: %s", c.method, c.path, c.token, c.device, rec.Code, c.want, rec.Body)
		}
		if rec.Code < 300 && rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("%s %s: feedback responses must be private", c.method, c.path)
		}
	}
}

func TestRecFeedbackUnavailableWithoutStore(t *testing.T) {
	srv := &Server{accounts: &stubAccounts{}}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	if rec := doDevice(mux, "GET", "/api/me/rec-feedback", "good", "", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list without store: %d, want 503", rec.Code)
	}
}

func TestRecFeedbackOwners(t *testing.T) {
	fb := &stubFeedback{}
	mux := feedbackMux(newStubRecs(), fb)
	body := `{"kind":"not_interested_performer","target":"p1"}`
	doDevice(mux, "POST", "/api/recs/feedback", "good", testDevice, body) // the token wins
	doDevice(mux, "POST", "/api/recs/feedback", "", testDevice, body)
	if len(fb.owners) != 2 || fb.owners[0] != (store.RecFeedbackOwner{UserID: 1}) || fb.owners[1] != (store.RecFeedbackOwner{DeviceID: testDevice}) {
		t.Fatalf("owners = %+v", fb.owners)
	}
	if k := fb.owners[1].Key(); k != "d:"+community.DeviceHash(testDevice) || strings.Contains(k, testDevice) {
		t.Errorf("device owner key %q must be the hash, never the raw id", k)
	}
	if fb.added[0] != "not_interested_performer:p1" {
		t.Errorf("added = %v", fb.added)
	}
}

func TestRecFeedbackUndoAndList(t *testing.T) {
	fb := &stubFeedback{items: []store.RecFeedback{{Kind: recs.FeedbackTag, Target: "drama", Name: "drama"}}}
	mux := feedbackMux(newStubRecs(), fb)
	rec := doDevice(mux, "DELETE", "/api/recs/feedback?kind=not_interested_tag&target=none", "good", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"undone":false`) {
		t.Errorf("undo nothing: %d %s", rec.Code, rec.Body)
	}
	rec = doDevice(mux, "GET", "/api/me/rec-feedback", "good", "", "")
	var body struct {
		Items []store.RecFeedback `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].Target != "drama" {
		t.Fatalf("list = %s (%v)", rec.Body, err)
	}
	fb.items = nil
	if rec = doDevice(mux, "GET", "/api/me/rec-feedback", "good", "", ""); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty list should be [], got %s", rec.Body)
	}
}

func TestRecFeedbackRateLimited(t *testing.T) {
	mux := feedbackMux(newStubRecs(), &stubFeedback{})
	body := `{"kind":"not_interested_release","target":"r2"}`
	for i := 0; i < limitRecFeedbackOwner.limit; i++ {
		if rec := doDevice(mux, "POST", "/api/recs/feedback", "", testDevice, body); rec.Code != 201 {
			t.Fatalf("request %d: %d", i, rec.Code)
		}
	}
	if rec := doDevice(mux, "POST", "/api/recs/feedback", "", testDevice, body); rec.Code != http.StatusTooManyRequests {
		t.Errorf("over the limit: %d, want 429", rec.Code)
	}
	// Another owner has its own budget.
	if rec := doDevice(mux, "POST", "/api/recs/feedback", "good", "", body); rec.Code != 201 {
		t.Errorf("signed-in owner: %d, want 201", rec.Code)
	}
}

// GET /api/recs passes the feedback owner to recall and ranks with the
// feedback recall returns.
func TestRecsAppliesFeedback(t *testing.T) {
	st := newStubRecs()
	var fbs recs.Feedback
	fbs.Add(recs.FeedbackRelease, 2, "")
	st.recall.Feedback = fbs
	mux := feedbackMux(st, &stubFeedback{})

	rec := doDevice(mux, "GET", "/api/recs?slot=home", "", testDevice, "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if st.gotQuery.Feedback != (store.RecFeedbackOwner{DeviceID: testDevice}) || st.gotQuery.UserID != 0 {
		t.Errorf("visitor query = %+v", st.gotQuery)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Error("recs personalized by device feedback must not be cached publicly")
	}
	var body struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, it := range body.Items {
		if it.ID == 2 {
			t.Fatalf("release marked 不感兴趣 returned: %s", rec.Body)
		}
	}
	if len(body.Items) == 0 {
		t.Fatal("other releases should stay")
	}

	doDevice(mux, "GET", "/api/recs?slot=related&release=known", "good", "", "")
	if st.gotQuery.Feedback != (store.RecFeedbackOwner{UserID: 1}) {
		t.Errorf("signed-in query = %+v", st.gotQuery)
	}
	doDevice(mux, "GET", "/api/recs?slot=home", "", "", "")
	if st.gotQuery.Feedback.Key() != "" {
		t.Errorf("anonymous query without device should carry no owner: %+v", st.gotQuery)
	}
	if rec := doDevice(mux, "GET", "/api/recs?slot=home", "", "", ""); rec.Header().Get("Cache-Control") == "private, no-store" {
		t.Error("plain visitor recs should stay cacheable")
	}
}

// POST /api/recs/feedback/claim needs a valid token and a valid device id,
// and hands both to the store.
func TestRecFeedbackClaim(t *testing.T) {
	for _, c := range []struct {
		token, device string
		want          int
	}{
		{"good", testDevice, 200},
		{"", testDevice, 401},          // a visitor cannot claim
		{"bad-token", testDevice, 401}, // the token must be valid
		{"good", "", 400},              // no device
		{"good", "short", 400},         // invalid device id
	} {
		fb := &stubFeedback{}
		rec := doDevice(feedbackMux(newStubRecs(), fb), "POST", "/api/recs/feedback/claim", c.token, c.device, "")
		if rec.Code != c.want {
			t.Errorf("claim (token %q device %q): %d, want %d: %s", c.token, c.device, rec.Code, c.want, rec.Body)
		}
		if c.want != 200 {
			if len(fb.claims) != 0 {
				t.Errorf("claim (token %q device %q) reached the store: %v", c.token, c.device, fb.claims)
			}
			continue
		}
		if len(fb.claims) != 1 || fb.claims[0] != "1:"+testDevice {
			t.Errorf("claims = %v", fb.claims)
		}
		if !strings.Contains(rec.Body.String(), `"claimed":3`) || rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("claim reply %s (Cache-Control %q)", rec.Body, rec.Header().Get("Cache-Control"))
		}
	}
}
