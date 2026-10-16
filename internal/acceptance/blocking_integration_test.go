package acceptance

import (
	"avxxx/internal/catalog"
	"avxxx/internal/store"
	"errors"
	"net/http"
	"strings"
	"testing"
)

const logoOnlyPage = `<!DOCTYPE html><html><head><meta charset="utf-8"><title>JAV Japanese Porn Actress &amp; Idol Database</title></head>
<body><header class="site-header"><h1 class="site-title"><a href="/">JAV Japanese Porn Actress &amp; Idol Database</a></h1></header>
<main><p>DOB: 2001-01-01</p></main></body></html>`

const challengePage = `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body><form id="challenge-form" action="/cdn-cgi/challenge-platform/h/b"></form></body></html>`

const emptyListing = `<!DOCTYPE html><html><head><title>Movies Archive - JAV Database</title></head><body><p>Nothing found.</p></body></html>`

// retryable is the error code of an ordinary failure the queue retries
// (jobs.status back to pending); the other codes fail the job for good.
const retryable = "fetch_failed"

// checkUnchanged compares the database with before. strict: the run saved
// nothing, so nothing at all may differ (last_seen_at and credit display
// attributes included). Otherwise records the run re-saved before it failed
// may refresh last_seen_at (never lose it) but no stored value may change.
// Discovery checkpoints must never move.
func (e *env) checkUnchanged(before snapshot, strict bool) {
	e.t.Helper()
	after := e.snap()
	tables := entityTables
	if strict {
		tables = append([]string{"release_performers"}, entityTables...)
	}
	if d := diffRows(before, after, tables); d != "" {
		e.t.Errorf("stored catalog values changed:%s", d)
	}
	if d := diffRows(before, after, []string{"discovery_checkpoints"}); d != "" {
		e.t.Errorf("discovery checkpoints moved:%s", d)
	}
	if d := keptLastSeen(before, after); d != "" {
		e.t.Errorf("source records lost last_seen_at:%s", d)
	}
	if strict {
		if d := sameLastSeen(before, after); d != "" {
			e.t.Errorf("a run that saved nothing touched last_seen_at:%s", d)
		}
	}
}

// checkValues is checkUnchanged for a run that succeeded: a completed pass
// legitimately advances its checkpoint and refreshes last_seen_at, but no
// stored value may change.
func (e *env) checkValues(before snapshot) {
	e.t.Helper()
	after := e.snap()
	if d := diffRows(before, after, entityTables); d != "" {
		e.t.Errorf("stored catalog values changed:%s", d)
	}
	if d := keptLastSeen(before, after); d != "" {
		e.t.Errorf("source records lost last_seen_at:%s", d)
	}
}

func (e *env) wantFailed(o outcome, code string) {
	e.t.Helper()
	want := "failed"
	if code == retryable {
		want = "pending"
	}
	if o.Status != want || o.ErrorCode != code {
		e.t.Errorf("want %s/%s, got %s", want, code, o)
	}
}

func (e *env) blocked(src int64) bool {
	return e.int64(`SELECT count(*) FROM sources WHERE id=$1 AND blocked_until>now()`, src) == 1
}

// TestBlockingNeverBlanksDataIntegration crawls every fixture site, then
// replays each way a source can refuse or break — HTTP 403 / 429 / 500 / 503
// challenge, challenge pages, robots.txt disallow, license gate, budget
// exhaustion, unparsable, skipped, degraded, empty and truncated pages, DMM
// API errors — and requires that no stored value is deleted or blanked, no
// source record loses its last_seen_at and no discovery checkpoint moves
// past the failed batch (FINAL_DATA_DESIGN 11: 403、503、验证码和空响应不会清空
// 已有字段; checkpoint 只在完整批次成功后推进).
func TestBlockingNeverBlanksDataIntegration(t *testing.T) {
	root := newEnv(t)
	c := root.newCatalog(t)
	root.crawlAll(c, catalog.PhaseBaseline)
	root.crawlAll(c, catalog.PhaseIncremental)
	jdb := c.jdbID
	url := "https://acceptance.test/terms"

	// restore puts the fixtures, robots.txt, license, policy and queue back.
	restore := func(e *env, src int64) {
		c.load(variant{})
		c.jdb.setPage("/robots.txt", page{status: http.StatusNotFound})
		c.dmm.setPage("/robots.txt", page{status: http.StatusNotFound})
		e.freshRobots()
		e.unblock(src)
		if _, err := e.s.SetSourceLicense(e.ctx, jdb, store.LicenseReview{LicenseStatus: catalog.LicenseTermsConfirmed, TermsURL: &url, Reviewed: true}, "acceptance"); err != nil {
			e.t.Fatal(err)
		}
		if err := e.s.UpsertCrawlPolicy(e.ctx, store.CrawlPolicy{SourceID: src, MinRequestInterval: "0 seconds"}); err != nil {
			e.t.Fatal(err)
		}
		e.exec(`DELETE FROM source_crawl_usage WHERE source_id=$1`, src)
		e.exec(`UPDATE jobs SET status='failed',finished_at=now() WHERE status IN ('pending','running')`)
	}
	listing := func(p page) func() { return func() { c.jdb.setPage("/movies/", p) } }

	for _, sc := range []struct {
		name   string
		phase  catalog.Phase
		inject func()
		code   string // expected jobs.error_code
		block  bool   // the source is parked as blocked
	}{
		{"HTTP 403 on the listing", catalog.PhaseIncremental, listing(page{status: http.StatusForbidden, body: "Forbidden"}), "source_blocked", true},
		{"HTTP 429 with a long Retry-After", catalog.PhaseIncremental,
			listing(page{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "3600"}}), "source_blocked", true},
		{"HTTP 503 challenge on the listing", catalog.PhaseIncremental,
			listing(page{status: http.StatusServiceUnavailable, body: challengePage, header: map[string]string{"cf-mitigated": "challenge"}}), "source_blocked", true},
		{"HTTP 200 challenge page on the first detail page", catalog.PhaseBaseline,
			func() { c.jdb.set("/movies/juq-743/", challengePage) }, "source_blocked", true},
		{"HTTP 500 on the listing", catalog.PhaseIncremental, listing(page{status: http.StatusInternalServerError, body: "oops"}), retryable, false},
		{"HTTP 404 on the listing", catalog.PhaseBaseline, listing(page{status: http.StatusNotFound, body: "gone"}), retryable, false},
		{"robots.txt disallows the site", catalog.PhaseIncremental,
			func() { c.jdb.set("/robots.txt", "User-agent: *\nDisallow: /\n") }, catalog.CodeRobotsDisallow, false},
		{"robots.txt unreachable (5xx) on an automatic crawl", catalog.PhaseIncremental,
			func() { c.jdb.setPage("/robots.txt", page{status: http.StatusBadGateway}) }, catalog.CodeRobotsDisallow, false},
		{"unparsable release page (no title)", catalog.PhaseBaseline,
			func() {
				c.jdb.set("/movies/juq-743/", "<html><body><p>Service temporarily unavailable</p></body></html>")
			}, retryable, false},
		{"empty listing page (incremental)", catalog.PhaseIncremental, listing(page{body: emptyListing}), retryable, false},
		{"empty listing page (baseline)", catalog.PhaseBaseline, listing(page{body: emptyListing}), retryable, false},
		{"truncated (zero-byte) listing page", catalog.PhaseBaseline, listing(page{}), retryable, false},
	} {
		t.Run(sc.name, func(t *testing.T) {
			e := root.sub(t)
			defer restore(e, jdb)
			before := e.snap()
			listingHits := c.jdb.hitCount("/movies/")
			sc.inject()
			e.freshRobots()
			e.wantFailed(e.run(jdb, sc.phase), sc.code)
			if got := e.blocked(jdb); got != sc.block {
				t.Errorf("source blocked = %v, want %v", got, sc.block)
			}
			if sc.code == catalog.CodeRobotsDisallow && c.jdb.hitCount("/movies/") != listingHits {
				t.Error("robots.txt refused the listing but it was fetched")
			}
			e.checkUnchanged(before, true)
		})
	}

	t.Run("blocked source is not claimed until Retry-After; others keep running", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		c.jdb.setPage("/movies/", page{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "3600"}})
		e.wantFailed(e.run(jdb, catalog.PhaseIncremental), "source_blocked")
		e.mustSucceed(e.run(c.guruID, catalog.PhaseIncremental), "javguru while javdatabase is blocked")
		before := e.snap()
		if o := e.run(jdb, catalog.PhaseIncremental); o.Status != "not_claimed" {
			t.Errorf("a parked source ran: %s", o)
		}
		e.checkUnchanged(before, true)
	})

	t.Run("challenge on page 2 keeps the baseline cursor on page 2", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		before := e.snap()
		c.jdb.set("/movies/abc-002/", challengePage)
		e.wantFailed(e.run(jdb, catalog.PhaseBaseline), "source_blocked")
		after := e.snap()
		if d := diffRows(before, after, entityTables); d != "" {
			t.Errorf("stored values changed:%s", d)
		}
		if d := keptLastSeen(before, after); d != "" {
			t.Errorf("source records lost last_seen_at:%s", d)
		}
		cursorSQL := `SELECT cursor FROM crawl_checkpoints WHERE source_id=$1 AND task_type='catalog_discovery' AND partition_key='baseline'`
		if got, want := e.str(cursorSQL, jdb), c.jdb.url("/movies/page/2/"); got != want {
			t.Errorf("baseline cursor = %q, want the failed page %q", got, want)
		}
		// The next run resumes on page 2, not from the head.
		c.load(variant{})
		e.unblock(jdb)
		head := c.jdb.hitCount("/movies/")
		e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "resumed baseline")
		if c.jdb.hitCount("/movies/") != head {
			t.Error("resumed baseline re-read page 1")
		}
		if got := e.str(cursorSQL, jdb); got != "" {
			t.Errorf("baseline cursor after the completed pass = %q", got)
		}
	})

	t.Run("robots.txt disallows one detail page", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		before := e.snap()
		c.jdb.set("/robots.txt", "User-agent: *\nDisallow: /movies/juq-743/\n")
		e.freshRobots()
		hits := c.jdb.hitCount("/movies/juq-743/")
		o := e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "baseline with one disallowed page")
		if o.Result["robots_skipped"] != float64(1) {
			t.Errorf("robots_skipped = %v, want 1", o.Result["robots_skipped"])
		}
		if c.jdb.hitCount("/movies/juq-743/") != hits {
			t.Error("disallowed detail page was fetched")
		}
		after := e.snap()
		if d := diffRows(before, after, entityTables); d != "" {
			t.Errorf("stored values changed:%s", d)
		}
		if k := "javdatabase-fixture|release|juq-743"; !after.lastSeen[k].Equal(before.lastSeen[k]) {
			t.Errorf("%s last_seen_at moved although the page was not fetched", k)
		}
	})

	t.Run("license gate", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		before := e.snap()
		if _, err := e.s.SetSourceLicense(e.ctx, jdb, store.LicenseReview{LicenseStatus: catalog.LicenseReviewRequired}, "acceptance"); err != nil {
			t.Fatal(err)
		}
		for _, ph := range []catalog.Phase{catalog.PhaseBaseline, catalog.PhaseIncremental} {
			var gr *store.GateRefusal
			if _, err := e.s.Enqueue(e.ctx, jdb, store.EnqueueRequest{Phase: ph}); !errors.As(err, &gr) || gr.Code != catalog.CodeLicenseGate {
				t.Errorf("enqueue %s under review_required: err %v, want a license_gate refusal", ph, err)
			}
		}
		e.exec(`UPDATE sources SET next_run_at=now()-interval '1 minute' WHERE id=$1`, jdb)
		if _, err := e.s.EnqueueDue(e.ctx); err != nil {
			t.Fatal(err)
		}
		if n := e.int64(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='pending'`, jdb); n != 0 {
			t.Errorf("scheduler queued %d runnable jobs for a gated source", n)
		}
		if n := e.int64(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='failed' AND error_code='license_gate'`, jdb); n == 0 {
			t.Error("scheduler did not record why the gated source got no job")
		}
		e.exec(`UPDATE jobs SET status='failed',finished_at=now() WHERE status='pending'`) // other sources' due jobs
		// Verify still runs (manual, page-capped) and writes nothing.
		e.mustSucceed(e.run(jdb, catalog.PhaseVerify), "verify under review_required")
		e.checkUnchanged(before, true)
		// A job queued before the license was withdrawn is refused at claim.
		if _, err := e.s.SetSourceLicense(e.ctx, jdb, store.LicenseReview{LicenseStatus: catalog.LicenseTermsConfirmed, TermsURL: &url, Reviewed: true}, "acceptance"); err != nil {
			t.Fatal(err)
		}
		job, err := e.s.Enqueue(e.ctx, jdb, store.EnqueueRequest{Phase: catalog.PhaseIncremental})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.SetSourceLicense(e.ctx, jdb, store.LicenseReview{LicenseStatus: catalog.LicenseManualOnly, TermsURL: &url, Reviewed: true}, "acceptance"); err != nil {
			t.Fatal(err)
		}
		hits := c.jdb.hitCount("/movies/")
		if o := e.claimAndRun(jdb); o.Status != "not_claimed" {
			t.Errorf("gated job ran: %s", o)
		}
		if code := e.str(`SELECT error_code FROM jobs WHERE id=$1`, job); code != catalog.CodeLicenseGate {
			t.Errorf("gated job error_code = %s, want license_gate", code)
		}
		if c.jdb.hitCount("/movies/") != hits {
			t.Error("gated job fetched the listing")
		}
		e.checkUnchanged(before, true)
	})

	t.Run("budget exhausted mid-run, then at start", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		e.exec(`DELETE FROM source_crawl_usage WHERE source_id=$1`, jdb)
		before := e.snap()
		// One listing page and one detail page, then the budget is spent.
		if err := e.s.UpsertCrawlPolicy(e.ctx, store.CrawlPolicy{SourceID: jdb, MinRequestInterval: "0 seconds", BaselineDailyPageBudget: 2}); err != nil {
			t.Fatal(err)
		}
		o := e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "budget-stopped baseline")
		if o.Result["stop"] != string(catalog.StopBudget) {
			t.Errorf("stop = %v, want budget", o.Result["stop"])
		}
		e.checkUnchanged(before, false)
		hits := c.jdb.hitCount("/movies/")
		e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "exhausted baseline")
		if c.jdb.hitCount("/movies/") != hits {
			t.Error("a run with an exhausted budget fetched the listing")
		}
		e.checkUnchanged(before, false)
	})

	t.Run("performer page without a name is skipped (ErrSkipRecord)", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		before := e.snap()
		c.jdb.set("/idols/yurine-tsukino/", logoOnlyPage)
		o := e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "baseline with a skipped page")
		if o.Result["parse_skipped"] != float64(1) {
			t.Errorf("parse_skipped = %v, want 1", o.Result["parse_skipped"])
		}
		e.checkValues(before)
		if k, after := "javdatabase-fixture|performer|yurine-tsukino", e.snap(); !after.lastSeen[k].Equal(before.lastSeen[k]) {
			t.Errorf("%s last_seen_at moved although its page was skipped", k)
		}
	})

	t.Run("degraded release page (title only) keeps the release and its data", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, jdb)
		before := e.snap()
		c.jdb.set("/movies/abc-001/", htmlPage("ABC-001 Example Title - JAV Database", `<div class="entry-header"><h1 class="entry-title">ABC-001 Example Title</h1></div>`))
		e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "baseline with a degraded page")
		e.checkValues(before)
		// The plain page again: the record is back to full values on the same release.
		c.load(variant{})
		e.mustSucceed(e.run(jdb, catalog.PhaseBaseline), "baseline after the page recovered")
		e.checkValues(before)
	})

	t.Run("DMM API error and 429 keep data and never leak credentials", func(t *testing.T) {
		e := root.sub(t)
		defer restore(e, c.dmmID)
		before := e.snap()
		c.dmm.set(dmmItemList, `{"request":{"parameters":{"api_id":"`+dmmAPIID+`"}},"result":{"status":"400","message":"LIMIT_EXCEEDED"}}`)
		e.wantFailed(e.run(c.dmmID, catalog.PhaseIncremental), retryable)
		e.checkUnchanged(before, true)
		c.dmm.setPage(dmmItemList, page{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "7200"}})
		e.wantFailed(e.run(c.dmmID, catalog.PhaseIncremental), "source_blocked")
		e.checkUnchanged(before, true)
		leaks := e.str(`SELECT string_agg(COALESCE(error,'')||' '||COALESCE(result_counts::text,''),' ') FROM jobs WHERE source_id=$1`, c.dmmID) +
			e.str(`SELECT COALESCE(blocked_reason,'') FROM sources WHERE id=$1`, c.dmmID) +
			e.str(`SELECT string_agg(COALESCE(cursor,''),' ') FROM crawl_checkpoints WHERE source_id=$1`, c.dmmID)
		for _, secret := range []string{dmmAPIID, dmmAffiliate} {
			if strings.Contains(leaks, secret) {
				t.Errorf("DMM credential %q in job errors / results / block reason / cursor: %s", secret, leaks)
			}
		}
	})
}
