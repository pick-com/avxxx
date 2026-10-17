package store

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStaleCacheSingleLoad(t *testing.T) {
	var c staleCache[int]
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var loads atomic.Int32
	release := make(chan struct{})
	slowLoad := func() (int, error) {
		n := loads.Add(1)
		<-release
		return int(n) * 10, nil
	}

	// First load: 20 concurrent callers share one load and all get its value.
	var wg sync.WaitGroup
	got := make([]int, 20)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.get(time.Minute, clock, slowLoad)
			if err != nil {
				t.Error(err)
			}
			got[i] = v
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("first load ran %d times", loads.Load())
	}
	for _, v := range got {
		if v != 10 {
			t.Fatalf("first load values %v", got)
		}
	}

	// Fresh: no load.
	if v, _ := c.get(time.Minute, clock, slowLoad); v != 10 || loads.Load() != 1 {
		t.Fatalf("fresh get = %d, loads %d", v, loads.Load())
	}

	// Expired: one caller reloads; the others get the stale value at once.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	release = make(chan struct{})
	done := make(chan int)
	go func() {
		v, _ := c.get(time.Minute, clock, slowLoad)
		done <- v
	}()
	for loads.Load() != 2 {
		time.Sleep(time.Millisecond)
	}
	for range 10 {
		if v, err := c.get(time.Minute, clock, slowLoad); v != 10 || err != nil {
			t.Fatalf("during reload got %d %v, want stale 10", v, err)
		}
	}
	close(release)
	if v := <-done; v != 20 {
		t.Fatalf("reload returned %d", v)
	}
	if v, _ := c.get(time.Minute, clock, slowLoad); v != 20 || loads.Load() != 2 {
		t.Fatalf("after reload %d, loads %d", v, loads.Load())
	}

	// A failed reload returns its error and keeps the old value for others.
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	boom := errors.New("boom")
	if _, err := c.get(time.Minute, clock, func() (int, error) { return 0, boom }); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
	if v, err := c.get(time.Minute, clock, func() (int, error) { return 30, nil }); v != 30 || err != nil {
		t.Fatalf("retry after failure %d %v", v, err)
	}
}
