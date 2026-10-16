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

// Test credentials: distinctive so any leak is easy to spot.
var testDMMCreds = DMMCredentials{APIID: "TESTAPIIDzq81xk", AffiliateID: "testaff-990"}

// dmmAPI serves the doc-derived fixtures like the real API: credentials are
// required and echoed back (request.parameters, af_id, affi_id).
type dmmAPI struct {
	*httptest.Server
	mu       sync.Mutex
	queries  []url.Values
	override http.HandlerFunc
	// robots serves /robots.txt (nil = 404); those requests are kept in
	// robotsQueries, not queries.
	robots        http.HandlerFunc
	robotsQueries []url.Values
}

func newDMMAPI(t *testing.T) *dmmAPI {
	t.Helper()
	page1 := fixture(t, "dmm/itemlist_page1.json")
	page2 := fixture(t, "dmm/itemlist_page2.json")
	actress := fixture(t, "dmm/actresssearch.json")
	a := &dmmAPI{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		a.mu.Lock()
		if r.URL.Path == "/robots.txt" {
			a.robotsQueries = append(a.robotsQueries, q)
			robots := a.robots
			a.mu.Unlock()
			if robots == nil {
				http.NotFound(w, r)
			} else {
				robots(w, r)
			}
			return
		}
		a.queries = append(a.queries, q)
		override := a.override
		a.mu.Unlock()
		if override != nil {
			override(w, r)
			return
		}
		if q.Get("api_id") == "" || q.Get("affiliate_id") == "" {
			http.Error(w, "missing credentials", http.StatusBadRequest)
			return
		}
		var body string
		switch r.URL.Path {
		case "/affiliate/v3/ItemList":
			switch {
			case q.Get("gte_date") != "":
				// Incremental pass: only the newest item is in range.
				body = strings.NewReplacer(`"total_count": 3`, `"total_count": 1`, `"first_position": 3`, `"first_position": 1`).Replace(page2)
			case q.Get("offset") == "" || q.Get("offset") == "1":
				body = page1
			case q.Get("offset") == "3":
				body = page2
			default:
				http.NotFound(w, r)
				return
			}
		case "/affiliate/v3/ActressSearch":
			body = actress
		default:
			http.NotFound(w, r)
			return
		}
		body = strings.NewReplacer("{{API_ID}}", q.Get("api_id"), "{{AFFILIATE_ID}}", q.Get("affiliate_id")).Replace(body)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *dmmAPI) set(h http.HandlerFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.override = h
}

func (a *dmmAPI) host() string { u, _ := url.Parse(a.URL); return u.Host }
func (a *dmmAPI) requests() []url.Values {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]url.Values(nil), a.queries...)
}

func (a *dmmAPI) itemListURL() string {
	return a.URL + "/affiliate/v3/ItemList?floor=videoa&hits=2&output=json&service=digital&site=FANZA&sort=date"
}

// recordingSink keeps full records for inspection.
type recordingSink struct {
	mu   sync.Mutex
	recs []Record
}

func (s *recordingSink) SaveCatalogRecord(_ context.Context, _ int64, _ string, rec Record) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, rec)
	return int64(len(s.recs)), nil
}

func (s *recordingSink) keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, r := range s.recs {
		out = append(out, r.SourceKey)
	}
	return out
}

func dmmCrawl(t *testing.T, api *dmmAPI, a *DMMAdapter, cps CheckpointStore, sink RecordSink, start string, now time.Time) *Crawl {
	t.Helper()
	client, err := HTTPClientFor(a, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	return &Crawl{SourceID: 7, StartURL: start, Adapter: a, Fetcher: NewFetcher(client, FetchPolicy{}), Checkpoints: cps, Sink: RedactingSink{Sink: sink, Redactor: a}, Now: func() time.Time { return now }}
}

func discoverFixture(t *testing.T, a *DMMAdapter, name, rawURL string) []Record {
	t.Helper()
	body := strings.NewReplacer("{{API_ID}}", testDMMCreds.APIID, "{{AFFILIATE_ID}}", testDMMCreds.AffiliateID).Replace(fixture(t, name))
	recs, err := a.Discover(context.Background(), Document{URL: rawURL, FinalURL: rawURL, Status: 200, Body: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// DMM inside the phase model: a baseline pass pages through the archive on
// its own checkpoint; incremental runs start at the head, narrowed by
// gte_date from the incremental checkpoint's watermark (never the
// baseline's), and verify neither narrows nor touches checkpoints.
func TestDMMCrawlPhasesUseIncrementalWatermark(t *testing.T) {
	api := newDMMAPI(t)
	a := NewDMMAdapterWith(testDMMCreds, api.host())
	cps := newFakeCheckpoints()
	sink := &recordingSink{}
	first := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	baseKey := CheckpointKey{SourceID: 7, TaskType: TaskDiscovery, Partition: string(PhaseBaseline), ParserVersion: "dmm"}
	incKey := CheckpointKey{SourceID: 7, TaskType: TaskDiscovery, Partition: string(PhaseIncremental), ParserVersion: "dmm"}

	base := dmmCrawl(t, api, a, cps, sink, api.itemListURL(), first)
	base.Phase = PhaseBaseline
	res, err := base.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 2 || res.Records != 3 || res.Stop != StopComplete {
		t.Fatalf("baseline pass = %+v", res)
	}
	if got := strings.Join(sink.keys(), ","); got != "15dss00145,sone00156,118abp00123" {
		t.Fatalf("saved %s", got)
	}
	reqs := api.requests()
	if len(reqs) != 2 || reqs[1].Get("offset") != "3" {
		t.Fatalf("requests = %v", reqs)
	}
	for _, q := range reqs {
		if q.Get("api_id") != testDMMCreds.APIID || q.Get("affiliate_id") != testDMMCreds.AffiliateID {
			t.Fatalf("credentials not sent: %v", q)
		}
	}
	if cp := cps.rows[baseKey]; cp.Cursor != "" || cp.Watermark == nil || !cp.Watermark.Equal(first) {
		t.Fatalf("baseline checkpoint after full pass = %+v", cp)
	}
	if _, ok := cps.rows[incKey]; ok {
		t.Fatal("baseline wrote the incremental checkpoint")
	}

	// First incremental run: no incremental watermark yet, so it reads the
	// head without gte_date (a completed baseline does not narrow it).
	second := first.Add(time.Hour)
	inc := dmmCrawl(t, api, a, cps, &recordingSink{}, api.itemListURL(), second)
	inc.Phase = PhaseIncremental
	if res, err = inc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res.Records != 3 || res.Stop != StopComplete {
		t.Fatalf("first incremental = %+v", res)
	}
	reqs = api.requests()
	if got := reqs[2].Get("gte_date"); got != "" {
		t.Fatalf("first incremental narrowed by the baseline watermark: gte_date = %q", got)
	}
	incCp := cps.rows[incKey]
	if incCp.Watermark == nil || !incCp.Watermark.Equal(second) || !strings.Contains(incCp.Cursor, "release:15dss00145") {
		t.Fatalf("incremental checkpoint = %+v", incCp)
	}

	// Next incremental run: gte_date from the incremental watermark (JST,
	// minus the overlap); one page, only the new item.
	sink2 := &recordingSink{}
	inc = dmmCrawl(t, api, a, cps, sink2, api.itemListURL(), second.Add(24*time.Hour))
	inc.Phase = PhaseIncremental
	if res, err = inc.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 || res.Records != 1 || res.Stop != StopComplete {
		t.Fatalf("incremental pass = %+v", res)
	}
	reqs = api.requests()
	// 2026-10-01 04:00 UTC = 13:00 JST; minus 72h.
	if got := reqs[len(reqs)-1].Get("gte_date"); got != "2026-09-28T13:00:00" {
		t.Fatalf("gte_date = %q", got)
	}
	if cp := cps.rows[baseKey]; cp.Cursor != "" || !cp.Watermark.Equal(first) {
		t.Fatalf("incremental touched the baseline checkpoint: %+v", cp)
	}

	// Verify: never narrowed, never writes a checkpoint.
	before := len(cps.advances) + len(cps.fails)
	n := len(api.requests())
	ver := dmmCrawl(t, api, a, nil, &recordingSink{}, api.itemListURL(), second.Add(48*time.Hour))
	ver.Phase = PhaseVerify
	if res, err = ver.Run(context.Background()); err != nil || res.Records != 3 {
		t.Fatalf("verify = %+v %v", res, err)
	}
	for _, q := range api.requests()[n:] {
		if q.Get("gte_date") != "" {
			t.Fatalf("verify narrowed: %v", q)
		}
	}
	if len(cps.advances)+len(cps.fails) != before {
		t.Fatal("verify wrote a checkpoint")
	}
}

func TestDMMCrawlResumesFromCursorWithoutCredentials(t *testing.T) {
	api := newDMMAPI(t)
	a := NewDMMAdapterWith(testDMMCreds, api.host())
	cps := newFakeCheckpoints()
	sink := &recordingSink{}
	c := dmmCrawl(t, api, a, cps, sink, api.itemListURL(), time.Now())
	c.Phase = PhaseBaseline
	c.MaxPages = 1
	if _, err := c.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	cp := cps.rows[CheckpointKey{SourceID: 7, TaskType: TaskDiscovery, Partition: string(PhaseBaseline), ParserVersion: "dmm"}]
	if !strings.Contains(cp.Cursor, "offset=3") || strings.Contains(cp.Cursor, "api_id") || strings.Contains(cp.Cursor, "affiliate_id") {
		t.Fatalf("cursor = %q", cp.Cursor)
	}
	c.MaxPages = 0
	res, err := c.Run(context.Background())
	if err != nil || res.Records != 1 {
		t.Fatalf("resume = %+v %v", res, err)
	}
}

func TestDMMReleaseMapping(t *testing.T) {
	a := NewDMMAdapterWith(testDMMCreds, DMMAPIHost)
	recs := discoverFixture(t, a, "dmm/itemlist_page1.json", DMMItemListURL)
	recs = append(recs, discoverFixture(t, a, "dmm/itemlist_page2.json", DMMItemListURL)...)
	if len(recs) != 3 {
		t.Fatalf("records = %d", len(recs))
	}
	r := recs[0]
	if r.EntityType != Release || r.SourceKey != "15dss00145" || r.URL != "http://video.dmm.co.jp/av/content/?id=15dss00145" {
		t.Fatalf("record = %+v", r)
	}
	want := map[string]any{
		"title":                "GET！！ 素人ナンパ Best100！！ 街角女子ベスト100人 8時間",
		"content_id":           "15dss00145",
		"product_id":           "15dss00145dl",
		"dvd_code":             "DSS-145",
		"dvd_code_origin":      "content_id",
		"release_date":         "2012-08-03",
		"release_datetime":     "2012/8/3 10:00",
		"runtime_minutes":      "350",
		"maker":                "桃太郎映像出版",
		"maker_id":             "40016",
		"label":                "LADY HUNTERS",
		"label_id":             "76",
		"series":               "GETシリーズ",
		"series_id":            "1006",
		"cover_url":            "http://pics.dmm.co.jp/digital/video/15dss00145/15dss00145pl.jpg",
		"official_preview_url": "http://www.dmm.co.jp/litevideo/-/part/=/cid=15dss145/size=720_480/affi_id={affiliate_id}/",
		"purchase_url":         "https://al.fanza.co.jp/?lurl=https%3A%2F%2Fvideo.dmm.co.jp%2Fav%2Fcontent%2F%3Fid%3D15dss00145&af_id={affiliate_id}&ch=api",
		"product_url":          "http://video.dmm.co.jp/av/content/?id=15dss00145",
		"price":                "300～",
	}
	for k, v := range want {
		if r.Fields[k] != v {
			t.Errorf("%s = %#v, want %#v", k, r.Fields[k], v)
		}
	}
	for k, v := range map[string][]string{
		"genres":            {"ベスト・総集編"},
		"tags":              {"ベスト・総集編"},
		"genre_ids":         {"6003"},
		"performers":        {"小澤マリア"},
		"sample_image_urls": {"http://pics.dmm.co.jp/digital/video/15dss00145/15dss00145jp-1.jpg"},
		"delivery_types":    {"stream"},
	} {
		got, ok := r.Fields[k].([]string) // SaveCatalogRecord type-asserts []string
		if !ok || strings.Join(got, "|") != strings.Join(v, "|") {
			t.Errorf("%s = %#v, want %v", k, r.Fields[k], v)
		}
	}
	refs := r.Fields["performer_refs"].([]map[string]string)
	if len(refs) != 1 || refs[0]["dmm_actress_id"] != "15187" || refs[0]["source_key"] != "dmm-actress-15187" {
		t.Errorf("performer_refs = %v", refs)
	}
	if _, ok := r.Fields["director"]; ok {
		t.Error("absent director must stay unmapped")
	}

	b := recs[1].Fields
	if b["dvd_code"] != "SONE-156" || b["dvd_code_origin"] != "maker_product" || b["content_id"] != "sone00156" {
		t.Errorf("maker_product code: %v %v", b["dvd_code"], b["dvd_code_origin"])
	}
	if b["director"] != "前田文豪" || b["director_id"] != "106690" || b["release_date"] != "2012-08-01" {
		t.Errorf("item B = %v", b)
	}
	if refs := b["performer_refs"].([]map[string]string); refs[0]["ruby"] != "うんぱい" {
		t.Errorf("ruby = %v", refs)
	}
	if c := recs[2].Fields; c["dvd_code"] != "ABP-123" || c["content_id"] != "118abp00123" {
		t.Errorf("prefixed content id: %v %v", c["dvd_code"], c["content_id"])
	}
}

func TestDMMDVDCode(t *testing.T) {
	for _, tt := range []struct{ cid, mp, code, origin string }{
		{"15dss00145", "", "DSS-145", "content_id"},
		{"sone00708", "", "SONE-708", "content_id"},
		{"118abp00123", "", "ABP-123", "content_id"},
		{"sone156", "SONE-156", "SONE-156", "maker_product"},
		{"h_1234abc00123", "", "", ""},                   // not letters+digits: leave unmapped
		{"x00001", "10003-54653", "X-001", "content_id"}, // numeric maker number is not a DVD code
	} {
		code, origin := DMMDVDCode(tt.cid, tt.mp)
		if code != tt.code || origin != tt.origin {
			t.Errorf("DMMDVDCode(%q,%q) = %q,%q want %q,%q", tt.cid, tt.mp, code, origin, tt.code, tt.origin)
		}
	}
}

func TestDMMActressMapping(t *testing.T) {
	a := NewDMMAdapterWith(testDMMCreds, DMMAPIHost)
	recs := discoverFixture(t, a, "dmm/actresssearch.json", DMMActressSearchURL)
	if len(recs) != 2 {
		t.Fatalf("records = %d", len(recs))
	}
	r := recs[0]
	if r.EntityType != Performer || r.SourceKey != "dmm-actress-15365" || !strings.HasPrefix(r.URL, "https://api.dmm.com/affiliate/v3/ActressSearch?actress_id=15365") {
		t.Fatalf("record = %+v", r)
	}
	for k, v := range map[string]string{
		"name": "麻美ゆま", "name_ja": "麻美ゆま", "name_kana": "あさみゆま", "dmm_actress_id": "15365",
		"birth_date": "1987-03-24", "height_cm": "158", "bust_cm": "96", "waist_cm": "58", "hip_cm": "88",
		"cup_size": "H", "blood_type": "AB", "birth_place": "東京都", "hobby": "香水集め、英会話、ピアノ",
	} {
		if r.Fields[k] != v {
			t.Errorf("%s = %#v, want %q", k, r.Fields[k], v)
		}
	}
	if imgs := r.Fields["image_urls"].(map[string]string); imgs["large"] != "http://pics.dmm.co.jp/mono/actjpgs/asami_yuma.jpg" {
		t.Errorf("image_urls = %v", imgs)
	}
	empty := recs[1].Fields
	for _, k := range []string{"birth_date", "height_cm", "cup_size", "bust_cm", "name_kana", "birth_place"} {
		if _, ok := empty[k]; ok {
			t.Errorf("empty/null %s must stay unmapped, got %#v", k, empty[k])
		}
	}
}

func TestDMMNotConfigured(t *testing.T) {
	t.Setenv("DMM_API_ID", "")
	t.Setenv("DMM_AFFILIATE_ID", "")
	a := NewDMMAdapter()
	err := a.Configured()
	if !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "DMM_API_ID and DMM_AFFILIATE_ID") {
		t.Fatalf("Configured() = %v", err)
	}
	if _, err := HTTPClientFor(a, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("HTTPClientFor = %v", err)
	}
	t.Setenv("DMM_API_ID", "x")
	if err := NewDMMAdapter().Configured(); err == nil || strings.Contains(err.Error(), "DMM_API_ID") {
		t.Fatalf("only affiliate id missing: %v", err)
	}
	t.Setenv("DMM_AFFILIATE_ID", "y-990")
	if err := NewDMMAdapter().Configured(); err != nil {
		t.Fatal(err)
	}
	// Adapters without configuration needs are unaffected.
	if _, err := HTTPClientFor(NewJAVDatabaseAdapter(), nil); err != nil {
		t.Fatal(err)
	}
}

// TestDMMCredentialsNeverLoggedOrStored runs a crawl plus failure paths and
// fails if either credential appears in anything that is logged or persisted:
// records (source_records.raw_data / field values), URLs, cursors, checkpoint
// error codes, job error text and log output.
func TestDMMCredentialsNeverLoggedOrStored(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	api := newDMMAPI(t)
	a := NewDMMAdapterWith(testDMMCreds, api.host())
	cps := newFakeCheckpoints()
	sink := &recordingSink{}
	var stored []string
	check := func(where string, err error) {
		res, rerr := dmmCrawl(t, api, a, cps, sink, where, time.Now()).Run(context.Background())
		if rerr == nil {
			rerr = err
		}
		rerr = SanitizeError(rerr, a)
		log.Printf("source=7 pages=%d records=%d cursor=%q err=%v", res.Pages, res.Records, res.Cursor, rerr)
		if rerr != nil {
			stored = append(stored, rerr.Error())
			if b, ok := IsBlocked(rerr); ok {
				stored = append(stored, b.Error(), b.URL)
			}
		}
	}
	check(api.itemListURL(), nil)
	check(api.URL+"/affiliate/v3/ActressSearch?hits=100&output=json&sort=id", nil)

	// Failure paths: 429 block, 5xx, API status error.
	api.set(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		http.Error(w, "slow down "+r.URL.RawQuery, http.StatusTooManyRequests)
	})
	check(api.itemListURL()+"&offset=1", nil)
	api.set(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom "+r.URL.RawQuery, http.StatusInternalServerError)
	})
	cps2 := newFakeCheckpoints()
	res, err := dmmCrawl(t, api, a, cps2, sink, api.itemListURL(), time.Now()).Run(context.Background())
	stored = append(stored, fmt.Sprint(res), SanitizeError(err, a).Error())
	api.set(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"request":{"parameters":{"api_id":%q,"affiliate_id":%q}},"result":{"status":400}}`, r.URL.Query().Get("api_id"), r.URL.Query().Get("affiliate_id"))
	})
	res, err = dmmCrawl(t, api, a, newFakeCheckpoints(), sink, api.itemListURL(), time.Now()).Run(context.Background())
	var apiErr *DMMAPIError
	if !errors.As(err, &apiErr) || apiErr.Status != "400" {
		t.Fatalf("api status error = %v", err)
	}
	stored = append(stored, fmt.Sprint(res), SanitizeError(err, a).Error())

	// A transport error whose text carries a signed URL (e.g. after a redirect).
	leaky := &url.Error{Op: "Get", URL: "https://api.dmm.com/affiliate/v3/ItemList?api_id=" + testDMMCreds.APIID + "&affiliate_id=" + testDMMCreds.AffiliateID + "&site=FANZA", Err: errors.New("connection reset")}
	stored = append(stored, SanitizeError(leaky, a).Error())

	if len(sink.recs) != 5 {
		t.Fatalf("saved %d records, want 5 (3 releases + 2 performers)", len(sink.recs))
	}
	for _, rec := range sink.recs {
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, string(b))
	}
	for _, f := range []*fakeCheckpoints{cps, cps2} {
		for k, cp := range f.rows {
			stored = append(stored, fmt.Sprint(k, cp.Cursor, cp.LastErrorCode))
		}
		stored = append(stored, f.advances...)
		stored = append(stored, f.fails...)
	}
	stored = append(stored, logs.String())

	all := strings.Join(stored, "\n")
	for _, secret := range []string{testDMMCreds.APIID, testDMMCreds.AffiliateID} {
		if strings.Contains(all, secret) {
			t.Fatalf("credential %q leaked into stored/logged output:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, "rate_limited") || !strings.Contains(all, "HTTP 500") || !strings.Contains(all, "connection reset") {
		t.Fatalf("failure paths not exercised:\n%s", all)
	}
	if !strings.Contains(all, DMMAffiliatePlaceholder) {
		t.Fatal("affiliate links should keep a placeholder for render-time substitution")
	}
}

func TestDMMTransportOnlySignsAPIHost(t *testing.T) {
	var got url.Values
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Query() }))
	defer other.Close()
	a := NewDMMAdapterWith(testDMMCreds, "api.example.invalid")
	client, err := HTTPClientFor(a, other.Client())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Get(other.URL + "/x?a=1")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.Get("api_id") != "" || got.Get("affiliate_id") != "" || got.Get("a") != "1" {
		t.Fatalf("other host received %v", got)
	}
}

func TestDMMBlockAndServerErrors(t *testing.T) {
	api := newDMMAPI(t)
	a := NewDMMAdapterWith(testDMMCreds, api.host())
	api.set(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	cps := newFakeCheckpoints()
	_, err := dmmCrawl(t, api, a, cps, &recordingSink{}, api.itemListURL(), time.Now()).Run(context.Background())
	b, ok := IsBlocked(err)
	if !ok || b.RetryAfter != 10*time.Minute || b.Reason != "rate_limited" {
		t.Fatalf("429 = %v", err)
	}
	if got := strings.Join(cps.fails, ","); got != "7:catalog_discovery:source_blocked" {
		t.Fatalf("fails = %s", got)
	}

	api.set(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	cps = newFakeCheckpoints()
	_, err = dmmCrawl(t, api, a, cps, &recordingSink{}, api.itemListURL(), time.Now()).Run(context.Background())
	if _, blocked := IsBlocked(err); blocked || err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("502 = %v", err)
	}
	if got := strings.Join(cps.fails, ","); got != "7:catalog_discovery:index_fetch_failed" {
		t.Fatalf("fails = %s", got)
	}

	// Malformed body: discover fails, cursor does not move.
	api.set(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>not json</html>") })
	cps = newFakeCheckpoints()
	_, err = dmmCrawl(t, api, a, cps, &recordingSink{}, api.itemListURL(), time.Now()).Run(context.Background())
	if err == nil || !strings.Contains(strings.Join(cps.fails, ","), "discover_failed") {
		t.Fatalf("malformed = %v %v", err, cps.fails)
	}
}

func TestDMMNextPageWindowsPastMaxOffset(t *testing.T) {
	a := NewDMMAdapterWith(testDMMCreds, DMMAPIHost)
	page := func(first int, dates ...string) Document {
		items := []string{}
		for i, d := range dates {
			items = append(items, fmt.Sprintf(`{"content_id":"abc%05d","title":"t","date":%q}`, i, d))
		}
		body := fmt.Sprintf(`{"result":{"status":200,"result_count":%d,"total_count":900000,"first_position":%d,"items":[%s]}}`, len(dates), first, strings.Join(items, ","))
		return Document{URL: DMMItemListURL + "&offset=" + fmt.Sprint(first), Body: []byte(body)}
	}
	if next := a.NextPage(page(49901, "2020-01-02 10:00:00")); !strings.Contains(next, "offset=49902") {
		t.Fatalf("below the limit = %q", next)
	}
	// 49901 + 100 > 50000: shift the window to the oldest date on the page.
	dates := make([]string, 100)
	for i := range dates {
		dates[i] = "2020-01-05 10:00:00"
	}
	dates[99] = "2020-01-03 09:30:00"
	next := a.NextPage(page(49901, dates...))
	u, _ := url.Parse(next)
	if u.Query().Get("lte_date") != "2020-01-03T09:30:00" || u.Query().Get("offset") != "1" {
		t.Fatalf("window shift = %q", next)
	}
	// No progress (same oldest date as the current window): stop.
	stuck := page(49901, dates...)
	stuck.URL = next + "&offset=49901"
	if got := a.NextPage(stuck); got != "" {
		t.Fatalf("stuck window = %q", got)
	}
	// Last page and empty page end the pass.
	last := Document{URL: DMMItemListURL, Body: []byte(`{"result":{"status":200,"result_count":2,"total_count":2,"first_position":1,"items":[]}}`)}
	if a.NextPage(last) != "" {
		t.Fatal("last page must end the pass")
	}
	if a.IncrementalStart(DMMActressSearchURL, time.Now()) != "" {
		t.Fatal("ActressSearch has no date filter")
	}
}

func TestDMMRedact(t *testing.T) {
	a := NewDMMAdapterWith(DMMCredentials{APIID: "KEY+/1", AffiliateID: "aff-990"}, DMMAPIHost)
	in := "GET https://api.dmm.com/affiliate/v3/ItemList?api_id=KEY%2B%2F1&affiliate_id=aff-990&site=FANZA then af_id=aff-990 and KEY+/1"
	out := a.Redact(in)
	if strings.Contains(out, "KEY") || strings.Contains(out, "aff-990") || !strings.Contains(out, "?site=FANZA") {
		t.Fatalf("Redact = %q", out)
	}
	if NewDMMAdapterWith(DMMCredentials{}, "").Redact("abc") != "abc" {
		t.Fatal("empty credentials must not rewrite text")
	}
}

// The DMM API host gets the same robots.txt treatment as any site: a 404
// means no restrictions (and the robots.txt request never carries
// credentials); a Disallow stops the automatic run before any API call.
func TestDMMCrawlHonorsRobots(t *testing.T) {
	for _, tc := range []struct {
		name    string
		robots  http.HandlerFunc
		allowed bool
	}{
		{"robots 404", nil, true},
		{"robots disallow", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "User-agent: *\nDisallow: /affiliate/\n") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newDMMAPI(t)
			api.robots = tc.robots
			a := NewDMMAdapterWith(testDMMCreds, api.host())
			sink := &recordingSink{}
			c := dmmCrawl(t, api, a, newFakeCheckpoints(), sink, api.itemListURL(), time.Now())
			c.Phase = PhaseIncremental
			c.Fetcher.Robots, c.Fetcher.Automatic = NewRobots(api.Client(), "test"), true
			res, err := c.Run(context.Background())
			if len(api.robotsQueries) != 1 || api.robotsQueries[0].Get("api_id") != "" {
				t.Fatalf("robots.txt requests = %v", api.robotsQueries)
			}
			if tc.allowed {
				if err != nil || res.Records != 3 {
					t.Fatalf("res = %+v err = %v", res, err)
				}
				return
			}
			if _, ok := IsRobots(err); !ok || len(sink.keys()) != 0 || len(api.requests()) != 0 {
				t.Fatalf("err = %v, saved %v, api requests %v", err, sink.keys(), api.requests())
			}
		})
	}
}
