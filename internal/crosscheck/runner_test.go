package crosscheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/catalog"
)

const testToken = "crossCheckTOKENx77"

type fakeStore struct {
	mu        sync.Mutex
	src       Source
	found     bool
	due       []Candidate
	attempts  []Attempt
	saved     []catalog.Record
	runs      []audit.Run
	blocked   []string
	policy    catalog.FetchPolicy
	exhausted bool
	tried     int
	usage     int64
}

func (f *fakeStore) SaveCatalogRecord(_ context.Context, _ int64, _ string, rec catalog.Record) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, rec)
	return int64(len(f.saved)), nil
}
func (f *fakeStore) CrossCheckSource(context.Context, string) (Source, bool, error) {
	return f.src, f.found, nil
}
func (f *fakeStore) DueCrossChecks(_ context.Context, _ int64, _ time.Time, limit int, exclude []int64) ([]Candidate, error) {
	skip := map[int64]bool{}
	for _, id := range exclude {
		skip[id] = true
	}
	var out []Candidate
	for _, c := range f.due {
		if !skip[c.ReleaseID] && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeStore) CrossCheckTriedSince(context.Context, int64, time.Time) (int, error) {
	return f.tried, nil
}
func (f *fakeStore) RecordCrossCheckAttempt(_ context.Context, a Attempt) error {
	f.attempts = append(f.attempts, a)
	return nil
}
func (f *fakeStore) CrossCheckFetchPolicy(context.Context, int64) (catalog.FetchPolicy, bool, error) {
	return f.policy, f.exhausted, nil
}
func (f *fakeStore) RecordCrossCheckUsage(_ context.Context, _ int64, pages, _ int64) error {
	f.usage += pages
	return nil
}
func (f *fakeStore) BlockSource(_ context.Context, id int64, d time.Duration, reason string) error {
	f.blocked = append(f.blocked, fmt.Sprintf("%d:%s:%s", id, d, reason))
	return nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func scene(id, code string) string {
	return fmt.Sprintf(`{"id":%q,"title":"%s title","sku":%q,"date":"2024-04-23","site":{"name":"S1 NO.1 STYLE","id":900010}}`, id, code, code)
}

// fakeAPI answers GET /jav?q=<code> from bodies keyed by the upper-case code.
type fakeAPI struct {
	*httptest.Server
	mu       sync.Mutex
	bodies   map[string]string
	status   int
	requests []*http.Request
}

func newFakeAPI(t *testing.T) *fakeAPI {
	a := &fakeAPI{bodies: map[string]string{}}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		a.requests = append(a.requests, r.Clone(context.Background()))
		status, body := a.status, a.bodies[r.URL.Query().Get("q")]
		a.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if status != 0 {
			w.Header().Set("Retry-After", "600")
			http.Error(w, "nope "+r.Header.Get("Authorization"), status)
			return
		}
		if body == "" {
			body = `{"data":[],"meta":{"total":0,"per_page":10,"current_page":1,"last":1}}`
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *fakeAPI) host() string { u, _ := url.Parse(a.URL); return u.Host }

func list(scenes ...string) string {
	return `{"data":[` + strings.Join(scenes, ",") + `],"meta":{"total":1,"per_page":10,"current_page":1,"last":1}}`
}

func reviewedSource() Source {
	now := time.Now()
	return Source{ID: 41, Name: "ThePornDB JAV", Enabled: true,
		Governance: catalog.SourceGovernance{Adapter: "theporndb", LicenseStatus: catalog.LicenseOfficialAPI, TermsURL: catalog.ThePornDBDocsURL, TermsReviewedAt: &now}}
}

func newRunner(st *fakeStore, api *fakeAPI, token string) *Runner {
	return &Runner{Store: st, Adapters: []catalog.LookupAdapter{catalog.NewThePornDBAdapterWith(token, api.host())},
		HTTPClient: api.Client(), UserAgent: "avxxx-test (+ops@example.test)", Config: Config{MaxPerRun: 10, DailyCap: 100, BatchSize: 2}}
}

func TestRunnerOutcomes(t *testing.T) {
	api := newFakeAPI(t)
	api.bodies["SONE-156"] = list(scene("5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01", "SONE-156"), scene("9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d", "SONE-1560"))
	api.bodies["ABP-123"] = list(scene("11111111-2222-4333-8444-555555555555", "ABP-123"), scene("66666666-7777-4888-9999-000000000000", "ABP-123"))
	api.bodies["MIDV-001"] = list(scene("aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "MIDV-001"))
	api.bodies["JUQ-743"] = list(scene("bbbbbbbb-cccc-4ddd-8eee-ffffffffffff", "JUQ-744"))
	prevMiss := &Attempt{Status: StatusNotFound, Attempts: 2}
	st := &fakeStore{src: reviewedSource(), found: true, due: []Candidate{
		{ReleaseID: 1, Slug: "sone-156", Codes: []string{"SONE-156"}, Reasons: []string{"missing_maker"}},
		{ReleaseID: 2, Slug: "abp-123", Codes: []string{"ABP-123"}},
		{ReleaseID: 3, Slug: "midv-001-2", Codes: []string{"MIDV-001"}},
		{ReleaseID: 4, Slug: "juq-743", Codes: []string{"JUQ-743"}, Prev: prevMiss},
	}}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	r := newRunner(st, api, testToken)
	r.Now = func() time.Time { return now }
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ar := rep.Adapters[0]
	if ar.Checked != 4 || ar.Found != 1 || ar.Ambiguous != 2 || ar.NotFound != 1 || ar.Requests != 4 || ar.Stopped != "" {
		t.Fatalf("report %+v", ar)
	}
	if len(st.saved) != 1 || st.saved[0].Fields["dvd_code"] != "SONE-156" || st.saved[0].SourceKey != "tpdb-jav-5b0d3a6e-4f1c-4c39-9a51-6f0a1f2e8c01" {
		t.Fatalf("saved %+v", st.saved)
	}
	byRelease := map[int64]Attempt{}
	for _, a := range st.attempts {
		byRelease[a.ReleaseID] = a
	}
	if a := byRelease[1]; a.Status != StatusFound || a.NextTryAt != nil || a.MatchedKey == "" || a.SourceID != 41 || len(a.Reasons) != 1 {
		t.Fatalf("found attempt %+v", a)
	}
	if a := byRelease[2]; a.Status != StatusAmbiguous || len(a.Candidates) != 2 || a.NextTryAt != nil {
		t.Fatalf("ambiguous attempt %+v", a)
	}
	if a := byRelease[3]; a.Status != StatusAmbiguous || !strings.Contains(a.LastError, "midv-001-2") {
		t.Fatalf("slug mismatch attempt %+v", a)
	}
	if a := byRelease[4]; a.Status != StatusNotFound || a.Attempts != 3 || !a.NextTryAt.Equal(now.Add(NotFoundSlowRetry)) || len(a.Candidates) != 1 {
		t.Fatalf("not found attempt %+v", a)
	}
	if len(st.runs) != 1 || st.runs[0].Job != "crosscheck.theporndb" || st.runs[0].Result != audit.ResultIssues || st.runs[0].Fixed != 1 {
		t.Fatalf("runs %+v", st.runs)
	}
	if st.usage != 4 {
		t.Fatalf("usage %d", st.usage)
	}
	for _, req := range api.requests {
		if req.URL.Query().Get("q") == "" || strings.Contains(req.URL.String(), testToken) {
			t.Fatalf("request %s", req.URL)
		}
	}
}

func TestRunnerGates(t *testing.T) {
	api := newFakeAPI(t)
	due := []Candidate{{ReleaseID: 1, Slug: "sone-156", Codes: []string{"SONE-156"}}}
	future := time.Now().Add(time.Hour)
	unreviewed := reviewedSource()
	unreviewed.Governance.TermsReviewedAt = nil
	disabled := reviewedSource()
	disabled.Enabled = false
	blocked := reviewedSource()
	blocked.BlockedUntil = &future
	for _, tc := range []struct {
		name, want string
		st         *fakeStore
		token, ua  string
		result     string
	}{
		{"no source", "no_source", &fakeStore{}, testToken, "ua (+x@y.z)", audit.ResultOK},
		{"disabled", "disabled", &fakeStore{src: disabled, found: true, due: due}, testToken, "ua (+x@y.z)", audit.ResultOK},
		{"blocked", "blocked", &fakeStore{src: blocked, found: true, due: due}, testToken, "ua (+x@y.z)", audit.ResultOK},
		{"license gate", "license_gate", &fakeStore{src: unreviewed, found: true, due: due}, testToken, "ua (+x@y.z)", audit.ResultIssues},
		{"no contact", "config", &fakeStore{src: reviewedSource(), found: true, due: due}, testToken, "", audit.ResultIssues},
		{"no token", "not_configured", &fakeStore{src: reviewedSource(), found: true, due: due}, "", "ua (+x@y.z)", audit.ResultIssues},
		{"budget", "budget", &fakeStore{src: reviewedSource(), found: true, due: due, exhausted: true}, testToken, "ua (+x@y.z)", audit.ResultOK},
		{"daily cap", "daily_cap", &fakeStore{src: reviewedSource(), found: true, due: due, tried: 100}, testToken, "ua (+x@y.z)", audit.ResultOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(tc.st, api, tc.token)
			r.UserAgent = tc.ua
			rep, err := r.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := rep.Adapters[0].Stopped; got != tc.want {
				t.Fatalf("stopped = %q, want %q (%+v)", got, tc.want, rep.Adapters[0])
			}
			if len(tc.st.attempts) != 0 || len(tc.st.saved) != 0 || len(tc.st.runs) != 1 || tc.st.runs[0].Result != tc.result {
				t.Fatalf("attempts %v saved %v runs %+v", tc.st.attempts, tc.st.saved, tc.st.runs)
			}
		})
	}
	if len(api.requests) != 0 {
		t.Fatalf("gated runs made %d requests", len(api.requests))
	}
}

func TestRunnerRateLimitAndErrors(t *testing.T) {
	api := newFakeAPI(t)
	api.status = http.StatusTooManyRequests
	due := []Candidate{{ReleaseID: 1, Slug: "a-1", Codes: []string{"A-1"}}, {ReleaseID: 2, Slug: "a-2", Codes: []string{"A-2"}}}
	st := &fakeStore{src: reviewedSource(), found: true, due: due}
	rep, err := newRunner(st, api, testToken).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar := rep.Adapters[0]; ar.Stopped != "rate_limited" || ar.Checked != 1 || ar.Errors != 1 {
		t.Fatalf("report %+v", ar)
	}
	if len(st.blocked) != 1 || !strings.Contains(st.blocked[0], "41:10m0s:rate_limited") {
		t.Fatalf("blocked %v", st.blocked)
	}
	if a := st.attempts[0]; a.Status != StatusError || a.NextTryAt == nil || !strings.Contains(a.LastError, "rate_limited") {
		t.Fatalf("attempt %+v", a)
	}

	api.status = http.StatusInternalServerError
	due = append(due, Candidate{ReleaseID: 3, Slug: "a-3", Codes: []string{"A-3"}}, Candidate{ReleaseID: 4, Slug: "a-4", Codes: []string{"A-4"}})
	st = &fakeStore{src: reviewedSource(), found: true, due: due}
	rep, err = newRunner(st, api, testToken).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar := rep.Adapters[0]; ar.Stopped != "error_streak" || ar.Checked != MaxErrorStreak || st.runs[0].Result != audit.ResultIssues {
		t.Fatalf("report %+v", ar)
	}
	if a := st.attempts[0]; a.Attempts != 1 || !a.NextTryAt.After(a.TriedAt.Add(ErrorRetryBase-time.Second)) {
		t.Fatalf("error backoff %+v", a)
	}
	// Nothing recorded carries the token, which the fake API echoes.
	b, _ := json.Marshal([]any{st.attempts, st.runs, st.blocked})
	if strings.Contains(string(b), testToken) {
		t.Fatalf("token leaked: %s", b)
	}
}

func TestRunnerBudgetStopsWithoutAttempt(t *testing.T) {
	api := newFakeAPI(t)
	due := []Candidate{{ReleaseID: 1, Slug: "a-1", Codes: []string{"A-1"}}, {ReleaseID: 2, Slug: "a-2", Codes: []string{"A-2"}}}
	st := &fakeStore{src: reviewedSource(), found: true, due: due, policy: catalog.FetchPolicy{MaxPages: 1}}
	rep, err := newRunner(st, api, testToken).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ar := rep.Adapters[0]; ar.Stopped != "budget" || ar.Checked != 1 || len(st.attempts) != 1 || st.usage != 1 {
		t.Fatalf("report %+v attempts %v", ar, st.attempts)
	}
}

func TestNextTryAndConfig(t *testing.T) {
	now := time.Now()
	if NextTry(StatusFound, 1, now) != nil || NextTry(StatusAmbiguous, 1, now) != nil {
		t.Fatal("found / ambiguous retried automatically")
	}
	if got := NextTry(StatusError, 3, now).Sub(now); got != 2*time.Hour {
		t.Fatalf("error streak 3 = %s", got)
	}
	if got := NextTry(StatusError, 40, now).Sub(now); got != ErrorRetryMax {
		t.Fatalf("error cap = %s", got)
	}
	if Budget(100, 1000, 950) != 50 || Budget(100, 10, 20) != 0 {
		t.Fatal("budget")
	}
	env := map[string]string{"CROSSCHECK_MAX_PER_RUN": "7", "CROSSCHECK_DAILY_CAP": "-1"}
	c, warn := ConfigFromEnv(func(k string) string { return env[k] })
	if c.MaxPerRun != 7 || c.DailyCap != DefaultDailyCap || len(warn) != 1 {
		t.Fatalf("config %+v %v", c, warn)
	}
}
