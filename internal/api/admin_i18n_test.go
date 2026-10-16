package api

import (
	"avxxx/internal/i18n"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type stubI18n struct {
	err       error // returned by every store call when set
	set       i18n.Set
	ui        map[string]string
	gotFilter store.TaskFilter
	gotRef    i18n.Ref
	gotLang   string
	gotValue  string
	gotAction string
	gotBy     string
	gotLangs  []string
	gotRefs   []i18n.Ref
	readCalls int
}

func (s *stubI18n) PublicTranslations(_ context.Context, langs []string, refs []i18n.Ref) (i18n.Set, error) {
	s.readCalls++
	s.gotLangs, s.gotRefs = langs, refs
	return s.set, s.err
}
func (s *stubI18n) TranslationTasks(_ context.Context, f store.TaskFilter) ([]store.TranslationTask, string, error) {
	s.gotFilter = f
	return []store.TranslationTask{{EntityType: "release", EntityID: "1", Field: "title", Lang: "en", Status: "missing"}}, "", s.err
}
func (s *stubI18n) TranslationStats(context.Context) (map[string]any, error) {
	return map[string]any{"total_fields": 3}, s.err
}
func (s *stubI18n) UpsertTranslation(_ context.Context, ref i18n.Ref, lang, value, by string) (store.TranslationTask, error) {
	s.gotRef, s.gotLang, s.gotValue, s.gotBy = ref, lang, value, by
	return store.TranslationTask{EntityType: ref.EntityType, EntityID: ref.EntityID, Field: ref.Field, Lang: lang, Status: "draft"}, s.err
}
func (s *stubI18n) TransitionTranslation(_ context.Context, ref i18n.Ref, lang, action, by string) (store.TranslationTask, error) {
	s.gotRef, s.gotLang, s.gotAction, s.gotBy = ref, lang, action, by
	return store.TranslationTask{Status: "reviewed"}, s.err
}
func (s *stubI18n) MarkStaleTranslations(context.Context) (int64, error) { return 2, s.err }
func (s *stubI18n) GlossaryTerms(context.Context, string, string, int) ([]store.GlossaryEntry, error) {
	return []store.GlossaryEntry{}, s.err
}
func (s *stubI18n) UpsertGlossaryTerm(_ context.Context, g store.GlossaryEntry, _ string) (store.GlossaryEntry, error) {
	g.ID = 1
	return g, s.err
}
func (s *stubI18n) UpdateGlossaryTerm(_ context.Context, id int64, g store.GlossaryEntry, _ string) (store.GlossaryEntry, error) {
	g.ID = id
	return g, s.err
}
func (s *stubI18n) DeleteGlossaryTerm(context.Context, int64) error { return s.err }
func (s *stubI18n) UIStrings(context.Context, string, string, string) ([]store.UIString, error) {
	return []store.UIString{}, s.err
}
func (s *stubI18n) UpsertUIString(_ context.Context, u store.UIString, _ string) (store.UIString, error) {
	return u, s.err
}
func (s *stubI18n) PublicUIStrings(context.Context, string) (map[string]string, error) {
	return s.ui, s.err
}

func i18nMux(st *stubI18n) *http.ServeMux {
	srv := &Server{adminToken: "secret", i18n: st}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestAdminI18nStatusCodes(t *testing.T) {
	const tr = "/api/admin/i18n/translations/"
	unique := &pgconn.PgError{Code: "23505"}
	tests := []struct {
		name, method, path, body string
		err                      error
		want                     int
		wantBody                 string
	}{
		{"tasks default", "GET", "/api/admin/i18n/translations", "", nil, 200, `"items"`},
		{"tasks filtered", "GET", "/api/admin/i18n/translations?lang=zh-TW&status=stale&entity_type=tag&limit=5", "", nil, 200, `"items"`},
		{"tasks missing", "GET", "/api/admin/i18n/translations?lang=en&missing=true", "", nil, 200, `"status":"missing"`},
		{"tasks missing needs lang", "GET", "/api/admin/i18n/translations?missing=true", "", nil, 400, "needs lang"},
		{"tasks bad lang", "GET", "/api/admin/i18n/translations?lang=fr", "", nil, 400, "unsupported lang"},
		{"tasks bad status", "GET", "/api/admin/i18n/translations?status=published", "", nil, 400, "invalid status"},
		{"tasks bad entity", "GET", "/api/admin/i18n/translations?entity_type=video", "", nil, 400, "invalid entity_type"},
		{"tasks bad missing", "GET", "/api/admin/i18n/translations?missing=maybe", "", nil, 400, "invalid missing"},
		{"tasks bad cursor", "GET", "/api/admin/i18n/translations?cursor=zz", "", nil, 400, "invalid cursor"},
		{"tasks store failure", "GET", "/api/admin/i18n/translations", "", errors.New("boom"), 500, "request failed"},
		{"stats", "GET", "/api/admin/i18n/stats", "", nil, 200, `"total_fields":3`},
		{"mark stale", "POST", "/api/admin/i18n/translations/mark-stale", "", nil, 200, `"marked_stale":2`},
		{"upsert ok", "PUT", tr + "release/12/title/zh-TW", `{"value":" 標題 "}`, nil, 200, `"status":"draft"`},
		{"upsert tag", "PUT", tr + "tag/big-tits/name/en", `{"value":"x"}`, nil, 200, `"entity_id":"big-tits"`},
		{"upsert empty value", "PUT", tr + "release/12/title/zh-TW", `{"value":"  "}`, nil, 400, "value must be"},
		{"upsert bad json", "PUT", tr + "release/12/title/zh-TW", `{`, nil, 400, "invalid JSON body"},
		{"upsert bad entity", "PUT", tr + "video/12/title/en", `{"value":"x"}`, nil, 400, "unknown entity_type"},
		{"upsert bad field", "PUT", tr + "release/12/name/en", `{"value":"x"}`, nil, 400, "unknown field"},
		{"upsert bad id", "PUT", tr + "release/abc/title/en", `{"value":"x"}`, nil, 400, "invalid entity_id"},
		{"upsert bad lang", "PUT", tr + "release/12/title/zh-tw", `{"value":"x"}`, nil, 400, "unsupported lang"},
		{"upsert missing entity", "PUT", tr + "release/12/title/en", `{"value":"x"}`, pgx.ErrNoRows, 404, "not found"},
		{"submit ok", "POST", tr + "release/12/title/en/submit", "", nil, 200, `"status"`},
		{"submit wrong status", "POST", tr + "release/12/title/en/submit", "", i18n.ErrTransition, 409, "invalid status transition"},
		{"review missing", "POST", tr + "performer/3/name/ja/review", "", pgx.ErrNoRows, 404, "not found"},
		{"review wrong status", "POST", tr + "performer/3/name/ja/review", "", i18n.ErrTransition, 409, "invalid status transition"},
		{"review bad lang", "POST", tr + "performer/3/name/de/review", "", nil, 400, "unsupported lang"},
		{"glossary list", "GET", "/api/admin/i18n/glossary?lang=ja&q=x", "", nil, 200, `"items":[]`},
		{"glossary list bad lang", "GET", "/api/admin/i18n/glossary?lang=xx", "", nil, 400, "unsupported lang"},
		{"glossary create", "POST", "/api/admin/i18n/glossary", `{"term":"中出し","lang":"en","translation":"creampie"}`, nil, 200, `"id":1`},
		{"glossary create bad lang", "POST", "/api/admin/i18n/glossary", `{"term":"a","lang":"de","translation":"b"}`, nil, 400, "unsupported lang"},
		{"glossary create no term", "POST", "/api/admin/i18n/glossary", `{"lang":"en","translation":"b"}`, nil, 400, "term is required"},
		{"glossary create no translation", "POST", "/api/admin/i18n/glossary", `{"term":"a","lang":"en"}`, nil, 400, "translation is required"},
		{"glossary update", "PUT", "/api/admin/i18n/glossary/9", `{"term":"a","lang":"en","translation":"b"}`, nil, 200, `"id":9`},
		{"glossary update clash", "PUT", "/api/admin/i18n/glossary/9", `{"term":"a","lang":"en","translation":"b"}`, unique, 409, "conflict"},
		{"glossary update missing", "PUT", "/api/admin/i18n/glossary/9", `{"term":"a","lang":"en","translation":"b"}`, pgx.ErrNoRows, 404, "not found"},
		{"glossary update bad id", "PUT", "/api/admin/i18n/glossary/x", `{}`, nil, 400, "invalid id"},
		{"glossary delete", "DELETE", "/api/admin/i18n/glossary/9", "", nil, 200, `"ok":true`},
		{"glossary delete missing", "DELETE", "/api/admin/i18n/glossary/9", "", pgx.ErrNoRows, 404, "not found"},
		{"ui list", "GET", "/api/admin/i18n/ui?lang=ko&status=reviewed&prefix=nav.", "", nil, 200, `"items":[]`},
		{"ui list bad status", "GET", "/api/admin/i18n/ui?status=stale", "", nil, 400, "invalid status"},
		{"ui upsert", "PUT", "/api/admin/i18n/ui/ko/nav.home", `{"value":"홈"}`, nil, 200, `"status":"draft"`},
		{"ui upsert reviewed", "PUT", "/api/admin/i18n/ui/ko/nav.home", `{"value":"홈","status":"reviewed"}`, nil, 200, `"status":"reviewed"`},
		{"ui upsert bad status", "PUT", "/api/admin/i18n/ui/ko/nav.home", `{"value":"홈","status":"stale"}`, nil, 400, "invalid status"},
		{"ui upsert bad key", "PUT", "/api/admin/i18n/ui/ko/.home", `{"value":"홈"}`, nil, 400, "invalid key"},
		{"ui upsert empty", "PUT", "/api/admin/i18n/ui/ko/nav.home", `{"value":""}`, nil, 400, "value must be"},
		{"ui upsert bad lang", "PUT", "/api/admin/i18n/ui/fr/nav.home", `{"value":"x"}`, nil, 400, "unsupported lang"},
		{"public ui unknown lang", "GET", "/api/i18n/ui/fr", "", nil, 404, "not found"},
		{"public ui failure", "GET", "/api/i18n/ui/en", "", errors.New("boom"), 500, "request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &stubI18n{err: tt.err}
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			i18nMux(st).ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %s, want it to contain %s", rec.Body.String(), tt.wantBody)
			}
		})
	}
}

func TestAdminI18nPassesArguments(t *testing.T) {
	st := &stubI18n{}
	mux := i18nMux(st)
	do := func(method, path, body string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("X-Admin-User", " editor-1 ")
		mux.ServeHTTP(httptest.NewRecorder(), req)
	}
	do("GET", "/api/admin/i18n/translations?lang=th&missing=1&entity_type=performer&limit=7", "")
	if f := st.gotFilter; f.Lang != "th" || !f.Missing || f.EntityType != "performer" || f.Limit != 7 {
		t.Errorf("filter %+v", f)
	}
	do("PUT", "/api/admin/i18n/translations/performer/5/biography/vi/", `{"value":"x"}`) // trailing slash: no route
	do("PUT", "/api/admin/i18n/translations/performer/5/biography/vi", `{"value":"  Tiểu sử "}`)
	if st.gotRef != (i18n.Ref{EntityType: "performer", EntityID: "5", Field: "biography"}) || st.gotLang != "vi" || st.gotValue != "Tiểu sử" || st.gotBy != "editor-1" {
		t.Errorf("upsert got %+v %q %q %q", st.gotRef, st.gotLang, st.gotValue, st.gotBy)
	}
	do("POST", "/api/admin/i18n/translations/tag/creampie/name/es/review", "")
	if st.gotAction != i18n.ActionReview || st.gotRef.EntityID != "creampie" || st.gotLang != "es" {
		t.Errorf("review got %q %+v %q", st.gotAction, st.gotRef, st.gotLang)
	}
	do("POST", "/api/admin/i18n/translations/tag/creampie/name/es/submit", "")
	if st.gotAction != i18n.ActionSubmit {
		t.Errorf("submit got %q", st.gotAction)
	}
}

func TestAdminI18nRequiresToken(t *testing.T) {
	rec := httptest.NewRecorder()
	i18nMux(&stubI18n{}).ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/i18n/translations", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}

func TestPublicUIStringsETag(t *testing.T) {
	st := &stubI18n{ui: map[string]string{"nav.home": "首頁", "nav.discover": "探索"}}
	mux := i18nMux(st)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/i18n/ui/zh-TW", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, st.ui) {
		t.Fatalf("body %s (%v)", rec.Body.String(), err)
	}
	etag := rec.Header().Get("ETag")
	if !strings.HasPrefix(etag, `"`) || rec.Header().Get("Cache-Control") == "" || rec.Header().Get("Content-Language") != "zh-TW" {
		t.Errorf("headers %v", rec.Header())
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		req := httptest.NewRequest("GET", "/api/i18n/ui/zh-TW", nil)
		req.Header.Set("If-None-Match", inm)
		rec2 := httptest.NewRecorder()
		mux.ServeHTTP(rec2, req)
		if rec2.Code != http.StatusNotModified || rec2.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: status %d body %q", inm, rec2.Code, rec2.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/api/i18n/ui/zh-TW", nil)
	req.Header.Set("If-None-Match", `"stale"`)
	rec3 := httptest.NewRecorder()
	mux.ServeHTTP(rec3, req)
	if rec3.Code != 200 {
		t.Errorf("mismatched etag: status %d", rec3.Code)
	}
	// Different content, different ETag.
	st.ui["nav.home"] = "主頁"
	rec4 := httptest.NewRecorder()
	mux.ServeHTTP(rec4, httptest.NewRequest("GET", "/api/i18n/ui/zh-TW", nil))
	if rec4.Header().Get("ETag") == etag {
		t.Error("ETag did not change with content")
	}
	// Empty bundle is still a JSON object.
	st.ui = nil
	rec5 := httptest.NewRecorder()
	mux.ServeHTTP(rec5, httptest.NewRequest("GET", "/api/i18n/ui/th", nil))
	if strings.TrimSpace(rec5.Body.String()) != "null" && strings.TrimSpace(rec5.Body.String()) != "{}" {
		t.Errorf("empty body %q", rec5.Body.String())
	}
}

func TestContentLang(t *testing.T) {
	tests := []struct {
		query, accept, want string
		wantErr             bool
	}{
		{"", "", "", false},
		{"lang=zh-TW", "", "zh-TW", false},
		{"lang=zh-hant", "en", "zh-TW", false},
		{"lang=en", "ja", "en", false},
		{"lang=", "ko,en;q=0.5", "ko", false},
		{"", "fr-FR, ja;q=0.3", "ja", false},
		{"", "fr-FR", "", false},
		{"lang=fr", "en", "", true},
	}
	for _, tt := range tests {
		req := httptest.NewRequest("GET", "/api/releases?"+tt.query, nil)
		if tt.accept != "" {
			req.Header.Set("Accept-Language", tt.accept)
		}
		got, err := contentLang(req)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("%q/%q: got %q,%v", tt.query, tt.accept, got, err)
		}
	}
}

// Catalog routes reject an unsupported ?lang= before touching the store.
func TestCatalogRejectsUnsupportedLang(t *testing.T) {
	mux := i18nMux(&stubI18n{})
	for _, p := range []string{"/api/releases?lang=fr", "/api/releases/abc?lang=fr", "/api/performers?lang=xx", "/api/performers/abc?lang=de", "/api/catalog/search?q=a&lang=it"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unsupported lang") {
			t.Errorf("%s: status %d body %s", p, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Language") {
			t.Errorf("%s: missing Vary", p)
		}
	}
}

func TestLocalizeCatalogPayloads(t *testing.T) {
	set := i18n.Set{}
	set.Add(i18n.Ref{EntityType: "release", EntityID: "1", Field: "title"}, "zh-CN", "简体标题")
	set.Add(i18n.Ref{EntityType: "release", EntityID: "1", Field: "synopsis"}, "zh-TW", "繁體簡介")
	set.Add(i18n.Ref{EntityType: "tag", EntityID: "mature", Field: "name"}, "en", "Mature")
	set.Add(i18n.Ref{EntityType: "performer", EntityID: "5", Field: "name"}, "zh-TW", "月野")
	st := &stubI18n{set: set}
	srv := &Server{i18n: st}
	ctx := context.Background()

	// No language: nothing loaded, payload untouched.
	l, err := srv.localizer(ctx, "", releaseRefs([]model.Release{{ID: 1}}))
	if l != nil || err != nil || st.readCalls != 0 {
		t.Fatalf("no lang: %v %v calls=%d", l, err, st.readCalls)
	}
	rs := []model.Release{{ID: 1, CanonicalTitle: "Original"}, {ID: 2, CanonicalTitle: "Two"}}
	if got := localizeReleases(nil, rs); !reflect.DeepEqual(got, rs) {
		t.Errorf("nil localizer changed releases")
	}

	// List: one batched read for every row, chain from zh-TW.
	l, err = srv.localizer(ctx, "zh-TW", releaseRefs(rs))
	if err != nil || st.readCalls != 1 || len(st.gotRefs) != 2 || !reflect.DeepEqual(st.gotLangs, []string{"zh-TW", "zh-CN", "en"}) {
		t.Fatalf("batch read: err=%v calls=%d refs=%v langs=%v", err, st.readCalls, st.gotRefs, st.gotLangs)
	}
	b, _ := json.Marshal(withLang(page(localizeReleases(l, rs), ""), l))
	for _, want := range []string{`"lang":"zh-TW"`, `"canonical_title":"Original"`, `"display_title":"简体标题","display_title_lang":"zh-CN"`, `"display_title":"Two","display_title_lang":"original"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("list %s missing %s", b, want)
		}
	}

	// Detail: title, synopsis and tags in one read.
	d := store.ReleaseDetail{Release: model.Release{ID: 1, CanonicalTitle: "Original"}, Synopsis: "あらすじ",
		Tags: []store.TagRef{{Slug: "mature", Name: "熟女"}, {Slug: "other", Name: "Other"}}}
	l, _ = srv.localizer(ctx, "en", releaseDetailRefs(d))
	if len(st.gotRefs) != 4 {
		t.Errorf("detail refs %v", st.gotRefs)
	}
	b, _ = json.Marshal(localizeReleaseDetail(l, d))
	for _, want := range []string{`"lang":"en"`, `"synopsis":"あらすじ"`, `"display_synopsis":"繁體簡介","display_synopsis_lang":"zh-TW"`, `"display_title":"简体标题"`,
		`{"slug":"mature","name":"熟女","display_name":"Mature","display_name_lang":"en"}`, `{"slug":"other","name":"Other","display_name":"Other","display_name_lang":"original"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("detail %s missing %s", b, want)
		}
	}
	if strings.Count(string(b), `"tags"`) != 1 {
		t.Errorf("tags emitted twice: %s", b)
	}

	// ja goes straight to the original.
	l, _ = srv.localizer(ctx, "ja", releaseDetailRefs(d))
	b, _ = json.Marshal(localizeReleaseDetail(l, d))
	if !strings.Contains(string(b), `"display_title":"Original","display_title_lang":"original"`) {
		t.Errorf("ja detail %s", b)
	}

	// Performer list, detail map and search maps.
	ps := []store.PerformerListItem{{Performer: model.Performer{ID: 5, CanonicalName: "Tsukino"}}}
	l, _ = srv.localizer(ctx, "zh-CN", performerRefs(ps))
	b, _ = json.Marshal(localizePerformers(l, ps))
	if !strings.Contains(string(b), `"canonical_name":"Tsukino"`) || !strings.Contains(string(b), `"display_name":"月野","display_name_lang":"zh-TW"`) {
		t.Errorf("performers %s", b)
	}
	m := map[string]any{"id": float64(5), "canonical_name": "Tsukino", "biography": nil}
	localizePerformerDetail(l, m)
	if m["display_name"] != "月野" || m["lang"] != "zh-CN" {
		t.Errorf("performer detail %v", m)
	}
	if _, ok := m["display_biography"]; ok {
		t.Errorf("empty biography should not get display_biography: %v", m)
	}
	items := []map[string]any{{"type": "release", "id": int64(1), "name": "Original"}, {"type": "performer", "id": int64(5), "name": "Tsukino"}}
	refs := searchRefs(items)()
	if refs[0] != (i18n.Ref{EntityType: "release", EntityID: "1", Field: "title"}) || refs[1] != (i18n.Ref{EntityType: "performer", EntityID: "5", Field: "name"}) {
		t.Errorf("search refs %v", refs)
	}
	localizeSearch(l, items)
	if items[0]["display_name"] != "简体标题" || items[1]["display_name"] != "月野" || items[1]["display_name_lang"] != "zh-TW" {
		t.Errorf("search %v", items)
	}

	// Store failure surfaces as an error.
	st.err = errors.New("boom")
	if _, err := srv.localizer(ctx, "en", releaseRefs(rs)); err == nil {
		t.Error("want error")
	}
}
