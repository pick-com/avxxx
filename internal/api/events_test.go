package api

import (
	"avxxx/internal/events"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stubSink is an in-memory eventSink that dedupes by event_id like the store.
type stubSink struct {
	seen        map[string]bool
	stored      []events.Event
	quarantined []events.Rejected
	insertErr   error
	quarErr     error
}

func (s *stubSink) InsertEvents(_ context.Context, evs []events.Event) (int, error) {
	if s.insertErr != nil {
		return 0, s.insertErr
	}
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	n := 0
	for _, e := range evs {
		if !s.seen[e.EventID] {
			s.seen[e.EventID] = true
			s.stored = append(s.stored, e)
			n++
		}
	}
	return n, nil
}

func (s *stubSink) QuarantineEvents(_ context.Context, rs []events.Rejected) error {
	s.quarantined = append(s.quarantined, rs...)
	return s.quarErr
}

var fixedNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

func evJSON(id string, consent ...string) string {
	if consent == nil {
		consent = []string{"necessary", "analytics"}
	}
	c, _ := json.Marshal(consent)
	return fmt.Sprintf(`{"event_id":%q,"event":"pause","schema_ver":1,"ts_client":1760529600000,`+
		`"device_id":"dev","session_id":"s","play_id":"p","platform":"web","app_ver":"1.0.0",`+
		`"consent":%s,"props":{"position_ms":1000}}`, id, c)
}

func batch(evs ...string) string { return "[" + strings.Join(evs, ",") + "]" }

func postEvents(t *testing.T, sink eventSink, accounts accountStore, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	srv := &Server{accounts: accounts}
	req := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ingestEvents(sink, events.Default, func() time.Time { return fixedNow })(rec, req)
	return rec
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) ingestResult {
	t.Helper()
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d, want 202; body %s", rec.Code, rec.Body)
	}
	var res ingestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestIngestEventsCounts(t *testing.T) {
	sink := &stubSink{}
	body := batch(evJSON("a"), evJSON("b"), evJSON("a"), `{"event":"nope","event_id":"z"}`, `42`)
	res := decodeResult(t, postEvents(t, sink, nil, body, nil))
	if res != (ingestResult{Accepted: 2, Duplicates: 1, Quarantined: 2}) {
		t.Errorf("first batch %+v", res)
	}
	// Resend (weak-network retry): everything valid is now a duplicate.
	res = decodeResult(t, postEvents(t, sink, nil, batch(evJSON("a"), evJSON("b"), evJSON("c")), nil))
	if res != (ingestResult{Accepted: 1, Duplicates: 2, Quarantined: 0}) {
		t.Errorf("resend %+v", res)
	}
	if len(sink.stored) != 3 || len(sink.quarantined) != 2 {
		t.Errorf("stored %d, quarantined %d", len(sink.stored), len(sink.quarantined))
	}
	for _, e := range sink.stored {
		if !e.TSServer.Equal(fixedNow) {
			t.Errorf("ts_server %v", e.TSServer)
		}
	}
	if !strings.Contains(sink.quarantined[0].Reason, `unknown event "nope"`) {
		t.Errorf("reason %q", sink.quarantined[0].Reason)
	}
}

func TestIngestEventsUserAndConsent(t *testing.T) {
	tests := []struct {
		name     string
		hdr      map[string]string
		consent  []string
		wantUser bool
		wantDev  bool
	}{
		{"anonymous", nil, nil, false, true},
		{"valid token", map[string]string{"Authorization": "Bearer good"}, nil, true, true},
		{"invalid token is not rejected", map[string]string{"Authorization": "Bearer bad"}, nil, false, true},
		{"no analytics consent strips ids", map[string]string{"Authorization": "Bearer good"}, []string{"necessary"}, false, false},
		{"GPC strips ids", map[string]string{"Authorization": "Bearer good", "Sec-GPC": "1"}, nil, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &stubSink{}
			res := decodeResult(t, postEvents(t, sink, &stubAccounts{}, batch(evJSON("e1", tt.consent...)), tt.hdr))
			if res.Accepted != 1 || len(sink.stored) != 1 {
				t.Fatalf("result %+v", res)
			}
			e := sink.stored[0]
			if got := e.UserID != nil && *e.UserID == 1; got != tt.wantUser {
				t.Errorf("user_id = %v, want attached=%v", e.UserID, tt.wantUser)
			}
			if got := e.DeviceID != ""; got != tt.wantDev {
				t.Errorf("device_id = %q, want kept=%v", e.DeviceID, tt.wantDev)
			}
		})
	}
}

func TestIngestEventsTokenStoreError(t *testing.T) {
	sink := &stubSink{}
	acc := &stubAccounts{idErr: errors.New("db down")}
	res := decodeResult(t, postEvents(t, sink, acc, batch(evJSON("e1")), map[string]string{"Authorization": "Bearer good"}))
	if res.Accepted != 1 || sink.stored[0].UserID != nil {
		t.Errorf("result %+v, user %v", res, sink.stored[0].UserID)
	}
}

func TestIngestEventsLimits(t *testing.T) {
	var fifty, fiftyOne []string
	for i := range 51 {
		ev := evJSON(fmt.Sprintf("e%d", i))
		if i < 50 {
			fifty = append(fifty, ev)
		}
		fiftyOne = append(fiftyOne, ev)
	}
	huge := batch(evJSON("big") + strings.Repeat(" ", maxEventBodyBytes))
	tests := []struct {
		name string
		body string
		code int
	}{
		{"50 events ok", batch(fifty...), http.StatusAccepted},
		{"51 events", batch(fiftyOne...), http.StatusRequestEntityTooLarge},
		{"body over 256 KB", huge, http.StatusRequestEntityTooLarge},
		{"not an array", evJSON("x"), http.StatusBadRequest},
		{"invalid JSON", "[{", http.StatusBadRequest},
		{"empty array", "[]", http.StatusBadRequest},
		{"empty body", "", http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sink := &stubSink{}
			rec := postEvents(t, sink, nil, tt.body, nil)
			if rec.Code != tt.code {
				t.Fatalf("status %d, want %d; body %s", rec.Code, tt.code, rec.Body)
			}
			if tt.code != http.StatusAccepted {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
					t.Errorf("error body %q", rec.Body)
				}
				if len(sink.stored)+len(sink.quarantined) != 0 {
					t.Error("rejected batch reached the sink")
				}
			}
		})
	}
}

func TestIngestEventsSinkErrors(t *testing.T) {
	rec := postEvents(t, &stubSink{insertErr: errors.New("connection refused")}, nil, batch(evJSON("a")), nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("insert failure: %d %s", rec.Code, rec.Body)
	}
	// A failed quarantine write does not fail the batch.
	res := decodeResult(t, postEvents(t, &stubSink{quarErr: errors.New("x")}, nil, batch(evJSON("a"), `{}`), nil))
	if res != (ingestResult{Accepted: 1, Quarantined: 1}) {
		t.Errorf("quarantine failure: %+v", res)
	}
	// No store configured.
	if rec := postEvents(t, nil, nil, batch(evJSON("a")), nil); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("nil sink: %d", rec.Code)
	}
}

func TestEventsRouteWithoutStore(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(batch(evJSON("a")))))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}
