package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeAttributions struct{ scope store.AttributionScope }

const dmmCredit = `Powered by <a href="https://affiliate.dmm.com/api/">FANZA Webサービス</a>`

func (f *fakeAttributions) Attributions(_ context.Context, s store.AttributionScope) ([]store.Attribution, error) {
	f.scope = s
	return []store.Attribution{{Source: "DMM", HTML: dmmCredit}}, nil
}

func TestAttributions(t *testing.T) {
	f := &fakeAttributions{}
	h := attributionsHandler(f)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/attributions?release=abc-001", nil))
	if rec.Code != http.StatusOK || f.scope != (store.AttributionScope{Kind: "release", Slug: "abc-001"}) {
		t.Fatalf("status %d scope %+v", rec.Code, f.scope)
	}
	// The html decodes to exactly the stored string.
	var body struct {
		Items []store.Attribution `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || len(body.Items) != 1 || body.Items[0].HTML != dmmCredit {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/attributions", nil))
	if rec.Code != http.StatusOK || f.scope.Kind != "" {
		t.Fatalf("status %d scope %+v", rec.Code, f.scope)
	}

	rec = httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/api/attributions?release=a&performer=b", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("two scopes: status %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	attributionsHandler(nil)(rec, httptest.NewRequest(http.MethodGet, "/api/attributions", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store: status %d", rec.Code)
	}
}
