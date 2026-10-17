package api

import (
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCachedJSONBuildsOncePerKey(t *testing.T) {
	srv := &Server{homeCache: newRespCache()}
	var builds atomic.Int32
	build := func() (any, error) {
		builds.Add(1)
		time.Sleep(30 * time.Millisecond)
		return map[string]int{"n": 1}, nil
	}
	var wg sync.WaitGroup
	bodies := make([]string, 20)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			srv.cachedJSON(rec, "k", time.Minute, "public, max-age=1", build)
			bodies[i] = rec.Body.String()
		}()
	}
	wg.Wait()
	if n := builds.Load(); n != 1 {
		t.Fatalf("20 concurrent misses built %d times", n)
	}
	for _, b := range bodies {
		if b != bodies[0] || b == "" {
			t.Fatalf("bodies differ: %q vs %q", b, bodies[0])
		}
	}
	if len(srv.homeCache.building) != 0 {
		t.Errorf("build locks left behind: %v", srv.homeCache.building)
	}

	// Errors are not cached: the next request builds again.
	boom := errors.New("boom")
	rec := httptest.NewRecorder()
	srv.cachedJSON(rec, "e", time.Minute, "", func() (any, error) { return nil, boom })
	if rec.Code < 500 {
		t.Fatalf("error status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.cachedJSON(rec, "e", time.Minute, "", func() (any, error) { return 2, nil })
	if rec.Body.String() != "2\n" {
		t.Fatalf("retry after error: %q", rec.Body.String())
	}

	// A nil cache builds every time.
	var nilSrv Server
	n := 0
	for range 2 {
		nilSrv.cachedJSON(httptest.NewRecorder(), "k", time.Minute, "", func() (any, error) { n++; return n, nil })
	}
	if n != 2 {
		t.Errorf("nil cache built %d times", n)
	}
}
