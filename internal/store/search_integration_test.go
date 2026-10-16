package store_test

// Search integration test (PRD 4.5, AdminSearch). Runs only under
// `make test-integration` (AVXXX_TEST_DATABASE_URL) in its own database
// (store.OpenIsolatedTestDB): the zero-result triage looks for near-matches
// across the whole catalog, so rows other tests leave behind (e.g. the
// "Bench 1000" titles of TestRecsLatencyIntegration) must not be visible.
// Every row it creates still carries a per-run suffix.

import (
	"avxxx/internal/api"
	"avxxx/internal/events"
	"avxxx/internal/identity"
	"avxxx/internal/search"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func searchHTTP(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if strings.HasPrefix(path, "/api/admin/") {
		req.Header.Set("Authorization", "Bearer search-admin")
		req.Header.Set("X-Admin-User", "search-editor")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func items(out map[string]any) []map[string]any {
	raw, _ := out["items"].([]any)
	res := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		m, _ := x.(map[string]any)
		res = append(res, m)
	}
	return res
}

func TestSearchIntegration(t *testing.T) {
	store.OpenIsolatedTestDB(t, "search")
	ctx := context.Background()
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	t.Setenv("ADMIN_TOKEN", "search-admin")
	h := api.New(s, nil)
	// Zero-padded: an unpadded sub-second suffix can be as short as "81000"
	// (macOS clocks tick in microseconds), and a short all-digit tail is
	// what made "没有的片81000" look like "Bench 1000".
	sfx := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	// Letters-only tag for product codes so they look like codes.
	letters := ""
	for _, c := range sfx[len(sfx)-4:] {
		letters += string(rune('a' + (c-'0')%26))
	}
	code := "zq" + letters // e.g. zqabcd
	word := "夜航" + letters // CJK title stem unique to this run
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) (id int64) {
		t.Helper()
		if err := s.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}

	// ---- SQL search_code_keys mirrors identity.CodeKeys
	for _, in := range []string{"JUQ-743", "juq743", "juq00743", "118abp00123", "fc2-ppv-1234567", "ABP-12", "ｊｕｑ－７４３", "x", "--", "SSIS-1000a"} {
		var got []string
		if err := s.QueryRow(ctx, `SELECT search_code_keys($1)`, in).Scan(&got); err != nil {
			t.Fatal(err)
		}
		want := identity.CodeKeys(in)
		if want == nil {
			want = []string{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("search_code_keys(%q) = %v, CodeKeys = %v", in, got, want)
		}
	}
	t.Logf("pg_trgm installed: %v", s.SearchTrgm(ctx))

	// ---- catalog
	relA := scalar(`INSERT INTO releases(slug,canonical_title,title_ja,status,release_date) VALUES($1,$2,$3,'published','2024-01-02') RETURNING id`,
		"search-a-"+sfx, word+"的人", word+"の人")
	relB := scalar(`INSERT INTO releases(slug,canonical_title,status) VALUES($1,$2,'published') RETURNING id`, "search-b-"+sfx, "Summer Rain "+letters)
	relUp := scalar(`INSERT INTO releases(slug,canonical_title,status,scheduled_at) VALUES($1,$2,'upcoming',now()+interval '3 days') RETURNING id`,
		"search-up-"+sfx, word+"的人 续")
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'dvd_code',$2,$3)`,
		relA, strings.ToUpper(code)+"-743", code+"-743")
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'content_id',$2,$2)`,
		relB, "118"+code+"00512")
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$3)`, relA, "Tag"+letters, "tag-"+letters)
	exec(`INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,reviewed_value)
		VALUES('release',$1,'title','en',$2,'reviewed',i18n_hash($3),$2)`, fmt.Sprint(relA), "Night Flight "+letters, word+"的人")
	perf := scalar(`INSERT INTO performers(slug,canonical_name,canonical_name_ja) VALUES($1,$2,$3) RETURNING id`,
		"search-p-"+sfx, "波多野"+letters, "はたの"+letters)
	exec(`INSERT INTO performer_aliases(performer_id,name,normalized_name) VALUES($1,$2,$3)`, perf, "Yui Hatano"+letters, identity.AliasForm("Yui Hatano"+letters))
	exec(`INSERT INTO release_performers(release_id,performer_id) VALUES($1,$2)`, relA, perf)

	search1 := func(q string) map[string]any {
		t.Helper()
		code, out := searchHTTP(t, h, "GET", "/api/catalog/search?q="+url.QueryEscape(q), "")
		if code != 200 {
			t.Fatalf("search %q: %d %v", q, code, out)
		}
		return out
	}
	first := func(out map[string]any) map[string]any {
		if it := items(out); len(it) > 0 {
			return it[0]
		}
		return map[string]any{}
	}

	// ---- product codes: every spelling hits the release first
	for _, q := range []string{code + "743", strings.ToUpper(code) + "-743", code + "00743", " " + strings.ToUpper(code) + " 743 "} {
		out := search1(q)
		f := first(out)
		if f["slug"] != "search-a-"+sfx || f["match"] != "code" {
			t.Errorf("code %q: first item %v", q, f)
		}
		if em, _ := out["exact_match"].(map[string]any); em["slug"] != "search-a-"+sfx {
			t.Errorf("code %q: exact_match %v", q, out["exact_match"])
		}
	}
	if f := first(search1(code + "512")); f["slug"] != "search-b-"+sfx { // loose key of a content id
		t.Errorf("content id loose key: %v", f)
	}

	// ---- cross-language: reviewed English title and performer alias
	if f := first(search1("night flight " + letters)); f["slug"] != "search-a-"+sfx || f["match"] != "translation" {
		t.Errorf("english title: %v", f)
	}
	out := search1("yui hatano" + letters)
	if f := first(out); f["slug"] != "search-p-"+sfx || f["match"] != "alias" || f["type"] != "performer" {
		t.Errorf("alias: %v", out)
	}
	// Performer cards come before releases.
	out = search1(word)
	if it := items(out); len(it) < 1 || it[0]["type"] != "release" {
		t.Errorf("title search: %v", out)
	}
	if _, ok := out["rescued"]; ok {
		t.Errorf("plain hit must not be rescued: %v", out)
	}

	// ---- modifier-stripping retry
	out = search1(word + "的人全集 在线看")
	if out["rescued"] != true || out["effective_query"] != word+"的人" || first(out)["slug"] != "search-a-"+sfx {
		t.Errorf("retry: %v", out)
	}
	out = search1("完全没有的片" + letters + " 高清")
	if len(items(out)) != 0 || out["rescued"] != nil {
		t.Errorf("unrescuable: %v", out)
	}

	// ---- synonyms and redirects (admin CRUD, then applied)
	code1, syn := searchHTTP(t, h, "POST", "/api/admin/search/synonyms", fmt.Sprintf(`{"term":"NF%s","canonical":"night flight %s"}`, letters, letters))
	if code1 != 201 || syn["created_by"] != "token:search-editor" || syn["term"] != "nf"+letters {
		t.Fatalf("create synonym: %d %v", code1, syn)
	}
	if c, _ := searchHTTP(t, h, "POST", "/api/admin/search/synonyms", fmt.Sprintf(`{"term":"nf%s","canonical":"x"}`, letters)); c != 409 {
		t.Errorf("duplicate synonym: %d", c)
	}
	out = search1("NF" + letters)
	if out["effective_query"] != "night flight "+letters || first(out)["slug"] != "search-a-"+sfx {
		t.Errorf("synonym applied: %v", out)
	}
	synID := int64(syn["id"].(float64))
	if c, _ := searchHTTP(t, h, "PUT", fmt.Sprintf("/api/admin/search/synonyms/%d", synID), fmt.Sprintf(`{"term":"nf%s","canonical":"%s的人"}`, letters, word)); c != 200 {
		t.Errorf("update synonym: %d", c)
	}
	c, red := searchHTTP(t, h, "POST", "/api/admin/search/redirects", fmt.Sprintf(`{"term":"怎么下载%s","target_url":"/help/download","reason":"help"}`, letters))
	if c != 201 {
		t.Fatalf("create redirect: %d %v", c, red)
	}
	out = search1("怎么下载" + letters)
	if r, _ := out["redirect"].(map[string]any); r["target_url"] != "/help/download" {
		t.Errorf("redirect: %v", out)
	}
	if c, _ := searchHTTP(t, h, "DELETE", fmt.Sprintf("/api/admin/search/synonyms/%d", synID), ""); c != 204 {
		t.Errorf("delete synonym: %d", c)
	}
	if c, _ := searchHTTP(t, h, "DELETE", fmt.Sprintf("/api/admin/search/synonyms/%d", synID), ""); c != 404 {
		t.Errorf("delete synonym twice: %d", c)
	}

	// ---- suggestions: code prefix, title, upcoming, tag
	c, sug := searchHTTP(t, h, "GET", "/api/search/suggest?q="+url.QueryEscape(word), "")
	types := map[string]bool{}
	for _, it := range items(sug) {
		types[it["type"].(string)] = true
	}
	if c != 200 || !types["release"] || !types["upcoming"] || len(items(sug)) > search.SuggestLimit {
		t.Errorf("suggest title: %d %v", c, sug)
	}
	_, sug = searchHTTP(t, h, "GET", "/api/search/suggest?q="+code+"-7", "")
	if it := items(sug); len(it) == 0 || it[0]["slug"] != "search-a-"+sfx {
		t.Errorf("suggest code prefix: %v", sug)
	}
	_, sug = searchHTTP(t, h, "GET", "/api/search/suggest?q=tag"+letters, "")
	if it := items(sug); len(it) == 0 || it[0]["type"] != "tag" || it[0]["slug"] != "tag-"+letters {
		t.Errorf("suggest tag: %v", sug)
	}
	_, sug = searchHTTP(t, h, "GET", "/api/search/suggest?q=yui%20hatano"+letters, "")
	if it := items(sug); len(it) == 0 || it[0]["type"] != "performer" {
		t.Errorf("suggest alias: %v", sug)
	}
	_ = relUp

	// ---- events -> search_stats job -> daily aggregate + hot board
	now := time.Now().UTC()
	var evs []events.Event
	n := 0
	add := func(session, q string, results int, rank int, rescued bool) {
		n++
		uid := int64(424242) // user ids exist on events but must never reach the aggregate
		props := map[string]any{"query": q, "result_count": results, "is_retry": false}
		if rank > 0 {
			props["clicked_rank"] = rank
		}
		if rescued {
			props["rescued"] = true
		}
		evs = append(evs, events.Event{EventID: fmt.Sprintf("search-%s-%d", sfx, n), Name: "search", SchemaVer: 1, TSClient: now, TSServer: now.Add(-time.Minute),
			UserID: &uid, SessionID: fmt.Sprintf("sess-%s-%s", sfx, session), Platform: "web", AppVer: "1", UILang: "zh-CN",
			Consent: []string{"necessary", "analytics"}, Props: props})
	}
	hot := word + "的人"
	for i, sess := range []string{"a", "b", "c", "d"} {
		add(sess, "  "+strings.ToUpper(hot)+" ", 3, i%2+1, false) // normalizes to hot; 2 clicks at rank 1, 2 at rank 2
	}
	add("a", hot, 3, 25, false) // a click beyond the first page
	missing := "没有的片" + sfx
	for _, sess := range []string{"a", "b", "c", "d", "e"} {
		add(sess, missing, 0, 0, false)
	}
	add("f", hot+"全集", 2, 0, true)
	add("g", "x", 0, 0, false) // too short: dropped
	blocked := "屏蔽词" + letters
	for _, sess := range []string{"a", "b", "c"} {
		add(sess, blocked, 4, 0, false)
	}
	if _, err := s.InsertEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if c, _ := searchHTTP(t, h, "POST", "/api/admin/search/blocklist", fmt.Sprintf(`{"term":"%s","reason":"test"}`, blocked)); c != 201 {
		t.Fatalf("block: %d", c)
	}
	rep, err := (&search.Runner{Store: s}).Run(ctx)
	if err != nil {
		t.Fatalf("search job: %v (%+v)", err, rep)
	}
	type daily struct{ searches, zero, clicks, fp, rescued int }
	get := func(q string) daily {
		var d daily
		err := s.QueryRow(ctx, `SELECT COALESCE(sum(searches),0),COALESCE(sum(zero_results),0),COALESCE(sum(clicks),0),COALESCE(sum(first_page_clicks),0),COALESCE(sum(rescued),0)
			FROM search_query_daily WHERE q_norm=$1`, q).Scan(&d.searches, &d.zero, &d.clicks, &d.fp, &d.rescued)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if d := get(hot); d != (daily{5, 0, 5, 4, 0}) {
		t.Errorf("daily %s = %+v", hot, d)
	}
	if d := get(missing); d != (daily{5, 5, 0, 0, 0}) {
		t.Errorf("daily %s = %+v", missing, d)
	}
	if d := get(hot + "全集"); d.rescued != 1 {
		t.Errorf("rescued row %+v", d)
	}
	if d := get("x"); d.searches != 0 {
		t.Errorf("short query kept: %+v", d)
	}
	var cols int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name IN ('search_query_daily','search_trending') AND column_name IN ('user_id','device_id','session_id')`).Scan(&cols); err != nil || cols != 0 {
		t.Errorf("aggregate tables carry identifiers: %d %v", cols, err)
	}
	// Re-running is idempotent.
	if _, err := (&search.Runner{Store: s}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if d := get(hot); d.searches != 5 {
		t.Errorf("second run double-counted: %+v", d)
	}

	c, hotOut := searchHTTP(t, h, "GET", "/api/search/hot", "")
	var hotTerms []string
	for _, it := range items(hotOut) {
		hotTerms = append(hotTerms, it["q"].(string))
	}
	joined := "|" + strings.Join(hotTerms, "|") + "|"
	if c != 200 || !strings.Contains(joined, "|"+hot+"|") {
		t.Errorf("hot board missing %s: %v", hot, hotTerms)
	}
	if strings.Contains(joined, missing) || strings.Contains(joined, blocked) {
		t.Errorf("hot board has zero-result or blocked terms: %v", hotTerms)
	}

	// ---- AdminSearch board
	c, zr := searchHTTP(t, h, "GET", "/api/admin/search/zero-results?days=1&min=5", "")
	var term map[string]any
	for _, it := range items(zr) {
		if it["term"] == missing {
			term = it
		}
	}
	if c != 200 || term == nil || term["zero_results"].(float64) != 5 || term["class"] != string(search.ClassNotInLibrary) || term["class_label"] != "库里没有" {
		t.Errorf("zero-results: %d %v", c, term)
	}
	// A near-match term (the title with a typo) is "missed".
	var nm []store.NearMatch
	if nm, err = s.NearMatches(ctx, word+"的人们", 3); err != nil || len(nm) == 0 || nm[0].ID != relA {
		t.Errorf("near matches: %+v %v", nm, err)
	}
	c, st := searchHTTP(t, h, "GET", "/api/admin/search/stats?days=1", "")
	tot, _ := st["totals"].(map[string]any)
	if c != 200 || tot["searches"].(float64) < 11 || tot["zero_result_target"] != 0.05 {
		t.Errorf("stats: %d %v", c, st)
	}

	// ---- fixes from a term
	c, fix := searchHTTP(t, h, "POST", "/api/admin/search/terms/alias", fmt.Sprintf(`{"term":"Hatano Y %s","performer_id":%d,"reason":"zero-result term"}`, letters, perf))
	if c != 201 || fix["action"] != "add_alias" {
		t.Fatalf("alias fix: %d %v", c, fix)
	}
	if c, _ := searchHTTP(t, h, "POST", "/api/admin/search/terms/alias", fmt.Sprintf(`{"term":"Hatano Y %s","performer_id":%d}`, letters, perf)); c != 409 {
		t.Errorf("duplicate alias: %d", c)
	}
	if c, _ := searchHTTP(t, h, "POST", "/api/admin/search/terms/alias", `{"term":"nobody","performer_id":999999999}`); c != 404 {
		t.Errorf("missing performer: %d", c)
	}
	if f := first(search1("hatano y " + letters)); f["slug"] != "search-p-"+sfx || f["match"] != "alias" {
		t.Errorf("search by new alias: %v", f)
	}
	c, fix = searchHTTP(t, h, "POST", "/api/admin/search/terms/english-title", fmt.Sprintf(`{"term":"rain of summer %s","release_id":%d,"title":"Rain Of Summer %s"}`, letters, relB, letters))
	if c != 201 {
		t.Fatalf("english title fix: %d %v", c, fix)
	}
	if f := first(search1("rain of summer " + letters)); f["slug"] != "search-b-"+sfx || f["match"] != "translation" {
		t.Errorf("search by new english title: %v", f)
	}
	var actor, action string
	if err := s.QueryRow(ctx, `SELECT actor,action FROM review_events WHERE entity_type='release' AND entity_id=$1 ORDER BY id DESC LIMIT 1`, relB).Scan(&actor, &action); err != nil || actor != "token:search-editor" || action != "add_english_title" {
		t.Errorf("review event %q %q %v", actor, action, err)
	}
	if err := s.QueryRow(ctx, `SELECT actor FROM review_events WHERE entity_type='performer' AND entity_id=$1 AND action='add_alias' ORDER BY id DESC LIMIT 1`, perf).Scan(&actor); err != nil || actor != "token:search-editor" {
		t.Errorf("alias review event %q %v", actor, err)
	}
}
