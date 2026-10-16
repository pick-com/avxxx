// Package completeness computes the release / performer field completeness
// report (ROADMAP "作品、演员和字段完整性报告", AdminDashboard / AdminAudit):
// the share of entities with each field, coverage per source, missing-field
// counts ranked by popularity, and a trend over the nightly snapshots
// (job kind JobKind: cmd/scheduler enqueues it nightly, cmd/audit runs it).
//
// The math is pure: the store streams one Facts row per entity and Builder
// aggregates them.
package completeness

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// JobKind is jobs.kind of the nightly snapshot job.
const JobKind = "completeness_snapshot"

// Entity types.
const (
	EntityRelease   = "release"
	EntityPerformer = "performer"
)

// Group-by values.
const (
	GroupField  = "field"
	GroupSource = "source"
	GroupMaker  = "maker"
)

// FieldDef is one reported field. Required fields make up "complete";
// Parsers are the source_field_values.field_name values that supply it
// (empty for derived fields such as photos), used for per-source coverage.
type FieldDef struct {
	Name     string
	Required bool
	Parsers  []string
}

// ReleaseFields: required = what FINAL_DATA_DESIGN 9.1 / 3.2 needs to
// identify and list a release (title, product code, release date, maker,
// performers, cover); the rest are tracked.
var ReleaseFields = []FieldDef{
	{"canonical_title", true, []string{"title", "canonical_title"}},
	{"product_code", true, []string{"dvd_code", "content_id"}},
	{"release_date", true, []string{"release_date"}},
	{"maker_name", true, []string{"maker", "maker_name"}},
	{"performers", true, []string{"performers"}},
	{"cover", true, []string{"cover", "cover_url"}},
	{"title_ja", false, []string{"title_ja"}},
	{"runtime_minutes", false, []string{"runtime", "runtime_minutes"}},
	{"label_name", false, []string{"label", "label_name"}},
	{"series", false, []string{"series", "series_name"}},
	{"director_name", false, []string{"director", "director_name"}},
	{"tags", false, []string{"tags", "genres"}},
	{"synopsis", false, []string{"synopsis"}},
}

// PerformerFields: required = canonical_name plus the AdminActors 信息完整度
// fields (store.CompletenessFields); the rest are tracked.
var PerformerFields = []FieldDef{
	{"canonical_name", true, []string{"name", "canonical_name"}},
	{"canonical_name_ja", true, []string{"name_ja", "canonical_name_ja"}},
	{"birth_date", true, []string{"birth_date"}},
	{"birth_place", true, []string{"birth_place"}},
	{"debut_date", true, []string{"debut_date"}},
	{"height_cm", true, []string{"height_cm", "height"}},
	{"photo", true, nil},
	{"measurements", false, []string{"bust_cm", "waist_cm", "hip_cm", "bust", "waist", "hip"}},
	{"cup_size", false, []string{"cup_size", "cup"}},
	{"biography", false, []string{"biography"}},
	{"aliases", false, nil},
	{"works", false, nil},
}

// Fields returns the field list of entity, or nil when unknown.
func Fields(entity string) []FieldDef {
	switch entity {
	case EntityRelease:
		return ReleaseFields
	case EntityPerformer:
		return PerformerFields
	}
	return nil
}

// ParserMap maps every parser field name of entity to its report field.
func ParserMap(entity string) map[string]string {
	out := map[string]string{}
	for _, f := range Fields(entity) {
		for _, p := range f.Parsers {
			out[p] = f.Name
		}
	}
	return out
}

// Facts is one entity as the report sees it. Have is indexed like
// Fields(entity). Provided maps a source id to the report fields that source
// currently supplies a value for.
type Facts struct {
	ID         int64
	Slug       string
	Name       string
	Maker      string
	Popularity int64
	Have       []bool
	Sources    []int64
	Provided   map[int64][]string
}

// SourceInfo names a source.
type SourceInfo struct {
	ID   int64
	Name string
	Kind string
}

// EntityRef is a popular entity missing a field.
type EntityRef struct {
	ID         int64  `json:"id"`
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	Popularity int64  `json:"popularity"`
}

// FieldStat is the completeness of one field within a group.
type FieldStat struct {
	Field             string      `json:"field"`
	Required          bool        `json:"required"`
	Total             int64       `json:"total"`
	Present           int64       `json:"present"`
	Missing           int64       `json:"missing"`
	Percent           float64     `json:"percent"`
	MissingPopularity int64       `json:"missing_popularity"` // sum of popularity of entities missing it
	SourceProvided    *int64      `json:"source_provided,omitempty"`
	TopMissing        []EntityRef `json:"top_missing,omitempty"`
}

// Group is one row group of group_by=source|maker.
type Group struct {
	Key                     string      `json:"key"`
	SourceID                int64       `json:"source_id,omitempty"`
	SourceKind              string      `json:"source_kind,omitempty"`
	Total                   int64       `json:"total"`
	RequiredComplete        int64       `json:"required_complete"`
	RequiredCompletePercent float64     `json:"required_complete_percent"`
	Fields                  []FieldStat `json:"fields"`
}

// SourceCoverage is the share of entities with a record from one source.
type SourceCoverage struct {
	SourceID   int64   `json:"source_id"`
	Name       string  `json:"name"`
	SourceKind string  `json:"source_kind"`
	Entities   int64   `json:"entities"`
	Percent    float64 `json:"percent"`
}

// TrendPoint is one nightly snapshot as percentages.
type TrendPoint struct {
	Date                    string             `json:"date"`
	Total                   int64              `json:"total"`
	RequiredCompletePercent float64            `json:"required_complete_percent"`
	Fields                  map[string]float64 `json:"fields"`
}

// Report is the GET /api/admin/reports/completeness payload.
type Report struct {
	Entity                  string           `json:"entity"`
	GroupBy                 string           `json:"group_by"`
	Source                  string           `json:"source,omitempty"`
	GeneratedAt             time.Time        `json:"generated_at"`
	Total                   int64            `json:"total"`
	RequiredComplete        int64            `json:"required_complete"`
	RequiredCompletePercent float64          `json:"required_complete_percent"`
	Fields                  []FieldStat      `json:"fields"`
	Groups                  []Group          `json:"groups,omitempty"`
	Sources                 []SourceCoverage `json:"sources"`
	Trend                   []TrendPoint     `json:"trend"`
}

// Options select a report.
type Options struct {
	Entity     string
	GroupBy    string // field (default) | source | maker
	Source     string // source id or name; "" = every entity
	TrendRuns  int    // snapshots in the trend (default 14, max MaxTrendRuns)
	TopMissing int    // popular entities listed per missing field (default 5)
	MaxGroups  int    // groups kept for group_by=maker (default 100)
}

// MaxTrendRuns bounds Options.TrendRuns.
const MaxTrendRuns = 365

// Errors for invalid options; the API maps them to 400.
var (
	ErrUnknownEntity  = errors.New("entity must be release or performer")
	ErrUnknownGroupBy = errors.New("group_by must be field, source or maker")
	ErrMakerPerformer = errors.New("group_by=maker is only available for releases")
	ErrUnknownSource  = errors.New("unknown source")
)

// Validate normalizes defaults and checks o.
func (o *Options) Validate() error {
	if Fields(o.Entity) == nil {
		return ErrUnknownEntity
	}
	if o.GroupBy == "" {
		o.GroupBy = GroupField
	}
	switch o.GroupBy {
	case GroupField, GroupSource:
	case GroupMaker:
		if o.Entity != EntityRelease {
			return ErrMakerPerformer
		}
	default:
		return ErrUnknownGroupBy
	}
	if o.TrendRuns <= 0 {
		o.TrendRuns = 14
	}
	if o.TrendRuns > MaxTrendRuns {
		o.TrendRuns = MaxTrendRuns
	}
	if o.TopMissing <= 0 {
		o.TopMissing = 5
	}
	if o.MaxGroups <= 0 {
		o.MaxGroups = 100
	}
	return nil
}

// Percent is part/total in percent, rounded to one decimal (0 when total is 0).
func Percent(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return math.Round(float64(part)*1000/float64(total)) / 10
}

type acc struct {
	key         string
	srcID       int64
	srcKind     string
	total       int64
	reqComplete int64
	present     []int64
	missingPop  []int64
	provided    []int64
	top         [][]EntityRef
}

func newAcc(n int, key string) *acc {
	return &acc{key: key, present: make([]int64, n), missingPop: make([]int64, n), provided: make([]int64, n), top: make([][]EntityRef, n)}
}

// Builder aggregates Facts into a Report.
type Builder struct {
	opts      Options
	fields    []FieldDef
	index     map[string]int
	overall   *acc
	groups    map[string]*acc
	sources   map[int64]SourceInfo
	srcCounts map[int64]int64
}

// NewBuilder starts a report; opts must be validated.
func NewBuilder(opts Options, sources []SourceInfo) *Builder {
	fs := Fields(opts.Entity)
	b := &Builder{opts: opts, fields: fs, index: map[string]int{}, overall: newAcc(len(fs), "all"),
		groups: map[string]*acc{}, sources: map[int64]SourceInfo{}, srcCounts: map[int64]int64{}}
	for i, f := range fs {
		b.index[f.Name] = i
	}
	for _, s := range sources {
		b.sources[s.ID] = s
	}
	return b
}

func (b *Builder) complete(f Facts) bool {
	for i, d := range b.fields {
		if d.Required && (i >= len(f.Have) || !f.Have[i]) {
			return false
		}
	}
	return true
}

func (a *acc) add(b *Builder, f Facts, complete bool, provided []string, keepTop bool) {
	a.total++
	if complete {
		a.reqComplete++
	}
	for i := range b.fields {
		if i < len(f.Have) && f.Have[i] {
			a.present[i]++
			continue
		}
		a.missingPop[i] += f.Popularity
		if keepTop {
			a.top[i] = insertTop(a.top[i], EntityRef{ID: f.ID, Slug: f.Slug, Name: f.Name, Popularity: f.Popularity}, b.opts.TopMissing)
		}
	}
	for _, p := range provided {
		if i, ok := b.index[p]; ok {
			a.provided[i]++
		}
	}
}

// insertTop keeps the n most popular refs (ties: lower id first).
func insertTop(top []EntityRef, r EntityRef, n int) []EntityRef {
	i := sort.Search(len(top), func(i int) bool {
		return top[i].Popularity < r.Popularity || (top[i].Popularity == r.Popularity && top[i].ID > r.ID)
	})
	if i >= n {
		return top
	}
	top = append(top, EntityRef{})
	copy(top[i+1:], top[i:])
	top[i] = r
	if len(top) > n {
		top = top[:n]
	}
	return top
}

// Add folds one entity into the report.
func (b *Builder) Add(f Facts) {
	complete := b.complete(f)
	b.overall.add(b, f, complete, nil, true)
	seen := map[int64]bool{}
	for _, s := range f.Sources {
		if seen[s] {
			continue
		}
		seen[s] = true
		b.srcCounts[s]++
		if b.opts.GroupBy == GroupSource {
			key := strconv.FormatInt(s, 10)
			g := b.groups[key]
			if g == nil {
				g = newAcc(len(b.fields), key)
				g.srcID = s
				if info, ok := b.sources[s]; ok {
					g.key, g.srcKind = info.Name, info.Kind
				}
				b.groups[key] = g
			}
			g.add(b, f, complete, dedupe(f.Provided[s]), false)
		}
	}
	if b.opts.GroupBy == GroupMaker {
		key := f.Maker
		if key == "" {
			key = "(unknown)"
		}
		g := b.groups[key]
		if g == nil {
			g = newAcc(len(b.fields), key)
			b.groups[key] = g
		}
		g.add(b, f, complete, nil, false)
	}
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	out := xs[:0:0]
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func (b *Builder) stats(a *acc, withProvided, withTop bool) []FieldStat {
	out := make([]FieldStat, len(b.fields))
	for i, d := range b.fields {
		st := FieldStat{Field: d.Name, Required: d.Required, Total: a.total, Present: a.present[i], Missing: a.total - a.present[i],
			Percent: Percent(a.present[i], a.total), MissingPopularity: a.missingPop[i]}
		if withProvided && len(d.Parsers) > 0 {
			v := a.provided[i]
			st.SourceProvided = &v
		}
		if withTop && len(a.top[i]) > 0 {
			st.TopMissing = a.top[i]
		}
		out[i] = st
	}
	return out
}

// RankFields orders field stats by missing popularity, then missing count,
// required first, then name: the fields worth filling first come first.
func RankFields(fs []FieldStat) {
	sort.SliceStable(fs, func(a, b int) bool {
		x, y := fs[a], fs[b]
		if x.MissingPopularity != y.MissingPopularity {
			return x.MissingPopularity > y.MissingPopularity
		}
		if x.Missing != y.Missing {
			return x.Missing > y.Missing
		}
		if x.Required != y.Required {
			return x.Required
		}
		return x.Field < y.Field
	})
}

// Report finishes the aggregation (without trend).
func (b *Builder) Report(now time.Time) Report {
	o := b.overall
	r := Report{Entity: b.opts.Entity, GroupBy: b.opts.GroupBy, Source: b.opts.Source, GeneratedAt: now, Total: o.total,
		RequiredComplete: o.reqComplete, RequiredCompletePercent: Percent(o.reqComplete, o.total),
		Fields: b.stats(o, false, true), Sources: []SourceCoverage{}, Trend: []TrendPoint{}}
	RankFields(r.Fields)
	for id, n := range b.srcCounts {
		info := b.sources[id]
		name := info.Name
		if name == "" {
			name = strconv.FormatInt(id, 10)
		}
		r.Sources = append(r.Sources, SourceCoverage{SourceID: id, Name: name, SourceKind: info.Kind, Entities: n, Percent: Percent(n, o.total)})
	}
	sort.Slice(r.Sources, func(i, j int) bool {
		if r.Sources[i].Entities != r.Sources[j].Entities {
			return r.Sources[i].Entities > r.Sources[j].Entities
		}
		return r.Sources[i].SourceID < r.Sources[j].SourceID
	})
	if b.opts.GroupBy != GroupField {
		r.Groups = []Group{}
		for _, g := range b.groups {
			r.Groups = append(r.Groups, Group{Key: g.key, SourceID: g.srcID, SourceKind: g.srcKind, Total: g.total, RequiredComplete: g.reqComplete,
				RequiredCompletePercent: Percent(g.reqComplete, g.total), Fields: b.stats(g, b.opts.GroupBy == GroupSource, false)})
		}
		sort.Slice(r.Groups, func(i, j int) bool {
			if r.Groups[i].Total != r.Groups[j].Total {
				return r.Groups[i].Total > r.Groups[j].Total
			}
			return r.Groups[i].Key < r.Groups[j].Key
		})
		if b.opts.GroupBy == GroupMaker && len(r.Groups) > b.opts.MaxGroups {
			r.Groups = r.Groups[:b.opts.MaxGroups]
		}
	}
	return r
}

// Snapshot is one stored nightly snapshot (completeness_snapshots).
type Snapshot struct {
	Date             time.Time        `json:"date"`
	Entity           string           `json:"entity"`
	Total            int64            `json:"total"`
	RequiredComplete int64            `json:"required_complete"`
	Fields           map[string]int64 `json:"fields"`  // field -> present
	Sources          map[string]int64 `json:"sources"` // source name -> entities
	JobID            int64            `json:"job_id,omitempty"`
}

// SnapshotOf turns an all-entities report into a snapshot for date.
func SnapshotOf(r Report, date time.Time) Snapshot {
	s := Snapshot{Date: date, Entity: r.Entity, Total: r.Total, RequiredComplete: r.RequiredComplete, Fields: map[string]int64{}, Sources: map[string]int64{}}
	for _, f := range r.Fields {
		s.Fields[f.Field] = f.Present
	}
	for _, c := range r.Sources {
		s.Sources[c.Name] = c.Entities
	}
	return s
}

// Trend turns snapshots (any order) into points, oldest first.
func Trend(snaps []Snapshot) []TrendPoint {
	sorted := append([]Snapshot(nil), snaps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Date.Before(sorted[j].Date) })
	out := make([]TrendPoint, 0, len(sorted))
	for _, s := range sorted {
		p := TrendPoint{Date: s.Date.Format(time.DateOnly), Total: s.Total, RequiredCompletePercent: Percent(s.RequiredComplete, s.Total), Fields: map[string]float64{}}
		for f, n := range s.Fields {
			p.Fields[f] = Percent(n, s.Total)
		}
		out = append(out, p)
	}
	return out
}

// Reader is what a report needs from the database.
type Reader interface {
	// CompletenessSources lists every source.
	CompletenessSources(ctx context.Context) ([]SourceInfo, error)
	// CompletenessFacts streams one Facts per unmerged entity (active
	// performers), restricted to entities with a record from sourceID
	// when it is not 0.
	CompletenessFacts(ctx context.Context, entity string, sourceID int64, fn func(Facts) error) error
	// CompletenessSnapshots returns the latest n snapshots of entity.
	CompletenessSnapshots(ctx context.Context, entity string, n int) ([]Snapshot, error)
}

// ResolveSource finds a source by id or (case-insensitive) name.
func ResolveSource(sources []SourceInfo, q string) (SourceInfo, error) {
	q = strings.TrimSpace(q)
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		for _, s := range sources {
			if s.ID == id {
				return s, nil
			}
		}
	}
	for _, s := range sources {
		if strings.EqualFold(s.Name, q) {
			return s, nil
		}
	}
	return SourceInfo{}, ErrUnknownSource
}

// BuildReport computes a report and its trend. The trend always describes
// the whole catalog (snapshots are not per source).
func BuildReport(ctx context.Context, rd Reader, opts Options, now time.Time) (Report, error) {
	if err := opts.Validate(); err != nil {
		return Report{}, err
	}
	sources, err := rd.CompletenessSources(ctx)
	if err != nil {
		return Report{}, err
	}
	var sourceID int64
	if opts.Source != "" {
		s, err := ResolveSource(sources, opts.Source)
		if err != nil {
			return Report{}, err
		}
		sourceID, opts.Source = s.ID, s.Name
	}
	b := NewBuilder(opts, sources)
	if err := rd.CompletenessFacts(ctx, opts.Entity, sourceID, func(f Facts) error { b.Add(f); return nil }); err != nil {
		return Report{}, err
	}
	r := b.Report(now)
	snaps, err := rd.CompletenessSnapshots(ctx, opts.Entity, opts.TrendRuns)
	if err != nil {
		return Report{}, err
	}
	r.Trend = Trend(snaps)
	return r, nil
}

// CSVHeader is the column list of WriteCSV.
var CSVHeader = []string{"group", "source_kind", "field", "required", "total", "present", "missing", "percent", "missing_popularity", "source_provided"}

// WriteCSV writes the field rows of r: one row per field for group_by=field
// (group "all"), else one per group and field. Cells that a spreadsheet
// would read as a formula are prefixed with an apostrophe.
func WriteCSV(w io.Writer, r Report) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	row := func(group, kind string, f FieldStat) error {
		prov := ""
		if f.SourceProvided != nil {
			prov = strconv.FormatInt(*f.SourceProvided, 10)
		}
		return cw.Write([]string{safeCell(group), safeCell(kind), f.Field, strconv.FormatBool(f.Required), strconv.FormatInt(f.Total, 10),
			strconv.FormatInt(f.Present, 10), strconv.FormatInt(f.Missing, 10), fmt.Sprintf("%.1f", f.Percent),
			strconv.FormatInt(f.MissingPopularity, 10), prov})
	}
	if r.GroupBy == GroupField || len(r.Groups) == 0 {
		for _, f := range r.Fields {
			if err := row("all", "", f); err != nil {
				return err
			}
		}
	} else {
		for _, g := range r.Groups {
			for _, f := range g.Fields {
				if err := row(g.Key, g.SourceKind, f); err != nil {
					return err
				}
			}
		}
	}
	cw.Flush()
	return cw.Error()
}

func safeCell(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// Store is what the nightly snapshot job needs.
type Store interface {
	Reader
	SaveCompletenessSnapshot(ctx context.Context, s Snapshot) error
}

// Runner executes one snapshot run.
type Runner struct {
	Store Store
	JobID int64
	Now   func() time.Time
}

// RunReport summarizes one run.
type RunReport struct {
	Date     string           `json:"date"`
	Entities map[string]int64 `json:"entities"`          // entity -> total
	Complete map[string]int64 `json:"required_complete"` // entity -> required-complete
}

// Run computes and stores today's snapshot of every entity type. A rerun
// on the same day replaces that day's rows.
func (r *Runner) Run(ctx context.Context) (RunReport, error) {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	date := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rep := RunReport{Date: date.Format(time.DateOnly), Entities: map[string]int64{}, Complete: map[string]int64{}}
	sources, err := r.Store.CompletenessSources(ctx)
	if err != nil {
		return rep, err
	}
	for _, entity := range []string{EntityRelease, EntityPerformer} {
		opts := Options{Entity: entity}
		if err := opts.Validate(); err != nil {
			return rep, err
		}
		b := NewBuilder(opts, sources)
		if err := r.Store.CompletenessFacts(ctx, entity, 0, func(f Facts) error { b.Add(f); return nil }); err != nil {
			return rep, fmt.Errorf("%s facts: %w", entity, err)
		}
		snap := SnapshotOf(b.Report(now), date)
		snap.JobID = r.JobID
		if err := r.Store.SaveCompletenessSnapshot(ctx, snap); err != nil {
			return rep, fmt.Errorf("%s snapshot: %w", entity, err)
		}
		rep.Entities[entity], rep.Complete[entity] = snap.Total, snap.RequiredComplete
	}
	return rep, nil
}
