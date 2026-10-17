package community

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// Posting limits (PRD 5.5: new accounts at most 3 danmaku per minute;
// tech plan 3.8: the same check guards reviews).
const (
	NewPosterAge       = 3 * 24 * time.Hour
	NewPosterPerMinute = 3
	DanmakuPerMinute   = 20
	ReviewPerMinute    = 5
	CommentPerMinute   = 5
	// ClipPerMinute bounds 片段 creation per user (new accounts: NewPosterPerMinute).
	ClipPerMinute = 5
	// ReactionPerMinute bounds 赞 / 踩 toggles per voter (users and devices alike).
	ReactionPerMinute  = 30
	PostingLimitWindow = time.Minute
	// DefaultLimiterMaxKeys caps the keys a MemoryLimiter tracks. A key
	// holds at most `limit` timestamps (8 bytes each) plus ~250 bytes of
	// bookkeeping: ~120 MB if every key sat at the largest limit in use
	// (120), a few MB for realistic traffic.
	DefaultLimiterMaxKeys = 100_000
	// maxLimiterKeyLen is the longest key stored verbatim; longer keys
	// (built from caller-supplied values) are stored as their SHA-256, so
	// every key costs a bounded number of bytes.
	maxLimiterKeyLen = 128
)

// PostingLimit is the per-minute limit for a poster of the given account
// age; kind is "danmaku", "review", "comment" or "clip".
func PostingLimit(kind string, accountAge time.Duration) int {
	if accountAge < NewPosterAge {
		return NewPosterPerMinute
	}
	switch kind {
	case "review":
		return ReviewPerMinute
	case "comment":
		return CommentPerMinute
	case "clip":
		return ClipPerMinute
	}
	return DanmakuPerMinute
}

// Limiter decides whether one more event for key fits in limit per window.
// A true result consumes one slot. Implementations: MemoryLimiter (one API
// process); a shared store (Redis) can replace it behind this interface when
// the API runs on several hosts.
type Limiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// MemoryLimiter is a sliding-window log limiter kept in process memory.
//
// It is bounded, whatever the rate of new keys (anonymous devices, client
// addresses):
//   - a key is dropped once its window has passed since its last hit.
//     Keys are kept in one most-recently-used-first list per window length,
//     so the expired ones collect at the backs of the lists and every
//     Allow drops them in amortised O(1): no periodic full sweep, and a
//     key is never dropped while it still has hits inside its own window;
//   - it tracks at most maxKeys keys. A new key past the cap evicts the
//     least recently used key, which forgets that key's hits (a looser
//     limit for it under a flood of new keys, never a refusal for anyone
//     else); a key being refused counts as used, so it is evicted last;
//   - keys longer than maxLimiterKeyLen are stored hashed.
type MemoryLimiter struct {
	mu       sync.Mutex
	keys     map[string]*limiterEntry
	byWindow map[time.Duration]*list.List // of *limiterEntry, most recently used first
	maxKeys  int
	evicted  uint64
	now      func() time.Time
}

type limiterEntry struct {
	key    string
	window time.Duration
	hits   []int64 // UnixNano, ascending; at most the key's limit
	used   int64   // last Allow, UnixNano (eviction order)
	elem   *list.Element
}

// NewMemoryLimiter returns an empty limiter using the wall clock, capped at
// DefaultLimiterMaxKeys keys.
func NewMemoryLimiter() *MemoryLimiter { return NewBoundedMemoryLimiter(DefaultLimiterMaxKeys) }

// NewBoundedMemoryLimiter returns an empty limiter tracking at most maxKeys
// keys (DefaultLimiterMaxKeys when maxKeys <= 0).
func NewBoundedMemoryLimiter(maxKeys int) *MemoryLimiter {
	if maxKeys <= 0 {
		maxKeys = DefaultLimiterMaxKeys
	}
	return &MemoryLimiter{keys: map[string]*limiterEntry{}, byWindow: map[time.Duration]*list.List{}, maxKeys: maxKeys, now: time.Now}
}

// SetClock replaces the clock (tests).
func (l *MemoryLimiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Len is the number of keys tracked.
func (l *MemoryLimiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.keys)
}

// Evicted is the number of keys dropped, still inside their window,
// because the cap was reached.
func (l *MemoryLimiter) Evicted() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.evicted
}

// limiterKey bounds the bytes a key costs.
func limiterKey(key string) string {
	if len(key) <= maxLimiterKeyLen {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Allow implements Limiter.
func (l *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	key = limiterKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now().UnixNano()
	l.expire(now)
	e := l.keys[key]
	switch {
	case e == nil:
		for len(l.keys) >= l.maxKeys && l.evictOldest() {
			l.evicted++
		}
		e = &limiterEntry{key: key, window: window}
		e.elem = l.list(window).PushFront(e)
		l.keys[key] = e
	case e.window != window: // one key, another window: move it
		l.unlink(e)
		e.window = window
		e.elem = l.list(window).PushFront(e)
	}
	// Any use, refused or not, makes the key most recently used, so a key
	// that is being throttled is the last to be evicted.
	l.byWindow[window].MoveToFront(e.elem)
	e.used = now
	e.hits = prune(e.hits, now-int64(window))
	if len(e.hits) >= limit {
		return false, nil
	}
	e.hits = append(e.hits, now)
	return true, nil
}

func (l *MemoryLimiter) list(window time.Duration) *list.List {
	ls := l.byWindow[window]
	if ls == nil {
		ls = list.New()
		l.byWindow[window] = ls
	}
	return ls
}

// expire drops every key whose window has passed since its last hit.
func (l *MemoryLimiter) expire(now int64) {
	for w, ls := range l.byWindow {
		for b := ls.Back(); b != nil; b = ls.Back() {
			e := b.Value.(*limiterEntry)
			if lastHit(e) > now-int64(w) {
				break
			}
			l.remove(e)
		}
	}
}

// evictOldest drops the least recently used key (the backs of the window
// lists); false when there is none.
func (l *MemoryLimiter) evictOldest() bool {
	var oldest *limiterEntry
	for _, ls := range l.byWindow {
		if b := ls.Back(); b != nil {
			if e := b.Value.(*limiterEntry); oldest == nil || e.used < oldest.used {
				oldest = e
			}
		}
	}
	if oldest == nil {
		return false
	}
	l.remove(oldest)
	return true
}

func (l *MemoryLimiter) remove(e *limiterEntry) {
	l.unlink(e)
	delete(l.keys, e.key)
}

// unlink takes e off its window list, dropping the list once empty.
func (l *MemoryLimiter) unlink(e *limiterEntry) {
	ls := l.byWindow[e.window]
	ls.Remove(e.elem)
	if ls.Len() == 0 {
		delete(l.byWindow, e.window)
	}
}

func lastHit(e *limiterEntry) int64 {
	if len(e.hits) == 0 {
		return 0
	}
	return e.hits[len(e.hits)-1]
}

// prune drops timestamps at or before cut (hits are in time order).
func prune(ts []int64, cut int64) []int64 {
	i := 0
	for i < len(ts) && ts[i] <= cut {
		i++
	}
	return ts[i:]
}
