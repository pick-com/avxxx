package media

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Resolve results (direct stream URLs) are cached per (source, id) in two
// levels: an in-process map (L1) in front of an optional shared store (L2,
// Postgres media_resolve_cache) so restarts and several API instances reuse
// them.
//
// Sliding TTL: every hit pushes the entry's expiry to now + ttl, but never
// past its hard cap, fixed when the result was resolved:
//   - the earliest stream / subtitle expires_at minus 30 s when known
//     (adapters set it, or it is read from the signed URL: expiry.go);
//   - otherwise resolved_at + maxAge (MEDIA_RESOLVE_MAX_AGE).
//
// Refresh (?refresh=1, sent by the player when a cached URL fails): evicts the
// entry from both levels and resolves again, at most once per refreshEvery
// per (source, id) and API instance; otherwise the cached (or a fresh)
// result is returned.

const (
	// DefaultResolveMaxAge is MEDIA_RESOLVE_MAX_AGE's default.
	DefaultResolveMaxAge = 30 * time.Minute
	// DefaultResolveRefreshInterval is the minimum gap between two forced
	// refreshes of one (source, id).
	DefaultResolveRefreshInterval = 60 * time.Second
	// resolveExpiryMargin keeps a cached result this far from the streams'
	// real expiry so a player has time to start.
	resolveExpiryMargin = 30 * time.Second
	// MaxStoredResolveBytes caps one L2 row's JSON; bigger results stay L1-only.
	MaxStoredResolveBytes = 64 << 10
	maxResolveEntries     = 10000
	resolvePurgeEvery     = 10 * time.Minute
	resolveStoreTimeout   = 2 * time.Second
)

// StoredResolve is one shared-cache row.
type StoredResolve struct {
	Result       json.RawMessage
	ExpiresAt    time.Time // sliding expiry
	MaxExpiresAt time.Time // hard cap; ExpiresAt never moves past it
	RefreshedAt  time.Time // when the result was resolved from the provider
}

// ResolveStore is the shared L2 cache (store.Store implements it). nil keeps
// the cache in-process only.
type ResolveStore interface {
	// LoadResolve returns the row for key when it is still valid at now,
	// else nil.
	LoadResolve(ctx context.Context, key string, now time.Time) (*StoredResolve, error)
	SaveResolve(ctx context.Context, key string, e StoredResolve) error
	// ExtendResolve moves expires_at forward to at most max_expires_at.
	ExtendResolve(ctx context.Context, key string, expiresAt time.Time) error
	DeleteResolve(ctx context.Context, key string) error
	// PurgeResolves deletes rows expired at now.
	PurgeResolves(ctx context.Context, now time.Time) (int64, error)
}

// resolveHardCap is the latest time a result resolved at now may be served:
// the earliest known stream expiry minus the margin, else now + maxAge.
func resolveHardCap(v *ResolveResult, now time.Time, maxAge time.Duration) time.Time {
	var earliest *time.Time
	for _, list := range [][]Stream{v.Streams, v.Subtitles} {
		for _, st := range list {
			if st.ExpiresAt != nil && (earliest == nil || st.ExpiresAt.Before(*earliest)) {
				earliest = st.ExpiresAt
			}
		}
	}
	if earliest != nil {
		return earliest.Add(-resolveExpiryMargin)
	}
	return now.Add(maxAge)
}

// slideExpiry is the expiry after an access at now: now + ttl, capped.
func slideExpiry(now time.Time, ttl time.Duration, hardCap time.Time) time.Time {
	exp := now.Add(ttl)
	if exp.After(hardCap) {
		return hardCap
	}
	return exp
}

type resolveEntry struct {
	result      *ResolveResult
	expires     time.Time
	maxExpires  time.Time
	storedUntil time.Time // expires last written to the store (zero: not stored)
}

type resolveCache struct {
	mu           sync.Mutex
	entries      map[string]*resolveEntry
	refreshes    map[string]time.Time
	ttl          time.Duration
	maxAge       time.Duration
	refreshEvery time.Duration
	store        ResolveStore
	clock        func() time.Time
	lastPurge    time.Time
	purging      bool
}

func newResolveCache(ttl, maxAge time.Duration) *resolveCache {
	if maxAge <= 0 {
		maxAge = DefaultResolveMaxAge
	}
	return &resolveCache{entries: map[string]*resolveEntry{}, refreshes: map[string]time.Time{}, ttl: ttl, maxAge: maxAge, refreshEvery: DefaultResolveRefreshInterval}
}

func (c *resolveCache) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

func (c *resolveCache) enabled() bool { return c != nil && c.ttl > 0 }

// get returns a valid cached result and slides its expiry.
func (c *resolveCache) get(ctx context.Context, key string) (*ResolveResult, bool) {
	now := c.now()
	c.mu.Lock()
	e, ok := c.entries[key]
	if ok && now.Before(e.expires) {
		e.expires = maxTime(e.expires, slideExpiry(now, c.ttl, e.maxExpires))
		// Write the slide through only when it moved by a quarter TTL, so
		// a busy page is not one UPDATE per view.
		var extendTo time.Time
		if c.store != nil && !e.storedUntil.IsZero() && e.expires.Sub(e.storedUntil) >= c.ttl/4 {
			e.storedUntil = e.expires
			extendTo = e.expires
		}
		v := e.result
		c.mu.Unlock()
		if !extendTo.IsZero() {
			c.storeCall(ctx, "extend", func(ctx context.Context) error { return c.store.ExtendResolve(ctx, key, extendTo) })
		}
		return v, true
	}
	if ok {
		delete(c.entries, key)
	}
	c.mu.Unlock()
	if c.store == nil {
		return nil, false
	}
	var row *StoredResolve
	c.storeCall(ctx, "load", func(ctx context.Context) (err error) {
		row, err = c.store.LoadResolve(ctx, key, now)
		return err
	})
	if row == nil || !now.Before(row.ExpiresAt) {
		return nil, false
	}
	var v ResolveResult
	if json.Unmarshal(row.Result, &v) != nil || len(v.Streams) == 0 {
		return nil, false
	}
	exp := maxTime(row.ExpiresAt, slideExpiry(now, c.ttl, row.MaxExpiresAt))
	if exp.After(row.ExpiresAt) {
		c.storeCall(ctx, "extend", func(ctx context.Context) error { return c.store.ExtendResolve(ctx, key, exp) })
	}
	c.setL1(key, &resolveEntry{result: &v, expires: exp, maxExpires: row.MaxExpiresAt, storedUntil: exp}, now)
	return &v, true
}

// put caches a freshly resolved result in both levels (when it is not
// already past its cap).
func (c *resolveCache) put(ctx context.Context, key string, v *ResolveResult) {
	if v == nil {
		return
	}
	now := c.now()
	hardCap := resolveHardCap(v, now, c.maxAge)
	exp := slideExpiry(now, c.ttl, hardCap)
	if !exp.After(now) {
		return
	}
	e := &resolveEntry{result: v, expires: exp, maxExpires: hardCap}
	if c.store != nil && storable(v) {
		if b, err := json.Marshal(v); err == nil && len(b) <= MaxStoredResolveBytes {
			e.storedUntil = exp
			c.storeCall(ctx, "save", func(ctx context.Context) error {
				return c.store.SaveResolve(ctx, key, StoredResolve{Result: b, ExpiresAt: exp, MaxExpiresAt: hardCap, RefreshedAt: now})
			})
		}
	}
	c.setL1(key, e, now)
	c.maybePurge(now)
}

// evict drops key from both levels.
func (c *resolveCache) evict(ctx context.Context, key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
	if c.store != nil {
		c.storeCall(ctx, "delete", func(ctx context.Context) error { return c.store.DeleteResolve(ctx, key) })
	}
}

// allowRefresh reports whether key may be force-refreshed now, and records
// the refresh when it may.
func (c *resolveCache) allowRefresh(key string) bool {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.refreshes[key]; ok && now.Sub(last) < c.refreshEvery {
		return false
	}
	if len(c.refreshes) >= maxResolveEntries {
		for k, at := range c.refreshes {
			if now.Sub(at) >= c.refreshEvery {
				delete(c.refreshes, k)
			}
		}
		if len(c.refreshes) >= maxResolveEntries {
			return false // flooded: refuse rather than grow without bound
		}
	}
	c.refreshes[key] = now
	return true
}

func (c *resolveCache) setL1(key string, e *resolveEntry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxResolveEntries {
		for k, v := range c.entries {
			if !now.Before(v.expires) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= maxResolveEntries {
			c.entries = map[string]*resolveEntry{}
		}
	}
	c.entries[key] = e
}

// maybePurge deletes expired L2 rows at most every resolvePurgeEvery, in the
// background.
func (c *resolveCache) maybePurge(now time.Time) {
	if c.store == nil {
		return
	}
	c.mu.Lock()
	if c.purging || now.Sub(c.lastPurge) < resolvePurgeEvery {
		c.mu.Unlock()
		return
	}
	c.purging, c.lastPurge = true, now
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			c.purging = false
			c.mu.Unlock()
		}()
		c.storeCall(context.Background(), "purge", func(ctx context.Context) error {
			_, err := c.store.PurgeResolves(ctx, now)
			return err
		})
	}()
}

// storeCall runs one L2 operation with a short timeout; L2 errors only log,
// the in-process cache keeps working.
func (c *resolveCache) storeCall(ctx context.Context, op string, f func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolveStoreTimeout)
	defer cancel()
	if err := f(ctx); err != nil {
		slog.Warn("media resolve cache store", "op", op, "err", err)
	}
}

// storable is false for results that carry credentials in request headers:
// those never leave the process.
func storable(v *ResolveResult) bool {
	for _, list := range [][]Stream{v.Streams, v.Subtitles} {
		for _, st := range list {
			for h := range st.Headers {
				switch strings.ToLower(h) {
				case "cookie", "authorization", "proxy-authorization", "x-api-key":
					return false
				}
			}
		}
	}
	return true
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
