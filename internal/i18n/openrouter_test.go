package i18n

import (
	"avxxx/internal/obs"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testKey is distinctive so any leak is easy to spot. It is not a real key;
// no test talks to openrouter.ai.
const testKey = "sk-or-v1-TESTKEY-LEAKCANARY-0123456789abcdef"

// fakeOpenRouter is an httptest OpenRouter: handler answers each request;
// it records every request body and header.
type fakeOpenRouter struct {
	srv      *httptest.Server
	mu       sync.Mutex
	handler  func(w http.ResponseWriter, r *http.Request, req chatRequest, n int)
	bodies   []chatRequest
	headers  []http.Header
	inFlight atomic.Int32
	maxIn    atomic.Int32
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	f := &fakeOpenRouter{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.inFlight.Add(1)
		defer f.inFlight.Add(-1)
		for {
			m := f.maxIn.Load()
			if n <= m || f.maxIn.CompareAndSwap(m, n) {
				break
			}
		}
		var req chatRequest
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Errorf("request body: %v", err)
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, req)
		f.headers = append(f.headers, r.Header.Clone())
		cnt := len(f.bodies)
		h := f.handler
		f.mu.Unlock()
		h(w, r, req, cnt)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenRouter) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.bodies)
}

// reply writes a chat completion whose content is content.
func reply(w http.ResponseWriter, model, content string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"model": model,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}, "finish_reason": "stop"}}})
}

func translations(ts ...string) string {
	b, _ := json.Marshal(map[string]any{"translations": ts})
	return string(b)
}

// userItems decodes the items of the user prompt.
func userItems(t *testing.T, req chatRequest) (items []string, glossary []promptGlossary) {
	var p struct {
		Items    []string         `json:"items"`
		Glossary []promptGlossary `json:"glossary"`
	}
	if err := json.Unmarshal([]byte(req.Messages[1].Content), &p); err != nil {
		t.Fatalf("user prompt is not JSON: %v", err)
	}
	return p.Items, p.Glossary
}

type sleepLog struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *sleepLog) sleep(_ context.Context, d time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.d = append(s.d, d)
	return nil
}

func newTestTranslator(t *testing.T, f *fakeOpenRouter, mod func(*OpenRouterConfig)) (*OpenRouter, *MemoryBudget, *sleepLog) {
	t.Helper()
	cfg := OpenRouterConfig{APIKey: testKey, Models: []string{"m/one:free", "m/two:free"}, Referer: "https://avxxx.test", Title: "avxxx",
		URL: f.srv.URL + "/api/v1/chat/completions", Timeout: 5 * time.Second}
	if mod != nil {
		mod(&cfg)
	}
	b := &MemoryBudget{}
	o, err := NewOpenRouter(cfg, b, f.srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	sl := &sleepLog{}
	o.Sleep = sl.sleep
	o.Now = func() time.Time { return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC) }
	return o, b, sl
}

func TestOpenRouterBatchAndGlossary(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		items, _ := userItems(t, req)
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = "ZH:" + it
		}
		reply(w, req.Model, translations(out...))
	}
	o, b, _ := newTestTranslator(t, f, nil)
	glossary := []GlossaryTerm{
		{Term: "素人", Lang: ZhCN, Translation: "素人"},
		{Term: "人妻", Lang: ZhCN, Translation: "人妻"},
		{Term: "人妻温泉", Lang: ZhCN, Translation: "人妻温泉旅行"},
		{Term: "人妻", Lang: En, Translation: "Married Woman"}, // other language
		{Term: "巨乳", Lang: ZhCN, Translation: "巨乳"},          // not in the batch
	}
	res, err := o.TranslateBatch(context.Background(), Batch{To: ZhCN, Texts: []string{"人妻温泉 ABP-123", "素人の夏"}, Glossary: glossary})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "m/one:free" || len(res.Texts) != 2 || res.Texts[0] != "ZH:人妻温泉 ABP-123" || res.Texts[1] != "ZH:素人の夏" {
		t.Fatalf("result = %+v", res)
	}
	if f.requests() != 1 || b.Used(o.Now()) != 1 {
		t.Fatalf("requests = %d, budget used = %d", f.requests(), b.Used(o.Now()))
	}
	h := f.headers[0]
	if h.Get("Authorization") != "Bearer "+testKey || h.Get("HTTP-Referer") != "https://avxxx.test" || h.Get("X-Title") != "avxxx" {
		t.Fatalf("headers = %v", h)
	}
	req := f.bodies[0]
	if req.Model != "m/one:free" || req.ResponseFormat["type"] != "json_object" || len(req.Messages) != 2 {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(req.Messages[0].Content, "Simplified Chinese") || !strings.Contains(req.Messages[0].Content, "glossary") {
		t.Fatalf("system prompt = %q", req.Messages[0].Content)
	}
	_, gl := userItems(t, req)
	want := []promptGlossary{{"人妻温泉", "人妻温泉旅行"}, {"素人", "素人"}, {"人妻", "人妻"}}
	if fmt.Sprint(gl) != fmt.Sprint(want) {
		t.Fatalf("glossary sent = %v, want only the zh-CN terms in the batch, longest first: %v", gl, want)
	}
}

func TestOpenRouterInvalidOutputRetriedOnce(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, n int) {
		if n == 1 {
			reply(w, req.Model, translations("only one")) // two items asked
			return
		}
		reply(w, req.Model, "```json\n"+translations("a", "b")+"\n```")
	}
	o, _, _ := newTestTranslator(t, f, nil)
	res, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x", "y"}})
	if err != nil || fmt.Sprint(res.Texts) != "[a b]" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if f.requests() != 2 || len(f.bodies[1].Messages) != 3 || !strings.Contains(f.bodies[1].Messages[2].Content, "rejected") {
		t.Fatalf("retry request = %+v", f.bodies)
	}

	f2 := newFakeOpenRouter(t)
	f2.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		reply(w, req.Model, "Sorry, I can't help with that.")
	}
	o2, _, _ := newTestTranslator(t, f2, nil)
	if _, err := o2.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}}); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("err = %v, want ErrInvalidOutput", err)
	}
	if f2.requests() != 2 {
		t.Fatalf("invalid output: %d requests, want exactly one retry", f2.requests())
	}
}

func TestOpenRouterPlaceholderValidation(t *testing.T) {
	src := []string{"Blocked words {n}/{max}", "At most {n, plural, one {# blocked word} other {# blocked words}}"}
	f := newFakeOpenRouter(t)
	var good atomic.Bool
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		if good.Load() {
			reply(w, req.Model, translations("屏蔽词 {n}/{max}", "最多 {n, plural, other {# 个屏蔽词}}"))
			return
		}
		reply(w, req.Model, translations("屏蔽词 {count}/{max}", "最多 # 个屏蔽词"))
	}
	o, _, _ := newTestTranslator(t, f, nil)
	_, err := o.TranslateBatch(context.Background(), Batch{To: ZhCN, Texts: src, UI: true})
	if !errors.Is(err, ErrInvalidOutput) || !strings.Contains(err.Error(), "placeholders") {
		t.Fatalf("err = %v, want a placeholder rejection", err)
	}
	if !strings.Contains(f.bodies[0].Messages[0].Content, "user-interface") {
		t.Fatal("UI batches should say so in the prompt")
	}
	good.Store(true)
	res, err := o.TranslateBatch(context.Background(), Batch{To: ZhCN, Texts: src, UI: true})
	if err != nil || len(res.Texts) != 2 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestOpenRouter429Backoff(t *testing.T) {
	// Retry-After honoured, then success on the same model.
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, n int) {
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			http.Error(w, `{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-min"}}`, http.StatusTooManyRequests)
			return
		}
		reply(w, req.Model, translations("ok"))
	}
	o, b, sl := newTestTranslator(t, f, nil)
	res, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if err != nil || res.Model != "m/one:free" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if fmt.Sprint(sl.d) != "[7s]" || b.Used(o.Now()) != 2 {
		t.Fatalf("sleeps = %v, budget used = %d", sl.d, b.Used(o.Now()))
	}

	// No Retry-After: exponential backoff per model, then the next model,
	// then ErrRateLimited. A 200 body carrying error 429 counts as a 429.
	f2 := newFakeOpenRouter(t)
	f2.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, n int) {
		if req.Model == "m/two:free" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"error":{"code":429,"message":"upstream rate limited"}}`)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}
	o2, _, sl2 := newTestTranslator(t, f2, func(c *OpenRouterConfig) { c.MaxRetries = 2 })
	_, err = o2.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if fmt.Sprint(sl2.d) != "[2s 4s 2s 4s]" || f2.requests() != 6 {
		t.Fatalf("sleeps = %v, requests = %d", sl2.d, f2.requests())
	}

	// A Retry-After beyond MaxRetryWait moves on without sleeping.
	f3 := newFakeOpenRouter(t)
	f3.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		if req.Model == "m/one:free" {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		reply(w, req.Model, translations("ok"))
	}
	o3, _, sl3 := newTestTranslator(t, f3, nil)
	res, err = o3.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if err != nil || res.Model != "m/two:free" || len(sl3.d) != 0 {
		t.Fatalf("res = %+v, err = %v, sleeps = %v", res, err, sl3.d)
	}
}

func TestOpenRouterDailyCap(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		reply(w, req.Model, translations("ok"))
	}
	o, b, _ := newTestTranslator(t, f, func(c *OpenRouterConfig) { c.DailyCap = 2 })
	for i := 0; i < 2; i++ {
		if _, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if !errors.Is(err, ErrDailyCap) {
		t.Fatalf("err = %v, want ErrDailyCap", err)
	}
	if f.requests() != 2 || b.Used(o.Now()) != 2 {
		t.Fatalf("requests = %d, used = %d: the cap must stop before the HTTP call", f.requests(), b.Used(o.Now()))
	}
	// A new UTC day has a fresh budget.
	o.Now = func() time.Time { return time.Date(2026, 10, 17, 0, 0, 1, 0, time.UTC) }
	if _, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}}); err != nil {
		t.Fatalf("next day: %v", err)
	}
}

func TestOpenRouterFallbackAndAuth(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		if req.Model == "m/one:free" {
			http.Error(w, `{"error":{"code":404,"message":"No endpoints found for m/one:free"}}`, http.StatusNotFound)
			return
		}
		reply(w, "m/two:free", translations("ok"))
	}
	o, _, _ := newTestTranslator(t, f, nil)
	res, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if err != nil || res.Model != "m/two:free" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}

	f2 := newFakeOpenRouter(t)
	f2.handler = func(w http.ResponseWriter, _ *http.Request, _ chatRequest, _ int) {
		http.Error(w, `{"error":{"code":401,"message":"User not found."}}`, http.StatusUnauthorized)
	}
	o2, _, _ := newTestTranslator(t, f2, nil)
	_, err = o2.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Status != 401 || f2.requests() != 1 {
		t.Fatalf("err = %v after %d requests, want one 401", err, f2.requests())
	}
}

func TestOpenRouterConcurrencyLimit(t *testing.T) {
	f := newFakeOpenRouter(t)
	f.handler = func(w http.ResponseWriter, _ *http.Request, req chatRequest, _ int) {
		time.Sleep(20 * time.Millisecond)
		reply(w, req.Model, translations("ok"))
	}
	o, _, _ := newTestTranslator(t, f, func(c *OpenRouterConfig) { c.Concurrency = 5 }) // clamped to 2
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if m := f.maxIn.Load(); m > MaxOpenRouterConcurrency || m < 1 {
		t.Fatalf("max in flight = %d, want at most %d", m, MaxOpenRouterConcurrency)
	}
}

func TestOpenRouterTimeout(t *testing.T) {
	f := newFakeOpenRouter(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.handler = func(w http.ResponseWriter, r *http.Request, _ chatRequest, _ int) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	o, _, _ := newTestTranslator(t, f, func(c *OpenRouterConfig) { c.Timeout = 50 * time.Millisecond; c.Models = []string{"m/one:free"} })
	start := time.Now()
	_, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err = %v after %s, want a per-request timeout", err, time.Since(start))
	}
}

// TestOpenRouterKeyNeverLeaks drives every failure path with a server that
// echoes the Authorization header back, and checks the key appears in no
// error, formatted config, log line or report.
func TestOpenRouterKeyNeverLeaks(t *testing.T) {
	f := newFakeOpenRouter(t)
	var mode atomic.Int32
	f.handler = func(w http.ResponseWriter, r *http.Request, req chatRequest, _ int) {
		auth := r.Header.Get("Authorization")
		key := strings.TrimPrefix(auth, "Bearer ")
		switch mode.Load() {
		case 0:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down "+auth, http.StatusTooManyRequests)
		case 1:
			http.Error(w, "boom key="+key+" "+auth, http.StatusInternalServerError)
		case 2:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"error":{"code":403,"message":"key %s is not allowed"}}`, key)
		case 3:
			reply(w, req.Model, "not json "+key)
		default:
			http.Error(w, "invalid key: "+key, http.StatusUnauthorized)
		}
	}
	var logs bytes.Buffer
	logger := obs.NewLogger(&logs, "test", obs.LogConfig{})
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	o, _, _ := newTestTranslator(t, f, func(c *OpenRouterConfig) { c.MaxRetries = 1 })
	var stored []string
	for m := int32(0); m <= 4; m++ {
		mode.Store(m)
		_, err := o.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
		if err == nil {
			t.Fatalf("mode %d: want an error", m)
		}
		stored = append(stored, err.Error(), fmt.Sprintf("%v %+v", err, err))
		logger.Error("translate failed", "err", err, "config", o.Config())
		log.Printf("translate failed: %v", err)
	}
	// A transport error (server gone) and an unreachable URL.
	cfg := o.Config()
	cfg.URL = "http://127.0.0.1:1/api/v1/chat/completions?key=" + testKey
	o2, err := NewOpenRouter(cfg, &MemoryBudget{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = o2.TranslateBatch(context.Background(), Batch{To: En, Texts: []string{"x"}})
	stored = append(stored, fmt.Sprint(err))
	c := o.Config()
	stored = append(stored, fmt.Sprintf("%v|%+v|%#v|%s", c, c, c, c), fmt.Sprint(c), c.String())
	cfgEnv, warn := OpenRouterConfigFromEnv(func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			return testKey
		}
		if k == "OPENROUTER_DAILY_CAP" {
			return "x" + testKey
		}
		return ""
	})
	logger.Info("config", "openrouter", cfgEnv, "warnings", warn)
	stored = append(stored, strings.Join(warn, ";"), fmt.Sprintf("%+v", cfgEnv))
	if raw, err := json.Marshal(map[string]any{"cfg": c.String()}); err == nil {
		stored = append(stored, string(raw))
	}
	stored = append(stored, logs.String())
	all := strings.Join(stored, "\n")
	if strings.Contains(all, testKey) || strings.Contains(all, "LEAKCANARY") {
		t.Fatalf("API key leaked into stored/logged output:\n%s", all)
	}
	for _, want := range []string{"HTTP 429", "HTTP 500", "HTTP 403", "HTTP 401", "output invalid"} {
		if !strings.Contains(all, want) {
			t.Fatalf("failure path %q not exercised:\n%s", want, all)
		}
	}
}

func TestOpenRouterConfigFromEnv(t *testing.T) {
	env := map[string]string{}
	get := func(k string) string { return env[k] }
	c, warn := OpenRouterConfigFromEnv(get)
	if c.Enabled() || len(warn) != 0 || c.Models[0] != DefaultOpenRouterModels[0] || c.DailyCap != DefaultOpenRouterDailyCap || c.Concurrency != 1 {
		t.Fatalf("defaults = %v, warn = %v", c, warn)
	}
	for _, m := range c.Models {
		if !strings.HasSuffix(m, ":free") {
			t.Fatalf("default model %q is not a free model", m)
		}
	}
	if _, err := NewOpenRouter(c, &MemoryBudget{}, nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no key: err = %v", err)
	}
	env = map[string]string{"OPENROUTER_API_KEY": " k ", "OPENROUTER_MODEL": "a:free, b:free ,", "OPENROUTER_DAILY_CAP": "50",
		"OPENROUTER_CONCURRENCY": "3", "OPENROUTER_TIMEOUT": "30s", "OPENROUTER_TITLE": "bad\nvalue", "OPENROUTER_REFERER": "https://x.test"}
	c, warn = OpenRouterConfigFromEnv(get)
	if !c.Enabled() || fmt.Sprint(c.Models) != "[a:free b:free]" || c.DailyCap != 50 || c.Concurrency != 1 || c.Timeout != 30*time.Second ||
		c.Title != "" || c.Referer != "https://x.test" || len(warn) != 2 {
		t.Fatalf("config = %v %q %q, warn = %v", c, c.Title, c.Referer, warn)
	}
}

func TestPlaceholders(t *testing.T) {
	cases := []struct {
		src, dst string
		ok       bool
	}{
		{"Jump to {time}", "跳转到 {time}", true},
		{"Jump to {time}", "跳转到 {zeit}", false},
		{"{n}/{max}", "{max} 中 {n}", true},
		{"{n}/{max}", "{n}", false},
		{"{n, plural, one {# word} other {# words}}", "{n, plural, other {# 个词}}", true},
		{"{n, plural, one {# word} other {# words}}", "{n, plural, other {# 个词}", false},
		{"{n, plural, one {# word} other {# words}}", "{count, plural, other {# 个词}}", false},
		{"{n, plural, one {{a} word} other {{a} words}}", "{n, plural, other {{a} 个词}}", true},
		{"{n, plural, one {{a} word} other {{a} words}}", "{n, plural, other {个词}}", false},
		{"{g, select, male {He} female {She} other {They}}", "{g, select, male {他} female {她} other {他们}}", true},
		{"{g, select, male {He} female {She} other {They}}", "{g, select, 男 {他} other {他们}}", false},
		{"{d, date, short}", "{d, date, short}", true},
		{"no braces", "没有", true},
		{"no braces", "有 {x}", false},
		{"stray { brace", "随便", true}, // unparsable source: not checked
	}
	for _, c := range cases {
		err := CheckPlaceholders(c.src, c.dst)
		if (err == nil) != c.ok {
			t.Errorf("CheckPlaceholders(%q, %q) = %v, want ok=%v", c.src, c.dst, err, c.ok)
		}
	}
}

func TestParseTranslations(t *testing.T) {
	b := Batch{Texts: []string{"a", "b"}}
	for _, bad := range []string{
		``, `[]`, `{"translations":["x"]}`, `{"translations":["x",""]}`, `{"translations":["x",1]}`,
		`{"translations":["x","y"]} trailing`, `{"items":["x","y"]}`, `here you go: {"translations":["x","y"]}`,
	} {
		if _, err := ParseTranslations(bad, b); !errors.Is(err, ErrInvalidOutput) {
			t.Errorf("ParseTranslations(%q) err = %v, want ErrInvalidOutput", bad, err)
		}
	}
	for _, good := range []string{`{"translations":["x","y"]}`, "```json\n{\"translations\":[\" x \",\"y\"]}\n```", "```\n{\"translations\":[\"x\",\"y\"]}```"} {
		got, err := ParseTranslations(good, b)
		if err != nil || fmt.Sprint(got) != "[x y]" {
			t.Errorf("ParseTranslations(%q) = %v, %v", good, got, err)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	if d := retryAfter(h, now); d != -1 {
		t.Fatalf("none = %v", d)
	}
	h.Set("X-RateLimit-Reset", fmt.Sprint(now.Add(9*time.Second).UnixMilli()))
	if d := retryAfter(h, now); d != 9*time.Second {
		t.Fatalf("reset = %v", d)
	}
	h.Set("Retry-After", now.Add(30*time.Second).Format(http.TimeFormat))
	if d := retryAfter(h, now); d != 30*time.Second {
		t.Fatalf("date = %v", d)
	}
	h.Set("Retry-After", "12")
	if d := retryAfter(h, now); d != 12*time.Second {
		t.Fatalf("seconds = %v", d)
	}
}
