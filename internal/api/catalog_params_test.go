package api

import (
	"avxxx/internal/store"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestParseReleaseFilter(t *testing.T) {
	cur := store.EncodeCursor(store.Cursor{Sort: "oldest", Key: "2020-01-01", ID: 4})
	q, _ := url.ParseQuery("q=abc&tag=drama&maker=S1&performer=ai&year=2023&min_runtime=60&max_runtime=120&sort=oldest&limit=50&cursor=" + cur)
	f, err := parseReleaseFilter(q)
	if err != nil {
		t.Fatal(err)
	}
	if f.Q != "abc" || f.Tag != "drama" || f.Maker != "S1" || f.Performer != "ai" || f.Year != 2023 ||
		f.MinRuntime != 60 || f.MaxRuntime != 120 || f.Sort != "oldest" || f.Limit != 50 ||
		f.Cursor == nil || f.Cursor.ID != 4 {
		t.Errorf("filter = %+v", f)
	}
	f, err = parseReleaseFilter(url.Values{})
	if err != nil || f.Sort != "newest" || f.Limit != 0 || f.Cursor != nil {
		t.Errorf("defaults = %+v, %v", f, err)
	}
}

func TestParsePerformerFilter(t *testing.T) {
	f, err := parsePerformerFilter(url.Values{"sort": {"works"}, "limit": {"5"}})
	if err != nil || f.Sort != "works" || f.Limit != 5 {
		t.Errorf("filter = %+v, %v", f, err)
	}
	f, err = parsePerformerFilter(url.Values{})
	if err != nil || f.Sort != "name" {
		t.Errorf("defaults = %+v, %v", f, err)
	}
}

// Invalid parameters are rejected with 400 before the store is touched, so a
// Server without a database is enough.
func TestCatalogListBadParams(t *testing.T) {
	newestCur := store.EncodeCursor(store.Cursor{Sort: "newest", Key: "2020-01-01", ID: 1})
	badKey := store.EncodeCursor(store.Cursor{Sort: "newest", Key: "not-a-date", ID: 1})
	cases := []string{
		"/api/releases?sort=views",
		"/api/releases?limit=abc",
		"/api/releases?limit=-1",
		"/api/releases?year=99",
		"/api/releases?year=20x3",
		"/api/releases?min_runtime=-5",
		"/api/releases?min_runtime=200&max_runtime=100",
		"/api/releases?cursor=%21%21",
		"/api/releases?sort=runtime&cursor=" + newestCur,
		"/api/releases?cursor=" + badKey, // caught by the store's query builder
		"/api/performers?sort=popular",
		"/api/performers?limit=x",
		"/api/performers?cursor=" + newestCur,
	}
	mux := http.NewServeMux()
	srv := &Server{}
	for _, rt := range srv.catalogRoutes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	for _, path := range cases {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body %s)", path, rec.Code, rec.Body)
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf("%s: body %q", path, rec.Body)
		}
	}
}

func TestPageEnvelope(t *testing.T) {
	b, _ := json.Marshal(page([]int{}, ""))
	if string(b) != `{"items":[],"next_cursor":null}` {
		t.Errorf("last page = %s", b)
	}
	b, _ = json.Marshal(page([]int{1}, "abc"))
	if string(b) != `{"items":[1],"next_cursor":"abc"}` {
		t.Errorf("page = %s", b)
	}
}
