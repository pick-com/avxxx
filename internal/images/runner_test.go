package images

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"avxxx/internal/objstore"
)

// ---- fetch

type fakeFetchStore struct {
	mu         sync.Mutex
	sources    []SourcePolicy
	candidates []Candidate
	keys       map[string]bool
	saved      []StoredImage
	attempts   map[string]string // entity key -> status
	asked      [][]int64         // sourceIDs per ImageCandidates call
	runs       []audit.Run
}

func ekey(c Candidate) string { return fmt.Sprintf("%s/%d/%s", c.EntityType, c.EntityID, c.AssetType) }

func (f *fakeFetchStore) ImageSources(context.Context) ([]SourcePolicy, error) { return f.sources, nil }

func (f *fakeFetchStore) ImageCandidates(_ context.Context, ids []int64, _ time.Time, limit int) ([]Candidate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, append([]int64(nil), ids...))
	allowed := map[int64]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	var out []Candidate
	for _, c := range f.candidates {
		if _, done := f.attempts[ekey(c)]; done || !allowed[c.SourceID] {
			continue
		}
		if out = append(out, c); len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeFetchStore) ObjectKeyStored(_ context.Context, key string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keys[key], nil
}

func (f *fakeFetchStore) SaveImage(_ context.Context, img StoredImage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, img)
	for _, k := range img.Keys {
		f.keys[k] = true
	}
	f.attempts[ekey(img.Candidate)] = "stored"
	return nil
}

func (f *fakeFetchStore) RecordFetchAttempt(_ context.Context, c Candidate, _, status, errText string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[ekey(c)] = status + ": " + errText
	return nil
}

func (f *fakeFetchStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

type imageSite struct {
	*httptest.Server
	mu   sync.Mutex
	hits map[string]int
}

func newImageSite(t *testing.T, files map[string][]byte, types map[string]string) *imageSite {
	s := &imageSite{hits: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hits[r.URL.Path]++
		s.mu.Unlock()
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /private/\n")
			return
		}
		if r.URL.Path == "/blocked.jpg" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", types[r.URL.Path])
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *imageSite) count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

func newRunner(st FetchStore, objects objstore.Storage, site *imageSite) *FetchRunner {
	return &FetchRunner{Store: st, Objects: objects, UserAgent: catalog.UserAgent("ops@example.com"), Client: site.Client(), AllowPrivateHosts: true,
		NewFetcher: func(SourcePolicy) *catalog.Fetcher {
			f := catalog.NewFetcher(site.Client(), catalog.FetchPolicy{MaxPages: 100})
			f.UserAgent, f.Robots, f.Automatic = catalog.UserAgent("ops@example.com"), catalog.NewRobots(site.Client(), "ua"), true
			return f
		}}
}

func cand(et string, id, src int64, url string) Candidate {
	c := Candidate{EntityType: et, EntityID: id, AssetType: StoredAssetTypes[et], SourceID: src, SourceRecordID: id * 10}
	if et == EntityRelease {
		c.Evidence = map[string]any{"cover_url": url}
	} else {
		c.Evidence = map[string]any{"image_urls": map[string]any{"large": url}}
	}
	return c
}

func TestFetchRunnerStoresOnlyFromPermittedSources(t *testing.T) {
	cover := jpegBytes(t, 800, 538)
	site := newImageSite(t, map[string][]byte{"/c1.jpg": cover, "/p1.jpg": jpegBytes(t, 400, 600), "/page.html": []byte("<html></html>"),
		"/private/x.jpg": cover, "/denied.jpg": cover},
		map[string]string{"/c1.jpg": "image/jpeg", "/p1.jpg": "image/jpeg", "/page.html": "text/html", "/private/x.jpg": "image/jpeg", "/denied.jpg": "image/jpeg"})
	allowed := SourcePolicy{ID: 1, Adapter: "dmm", LicenseStatus: catalog.LicenseOfficialAPI, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	metadataOnly := SourcePolicy{ID: 2, Adapter: "javdatabase", LicenseStatus: catalog.LicenseTermsConfirmed, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	flagOff := SourcePolicy{ID: 3, Adapter: "dmm", LicenseStatus: catalog.LicenseLicensed, TermsReviewedAt: reviewed()}
	st := &fakeFetchStore{sources: []SourcePolicy{allowed, metadataOnly, flagOff}, keys: map[string]bool{}, attempts: map[string]string{},
		candidates: []Candidate{
			cand(EntityRelease, 1, 1, site.URL+"/c1.jpg"),
			cand(EntityPerformer, 5, 1, site.URL+"/p1.jpg"),
			cand(EntityRelease, 2, 1, site.URL+"/page.html"),
			cand(EntityRelease, 3, 1, site.URL+"/missing.jpg"),
			cand(EntityRelease, 4, 1, site.URL+"/private/x.jpg"),
			cand(EntityRelease, 6, 2, site.URL+"/denied.jpg"),
			cand(EntityRelease, 7, 3, site.URL+"/denied.jpg"),
		}}
	mem := objstore.NewMemory()
	rep, err := newRunner(st, mem, site).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(rep.Allowed) != "[1]" || len(rep.Skipped) != 1 || rep.Skipped[0].SourceID != 2 {
		t.Errorf("allowed=%v skipped=%+v", rep.Allowed, rep.Skipped)
	}
	for _, ids := range st.asked {
		if fmt.Sprint(ids) != "[1]" {
			t.Errorf("candidates were requested for sources %v", ids)
		}
	}
	if site.count("/denied.jpg") != 0 {
		t.Error("an image was downloaded from a source without image permission")
	}
	if site.count("/private/x.jpg") != 0 {
		t.Error("robots.txt disallow was not honoured")
	}
	if rep.Stored != 2 || rep.Rejected != 1 || rep.Failed != 2 {
		t.Errorf("report = %+v", rep)
	}
	if !strings.HasPrefix(st.attempts["release/2/cover"], "failed: image rejected: content type") ||
		!strings.Contains(st.attempts["release/3/cover"], "HTTP 404") || !strings.HasPrefix(st.attempts["release/4/cover"], "skipped: robots.txt") {
		t.Errorf("attempts = %v", st.attempts)
	}
	sha := SHA256Hex(cover)
	want := []string{"release/1/cover/" + sha + ".jpg", "release/1/cover/" + sha + ".w320.jpg", "release/1/cover/" + sha + ".w720.jpg"}
	keys := mem.Keys()
	for _, k := range want {
		if !contains(keys, k) {
			t.Errorf("missing object %s in %v", k, keys)
		}
	}
	// Performer 400x600: original + w320 only.
	if n := countPrefix(keys, "performer/5/avatar/"); n != 2 {
		t.Errorf("performer objects = %d (%v)", n, keys)
	}
	if len(st.runs) != 1 || st.runs[0].Job != JobKindFetch || st.runs[0].Result != audit.ResultIssues {
		t.Errorf("runs = %+v", st.runs)
	}
}

func TestFetchRunnerDedupe(t *testing.T) {
	cover := jpegBytes(t, 800, 538)
	site := newImageSite(t, map[string][]byte{"/c.jpg": cover}, map[string]string{"/c.jpg": "image/jpeg"})
	src := SourcePolicy{ID: 1, Adapter: "dmm", LicenseStatus: catalog.LicenseLicensed, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	st := &fakeFetchStore{sources: []SourcePolicy{src}, keys: map[string]bool{}, attempts: map[string]string{},
		candidates: []Candidate{cand(EntityRelease, 1, 1, site.URL+"/c.jpg")}}
	mem := objstore.NewMemory()
	if _, err := newRunner(st, mem, site).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	puts := mem.Puts
	// The same image found again for the same entity (e.g. after its rows
	// were re-created): every object key is already owned, nothing is
	// uploaded again.
	delete(st.attempts, "release/1/cover")
	rep, err := newRunner(st, mem, site).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mem.Puts != puts || rep.Deduped != 3 || rep.Objects != 0 || rep.Stored != 1 {
		t.Errorf("second run: puts %d -> %d, report %+v", puts, mem.Puts, rep)
	}
}

func TestFetchRunnerStopsBlockedSource(t *testing.T) {
	site := newImageSite(t, map[string][]byte{"/ok.jpg": jpegBytes(t, 300, 300)}, map[string]string{"/ok.jpg": "image/jpeg"})
	src := SourcePolicy{ID: 1, Adapter: "dmm", LicenseStatus: catalog.LicenseOfficialAPI, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	st := &fakeFetchStore{sources: []SourcePolicy{src}, keys: map[string]bool{}, attempts: map[string]string{},
		candidates: []Candidate{cand(EntityRelease, 1, 1, site.URL+"/blocked.jpg"), cand(EntityRelease, 2, 1, site.URL+"/ok.jpg")}}
	rep, err := newRunner(st, objstore.NewMemory(), site).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Blocked) != 1 || rep.Stored != 0 || site.count("/ok.jpg") != 0 {
		t.Errorf("a 429 must stop the source for the run: %+v ok-hits=%d", rep, site.count("/ok.jpg"))
	}
}

func TestFetchRunnerNeedsStorageAndContact(t *testing.T) {
	st := &fakeFetchStore{}
	rep, err := (&FetchRunner{Store: st, UserAgent: "ua (+ops@example.com)"}).Run(context.Background())
	if err != nil || rep.Stopped != "storage_not_configured" {
		t.Errorf("no storage: %+v %v", rep, err)
	}
	rep, err = (&FetchRunner{Store: st, Objects: objstore.NewMemory()}).Run(context.Background())
	if err != nil || rep.Stopped != "user_agent_not_configured" {
		t.Errorf("no contact: %+v %v", rep, err)
	}
	// A source pointing at an internal host is refused without a request.
	site := newImageSite(t, map[string][]byte{"/c.jpg": jpegBytes(t, 300, 300)}, map[string]string{"/c.jpg": "image/jpeg"})
	src := SourcePolicy{ID: 1, Adapter: "dmm", LicenseStatus: catalog.LicenseLicensed, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	st = &fakeFetchStore{sources: []SourcePolicy{src}, keys: map[string]bool{}, attempts: map[string]string{},
		candidates: []Candidate{cand(EntityRelease, 1, 1, site.URL+"/c.jpg")}}
	r := newRunner(st, objstore.NewMemory(), site)
	r.AllowPrivateHosts = false
	if rep, _ = r.Run(context.Background()); rep.Rejected != 1 || site.count("/c.jpg") != 0 {
		t.Errorf("internal host: %+v hits=%d", rep, site.count("/c.jpg"))
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func countPrefix(xs []string, p string) int {
	n := 0
	for _, v := range xs {
		if strings.HasPrefix(v, p) {
			n++
		}
	}
	return n
}

// ---- purge

type fakePurgeStore struct {
	assets  []Asset
	expired map[int64]bool
	deleted []int64
	state   map[string]string
	runs    []audit.Run
}

func (f *fakePurgeStore) MarkRetention(context.Context, time.Time, time.Duration) (int64, int64, error) {
	return int64(len(f.expired)), 0, nil
}

func (f *fakePurgeStore) ExpiredAssets(_ context.Context, _ time.Time, limit int) ([]Asset, error) {
	var out []Asset
	for _, a := range f.assets {
		if f.expired[a.ID] && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakePurgeStore) DeleteAssets(_ context.Context, ids []int64) error {
	f.deleted = append(f.deleted, ids...)
	keep := f.assets[:0]
	gone := map[int64]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	for _, a := range f.assets {
		if !gone[a.ID] {
			keep = append(keep, a)
		}
	}
	f.assets = keep
	return nil
}

func (f *fakePurgeStore) KnownObjectKeys(_ context.Context, keys []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, a := range f.assets {
		out[a.ObjectKey] = true
	}
	return out, nil
}

func (f *fakePurgeStore) StorageState(_ context.Context, n string) (string, error) {
	return f.state[n], nil
}
func (f *fakePurgeStore) SetStorageState(_ context.Context, n, v string) error {
	f.state[n] = v
	return nil
}
func (f *fakePurgeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func TestPurgeRunner(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	mem := objstore.NewMemory()
	mem.Now = func() time.Time { return now.Add(-48 * time.Hour) }
	ctx := context.Background()
	put := func(k string) { mem.Put(ctx, k, []byte(k), "image/jpeg") }
	for _, k := range []string{"release/1/cover/a.jpg", "release/1/cover/a.w320.jpg", "release/2/cover/b.jpg", "release/9/cover/orphan.jpg", "backups/db.sql"} {
		put(k)
	}
	mem.Now = func() time.Time { return now.Add(-time.Hour) }
	put("release/3/cover/fresh-orphan.jpg") // upload still in flight: grace period
	st := &fakePurgeStore{state: map[string]string{}, expired: map[int64]bool{1: true, 2: true},
		assets: []Asset{{ID: 2, ObjectKey: "release/1/cover/a.w320.jpg"}, {ID: 1, ObjectKey: "release/1/cover/a.jpg"}, {ID: 3, ObjectKey: "release/2/cover/b.jpg"}}}
	rep, err := (&PurgeRunner{Store: st, Objects: mem, Now: func() time.Time { return now }, Config: PurgeConfig{OrphanPageSize: 2}}).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := mem.Keys()
	sort.Strings(got)
	want := []string{"backups/db.sql", "release/2/cover/b.jpg", "release/3/cover/fresh-orphan.jpg"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("objects after purge = %v, want %v", got, want)
	}
	if fmt.Sprint(st.deleted) != "[2 1]" || rep.DeletedRows != 2 || rep.Orphans != 1 || !rep.ScanComplete || st.state[orphanCursor] != "" {
		t.Errorf("report %+v deleted %v state %v", rep, st.deleted, st.state)
	}
	if len(st.runs) != 1 || st.runs[0].Job != JobKindPurge || st.runs[0].Result != audit.ResultOK {
		t.Errorf("runs = %+v", st.runs)
	}
	// A bounded scan keeps its cursor for the next run.
	rep, err = (&PurgeRunner{Store: st, Objects: mem, Now: func() time.Time { return now }, Config: PurgeConfig{OrphanScanMax: 1, OrphanPageSize: 1}}).Run(ctx)
	if err != nil || rep.ScanComplete || st.state[orphanCursor] != "backups/db.sql" {
		t.Errorf("bounded scan: %+v %v cursor=%q", rep, err, st.state[orphanCursor])
	}
}

func TestPurgeRunnerWithoutStorageOnlyMarks(t *testing.T) {
	st := &fakePurgeStore{state: map[string]string{}, expired: map[int64]bool{1: true}, assets: []Asset{{ID: 1, ObjectKey: "release/1/cover/a.jpg"}}}
	rep, err := (&PurgeRunner{Store: st}).Run(context.Background())
	if err != nil || rep.Stopped != "storage_not_configured" || len(st.deleted) != 0 || rep.Marked != 1 {
		t.Errorf("%+v %v deleted=%v", rep, err, st.deleted)
	}
}
