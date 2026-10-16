package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Defaults for Runner.
const (
	DefaultBatchSize = 500
	DefaultTrimLimit = 5000
)

// stubNotes documents why each stub rule is not applicable yet. They still
// get a nightly_runs row so the dashboard shows all rules.
var stubNotes = map[Rule]string{
	RuleWatermark:   "needs frame sampling of self-hosted media; no media pipeline output is stored yet",
	RuleSubtitles:   "needs subtitle tracks per release; no subtitle table exists yet",
	RuleScreenshots: "needs screenshot/chapter assets per release; media_assets has no screenshot asset type populated yet",
}

// StubNote returns the reason a stub rule is not applicable, or "".
func StubNote(r Rule) string { return stubNotes[r] }

// Runner executes the nightly audit over a Store.
type Runner struct {
	Store     Store
	Age       AgeConfig
	Now       func() time.Time // defaults to time.Now
	BatchSize int              // releases per PublishedReleases page
	TrimLimit int              // max R-TITLE candidates per run
	JobID     int64            // jobs.id, recorded on nightly_runs rows
	// EventRetention is how long raw events are kept (default
	// DefaultEventRetention); R-STATS only rebuilds aggregates inside it.
	EventRetention time.Duration
	// Rules limits a run to these rules (still in AllRules order); empty
	// runs them all. audit -once -rules R-SEO runs one rule by hand.
	Rules []Rule
}

func (r *Runner) selected(rule Rule) bool {
	if len(r.Rules) == 0 {
		return true
	}
	for _, x := range r.Rules {
		if x == rule {
			return true
		}
	}
	return false
}

// Report summarizes one full run, one entry per rule in AllRules order.
type Report struct {
	StartedAt time.Time `json:"started_at"`
	Runs      []Run     `json:"runs"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Runner) batch() int {
	if r.BatchSize > 0 {
		return r.BatchSize
	}
	return DefaultBatchSize
}

func (r *Runner) age() AgeConfig {
	if r.Age == (AgeConfig{}) {
		return DefaultAgeConfig
	}
	return r.Age
}

// ruleOutcome is what a rule pass returns to Run.
type ruleOutcome struct {
	checked, fixed, issues int
	notApplicable          bool
	detail                 map[string]any
}

// Run executes every rule in AllRules order (or the selected Rules) and
// records one nightly_runs row per rule. A failing rule does not stop the others; their errors are joined
// and returned so the job is marked failed and retried.
func (r *Runner) Run(ctx context.Context) (Report, error) {
	rep := Report{StartedAt: r.now()}
	var errs []error
	for _, rule := range AllRules {
		if !r.selected(rule) {
			continue
		}
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		start := r.now()
		out, err := r.runRule(ctx, rule, start)
		run := Run{Job: "audit." + string(rule), JobID: r.JobID, StartedAt: start, DurationMS: r.now().Sub(start).Milliseconds(),
			Checked: out.checked, Fixed: out.fixed, Issues: out.issues, Detail: out.detail}
		switch {
		case err != nil:
			run.Result = ResultFailed
			if run.Detail == nil {
				run.Detail = map[string]any{}
			}
			run.Detail["error"] = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", rule, err))
		case out.notApplicable:
			run.Result = ResultNotApplicable
		case out.fixed > 0 || out.issues > 0:
			run.Result = ResultIssues
		default:
			run.Result = ResultOK
		}
		if recErr := r.Store.RecordRun(ctx, run); recErr != nil {
			errs = append(errs, fmt.Errorf("record %s run: %w", rule, recErr))
		}
		rep.Runs = append(rep.Runs, run)
	}
	return rep, errors.Join(errs...)
}

func (r *Runner) runRule(ctx context.Context, rule Rule, start time.Time) (ruleOutcome, error) {
	switch rule {
	case RuleAge:
		return r.ruleAge(ctx, start)
	case RuleMeta:
		return r.ruleMeta(ctx, start)
	case RuleTitle:
		return r.ruleTitle(ctx)
	case RuleSourceHealth:
		return r.ruleSource(ctx, start)
	case RulePerformerName:
		return r.rulePerformerName(ctx, start)
	case RuleSEO:
		return r.ruleSEO(ctx, start)
	case RuleStats:
		return r.ruleStats(ctx, start)
	}
	if note, ok := stubNotes[rule]; ok {
		return ruleOutcome{notApplicable: true, detail: map[string]any{"note": note}}, nil
	}
	return ruleOutcome{}, fmt.Errorf("unknown rule %s", rule)
}

// eachPublished pages through every published release.
func (r *Runner) eachPublished(ctx context.Context, fn func(ReleaseFacts) error) (int, error) {
	n := 0
	var after int64
	for {
		page, err := r.Store.PublishedReleases(ctx, after, r.batch())
		if err != nil {
			return n, err
		}
		for _, rel := range page {
			n++
			if err = fn(rel); err != nil {
				return n, err
			}
			after = rel.ID
		}
		if len(page) < r.batch() {
			return n, nil
		}
	}
}

// ---------------------------------------------------------------- R-AGE

func (r *Runner) ruleAge(ctx context.Context, start time.Time) (ruleOutcome, error) {
	out := ruleOutcome{detail: map[string]any{}}
	held := 0
	checked, err := r.eachPublished(ctx, func(rel ReleaseFacts) error {
		res, h, err := applyAge(ctx, r.Store, rel, r.age(), start)
		if res != nil && *res != IssueSuppressed {
			out.issues++
		}
		if h {
			held++
		}
		return err
	})
	out.checked = checked
	out.detail["held"] = held
	return out, err
}

// applyAge checks one release and, for any finding, opens a blocking issue
// and holds the release. A finding an admin already dismissed (same
// fingerprint) neither reopens the issue nor re-holds the release. It
// returns the issue outcome (nil when there was no finding) and whether the
// release status changed.
func applyAge(ctx context.Context, st Store, rel ReleaseFacts, cfg AgeConfig, seen time.Time) (*OpenResult, bool, error) {
	findings := CheckAge(rel, cfg)
	if len(findings) == 0 {
		return nil, false, nil
	}
	detail := map[string]any{"title": rel.Title, "performers": findings, "action": "hold:" + HoldStatus}
	if rel.ReleaseDate != nil {
		detail["release_date"] = rel.ReleaseDate.Format(time.DateOnly)
	}
	if rel.ShootYear > 0 {
		detail["shoot_year"] = rel.ShootYear
	}
	res, err := st.OpenIssue(ctx, Issue{Rule: RuleAge, EntityType: "release", EntityID: rel.ID, Severity: SeverityBlocking,
		Detail: detail, Fingerprint: ageFingerprint(findings), Priority: rel.Popularity, LastSeenAt: seen})
	if err != nil {
		return nil, false, err
	}
	if res == IssueSuppressed {
		return &res, false, nil
	}
	names := make([]string, 0, len(findings))
	for _, f := range findings {
		names = append(names, fmt.Sprintf("%s (%d, %s)", f.Name, f.Age, f.Reason))
	}
	changed, err := st.HoldRelease(ctx, rel.ID, "R-AGE: age at filming may be under 18: "+strings.Join(names, ", "),
		map[string]any{"rule": RuleAge, "performers": findings, "from_status": rel.Status})
	return &res, changed, err
}

// ---------------------------------------------------------------- R-META

type birthGap struct {
	p        PerformerFacts
	priority int64
	releases int
}

func (r *Runner) ruleMeta(ctx context.Context, start time.Time) (ruleOutcome, error) {
	out := ruleOutcome{detail: map[string]any{}}
	gaps := map[int64]*birthGap{}
	var order []int64
	releaseIssues := 0
	checked, err := r.eachPublished(ctx, func(rel ReleaseFacts) error {
		m := CheckMeta(rel)
		for _, p := range m.MissingBirth {
			g := gaps[p.ID]
			if g == nil {
				g = &birthGap{p: p}
				gaps[p.ID] = g
				order = append(order, p.ID)
			}
			g.priority += rel.Popularity
			g.releases++
		}
		if len(m.Release) == 0 {
			return nil
		}
		res, err := r.Store.OpenIssue(ctx, metaReleaseIssue(rel, m.Release, start))
		if err == nil && res != IssueSuppressed {
			releaseIssues++
		}
		return err
	})
	out.checked = checked
	if err != nil {
		return out, err
	}
	performerIssues := 0
	for _, id := range order {
		g := gaps[id]
		res, err := r.Store.OpenIssue(ctx, metaBirthIssue(g.p, g.priority, g.releases, start))
		if err != nil {
			return out, err
		}
		if res != IssueSuppressed {
			performerIssues++
		}
	}
	resolved, err := r.Store.ResolveStale(ctx, RuleMeta, start)
	out.issues = releaseIssues + performerIssues
	out.fixed = resolved
	out.detail["release_issues"] = releaseIssues
	out.detail["performer_issues"] = performerIssues
	out.detail["resolved_stale"] = resolved
	return out, err
}

func metaReleaseIssue(rel ReleaseFacts, missing []string, seen time.Time) Issue {
	sev := SeverityMedium
	for _, f := range missing {
		if f == "release_date" || f == "performers" {
			sev = SeverityHigh // age at filming cannot be checked
		}
	}
	return Issue{Rule: RuleMeta, EntityType: "release", EntityID: rel.ID, Severity: sev,
		Detail:      map[string]any{"title": rel.Title, "missing": missing},
		Fingerprint: Fingerprint(append([]string{"release"}, missing...)...), Priority: rel.Popularity, LastSeenAt: seen}
}

func metaBirthIssue(p PerformerFacts, priority int64, releases int, seen time.Time) Issue {
	return Issue{Rule: RuleMeta, EntityType: "performer", EntityID: p.ID, Severity: SeverityHigh,
		Detail:      map[string]any{"name": p.Name, "missing": []string{"birth_date"}, "published_releases": releases},
		Fingerprint: Fingerprint("performer", "birth_date"), Priority: priority, LastSeenAt: seen}
}

// ---------------------------------------------------------------- R-TITLE

func (r *Runner) ruleTitle(ctx context.Context) (ruleOutcome, error) {
	limit := r.TrimLimit
	if limit <= 0 {
		limit = DefaultTrimLimit
	}
	out := ruleOutcome{detail: map[string]any{}}
	fields, err := r.Store.TextFieldsNeedingTrim(ctx, limit)
	if err != nil {
		return out, err
	}
	out.checked = len(fields)
	var examples []map[string]any
	skipped := 0
	for _, f := range fields {
		v := NormalizeSpace(f.Value)
		if v == f.Value {
			continue
		}
		if v == "" { // never blank a name or title; leave it for a human
			skipped++
			continue
		}
		changed, err := r.Store.FixTextField(ctx, f, v)
		if err != nil {
			return out, err
		}
		if changed {
			out.fixed++
			if len(examples) < 10 {
				examples = append(examples, map[string]any{"entity_type": f.EntityType, "entity_id": f.EntityID, "field": f.Field, "before": f.Value, "after": v})
			}
		}
	}
	out.detail["examples"] = examples
	out.detail["skipped_blank"] = skipped
	out.detail["truncated"] = len(fields) >= limit
	return out, nil
}

// ---------------------------------------------------------------- R-SOURCE

// ruleSource keeps one open issue per playback source the source health job
// degraded or disabled (it opens them as it changes status; this pass
// re-raises dismissed-then-changed findings and resolves sources that
// recovered or were re-enabled by an admin).
func (r *Runner) ruleSource(ctx context.Context, start time.Time) (ruleOutcome, error) {
	facts, hasRuns, err := r.Store.SourceHealthIssues(ctx)
	if err != nil {
		return ruleOutcome{}, err
	}
	if !hasRuns {
		return ruleOutcome{notApplicable: true, detail: map[string]any{"note": "the source_health job has not recorded a run yet"}}, nil
	}
	out := ruleOutcome{checked: len(facts), detail: map[string]any{}}
	byStatus := map[string]int{}
	for _, f := range facts {
		res, err := r.Store.OpenIssue(ctx, SourceIssue(f, start))
		if err != nil {
			return out, err
		}
		if res != IssueSuppressed {
			out.issues++
			byStatus[f.Status]++
		}
	}
	resolved, err := r.Store.ResolveStale(ctx, RuleSourceHealth, start)
	out.fixed = resolved
	out.detail["by_status"] = byStatus
	out.detail["resolved_stale"] = resolved
	return out, err
}

// ---------------------------------------------------------------- inbound

// InboundResult is the outcome of CheckRelease.
type InboundResult struct {
	ReleaseID int64        `json:"release_id"`
	Age       []AgeFinding `json:"age_findings,omitempty"`
	Held      bool         `json:"held"`
	Missing   []string     `json:"missing,omitempty"`
	// MissingBirth lists performer ids without a birth date.
	MissingBirth []int64 `json:"missing_birth,omitempty"`
}

// CheckRelease runs R-AGE and R-META for one release, whatever its status
// (catalog ingest calls it after saving a record, before publishing). It
// opens issues and holds the release exactly as the nightly run would, but
// never resolves stale issues.
func CheckRelease(ctx context.Context, s Store, releaseID int64, cfg AgeConfig, now time.Time) (InboundResult, error) {
	if cfg == (AgeConfig{}) {
		cfg = DefaultAgeConfig
	}
	res := InboundResult{ReleaseID: releaseID}
	rel, err := s.ReleaseFacts(ctx, releaseID)
	if err != nil {
		return res, err
	}
	res.Age = CheckAge(rel, cfg)
	if _, res.Held, err = applyAge(ctx, s, rel, cfg, now); err != nil {
		return res, err
	}
	m := CheckMeta(rel)
	res.Missing = m.Release
	if len(m.Release) > 0 {
		if _, err = s.OpenIssue(ctx, metaReleaseIssue(rel, m.Release, now)); err != nil {
			return res, err
		}
	}
	for _, p := range m.MissingBirth {
		res.MissingBirth = append(res.MissingBirth, p.ID)
		if _, err = s.OpenIssue(ctx, metaBirthIssue(p, rel.Popularity, 1, now)); err != nil {
			return res, err
		}
	}
	return res, nil
}
