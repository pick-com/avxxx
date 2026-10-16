package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// testTPDBToken is distinctive so any leak is easy to spot.
const testTPDBToken = "tpdbTESTtoken|9qXz7w"

// tpdbAPI serves the doc-derived fixtures like the real API: the bearer
// token is required (403 "Unauthorized" otherwise, as documented).
type tpdbAPI struct {
	*httptest.Server
	mu       sync.Mutex
	reqs     []*http.Request
	override http.HandlerFunc
}

func newTPDBAPI(t *testing.T) *tpdbAPI {
	t.Helper()
	page1, page2 := fixture(t, "theporndb/jav_page1.json"), fixture(t, "theporndb/jav_page2.json")
	scene, perf := fixture(t, "theporndb/jav_scene.json"), fixture(t, "theporndb/performer.json")
	a := &tpdbAPI{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		a.reqs = append(a.reqs, r.Clone(context.Background()))
		override := a.override
		a.mu.Unlock()
		if override != nil {
			override(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+testTPDBToken {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Unauthorized"}`)
			return
		}
		q := r.URL.Query()
		var body string
		switch {
		case r.URL.Path == "/jav" && q.Get("date") != "":
			// Incremental pass: only the newest page is in range.
			body = strings.Replace(page2, `"current_page": 2`, `"current_page": 1`, 1)
			body = strings.Replace(body, `"last": 2`, `"last": 1`, 1)
		case r.URL.Path == "/jav" && (q.Get("page") == "" || q.Get("page") == "1"):
			body = page1
		case r.URL.Path == "/jav" && q.Get("page") == "2":
			body = page2
		case strings.HasPrefix(r.URL.Path, "/jav/"):
			body = scene
		case strings.HasPrefix(r.URL.Path, "/performers/"):
			body = perf
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"Not Found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *tpdbAPI) set(h http.HandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.override = h
}

func (a *tpdbAPI) host() string { u, _ := url.Parse(a.URL); return u.Host }

func (a *tpdbAPI) requests() []*http.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*http.Request(nil), a.reqs...)
}

func (a *tpdbAPI) listURL() string { return a.URL + "/jav?orderBy=recently_released&per_page=2" }

func tpdbCrawl(t *testing.T, api *tpdbAPI, a *ThePornDBAdapter, cps CheckpointStore, sink RecordSink, start string, now time.Time) *Crawl {
	t.Helper()
	client, err := HTTPClientFor(a, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &Crawl{SourceID: 9, StartURL: start, Adapter: a, Fetcher: NewFetcher(client, FetchPolicy{}), Checkpoints: cps,
		Sink: RedactingSink{Sink: sink, Redactor: a}, Now: func() time.Time { return now }}
}

func tpdbFixtureRecords(t *testing.T, name, rawURL string) []Record {
	t.Helper()
	recs, err := NewThePornDBAdapterWith(testTPDBToken, "").Discover(context.Background(), Document{URL: rawURL, Status: 200, Body: []byte(fixture(t, name))})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestThePornDBReleaseMapping(t *testing.T) {
	recs := tpdbFixtureRecords(t, "theporndb/jav_page1.json", ThePornDBJAVListURL)
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	// The schema example verbatim: a code-shaped sku is taken as the code,
	// the example performer (gender "Male") is not credited.
	ex := recs[0]
	if ex.EntityType != Release || ex.SourceKey != "tpdb-jav-d284e2a0-789d-4e6c-beb9-6e61c24e8b84" || ex.URL != "https://api.theporndb.net/jav/d284e2a0-789d-4e6c-beb9-6e61c24e8b84" {
		t.Fatalf("example record %+v", ex)
	}
	if ex.Fields["dvd_code"] != "SKU-001" || ex.Fields["dvd_code_origin"] != "sku" || ex.Fields["maker"] != "Naughty Office" || ex.Fields["release_date"] != "2021-11-15" {
		t.Fatalf("example fields %v", ex.Fields)
	}
	if _, ok := ex.Fields["performers"]; ok {
		t.Fatalf("male performer credited: %v", ex.Fields["performers"])
	}
	if ex.Fields["director"] != "Test Test" {
		t.Fatalf("director %v", ex.Fields["director"])
	}

	r := recs[1]
	want := map[string]any{
		"title": "SONE-156 Placeholder Title", "theporndb_id": "5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01", "theporndb_numeric_id": "900001",
		"dvd_code": "SONE-156", "dvd_code_origin": "sku", "sku": "SONE-156", "external_id": "sone00156",
		"release_date": "2024-04-23", "maker": "S1 NO.1 STYLE", "theporndb_site_id": "900010", "director": "Placeholder Director",
		"cover_url": "https://cdn.theporndb.net/jav/placeholder/sone-156-cover.jpg", "theporndb_duration": "7200",
		"studio_url": "https://studio.example/works/sone00156", "theporndb_type": "JAV",
	}
	for k, v := range want {
		if r.Fields[k] != v {
			t.Errorf("%s = %#v, want %#v", k, r.Fields[k], v)
		}
	}
	for _, k := range []string{"maker_id", "runtime_minutes", "tags", "synopsis", "description", "content_id"} {
		if _, ok := r.Fields[k]; ok {
			t.Errorf("unmapped field %s present: %v", k, r.Fields[k])
		}
	}
	if got := r.Fields["performers"]; fmt.Sprint(got) != "[Placeholder Actress]" {
		t.Fatalf("performers %v", got)
	}
	refs := r.Fields["performer_refs"].([]map[string]string)
	if len(refs) != 1 || refs[0]["theporndb_performer_id"] != "7e8d5654-2ca4-46de-ac1e-8683f9e7f778" || refs[0]["source_key"] != "tpdb-performer-7e8d5654-2ca4-46de-ac1e-8683f9e7f778" {
		t.Fatalf("refs %v", refs)
	}
	imgs := r.Fields["image_urls"].(map[string]string)
	if imgs["image"] == "" || imgs["poster"] == "" || imgs["posters_full"] == "" || len(imgs) != 3 {
		t.Fatalf("images %v", imgs)
	}

	// external_id fallback for the code; a single resource response.
	p2 := tpdbFixtureRecords(t, "theporndb/jav_page2.json", ThePornDBJAVListURL+"&page=2")
	if len(p2) != 1 || p2[0].Fields["dvd_code"] != "ABP-123" || p2[0].Fields["dvd_code_origin"] != "external_id" {
		t.Fatalf("page 2 %+v", p2)
	}
	one := tpdbFixtureRecords(t, "theporndb/jav_scene.json", "https://api.theporndb.net/jav/5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01")
	if len(one) != 1 || one[0].SourceKey != r.SourceKey || one[0].Fields["dvd_code"] != "SONE-156" {
		t.Fatalf("single scene %+v", one)
	}
}

func TestThePornDBPerformerMapping(t *testing.T) {
	recs := tpdbFixtureRecords(t, "theporndb/performer.json", "https://api.theporndb.net/performers/32aa4b68-4dbe-49f2-92a5-0e54e5074f4b")
	if len(recs) != 1 {
		t.Fatalf("records %d", len(recs))
	}
	p := recs[0]
	want := map[string]any{"name": "Jessica Jaymes", "theporndb_performer_id": "32aa4b68-4dbe-49f2-92a5-0e54e5074f4b",
		"birth_date": "1990-01-01", "birth_place": "Florida, USA", "birthplace_code": "US", "nationality": "American",
		"height_cm": "160", "height_raw": "160cm", "measurements_raw": "34C-24-34", "cupsize_raw": "34C", "career_start_year": "2014",
		"disambiguation": "2005", "gender": "Male"}
	for k, v := range want {
		if p.Fields[k] != v {
			t.Errorf("%s = %#v, want %#v", k, p.Fields[k], v)
		}
	}
	if p.EntityType != Performer || p.SourceKey != "tpdb-performer-32aa4b68-4dbe-49f2-92a5-0e54e5074f4b" || fmt.Sprint(p.Fields["aliases"]) != "[Jessi Jaymes]" {
		t.Fatalf("performer %+v", p)
	}
	for _, k := range []string{"cup_size", "bust_cm", "waist_cm", "hip_cm", "name_ja", "biography", "ethnicity"} {
		if _, ok := p.Fields[k]; ok {
			t.Errorf("unmapped field %s present", k)
		}
	}
	list := tpdbFixtureRecords(t, "theporndb/performers_page1.json", "https://api.theporndb.net/performers?per_page=25")
	if len(list) != 2 {
		t.Fatalf("list %d", len(list))
	}
	partial := list[1].Fields
	if _, ok := partial["birth_date"]; ok || partial["birthday_type"] != "YEAR" || partial["birthday_raw"] != "1999-01-01" {
		t.Fatalf("partial birthday mapped: %v", partial)
	}
	if _, ok := partial["height_cm"]; ok || partial["height_raw"] != "158" {
		t.Fatalf("unit-less height mapped: %v", partial)
	}
}

func TestThePornDBDiscoverErrors(t *testing.T) {
	a := NewThePornDBAdapterWith(testTPDBToken, "")
	for _, tc := range []struct{ url, body string }{
		{"https://api.theporndb.net/scenes", `{"data":[]}`},
		{"https://api.theporndb.net/jav", `<html>`},
		{"https://api.theporndb.net/jav", `{"message":"Unauthorized"}`},
	} {
		if _, err := a.Discover(context.Background(), Document{URL: tc.url, Body: []byte(tc.body)}); err == nil {
			t.Errorf("%s %s accepted", tc.url, tc.body)
		}
	}
	// Records without a UUID id or a title are dropped, not failed.
	recs, err := a.Discover(context.Background(), Document{URL: "https://api.theporndb.net/jav", Body: []byte(`{"data":[{"id":"12","title":"x"},{"id":"d284e2a0-789d-4e6c-beb9-6e61c24e8b84","title":""}],"meta":{}}`)})
	if err != nil || len(recs) != 0 {
		t.Fatalf("recs %v err %v", recs, err)
	}
}

func TestThePornDBNextPageAndIncrementalStart(t *testing.T) {
	a := NewThePornDBAdapterWith(testTPDBToken, "")
	page := func(u string, cur, last int, data string) Document {
		return Document{URL: u, Body: []byte(fmt.Sprintf(`{"data":%s,"meta":{"total":5,"per_page":2,"current_page":%d,"last":%d}}`, data, cur, last))}
	}
	if got := a.NextPage(page(ThePornDBJAVListURL, 1, 3, `[{}]`)); got != "https://api.theporndb.net/jav?orderBy=recently_released&page=2&per_page=100" {
		t.Fatalf("next = %q", got)
	}
	for name, d := range map[string]Document{
		"last page":     page(ThePornDBJAVListURL+"&page=3", 3, 3, `[{}]`),
		"empty page":    page(ThePornDBJAVListURL, 1, 3, `[]`),
		"page mismatch": page(ThePornDBJAVListURL+"&page=2", 1, 3, `[{}]`),
		"single":        {URL: "https://api.theporndb.net/jav/x", Body: []byte(`{"data":{"id":"x"}}`)},
		"no meta":       {URL: ThePornDBJAVListURL, Body: []byte(`{"data":[{}]}`)},
	} {
		if got := a.NextPage(d); got != "" {
			t.Errorf("%s: next = %q", name, got)
		}
	}
	wm := time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)
	got := a.IncrementalStart(ThePornDBJAVListURL+"&page=4", wm)
	u, _ := url.Parse(got)
	if q := u.Query(); q.Get("date") != "2026-10-07" || q.Get("date_operation") != ">=" || q.Get("page") != "" || q.Get("orderBy") != "recently_released" {
		t.Fatalf("incremental start = %q", got)
	}
	for _, start := range []string{"https://api.theporndb.net/jav?orderBy=recently_created", "https://api.theporndb.net/performers?per_page=25"} {
		if got := a.IncrementalStart(start, wm); got != "" {
			t.Errorf("%s narrowed to %q", start, got)
		}
	}
	if got := a.IncrementalStart(ThePornDBJAVListURL, time.Time{}); got != "" {
		t.Errorf("zero watermark narrowed: %q", got)
	}
}

// Discovery inside the phase model: a baseline pass pages through GET /jav;
// incremental runs narrow by the incremental watermark (date >= watermark -
// 72h); every request carries the bearer token and asks for JSON.
func TestThePornDBCrawlPhases(t *testing.T) {
	api := newTPDBAPI(t)
	a := NewThePornDBAdapterWith(testTPDBToken, api.host())
	cps := newFakeCheckpoints()
	sink := &recordingSink{}
	first := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	base := tpdbCrawl(t, api, a, cps, sink, api.listURL(), first)
	base.Phase = PhaseBaseline
	res, err := base.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 2 || res.Records != 3 || res.Stop != StopComplete {
		t.Fatalf("baseline = %+v", res)
	}
	if got := strings.Join(sink.keys(), ","); !strings.Contains(got, "tpdb-jav-5b0d3a6e") || !strings.Contains(got, "tpdb-jav-9a8b7c6d") {
		t.Fatalf("saved %s", got)
	}
	for _, r := range api.requests() {
		if r.Header.Get("Authorization") != "Bearer "+testTPDBToken || r.Header.Get("Accept") != "application/json" {
			t.Fatalf("request headers %v", r.Header)
		}
		if strings.Contains(r.URL.String(), testTPDBToken) {
			t.Fatalf("token in URL %s", r.URL)
		}
	}
	inc := tpdbCrawl(t, api, a, cps, &recordingSink{}, api.listURL(), first.Add(time.Hour))
	inc.Phase = PhaseIncremental
	if res, err = inc.Run(context.Background()); err != nil || res.Records != 3 {
		t.Fatalf("first incremental = %+v %v", res, err)
	}
	sink2 := &recordingSink{}
	inc = tpdbCrawl(t, api, a, cps, sink2, api.listURL(), first.Add(25*time.Hour))
	inc.Phase = PhaseIncremental
	if res, err = inc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := api.requests()
	last := reqs[len(reqs)-1].URL.Query()
	// Watermark 2026-10-01 04:00 UTC minus 72h.
	if last.Get("date") != "2026-09-28" || last.Get("date_operation") != ">=" || res.Records != 1 {
		t.Fatalf("incremental = %+v, query %v", res, last)
	}
}

func TestThePornDBNotConfigured(t *testing.T) {
	t.Setenv("THEPORNDB_API_TOKEN", "")
	a := NewThePornDBAdapter()
	if err := a.Configured(); !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "THEPORNDB_API_TOKEN") {
		t.Fatalf("Configured() = %v", err)
	}
	if _, err := HTTPClientFor(a, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("HTTPClientFor = %v", err)
	}
	// Unconfigured, fixtures still parse (fixture acceptance needs no token).
	if recs := tpdbFixtureRecords(t, "theporndb/jav_scene.json", "https://api.theporndb.net/jav/x"); len(recs) != 1 {
		t.Fatal("unconfigured adapter must still parse")
	}
	t.Setenv("THEPORNDB_API_TOKEN", " tok ")
	if err := NewThePornDBAdapter().Configured(); err != nil {
		t.Fatal(err)
	}
}

func TestThePornDBTransportOnlySignsAPIHost(t *testing.T) {
	var gotAuth []string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = append(gotAuth, r.Header.Get("Authorization"))
	}))
	defer other.Close()
	api := newTPDBAPI(t)
	api.set(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/elsewhere", http.StatusFound)
	})
	a := NewThePornDBAdapterWith(testTPDBToken, api.host())
	client, err := HTTPClientFor(a, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(api.URL + "/jav")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(gotAuth) != 1 || gotAuth[0] != "" {
		t.Fatalf("redirect target received Authorization %q", gotAuth)
	}
	if reqs := api.requests(); len(reqs) != 1 || reqs[0].Header.Get("Authorization") == "" {
		t.Fatal("API host not signed")
	}
	if resp.Request.Header.Get("Authorization") != "" {
		t.Fatal("caller sees the signed request")
	}
}

// TestThePornDBTokenNeverLoggedOrStored runs crawls plus failure paths and
// fails if the token appears in anything logged or persisted: records,
// cursors, checkpoint codes, job error text, block reasons, log output.
func TestThePornDBTokenNeverLoggedOrStored(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	api := newTPDBAPI(t)
	a := NewThePornDBAdapterWith(testTPDBToken, api.host())
	cps := newFakeCheckpoints()
	sink := &recordingSink{}
	var stored []string
	run := func(start string) {
		res, err := tpdbCrawl(t, api, a, cps, sink, start, time.Now()).Run(context.Background())
		err = SanitizeError(err, a)
		log.Printf("source=9 pages=%d records=%d cursor=%q err=%v", res.Pages, res.Records, res.Cursor, err)
		stored = append(stored, fmt.Sprint(res))
		if err != nil {
			stored = append(stored, err.Error())
			if b, ok := IsBlocked(err); ok {
				stored = append(stored, b.Error(), b.URL, b.Reason)
			}
		}
	}
	run(api.listURL())
	// A response that echoes the Authorization header into data fields.
	api.set(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fmt.Fprintf(w, `{"data":{"id":"5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01","title":%q,"url":%q,"sku":"SONE-156"}}`, "echo "+auth, "https://x.example/?t="+url.QueryEscape(strings.TrimPrefix(auth, "Bearer ")))
	})
	run(api.URL + "/jav/5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01")
	// Failure paths: 429 block, 5xx, 403 with the token echoed in the body.
	api.set(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down "+r.Header.Get("Authorization"), http.StatusTooManyRequests)
	})
	run(api.listURL())
	api.set(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom "+r.Header.Get("Authorization"), http.StatusInternalServerError)
	})
	run(api.listURL())
	api.set(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"message":"Unauthorized %s"}`, r.Header.Get("Authorization"))
	})
	run(api.listURL())
	// A transport error whose text carries the token.
	leaky := &url.Error{Op: "Get", URL: "https://api.theporndb.net/jav?token=" + url.QueryEscape(testTPDBToken), Err: errors.New("connection reset by " + testTPDBToken)}
	stored = append(stored, SanitizeError(leaky, a).Error())

	if len(sink.recs) != 4 {
		t.Fatalf("saved %d records, want 4", len(sink.recs))
	}
	for _, rec := range sink.recs {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, string(b))
	}
	for k, cp := range cps.rows {
		stored = append(stored, fmt.Sprint(k, cp.Cursor, cp.LastErrorCode))
	}
	stored = append(stored, cps.advances...)
	stored = append(stored, cps.fails...)
	stored = append(stored, logs.String())
	all := strings.Join(stored, "\n")
	for _, secret := range []string{testTPDBToken, url.QueryEscape(testTPDBToken)} {
		if strings.Contains(all, secret) {
			t.Fatalf("token %q leaked into stored/logged output:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, "rate_limited") || !strings.Contains(all, "HTTP 500") || !strings.Contains(all, "forbidden") || !strings.Contains(all, "[redacted]") {
		t.Fatalf("failure paths not exercised:\n%s", all)
	}
}

func TestExactCodeMatches(t *testing.T) {
	recs := tpdbFixtureRecords(t, "theporndb/jav_page1.json", ThePornDBJAVListURL)
	recs = append(recs, tpdbFixtureRecords(t, "theporndb/jav_scene.json", "https://api.theporndb.net/jav/x")...)
	m := ExactCodeMatches(recs, []string{"sone-156"})
	if len(m) != 1 || m[0].Fields["dvd_code"] != "SONE-156" {
		t.Fatalf("matches %v", m)
	}
	if m := ExactCodeMatches(recs, []string{"SONE-0156"}); len(m) != 1 {
		t.Fatalf("zero-padded code not matched: %v", m)
	}
	if m := ExactCodeMatches(recs, []string{"SONE-157", ""}); len(m) != 0 {
		t.Fatalf("wrong code matched: %v", m)
	}
	lookup := NewThePornDBAdapterWith(testTPDBToken, "").LookupURLs("sone-156")
	if len(lookup) != 1 || lookup[0] != "https://api.theporndb.net/jav?per_page=10&q=SONE-156" {
		t.Fatalf("lookup urls %v", lookup)
	}
}
