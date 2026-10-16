package store_test

// Catalog image storage end to end (internal/images, migrations/031):
// candidate selection by source permission, the fetch job against a fake
// image site and a signature-checking fake S3, the public cover/portrait
// fields and /api/images/ proxy, admin suppress/restore with review_events,
// and the purge job. Runs only under `make test-integration`.

import (
	"avxxx/internal/api"
	"avxxx/internal/catalog"
	"avxxx/internal/images"
	"avxxx/internal/objstore"
	"avxxx/internal/objstore/objstoretest"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func itestJPEG(t *testing.T, w, h int, shade uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{shade, uint8(x), uint8(y), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageStorageIntegration(t *testing.T) {
	s, ctx := openRecsDB(t)
	sfx := fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
	scalar := func(sql string, args ...any) (id int64) {
		t.Helper()
		if err := s.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	// ---- image site
	files := map[string][]byte{"/r1.jpg": itestJPEG(t, 800, 538, 10), "/p1.jpg": itestJPEG(t, 400, 600, 20), "/any.jpg": itestJPEG(t, 300, 300, 30)}
	hits := map[string]int{}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits[r.URL.Path]++
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nAllow: /\n")
			return
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(b)
	}))
	defer site.Close()

	// ---- sources: A may store images; B's license covers metadata only.
	srcA := scalar(`INSERT INTO sources(name,url,adapter,source_kind,license_status,terms_url,terms_reviewed_at,terms_reviewed_by)
		VALUES('img-a',$1,'dmm','official_catalog','official_api','https://terms.test/a',now(),'legal') RETURNING id`, "https://img-a.test/"+sfx)
	srcB := scalar(`INSERT INTO sources(name,url,adapter,license_status,terms_url,terms_reviewed_at) VALUES('img-b',$1,'javdatabase','terms_confirmed','https://terms.test/b',now()) RETURNING id`,
		"https://img-b.test/"+sfx)
	if _, err := s.SetSourceImages(ctx, srcB, true, "metadata terms", "ops"); err == nil || !strings.Contains(err.Error(), "does not cover images") {
		t.Fatalf("enabling images on a terms_confirmed source: %v", err)
	}
	if _, err := s.SetSourceImages(ctx, srcA, true, "", "ops"); err == nil {
		t.Fatal("enabling without a note must fail")
	}
	pol, err := s.SetSourceImages(ctx, srcA, true, "API guide 4.2 permits cover display", "ops")
	if err != nil || !pol.Decision.Allowed || pol.ImagesReviewedBy != "ops" {
		t.Fatalf("SetSourceImages = %+v %v", pol, err)
	}
	if n := scalar(`SELECT count(*) FROM review_events WHERE entity_type='source' AND entity_id=$1 AND action='images_review' AND actor='ops'`, srcA); n != 1 {
		t.Fatalf("images_review events = %d", n)
	}
	// Force B's flag on directly: the license still keeps it out.
	exec(`UPDATE sources SET images_allowed=true,images_reviewed_at=now() WHERE id=$1`, srcB)

	release := func(n int, status string) (int64, string) {
		slug := fmt.Sprintf("img-%s-%d", sfx, n)
		return scalar(`INSERT INTO releases(slug,canonical_title,status) VALUES($1,$2,$3) RETURNING id`, slug, "Image test "+slug, status), slug
	}
	performer := func(n int) (int64, string) {
		slug := fmt.Sprintf("imgp-%s-%d", sfx, n)
		return scalar(`INSERT INTO performers(slug,canonical_name) VALUES($1,$2) RETURNING id`, slug, "Img Performer "+slug), slug
	}
	record := func(src int64, et string, id int64, raw string) {
		col := et + "_id"
		exec(`INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,`+col+`,raw_data,parser_version) VALUES($1,$2,$3,$4,$5,$6::jsonb,'t')`,
			src, et, fmt.Sprintf("%s-%d-%d", et, id, src), site.URL, id, raw)
	}
	r1, r1slug := release(1, "published")
	r2, _ := release(2, "published")
	r3, _ := release(3, "draft")
	p1, p1slug := performer(1)
	p2, _ := performer(2)
	record(srcA, "release", r1, fmt.Sprintf(`{"cover_url":%q}`, site.URL+"/r1.jpg"))
	record(srcB, "release", r2, fmt.Sprintf(`{"cover_url":%q}`, site.URL+"/any.jpg"))
	record(srcA, "release", r3, fmt.Sprintf(`{"cover_url":%q}`, site.URL+"/any.jpg"))
	record(srcA, "performer", p1, fmt.Sprintf(`{"image_urls":{"large":%q}}`, site.URL+"/p1.jpg"))
	record(srcA, "performer", p2, fmt.Sprintf(`{"image_urls":{"large":%q}}`, site.URL+"/any.jpg"))
	exec(`INSERT INTO performer_requests(performer_id,kind,requester_contact,status,due_at,resolved_at,resolved_by) VALUES($1,'remove_photo','agency@test','approved',now(),now(),'ops')`, p2)

	mine := map[string]bool{fmt.Sprint("release", r1): true, fmt.Sprint("release", r2): true, fmt.Sprint("release", r3): true,
		fmt.Sprint("performer", p1): true, fmt.Sprint("performer", p2): true}
	candidates := func() []string {
		t.Helper()
		cs, err := s.ImageCandidates(ctx, []int64{srcA}, time.Now(), 500)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range cs {
			if k := fmt.Sprint(c.EntityType, c.EntityID); mine[k] {
				out = append(out, k)
			}
		}
		return out
	}
	if got := fmt.Sprint(candidates()); got != fmt.Sprint([]string{fmt.Sprint("release", r1), fmt.Sprint("performer", p1)}) {
		t.Fatalf("candidates = %v (want r1 then p1: B is not image-permitted, r3 is a draft, p2 has an approved remove_photo)", got)
	}

	// ---- fetch through the signed S3 client.
	fake := objstoretest.New("itest-key", "itest-secret", "us-east-1")
	defer fake.Close()
	objects := objstore.NewS3(fake.Config("media"), nil)
	if err := objects.EnsureBucket(ctx); err != nil {
		t.Fatal(err)
	}
	ua := catalog.UserAgent("ops@example.com")
	runner := &images.FetchRunner{Store: s, Objects: objects, UserAgent: ua, Client: site.Client(), AllowPrivateHosts: true,
		NewFetcher: func(images.SourcePolicy) *catalog.Fetcher {
			f := catalog.NewFetcher(site.Client(), catalog.FetchPolicy{MaxPages: 50})
			f.UserAgent, f.Robots, f.Automatic = ua, catalog.NewRobots(site.Client(), ua), true
			return f
		}}
	rep, err := runner.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Stored < 2 || hits["/any.jpg"] != 0 {
		t.Fatalf("fetch report %+v, /any.jpg hits %d", rep, hits["/any.jpg"])
	}
	if got := candidates(); len(got) != 0 {
		t.Fatalf("stored entities are still candidates: %v", got)
	}
	var rows int64
	var origKey string
	if err := s.QueryRow(ctx, `SELECT count(*), max(object_key) FILTER (WHERE variant='original') FROM media_assets WHERE entity_type='release' AND entity_id=$1`, r1).Scan(&rows, &origKey); err != nil || rows != 3 {
		t.Fatalf("r1 rows = %d %v", rows, err)
	}
	if !strings.HasPrefix(origKey, fmt.Sprintf("release/%d/cover/", r1)) || len(fake.Store.Keys()) < 5 {
		t.Fatalf("object key %s, objects %v", origKey, fake.Store.Keys())
	}

	// ---- public API: cover / portrait fields and the proxy.
	t.Setenv("ADMIN_TOKEN", "img-admin")
	t.Setenv("S3_ENDPOINT", fake.URL)
	t.Setenv("S3_BUCKET", "media")
	t.Setenv("S3_ACCESS_KEY", "itest-key")
	t.Setenv("S3_SECRET_KEY", "itest-secret")
	t.Setenv("S3_PUBLIC_BASE_URL", "")
	t.Setenv("IMAGE_URL_MODE", "")
	h := api.New(s, nil)
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if strings.HasPrefix(path, "/api/admin/") {
			req.Header.Set("Authorization", "Bearer img-admin")
			req.Header.Set("X-Admin-User", "takedown-desk")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, b
	}
	type img struct {
		URL    string `json:"url"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Srcset []struct {
			Width int `json:"width"`
		} `json:"srcset"`
	}
	var rel struct {
		Cover *img `json:"cover"`
	}
	code, body := call("GET", "/api/releases/"+r1slug, "")
	if code != 200 || json.Unmarshal(body, &rel) != nil || rel.Cover == nil || rel.Cover.Width != 720 || rel.Cover.Height != 484 || len(rel.Cover.Srcset) != 3 {
		t.Fatalf("release detail %d %s", code, body)
	}
	if !strings.HasPrefix(rel.Cover.URL, "/api/images/release/") {
		t.Fatalf("cover url %s", rel.Cover.URL)
	}
	code, body = call("GET", rel.Cover.URL, "")
	if code != 200 {
		t.Fatalf("proxy %d %s", code, body)
	}
	if cfg, err := jpeg.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width != 720 {
		t.Fatalf("proxied image %+v %v", cfg, err)
	}
	var perf struct {
		Portrait *img `json:"portrait"`
	}
	if code, body = call("GET", "/api/performers/"+p1slug, ""); code != 200 || json.Unmarshal(body, &perf) != nil || perf.Portrait == nil || perf.Portrait.Width != 400 {
		t.Fatalf("performer detail %d %s", code, body)
	}
	if code, _ = call("GET", "/api/images/release/1/cover/../../etc.jpg", ""); code == 200 {
		t.Fatalf("traversal key served")
	}
	if code, _ = call("GET", "/api/images/release/1/cover/unknown.jpg", ""); code != 404 {
		t.Fatalf("unknown key = %d", code)
	}

	// ---- admin: suppress the cover (takedown).
	var origID int64 = scalar(`SELECT id FROM media_assets WHERE object_key=$1`, origKey)
	code, body = call("POST", fmt.Sprintf("/api/admin/media/%d/suppress", origID), `{"reason":"copyright notice #12"}`)
	if code != 200 {
		t.Fatalf("suppress %d %s", code, body)
	}
	if n := scalar(`SELECT count(*) FROM media_assets WHERE entity_type='release' AND entity_id=$1 AND suppressed_at IS NOT NULL`, r1); n != 3 {
		t.Fatalf("suppressed rows = %d, want original + 2 variants", n)
	}
	if n := scalar(`SELECT count(*) FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action='media_suppress' AND actor='takedown-desk'`, r1); n != 1 {
		t.Fatalf("media_suppress events = %d", n)
	}
	if code, _ = call("GET", rel.Cover.URL, ""); code != 404 {
		t.Fatalf("suppressed image still served: %d", code)
	}
	rel.Cover = nil
	if _, body = call("GET", "/api/releases/"+r1slug, ""); json.Unmarshal(body, &rel) != nil || rel.Cover != nil {
		t.Fatalf("suppressed cover still in the release: %s", body)
	}
	code, body = call("GET", fmt.Sprintf("/api/admin/media?entity_type=release&entity_id=%d", r1), "")
	if code != 200 || !strings.Contains(string(body), `"blocks":[{"asset_type":"cover"`) {
		t.Fatalf("admin media %d %s", code, body)
	}
	// The block outlives the rows: no re-download even after the purge.

	// ---- admin: suppress + restore the portrait.
	pOrig := scalar(`SELECT id FROM media_assets WHERE entity_type='performer' AND entity_id=$1 AND variant='original'`, p1)
	if code, body = call("POST", fmt.Sprintf("/api/admin/media/%d/suppress", pOrig), `{"reason":"check"}`); code != 200 {
		t.Fatalf("suppress portrait %d %s", code, body)
	}
	if code, body = call("POST", fmt.Sprintf("/api/admin/media/%d/restore", pOrig), `{"reason":"cleared"}`); code != 200 {
		t.Fatalf("restore portrait %d %s", code, body)
	}
	if n := scalar(`SELECT count(*) FROM media_assets WHERE entity_type='performer' AND entity_id=$1 AND suppressed_at IS NULL`, p1); n != 2 {
		t.Fatalf("restored rows = %d", n)
	}
	if n := scalar(`SELECT count(*) FROM review_events WHERE entity_type='performer' AND entity_id=$1 AND action IN ('media_suppress','media_restore')`, p1); n != 2 {
		t.Fatalf("portrait events = %d", n)
	}

	// ---- purge: the suppressed cover goes after the window; the performer
	// is hidden (takedown), so its photo goes too; then the job is idempotent.
	exec(`UPDATE performers SET status='hidden',hidden_reason='takedown' WHERE id=$1`, p1)
	pr := &images.PurgeRunner{Store: s, Objects: objects}
	if rep, err := pr.Run(ctx); err != nil || rep.Marked < 5 {
		t.Fatalf("first purge (mark only) %+v %v", rep, err)
	}
	if n := scalar(`SELECT count(*) FROM media_assets WHERE (entity_type='release' AND entity_id=$1) OR (entity_type='performer' AND entity_id=$2)`, r1, p1); n != 5 {
		t.Fatalf("rows deleted before the retention window: %d left", n)
	}
	later := time.Now().Add(25 * time.Hour)
	pr.Now = func() time.Time { return later }
	prep, err := pr.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n := scalar(`SELECT count(*) FROM media_assets WHERE (entity_type='release' AND entity_id=$1) OR (entity_type='performer' AND entity_id=$2)`, r1, p1); n != 0 {
		t.Fatalf("rows left after purge: %d (%+v)", n, prep)
	}
	for _, k := range fake.Store.Keys() {
		if strings.Contains(k, fmt.Sprintf("/release/%d/", r1)) || strings.Contains(k, fmt.Sprintf("/performer/%d/", p1)) {
			t.Fatalf("object %s survived the purge", k)
		}
	}
	if got := candidates(); len(got) != 0 {
		t.Fatalf("blocked / hidden entities are candidates again: %v", got)
	}
	// Restoring a performer with an approved remove_photo request is refused.
	exec(`INSERT INTO media_assets(entity_type,entity_id,asset_type,object_key,content_sha256,mime_type,suppressed_at) VALUES('performer',$1,'avatar',$2,$2,'image/jpeg',now())`, p2, "p2-"+sfx)
	p2asset := scalar(`SELECT id FROM media_assets WHERE object_key=$1`, "p2-"+sfx)
	if code, body = call("POST", fmt.Sprintf("/api/admin/media/%d/restore", p2asset), `{"reason":"oops"}`); code != 400 || !strings.Contains(string(body), "remove_photo") {
		t.Fatalf("restore despite remove_photo: %d %s", code, body)
	}
}
