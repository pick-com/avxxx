package acceptance

import (
	"avxxx/internal/catalog"
	"avxxx/internal/crawlrun"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- Database

// env is one acceptance test's private database, store and crawl runner.
type env struct {
	t        *testing.T
	ctx      context.Context
	s        *store.Store
	registry *catalog.Registry
	runner   *crawlrun.Runner
}

const testUA = "avxxx-catalog/acceptance (+https://acceptance.test/contact)"

// newEnv creates a fresh database next to AVXXX_TEST_DATABASE_URL (other
// packages' integration tests reset that one concurrently, and Claim takes
// any runnable job, so every test needs its own), runs the migrations and
// builds a runner with the javdatabase, xcity and javguru adapters. DMM is
// registered by newDMMSite with test credentials and a test host.
func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set (make test-integration)")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	dbName := fmt.Sprintf("avxxx_accept_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + dbName
	t.Setenv("DATABASE_URL", u.String())
	t.Setenv("ADMIN_TOKEN", "")
	t.Setenv("MEDIA_LINK_ON_DEMAND", "false")
	s, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	reg := catalog.NewRegistry()
	for _, a := range []catalog.Adapter{catalog.NewJAVDatabaseAdapter(), catalog.NewXCITYAdapter(), catalog.NewJavGuruAdapter()} {
		if err := reg.Register(a); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{t: t, ctx: ctx, s: s, registry: reg}
	e.runner = &crawlrun.Runner{Store: s, Registry: reg, UserAgent: testUA, Robots: catalog.NewRobots(&http.Client{Timeout: 5 * time.Second}, testUA)}
	return e
}

// sub returns e reporting to a subtest's t (the database and runner are shared).
func (e *env) sub(t *testing.T) *env {
	c := *e
	c.t = t
	return &c
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.s.Exec(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) int64(q string, args ...any) int64 {
	e.t.Helper()
	var v int64
	if err := e.s.QueryRow(e.ctx, q, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return v
}

func (e *env) str(q string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.s.QueryRow(e.ctx, q, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	if v == nil {
		return "<nil>"
	}
	return *v
}

// freshRobots drops the runner's robots.txt cache (a new worker process).
func (e *env) freshRobots() {
	e.runner.Robots = catalog.NewRobots(&http.Client{Timeout: 5 * time.Second}, testUA)
}

// addSource inserts an enabled catalog source whose terms were reviewed
// (license terms_confirmed unless kind says otherwise), with no request
// spacing so the suite runs fast.
func (e *env) addSource(name, adapter, startURL, kind string) int64 {
	e.t.Helper()
	license := catalog.LicenseTermsConfirmed
	if kind == "official_catalog" {
		license = catalog.LicenseOfficialAPI
	}
	id := e.int64(`INSERT INTO sources(name,url,adapter,source_kind,license_status,terms_url,terms_reviewed_at,terms_reviewed_by,enabled)
		VALUES($1,$2,$3,$4,$5,'https://acceptance.test/terms',now(),'acceptance',true) RETURNING id`, name, startURL, adapter, kind, license)
	if err := e.s.UpsertCrawlPolicy(e.ctx, store.CrawlPolicy{SourceID: id, MinRequestInterval: "0 seconds"}); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// ---- Jobs

// outcome is one job as the queue recorded it.
type outcome struct {
	JobID     int64
	Status    string // status right after Finish (before the suite settles retries)
	ErrorCode string
	Error     string
	Result    map[string]any
}

func (o outcome) String() string {
	return fmt.Sprintf("job %d status=%s code=%q err=%q result=%v", o.JobID, o.Status, o.ErrorCode, o.Error, o.Result)
}

// run enqueues one job of phase for src through the admin path, claims it,
// executes it exactly like cmd/scraper and finishes it. A retryable failure
// (status back to pending) is closed as failed so the next run can enqueue.
func (e *env) run(src int64, phase catalog.Phase) outcome {
	e.t.Helper()
	if _, err := e.s.Enqueue(e.ctx, src, store.EnqueueRequest{Phase: phase, Actor: "acceptance"}); err != nil {
		e.t.Fatalf("enqueue %s for source %d: %v", phase, src, err)
	}
	return e.claimAndRun(src)
}

func (e *env) claimAndRun(src int64) outcome {
	e.t.Helper()
	job, err := e.s.Claim(e.ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return outcome{Status: "not_claimed"}
	}
	if err != nil {
		e.t.Fatalf("claim: %v", err)
	}
	if job.Source.ID != src {
		e.t.Fatalf("claimed job %d of source %d, want source %d", job.ID, job.Source.ID, src)
	}
	items, cause := e.runner.Execute(e.ctx, job)
	if err := e.s.Finish(e.ctx, job.ID, items, cause); err != nil {
		e.t.Fatalf("finish job %d: %v", job.ID, err)
	}
	o := outcome{JobID: job.ID}
	var raw []byte
	if err := e.s.QueryRow(e.ctx, `SELECT status,COALESCE(error_code,''),COALESCE(error,''),COALESCE(result_counts,'{}'::jsonb) FROM jobs WHERE id=$1`, job.ID).
		Scan(&o.Status, &o.ErrorCode, &o.Error, &raw); err != nil {
		e.t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &o.Result)
	e.exec(`UPDATE jobs SET status='failed',finished_at=now() WHERE id=$1 AND status='pending'`, job.ID)
	return o
}

// mustSucceed fails the test unless the job succeeded.
func (e *env) mustSucceed(o outcome, what string) outcome {
	e.t.Helper()
	if o.Status != "succeeded" {
		e.t.Fatalf("%s: %s", what, o)
	}
	return o
}

// unblock clears a source's block (as if Retry-After had elapsed).
func (e *env) unblock(src int64) {
	e.exec(`UPDATE sources SET blocked_until=NULL,blocked_reason=NULL,next_run_at=NULL WHERE id=$1`, src)
}

// ---- Fixture sites

type page struct {
	status int
	body   string
	header map[string]string
}

// site is an httptest server serving fixture pages by path + sorted query
// (DMM credentials stripped), with per-page overrides to inject failures.
type site struct {
	srv   *httptest.Server
	mu    sync.Mutex
	pages map[string]page
	hits  map[string]int
	// creds, when set, must arrive as api_id / affiliate_id on every request
	// but /robots.txt (proves the DMM transport signs requests).
	creds *catalog.DMMCredentials
}

func pageKey(u *url.URL) string {
	q := u.Query()
	q.Del("api_id")
	q.Del("affiliate_id")
	q.Del("gte_date") // DMM incremental date filter: the fixture API ignores it
	if len(q) == 0 {
		return u.Path
	}
	return u.Path + "?" + q.Encode()
}

func newSite(t *testing.T) *site {
	s := &site{pages: map[string]page{}, hits: map[string]int{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k := pageKey(r.URL)
		s.mu.Lock()
		s.hits[k]++
		p, ok := s.pages[k]
		creds := s.creds
		s.mu.Unlock()
		if creds != nil && r.URL.Path != "/robots.txt" {
			q := r.URL.Query()
			if q.Get("api_id") != creds.APIID || q.Get("affiliate_id") != creds.AffiliateID {
				http.Error(w, "unsigned request", http.StatusBadRequest)
				return
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		for k, v := range p.header {
			w.Header().Set(k, v)
		}
		if p.status == 0 {
			p.status = http.StatusOK
		}
		w.WriteHeader(p.status)
		_, _ = w.Write([]byte(p.body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *site) key(pathQuery string) string {
	u, err := url.Parse(pathQuery)
	if err != nil {
		panic(err)
	}
	return pageKey(u)
}

// set serves body (HTTP 200) at pathQuery.
func (s *site) set(pathQuery, body string) { s.setPage(pathQuery, page{body: body}) }

func (s *site) setPage(pathQuery string, p page) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages[s.key(pathQuery)] = p
}

// get returns the page currently served at pathQuery.
func (s *site) get(pathQuery string) page {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pages[s.key(pathQuery)]
}

func (s *site) hitCount(pathQuery string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[s.key(pathQuery)]
}

func (s *site) url(pathQuery string) string { return s.srv.URL + pathQuery }

// ---- Snapshots

// snapshotQueries render every catalog table as sorted "|"-joined rows keyed
// by slugs and source keys (not ids), so a diff reads as data.
var snapshotQueries = []struct{ name, sql string }{
	{"performers", `SELECT concat_ws('|',p.slug,p.canonical_name,p.canonical_name_ja,p.birth_date,p.birth_place,p.debut_date,p.height_cm,p.cup_size,p.status,p.birth_date_hidden,m.slug)
		FROM performers p LEFT JOIN performers m ON m.id=p.merged_into_id`},
	{"releases", `SELECT concat_ws('|',r.slug,r.canonical_title,r.release_date,r.maker_name,r.director_name,r.label_name,r.series_name,r.runtime_minutes,r.status,m.slug)
		FROM releases r LEFT JOIN releases m ON m.id=r.merged_into_id`},
	{"release_identifiers", `SELECT concat_ws('|',r.slug,i.identifier_type,i.identifier_value,i.normalized_value,i.identifier_scope) FROM release_identifiers i JOIN releases r ON r.id=i.release_id`},
	{"performer_aliases", `SELECT concat_ws('|',p.slug,a.name,a.normalized_name) FROM performer_aliases a JOIN performers p ON p.id=a.performer_id`},
	{"performer_external_ids", `SELECT concat_ws('|',e.namespace,e.external_id,p.slug) FROM performer_external_ids e JOIN performers p ON p.id=e.performer_id`},
	{"release_performers", `SELECT concat_ws('|',r.slug,p.slug,rp.billing_order,rp.credited_name,rp.is_primary) FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id`},
	// Who is credited on what, without the display attributes (credited_name,
	// billing_order) that follow the last source to re-save a record.
	{"credits", `SELECT concat_ws('|',r.slug,p.slug) FROM release_performers rp JOIN releases r ON r.id=rp.release_id JOIN performers p ON p.id=rp.performer_id`},
	{"release_performer_sources", `SELECT concat_ws('|',r.slug,p.slug,sr.source_record_key) FROM release_performer_sources x JOIN releases r ON r.id=x.release_id JOIN performers p ON p.id=x.performer_id JOIN source_records sr ON sr.id=x.source_record_id`},
	{"release_tags", `SELECT concat_ws('|',r.slug,t.normalized_tag) FROM release_tags t JOIN releases r ON r.id=t.release_id`},
	{"release_tag_sources", `SELECT concat_ws('|',r.slug,t.normalized_tag,sr.source_record_key) FROM release_tag_sources t JOIN releases r ON r.id=t.release_id JOIN source_records sr ON sr.id=t.source_record_id`},
	{"source_records", `SELECT concat_ws('|',s.name,sr.entity_type,sr.source_record_key,COALESCE(p.slug,r.slug),sr.resolution_status)
		FROM source_records sr JOIN sources s ON s.id=sr.source_id LEFT JOIN performers p ON p.id=sr.performer_id LEFT JOIN releases r ON r.id=sr.release_id`},
	{"source_field_values_current", `SELECT concat_ws('|',s.name,sr.source_record_key,f.field_name,f.normalized_value::text)
		FROM source_field_values f JOIN source_records sr ON sr.id=f.source_record_id JOIN sources s ON s.id=sr.source_id WHERE f.is_current`},
	{"series", `SELECT concat_ws('|',slug,name,maker_name) FROM series`},
	{"slug_redirects", `SELECT concat_ws('|',entity_type,old_slug) FROM slug_redirects`},
	{"field_conflicts_pending", `SELECT concat_ws('|',entity_type,entity_id,field_name) FROM field_conflicts WHERE status='pending'`},
	{"release_overrides", `SELECT concat_ws('|',r.slug,o.field_name,o.value::text) FROM release_overrides o JOIN releases r ON r.id=o.release_id`},
	{"performer_overrides", `SELECT concat_ws('|',p.slug,o.field_name,o.value::text) FROM performer_overrides o JOIN performers p ON p.id=o.performer_id`},
	{"not_duplicate_pairs", `SELECT concat_ws('|',entity_type,a_id,b_id) FROM not_duplicate_pairs`},
	{"items", `SELECT concat_ws('|',url,title) FROM items`},
	{"discovery_checkpoints", `SELECT concat_ws('|',s.name,c.partition_key,c.cursor,c.watermark,c.completed_pages,c.last_success_at)
		FROM crawl_checkpoints c JOIN sources s ON s.id=c.source_id WHERE c.task_type='catalog_discovery'`},
}

// entityTables are the tables whose rows must never change when a crawl
// fails or is blocked (values included). Checkpoints are compared separately.
// A run that re-saved some records before failing may legitimately refresh
// the credit display attributes, so it is compared on "credits"; a run that
// saved nothing must also leave "release_performers" untouched.
var entityTables = []string{"performers", "releases", "release_identifiers", "performer_aliases", "performer_external_ids", "credits",
	"release_performer_sources", "release_tags", "release_tag_sources", "source_records", "series", "slug_redirects", "release_overrides", "performer_overrides", "items"}

// identityTables are the tables whose row count a re-crawl must not grow.
var identityTables = []string{"performers", "releases", "release_identifiers", "performer_aliases", "performer_external_ids", "release_performers",
	"release_performer_sources", "release_tags", "release_tag_sources", "source_records", "source_field_values_current", "series", "slug_redirects", "items"}

type snapshot struct {
	rows     map[string][]string
	lastSeen map[string]time.Time // source name|type|key -> last_seen_at
	history  int64                // all source_field_values rows
}

func (e *env) snap() snapshot {
	e.t.Helper()
	sn := snapshot{rows: map[string][]string{}, lastSeen: map[string]time.Time{}}
	for _, q := range snapshotQueries {
		rows, err := e.s.Query(e.ctx, q.sql)
		if err != nil {
			e.t.Fatalf("snapshot %s: %v", q.name, err)
		}
		out := []string{}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				e.t.Fatal(err)
			}
			out = append(out, v)
		}
		rows.Close()
		sort.Strings(out)
		sn.rows[q.name] = out
	}
	rows, err := e.s.Query(e.ctx, `SELECT s.name||'|'||sr.entity_type||'|'||sr.source_record_key, sr.last_seen_at FROM source_records sr JOIN sources s ON s.id=sr.source_id`)
	if err != nil {
		e.t.Fatal(err)
	}
	for rows.Next() {
		var k string
		var ts time.Time
		if err := rows.Scan(&k, &ts); err != nil {
			e.t.Fatal(err)
		}
		sn.lastSeen[k] = ts
	}
	rows.Close()
	sn.history = e.int64(`SELECT count(*) FROM source_field_values`)
	return sn
}

func (sn snapshot) count(table string) int { return len(sn.rows[table]) }

// counts renders the row counts of tables, for failure messages.
func (sn snapshot) counts(tables []string) string {
	var b strings.Builder
	for _, t := range tables {
		fmt.Fprintf(&b, "%s=%d ", t, sn.count(t))
	}
	return strings.TrimSpace(b.String())
}

// diffRows returns a readable diff of tables between two snapshots ("" when
// equal): per table the count change and the added / removed rows.
func diffRows(before, after snapshot, tables []string) string {
	var b strings.Builder
	for _, t := range tables {
		was, now := before.rows[t], after.rows[t]
		inWas := map[string]int{}
		for _, r := range was {
			inWas[r]++
		}
		var added, removed []string
		for _, r := range now {
			if inWas[r] > 0 {
				inWas[r]--
			} else {
				added = append(added, r)
			}
		}
		for r, n := range inWas {
			for range n {
				removed = append(removed, r)
			}
		}
		if len(added) == 0 && len(removed) == 0 {
			continue
		}
		sort.Strings(added)
		sort.Strings(removed)
		fmt.Fprintf(&b, "\n  %s: %d -> %d rows", t, len(was), len(now))
		for _, r := range removed {
			fmt.Fprintf(&b, "\n    - %s", r)
		}
		for _, r := range added {
			fmt.Fprintf(&b, "\n    + %s", r)
		}
	}
	return b.String()
}

// diffCounts returns the tables whose row counts differ ("" when equal),
// with the added / removed rows of each.
func diffCounts(before, after snapshot, tables []string) string {
	var changed []string
	for _, t := range tables {
		if before.count(t) != after.count(t) {
			changed = append(changed, t)
		}
	}
	return diffRows(before, after, changed)
}

// keptLastSeen reports source records whose last_seen_at went missing or
// moved backwards between two snapshots.
func keptLastSeen(before, after snapshot) string {
	var b strings.Builder
	keys := make([]string, 0, len(before.lastSeen))
	for k := range before.lastSeen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		now, ok := after.lastSeen[k]
		switch {
		case !ok:
			fmt.Fprintf(&b, "\n    %s: source record gone", k)
		case now.Before(before.lastSeen[k]):
			fmt.Fprintf(&b, "\n    %s: last_seen_at %s -> %s", k, before.lastSeen[k].Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		}
	}
	return b.String()
}

// sameLastSeen reports source records whose last_seen_at changed at all.
func sameLastSeen(before, after snapshot) string {
	var b strings.Builder
	for k, was := range before.lastSeen {
		if now := after.lastSeen[k]; !now.Equal(was) {
			fmt.Fprintf(&b, "\n    %s: last_seen_at %s -> %s", k, was.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		}
	}
	return b.String()
}
