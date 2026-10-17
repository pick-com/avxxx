package community

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// A flood of new keys (anonymous devices, addresses) never grows the
// limiter past its cap, and keys that are still limiting survive while
// older idle ones are evicted first.
func TestMemoryLimiterBoundedUnderNewKeys(t *testing.T) {
	const max = 1000
	l := NewBoundedMemoryLimiter(max)
	now := time.Unix(1_700_000_000, 0)
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()

	// A hot key at its limit.
	for range 3 {
		l.Allow(ctx, "hot", 3, 10*time.Minute)
	}
	for i := range 50 * max {
		now = now.Add(time.Millisecond)
		if ok, _ := l.Allow(ctx, fmt.Sprintf("reaction:dev-%d", i), 30, time.Minute); !ok {
			t.Fatalf("new key %d refused", i)
		}
		if i%997 == 0 { // the hot key keeps being used and stays limited
			if ok, _ := l.Allow(ctx, "hot", 3, 10*time.Minute); ok {
				t.Fatalf("hot key forgot its hits after %d new keys", i)
			}
		}
		if n := l.Len(); n > max {
			t.Fatalf("after %d new keys Len = %d > cap %d", i+1, n, max)
		}
	}
	if l.Evicted() == 0 {
		t.Error("expected evictions past the cap")
	}
	// All of it expires once the windows pass.
	now = now.Add(11 * time.Minute)
	l.Allow(ctx, "last", 1, time.Minute)
	if n := l.Len(); n != 1 {
		t.Errorf("after the windows passed Len = %d, want 1", n)
	}
}

// Without hitting the cap, idle keys are dropped as they expire, so the
// map follows the live key count rather than everything ever seen.
func TestMemoryLimiterExpiresProportionally(t *testing.T) {
	l := NewMemoryLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()
	// 100 new keys per second with a one-minute window: at most ~6000 live.
	for i := range 30_000 {
		now = now.Add(10 * time.Millisecond)
		l.Allow(ctx, fmt.Sprintf("anon-vote:%d", i), 60, time.Minute)
		if n := l.Len(); n > 6001 {
			t.Fatalf("Len = %d after %d keys, want <= 6001 live keys", n, i+1)
		}
	}
	if l.Evicted() != 0 {
		t.Errorf("evicted %d keys below the cap", l.Evicted())
	}
}

// A long-window key is not dropped early by traffic on short windows (the
// old sweep cut at max(window of the triggering call, 5 min), forgetting a
// 10-minute key after 5).
func TestMemoryLimiterKeepsLongWindows(t *testing.T) {
	l := NewMemoryLimiter()
	now := time.Unix(1_700_000_000, 0)
	l.SetClock(func() time.Time { return now })
	ctx := context.Background()
	for range 2 {
		l.Allow(ctx, "media-refresh:1.2.3.4", 2, 10*time.Minute)
	}
	now = now.Add(7 * time.Minute)
	for i := range 100 {
		l.Allow(ctx, fmt.Sprint("comment:", i), 5, time.Minute)
	}
	if ok, _ := l.Allow(ctx, "media-refresh:1.2.3.4", 2, 10*time.Minute); ok {
		t.Fatal("10-minute window forgotten after 7 minutes")
	}
	now = now.Add(3*time.Minute + time.Second)
	if ok, _ := l.Allow(ctx, "media-refresh:1.2.3.4", 2, 10*time.Minute); !ok {
		t.Fatal("window passed: should pass")
	}
}

// Over-long keys cost a fixed number of bytes and still limit.
func TestMemoryLimiterHashesLongKeys(t *testing.T) {
	l := NewMemoryLimiter()
	ctx := context.Background()
	long := "login-email:" + strings.Repeat("a", 10_000)
	for range 2 {
		l.Allow(ctx, long, 2, time.Minute)
	}
	if ok, _ := l.Allow(ctx, long, 2, time.Minute); ok {
		t.Fatal("hashed key must still be limited")
	}
	if ok, _ := l.Allow(ctx, long+"b", 2, time.Minute); !ok {
		t.Fatal("a different long key is independent")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.keys {
		if len(k) > maxLimiterKeyLen {
			t.Errorf("stored key of %d bytes", len(k))
		}
	}
}

func TestMemoryLimiterConcurrent(t *testing.T) {
	l := NewBoundedMemoryLimiter(500)
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 2000 {
				l.Allow(ctx, fmt.Sprintf("k%d-%d", g, i), 5, time.Duration(1+i%3)*time.Minute)
				l.Allow(ctx, "shared", 1_000_000, time.Minute)
			}
		}()
	}
	wg.Wait()
	if n := l.Len(); n > 500 {
		t.Errorf("Len = %d > cap", n)
	}
}
