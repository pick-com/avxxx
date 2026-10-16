package acceptance

import (
	"avxxx/internal/api"
	"avxxx/internal/catalog"
	"avxxx/internal/store"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// skipPublicRoutes need live third-party media adapters (none in tests).
var skipPublicRoutes = map[string]bool{
	"GET /api/search":                  true,
	"GET /api/detail/{source}/{id...}": true,
}

var (
	restWildcard = regexp.MustCompile(`\{[^}]+\.\.\.\}`)
	wildcard     = regexp.MustCompile(`\{[^}]+\}`)
)

// TestPublicAPINeverLeaksIntegration crawls every fixture site (raw_data
// carries a canary, DMM responses echo the API credentials), applies admin
// overrides with a canary reason and actor, hides a birthday, holds,
// unpublishes and leaves releases in draft, merges a performer — then walks
// every public GET route (api.Patterns, so new routes are covered) with every
// release, performer and series slug and several queries, and requires that
// no response contains raw_data, a canary, a DMM credential, an override
// reason or actor, the hidden birthday, or any title / slug of a release
// that is not published (FINAL_DATA_DESIGN 9: 公开接口只返回已发布且未软删除的
// 数据，不返回 raw_data … 和内部错误).
func TestPublicAPINeverLeaksIntegration(t *testing.T) {
	e := newEnv(t)
	c := e.newCatalog(t)
	e.crawlAll(c, catalog.PhaseBaseline)
	e.crawlAll(c, catalog.PhaseIncremental)
	ctx := e.ctx
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := func(m map[string]any) map[string]json.RawMessage {
		out := map[string]json.RawMessage{}
		for k, v := range m {
			b, err := json.Marshal(v)
			must(err)
			out[k] = b
		}
		return out
	}

	// Published: juq-743, abc-001, the XCITY title. Not public: abc-002
	// (held_review), abc-003 (unpublished), abp-123 (draft, as crawled).
	e.exec(`UPDATE releases SET status='published' WHERE slug IN ('juq-743','abc-001','summer-story-xc-source-5001')`)
	e.exec(`UPDATE releases SET status='held_review' WHERE slug='abc-002'`)
	e.exec(`UPDATE releases SET status='unpublished' WHERE slug='abc-003'`)
	notPublic := map[string]string{} // leak marker -> what it is
	for _, slug := range []string{"abc-002", "abc-003", "abp-123"} {
		notPublic[slug] = "slug of non-public release " + slug
		notPublic[e.str(`SELECT canonical_title FROM releases WHERE slug=$1`, slug)] = "title of non-public release " + slug
	}
	rp, err := store.ParseReleasePatch(raw(map[string]any{"label": "Admin Label", "reason": overrideNote}))
	must(err)
	_, err = e.s.PatchRelease(ctx, e.int64(`SELECT id FROM releases WHERE slug='juq-743'`), rp, overrideActor)
	must(err)
	// Jane Doe's birthday (1995-01-01, from two sources) is hidden.
	hp, err := store.ParsePerformerPatch(raw(map[string]any{"birth_date_hidden": true, "reason": overrideNote}))
	must(err)
	_, err = e.s.PatchPerformer(ctx, e.int64(`SELECT id FROM performers WHERE slug='jane-doe'`), hp, overrideActor)
	must(err)
	// A merged performer must resolve to its target, not leak the loser's data.
	cand := e.int64(`INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence,status)
		SELECT 'performer',a.id,b.id,0.9,'{}','pending' FROM performers a, performers b WHERE a.slug='jane-doe' AND b.slug='jeendou' RETURNING id`)
	_, err = e.s.MergeCandidate(ctx, cand, 0, overrideActor, overrideNote)
	must(err)

	// The canaries really are in the database (the walk is not vacuous).
	for what, q := range map[string]string{
		"raw_data canary":          `SELECT count(*) FROM source_records WHERE raw_data::text LIKE '%` + rawCanary + `%'`,
		"field canary":             `SELECT count(*) FROM source_field_values WHERE normalized_value::text LIKE '%` + rawCanary + `%'`,
		"override reason":          `SELECT count(*) FROM release_overrides WHERE reason='` + overrideNote + `'`,
		"hidden birthday (stored)": `SELECT count(*) FROM performers WHERE slug='jane-doe' AND birth_date='1995-01-01' AND birth_date_hidden`,
	} {
		if e.int64(q) == 0 {
			t.Fatalf("fixture missing: %s", what)
		}
	}
	// DMM credentials never reach the database either (redacted at ingest).
	for _, q := range []string{
		`SELECT count(*) FROM source_records WHERE raw_data::text LIKE '%%%s%%' OR source_url LIKE '%%%s%%'`,
		`SELECT count(*) FROM source_field_values WHERE normalized_value::text LIKE '%%%s%%' OR raw_value::text LIKE '%%%s%%'`,
		`SELECT count(*) FROM crawl_checkpoints WHERE cursor LIKE '%%%s%%' OR cursor LIKE '%%%s%%'`,
		`SELECT count(*) FROM jobs WHERE error LIKE '%%%s%%' OR result_counts::text LIKE '%%%s%%'`,
	} {
		for _, secret := range []string{dmmAPIID, dmmAffiliate} {
			if n := e.int64(fmt.Sprintf(q, secret, secret)); n != 0 {
				t.Errorf("DMM credential %q stored: %s -> %d rows", secret, q, n)
			}
		}
	}

	// Compliance personal data (PRD 15, migration 041): a copyright notice
	// with a counter-notice that took a release down, feedback with a
	// contact address, and consent records. None of it is ever public; the
	// taken-down release answers 451 without naming itself.
	const (
		tdName, tdEmail, tdPhone, tdAddress = "Claimant Canary-7Q", "claimant-canary@studio.example", "+81-3-0000-7777", "Canary Street 7"
		tdWork, tdSignature                 = "Work Canary-7Q description", "Signature Canary-7Q"
		counterName, counterEmail           = "Counter Canary-7Q", "counter-canary@distributor.example"
		fbMessage, fbEmail                  = "Feedback Canary-7Q message", "feedback-canary@example.com"
	)
	tdRelease := e.int64(`INSERT INTO releases(slug,canonical_title,status) VALUES('td-canary-001','Taken Down Canary Title','published') RETURNING id`)
	receipt, err := e.s.CreateTakedown(ctx, store.TakedownInput{ClaimantName: tdName, ClaimantEmail: tdEmail, ClaimantPhone: tdPhone, ClaimantAddress: tdAddress,
		RightsOwner: "Studio", Relationship: "agent", WorkDescription: tdWork, InfringingURLs: []string{"https://reel.example/releases/td-canary-001"},
		GoodFaith: true, Accuracy: true, Signature: tdSignature}, time.Now())
	must(err)
	tdID := e.int64(`SELECT id FROM takedown_requests WHERE ticket='` + receipt.Ticket + `'`)
	_, err = e.s.TakedownAction(ctx, tdID, store.TakedownActionInput{Action: store.TakedownUnpublishRelease, ReleaseID: tdRelease, Reason: overrideNote}, overrideActor)
	must(err)
	_, err = e.s.SubmitCounterNotice(ctx, receipt.Ticket, store.CounterNoticeInput{Name: counterName, Email: counterEmail, Address: tdAddress, Statement: tdWork, Consent: true, Signature: counterName})
	must(err)
	_, err = e.s.CreateFeedback(ctx, store.FeedbackInput{Category: "bug", Message: fbMessage, Email: fbEmail, PageURL: "/releases/juq-743"})
	must(err)
	_, err = e.s.RecordConsent(ctx, store.ConsentRecord{DeviceHash: "consent-device-hash-canary", Input: store.ConsentInput{Analytics: true, Source: "banner"}})
	must(err)

	leaks := map[string]string{
		"raw_data": "the raw_data column", rawCanary: "a source payload canary", "raw_html": "raw HTML",
		dmmAPIID: "the DMM API id", dmmAffiliate: "the DMM affiliate id", "api_id=": "a DMM credential parameter", "affiliate_id=": "a DMM credential parameter",
		overrideNote: "an override / review reason", overrideActor: "an override / review actor", "1995-01-01": "a hidden birthday",
		tdName: "a takedown claimant name", tdEmail: "a takedown claimant email", tdPhone: "a takedown claimant phone", tdAddress: "a takedown address",
		tdWork: "a takedown work description", tdSignature: "a takedown signature", receipt.Ticket: "a takedown ticket number",
		counterName: "a counter-notifier name", counterEmail: "a counter-notifier email",
		fbMessage: "a feedback message", fbEmail: "a feedback contact email", "consent-device-hash-canary": "a consent device hash",
		"td-canary-001": "slug of a taken-down release", "Taken Down Canary Title": "title of a taken-down release",
	}
	for k, v := range notPublic {
		leaks[k] = v
	}
	slugs := func(q string) []string {
		rows, err := e.s.Query(ctx, q)
		must(err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			must(rows.Scan(&s))
			out = append(out, s)
		}
		return out
	}
	releaseSlugs := slugs(`SELECT slug FROM releases ORDER BY slug`)
	performerSlugs := slugs(`SELECT slug FROM performers ORDER BY slug`)
	seriesSlugs := append(slugs(`SELECT slug FROM series ORDER BY slug`), "none")
	queries := []string{"", "?q=abc&lang=en", "?q=juq-743", "?limit=100&sort=newest", "?release=juq-743&performer=jane-doe&series=x"}

	h := api.New(e.s, nil)
	walked, routes := 0, 0
	var failures []string
	for _, pattern := range api.Patterns() {
		method, path, _ := strings.Cut(pattern, " ")
		if method != http.MethodGet || strings.HasPrefix(path, "/api/admin/") || skipPublicRoutes[pattern] {
			continue
		}
		routes++
		values := []string{""}
		switch {
		case !strings.Contains(path, "{slug}"):
		case strings.HasPrefix(path, "/api/performers/"):
			values = performerSlugs
		case strings.HasPrefix(path, "/api/series/"):
			values = seriesSlugs
		default:
			values = releaseSlugs
		}
		for _, v := range values {
			p := strings.ReplaceAll(strings.ReplaceAll(path, "{slug}", v), "{lang}", "en")
			p = wildcard.ReplaceAllString(restWildcard.ReplaceAllString(p, "a/b"), "1")
			for _, q := range queries {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(method, p+q, nil))
				body, _ := io.ReadAll(rec.Body)
				walked++
				if rec.Code >= 500 {
					failures = append(failures, fmt.Sprintf("GET %s%s: HTTP %d %s", p, q, rec.Code, body))
				}
				for marker, what := range leaks {
					if strings.Contains(string(body), marker) {
						failures = append(failures, fmt.Sprintf("GET %s%s leaks %s (%q)", p, q, what, marker))
					}
				}
			}
		}
	}
	sort.Strings(failures)
	for _, f := range failures {
		t.Error(f)
	}
	t.Logf("walked %d requests over %d public GET routes", walked, routes)
	if routes < 30 || walked < 500 {
		t.Errorf("walked only %d requests over %d routes", walked, routes)
	}

	// Non-public releases are 404 in every detail route; published ones are visible.
	for _, slug := range []string{"abc-002", "abc-003", "abp-123"} {
		for _, p := range []string{"/api/releases/" + slug, "/api/releases/" + slug + "/evidence", "/api/releases/" + slug + "/performers"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
			if rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404 for a non-public release", p, rec.Code)
			}
		}
	}
	// A release held down by a copyright ticket is 451, its sub-resources 404.
	for p, want := range map[string]int{
		"/api/releases/td-canary-001":            http.StatusUnavailableForLegalReasons,
		"/api/releases/td-canary-001/evidence":   http.StatusNotFound,
		"/api/releases/td-canary-001/performers": http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != want {
			t.Errorf("GET %s = %d, want %d", p, rec.Code, want)
		}
	}
	// The ticket lookup needs the email (header) and returns the status only.
	for email, want := range map[string]int{"": http.StatusNotFound, "wrong@example.com": http.StatusNotFound, strings.ToUpper(tdEmail): http.StatusOK} {
		req := httptest.NewRequest(http.MethodGet, "/api/takedowns/"+receipt.Ticket, nil)
		req.Header.Set("X-Takedown-Email", email)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != want || strings.Contains(body, tdName) || strings.Contains(body, counterEmail) || strings.Contains(body, "td-canary-001") {
			t.Errorf("ticket lookup with %q = %d %s", email, rec.Code, body)
		}
	}
	for p, want := range map[string]string{
		"/api/releases/juq-743":             `"juq-743"`,
		"/api/releases/juq-743/evidence":    `dmm-itemlist-fixture`,
		"/api/performers/jane-doe":          `"jane-doe"`,
		"/api/performers/jane-doe/evidence": `javguru-fixture`,
		"/api/releases?limit=100":           `"abc-001"`,
		"/api/items":                        `Feed Two`,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d, want %s in %.300s", p, rec.Code, want, rec.Body)
		}
	}
}
