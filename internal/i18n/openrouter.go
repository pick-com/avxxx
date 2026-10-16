package i18n

// OpenRouter machine translation (tech plan 3.10, ROADMAP 机器翻译): a
// Translator over OpenRouter's OpenAI-compatible chat completions API
// (https://openrouter.ai/docs/api-reference/chat-completion), defaulting to
// free (":free") models. Free models are rate limited per minute and per
// day (https://openrouter.ai/docs/api-reference/limits), so the client is
// deliberately polite: at most two requests in flight, a daily request cap
// shared through the database, 429 backoff honouring Retry-After, and a
// per-request timeout. The API key travels only in the Authorization header
// and is scrubbed from every error, log value and report.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"avxxx/internal/obs"
)

// OpenRouterURL is the chat completions endpoint.
const OpenRouterURL = "https://openrouter.ai/api/v1/chat/completions"

// DefaultOpenRouterModels is the default OPENROUTER_MODEL: free models,
// tried in order. Gemma 4 (Google DeepMind, multilingual including CJK) is
// first; the list was taken from https://openrouter.ai/api/v1/models
// (ids ending in ":free", 2026-10-16) and the model page
// https://openrouter.ai/google/gemma-4-31b-it:free. Free models come and go,
// so the list is configurable and an unavailable model falls through to the
// next one.
var DefaultOpenRouterModels = []string{
	"google/gemma-4-31b-it:free",
	"google/gemma-4-26b-a4b-it:free",
	"nvidia/nemotron-3-super-120b-a12b:free",
}

// Defaults for the free tier.
const (
	DefaultOpenRouterDailyCap    = 200
	DefaultOpenRouterConcurrency = 1
	MaxOpenRouterConcurrency     = 2
	DefaultOpenRouterTimeout     = 90 * time.Second
	DefaultOpenRouterRetries     = 3
	DefaultOpenRouterMaxWait     = 60 * time.Second
	DefaultOpenRouterBatchSize   = 20
)

// Errors returned (wrapped) by the OpenRouter translator.
var (
	ErrNotConfigured = errors.New("machine translation not configured (OPENROUTER_API_KEY)")
	ErrDailyCap      = errors.New("machine translation daily request cap reached")
	ErrRateLimited   = errors.New("machine translation rate limited")
	ErrInvalidOutput = errors.New("machine translation output invalid")
)

// OpenRouterConfig configures the OpenRouter translator. Its String,
// GoString and LogValue never include the API key.
type OpenRouterConfig struct {
	APIKey string
	// Models are tried in order (OPENROUTER_MODEL, comma-separated).
	Models []string
	// Referer / Title are OpenRouter's optional app attribution headers
	// (HTTP-Referer, X-Title).
	Referer string
	Title   string
	// DailyCap is the most requests per UTC day across all processes
	// (OPENROUTER_DAILY_CAP); every HTTP attempt, retries included, counts.
	DailyCap int
	// Concurrency is the most requests in flight in this process (1-2).
	Concurrency int
	// Timeout bounds one HTTP request (OPENROUTER_TIMEOUT).
	Timeout time.Duration
	// MaxRetries is how many times one model is retried after a 429.
	MaxRetries int
	// MaxRetryWait is the longest Retry-After the client sleeps through;
	// a longer one moves on to the next model.
	MaxRetryWait time.Duration
	// BatchSize is how many strings go in one request (OPENROUTER_BATCH_SIZE).
	BatchSize int
	// URL overrides OpenRouterURL (tests only; not read from the environment).
	URL string
}

// Enabled reports whether a key is configured.
func (c OpenRouterConfig) Enabled() bool { return strings.TrimSpace(c.APIKey) != "" }

func (c OpenRouterConfig) redacted() string {
	key := ""
	if c.Enabled() {
		key = obs.Redacted
	}
	return fmt.Sprintf("OpenRouterConfig{APIKey:%q Models:%v DailyCap:%d Concurrency:%d Timeout:%s BatchSize:%d}",
		key, c.Models, c.DailyCap, c.Concurrency, c.Timeout, c.BatchSize)
}

// String never includes the API key.
func (c OpenRouterConfig) String() string { return c.redacted() }

// GoString never includes the API key (%#v).
func (c OpenRouterConfig) GoString() string { return c.redacted() }

// Format never includes the API key, whatever the verb (%v, %+v, %#v, %s).
func (c OpenRouterConfig) Format(f fmt.State, _ rune) { io.WriteString(f, c.redacted()) }

// LogValue never includes the API key.
func (c OpenRouterConfig) LogValue() slog.Value {
	return slog.GroupValue(slog.Bool("configured", c.Enabled()), slog.String("models", strings.Join(c.Models, ",")),
		slog.Int("daily_cap", c.DailyCap), slog.Int("concurrency", c.Concurrency), slog.Duration("timeout", c.Timeout))
}

func (c OpenRouterConfig) withDefaults() OpenRouterConfig {
	if len(c.Models) == 0 {
		c.Models = append([]string(nil), DefaultOpenRouterModels...)
	}
	if c.DailyCap <= 0 {
		c.DailyCap = DefaultOpenRouterDailyCap
	}
	if c.Concurrency <= 0 {
		c.Concurrency = DefaultOpenRouterConcurrency
	}
	if c.Concurrency > MaxOpenRouterConcurrency {
		c.Concurrency = MaxOpenRouterConcurrency
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultOpenRouterTimeout
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	} else if c.MaxRetries == 0 {
		c.MaxRetries = DefaultOpenRouterRetries
	}
	if c.MaxRetryWait <= 0 {
		c.MaxRetryWait = DefaultOpenRouterMaxWait
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultOpenRouterBatchSize
	}
	if c.URL == "" {
		c.URL = OpenRouterURL
	}
	return c
}

// OpenRouterConfigFromEnv reads OPENROUTER_API_KEY, OPENROUTER_MODEL,
// OPENROUTER_REFERER, OPENROUTER_TITLE, OPENROUTER_DAILY_CAP,
// OPENROUTER_CONCURRENCY, OPENROUTER_TIMEOUT and OPENROUTER_BATCH_SIZE.
// Invalid values keep the default and add a warning (never containing the
// key). An empty key leaves the translator disabled.
func OpenRouterConfigFromEnv(getenv func(string) string) (OpenRouterConfig, []string) {
	var c OpenRouterConfig
	var warn []string
	c.APIKey = strings.TrimSpace(getenv("OPENROUTER_API_KEY"))
	for _, m := range strings.Split(getenv("OPENROUTER_MODEL"), ",") {
		if m = strings.TrimSpace(m); m != "" {
			c.Models = append(c.Models, m)
		}
	}
	for _, h := range []struct {
		name string
		dst  *string
	}{{"OPENROUTER_REFERER", &c.Referer}, {"OPENROUTER_TITLE", &c.Title}} {
		v := strings.TrimSpace(getenv(h.name))
		if strings.ContainsAny(v, "\r\n") || len(v) > 300 {
			warn = append(warn, h.name+" ignored: must be one line of at most 300 bytes")
			continue
		}
		*h.dst = v
	}
	ints := []struct {
		name     string
		dst      *int
		min, max int
	}{
		{"OPENROUTER_DAILY_CAP", &c.DailyCap, 1, 100000},
		{"OPENROUTER_CONCURRENCY", &c.Concurrency, 1, MaxOpenRouterConcurrency},
		{"OPENROUTER_BATCH_SIZE", &c.BatchSize, 1, 100},
	}
	for _, e := range ints {
		v := strings.TrimSpace(getenv(e.name))
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < e.min || n > e.max {
			// The value is not echoed: a misplaced key must not reach the logs.
			warn = append(warn, fmt.Sprintf("%s ignored: want an integer %d-%d", e.name, e.min, e.max))
			continue
		}
		*e.dst = n
	}
	if v := strings.TrimSpace(getenv("OPENROUTER_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second || d > 10*time.Minute {
			warn = append(warn, "OPENROUTER_TIMEOUT ignored: want a duration between 5s and 10m")
		} else {
			c.Timeout = d
		}
	}
	return c.withDefaults(), warn
}

// RequestBudget is the shared daily request counter (the store's
// mt_usage table). ReserveRequest takes one request from day's budget and
// reports false when cap requests were already made that UTC day.
type RequestBudget interface {
	ReserveRequest(ctx context.Context, day time.Time, cap int) (bool, error)
}

// MemoryBudget is an in-process RequestBudget (tests, one-off commands).
type MemoryBudget struct {
	mu   sync.Mutex
	used map[string]int
}

// ReserveRequest implements RequestBudget.
func (m *MemoryBudget) ReserveRequest(_ context.Context, day time.Time, cap int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == nil {
		m.used = map[string]int{}
	}
	k := day.UTC().Format(time.DateOnly)
	if m.used[k] >= cap {
		return false, nil
	}
	m.used[k]++
	return true, nil
}

// Used returns the requests reserved on day.
func (m *MemoryBudget) Used(day time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used[day.UTC().Format(time.DateOnly)]
}

// ProviderError is a non-success answer from OpenRouter. Message is
// scrubbed of the key and other secrets.
type ProviderError struct {
	Status  int
	Model   string
	Message string
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("openrouter %s: HTTP %d: %s", e.Model, e.Status, e.Message)
}

// OpenRouter implements Translator.
type OpenRouter struct {
	cfg    OpenRouterConfig
	client *http.Client
	budget RequestBudget
	sem    chan struct{}
	// Now and Sleep are replaceable in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

// NewOpenRouter returns the translator, or ErrNotConfigured without a key.
// client may be nil (a default client; each request still gets cfg.Timeout).
func NewOpenRouter(cfg OpenRouterConfig, budget RequestBudget, client *http.Client) (*OpenRouter, error) {
	if !cfg.Enabled() {
		return nil, ErrNotConfigured
	}
	if budget == nil {
		return nil, errors.New("openrouter: a request budget is required")
	}
	cfg = cfg.withDefaults()
	if client == nil {
		client = &http.Client{}
	}
	return &OpenRouter{cfg: cfg, client: client, budget: budget, sem: make(chan struct{}, cfg.Concurrency),
		Now: time.Now, Sleep: sleepCtx}, nil
}

// Config returns the effective configuration (print it only through its
// redacting String / LogValue).
func (o *OpenRouter) Config() OpenRouterConfig { return o.cfg }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Redact removes the API key and other secret-looking text from s.
func (o *OpenRouter) Redact(s string) string {
	if k := strings.TrimSpace(o.cfg.APIKey); k != "" {
		s = strings.ReplaceAll(s, k, obs.Redacted)
	}
	return obs.ScrubString(s)
}

// LanguageNames are the English names given to the model.
var LanguageNames = map[string]string{
	ZhCN: "Simplified Chinese (zh-CN)",
	ZhTW: "Traditional Chinese (zh-TW, Taiwan usage)",
	En:   "English",
	Ja:   "Japanese",
	Ko:   "Korean",
	Vi:   "Vietnamese",
	Es:   "Spanish",
	Th:   "Thai",
}

func langName(l string) string {
	if n, ok := LanguageNames[l]; ok {
		return n
	}
	if l == "" || l == Original {
		return "the source language (usually Japanese; detect it)"
	}
	return l
}

// systemPrompt is the fixed instruction block.
func systemPrompt(b Batch) string {
	var sb strings.Builder
	sb.WriteString("You are a professional translator for a Japanese adult-video catalog website. ")
	fmt.Fprintf(&sb, "Translate every string in \"items\" from %s into %s.\n", langName(b.From), langName(b.To))
	sb.WriteString(`Rules:
1. Reply with JSON only, exactly {"translations": [...]}: one string per input item, same count, same order. No comments, no markdown.
2. When a glossary term appears in an item, use its glossary translation exactly.
3. Keep every {placeholder} exactly as written, e.g. {n} or {date}. In ICU blocks like {n, plural, one {# item} other {# items}} keep the argument name, the keyword and the selectors and the # sign; translate only the text inside the branches.
4. Keep product codes (e.g. ABP-123), numbers, URLs and Latin-script brand names unchanged.
5. Personal names are names, not words: never translate their meaning. Keep them as written in the source, or use the standard romanization when the target language is written in Latin script.
6. Translate faithfully and naturally; do not summarise, censor, explain, add or omit content.
`)
	if b.UI {
		sb.WriteString("7. These are user-interface strings: keep them short and consistent in tone.\n")
	}
	return sb.String()
}

type promptGlossary struct {
	Term        string `json:"term"`
	Translation string `json:"translation"`
}

// userPrompt is the JSON payload with the items and the glossary terms that
// occur in them (all terms when few).
func userPrompt(b Batch) (string, error) {
	gl := []promptGlossary{}
	for _, g := range RelevantGlossary(b.Glossary, b.To, b.Texts) {
		gl = append(gl, promptGlossary{g.Term, g.Translation})
	}
	payload := struct {
		Source   string           `json:"source_language"`
		Target   string           `json:"target_language"`
		Glossary []promptGlossary `json:"glossary"`
		Items    []string         `json:"items"`
	}{langName(b.From), langName(b.To), gl, b.Texts}
	raw, err := json.Marshal(payload)
	return string(raw), err
}

// RelevantGlossary returns the glossary entries for lang whose term occurs
// in at least one of texts (case-insensitively), longest term first so the
// model prefers the most specific match.
func RelevantGlossary(glossary []GlossaryTerm, lang string, texts []string) []GlossaryTerm {
	var out []GlossaryTerm
	lower := make([]string, len(texts))
	for i, t := range texts {
		lower[i] = strings.ToLower(t)
	}
	seen := map[string]bool{}
	for _, g := range glossary {
		term := strings.TrimSpace(g.Term)
		if term == "" || strings.TrimSpace(g.Translation) == "" || (g.Lang != "" && g.Lang != lang) || seen[term] {
			continue
		}
		lt := strings.ToLower(term)
		for _, t := range lower {
			if strings.Contains(t, lt) {
				out = append(out, g)
				seen[term] = true
				break
			}
		}
	}
	for i := 1; i < len(out); i++ { // insertion sort: stable, tiny lists
		for j := i; j > 0 && len([]rune(out[j].Term)) > len([]rune(out[j-1].Term)); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string            `json:"model"`
	Messages       []chatMessage     `json:"messages"`
	Temperature    float64           `json:"temperature"`
	ResponseFormat map[string]string `json:"response_format,omitempty"`
	Reasoning      map[string]bool   `json:"reasoning,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ParseTranslations strictly parses a model reply for b: a JSON object
// {"translations": [...]} (optionally inside one markdown code fence) with
// exactly len(b.Texts) non-empty strings, each keeping its source's
// placeholders. Errors wrap ErrInvalidOutput.
func ParseTranslations(content string, b Batch) ([]string, error) {
	s := strings.TrimSpace(content)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		if nl := strings.IndexByte(s, '\n'); nl >= 0 && !strings.Contains(s[:nl], "{") {
			s = s[nl+1:] // ```json
		}
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(s))
	var out struct {
		Translations *[]any `json:"translations"`
	}
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: not a JSON object: %v", ErrInvalidOutput, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data after the JSON object", ErrInvalidOutput)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("%w: trailing data after the JSON object", ErrInvalidOutput)
	}
	if out.Translations == nil {
		return nil, fmt.Errorf("%w: missing \"translations\"", ErrInvalidOutput)
	}
	if got := len(*out.Translations); got != len(b.Texts) {
		return nil, fmt.Errorf("%w: %d translations for %d items", ErrInvalidOutput, got, len(b.Texts))
	}
	texts := make([]string, len(b.Texts))
	for i, v := range *out.Translations {
		str, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%w: item %d is not a string", ErrInvalidOutput, i)
		}
		str = strings.TrimSpace(str)
		if str == "" {
			return nil, fmt.Errorf("%w: item %d is empty", ErrInvalidOutput, i)
		}
		if err := CheckPlaceholders(b.Texts[i], str); err != nil {
			return nil, fmt.Errorf("%w: item %d: %v", ErrInvalidOutput, i, err)
		}
		texts[i] = str
	}
	return texts, nil
}

// TranslateBatch implements Translator. Models are tried in order: a 429 is
// retried on the same model after Retry-After (or exponential backoff) up
// to MaxRetries times, an unavailable model (404, 5xx) moves on to the next,
// a 401/402/403 stops. Output that fails ParseTranslations is retried once
// with the reason; a second failure is ErrInvalidOutput. Every HTTP attempt
// takes one request from the daily budget first.
func (o *OpenRouter) TranslateBatch(ctx context.Context, b Batch) (Result, error) {
	if len(b.Texts) == 0 {
		return Result{}, nil
	}
	if !Supported(b.To) {
		return Result{}, fmt.Errorf("openrouter: unsupported target language %q", b.To)
	}
	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	defer func() { <-o.sem }()
	user, err := userPrompt(b)
	if err != nil {
		return Result{}, err
	}
	messages := []chatMessage{{"system", systemPrompt(b)}, {"user", user}}
	var lastErr error
	invalidRetried := false
models:
	for _, model := range o.cfg.Models {
		msgs := messages
		for attempt := 0; ; attempt++ {
			texts, used, wait, err := o.attempt(ctx, model, msgs, b)
			if err == nil {
				return Result{Texts: texts, Model: used}, nil
			}
			lastErr = err
			if errors.Is(err, ErrDailyCap) || ctx.Err() != nil {
				return Result{}, err
			}
			if errors.Is(err, ErrInvalidOutput) {
				if invalidRetried {
					return Result{}, err
				}
				invalidRetried = true
				msgs = append(append([]chatMessage(nil), messages...), chatMessage{"user",
					"Your previous reply was rejected (" + o.Redact(err.Error()) + "). Reply again with only the JSON object, exactly " +
						strconv.Itoa(len(b.Texts)) + " translations, placeholders unchanged."})
				continue
			}
			var pe *ProviderError
			if !errors.As(err, &pe) {
				continue models // network error or timeout: try the next model
			}
			switch pe.Status {
			case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
				return Result{}, err // key or credit problem: other models fail too
			case http.StatusTooManyRequests:
				if wait < 0 { // no Retry-After: exponential backoff 2s, 4s, 8s...
					wait = backoffBase << attempt
				}
				if attempt >= o.cfg.MaxRetries || wait > o.cfg.MaxRetryWait {
					continue models
				}
				if err := o.Sleep(ctx, wait); err != nil {
					return Result{}, err
				}
				continue
			}
			continue models // unavailable model, other 4xx/5xx: try the next one
		}
	}
	var pe *ProviderError
	if errors.As(lastErr, &pe) && pe.Status == http.StatusTooManyRequests {
		return Result{}, fmt.Errorf("%w: %v", ErrRateLimited, lastErr)
	}
	return Result{}, lastErr
}

// maxResponseBytes bounds a reply body.
const maxResponseBytes = 4 << 20

// attempt makes one HTTP request. wait is the Retry-After of a 429 (-1 when
// the reply names none).
func (o *OpenRouter) attempt(ctx context.Context, model string, msgs []chatMessage, b Batch) (texts []string, used string, wait time.Duration, err error) {
	ok, err := o.budget.ReserveRequest(ctx, o.Now().UTC(), o.cfg.DailyCap)
	if err != nil {
		return nil, "", 0, fmt.Errorf("openrouter: daily budget: %w", err)
	}
	if !ok {
		return nil, "", 0, fmt.Errorf("%w (%d requests per UTC day, OPENROUTER_DAILY_CAP)", ErrDailyCap, o.cfg.DailyCap)
	}
	body, err := json.Marshal(chatRequest{Model: model, Messages: msgs, Temperature: 0.2,
		ResponseFormat: map[string]string{"type": "json_object"}, Reasoning: map[string]bool{"exclude": true}})
	if err != nil {
		return nil, "", 0, err
	}
	rctx, cancel := context.WithTimeout(ctx, o.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, o.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, "", 0, errors.New(o.Redact(err.Error()))
	}
	req.Header.Set("Authorization", "Bearer "+o.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if o.cfg.Referer != "" {
		req.Header.Set("HTTP-Referer", o.cfg.Referer)
	}
	if o.cfg.Title != "" {
		req.Header.Set("X-Title", o.cfg.Title)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", 0, ctx.Err()
		}
		return nil, "", 0, fmt.Errorf("openrouter %s: %s", model, o.Redact(err.Error()))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, "", 0, fmt.Errorf("openrouter %s: read: %s", model, o.Redact(err.Error()))
	}
	var cr chatResponse
	jsonErr := json.Unmarshal(raw, &cr)
	status := resp.StatusCode
	if status == http.StatusOK && jsonErr == nil && cr.Error != nil {
		// OpenRouter can report upstream errors in a 200 body.
		status = http.StatusBadGateway
		if n, ok := errorCode(cr.Error.Code); ok && n >= 400 {
			status = n
		}
	}
	if status != http.StatusOK {
		msg := strings.TrimSpace(string(raw))
		if jsonErr == nil && cr.Error != nil && cr.Error.Message != "" {
			msg = cr.Error.Message
		}
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		pe := &ProviderError{Status: status, Model: model, Message: o.Redact(msg)}
		if status == http.StatusTooManyRequests {
			wait = retryAfter(resp.Header, o.Now())
		}
		return nil, "", wait, pe
	}
	if jsonErr != nil || len(cr.Choices) == 0 {
		return nil, "", 0, fmt.Errorf("%w: response has no choices", ErrInvalidOutput)
	}
	texts, err = ParseTranslations(cr.Choices[0].Message.Content, b)
	if err != nil {
		return nil, "", 0, err
	}
	used = cr.Model
	if used == "" {
		used = model
	}
	return texts, used, 0, nil
}

func errorCode(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil
	}
	return 0, false
}

// backoffBase is the first 429 backoff when the reply names no wait.
const backoffBase = 2 * time.Second

// retryAfter is the wait a 429 asks for: Retry-After (seconds or an HTTP
// date), else OpenRouter's X-RateLimit-Reset (epoch milliseconds), else -1
// (unknown: the caller backs off exponentially). A known wait is never
// negative.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			return max(t.Sub(now), 0)
		}
	}
	if v := strings.TrimSpace(h.Get("X-RateLimit-Reset")); v != "" {
		if ms, err := strconv.ParseInt(v, 10, 64); err == nil && ms > 0 {
			return max(time.UnixMilli(ms).Sub(now), 0)
		}
	}
	return -1
}
