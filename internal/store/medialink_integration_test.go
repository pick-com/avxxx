package store_test

// Offline media linking integration test (internal/medialink). Runs only
// under `make test-integration` (AVXXX_TEST_DATABASE_URL). It uses a
// provider name unique to the run, registered in medialink.Providers for
// the duration of the test, so rows from other tests never interfere.

import (
	"avxxx/internal/api"
	"avxxx/internal/media"
	"avxxx/internal/medialink"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// linkSearch is a provider stub: results and errors per query.
type linkSearch struct {
	mu      sync.Mutex
	name    string
	results map[string][]string
	errs    map[string]error
	queries []string
}

func (f *linkSearch) Search(_ context.Context, _, q string, _ int) ([]media.SearchResult, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, q)
	if err := f.errs[q]; err != nil {
		return nil, false, err
	}
	var out []media.SearchResult
	for _, id := range f.results[q] {
		out = append(out, media.SearchResult{ID: id, Source: f.name, Title: id})
	}
	return out, false, nil
}

// linkAdapter is linkSearch as a media.Adapter, for the on-demand path
// through api.New and the media Service.
type linkAdapter struct{ *linkSearch }

func (a linkAdapter) Name() string { return a.name }
func (a linkAdapter) Search(ctx context.Context, q string, page int) ([]media.SearchResult, bool, error) {
	return a.linkSearch.Search(ctx, a.name, q, page)
}
func (a linkAdapter) Detail(context.Context, string) (*media.Detail, error) {
	return nil, fmt.Errorf("not used")
}
func (a linkAdapter) Resolve(context.Context, string) (*media.ResolveResult, error) {
	return nil, fmt.Errorf("not used")
}
func (f *linkSearch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func linkHTTP(t *testing.T, h http.Handler, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if strings.HasPrefix(path, "/api/admin/") {
		req.Header.Set("Authorization", "Bearer link-admin")
		req.Header.Set("X-Admin-User", "link-editor")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestMediaLinkIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	t.Setenv("ADMIN_TOKEN", "link-admin")
	sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	prov := "itest" + sfx
	medialink.Providers[prov] = "https://example.test/"
	t.Cleanup(func() { delete(medialink.Providers, prov) })

	scalar := func(sql string, args ...any) (id int64) {
		t.Helper()
		if err := s.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	code := func(n int) string { return fmt.Sprintf("zl%s-%03d", sfx, n) }
	// Dates in 2999 so these are the newest releases in the shared test DB.
	release := func(n int, status string, withCode bool) int64 {
		t.Helper()
		id := scalar(`INSERT INTO releases(slug,canonical_title,release_date,status) VALUES($1,$2,$3,$4) RETURNING id`,
			fmt.Sprintf("ml-%s-%d", sfx, n), fmt.Sprintf("Link test %d", n), time.Date(2999, 1, n, 0, 0, 0, 0, time.UTC), status)
		if withCode {
			scalar(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'dvd_code',upper($2),$2) RETURNING id`, id, code(n))
		}
		return id
	}
	r1, r2, r3, r4 := release(1, "published", true), release(2, "published", true), release(3, "published", true), release(4, "published", true)
	draft := release(5, "draft", true)
	merged := release(6, "published", true)
	if _, err := s.Exec(ctx, `UPDATE releases SET merged_into_id=$1 WHERE id=$2`, r1, merged); err != nil {
		t.Fatal(err)
	}
	noCode := release(7, "published", false)
	prelinked := release(8, "published", true)
	if _, err := s.Exec(ctx, `INSERT INTO release_media_sources(release_id,source,media_id) VALUES($1,$2,$3)`, prelinked, prov, "video/32/"+code(3)); err != nil {
		t.Fatal(err)
	}
	mine := map[int64]bool{r1: true, r2: true, r3: true, r4: true, draft: true, merged: true, noCode: true, prelinked: true}
	dueMine := func(now time.Time) []int64 {
		t.Helper()
		cs, err := s.DueMediaLinks(ctx, prov, now, 100, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, c := range cs {
			if mine[c.ReleaseID] {
				out = append(out, c.ReleaseID)
			}
		}
		return out
	}

	// ---- selection: published, unmerged, coded, unlinked; newest first.
	if got := dueMine(time.Now()); !slices.Equal(got, []int64{r4, r3, r2, r1}) {
		t.Fatalf("due = %v, want %v", got, []int64{r4, r3, r2, r1})
	}
	if cs, _ := s.DueMediaLinks(ctx, prov, time.Now(), 1, []int64{r4}); len(cs) != 1 || cs[0].ReleaseID != r3 || cs[0].Codes[0] != code(3) || cs[0].Prev != nil {
		t.Fatalf("exclude/limit: %+v", cs)
	}

	// ---- job run: found / ambiguous / not_found / 429 early stop.
	fs := &linkSearch{name: prov,
		results: map[string][]string{
			code(4): {"video/4/" + code(4), "video/44/" + code(4) + "-uncensored"},
			code(3): {"video/31/" + code(3), "video/32/" + code(3)},
		},
		errs: map[string]error{code(1): &media.StatusError{Source: prov, Code: 429}},
	}
	runner := &medialink.Runner{Store: s, Search: fs, Providers: []string{prov},
		Config: medialink.Config{MinInterval: 2 * time.Second, MaxPerRun: 50, DailyCap: 2000},
		Sleep:  func(context.Context, time.Duration) error { return nil }}
	rep, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pr := rep.Providers[0]
	if pr.Checked != 4 || pr.Found != 1 || pr.Ambiguous != 1 || pr.NotFound != 1 || pr.Errors != 1 || pr.Stopped != "rate_limited" {
		t.Fatalf("report %+v", pr)
	}
	var mediaID, mediaURL string
	if err := s.QueryRow(ctx, `SELECT media_id,media_url FROM release_media_sources WHERE release_id=$1 AND source=$2`, r4, prov).Scan(&mediaID, &mediaURL); err != nil {
		t.Fatal(err)
	}
	if mediaID != "video/4/"+code(4) || mediaURL != "https://example.test/video/4/"+code(4) {
		t.Fatalf("link %q %q", mediaID, mediaURL)
	}
	for id, want := range map[int64]string{r4: "found", r3: "ambiguous", r2: "not_found", r1: "error"} {
		a, err := s.MediaLinkAttempt(ctx, id, prov)
		if err != nil || a == nil || a.Status != want {
			t.Fatalf("attempt %d = %+v %v, want %s", id, a, err, want)
		}
		switch want {
		case "found", "ambiguous":
			if a.NextTryAt != nil {
				t.Errorf("%s must never auto-retry", want)
			}
		case "not_found":
			if d := a.NextTryAt.Sub(a.TriedAt); d != medialink.NotFoundRetry {
				t.Errorf("not_found retry after %s", d)
			}
		case "error":
			if d := a.NextTryAt.Sub(a.TriedAt); d != medialink.ErrorRetryBase || !strings.Contains(a.LastError, "429") {
				t.Errorf("error attempt %+v", a)
			}
		}
	}
	if a, _ := s.MediaLinkAttempt(ctx, r3, prov); !slices.Equal(a.Candidates, []string{"video/31/" + code(3), "video/32/" + code(3)}) {
		t.Fatalf("candidates %v", a.Candidates)
	}
	until, err := s.MediaLinkProviderBackoff(ctx, prov)
	if err != nil || !until.After(time.Now()) {
		t.Fatalf("provider backoff %v %v", until, err)
	}
	if n, _ := s.MediaLinkTriedSince(ctx, prov, time.Now().Add(-24*time.Hour)); n != 4 {
		t.Fatalf("tried since = %d, want 4", n)
	}
	before := fs.count()
	if rep, _ = runner.Run(ctx); rep.Providers[0].Stopped != "backoff" || fs.count() != before {
		t.Fatalf("run during backoff %+v", rep.Providers[0])
	}
	if got := dueMine(time.Now()); len(got) != 0 {
		t.Fatalf("nothing is due right after the run, got %v", got)
	}
	if got := dueMine(time.Now().Add(11 * time.Minute)); !slices.Equal(got, []int64{r1}) {
		t.Fatalf("error retry due after 10 min, got %v", got)
	}

	// ---- admin endpoints.
	h := api.New(s, nil)
	status, out := linkHTTP(t, h, "GET", "/api/admin/media-links?provider="+prov, "")
	if status != 200 || len(items(out)) != 4 {
		t.Fatalf("list %d %v", status, out)
	}
	byID := map[int64]map[string]any{}
	for _, it := range items(out) {
		byID[int64(it["release_id"].(float64))] = it
	}
	if it := byID[r4]; it["slug"] != fmt.Sprintf("ml-%s-4", sfx) || it["linked_media_id"] != "video/4/"+code(4) || it["codes"].([]any)[0] != code(4) {
		t.Fatalf("row %v", it)
	}
	status, out = linkHTTP(t, h, "GET", "/api/admin/media-links?provider="+prov+"&status=ambiguous", "")
	if status != 200 || len(items(out)) != 1 || len(items(out)[0]["candidates"].([]any)) != 2 {
		t.Fatalf("ambiguous filter %d %v", status, out)
	}
	seen := 0
	cursor := ""
	for page := 0; page < 5; page++ {
		status, out = linkHTTP(t, h, "GET", "/api/admin/media-links?limit=3&provider="+prov+"&cursor="+cursor, "")
		if status != 200 {
			t.Fatalf("page %d: %d %v", page, status, out)
		}
		seen += len(items(out))
		cursor, _ = out["next_cursor"].(string)
		if cursor == "" {
			break
		}
	}
	if seen != 4 {
		t.Fatalf("paginated %d rows, want 4", seen)
	}
	if status, _ = linkHTTP(t, h, "GET", "/api/admin/media-links?provider=nope", ""); status != 400 {
		t.Fatalf("unknown provider: %d", status)
	}
	if status, _ = linkHTTP(t, h, "GET", "/api/admin/media-links?provider="+prov+"&status=bogus", ""); status != 400 {
		t.Fatalf("unknown status: %d", status)
	}

	// retry: r2 (not_found, 7 days) is due again.
	if status, out = linkHTTP(t, h, "POST", fmt.Sprintf("/api/admin/media-links/%d/retry?provider=%s", r2, prov), ""); status != 200 {
		t.Fatalf("retry %d %v", status, out)
	}
	if got := dueMine(time.Now().Add(time.Second)); !slices.Contains(got, r2) {
		t.Fatalf("retried release not due: %v", got)
	}
	if status, _ = linkHTTP(t, h, "POST", fmt.Sprintf("/api/admin/media-links/%d/retry?provider=%s", noCode, prov), ""); status != 404 {
		t.Fatalf("retry without attempt: %d", status)
	}

	// choose: not a candidate 400, held by another release 409, then 200.
	choose := func(mid string) int {
		st, _ := linkHTTP(t, h, "POST", fmt.Sprintf("/api/admin/media-links/%d/choose", r3), fmt.Sprintf(`{"provider":%q,"media_id":%q}`, prov, mid))
		return st
	}
	if st := choose("video/99/" + code(3)); st != 400 {
		t.Fatalf("choose non-candidate: %d", st)
	}
	if st := choose("video/32/" + code(3)); st != 409 {
		t.Fatalf("choose media held by release %d: %d", prelinked, st)
	}
	if st := choose("video/31/" + code(3)); st != 200 {
		t.Fatalf("choose: %d", st)
	}
	var conf string
	if err := s.QueryRow(ctx, `SELECT media_id,confidence FROM release_media_sources WHERE release_id=$1 AND source=$2`, r3, prov).Scan(&mediaID, &conf); err != nil || mediaID != "video/31/"+code(3) || conf != "admin" {
		t.Fatalf("chosen link %q %q %v", mediaID, conf, err)
	}
	if a, _ := s.MediaLinkAttempt(ctx, r3, prov); a.Status != "found" || a.MatchedMediaID != "video/31/"+code(3) {
		t.Fatalf("attempt after choose %+v", a)
	}
	var actor string
	if err := s.QueryRow(ctx, `SELECT actor FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action='media_link_choose' ORDER BY id DESC LIMIT 1`, r3).Scan(&actor); err != nil || actor != "token:link-editor" {
		t.Fatalf("review event actor %q %v", actor, err)
	}

	// run: enqueues one media_link job (idempotent while it is pending).
	status, out = linkHTTP(t, h, "POST", "/api/admin/media-links/run", "")
	if status != 202 || out["job_id"] == nil {
		t.Fatalf("run %d %v", status, out)
	}
	jobID := int64(out["job_id"].(float64))
	if _, out = linkHTTP(t, h, "POST", "/api/admin/media-links/run", ""); int64(out["job_id"].(float64)) != jobID || out["enqueued"] != false {
		t.Fatalf("second run %v", out)
	}
	if _, err := s.Exec(ctx, `UPDATE jobs SET status='succeeded',finished_at=now() WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}

	// ---- on-demand playback through the API, sharing media_link_attempts.
	r9, r10 := release(9, "published", true), release(10, "published", true)
	od := &linkSearch{name: prov, results: map[string][]string{code(9): {"video/9/" + code(9)}}}
	reg := media.NewRegistry()
	reg.Register(linkAdapter{od})
	mh := media.NewHandler(media.NewService(reg))

	t.Setenv("MEDIA_LINK_ON_DEMAND", "false")
	offline := api.New(s, mh)
	if status, _ = linkHTTP(t, offline, "GET", fmt.Sprintf("/api/releases/ml-%s-9/playback?source=%s", sfx, prov), ""); status != 404 || od.count() != 0 {
		t.Fatalf("offline only: %d, %d searches", status, od.count())
	}
	if a, _ := s.MediaLinkAttempt(ctx, r9, prov); a != nil {
		t.Fatalf("offline-only playback recorded an attempt %+v", a)
	}

	t.Setenv("MEDIA_LINK_ON_DEMAND", "true")
	online := api.New(s, mh)
	// The provider is still backing off after the 429: no on-demand search.
	if status, _ = linkHTTP(t, online, "GET", fmt.Sprintf("/api/releases/ml-%s-9/playback?source=%s", sfx, prov), ""); status != 404 || od.count() != 0 {
		t.Fatalf("on-demand during provider backoff: %d, %d searches", status, od.count())
	}
	if _, err := s.Exec(ctx, `DELETE FROM nightly_runs WHERE job=$1`, medialink.JobKind+"."+prov); err != nil {
		t.Fatal(err)
	}
	status, out = linkHTTP(t, online, "GET", fmt.Sprintf("/api/releases/ml-%s-9/playback?source=%s", sfx, prov), "")
	if status != 200 || out["source_key"] != "video/9/"+code(9) {
		t.Fatalf("on-demand link %d %v", status, out)
	}
	if a, _ := s.MediaLinkAttempt(ctx, r9, prov); a == nil || a.Status != "found" {
		t.Fatalf("on-demand attempt %+v", a)
	}
	for range 2 {
		if status, _ = linkHTTP(t, online, "GET", fmt.Sprintf("/api/releases/ml-%s-10/playback?source=%s", sfx, prov), ""); status != 404 {
			t.Fatalf("miss: %d", status)
		}
	}
	if a, _ := s.MediaLinkAttempt(ctx, r10, prov); a == nil || a.Status != "not_found" || od.count() != 2 {
		t.Fatalf("miss attempt %+v after %d searches (want 2: one link, one miss)", a, od.count())
	}
	// The job sees the on-demand attempts and skips both.
	if got := dueMine(time.Now()); slices.Contains(got, r9) || slices.Contains(got, r10) {
		t.Fatalf("on-demand attempts not honored by selection: %v", got)
	}
}
