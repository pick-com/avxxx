package media

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// Bounds of the search ResultCache. Its keys embed the caller's q, so both
// the number of entries and the bytes they hold are capped.
const (
	// maxCacheEntries caps the number of cached results.
	maxCacheEntries = 10000
	// maxCacheBytes caps the total size of the cached results (encoded
	// JSON) plus their keys.
	maxCacheBytes = 32 << 20
	// maxCacheValueBytes: a larger result is served but not cached, so
	// one response cannot take a large share of the budget.
	maxCacheValueBytes = maxCacheBytes / 64
	// maxCacheKeyLen is the longest key kept verbatim (cacheKey).
	maxCacheKeyLen = 256
)

// cacheKey bounds the bytes a key costs in an in-process cache: keys
// longer than maxCacheKeyLen (built from caller-supplied q or ids) are
// replaced by their prefix up to the first ':' plus their SHA-256.
func cacheKey(key string) string {
	if len(key) <= maxCacheKeyLen {
		return key
	}
	prefix := ""
	for i := 0; i < len(key) && i < 32; i++ {
		if key[i] == ':' {
			prefix = key[:i+1]
			break
		}
	}
	sum := sha256.Sum256([]byte(key))
	return prefix + "sha256:" + hex.EncodeToString(sum[:])
}

// ResultCache is a short-lived cache of JSON results (provider searches).
// Values are kept encoded: Get returns a json.RawMessage of what Set was
// given. It holds at most maxCacheEntries entries and maxCacheBytes bytes;
// past either, the oldest entries (expired first, then by insertion) go.
type ResultCache struct {
	mu     sync.Mutex
	values map[string]*list.Element // of *cacheValue
	order  *list.List               // oldest insertion at the front
	bytes  int
	clock  func() time.Time // nil = time.Now (tests override it)
}

type cacheValue struct {
	key     string
	body    json.RawMessage
	expires time.Time
}

func (v *cacheValue) size() int { return len(v.key) + len(v.body) }

func NewResultCache() *ResultCache {
	return &ResultCache{values: map[string]*list.Element{}, order: list.New()}
}

func (c *ResultCache) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

// Get returns the cached JSON for key while it is fresh.
func (c *ResultCache) Get(key string) (any, bool) {
	key = cacheKey(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.values[key]
	if !ok {
		return nil, false
	}
	v := el.Value.(*cacheValue)
	if !c.now().Before(v.expires) {
		c.remove(el)
		return nil, false
	}
	return v.body, true
}

// Set caches value's JSON encoding for ttl. A value that does not encode,
// or encodes to more than maxCacheValueBytes, is not cached.
func (c *ResultCache) Set(key string, value any, ttl time.Duration) {
	body, err := json.Marshal(value)
	if err != nil || len(body) > maxCacheValueBytes {
		return
	}
	key = cacheKey(key)
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if el, ok := c.values[key]; ok {
		c.remove(el)
	}
	v := &cacheValue{key: key, body: body, expires: now.Add(ttl)}
	c.values[key] = c.order.PushBack(v)
	c.bytes += v.size()
	c.shrink(now)
}

// Len and Bytes report the cache's size (tests).
func (c *ResultCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.values)
}

func (c *ResultCache) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

// shrink drops expired entries from the front (oldest insertions; TTLs are
// uniform per call site, so they expire first) and then the oldest entries
// until the cache fits its bounds.
func (c *ResultCache) shrink(now time.Time) {
	for el := c.order.Front(); el != nil; el = c.order.Front() {
		over := len(c.values) > maxCacheEntries || c.bytes > maxCacheBytes
		if !over && now.Before(el.Value.(*cacheValue).expires) {
			return
		}
		c.remove(el)
	}
}

func (c *ResultCache) remove(el *list.Element) {
	v := c.order.Remove(el).(*cacheValue)
	delete(c.values, v.key)
	c.bytes -= v.size()
}
