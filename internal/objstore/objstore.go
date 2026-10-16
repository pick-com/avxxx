// Package objstore is the object storage behind catalog images
// (docs/IMAGE_STORAGE.md): a small Storage interface, an S3-compatible
// client (MinIO, AWS S3, R2, ...) signed with AWS Signature Version 4, and
// an in-memory implementation for tests.
//
// The S3 client is a deliberate, minimal implementation rather than
// minio-go or aws-sdk-go-v2: the image jobs need five operations (put, get,
// delete, list, presign) against one private bucket, and the SDKs would add
// a dozen transitive modules for that. The signer is checked against AWS's
// published Signature V4 examples (sigv4_test.go).
package objstore

import (
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned by Get for a missing object.
var ErrNotFound = errors.New("objstore: object not found")

// Object is one listed object.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// Info describes an object returned by Get.
type Info struct {
	ContentType  string
	Size         int64
	ETag         string
	LastModified time.Time
}

// Storage is the object store the image jobs and the API use. Keys never
// start with "/". Implementations are safe for concurrent use.
type Storage interface {
	// Put stores body under key, replacing any existing object.
	Put(ctx context.Context, key string, body []byte, contentType string) error
	// Get opens an object; the caller closes the reader. Missing: ErrNotFound.
	Get(ctx context.Context, key string) (io.ReadCloser, Info, error)
	// Delete removes an object; a missing object is not an error.
	Delete(ctx context.Context, key string) error
	// List returns up to max objects whose key starts with prefix, in key
	// order, strictly after startAfter ("" = from the start). more reports
	// whether further objects follow.
	List(ctx context.Context, prefix, startAfter string, max int) (objs []Object, more bool, err error)
	// PresignGet returns a URL that reads key without credentials until
	// now+expires.
	PresignGet(key string, expires time.Duration, now time.Time) (string, error)
}

// Memory is an in-memory Storage for tests.
type Memory struct {
	mu      sync.Mutex
	objects map[string]memObject
	// Now stamps LastModified (default time.Now).
	Now func() time.Time
	// Puts counts Put calls (dedupe tests).
	Puts int
}

type memObject struct {
	body        []byte
	contentType string
	modified    time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Memory { return &Memory{objects: map[string]memObject{}} }

func (m *Memory) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

func (m *Memory) Put(_ context.Context, key string, body []byte, contentType string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{body: append([]byte(nil), body...), contentType: contentType, modified: m.now()}
	m.Puts++
	return nil
}

// SetModified backdates an object (orphan grace tests).
func (m *Memory) SetModified(key string, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.objects[key]; ok {
		o.modified = t
		m.objects[key] = o
	}
}

func (m *Memory) Get(_ context.Context, key string) (io.ReadCloser, Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objects[key]
	if !ok {
		return nil, Info{}, ErrNotFound
	}
	return io.NopCloser(strings.NewReader(string(o.body))), Info{ContentType: o.contentType, Size: int64(len(o.body)), LastModified: o.modified}, nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *Memory) List(_ context.Context, prefix, startAfter string, max int) ([]Object, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.objects))
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) && k > startAfter {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	more := false
	if max > 0 && len(keys) > max {
		keys, more = keys[:max], true
	}
	out := make([]Object, len(keys))
	for i, k := range keys {
		out[i] = Object{Key: k, Size: int64(len(m.objects[k].body)), LastModified: m.objects[k].modified}
	}
	return out, more, nil
}

func (m *Memory) PresignGet(key string, expires time.Duration, now time.Time) (string, error) {
	return "memory://" + key + "?expires=" + now.Add(expires).UTC().Format(time.RFC3339), nil
}

// Keys returns every stored key, sorted (tests).
func (m *Memory) Keys() []string {
	objs, _, _ := m.List(context.Background(), "", "", 0)
	out := make([]string, len(objs))
	for i, o := range objs {
		out[i] = o.Key
	}
	return out
}

// ValidKey rejects keys the S3 client would mangle or that could escape the
// bucket prefix scheme.
func ValidKey(key string) error {
	switch {
	case key == "" || len(key) > 512:
		return errors.New("objstore: key must be 1-512 bytes")
	case strings.HasPrefix(key, "/") || strings.Contains(key, "//") || strings.Contains(key, ".."):
		return errors.New("objstore: invalid key")
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f || r == '\\' || r == '?' || r == '#' {
			return errors.New("objstore: invalid key character")
		}
	}
	return nil
}
