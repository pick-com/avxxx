package images

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"avxxx/internal/catalog"
	"avxxx/internal/objstore"
)

func reviewed() *time.Time { t := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC); return &t }

func TestSourceAllowed(t *testing.T) {
	ok := SourcePolicy{ID: 1, Adapter: "dmm", LicenseStatus: catalog.LicenseOfficialAPI, TermsReviewedAt: reviewed(), ImagesAllowed: true, ImagesReviewedAt: reviewed()}
	cases := []struct {
		name   string
		edit   func(*SourcePolicy)
		allow  bool
		reason string
	}{
		{"official api, reviewed, flag on", func(*SourcePolicy) {}, true, ""},
		{"licensed", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseLicensed }, true, ""},
		{"internal", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseInternal }, true, ""},
		{"terms_confirmed covers metadata only", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseTermsConfirmed }, false, "does not cover images"},
		{"review_required", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseReviewRequired }, false, "does not cover images"},
		{"manual_only", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseManualOnly }, false, "does not cover images"},
		{"denied", func(p *SourcePolicy) { p.LicenseStatus = catalog.LicenseDenied }, false, "does not cover images"},
		{"terms not reviewed", func(p *SourcePolicy) { p.TermsReviewedAt = nil }, false, "terms have not been reviewed"},
		{"flag off", func(p *SourcePolicy) { p.ImagesAllowed = false }, false, "images_allowed is off"},
		{"flag without review", func(p *SourcePolicy) { p.ImagesReviewedAt = nil }, false, "no recorded review"},
		{"rss feed", func(p *SourcePolicy) { p.Adapter = "rss" }, false, "feed"},
	}
	for _, c := range cases {
		p := ok
		c.edit(&p)
		d := SourceAllowed(p)
		if d.Allowed != c.allow || !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s: %+v, want allowed=%v reason~%q", c.name, d, c.allow, c.reason)
		}
	}
	if LicensePermitsImages(catalog.LicenseTermsConfirmed) || !LicensePermitsImages(catalog.LicenseLicensed) {
		t.Error("LicensePermitsImages")
	}
}

func TestAssetAllowed(t *testing.T) {
	for _, c := range []struct {
		et, at string
		want   bool
	}{{"release", "cover", true}, {"performer", "avatar", true}, {"release", "gallery", false}, {"release", "thumbnail", false},
		{"performer", "gallery", false}, {"performer", "cover", false}, {"series", "cover", false}, {"release", "", false}} {
		if got := AssetAllowed(c.et, c.at); got != c.want {
			t.Errorf("AssetAllowed(%s,%s)=%v", c.et, c.at, got)
		}
	}
}

func TestKeys(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	if got := ObjectKey("release", 42, "cover", sha, "jpg"); got != "release/42/cover/"+sha+".jpg" {
		t.Errorf("ObjectKey = %s", got)
	}
	k := VariantKey("performer", 7, "avatar", sha, "w320", "jpg")
	if k != "performer/7/avatar/"+sha+".w320.jpg" {
		t.Errorf("VariantKey = %s", k)
	}
	for _, key := range []string{k, ObjectKey("release", 1, "cover", sha, "png")} {
		if objstore.ValidKey(key) != nil || !managedKey(key) {
			t.Errorf("key %s not valid/managed", key)
		}
	}
	if managedKey("backups/x.sql") || managedKey("release/1/cover") {
		t.Error("managedKey accepts foreign keys")
	}
}

func testImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x + y), 255})
		}
	}
	return img
}

func jpegBytes(t *testing.T, w, h int) []byte {
	var b bytes.Buffer
	if err := jpeg.Encode(&b, testImage(w, h), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func pngBytes(t *testing.T, img image.Image) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestProcessCapsAndDerives(t *testing.T) {
	body := jpegBytes(t, 2000, 1000)
	p, err := Process(body, "image/jpeg; charset=binary", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceSHA256 != SHA256Hex(body) {
		t.Error("SourceSHA256 is not the hash of the downloaded bytes")
	}
	if o := p.Original; o.Width != 1600 || o.Height != 800 || o.MIME != "image/jpeg" || o.SHA256 == p.SourceSHA256 {
		t.Errorf("capped original = %dx%d %s", o.Width, o.Height, o.MIME)
	}
	if len(p.Derived) != 2 || p.Derived[0].Variant != VariantW320 || p.Derived[0].Width != 320 || p.Derived[0].Height != 160 ||
		p.Derived[1].Variant != VariantW720 || p.Derived[1].Height != 360 {
		t.Fatalf("derived = %+v", p.Derived)
	}
	for _, d := range p.Derived {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(d.Body))
		if err != nil || cfg.Width != d.Width || cfg.Height != d.Height {
			t.Errorf("derived %s decodes to %+v %v", d.Variant, cfg, err)
		}
	}
}

func TestProcessKeepsSmallOriginal(t *testing.T) {
	body := pngBytes(t, testImage(500, 300))
	p, err := Process(body, "image/png", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p.Original.Body, body) || p.Original.Ext != "png" || p.Original.SHA256 != p.SourceSHA256 {
		t.Error("an original within the cap must be stored byte for byte")
	}
	if len(p.Derived) != 1 || p.Derived[0].Variant != VariantW320 {
		t.Errorf("500px original: derived = %+v, want only w320", p.Derived)
	}
	// Transparent PNGs are flattened on white for JPEG variants.
	tr := image.NewNRGBA(image.Rect(0, 0, 400, 400))
	p, err = Process(pngBytes(t, tr), "image/png", DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	dec, _ := jpeg.Decode(bytes.NewReader(p.Derived[0].Body))
	if r, g, b, _ := dec.At(10, 10).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent pixel became %d,%d,%d", r>>8, g>>8, b>>8)
	}
}

func TestProcessRejects(t *testing.T) {
	lim := DefaultLimits()
	small := jpegBytes(t, 32, 32)
	wide := pngBytes(t, image.NewGray(image.Rect(0, 0, 9000, 70)))
	jp := jpegBytes(t, 200, 200)
	cases := []struct {
		name string
		body []byte
		ct   string
		lim  func(*Limits)
		want string
	}{
		{"html", []byte("<html>blocked</html>"), "text/html", nil, "not jpeg"},
		{"gif type", jp, "image/gif", nil, "not jpeg"},
		{"header lies", jp, "image/png", nil, "content is image/jpeg"},
		{"webp header, png body", pngBytes(t, testImage(100, 100)), "image/webp", nil, "content is image/png"},
		{"too small", small, "image/jpeg", nil, "smaller than 64"},
		{"too wide", wide, "image/png", nil, "exceeds 8000"},
		{"too many bytes", jp, "image/jpeg", func(l *Limits) { l.MaxBytes = 100 }, "exceeds 100"},
		{"too many pixels", jp, "image/jpeg", func(l *Limits) { l.MaxPixels = 1000 }, "pixels"},
		{"empty", nil, "image/jpeg", nil, "empty"},
		{"truncated", jp[:len(jp)/3], "image/jpeg", nil, "decode"},
	}
	for _, c := range cases {
		l := lim
		if c.lim != nil {
			c.lim(&l)
		}
		_, err := Process(c.body, c.ct, l)
		if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want rejection containing %q", c.name, err, c.want)
		}
	}
}

func TestEvidenceURL(t *testing.T) {
	rel := map[string]any{"cover_url": "https://pics.example/cover-l.jpg", "image_urls": map[string]any{"small": "https://pics.example/s.jpg"}}
	if got := EvidenceURL(EntityRelease, rel); got != "https://pics.example/cover-l.jpg" {
		t.Errorf("release cover_url: %s", got)
	}
	rel = map[string]any{"cover_url": "javascript:alert(1)", "image_urls": map[string]any{"small": "https://pics.example/s.jpg", "list": "https://pics.example/l.jpg"}}
	if got := EvidenceURL(EntityRelease, rel); got != "https://pics.example/s.jpg" {
		t.Errorf("release fallback: %s", got)
	}
	perf := map[string]any{"image_urls": map[string]any{"small": "https://pics.example/a-s.jpg", "large": "https://pics.example/a-l.jpg"}}
	if got := EvidenceURL(EntityPerformer, perf); got != "https://pics.example/a-l.jpg" {
		t.Errorf("performer large: %s", got)
	}
	if got := EvidenceURL(EntityPerformer, map[string]any{"cover_url": "https://pics.example/c.jpg"}); got != "" {
		t.Errorf("performers ignore cover_url: %s", got)
	}
	if got := EvidenceURL(EntityRelease, map[string]any{"cover": "https://user:pw@pics.example/c.jpg"}); got != "" {
		t.Errorf("credentials in URL accepted: %s", got)
	}
	for _, u := range []string{"http://127.0.0.1/x.jpg", "http://localhost:9000/x.jpg", "http://10.1.2.3/x.jpg", "http://[::1]/x.jpg",
		"http://169.254.169.254/latest", "http://minio.internal/x.jpg", "http://192.168.1.1/x.jpg"} {
		if PublicURL(u) {
			t.Errorf("PublicURL(%s) = true", u)
		}
	}
	if !PublicURL("https://pics.dmm.co.jp/digital/video/abc/abcpl.jpg") {
		t.Error("public URL refused")
	}
}

func TestNextTry(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for attempts, want := range map[int]time.Duration{1: 24 * time.Hour, 2: 48 * time.Hour, 3: 96 * time.Hour, 10: 30 * 24 * time.Hour} {
		if got := NextTry(now, attempts).Sub(now); got != want {
			t.Errorf("NextTry(%d) = %s, want %s", attempts, got, want)
		}
	}
}

func TestURLBuilder(t *testing.T) {
	vs := []PublicAsset{
		{Variant: VariantOriginal, ObjectKey: "release/1/cover/aa.jpg", Width: 800, Height: 538},
		{Variant: VariantW720, ObjectKey: "release/1/cover/aa.w720.jpg", Width: 720, Height: 484},
		{Variant: VariantW320, ObjectKey: "release/1/cover/aa.w320.jpg", Width: 320, Height: 215},
	}
	b, err := URLConfigFromEnv(func(string) string { return "" }, objstore.NewMemory())
	if err != nil || b.Mode != ModeProxy {
		t.Fatalf("default mode = %+v %v", b, err)
	}
	img := b.Build(vs)
	if img.URL != "/api/images/release/1/cover/aa.w720.jpg" || img.Width != 720 || img.Height != 484 {
		t.Errorf("display = %+v", img)
	}
	if len(img.Srcset) != 3 || img.Srcset[0].Width != 320 || img.Srcset[2].Width != 800 {
		t.Errorf("srcset = %+v", img.Srcset)
	}
	// Only an original: it is the display size.
	if one := b.Build(vs[:1]); one.URL != "/api/images/release/1/cover/aa.jpg" || len(one.Srcset) != 1 {
		t.Errorf("original only = %+v", one)
	}
	// w320 + original, no w720: the original is shown.
	if two := b.Build([]PublicAsset{vs[2], vs[0]}); two.Width != 800 {
		t.Errorf("w320+original display = %+v", two)
	}
	if b.Build(nil) != nil {
		t.Error("no variants must give no image")
	}
	cdn, _ := URLConfigFromEnv(func(k string) string {
		return map[string]string{"S3_PUBLIC_BASE_URL": "https://img.example.com/"}[k]
	}, objstore.NewMemory())
	if got := cdn.URL("release/1/cover/aa.jpg"); got != "https://img.example.com/release/1/cover/aa.jpg" {
		t.Errorf("cdn = %s", got)
	}
	now := time.Date(2026, 10, 1, 7, 59, 0, 0, time.UTC)
	signed, err := URLConfigFromEnv(func(k string) string { return map[string]string{"IMAGE_URL_MODE": "signed"}[k] }, objstore.NewMemory())
	if err != nil || signed.TTL != 12*time.Hour {
		t.Fatal(signed, err)
	}
	signed.Now = func() time.Time { return now }
	a := signed.URL("release/1/cover/aa.jpg")
	signed.Now = func() time.Time { return now.Add(-time.Hour) }
	if signed.URL("release/1/cover/aa.jpg") != a {
		t.Error("signed URLs must be stable within a half-TTL window")
	}
	if !strings.Contains(a, "expires=2026-10-01T18:00:00Z") {
		t.Errorf("signed at the 6h boundary: %s", a)
	}
	for _, env := range []map[string]string{{"IMAGE_URL_MODE": "public"}, {"IMAGE_URL_MODE": "signed", "S3_SIGNED_URL_TTL": "1m"}, {"S3_PUBLIC_BASE_URL": "ftp://x"}} {
		if _, err := URLConfigFromEnv(func(k string) string { return env[k] }, objstore.NewMemory()); err == nil {
			t.Errorf("env %v accepted", env)
		}
	}
	if b, _ := URLConfigFromEnv(func(string) string { return "" }, nil); b != nil || b.Build(vs) != nil {
		t.Error("no storage must mean no URLs")
	}
}

func TestConfigFromEnv(t *testing.T) {
	c, warn := FetchConfigFromEnv(func(k string) string {
		return map[string]string{"IMAGE_FETCH_MIN_INTERVAL": "500ms", "IMAGE_FETCH_MAX_PER_RUN": "50", "IMAGE_MAX_BYTES": "99999999"}[k]
	})
	if c.MinInterval != DefaultFetchMinInterval || c.MaxPerRun != 50 || c.Limits.MaxBytes != 10<<20 || len(warn) != 1 {
		t.Errorf("fetch config = %+v %v", c, warn)
	}
	p, warn := PurgeConfigFromEnv(func(k string) string { return map[string]string{"MEDIA_PURGE_AFTER": "48h"}[k] })
	if p.After != 24*time.Hour || len(warn) != 1 {
		t.Errorf("purge after above the policy bound: %+v %v", p, warn)
	}
	p, _ = PurgeConfigFromEnv(func(k string) string { return map[string]string{"MEDIA_PURGE_AFTER": "2h"}[k] })
	if p.After != 2*time.Hour {
		t.Errorf("purge after = %s", p.After)
	}
}
