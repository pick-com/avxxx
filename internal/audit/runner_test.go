package audit

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

type reviewEvent struct {
	entityType string
	entityID   int64
	action     string
	actor      string
}

// fakeStore mirrors the PostgreSQL semantics of audit.Store in memory:
// one open issue per (rule, entity), dismissed fingerprints suppress,
// holds only move publishable releases, every change writes a review event.
type fakeStore struct {
	releases map[int64]*ReleaseFacts
	text     []TextField
	issues   []*Issue
	events   []reviewEvent
	runs     []Run
	nextID   int64
	failOn   string // method name that returns an error

	sourceFacts   []SourceHealthFacts
	sourceHasRuns bool

	names []PerformerNameFacts // R-PERFORMER-NAME input, id order
}

func newFake(rels ...ReleaseFacts) *fakeStore {
	f := &fakeStore{releases: map[int64]*ReleaseFacts{}}
	for i := range rels {
		r := rels[i]
		if r.Status == "" {
			r.Status = "published"
		}
		f.releases[r.ID] = &r
	}
	return f
}

func (f *fakeStore) fail(name string) error {
	if f.failOn == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (f *fakeStore) PublishedReleases(_ context.Context, after int64, limit int) ([]ReleaseFacts, error) {
	if err := f.fail("PublishedReleases"); err != nil {
		return nil, err
	}
	var ids []int64
	for id, r := range f.releases {
		if id > after && r.Status == "published" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []ReleaseFacts
	for _, id := range ids {
		if len(out) == limit {
			break
		}
		out = append(out, *f.releases[id])
	}
	return out, nil
}

func (f *fakeStore) ReleaseFacts(_ context.Context, id int64) (ReleaseFacts, error) {
	r, ok := f.releases[id]
	if !ok {
		return ReleaseFacts{}, errors.New("no rows")
	}
	return *r, nil
}

func (f *fakeStore) HoldRelease(_ context.Context, id int64, reason string, _ map[string]any) (bool, error) {
	if err := f.fail("HoldRelease"); err != nil {
		return false, err
	}
	r := f.releases[id]
	switch r.Status {
	case "published", "draft", "upcoming":
	default:
		return false, nil
	}
	r.Status = HoldStatus
	f.events = append(f.events, reviewEvent{"release", id, "hold", Actor})
	return true, nil
}

func (f *fakeStore) OpenIssue(_ context.Context, is Issue) (OpenResult, error) {
	for _, x := range f.issues {
		if x.Rule == is.Rule && x.EntityType == is.EntityType && x.EntityID == is.EntityID && x.Fingerprint == is.Fingerprint && x.Status == StatusDismissed {
			return IssueSuppressed, nil
		}
	}
	for _, x := range f.issues {
		if x.Rule == is.Rule && x.EntityType == is.EntityType && x.EntityID == is.EntityID && x.Status == StatusOpen {
			x.Detail, x.Fingerprint, x.Priority, x.Severity, x.LastSeenAt = is.Detail, is.Fingerprint, is.Priority, is.Severity, is.LastSeenAt
			return IssueRefreshed, nil
		}
	}
	f.nextID++
	is.ID, is.Status = f.nextID, StatusOpen
	f.issues = append(f.issues, &is)
	return IssueCreated, nil
}

func (f *fakeStore) ResolveStale(_ context.Context, rule Rule, before time.Time) (int, error) {
	n := 0
	for _, x := range f.issues {
		if x.Rule == rule && x.Status == StatusOpen && x.LastSeenAt.Before(before) {
			x.Status = StatusFixed
			n++
		}
	}
	return n, nil
}

func (f *fakeStore) TextFieldsNeedingTrim(_ context.Context, limit int) ([]TextField, error) {
	if len(f.text) > limit {
		return f.text[:limit], nil
	}
	return f.text, nil
}

func (f *fakeStore) FixTextField(_ context.Context, t TextField, v string) (bool, error) {
	for i := range f.text {
		if f.text[i] == t {
			f.text[i].Value = v
			f.events = append(f.events, reviewEvent{t.EntityType, t.EntityID, "auto_fix", Actor})
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) RecordRun(_ context.Context, r Run) error {
	f.runs = append(f.runs, r)
	return nil
}

func (f *fakeStore) SourceHealthIssues(context.Context) ([]SourceHealthFacts, bool, error) {
	if err := f.fail("SourceHealthIssues"); err != nil {
		return nil, false, err
	}
	return f.sourceFacts, f.sourceHasRuns, nil
}

func (f *fakeStore) PerformerNames(_ context.Context, after int64, limit int) ([]PerformerNameFacts, error) {
	if err := f.fail("PerformerNames"); err != nil {
		return nil, err
	}
	var out []PerformerNameFacts
	for _, p := range f.names {
		if p.ID > after && len(out) < limit {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeStore) open(rule Rule) []*Issue {
	var out []*Issue
	for _, x := range f.issues {
		if x.Rule == rule && x.Status == StatusOpen {
			out = append(out, x)
		}
	}
	return out
}

// clock returns a Now func that advances one second per call.
func clock() func() time.Time {
	t := time.Date(2026, 10, 15, 4, 30, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func catalog() []ReleaseFacts {
	adult := PerformerFacts{ID: 100, Name: "Adult", BirthDate: d("1995-01-01")}
	minor := PerformerFacts{ID: 101, Name: "Minor", BirthDate: d("2004-01-01")}
	late18 := PerformerFacts{ID: 102, Name: "Late", BirthDate: d("2002-10-01")}
	noBirth := PerformerFacts{ID: 103, Name: "Unknown"}
	return []ReleaseFacts{
		{ID: 1, Title: "fine", ReleaseDate: d("2020-12-01"), RuntimeMinutes: 120, Performers: []PerformerFacts{adult}, Popularity: 5},
		{ID: 2, Title: "minor", ReleaseDate: d("2020-12-01"), RuntimeMinutes: 120, Performers: []PerformerFacts{adult, minor}, Popularity: 50},
		{ID: 3, Title: "late 18", ReleaseDate: d("2020-12-01"), RuntimeMinutes: 120, Performers: []PerformerFacts{late18}},
		{ID: 4, Title: "gaps", Performers: []PerformerFacts{noBirth}, Popularity: 7},
		{ID: 5, Title: "gaps too", ReleaseDate: d("2021-01-01"), RuntimeMinutes: 90, Performers: []PerformerFacts{noBirth}, Popularity: 3},
		{ID: 6, Title: "draft minor", Status: "draft", ReleaseDate: d("2020-12-01"), RuntimeMinutes: 1, Performers: []PerformerFacts{minor}},
	}
}

func TestRunnerHoldsMinorsAndOpensIssues(t *testing.T) {
	f := newFake(catalog()...)
	f.text = []TextField{
		{"release", 1, "canonical_title", "  fine  title "},
		{"performer", 100, "canonical_name", "Adult"},
		{"performer", 101, "canonical_name", "   "},
	}
	r := &Runner{Store: f, Now: clock(), BatchSize: 2, JobID: 9}
	rep, err := r.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// R-AGE: releases 2 (under 18) and 3 (conservative 18) held; draft 6 not
	// scanned by the nightly run; 1 untouched.
	for id, want := range map[int64]string{1: "published", 2: HoldStatus, 3: HoldStatus, 4: "published", 6: "draft"} {
		if got := f.releases[id].Status; got != want {
			t.Errorf("release %d status %s, want %s", id, got, want)
		}
	}
	age := f.open(RuleAge)
	if len(age) != 2 || age[0].Severity != SeverityBlocking || age[0].EntityID != 2 || age[0].Priority != 50 {
		t.Fatalf("R-AGE issues: %+v", age)
	}
	holds := 0
	for _, e := range f.events {
		if e.action == "hold" {
			holds++
			if e.actor != Actor {
				t.Errorf("hold event actor %q", e.actor)
			}
		}
	}
	if holds != 2 {
		t.Errorf("hold events = %d, want 2", holds)
	}

	// R-META: after R-AGE held 2 and 3, the published set is 1, 4, 5.
	// Release 4 misses release_date+runtime; performer 103 misses birth date
	// and is ranked by the popularity of both its releases (7+3).
	meta := f.open(RuleMeta)
	if len(meta) != 2 {
		t.Fatalf("R-META issues: %+v", meta)
	}
	byEntity := map[string]*Issue{}
	for _, x := range meta {
		byEntity[x.EntityType] = x
	}
	if rel := byEntity["release"]; rel == nil || rel.EntityID != 4 || rel.Severity != SeverityHigh || rel.Priority != 7 {
		t.Errorf("release meta issue: %+v", rel)
	}
	if p := byEntity["performer"]; p == nil || p.EntityID != 103 || p.Priority != 10 {
		t.Errorf("performer meta issue: %+v", p)
	}

	// R-TITLE: one fix, the blank name left alone.
	if f.text[0].Value != "fine title" || f.text[2].Value != "   " {
		t.Errorf("text after fix: %+v", f.text)
	}

	// One nightly_runs row per rule, in order, with the job id.
	if len(f.runs) != len(AllRules) || len(rep.Runs) != len(AllRules) {
		t.Fatalf("runs = %d, want %d", len(f.runs), len(AllRules))
	}
	want := map[string]string{
		"audit.R-AGE": ResultIssues, "audit.R-META": ResultIssues, "audit.R-TITLE": ResultIssues,
		"audit.R-STATS": ResultNotApplicable, "audit.R-SOURCE": ResultNotApplicable, "audit.R-SEO": ResultNotApplicable,
	}
	for i, run := range f.runs {
		if run.Job != "audit."+string(AllRules[i]) || run.JobID != 9 {
			t.Errorf("run %d = %s job %d", i, run.Job, run.JobID)
		}
		if w, ok := want[run.Job]; ok && run.Result != w {
			t.Errorf("%s result %s, want %s", run.Job, run.Result, w)
		}
		if run.Result == ResultNotApplicable && run.Detail["note"] == "" {
			t.Errorf("%s stub has no note", run.Job)
		}
	}
	if f.runs[0].Checked != 5 || f.runs[0].Issues != 2 || f.runs[0].Detail["held"] != 2 {
		t.Errorf("R-AGE run: %+v", f.runs[0])
	}
	if f.runs[2].Fixed != 1 || f.runs[2].Checked != 3 {
		t.Errorf("R-TITLE run: %+v", f.runs[2])
	}
}

func TestRunnerIsIdempotent(t *testing.T) {
	f := newFake(catalog()...)
	r := &Runner{Store: f, Now: clock()}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	issues, events := len(f.issues), len(f.events)
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.issues) != issues {
		t.Errorf("rerun created issues: %d → %d", issues, len(f.issues))
	}
	if len(f.events) != events {
		t.Errorf("rerun wrote review events: %d → %d", events, len(f.events))
	}
	// Held releases stay held and their R-AGE issues stay open (never auto-resolved).
	if len(f.open(RuleAge)) != 2 || f.releases[2].Status != HoldStatus {
		t.Errorf("R-AGE after rerun: %d open, release 2 %s", len(f.open(RuleAge)), f.releases[2].Status)
	}
}

func TestRunnerResolvesFixedMetaButNeverUnholds(t *testing.T) {
	f := newFake(catalog()...)
	r := &Runner{Store: f, Now: clock()}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Someone fills in the gaps and corrects the minor's birth date.
	f.releases[4].ReleaseDate, f.releases[4].RuntimeMinutes = d("2021-02-01"), 100
	for _, rel := range f.releases {
		for i := range rel.Performers {
			if rel.Performers[i].BirthDate == nil {
				rel.Performers[i].BirthDate = d("1990-01-01")
			}
			if rel.Performers[i].ID == 101 {
				rel.Performers[i].BirthDate = d("1990-01-01")
			}
		}
	}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.open(RuleMeta)); n != 0 {
		t.Errorf("R-META open after fix: %d", n)
	}
	if f.runs[len(AllRules)+1].Fixed != 2 {
		t.Errorf("R-META second run fixed = %d, want 2", f.runs[len(AllRules)+1].Fixed)
	}
	if f.releases[2].Status != HoldStatus || len(f.open(RuleAge)) != 2 {
		t.Errorf("release 2 must stay held with its issue open: %s, %d", f.releases[2].Status, len(f.open(RuleAge)))
	}
}

func TestRunnerRespectsDismissal(t *testing.T) {
	f := newFake(catalog()[:2]...)
	r := &Runner{Store: f, Now: clock()}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	is := f.open(RuleAge)[0]
	is.Status = StatusDismissed
	f.releases[2].Status = "published" // a human reviewed and republished it
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.releases[2].Status != "published" || len(f.open(RuleAge)) != 0 {
		t.Errorf("dismissed finding re-raised: status %s, open %d", f.releases[2].Status, len(f.open(RuleAge)))
	}
	// Different data → different fingerprint → raised and held again.
	f.releases[2].Performers[1].BirthDate = d("2005-01-01")
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.releases[2].Status != HoldStatus || len(f.open(RuleAge)) != 1 {
		t.Errorf("changed finding not raised: status %s, open %d", f.releases[2].Status, len(f.open(RuleAge)))
	}
}

func TestRunnerRecordsFailuresAndContinues(t *testing.T) {
	f := newFake(catalog()...)
	f.failOn = "HoldRelease"
	_, err := (&Runner{Store: f, Now: clock()}).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "R-AGE") {
		t.Fatalf("err = %v, want R-AGE failure", err)
	}
	if len(f.runs) != len(AllRules) || f.runs[0].Result != ResultFailed || f.runs[0].Detail["error"] == nil {
		t.Fatalf("runs: %+v", f.runs[0])
	}
	if f.runs[1].Result == ResultFailed {
		t.Errorf("R-META should still run: %+v", f.runs[1])
	}
}

func TestRunnerMetaFailureDoesNotResolve(t *testing.T) {
	f := newFake(catalog()...)
	r := &Runner{Store: f, Now: clock()}
	if _, err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	open := len(f.open(RuleMeta))
	f.failOn = "PublishedReleases"
	if _, err := r.Run(context.Background()); err == nil {
		t.Fatal("want error")
	}
	if got := len(f.open(RuleMeta)); got != open {
		t.Errorf("failed scan resolved issues: %d → %d", open, got)
	}
}

func TestCheckReleaseInbound(t *testing.T) {
	f := newFake(catalog()...)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	res, err := CheckRelease(context.Background(), f, 6, AgeConfig{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Held || f.releases[6].Status != HoldStatus || len(res.Age) != 1 {
		t.Fatalf("draft minor not held: %+v, status %s", res, f.releases[6].Status)
	}
	res, err = CheckRelease(context.Background(), f, 4, AgeConfig{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Held || len(res.Missing) != 2 || len(res.MissingBirth) != 1 || len(f.open(RuleMeta)) != 2 {
		t.Fatalf("gaps: %+v, open meta %d", res, len(f.open(RuleMeta)))
	}
	// Inbound never resolves stale issues: re-checking a fixed release leaves
	// the issue for the nightly run to close.
	f.releases[4].ReleaseDate, f.releases[4].RuntimeMinutes = d("2021-01-01"), 90
	if _, err = CheckRelease(context.Background(), f, 4, AgeConfig{}, now); err != nil {
		t.Fatal(err)
	}
	if len(f.open(RuleMeta)) != 2 {
		t.Errorf("inbound resolved issues")
	}
}
