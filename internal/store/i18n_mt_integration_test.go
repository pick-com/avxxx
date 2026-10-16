package store

import (
	"avxxx/internal/i18n"
	"avxxx/internal/mtjob"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestMachineTranslationIntegration runs the i18n_machine job against an
// httptest OpenRouter (never the real API) on an isolated database:
// machine drafts with provenance, never public, never over a human row,
// the admin queue, failure backoff and the shared daily cap.
func TestMachineTranslationIntegration(t *testing.T) {
	openIsolatedTestDB(t, "mt")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // migrations re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var pub, pub2, draft, perf int64
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES('mt-1','人妻温泉旅行','published','2026-10-01') RETURNING id`).Scan(&pub))
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date,synopsis) VALUES('mt-2','素人の夏','published','2026-09-01','長いあらすじ') RETURNING id`).Scan(&pub2))
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('mt-3','下書き','draft') RETURNING id`).Scan(&draft))
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES('mt-p','三上悠亜') RETURNING id`).Scan(&perf))
	_, err = s.UpsertGlossaryTerm(ctx, GlossaryEntry{Term: "人妻", Lang: "zh-CN", Translation: "人妻"}, "admin")
	must(err)
	id := func(n int64) string { return strconv.FormatInt(n, 10) }

	// A reviewed human zh-CN title for pub2 must never be touched.
	ref2 := i18n.Ref{EntityType: "release", EntityID: id(pub2), Field: "title"}
	_, err = s.UpsertTranslation(ctx, ref2, "zh-CN", "素人之夏", "human")
	must(err)
	_, err = s.TransitionTranslation(ctx, ref2, "zh-CN", i18n.ActionSubmit, "human")
	must(err)
	_, err = s.TransitionTranslation(ctx, ref2, "zh-CN", i18n.ActionReview, "human")
	must(err)

	var calls atomic.Int32
	var sawGlossary atomic.Bool
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if fail.Load() {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var p struct {
			Target   string `json:"target_language"`
			Items    []string
			Glossary []struct{ Term string }
		}
		json.Unmarshal([]byte(req.Messages[1].Content), &p)
		if len(p.Glossary) > 0 && p.Glossary[0].Term == "人妻" {
			sawGlossary.Store(true)
		}
		out := make([]string, len(p.Items))
		for i, it := range p.Items {
			out[i] = "[" + strings.Fields(p.Target)[0] + "] " + it
		}
		b, _ := json.Marshal(map[string]any{"translations": out})
		json.NewEncoder(w).Encode(map[string]any{"model": "google/gemma-4-31b-it:free", "choices": []any{map[string]any{"message": map[string]any{"content": string(b)}}}})
	}))
	defer srv.Close()
	tr, err := i18n.NewOpenRouter(i18n.OpenRouterConfig{APIKey: "sk-or-v1-integration-test", Models: []string{"google/gemma-4-31b-it:free"}, URL: srv.URL, DailyCap: 50},
		s.MTBudget(MTProviderOpenRouter), srv.Client())
	must(err)
	runner := func() *mtjob.Runner {
		return &mtjob.Runner{Store: s, Translator: tr, Config: mtjob.Config{Langs: []string{"zh-CN", "en"}, MaxPerRun: 20}}
	}

	// Admin selection: performer name in ko (no human row) is queued; the
	// reviewed zh-CN title and the draft-less missing source are skipped.
	pname := i18n.Ref{EntityType: "performer", EntityID: id(perf), Field: "name"}
	n, err := s.QueueMachineTranslations(ctx, "ko", []i18n.Ref{pname, {EntityType: "release", EntityID: "999999", Field: "title"}}, "admin")
	if err != nil || n != 1 {
		t.Fatalf("queue = %d, %v", n, err)
	}
	if n, err = s.QueueMachineTranslations(ctx, "zh-CN", []i18n.Ref{ref2}, "admin"); err != nil || n != 0 {
		t.Fatalf("queue over a reviewed row = %d, %v", n, err)
	}

	rep, err := runner().Run(ctx)
	must(err)
	// 1 request + titles: pub (zh-CN, en) + pub2 (en); draft release and pub2 zh-CN skipped.
	if rep.Requested != 1 || rep.Missing != 3 || rep.Saved != 4 || rep.Failed != 0 {
		t.Fatalf("report = %+v", rep)
	}
	if !sawGlossary.Load() {
		t.Error("the zh-CN glossary term was not sent")
	}
	var status, value string
	var model *string
	var mtAt *time.Time
	var reviewed *string
	must(s.QueryRow(ctx, `SELECT status,value,mt_model,mt_at,reviewed_value FROM translations WHERE entity_type='release' AND entity_id=$1 AND field='title' AND lang='zh-CN'`, id(pub)).
		Scan(&status, &value, &model, &mtAt, &reviewed))
	if status != "machine" || value != "[Simplified] 人妻温泉旅行" || model == nil || *model != "google/gemma-4-31b-it:free" || mtAt == nil || reviewed != nil {
		t.Fatalf("machine row = %s %q %v %v %v", status, value, model, mtAt, reviewed)
	}
	var pv string
	must(s.QueryRow(ctx, `SELECT value FROM translations WHERE entity_type='performer' AND entity_id=$1 AND lang='ko'`, id(perf)).Scan(&pv))
	if pv != "[Korean] 三上悠亜" {
		t.Fatalf("performer ko = %q", pv)
	}
	var cnt int
	must(s.QueryRow(ctx, `SELECT count(*) FROM translations WHERE entity_id=$1 AND entity_type='release'`, id(draft)).Scan(&cnt))
	if cnt != 0 {
		t.Fatal("an unpublished release was translated")
	}
	must(s.QueryRow(ctx, `SELECT value FROM translations WHERE entity_type='release' AND entity_id=$1 AND field='title' AND lang='zh-CN'`, id(pub2)).Scan(&pv))
	if pv != "素人之夏" {
		t.Fatalf("human row overwritten: %q", pv)
	}
	// Machine output is never public.
	set, err := s.PublicTranslations(ctx, []string{"zh-CN", "en"}, []i18n.Ref{{EntityType: "release", EntityID: id(pub), Field: "title"}})
	must(err)
	if len(set) != 0 {
		t.Fatalf("machine output served publicly: %v", set)
	}
	// Provenance in the workbench.
	tasks, _, err := s.TranslationTasks(ctx, TaskFilter{Lang: "zh-CN", Status: "machine"})
	must(err)
	if len(tasks) != 1 || tasks[0].MTModel == nil || *tasks[0].MTModel != "google/gemma-4-31b-it:free" || tasks[0].MTAt == nil {
		t.Fatalf("tasks = %+v", tasks)
	}
	// Second run: nothing left.
	rep, err = runner().Run(ctx)
	must(err)
	if rep.Requested+rep.Missing != 0 {
		t.Fatalf("second run = %+v", rep)
	}

	// A human edit takes the row out of machine hands, keeping provenance.
	ref1 := i18n.Ref{EntityType: "release", EntityID: id(pub), Field: "title"}
	task, err := s.UpsertTranslation(ctx, ref1, "en", "Married Hot Spring Trip", "human")
	must(err)
	if task.Status != "draft" || task.MTModel == nil {
		t.Fatalf("edited task = %+v", task)
	}
	if n, err = s.QueueMachineTranslations(ctx, "en", []i18n.Ref{ref1}, "admin"); err != nil || n != 0 {
		t.Fatalf("queue over a draft = %d, %v", n, err)
	}
	// Machine drafts go through the normal review flow.
	_, err = s.TransitionTranslation(ctx, ref1, "zh-CN", i18n.ActionSubmit, "rev")
	must(err)
	_, err = s.TransitionTranslation(ctx, ref1, "zh-CN", i18n.ActionReview, "rev")
	must(err)
	if set, _ = s.PublicTranslations(ctx, []string{"zh-CN"}, []i18n.Ref{ref1}); set[ref1]["zh-CN"] != "[Simplified] 人妻温泉旅行" {
		t.Fatalf("reviewed machine draft not public: %v", set)
	}

	// The source changed while translating: the save is refused.
	it := mtjob.Item{Ref: i18n.Ref{EntityType: "release", EntityID: id(pub2), Field: "synopsis"}, Lang: "en", Source: "古いあらすじ"}
	if ok, err := s.SaveMachineTranslation(ctx, it, "old", "m", time.Now()); err != nil || ok {
		t.Fatalf("stale save = %v, %v", ok, err)
	}

	// Failures back off: a new title fails, gets an 'auto' request row and
	// is neither retried now nor picked up by the fill.
	var pub3 int64
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('mt-4','新作','published') RETURNING id`).Scan(&pub3))
	fail.Store(true)
	rep, err = runner().Run(ctx)
	must(err)
	if rep.Failed != 2 || rep.Saved != 0 {
		t.Fatalf("failing run = %+v", rep)
	}
	var attempts int
	var by string
	var next time.Time
	must(s.QueryRow(ctx, `SELECT attempts,requested_by,next_try_at FROM mt_requests WHERE entity_id=$1 AND lang='en'`, id(pub3)).Scan(&attempts, &by, &next))
	if attempts != 1 || by != "auto" || time.Until(next) < 50*time.Minute {
		t.Fatalf("backoff row = %d %q %v", attempts, by, next)
	}
	must(s.FailMTRequest(ctx, mtjob.Item{Ref: i18n.Ref{EntityType: "release", EntityID: id(pub3), Field: "title"}, Lang: "en"}, "again"))
	must(s.FailMTRequest(ctx, mtjob.Item{Ref: i18n.Ref{EntityType: "release", EntityID: id(pub3), Field: "title"}, Lang: "en"}, "again"))
	must(s.QueryRow(ctx, `SELECT attempts,next_try_at FROM mt_requests WHERE entity_id=$1 AND lang='en'`, id(pub3)).Scan(&attempts, &next))
	if attempts != 3 || time.Until(next) < 29*24*time.Hour {
		t.Fatalf("give-up row = %d %v", attempts, next)
	}
	fail.Store(false)
	rep, err = runner().Run(ctx)
	must(err)
	if rep.Requested+rep.Missing != 0 {
		t.Fatalf("backing-off items picked up: %+v", rep)
	}
	st, err := s.MachineTranslationStatus(ctx, MTProviderOpenRouter, time.Now())
	must(err)
	if st.UsedToday != int(calls.Load()) || st.Pending != 0 || st.Failing != 2 {
		t.Fatalf("status = %+v, calls = %d", st, calls.Load())
	}

	// The daily cap is shared through mt_usage.
	day := time.Date(2030, 1, 2, 23, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ok, err := s.ReserveMTRequest(ctx, "cap-test", day, 2)
		must(err)
		if ok != (i < 2) {
			t.Fatalf("reserve %d = %v", i, ok)
		}
	}
	if ok, _ := s.ReserveMTRequest(ctx, "cap-test", day.Add(2*time.Hour), 2); !ok {
		t.Fatal("next UTC day should have a fresh budget")
	}
	if ok, _ := s.ReserveMTRequest(ctx, "cap-zero", day, 0); ok {
		t.Fatal("a zero cap allows nothing")
	}
	var runs int
	must(s.QueryRow(ctx, `SELECT count(*) FROM nightly_runs WHERE job=$1`, mtjob.JobKind).Scan(&runs))
	if runs != 4 {
		t.Fatalf("nightly_runs = %d, want 4", runs)
	}
	if id, ok, err := s.EnqueueKindNow(ctx, JobKindMachineTranslation); err != nil || !ok || id == 0 {
		t.Fatalf("enqueue = %d %v %v", id, ok, err)
	}
}
