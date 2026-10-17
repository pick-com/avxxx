package store

import (
	"sync"
	"time"
)

// staleCache holds one value that is reloaded at most every ttl. Once it is
// older than ttl, a single caller reloads it while concurrent callers keep
// getting the previous value; before the first load, concurrent callers
// wait for one load instead of each running it. Without this, every request
// arriving while an expensive load (RatingPrior scans every rating: ~170 ms
// at 1M ratings) was in flight ran the same load.
type staleCache[T any] struct {
	mu      sync.Mutex
	val     T
	at      time.Time
	loading sync.Mutex
}

func (c *staleCache[T]) get(ttl time.Duration, now func() time.Time, load func() (T, error)) (T, error) {
	fresh := func() (T, time.Time, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.val, c.at, !c.at.IsZero() && now().Sub(c.at) < ttl
	}
	v, at, ok := fresh()
	if ok {
		return v, nil
	}
	if at.IsZero() {
		c.loading.Lock()
	} else if !c.loading.TryLock() {
		return v, nil // another caller is reloading: serve the previous value
	}
	defer c.loading.Unlock()
	if v, _, ok = fresh(); ok {
		return v, nil // loaded while we waited
	}
	nv, err := load()
	if err != nil {
		var zero T
		return zero, err
	}
	c.mu.Lock()
	c.val, c.at = nv, now()
	c.mu.Unlock()
	return nv, nil
}
