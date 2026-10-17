package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// Document is the bounded response passed from the fetcher to an adapter.
// Adapters never perform network requests or write to the database.
type Document struct {
	URL         string
	FinalURL    string
	Status      int
	ContentType string
	Body        []byte
}

type EntityType string

const (
	Performer EntityType = "performer"
	Release   EntityType = "release"
)

type Record struct {
	EntityType EntityType
	SourceKey  string
	URL        string
	Fields     map[string]any
	Links      []string
}

type Adapter interface {
	Name() string
	Discover(ctx context.Context, doc Document) ([]Record, error)
	Parse(ctx context.Context, doc Document, entity EntityType) (Record, error)
}

// Adapters returns every built-in catalog adapter (the scraper registry and
// the fixture acceptance harness share this list). The DMM adapter reads its
// credentials from the environment; unconfigured, it still parses fixtures
// but refuses to build an HTTP client (HTTPClientFor -> ErrNotConfigured).
// The ThePornDB adapter (THEPORNDB_API_TOKEN) behaves the same way.
// The official maker catalogs ("maker:<slug>", maker.go) follow, one per
// built-in profile; AdaptersFromEnv adds MAKER_PROFILES_FILE overrides.
// XSList and JAVLibrary are registered like any adapter; their sources are
// seeded disabled and review_required (migrations/*_vpn_sources.sql), so only manual verify
// jobs reach them until an operator enables them.
func Adapters() []Adapter {
	out := []Adapter{NewJAVDatabaseAdapter(), NewXCITYAdapter(), NewJavGuruAdapter(), NewDMMAdapter(), NewThePornDBAdapter(),
		NewXSListAdapter(), NewJAVLibraryAdapter()}
	return append(out, builtinMakerAdapters()...)
}

// LookupAdapter is implemented by adapters that can look a release up by
// product code (cross-check mode, internal/crosscheck): LookupURLs returns
// the API URLs to fetch for one code; Discover maps their responses.
type LookupAdapter interface {
	Adapter
	LookupURLs(code string) []string
}

type Registry struct{ adapters map[string]Adapter }

func NewRegistry() *Registry { return &Registry{adapters: make(map[string]Adapter)} }
func (r *Registry) Register(a Adapter) error {
	if a == nil || strings.TrimSpace(a.Name()) == "" {
		return errors.New("catalog: adapter name is required")
	}
	name := strings.ToLower(strings.TrimSpace(a.Name()))
	if _, ok := r.adapters[name]; ok {
		return errors.New("catalog: adapter already registered")
	}
	r.adapters[name] = a
	return nil
}
func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[strings.ToLower(strings.TrimSpace(name))]
	return a, ok
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(value string) string {
	s := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range s {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || unicode.IsSpace(r)) {
			b.WriteRune(r)
		}
	}
	s = nonSlug.ReplaceAllString(b.String(), "-")
	return strings.Trim(s, "-")
}

func ReleaseSlug(code, title, source, key string) string {
	if c := Slug(strings.ReplaceAll(code, " ", "-")); c != "" {
		return c
	}
	base := Slug(title)
	if base == "" {
		base = "release"
	}
	return base + "-" + Slug(source) + "-" + Slug(key)
}

func NormalizeIdentifier(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), ""))
}
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(strings.ToLower(strings.TrimSpace(p))))
		h.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}
