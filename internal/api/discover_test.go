package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestParseReleaseFilterDiscoverFacets(t *testing.T) {
	q, _ := url.ParseQuery("performer=ai&role=guest&min_height=1080&subtitle=zh-CN&audio_lang=ja")
	f, err := parseReleaseFilter(q)
	if err != nil || f.Role != store.CreditGuest || f.MinHeight != 1080 || f.Subtitle != "zh-CN" || f.AudioLang != "ja" {
		t.Errorf("filter = %+v, %v", f, err)
	}
	if f, err = parseReleaseFilter(url.Values{"subtitle": {"any"}}); err != nil || f.Subtitle != "any" {
		t.Errorf("subtitle any = %+v, %v", f, err)
	}
	for _, raw := range []string{
		"role=lead",               // needs performer
		"performer=ai&role=cameo", // unknown role
		"min_height=-1", "min_height=99999",
		"subtitle=zh_CN!", "audio_lang=JAPANESE",
	} {
		q, _ := url.ParseQuery(raw)
		if _, err := parseReleaseFilter(q); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
}

func TestParsePerformerFilterNavActors(t *testing.T) {
	f, err := parsePerformerFilter(url.Values{"sort": {"heat"}, "era": {"2010"}, "tag": {" drama "}, "audio_lang": {"ja"}})
	if err != nil || f.Sort != "heat" || f.Era != 2010 || f.Tag != "drama" || f.AudioLang != "ja" {
		t.Errorf("filter = %+v, %v", f, err)
	}
	for _, s := range []string{"followers", "rising", "works", "newest_work", "name"} {
		if _, err := parsePerformerFilter(url.Values{"sort": {s}}); err != nil {
			t.Errorf("sort %s: %v", s, err)
		}
	}
	for _, raw := range []string{"era=2015", "era=1900", "era=abc", "sort=popular", "audio_lang=x y"} {
		q, _ := url.ParseQuery(raw)
		if _, err := parsePerformerFilter(q); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
}

func TestCatalogSearchTypeAndCounts(t *testing.T) {
	st := &stubSearch{result: store.SearchResult{Items: []map[string]any{}, Query: "x", Counts: map[string]int{"performer": 2, "release": 40}}}
	h := searchServer(st)
	rec, out := doSearch(t, h, "GET", "/api/catalog/search?q=x&type=release&counts=1", "", false)
	if rec.Code != 200 || st.gotOpts != (store.SearchOptions{Type: "release", Counts: true}) {
		t.Fatalf("status %d opts %+v", rec.Code, st.gotOpts)
	}
	if c, _ := out["counts"].(map[string]any); c["release"] != float64(40) || c["performer"] != float64(2) {
		t.Errorf("counts = %v", out["counts"])
	}
	if rec, _ := doSearch(t, h, "GET", "/api/catalog/search?q=x&type=tag", "", false); rec.Code != 400 {
		t.Errorf("bad type: %d", rec.Code)
	}
	st.result.Counts = nil
	if _, out = doSearch(t, h, "GET", "/api/catalog/search?q=x", "", false); out["counts"] != nil || st.gotOpts != (store.SearchOptions{}) {
		t.Errorf("no counts asked: %v %+v", out["counts"], st.gotOpts)
	}
}

func TestTrendingTagChips(t *testing.T) {
	st := newStubRecs()
	rec := do(recsMux(st), "GET", "/api/trending?period=week&tag=drama", "", "")
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 200 || st.gotTag != "drama" {
		t.Fatalf("status %d tag %q", rec.Code, st.gotTag)
	}
	tags, _ := out["tags"].([]any)
	if len(tags) != 1 || tags[0].(map[string]any)["slug"] != "drama" {
		t.Errorf("tags = %v", out["tags"])
	}
}

type stubSearchHistory struct {
	keys    []string // normalized
	saved   []string // as typed
	cleared bool
}

func (s *stubSearchHistory) SearchHistory(context.Context, int64, int) ([]string, error) {
	return s.saved, nil
}
func (s *stubSearchHistory) RecordSearch(_ context.Context, _ int64, q, display string) error {
	s.keys = append([]string{q}, s.keys...)
	s.saved = append([]string{display}, s.saved...)
	return nil
}
func (s *stubSearchHistory) ClearSearchHistory(context.Context, int64) (int64, error) {
	n := int64(len(s.saved))
	s.saved, s.cleared = nil, true
	return n, nil
}

func TestSearchHistoryRoutes(t *testing.T) {
	hist := &stubSearchHistory{}
	srv := &Server{accounts: &stubAccounts{}, searchHist: hist}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	if rec := do(mux, "GET", "/api/me/search-history", "", ""); rec.Code != 401 {
		t.Errorf("anonymous: %d", rec.Code)
	}
	if rec := do(mux, "POST", "/api/me/search-history", "good", `{"q":"  夜航  的人 "}`); rec.Code != 200 || len(hist.saved) != 1 || hist.saved[0] != "夜航 的人" {
		t.Errorf("record: %d %v", rec.Code, hist.saved)
	}
	if rec := do(mux, "POST", "/api/me/search-history", "good", `{"q":" Summer  Ｎight "}`); rec.Code != 200 || hist.saved[0] != "Summer Ｎight" || hist.keys[0] != "summer night" || !strings.Contains(rec.Body.String(), `"q":"Summer Ｎight"`) {
		t.Errorf("record as typed: %d %v %v %s", rec.Code, hist.saved, hist.keys, rec.Body)
	}
	for _, body := range []string{`{"q":"   "}`, `{"q":"` + strings.Repeat("x", 300) + `"}`, `{"query":"x"}`} {
		if rec := do(mux, "POST", "/api/me/search-history", "good", body); rec.Code != 400 {
			t.Errorf("%s: %d", body, rec.Code)
		}
	}
	rec := do(mux, "GET", "/api/me/search-history", "good", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "夜航 的人") {
		t.Errorf("list: %d %s", rec.Code, rec.Body)
	}
	if rec = do(mux, "DELETE", "/api/me/search-history", "good", ""); rec.Code != 200 || !hist.cleared || !strings.Contains(rec.Body.String(), `"deleted":2`) {
		t.Errorf("clear: %d %s", rec.Code, rec.Body)
	}
}
