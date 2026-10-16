package api

import (
	"avxxx/internal/i18n"
	"avxxx/internal/store"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubMT struct {
	err      error
	queued   int
	gotLang  string
	gotRefs  []i18n.Ref
	gotBy    string
	enqueued []string
}

func (s *stubMT) QueueMachineTranslations(_ context.Context, lang string, refs []i18n.Ref, by string) (int, error) {
	s.gotLang, s.gotRefs, s.gotBy = lang, refs, by
	return s.queued, s.err
}
func (s *stubMT) MachineTranslationStatus(context.Context, string, time.Time) (store.MTStatus, error) {
	return store.MTStatus{UsedToday: 7, Pending: 3}, s.err
}
func (s *stubMT) EnqueueKindNow(_ context.Context, kind string) (int64, bool, error) {
	s.enqueued = append(s.enqueued, kind)
	return 42, true, nil
}

const mtTestKey = "sk-or-v1-APITEST-LEAKCANARY-0001"

func mtMux(st *stubMT, configured bool) *http.ServeMux {
	srv := &Server{adminToken: "secret", adminAuth: newFakeAdmins(), mt: st}
	cfg, _ := i18n.OpenRouterConfigFromEnv(func(k string) string {
		if k == "OPENROUTER_API_KEY" && configured {
			return mtTestKey
		}
		return ""
	})
	srv.mtCfg = cfg
	mux := http.NewServeMux()
	for _, rt := range srv.adminI18nMTRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestAdminMachineTranslate(t *testing.T) {
	const path = "/api/admin/i18n/machine-translate"
	many := `{"lang":"en","items":[` + strings.Repeat(`{"entity_type":"release","entity_id":"1","field":"title"},`, 200) + `{"entity_type":"release","entity_id":"1","field":"title"}]}`
	tests := []struct {
		name, method, body string
		configured         bool
		queued             int
		err                error
		want               int
		wantBody           string
		wantEnqueue        bool
	}{
		{"status", "GET", "", true, 0, nil, 200, `"used_today":7`, false},
		{"status not configured", "GET", "", false, 0, nil, 200, `"configured":false`, false},
		{"queue", "POST", `{"lang":"zh-TW","items":[{"entity_type":"release","entity_id":"12","field":"title"},{"entity_type":"performer","entity_id":"3","field":"name"}]}`, true, 1, nil, 200, `"skipped":1`, true},
		{"queue nothing eligible", "POST", `{"lang":"en","items":[{"entity_type":"tag","entity_id":"big-tits","field":"name"}]}`, true, 0, nil, 200, `"queued":0`, false},
		{"not configured", "POST", `{"lang":"en","items":[{"entity_type":"release","entity_id":"1","field":"title"}]}`, false, 0, nil, 409, "not configured", false},
		{"bad lang", "POST", `{"lang":"fr","items":[{"entity_type":"release","entity_id":"1","field":"title"}]}`, true, 0, nil, 400, "unsupported lang", false},
		{"no items", "POST", `{"lang":"en","items":[]}`, true, 0, nil, 400, "1-200", false},
		{"too many", "POST", many, true, 0, nil, 400, "1-200", false},
		{"bad ref", "POST", `{"lang":"en","items":[{"entity_type":"release","entity_id":"x","field":"title"}]}`, true, 0, nil, 400, "invalid entity_id", false},
		{"bad field", "POST", `{"lang":"en","items":[{"entity_type":"performer","entity_id":"1","field":"real_name"}]}`, true, 0, nil, 400, "unknown field", false},
		{"store failure", "POST", `{"lang":"en","items":[{"entity_type":"release","entity_id":"1","field":"title"}]}`, true, 0, errors.New("boom"), 500, "request failed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubMT{err: tt.err, queued: tt.queued}
			req := httptest.NewRequest(tt.method, path, strings.NewReader(tt.body))
			asAdmin(req, "translator-1")
			rec := httptest.NewRecorder()
			mtMux(st, tt.configured).ServeHTTP(rec, req)
			if rec.Code != tt.want || !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Fatalf("status %d body %s, want %d with %s", rec.Code, rec.Body.String(), tt.want, tt.wantBody)
			}
			if strings.Contains(rec.Body.String(), mtTestKey) {
				t.Fatal("the API key must never be returned")
			}
			if got := len(st.enqueued) == 1 && st.enqueued[0] == store.JobKindMachineTranslation; got != tt.wantEnqueue {
				t.Fatalf("enqueued = %v, want %v", st.enqueued, tt.wantEnqueue)
			}
			if tt.name == "queue" && (st.gotLang != "zh-TW" || len(st.gotRefs) != 2 || st.gotBy != "translator-1") {
				t.Fatalf("queue got %q %v %q", st.gotLang, st.gotRefs, st.gotBy)
			}
		})
	}
	rec := httptest.NewRecorder()
	mtMux(&stubMT{}, true).ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no credentials: status %d", rec.Code)
	}
}
