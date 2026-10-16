package images

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"avxxx/internal/model"
	"avxxx/internal/objstore"
)

// URL modes for public image URLs.
const (
	// ModeProxy serves images through the API (GET /api/images/{key}),
	// which checks suppression and entity visibility on every request.
	ModeProxy = "proxy"
	// ModeCDN prefixes keys with S3_PUBLIC_BASE_URL (a CDN with origin
	// access to the private bucket).
	ModeCDN = "cdn"
	// ModeSigned hands out presigned bucket URLs (IMAGE_URL_MODE=signed).
	ModeSigned = "signed"
)

// ProxyPrefix is the API path images are served under in ModeProxy.
const ProxyPrefix = "/api/images/"

// URLBuilder turns object keys into public URLs.
type URLBuilder struct {
	Mode    string
	BaseURL string // ModeCDN
	// ModeSigned: presigning store and URL lifetime. Signatures are made at
	// TTL/2 boundaries so a URL stays stable (cacheable HTML) for TTL/2 and
	// valid for at least TTL/2 after it is handed out.
	Objects objstore.Storage
	TTL     time.Duration
	Now     func() time.Time
}

// URLConfigFromEnv reads S3_PUBLIC_BASE_URL (CDN mode), IMAGE_URL_MODE
// (proxy | signed) and S3_SIGNED_URL_TTL (default 12h, 10m..7d). objects nil
// (storage disabled) returns nil: no image URLs at all.
func URLConfigFromEnv(getenv func(string) string, objects objstore.Storage) (*URLBuilder, error) {
	if objects == nil {
		return nil, nil
	}
	if base := strings.TrimRight(strings.TrimSpace(getenv("S3_PUBLIC_BASE_URL")), "/"); base != "" {
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("invalid S3_PUBLIC_BASE_URL %q", base)
		}
		return &URLBuilder{Mode: ModeCDN, BaseURL: base}, nil
	}
	switch m := strings.ToLower(strings.TrimSpace(getenv("IMAGE_URL_MODE"))); m {
	case "", ModeProxy:
		return &URLBuilder{Mode: ModeProxy}, nil
	case ModeSigned:
		ttl := 12 * time.Hour
		if v := strings.TrimSpace(getenv("S3_SIGNED_URL_TTL")); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d < 10*time.Minute || d > 7*24*time.Hour {
				return nil, fmt.Errorf("S3_SIGNED_URL_TTL %q must be between 10m and 168h", v)
			}
			ttl = d
		}
		return &URLBuilder{Mode: ModeSigned, Objects: objects, TTL: ttl}, nil
	default:
		return nil, fmt.Errorf("IMAGE_URL_MODE %q must be proxy or signed (set S3_PUBLIC_BASE_URL for a CDN)", m)
	}
}

// URL returns the public URL of key ("" when it cannot be built).
func (b *URLBuilder) URL(key string) string {
	if b == nil || objstore.ValidKey(key) != nil {
		return ""
	}
	switch b.Mode {
	case ModeCDN:
		return b.BaseURL + "/" + key
	case ModeSigned:
		now := time.Now()
		if b.Now != nil {
			now = b.Now()
		}
		half := b.TTL / 2
		signedAt := now.Truncate(half)
		u, err := b.Objects.PresignGet(key, b.TTL, signedAt)
		if err != nil {
			return ""
		}
		return u
	default:
		return ProxyPrefix + key
	}
}

// PublicAsset is one visible variant of an entity's current image.
type PublicAsset struct {
	EntityID  int64
	Variant   string
	ObjectKey string
	Width     int
	Height    int
}

// Build assembles the public image of one entity from its visible variants:
// url is the 720w variant when there is one (else the original), srcset
// lists every variant narrowest first. nil when there is nothing to show.
func (b *URLBuilder) Build(variants []PublicAsset) *model.Image {
	if b == nil || len(variants) == 0 {
		return nil
	}
	vs := append([]PublicAsset(nil), variants...)
	sort.Slice(vs, func(i, j int) bool { return vs[i].Width < vs[j].Width })
	img := &model.Image{}
	var display *PublicAsset
	for i := range vs {
		v := &vs[i]
		u := b.URL(v.ObjectKey)
		if u == "" || v.Width <= 0 || v.Height <= 0 {
			continue
		}
		img.Srcset = append(img.Srcset, model.ImageVariant{URL: u, Width: v.Width, Height: v.Height})
		if display == nil || v.Variant == VariantW720 || (display.Variant != VariantW720 && v.Variant == VariantOriginal) {
			display = v
		}
	}
	if display == nil {
		return nil
	}
	img.URL, img.Width, img.Height = b.URL(display.ObjectKey), display.Width, display.Height
	return img
}
