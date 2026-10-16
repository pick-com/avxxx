// Package mtjob is the sourceless 'i18n_machine' job (ROADMAP 机器翻译,
// tech plan 3.10): it machine-translates catalog fields with an
// i18n.Translator (OpenRouter) and stores the output as status 'machine'
// drafts with provenance (model id, time). Machine output is never public:
// a translator submits it and a reviewer approves it in /admin/i18n.
//
// A run first works through the fields admins selected (POST
// /api/admin/i18n/machine-translate, table mt_requests), then fills missing
// titles of published releases in the configured target languages
// (I18N_MT_LANGS), bounded by I18N_MT_MAX_PER_RUN strings. cmd/audit runs
// it; cmd/scheduler enqueues it every I18N_MT_EVERY (default 1h).
//
// Only fields with no translation, or an earlier machine draft nobody has
// edited, are ever written: drafts, reviewed and stale rows belong to
// humans. The store enforces this in SQL as well. Performer real names are
// never sent: they are not a translatable field (i18n.Fields has the stage
// name and biography only), and the background fill only does release
// titles.
package mtjob

import (
	"avxxx/internal/audit"
	"avxxx/internal/i18n"
	"avxxx/internal/obs"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// JobKind is jobs.kind of the machine translation job.
const JobKind = "i18n_machine"

// Defaults.
const (
	DefaultMaxPerRun = 60
	// MaxBatchRunes bounds the source text in one request (synopses are long).
	MaxBatchRunes = 6000
	// MaxAttempts is how often a failing field is retried before the job
	// leaves it alone for GiveUpFor.
	MaxAttempts = 3
	GiveUpFor   = 30 * 24 * time.Hour
	// maxErrorStreak consecutive failed batches stop the run.
	maxErrorStreak = 3
)

// DefaultLangs are the target languages of the background fill.
var DefaultLangs = []string{i18n.ZhCN, i18n.ZhTW, i18n.En}

// Item is one field to translate into Lang. Source is the source text the
// translation is made from; the store only saves the result while the
// source is unchanged.
type Item struct {
	Ref    i18n.Ref
	Lang   string
	Source string
	// Requested marks an admin-selected field (an mt_requests row).
	Requested bool
}

// Store is what the job needs from the database (internal/store/i18n_mt.go).
type Store interface {
	// DueMTRequests returns up to limit admin-requested fields due at now
	// that are still eligible, oldest request first, dropping requests whose
	// field became ineligible (edited by a human, or gone).
	DueMTRequests(ctx context.Context, now time.Time, limit int) ([]Item, error)
	// MissingReleaseTitles returns up to limit titles of published releases
	// that have no translation (and no pending request) in one of langs,
	// newest release first.
	MissingReleaseTitles(ctx context.Context, langs []string, limit int) ([]Item, error)
	// MTGlossary returns the glossary terms of lang.
	MTGlossary(ctx context.Context, lang string) ([]i18n.GlossaryTerm, error)
	// SaveMachineTranslation stores value as a machine draft of it with
	// provenance, unless a human row exists or the source changed since;
	// saved reports whether it was written. It clears the request.
	SaveMachineTranslation(ctx context.Context, it Item, value, model string, at time.Time) (saved bool, err error)
	// FailMTRequest records a failed attempt for it (creating the request
	// row for a background item) and backs it off (Backoff of its attempts).
	FailMTRequest(ctx context.Context, it Item, reason string) error
	RecordRun(ctx context.Context, r audit.Run) error
}

// Config bounds a run.
type Config struct {
	Langs     []string
	MaxPerRun int
	BatchSize int
}

// ConfigFromEnv reads I18N_MT_LANGS (comma-separated, default
// zh-CN,zh-TW,en; the source language ja is skipped) and
// I18N_MT_MAX_PER_RUN (default 60). Invalid values keep the defaults with a
// warning. BatchSize comes from the translator (OPENROUTER_BATCH_SIZE).
func ConfigFromEnv(getenv func(string) string) (Config, []string) {
	var c Config
	var warn []string
	if v := strings.TrimSpace(getenv("I18N_MT_LANGS")); v != "" {
		for _, l := range strings.Split(v, ",") {
			l = strings.TrimSpace(l)
			switch {
			case l == "":
			case !i18n.Supported(l):
				warn = append(warn, fmt.Sprintf("I18N_MT_LANGS: unsupported language %q ignored", l))
			case l == i18n.SourceLang:
				warn = append(warn, "I18N_MT_LANGS: ja is the source language, ignored")
			default:
				c.Langs = append(c.Langs, l)
			}
		}
	}
	if v := strings.TrimSpace(getenv("I18N_MT_MAX_PER_RUN")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			warn = append(warn, fmt.Sprintf("I18N_MT_MAX_PER_RUN=%q ignored: want 1-1000", v))
		} else {
			c.MaxPerRun = n
		}
	}
	return c.withDefaults(), warn
}

func (c Config) withDefaults() Config {
	if len(c.Langs) == 0 {
		c.Langs = append([]string(nil), DefaultLangs...)
	}
	if c.MaxPerRun <= 0 {
		c.MaxPerRun = DefaultMaxPerRun
	}
	if c.BatchSize <= 0 {
		c.BatchSize = i18n.DefaultOpenRouterBatchSize
	}
	return c
}

// Runner executes the job.
type Runner struct {
	Store Store
	// Translator is nil when machine translation is not configured: the
	// run then records "not_configured" and changes nothing.
	Translator i18n.Translator
	Config     Config
	JobID      int64
	Now        func() time.Time
}

// Report summarizes one run (also nightly_runs.detail).
type Report struct {
	StartedAt  time.Time      `json:"started_at"`
	Configured bool           `json:"configured"`
	Requested  int            `json:"requested"` // admin-selected fields picked up
	Missing    int            `json:"missing"`   // background release titles picked up
	Batches    int            `json:"batches"`
	Saved      int            `json:"saved"`
	Skipped    int            `json:"skipped"` // a human row appeared or the source changed
	Failed     int            `json:"failed"`
	ByLang     map[string]int `json:"by_lang"` // saved per language
	Models     map[string]int `json:"models"`  // saved per model
	// Stopped: not_configured | daily_cap | rate_limited | error_streak
	Stopped    string `json:"stopped,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Run translates up to MaxPerRun fields and records a nightly_runs row
// 'i18n_machine'. Hitting the daily cap or a persistent 429 ends the run
// early without failing it; store errors fail it.
func (r *Runner) Run(ctx context.Context) (rep Report, err error) {
	start := r.now()
	cfg := r.Config.withDefaults()
	rep = Report{StartedAt: start, Configured: r.Translator != nil, ByLang: map[string]int{}, Models: map[string]int{}}
	defer func() {
		rep.Reason = obs.ScrubString(rep.Reason)
		rep.DurationMS = r.now().Sub(start).Milliseconds()
		run := audit.Run{Job: JobKind, JobID: r.JobID, StartedAt: start, DurationMS: rep.DurationMS,
			Checked: rep.Requested + rep.Missing, Fixed: rep.Saved, Issues: rep.Failed, Result: audit.ResultOK,
			Detail: map[string]any{"saved": rep.Saved, "skipped": rep.Skipped, "failed": rep.Failed, "batches": rep.Batches,
				"by_lang": rep.ByLang, "models": rep.Models, "stopped": rep.Stopped, "reason": rep.Reason}}
		switch {
		case err != nil:
			run.Result = audit.ResultFailed
		case rep.Stopped == "not_configured":
			run.Result = audit.ResultNotApplicable
		case rep.Saved > 0 || rep.Failed > 0:
			run.Result = audit.ResultIssues
		}
		if rerr := r.Store.RecordRun(context.WithoutCancel(ctx), run); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if r.Translator == nil {
		rep.Stopped, rep.Reason = "not_configured", i18n.ErrNotConfigured.Error()
		return rep, nil
	}
	items, err := r.Store.DueMTRequests(ctx, start, cfg.MaxPerRun)
	if err != nil {
		return rep, err
	}
	rep.Requested = len(items)
	if left := cfg.MaxPerRun - len(items); left > 0 && len(cfg.Langs) > 0 {
		missing, err := r.Store.MissingReleaseTitles(ctx, cfg.Langs, left)
		if err != nil {
			return rep, err
		}
		rep.Missing = len(missing)
		items = append(items, missing...)
	}
	glossary := map[string][]i18n.GlossaryTerm{}
	streak := 0
	for _, batch := range Batches(items, cfg.BatchSize, MaxBatchRunes) {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		lang := batch[0].Lang
		g, ok := glossary[lang]
		if !ok {
			if g, err = r.Store.MTGlossary(ctx, lang); err != nil {
				return rep, err
			}
			glossary[lang] = g
		}
		texts := make([]string, len(batch))
		for i, it := range batch {
			texts[i] = it.Source
		}
		rep.Batches++
		res, terr := r.Translator.TranslateBatch(ctx, i18n.Batch{From: "", To: lang, Texts: texts, Glossary: g})
		if terr != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			rep.Reason = terr.Error()
			switch {
			case errors.Is(terr, i18n.ErrDailyCap):
				rep.Stopped = "daily_cap"
				return rep, nil
			case errors.Is(terr, i18n.ErrRateLimited):
				rep.Stopped = "rate_limited"
				return rep, nil
			}
			if err := r.fail(ctx, batch, terr); err != nil {
				return rep, err
			}
			rep.Failed += len(batch)
			if streak++; streak >= maxErrorStreak {
				rep.Stopped = "error_streak"
				return rep, nil
			}
			continue
		}
		streak = 0
		at := r.now()
		for i, it := range batch {
			saved, err := r.Store.SaveMachineTranslation(ctx, it, res.Texts[i], res.Model, at)
			if err != nil {
				return rep, err
			}
			if saved {
				rep.Saved++
				rep.ByLang[lang]++
				rep.Models[res.Model]++
			} else {
				rep.Skipped++
			}
		}
	}
	return rep, nil
}

// fail backs off every item of a failed batch.
func (r *Runner) fail(ctx context.Context, batch []Item, cause error) error {
	reason := obs.ScrubString(cause.Error())
	if len(reason) > 500 {
		reason = reason[:500]
	}
	for _, it := range batch {
		if err := r.Store.FailMTRequest(ctx, it, reason); err != nil {
			return err
		}
	}
	return nil
}

// Backoff is the wait after the n-th failed attempt (1-based): 1h, 6h,
// then GiveUpFor.
func Backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Hour
	case attempts < MaxAttempts:
		return 6 * time.Hour
	}
	return GiveUpFor
}

// Batches groups items by target language, in first-seen order, into
// batches of at most size items and maxRunes source runes (a single longer
// item gets a batch of its own).
func Batches(items []Item, size, maxRunes int) [][]Item {
	var order []string
	byLang := map[string][]Item{}
	for _, it := range items {
		if _, ok := byLang[it.Lang]; !ok {
			order = append(order, it.Lang)
		}
		byLang[it.Lang] = append(byLang[it.Lang], it)
	}
	var out [][]Item
	for _, l := range order {
		var cur []Item
		runes := 0
		for _, it := range byLang[l] {
			n := utf8.RuneCountInString(it.Source)
			if len(cur) > 0 && (len(cur) >= size || runes+n > maxRunes) {
				out = append(out, cur)
				cur, runes = nil, 0
			}
			cur = append(cur, it)
			runes += n
		}
		if len(cur) > 0 {
			out = append(out, cur)
		}
	}
	return out
}
