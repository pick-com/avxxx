package community

import (
	"context"
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
	// ReactionPerMinute bounds 赞 / 踩 toggles per voter (users and devices alike).
	ReactionPerMinute   = 30
	PostingLimitWindow  = time.Minute
	limiterSweepEvery   = 5 * time.Minute
	limiterMaxKeysSweep = 10000
)

// PostingLimit is the per-minute limit for a poster of the given account
// age; kind is "danmaku", "review" or "comment".
func PostingLimit(kind string, accountAge time.Duration) int {
	if accountAge < NewPosterAge {
		return NewPosterPerMinute
	}
	switch kind {
	case "review":
		return ReviewPerMinute
	case "comment":
		return CommentPerMinute
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
type MemoryLimiter struct {
	mu        sync.Mutex
	hits      map[string][]time.Time
	now       func() time.Time
	lastSweep time.Time
}

// NewMemoryLimiter returns an empty limiter using the wall clock.
func NewMemoryLimiter() *MemoryLimiter {
	return &MemoryLimiter{hits: map[string][]time.Time{}, now: time.Now}
}

// SetClock replaces the clock (tests).
func (l *MemoryLimiter) SetClock(now func() time.Time) {
	l.mu.Lock()
	l.now = now
	l.mu.Unlock()
}

// Allow implements Limiter.
func (l *MemoryLimiter) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	cut := now.Add(-window)
	kept := prune(l.hits[key], cut)
	if len(kept) >= limit {
		l.hits[key] = kept
		return false, nil
	}
	l.hits[key] = append(kept, now)
	if now.Sub(l.lastSweep) > limiterSweepEvery {
		l.sweep(now.Add(-max(window, limiterSweepEvery)))
		l.lastSweep = now
	}
	return true, nil
}

// prune drops timestamps at or before cut (hits are in time order).
func prune(ts []time.Time, cut time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// sweep forgets keys idle since before cut so memory stays bounded.
func (l *MemoryLimiter) sweep(cut time.Time) {
	n := 0
	for k, ts := range l.hits {
		if len(ts) == 0 || !ts[len(ts)-1].After(cut) {
			delete(l.hits, k)
		}
		if n++; n > limiterMaxKeysSweep {
			return
		}
	}
}
