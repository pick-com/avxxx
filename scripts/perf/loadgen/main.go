// Command loadgen is the small HTTP load tool behind docs/PERFORMANCE.md: it
// replays a list of named GET endpoints against an API and prints p50 / p95 /
// p99 latency per endpoint, optionally sampling a process's RSS meanwhile.
//
//	go run ./scripts/perf/loadgen -base http://127.0.0.1:38181 -n 200 -c 8 \
//	  -token "$TOKEN" -pid "$API_PID" -endpoints scripts/perf/endpoints.txt \
//	  -releases 5000 -performers 1000   # the volumes setup.sh seeded
//
// Each endpoints line is "name<TAB or spaces>path"; '#' starts a comment.
// Placeholders are filled per request with a skewed random index, the same
// shape scripts/perf/seed.sql generates:
//
//	{r}    release slug  perf-r-N   (hot releases more often)
//	{p}    performer slug perf-p-N
//	{tag}  tagN
//	{code} a dvd code like abc-012
//	{q}    a title word
//
// A line whose name starts with "auth:" sends Authorization: Bearer -token.
// -header adds a header to every request (repeatable), e.g. the age-gate
// cookie when -base is the SSR web server:
//
//	go run ./scripts/perf/loadgen -base http://127.0.0.1:3000 \
//	  -endpoints scripts/perf/ssr-endpoints.txt -header "Cookie: ag=v1.ok...."
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type endpoint struct{ name, path string }

// headerFlags collects repeated -header "Name: value" flags.
type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ", ") }
func (h *headerFlags) Set(v string) error {
	if name, _, ok := strings.Cut(v, ":"); !ok || strings.TrimSpace(name) == "" {
		return fmt.Errorf("header %q: want \"Name: value\"", v)
	}
	*h = append(*h, v)
	return nil
}

func main() {
	base := flag.String("base", "http://127.0.0.1:38181", "API base URL")
	file := flag.String("endpoints", "scripts/perf/endpoints.txt", "endpoint list")
	only := flag.String("only", "", "comma-separated endpoint names to run (default all)")
	n := flag.Int("n", 200, "requests per endpoint")
	c := flag.Int("c", 8, "concurrent workers")
	warm := flag.Int("warm", 10, "unmeasured warm-up requests per endpoint")
	token := flag.String("token", "", "bearer token for auth: endpoints")
	pid := flag.Int("pid", 0, "sample this process's RSS while running (0 = off)")
	releases := flag.Int("releases", 5000, "seeded releases (PERF_RELEASES of setup.sh)")
	performers := flag.Int("performers", 1000, "seeded performers (PERF_PERFORMERS of setup.sh)")
	var headers headerFlags
	flag.Var(&headers, "header", `extra request header "Name: value" (repeatable)`)
	flag.Parse()

	eps, err := readEndpoints(*file, *only)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	g := gen{releases: *releases, performers: *performers}
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: *c * 2}}

	var peakRSS atomic.Int64
	stop := make(chan struct{})
	if *pid > 0 {
		go sampleRSS(*pid, &peakRSS, stop)
	}
	fmt.Printf("%-28s %6s %8s %8s %8s %8s %6s\n", "endpoint", "n", "p50ms", "p95ms", "p99ms", "maxms", "errs")
	for _, ep := range eps {
		for range *warm {
			do(client, *base, ep, g, *token, headers)
		}
		lat := make([]float64, 0, *n)
		var mu sync.Mutex
		var errs atomic.Int64
		jobs := make(chan struct{})
		var wg sync.WaitGroup
		for range *c {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range jobs {
					d, ok := do(client, *base, ep, g, *token, headers)
					if !ok {
						errs.Add(1)
					}
					mu.Lock()
					lat = append(lat, d)
					mu.Unlock()
				}
			}()
		}
		for range *n {
			jobs <- struct{}{}
		}
		close(jobs)
		wg.Wait()
		slices.Sort(lat)
		fmt.Printf("%-28s %6d %8.1f %8.1f %8.1f %8.1f %6d\n", ep.name, len(lat), pct(lat, 50), pct(lat, 95), pct(lat, 99), lat[len(lat)-1], errs.Load())
	}
	close(stop)
	if *pid > 0 {
		time.Sleep(50 * time.Millisecond)
		fmt.Printf("peak RSS %d MiB (rss now %d MiB)\n", peakRSS.Load()/1024, rss(*pid)/1024)
	}
}

func readEndpoints(path, only string) ([]endpoint, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	want := map[string]bool{}
	for _, s := range strings.Split(only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			want[s] = true
		}
	}
	var out []endpoint
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fs := strings.Fields(line)
		if len(fs) != 2 {
			return nil, fmt.Errorf("bad endpoint line %q", line)
		}
		if len(want) > 0 && !want[fs[0]] {
			continue
		}
		out = append(out, endpoint{fs[0], fs[1]})
	}
	return out, sc.Err()
}

type gen struct{ releases, performers int }

// skew returns 1..n with index n*u^k (k=3: the seed's watch popularity).
func skew(n int, k float64) int { return 1 + int(float64(n)*math.Pow(rand.Float64(), k))%n }

func (g gen) fill(p string) string {
	r := strings.NewReplacer(
		"{r}", "perf-r-"+strconv.Itoa(skew(g.releases, 3)),
		"{p}", "perf-p-"+strconv.Itoa(skew(g.performers, 2)),
		"{tag}", "tag"+strconv.Itoa(skew(400, 2)-1),
		"{code}", code(skew(g.releases, 1)),
		"{q}", []string{"summer", "office", "campus", "night", "secret", "lesson", "diary"}[rand.IntN(7)],
	)
	return r.Replace(p)
}

// code mirrors seed.sql: prefix from g%400, number g/400+1.
func code(g int) string {
	x := g % 400
	return fmt.Sprintf("%c%c%c-%03d", 'a'+x%26, 'a'+(x/26)%26, 'a'+(x*7)%26, g/400+1)
}

func do(c *http.Client, base string, ep endpoint, g gen, token string, headers []string) (float64, bool) {
	req, _ := http.NewRequest(http.MethodGet, base+g.fill(ep.path), nil)
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ":")
		req.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	if strings.HasPrefix(ep.name, "auth:") && token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept-Language", "zh")
	t := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return float64(time.Since(t).Microseconds()) / 1000, false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return float64(time.Since(t).Microseconds()) / 1000, resp.StatusCode < 400 || resp.StatusCode == 404
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	return sorted[max(0, min(i, len(sorted)-1))]
}

// rss returns the resident set size of pid in KiB (ps works on macOS and Linux).
func rss(pid int) int64 {
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return v
}

func sampleRSS(pid int, peak *atomic.Int64, stop <-chan struct{}) {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if v := rss(pid); v > peak.Load() {
				peak.Store(v)
			}
		}
	}
}
