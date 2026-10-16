package images

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"mime"
	"net/http"
	"strings"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Limits bound what the fetch job accepts and stores.
type Limits struct {
	MaxBytes     int64 // downloaded bytes (IMAGE_MAX_BYTES)
	MaxDimension int   // longest stored side; larger originals are scaled down (IMAGE_MAX_DIMENSION)
	MinDimension int   // shortest accepted side
	MaxSide      int   // longest accepted source side (decompression-bomb guard)
	MaxPixels    int   // accepted source width*height
	JPEGQuality  int
}

// DefaultLimits are the documented policy values.
func DefaultLimits() Limits {
	return Limits{MaxBytes: 8 << 20, MaxDimension: 1600, MinDimension: 64, MaxSide: 8000, MaxPixels: 40_000_000, JPEGQuality: 82}
}

// DerivedWidths are the derived variants, narrowest first.
var DerivedWidths = []struct {
	Variant string
	Width   int
}{{VariantW320, 320}, {VariantW720, 720}}

// ErrRejected wraps every validation failure (never retried quickly).
var ErrRejected = errors.New("image rejected")

func reject(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, a...))
}

// Encoded is one stored object.
type Encoded struct {
	Variant string
	Body    []byte
	MIME    string
	Ext     string
	Width   int
	Height  int
	SHA256  string // of Body
}

// Processed is a validated download ready to store.
type Processed struct {
	// SourceSHA256 is the hash of the downloaded bytes: the dedupe key and
	// the {sha256} of every object key of this image.
	SourceSHA256 string
	Original     Encoded
	Derived      []Encoded
}

var acceptedTypes = map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp"}

// SHA256Hex is the lowercase hex SHA-256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sniff(body []byte) string {
	if len(body) >= 12 && string(body[0:4]) == "RIFF" && string(body[8:12]) == "WEBP" {
		return "image/webp"
	}
	return http.DetectContentType(body)
}

// Process validates a downloaded image and produces the stored original and
// its derived sizes. headerType is the response Content-Type; it must name
// an accepted type and agree with the sniffed content.
func Process(body []byte, headerType string, lim Limits) (Processed, error) {
	if lim.MaxBytes > 0 && int64(len(body)) > lim.MaxBytes {
		return Processed{}, reject("%d bytes exceeds %d", len(body), lim.MaxBytes)
	}
	if len(body) == 0 {
		return Processed{}, reject("empty body")
	}
	mt, _, _ := mime.ParseMediaType(headerType)
	mt = strings.ToLower(mt)
	ext, ok := acceptedTypes[mt]
	if !ok {
		return Processed{}, reject("content type %q is not jpeg, png or webp", headerType)
	}
	if got := sniff(body); got != mt {
		return Processed{}, reject("content is %s, header says %s", got, mt)
	}
	cfg, err := decodeConfig(mt, body)
	if err != nil {
		return Processed{}, reject("decode header: %v", err)
	}
	if err := checkDimensions(cfg.Width, cfg.Height, lim); err != nil {
		return Processed{}, err
	}
	img, err := decode(mt, body)
	if err != nil {
		return Processed{}, reject("decode: %v", err)
	}
	p := Processed{SourceSHA256: SHA256Hex(body)}
	b := img.Bounds()
	if lim.MaxDimension > 0 && max(b.Dx(), b.Dy()) > lim.MaxDimension {
		w, h := fit(b.Dx(), b.Dy(), lim.MaxDimension)
		enc, err := encodeJPEG(resize(img, w, h), lim.JPEGQuality)
		if err != nil {
			return Processed{}, err
		}
		p.Original = Encoded{Variant: VariantOriginal, Body: enc, MIME: "image/jpeg", Ext: "jpg", Width: w, Height: h, SHA256: SHA256Hex(enc)}
	} else {
		p.Original = Encoded{Variant: VariantOriginal, Body: body, MIME: mt, Ext: ext, Width: b.Dx(), Height: b.Dy(), SHA256: p.SourceSHA256}
	}
	for _, d := range DerivedWidths {
		if p.Original.Width <= d.Width {
			continue
		}
		h := max(1, (p.Original.Height*d.Width+p.Original.Width/2)/p.Original.Width)
		enc, err := encodeJPEG(resize(img, d.Width, h), lim.JPEGQuality)
		if err != nil {
			return Processed{}, err
		}
		p.Derived = append(p.Derived, Encoded{Variant: d.Variant, Body: enc, MIME: "image/jpeg", Ext: "jpg", Width: d.Width, Height: h, SHA256: SHA256Hex(enc)})
	}
	return p, nil
}

func checkDimensions(w, h int, lim Limits) error {
	switch {
	case w <= 0 || h <= 0:
		return reject("no dimensions")
	case lim.MinDimension > 0 && min(w, h) < lim.MinDimension:
		return reject("%dx%d is smaller than %d px", w, h, lim.MinDimension)
	case lim.MaxSide > 0 && max(w, h) > lim.MaxSide:
		return reject("%dx%d exceeds %d px", w, h, lim.MaxSide)
	case lim.MaxPixels > 0 && w*h > lim.MaxPixels:
		return reject("%dx%d exceeds %d pixels", w, h, lim.MaxPixels)
	}
	return nil
}

// fit scales w x h so the longest side is limit, keeping the aspect ratio.
func fit(w, h, limit int) (int, int) {
	if w >= h {
		return limit, max(1, (h*limit+w/2)/w)
	}
	return max(1, (w*limit+h/2)/h), limit
}

func decodeConfig(mt string, body []byte) (image.Config, error) {
	r := bytes.NewReader(body)
	switch mt {
	case "image/jpeg":
		return jpeg.DecodeConfig(r)
	case "image/png":
		return png.DecodeConfig(r)
	default:
		return webp.DecodeConfig(r)
	}
}

func decode(mt string, body []byte) (image.Image, error) {
	r := bytes.NewReader(body)
	switch mt {
	case "image/jpeg":
		return jpeg.Decode(r)
	case "image/png":
		return png.Decode(r)
	default:
		return webp.Decode(r)
	}
}

func resize(src image.Image, w, h int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// JPEG has no alpha: composite transparent pixels over white.
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	if quality <= 0 || quality > 100 {
		quality = 82
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ObjectKey is the key of an original: {entity_type}/{entity_id}/{asset_type}/{sha256}.{ext}.
func ObjectKey(entityType string, entityID int64, assetType, sha256Hex, ext string) string {
	return fmt.Sprintf("%s/%d/%s/%s.%s", entityType, entityID, assetType, sha256Hex, ext)
}

// VariantKey is the key of a derived size of the original with source hash
// sha256Hex: {entity_type}/{entity_id}/{asset_type}/{sha256}.{variant}.{ext}.
func VariantKey(entityType string, entityID int64, assetType, sha256Hex, variant, ext string) string {
	return fmt.Sprintf("%s/%d/%s/%s.%s.%s", entityType, entityID, assetType, sha256Hex, variant, ext)
}
