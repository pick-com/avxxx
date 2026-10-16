package catalog

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Field completeness and normalization checks shared by the fixture
// acceptance harness (cmd/fixturecheck) and manual verify jobs, which parse
// pages but never write to the catalog.

// RequiredFields are the fields a parsed record must carry to count as
// complete (FINAL_DATA_DESIGN 5.4, 11).
var RequiredFields = map[EntityType][]string{
	Performer: {"name", "name_ja", "birth_date", "height_cm"},
	Release:   {"title", "dvd_code", "release_date", "maker", "performers"},
}

// normalizedChecks are the fields whose values must already be in canonical
// form (identifiers and dates); key -> validator.
var (
	dvdCodePattern   = regexp.MustCompile(`^[a-z0-9]+-[0-9]+[a-z]?$`)
	contentIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

func normalizedDate(v string) bool {
	_, err := time.Parse(time.DateOnly, strings.TrimSpace(v))
	return err == nil
}

var normalizedChecks = map[EntityType]map[string]func(string) bool{
	Performer: {"birth_date": normalizedDate, "debut_date": normalizedDate},
	Release: {
		"dvd_code":     func(v string) bool { return dvdCodePattern.MatchString(ReleaseSlug(v, "", "", "")) },
		"content_id":   func(v string) bool { return contentIDPattern.MatchString(NormalizeIdentifier(v)) },
		"release_date": normalizedDate,
	},
}

// present reports whether a parsed field value carries information.
func present(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case []string:
		return len(x) > 0
	case []any:
		return len(x) > 0
	}
	return true
}

// EntityReport aggregates one entity type's parse results.
type EntityReport struct {
	Parsed int `json:"parsed"`
	Failed int `json:"failed"`
	// Present counts records carrying each required field.
	Present map[string]int `json:"present"`
	// Complete counts records carrying every required field.
	Complete int `json:"complete"`
	// Normalized / Checked count identifier and date values in canonical
	// form out of those present.
	Normalized int      `json:"normalized"`
	Checked    int      `json:"checked"`
	Samples    []string `json:"samples,omitempty"`
}

// RequiredPct is the share of required field slots that were present.
func (e EntityReport) RequiredPct(t EntityType) float64 {
	slots := e.Parsed * len(RequiredFields[t])
	if slots == 0 {
		return 0
	}
	n := 0
	for _, c := range e.Present {
		n += c
	}
	return 100 * float64(n) / float64(slots)
}

// NormalizedPct is the share of checked identifier/date values in canonical form.
func (e EntityReport) NormalizedPct() float64 {
	if e.Checked == 0 {
		return 0
	}
	return 100 * float64(e.Normalized) / float64(e.Checked)
}

// ParseReport aggregates parsed records for one source.
type ParseReport struct {
	Source     string                       `json:"source"`
	Entities   map[EntityType]*EntityReport `json:"entities"`
	Failures   []string                     `json:"failures,omitempty"`
	maxSamples int
	mu         sync.Mutex
}

// NewParseReport returns an empty report keeping up to maxSamples sample keys
// per entity type.
func NewParseReport(source string, maxSamples int) *ParseReport {
	return &ParseReport{Source: source, maxSamples: maxSamples, Entities: map[EntityType]*EntityReport{
		Performer: {Present: map[string]int{}}, Release: {Present: map[string]int{}},
	}}
}

// Add records one successfully parsed record.
func (r *ParseReport) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.Entities[rec.EntityType]
	if e == nil {
		e = &EntityReport{Present: map[string]int{}}
		r.Entities[rec.EntityType] = e
	}
	e.Parsed++
	complete := true
	for _, f := range RequiredFields[rec.EntityType] {
		if present(rec.Fields[f]) {
			e.Present[f]++
		} else {
			complete = false
		}
	}
	if complete {
		e.Complete++
	}
	for f, ok := range normalizedChecks[rec.EntityType] {
		v, _ := rec.Fields[f].(string)
		if strings.TrimSpace(v) == "" {
			continue
		}
		e.Checked++
		if ok(v) {
			e.Normalized++
		}
	}
	if len(e.Samples) < r.maxSamples {
		e.Samples = append(e.Samples, rec.SourceKey)
	}
}

// Fail records a page that could not be parsed.
func (r *ParseReport) Fail(t EntityType, what string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e := r.Entities[t]; e != nil {
		e.Failed++
	}
	r.Failures = append(r.Failures, fmt.Sprintf("%s: %v", what, err))
}

// SaveCatalogRecord makes a ParseReport a dry-run RecordSink: verify jobs
// collect what they parsed without writing anything to the catalog.
func (r *ParseReport) SaveCatalogRecord(_ context.Context, _ int64, _ string, rec Record) (int64, error) {
	r.Add(rec)
	return 0, nil
}

// Summary is a JSON-friendly snapshot (for jobs.result_counts).
func (r *ParseReport) Summary() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{"source": r.Source, "dry_run": true}
	keys := make([]string, 0, len(r.Entities))
	for t := range r.Entities {
		keys = append(keys, string(t))
	}
	sort.Strings(keys)
	for _, k := range keys {
		t := EntityType(k)
		e := r.Entities[t]
		out[k] = map[string]any{"parsed": e.Parsed, "failed": e.Failed, "complete": e.Complete,
			"required_pct": round1(e.RequiredPct(t)), "normalized_pct": round1(e.NormalizedPct()), "present": e.Present, "samples": e.Samples}
	}
	if len(r.Failures) > 0 {
		out["failures"] = r.Failures
	}
	return out
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
