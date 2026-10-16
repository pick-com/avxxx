package store_test

// Danmaku language filter and personal block words (PRD 5.5, migration 045)
// end to end, in an isolated database: the lang backfill runs over every
// danmaku row on Open.

import (
	"avxxx/internal/api"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDanmakuFiltersIntegration(t *testing.T) {
	store.OpenIsolatedTestDB(t, "danmaku_filters")
	ctx := context.Background()
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	sfx := fmt.Sprint(time.Now().UnixNano())
	m, err := s.Register(ctx, "dmf-"+sfx+"@example.test", "password123", "")
	if err != nil {
		t.Fatal(err)
	}
	token := m["token"].(string)
	uid := m["user"].(map[string]any)["id"].(int64)
	slug := "dmf-" + sfx
	var rel int64
	if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,'Filters','published',100) RETURNING id`, slug).Scan(&rel); err != nil {
		t.Fatal(err)
	}

	// ---- backfill: rows from before 045 (empty or a client tag) get the detected language on Open
	legacy := map[string]string{"前方高能": "zh", "すごい": "ja", "대박": "ko", "สนุก": "th", "xem lại đi": "vi", "¡olé!": "es", "nice": "en", "23333": "und", "神回ですね": "ja"}
	for body := range legacy {
		lang := ""
		if body == "nice" {
			lang = "zh-CN" // a client tag, not a detected value
		}
		if _, err := s.Exec(ctx, `INSERT INTO danmaku(release_id,t_ms,body,lang,user_id) VALUES($1,1000,$2,$3,$4)`, rel, body, lang, uid); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	if s, err = store.Open(ctx); err != nil { // re-applies every migration, 045 included
		t.Fatal(err)
	}
	rows, err := s.Query(ctx, `SELECT body,lang FROM danmaku WHERE release_id=$1`, rel)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var body, lang string
		if err := rows.Scan(&body, &lang); err != nil {
			t.Fatal(err)
		}
		if legacy[body] != lang {
			t.Errorf("backfilled lang of %q = %q, want %q", body, lang, legacy[body])
		}
	}
	rows.Close()

	h := api.New(s, nil)
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		var req *http.Request
		if body != "" {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(method, path, nil)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	// ---- POST detects the language; the client's lang is only a hint for Latin text
	for _, c := range []struct{ body, hint, want string }{
		{"カッコイイ", "zh-CN", "ja"},
		{"que bueno", "es", "es"},
		{"8888", "", "und"},
	} {
		code, d := do("POST", "/api/releases/"+slug+"/danmaku", fmt.Sprintf(`{"t_ms":5000,"body":%q,"lang":%q}`, c.body, c.hint))
		if code != http.StatusCreated || d["lang"] != c.want {
			t.Errorf("post %q (hint %q): %d %v, want lang %s", c.body, c.hint, code, d, c.want)
		}
	}
	code, page := do("GET", "/api/releases/"+slug+"/danmaku?from_ms=0&to_ms=60000", "")
	langs := map[string]bool{}
	list, _ := page["items"].([]any)
	for _, x := range list {
		langs[x.(map[string]any)["lang"].(string)] = true
	}
	if code != http.StatusOK || !langs["ja"] || !langs["es"] || !langs["und"] || !langs["zh"] {
		t.Errorf("GET danmaku langs: %d %v", code, langs)
	}

	// ---- personal block words sync through settings
	code, st := do("GET", "/api/me/settings", "")
	if w, ok := st["danmaku_block_words"].([]any); code != http.StatusOK || !ok || len(w) != 0 {
		t.Fatalf("default block words: %d %v", code, st["danmaku_block_words"])
	}
	code, st = do("PATCH", "/api/me/settings", `{"danmaku_block_words":[" 剧透 ","Spoiler","SPOILER"]}`)
	if code != http.StatusOK {
		t.Fatalf("patch block words: %d %v", code, st)
	}
	got, err := s.Settings(ctx, uid)
	if err != nil || !slices.Equal(got.DanmakuBlockWords, []string{"剧透", "Spoiler"}) {
		t.Errorf("stored block words = %v %v", got.DanmakuBlockWords, err)
	}
	// Other settings patches keep the list.
	if code, _ = do("PATCH", "/api/me/settings", `{"autoplay":false}`); code != http.StatusOK {
		t.Fatalf("patch autoplay: %d", code)
	}
	if got, _ = s.Settings(ctx, uid); len(got.DanmakuBlockWords) != 2 || got.Autoplay {
		t.Errorf("after autoplay patch: %+v", got)
	}
	if code, _ = do("PATCH", "/api/me/settings", `{"danmaku_block_words":["`+strings.Repeat("长", 21)+`"]}`); code != http.StatusBadRequest {
		t.Errorf("over-long word: %d", code)
	}
	if code, _ = do("PATCH", "/api/me/settings", `{"danmaku_block_words":[]}`); code != http.StatusOK {
		t.Errorf("clear: %d", code)
	}
	if got, _ = s.Settings(ctx, uid); len(got.DanmakuBlockWords) != 0 {
		t.Errorf("cleared block words = %v", got.DanmakuBlockWords)
	}
}
