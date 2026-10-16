package store

import (
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestCrawlGovernanceIntegration exercises migration 024 against a real,
// disposable PostgreSQL database: the license gate in Enqueue / EnqueueDue /
// Claim, one running job per source with phase ordering, per-phase budgets
// and checkpoints, a real incremental crawl through the store, and the field
// priority policy in SaveCatalogRecord. Skipped unless AVXXX_TEST_DATABASE_URL
// is set. Other pending crawl jobs are pushed into the future so Claim only
// sees this test's jobs.
func TestCrawlGovernanceIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // migration 024 must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}
	text := func(q string, args ...any) string {
		t.Helper()
		var v *string
		must(s.QueryRow(ctx, q, args...).Scan(&v))
		if v == nil {
			return "<nil>"
		}
		return *v
	}
	sfx := fmt.Sprint(time.Now().UnixNano())

	// ---- Conservative license migration and the CHECK constraint.
	if _, err := s.Exec(ctx, `INSERT INTO sources(name,url,license_status) VALUES('bad','https://bad.test/'||$1,'whatever')`, sfx); err == nil {
		t.Fatal("unknown license_status accepted")
	}

	var catID, feedID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('gov-cat','https://gov-cat.test/'||$1,'javdatabase','structured_database') RETURNING id`, sfx).Scan(&catID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('gov-feed','https://gov-feed.test/'||$1) RETURNING id`, sfx).Scan(&feedID))
	isolate := func() {
		exec(`UPDATE jobs SET available_at=now()+interval '1 day' WHERE status='pending' AND source_id IS NOT NULL AND source_id NOT IN ($1,$2)`, catID, feedID)
	}

	// ---- EnqueueDue: the unreviewed catalog source gets a license_gate
	// record instead of a job; the feed is not a catalog source.
	_, err = s.EnqueueDue(ctx)
	must(err)
	if n := count(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='pending'`, catID); n != 0 {
		t.Fatalf("gated source got %d runnable jobs", n)
	}
	if n := count(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='failed' AND error_code='license_gate' AND phase='incremental'`, catID); n != 1 {
		t.Fatalf("license_gate records = %d", n)
	}
	if n := count(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='pending' AND phase='incremental'`, feedID); n != 1 {
		t.Fatalf("feed jobs = %d", n)
	}
	exec(`UPDATE sources SET next_run_at=NULL WHERE id=$1`, catID)
	_, err = s.EnqueueDue(ctx)
	must(err)
	if n := count(`SELECT count(*) FROM jobs WHERE source_id=$1 AND error_code='license_gate'`, catID); n != 1 {
		t.Fatalf("an unchanged gate reason must be recorded once, got %d", n)
	}

	// ---- Manual jobs: automatic phases refused, verify capped.
	var refusal *GateRefusal
	if _, err = s.Enqueue(ctx, catID, EnqueueRequest{Phase: catalog.PhaseBaseline, Actor: "alice"}); !errors.As(err, &refusal) || refusal.Code != catalog.CodeLicenseGate {
		t.Fatalf("baseline on unreviewed source: %v", err)
	}
	verifyID, err := s.Enqueue(ctx, catID, EnqueueRequest{Phase: catalog.PhaseVerify, MaxPages: 50, Actor: "alice"})
	must(err)
	if n := count(`SELECT count(*) FROM jobs WHERE id=$1 AND page_cap=$2 AND requested_by='alice'`, verifyID, catalog.VerifyMaxPages); n != 1 {
		t.Fatal("verify job page cap not clamped")
	}

	// ---- License review: audited, unlocks automatic phases.
	terms := "https://gov-cat.test/terms"
	src, err := s.SetSourceLicense(ctx, catID, LicenseReview{LicenseStatus: catalog.LicenseTermsConfirmed, TermsURL: &terms, Reviewed: true, Note: "metadata crawling permitted"}, "alice")
	must(err)
	if !src.AutomaticAllowed || src.TermsReviewedBy != "alice" || src.TermsReviewedAt == nil {
		t.Fatalf("after review %+v", src)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='source' AND entity_id=$1 AND action='license_review' AND actor='alice'
		AND before_value->>'license_status'='review_required' AND after_value->>'license_status'='terms_confirmed'`, catID); n != 1 {
		t.Fatalf("license review events = %d", n)
	}
	var ve *ValidationError
	if _, err = s.SetSourceLicense(ctx, catID, LicenseReview{LicenseStatus: "bogus"}, "alice"); !errors.As(err, &ve) {
		t.Fatalf("bogus status: %v", err)
	}
	baselineID, err := s.Enqueue(ctx, catID, EnqueueRequest{Phase: catalog.PhaseBaseline, Actor: "alice"})
	must(err)
	var ce *ConflictError
	if _, err = s.Enqueue(ctx, catID, EnqueueRequest{Phase: catalog.PhaseBaseline}); !errors.As(err, &ce) {
		t.Fatalf("second active baseline: %v", err)
	}
	if text(`SELECT baseline_requested_by FROM sources WHERE id=$1`, catID) != "alice" {
		t.Fatal("baseline request not recorded")
	}
	exec(`UPDATE sources SET next_run_at=NULL WHERE id=$1`, catID)
	_, err = s.EnqueueDue(ctx)
	must(err)
	if n := count(`SELECT count(*) FROM jobs WHERE source_id=$1 AND status='pending' AND phase IN ('incremental','baseline','verify')`, catID); n != 3 {
		t.Fatalf("pending phases = %d, want verify+incremental+baseline", n)
	}

	// ---- Claim: verify first, one running job per source, incremental
	// before baseline, gate re-checked at claim time.
	isolate()
	j1, err := s.Claim(ctx)
	must(err)
	if j1.ID != verifyID || j1.Phase != catalog.PhaseVerify || j1.PageCap != catalog.VerifyMaxPages {
		t.Fatalf("first claim %+v", j1)
	}
	j2, err := s.Claim(ctx)
	must(err)
	if j2.Source.ID != feedID {
		t.Fatalf("while the catalog source runs, only the feed may be claimed: %+v", j2)
	}
	if _, err = s.Claim(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("third claim: %v", err)
	}
	must(s.Finish(ctx, j1.ID, nil, nil))
	must(s.Finish(ctx, j2.ID, nil, &catalog.RobotsError{URL: "https://gov-feed.test/x", Reason: "disallowed"}))
	if n := count(`SELECT count(*) FROM jobs WHERE id=$1 AND status='failed' AND error_code='robots_disallow'`, j2.ID); n != 1 {
		t.Fatal("robots refusal must fail the job without retry")
	}
	j3, err := s.Claim(ctx)
	must(err)
	if j3.Source.ID != catID || j3.Phase != catalog.PhaseIncremental {
		t.Fatalf("incremental must run before baseline: %+v", j3)
	}
	must(s.Finish(ctx, j3.ID, nil, nil))
	// The license is withdrawn before the baseline runs: Claim closes it.
	_, err = s.SetSourceLicense(ctx, catID, LicenseReview{LicenseStatus: catalog.LicenseManualOnly, Note: "terms changed"}, "bob")
	must(err)
	isolate()
	if _, err = s.Claim(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("gated baseline claimed: %v", err)
	}
	if n := count(`SELECT count(*) FROM jobs WHERE id=$1 AND status='failed' AND error_code='license_gate'`, baselineID); n != 1 {
		t.Fatal("claim-time gate must close the baseline job with license_gate")
	}
	// An open baseline request on a gated source is withdrawn by the scheduler.
	_, err = s.EnqueueDue(ctx)
	must(err)
	if text(`SELECT baseline_requested_at::text FROM sources WHERE id=$1`, catID) != "<nil>" {
		t.Fatal("gated baseline request not withdrawn")
	}
	_, err = s.SetSourceLicense(ctx, catID, LicenseReview{LicenseStatus: catalog.LicenseLicensed, TermsURL: &terms, Reviewed: true}, "alice")
	must(err)

	// ---- Per-phase budgets.
	must(s.UpsertCrawlPolicy(ctx, CrawlPolicy{SourceID: catID, MinRequestInterval: "2 seconds", DailyPageBudget: 10, BaselineDailyPageBudget: 5, BaselinePagesPerRun: 7}))
	must(s.RecordCrawlUsage(ctx, catID, catalog.PhaseBaseline, 5, 100))
	base, err := s.FetchPolicy(ctx, catID, catalog.PhaseBaseline)
	must(err)
	inc, err := s.FetchPolicy(ctx, catID, catalog.PhaseIncremental)
	must(err)
	ver, err := s.FetchPolicy(ctx, catID, catalog.PhaseVerify)
	must(err)
	if !base.Exhausted || inc.Exhausted || inc.Fetch.MaxPages != 10 || inc.Fetch.MinInterval != 2*time.Second || ver.Fetch.MaxPages != catalog.VerifyMaxRequests {
		t.Fatalf("budgets base=%+v inc=%+v ver=%+v", base, inc, ver)
	}
	if b2, _ := s.FetchPolicy(ctx, feedID, catalog.PhaseBaseline); b2.MaxPages != defaultBaselinePagesPerRun {
		t.Fatalf("baseline pages per run default %+v", b2)
	}
	pols, err := s.CrawlPolicies(ctx)
	must(err)
	found := false
	for _, p := range pols {
		found = found || (p.SourceID == catID && p.BaselinePagesPerRun == 7)
	}
	if !found {
		t.Fatal("crawl policy not listed")
	}

	// ---- A real incremental crawl through the store: its checkpoint lives
	// in the incremental partition and never touches the baseline one.
	code := fmt.Sprintf("GOV-%s", sfx[len(sfx)-6:])
	slug := strings.ToLower(code)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/movies/":
			fmt.Fprintf(w, `<html><body><a href="/movies/%s/">%s</a></body></html>`, slug, code)
		case "/movies/" + slug + "/":
			fmt.Fprintf(w, `<html><body><h1>Gov Title</h1><p>DVD ID: %s</p><p>Release Date: 2024-01-02</p><p>Studio: <a>Gov Maker</a></p></body></html>`, code)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	crawl := &catalog.Crawl{SourceID: catID, StartURL: srv.URL + "/movies/", Phase: catalog.PhaseIncremental, Adapter: catalog.JAVDatabaseAdapter{},
		Fetcher: catalog.NewFetcher(srv.Client(), catalog.FetchPolicy{}), Checkpoints: CatalogCheckpoints{S: s}, Sink: s}
	res, err := crawl.Run(ctx)
	must(err)
	if res.Records != 1 || res.Stop != catalog.StopComplete {
		t.Fatalf("crawl %+v", res)
	}
	if got := text(`SELECT cursor FROM crawl_checkpoints WHERE source_id=$1 AND task_type='catalog_discovery' AND partition_key='incremental'`, catID); got != "release:"+slug {
		t.Fatalf("incremental mark %q", got)
	}
	if n := count(`SELECT count(*) FROM crawl_checkpoints WHERE source_id=$1 AND partition_key='baseline'`, catID); n != 0 {
		t.Fatal("incremental crawl wrote a baseline checkpoint")
	}
	if res, err = crawl.Run(ctx); err != nil || res.Stop != catalog.StopWatermark || res.Records != 0 {
		t.Fatalf("second incremental run %+v %v", res, err)
	}
	cps, err := s.SourceCheckpoints(ctx, catID)
	must(err)
	if len(cps) != 1 || cps[0].PartitionKey != "incremental" || cps[0].Watermark == nil {
		t.Fatalf("checkpoints %+v", cps)
	}
	// The pre-024 unpartitioned discovery checkpoint migrates to baseline.
	exec(`INSERT INTO crawl_checkpoints(source_id,task_type,partition_key,cursor,parser_version) VALUES($1,'catalog_discovery','','https://gov-feed.test/p/3','x')`, feedID)
	s.Close()
	s, err = Open(ctx)
	must(err)
	if n := count(`SELECT count(*) FROM crawl_checkpoints WHERE source_id=$1 AND task_type='catalog_discovery' AND partition_key='baseline' AND cursor='https://gov-feed.test/p/3'`, feedID); n != 1 {
		t.Fatal("legacy checkpoint not moved to the baseline partition")
	}
	must(s.MarkBaselineComplete(ctx, catID))
	if text(`SELECT (baseline_completed_at IS NOT NULL)::text FROM source_crawl_policies WHERE source_id=$1`, catID) != "true" {
		t.Fatal("baseline completion not recorded")
	}

	// ---- Field priority policy.
	var officialID, aggID int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('fp-official','https://fp-o.test/'||$1,'javdatabase','official_catalog') RETURNING id`, sfx).Scan(&officialID))
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url,adapter,source_kind) VALUES('fp-agg','https://fp-a.test/'||$1,'javguru','aggregator') RETURNING id`, sfx).Scan(&aggID))
	fpCode := "FPC-" + sfx[len(sfx)-6:]
	rel := func(key, title, maker, date string) catalog.Record {
		return catalog.Record{EntityType: catalog.Release, SourceKey: key + sfx, URL: "https://fp.test/" + key + sfx,
			Fields: map[string]any{"title": title, "dvd_code": fpCode, "maker": maker, "release_date": date, "content_id": ""}}
	}
	aggRec := rel("agg", "Agg Title", "Same Maker", "2024-03-01")
	_, err = s.SaveCatalogRecord(ctx, aggID, "t1", aggRec)
	must(err)
	var relID int64
	must(s.QueryRow(ctx, `SELECT release_id FROM release_identifiers WHERE identifier_value=$1`, fpCode).Scan(&relID))
	if got := text(`SELECT canonical_title FROM releases WHERE id=$1`, relID); got != "Agg Title" {
		t.Fatalf("single source title %q", got)
	}
	_, err = s.SaveCatalogRecord(ctx, officialID, "t1", rel("off", "Official Title", "Same Maker", "2024-03-02"))
	must(err)
	_, err = s.SaveCatalogRecord(ctx, aggID, "t2", aggRec) // a lower-priority re-crawl must not win back
	must(err)
	if got := text(`SELECT canonical_title||'|'||release_date::text FROM releases WHERE id=$1`, relID); got != "Official Title|2024-03-02" {
		t.Fatalf("official_catalog must win: %q", got)
	}
	if got := text(`SELECT current_value #>> '{}' FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name='title' AND status='pending'`, relID); got != "Official Title" {
		t.Fatalf("title conflict current_value %q", got)
	}
	if n := count(`SELECT count(*) FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name IN ('maker','content_id') AND status='pending'`, relID); n != 0 {
		t.Fatalf("agreeing or empty fields opened %d conflicts", n)
	}
	// A policy row lets the aggregator win the title only.
	p, err := s.SetFieldPolicy(ctx, FieldPolicy{EntityType: "release", FieldName: "title", SourceID: aggID, Priority: 1}, "aggregator has the better romanization", "carol")
	must(err)
	if p.FieldName != "canonical_title" {
		t.Fatalf("policy field not normalized: %+v", p)
	}
	_, err = s.SaveCatalogRecord(ctx, aggID, "t3", aggRec)
	must(err)
	if got := text(`SELECT canonical_title||'|'||release_date::text FROM releases WHERE id=$1`, relID); got != "Agg Title|2024-03-02" {
		t.Fatalf("policy must move only the title: %q", got)
	}
	order, err := s.EffectiveFieldOrder(ctx, "release", "title")
	must(err)
	if len(order) < 2 || order[0].SourceID != aggID || !order[0].FromPolicy {
		t.Fatalf("effective order %+v", order[:min(len(order), 3)])
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='source' AND entity_id=$1 AND action='field_policy_set' AND actor='carol'`, aggID); n != 1 {
		t.Fatal("policy change not audited")
	}
	// Admin overrides beat every policy.
	conflictID := int64(0)
	must(s.QueryRow(ctx, `SELECT id FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name='title' AND status='pending'`, relID).Scan(&conflictID))
	_, err = s.ResolveConflict(ctx, conflictID, 0, json.RawMessage(`"Admin Title"`), "checked the box", "dave")
	must(err)
	_, err = s.SaveCatalogRecord(ctx, officialID, "t2", rel("off", "Official Title v2", "Same Maker", "2024-03-02"))
	must(err)
	if got := text(`SELECT canonical_title FROM releases WHERE id=$1`, relID); got != "Admin Title" {
		t.Fatalf("override lost: %q", got)
	}
	if n := count(`SELECT count(*) FROM field_conflicts WHERE entity_type='release' AND entity_id=$1 AND field_name='title' AND status='pending'`, relID); n != 0 {
		t.Fatal("overridden field reopened a conflict")
	}
	must(s.DeleteFieldPolicy(ctx, "release", "canonical_title", aggID, "revert", "carol"))
	if err = s.DeleteFieldPolicy(ctx, "release", "title", aggID, "", "carol"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("delete twice: %v", err)
	}
}
