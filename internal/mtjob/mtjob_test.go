package mtjob

import (
	"avxxx/internal/audit"
	"avxxx/internal/i18n"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeStore struct {
	requests []Item
	missing  []Item
	glossary map[string][]i18n.GlossaryTerm
	saved    map[string]string // ref/lang -> value
	models   map[string]string
	human    map[string]bool // a human row exists: save refuses
	failed   []string
	runs     []audit.Run
	gotLangs []string
	gotLimit int
}

func key(it Item) string {
	return it.Ref.EntityType + "/" + it.Ref.EntityID + "/" + it.Ref.Field + "/" + it.Lang
}

func (f *fakeStore) DueMTRequests(_ context.Context, _ time.Time, limit int) ([]Item, error) {
	if len(f.requests) > limit {
		return f.requests[:limit], nil
	}
	return f.requests, nil
}
func (f *fakeStore) MissingReleaseTitles(_ context.Context, langs []string, limit int) ([]Item, error) {
	f.gotLangs, f.gotLimit = langs, limit
	if len(f.missing) > limit {
		return f.missing[:limit], nil
	}
	return f.missing, nil
}
func (f *fakeStore) MTGlossary(_ context.Context, lang string) ([]i18n.GlossaryTerm, error) {
	return f.glossary[lang], nil
}
func (f *fakeStore) SaveMachineTranslation(_ context.Context, it Item, value, model string, _ time.Time) (bool, error) {
	if f.human[key(it)] {
		return false, nil
	}
	if f.saved == nil {
		f.saved, f.models = map[string]string{}, map[string]string{}
	}
	f.saved[key(it)], f.models[key(it)] = value, model
	return true, nil
}
func (f *fakeStore) FailMTRequest(_ context.Context, it Item, reason string) error {
	f.failed = append(f.failed, key(it)+": "+reason)
	return nil
}
func (f *fakeStore) RecordRun(_ context.Context, r audit.Run) error {
	f.runs = append(f.runs, r)
	return nil
}

type fakeTranslator struct {
	batches []i18n.Batch
	err     func(n int) error
}

func (t *fakeTranslator) TranslateBatch(_ context.Context, b i18n.Batch) (i18n.Result, error) {
	t.batches = append(t.batches, b)
	if t.err != nil {
		if err := t.err(len(t.batches)); err != nil {
			return i18n.Result{}, err
		}
	}
	out := make([]string, len(b.Texts))
	for i, s := range b.Texts {
		out[i] = b.To + ":" + s
	}
	return i18n.Result{Texts: out, Model: "m/x:free"}, nil
}

func rel(id, lang, title string, requested bool) Item {
	return Item{Ref: i18n.Ref{EntityType: i18n.EntityRelease, EntityID: id, Field: "title"}, Lang: lang, Source: title, Requested: requested}
}

func TestRunNotConfigured(t *testing.T) {
	st := &fakeStore{missing: []Item{rel("1", "en", "t", false)}}
	rep, err := (&Runner{Store: st}).Run(context.Background())
	if err != nil || rep.Stopped != "not_configured" || rep.Saved != 0 || len(st.runs) != 1 || st.runs[0].Result != audit.ResultNotApplicable {
		t.Fatalf("rep = %+v, err = %v, runs = %+v", rep, err, st.runs)
	}
}

func TestRunRequestsFirstThenMissing(t *testing.T) {
	st := &fakeStore{
		requests: []Item{rel("9", "ko", "依頼", true), {Ref: i18n.Ref{EntityType: i18n.EntityPerformer, EntityID: "4", Field: "name"}, Lang: "ko", Source: "三上悠亜", Requested: true}},
		missing:  []Item{rel("1", "zh-CN", "タイトル1", false), rel("1", "en", "タイトル1", false), rel("2", "zh-CN", "タイトル2", false)},
		glossary: map[string][]i18n.GlossaryTerm{"zh-CN": {{Term: "タイトル", Lang: "zh-CN", Translation: "标题"}}},
		human:    map[string]bool{"release/2/title/zh-CN": true},
	}
	tr := &fakeTranslator{}
	rep, err := (&Runner{Store: st, Translator: tr, Config: Config{MaxPerRun: 10, Langs: []string{"zh-CN", "en"}}, JobID: 5}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Requested != 2 || rep.Missing != 3 || rep.Saved != 4 || rep.Skipped != 1 || rep.Batches != 3 {
		t.Fatalf("rep = %+v", rep)
	}
	if st.gotLimit != 8 || fmt.Sprint(st.gotLangs) != "[zh-CN en]" {
		t.Fatalf("missing limit = %d langs = %v", st.gotLimit, st.gotLangs)
	}
	if tr.batches[0].To != "ko" || len(tr.batches[0].Texts) != 2 {
		t.Fatalf("first batch = %+v, want the admin requests", tr.batches[0])
	}
	if tr.batches[1].To != "zh-CN" || len(tr.batches[1].Glossary) != 1 {
		t.Fatalf("zh-CN batch = %+v, want the glossary", tr.batches[1])
	}
	if st.saved["release/1/title/zh-CN"] != "zh-CN:タイトル1" || st.models["release/1/title/zh-CN"] != "m/x:free" {
		t.Fatalf("saved = %v", st.saved)
	}
	if r := st.runs[0]; r.Job != JobKind || r.JobID != 5 || r.Fixed != 4 || r.Result != audit.ResultIssues {
		t.Fatalf("run = %+v", r)
	}
}

func TestRunStopsOnCapAndRateLimit(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{{i18n.ErrDailyCap, "daily_cap"}, {fmt.Errorf("%w: x", i18n.ErrRateLimited), "rate_limited"}} {
		st := &fakeStore{missing: []Item{rel("1", "en", "a", false), rel("2", "zh-CN", "b", false)}}
		tr := &fakeTranslator{err: func(int) error { return c.err }}
		rep, err := (&Runner{Store: st, Translator: tr}).Run(context.Background())
		if err != nil || rep.Stopped != c.want || len(tr.batches) != 1 || len(st.failed) != 0 {
			t.Fatalf("%v: rep = %+v err = %v batches = %d failed = %v", c.err, rep, err, len(tr.batches), st.failed)
		}
	}
}

func TestRunFailuresBackOffAndStreak(t *testing.T) {
	var items []Item
	for i, l := range []string{"en", "zh-CN", "zh-TW", "ko"} {
		items = append(items, rel(fmt.Sprint(i+1), l, "t", false))
	}
	st := &fakeStore{missing: items}
	tr := &fakeTranslator{err: func(int) error { return fmt.Errorf("%w: bad", i18n.ErrInvalidOutput) }}
	rep, err := (&Runner{Store: st, Translator: tr}).Run(context.Background())
	if err != nil || rep.Stopped != "error_streak" || rep.Failed != 3 || len(st.failed) != 3 {
		t.Fatalf("rep = %+v err = %v failed = %v", rep, err, st.failed)
	}
}

func TestBatches(t *testing.T) {
	long := strings.Repeat("あ", 40)
	items := []Item{rel("1", "en", "a", false), rel("2", "en", long, false), rel("3", "zh-CN", "c", false), rel("4", "en", "d", false), rel("5", "en", "e", false)}
	got := Batches(items, 3, 41)
	var shape []string
	for _, b := range got {
		var ids []string
		for _, it := range b {
			ids = append(ids, it.Ref.EntityID)
		}
		shape = append(shape, b[0].Lang+":"+strings.Join(ids, ","))
	}
	if fmt.Sprint(shape) != "[en:1,2 en:4,5 zh-CN:3]" {
		t.Fatalf("batches = %v", shape)
	}
}

func TestBackoffAndConfig(t *testing.T) {
	if Backoff(1) != time.Hour || Backoff(2) != 6*time.Hour || Backoff(MaxAttempts) != GiveUpFor {
		t.Fatal("backoff")
	}
	c, warn := ConfigFromEnv(func(k string) string {
		return map[string]string{"I18N_MT_LANGS": "en, ja,xx,ko", "I18N_MT_MAX_PER_RUN": "0"}[k]
	})
	if fmt.Sprint(c.Langs) != "[en ko]" || c.MaxPerRun != DefaultMaxPerRun || len(warn) != 3 {
		t.Fatalf("config = %+v warn = %v", c, warn)
	}
	c, _ = ConfigFromEnv(func(string) string { return "" })
	if fmt.Sprint(c.Langs) != "[zh-CN zh-TW en]" {
		t.Fatalf("default langs = %v", c.Langs)
	}
}

// TestRunWithFakeOpenRouter runs the job against an httptest OpenRouter
// whose error bodies echo the key: the run stops at the daily cap and the
// report (stored in nightly_runs) never contains the key.
func TestRunWithFakeOpenRouter(t *testing.T) {
	const testKey = "sk-or-v1-JOBTEST-LEAKCANARY-feedface"
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "upstream error for "+r.Header.Get("Authorization"), http.StatusBadGateway)
			return
		}
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var p struct{ Items []string }
		json.Unmarshal([]byte(req.Messages[1].Content), &p)
		out := make([]string, len(p.Items))
		for i := range p.Items {
			out[i] = "T" + fmt.Sprint(i)
		}
		b, _ := json.Marshal(map[string]any{"translations": out})
		json.NewEncoder(w).Encode(map[string]any{"model": "google/gemma-4-31b-it:free", "choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}}})
	}))
	defer srv.Close()
	tr, err := i18n.NewOpenRouter(i18n.OpenRouterConfig{APIKey: testKey, Models: []string{"a:free"}, URL: srv.URL, DailyCap: 2, BatchSize: 2}, &i18n.MemoryBudget{}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	st := &fakeStore{missing: []Item{rel("1", "en", "a", false), rel("2", "en", "b", false), rel("3", "en", "c", false), rel("4", "en", "d", false), rel("5", "en", "e", false)}}
	rep, err := (&Runner{Store: st, Translator: tr, Config: Config{BatchSize: 2}}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Batch 1 fails (502, only model), batch 2 succeeds, batch 3 hits the cap.
	if rep.Failed != 2 || rep.Saved != 2 || rep.Stopped != "daily_cap" || calls != 2 || rep.Models["google/gemma-4-31b-it:free"] != 2 {
		t.Fatalf("rep = %+v calls = %d", rep, calls)
	}
	raw, _ := json.Marshal(rep)
	all := string(raw) + fmt.Sprint(st.runs) + strings.Join(st.failed, "\n")
	if strings.Contains(all, testKey) || strings.Contains(all, "LEAKCANARY") {
		t.Fatalf("key leaked: %s", all)
	}
	if !strings.Contains(strings.Join(st.failed, ""), "HTTP 502") {
		t.Fatalf("failed = %v", st.failed)
	}
}
