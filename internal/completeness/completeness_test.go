package completeness

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"
)

// have builds a Have slice for entity with the named fields present.
func have(entity string, names ...string) []bool {
	fs := Fields(entity)
	out := make([]bool, len(fs))
	for i, f := range fs {
		for _, n := range names {
			if f.Name == n {
				out[i] = true
			}
		}
	}
	return out
}

var allRequired = []string{"canonical_title", "product_code", "release_date", "maker_name", "performers", "cover"}

func stat(fs []FieldStat, name string) FieldStat {
	for _, f := range fs {
		if f.Field == name {
			return f
		}
	}
	return FieldStat{}
}

func sample() []Facts {
	return []Facts{
		{ID: 1, Slug: "a", Name: "A", Maker: "M1", Popularity: 10, Have: have(EntityRelease, append(allRequired, "tags")...), Sources: []int64{1, 2},
			Provided: map[int64][]string{1: {"canonical_title", "product_code"}, 2: {"canonical_title", "canonical_title"}}},
		{ID: 2, Slug: "b", Name: "B", Maker: "M1", Popularity: 50, Have: have(EntityRelease, "canonical_title", "product_code"), Sources: []int64{1}},
		{ID: 3, Slug: "c", Name: "C", Maker: "", Popularity: 0, Have: have(EntityRelease, "canonical_title", "release_date"), Sources: []int64{2, 2}},
		{ID: 4, Slug: "d", Name: "D", Maker: "M2", Popularity: 50, Have: have(EntityRelease, "canonical_title")},
	}
}

func build(t *testing.T, opts Options, facts []Facts) Report {
	t.Helper()
	if err := opts.Validate(); err != nil {
		t.Fatal(err)
	}
	b := NewBuilder(opts, []SourceInfo{{ID: 1, Name: "official", Kind: "official_catalog"}, {ID: 2, Name: "agg", Kind: "aggregator"}})
	for _, f := range facts {
		b.Add(f)
	}
	return b.Report(time.Unix(0, 0))
}

func TestReportMath(t *testing.T) {
	r := build(t, Options{Entity: EntityRelease}, sample())
	if r.Total != 4 || r.RequiredComplete != 1 || r.RequiredCompletePercent != 25 {
		t.Fatalf("totals: %+v", r)
	}
	code := stat(r.Fields, "product_code")
	if code.Present != 2 || code.Missing != 2 || code.Percent != 50 || code.MissingPopularity != 50 {
		t.Errorf("product_code: %+v", code)
	}
	date := stat(r.Fields, "release_date")
	if date.Percent != 50 || date.MissingPopularity != 100 {
		t.Errorf("release_date: %+v", date)
	}
	// Top missing: most popular first, ties by id.
	if len(date.TopMissing) != 2 || date.TopMissing[0].ID != 2 || date.TopMissing[1].ID != 4 {
		t.Errorf("top missing: %+v", date.TopMissing)
	}
	// Ranked by missing popularity: fields nobody has (popularity 110) first,
	// then the required ones three releases miss (100), ... title (0) last;
	// equal popularity: more missing first, required first, then name.
	if r.Fields[len(r.Fields)-1].Field != "canonical_title" || r.Fields[0].MissingPopularity != 110 || r.Fields[0].Field != "director_name" {
		t.Errorf("ranking: first %+v last %+v", r.Fields[0], r.Fields[len(r.Fields)-1])
	}
	if len(r.Sources) != 2 || r.Sources[0].Entities != 2 || r.Sources[0].Percent != 50 {
		t.Errorf("source coverage: %+v", r.Sources)
	}
	if r.Groups != nil {
		t.Errorf("group_by=field has groups: %+v", r.Groups)
	}
}

func TestReportGroupBySource(t *testing.T) {
	r := build(t, Options{Entity: EntityRelease, GroupBy: GroupSource}, sample())
	if len(r.Groups) != 2 {
		t.Fatalf("groups: %+v", r.Groups)
	}
	var official Group
	for _, g := range r.Groups {
		if g.Key == "official" {
			official = g
		}
	}
	if official.Total != 2 || official.SourceKind != "official_catalog" || official.SourceID != 1 {
		t.Fatalf("official: %+v", official)
	}
	title := stat(official.Fields, "canonical_title")
	if title.SourceProvided == nil || *title.SourceProvided != 1 {
		t.Errorf("source provided: %+v", title)
	}
	if photo := stat(build(t, Options{Entity: EntityPerformer, GroupBy: GroupSource}, []Facts{{ID: 1, Sources: []int64{1}}}).Groups[0].Fields, "photo"); photo.SourceProvided != nil {
		t.Errorf("derived field has source_provided: %+v", photo)
	}
	// Duplicate source ids and provided fields count once.
	for _, g := range r.Groups {
		if g.Key == "agg" && (g.Total != 2 || *stat(g.Fields, "canonical_title").SourceProvided != 1) {
			t.Errorf("agg: %+v", g)
		}
	}
}

func TestReportGroupByMaker(t *testing.T) {
	r := build(t, Options{Entity: EntityRelease, GroupBy: GroupMaker, MaxGroups: 2}, sample())
	if len(r.Groups) != 2 || r.Groups[0].Key != "M1" || r.Groups[0].Total != 2 {
		t.Fatalf("maker groups: %+v", r.Groups)
	}
	if r.Groups[1].Key != "(unknown)" && r.Groups[1].Key != "M2" {
		t.Errorf("second group: %+v", r.Groups[1])
	}
}

func TestOptionsValidate(t *testing.T) {
	bad := map[string]Options{
		"entity":          {Entity: "video"},
		"group":           {Entity: EntityRelease, GroupBy: "tag"},
		"maker performer": {Entity: EntityPerformer, GroupBy: GroupMaker},
	}
	for name, o := range bad {
		if err := o.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	o := Options{Entity: EntityPerformer, TrendRuns: 10000}
	if err := o.Validate(); err != nil || o.GroupBy != GroupField || o.TrendRuns != MaxTrendRuns || o.TopMissing != 5 {
		t.Errorf("defaults: %+v %v", o, err)
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		p, t int64
		want float64
	}{{0, 0, 0}, {1, 3, 33.3}, {2, 3, 66.7}, {3, 3, 100}, {1, 8, 12.5}}
	for _, c := range cases {
		if got := Percent(c.p, c.t); got != c.want {
			t.Errorf("Percent(%d,%d) = %v want %v", c.p, c.t, got, c.want)
		}
	}
}

func TestSnapshotAndTrend(t *testing.T) {
	r := build(t, Options{Entity: EntityRelease}, sample())
	d1 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	s1 := SnapshotOf(r, d1)
	if s1.Total != 4 || s1.Fields["product_code"] != 2 || s1.Sources["official"] != 2 {
		t.Fatalf("snapshot: %+v", s1)
	}
	s2 := s1
	s2.Date = d1.AddDate(0, 0, 1)
	s2.Fields = map[string]int64{"product_code": 4}
	s2.RequiredComplete = 2
	tr := Trend([]Snapshot{s2, s1})
	if len(tr) != 2 || tr[0].Date != "2026-10-01" || tr[1].Fields["product_code"] != 100 || tr[1].RequiredCompletePercent != 50 {
		t.Errorf("trend: %+v", tr)
	}
}

func TestWriteCSV(t *testing.T) {
	facts := sample()
	facts[0].Maker = "=HYPERLINK(\"x\")"
	for _, g := range []string{GroupField, GroupMaker} {
		r := build(t, Options{Entity: EntityRelease, GroupBy: g}, facts)
		var buf bytes.Buffer
		if err := WriteCSV(&buf, r); err != nil {
			t.Fatal(err)
		}
		rows, err := csv.NewReader(&buf).ReadAll()
		if err != nil {
			t.Fatal(err)
		}
		want := len(ReleaseFields) + 1
		if g == GroupMaker {
			want = len(ReleaseFields)*len(r.Groups) + 1
		}
		if len(rows) != want || strings.Join(rows[0], ",") != strings.Join(CSVHeader, ",") {
			t.Errorf("%s: %d rows, header %v", g, len(rows), rows[0])
		}
		for _, row := range rows[1:] {
			if strings.HasPrefix(row[0], "=") {
				t.Errorf("formula cell not escaped: %v", row)
			}
		}
	}
}

type fakeReader struct {
	facts []Facts
	snaps []Snapshot
	saved []Snapshot
	gotID int64
}

func (f *fakeReader) CompletenessSources(context.Context) ([]SourceInfo, error) {
	return []SourceInfo{{ID: 1, Name: "Official", Kind: "official_catalog"}}, nil
}
func (f *fakeReader) CompletenessFacts(_ context.Context, entity string, sourceID int64, fn func(Facts) error) error {
	f.gotID = sourceID
	for _, x := range f.facts {
		if entity == EntityPerformer {
			x.Have = have(EntityPerformer, "canonical_name")
		}
		if err := fn(x); err != nil {
			return err
		}
	}
	return nil
}
func (f *fakeReader) CompletenessSnapshots(context.Context, string, int) ([]Snapshot, error) {
	return f.snaps, nil
}
func (f *fakeReader) SaveCompletenessSnapshot(_ context.Context, s Snapshot) error {
	f.saved = append(f.saved, s)
	return nil
}

func TestBuildReportAndRunner(t *testing.T) {
	fr := &fakeReader{facts: sample(), snaps: []Snapshot{{Date: time.Now(), Total: 1, Fields: map[string]int64{}}}}
	r, err := BuildReport(context.Background(), fr, Options{Entity: EntityRelease, Source: "official"}, time.Now())
	if err != nil || fr.gotID != 1 || r.Source != "Official" || len(r.Trend) != 1 {
		t.Fatalf("report: %+v %v (source id %d)", r, err, fr.gotID)
	}
	if _, err = BuildReport(context.Background(), fr, Options{Entity: EntityRelease, Source: "nope"}, time.Now()); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}
	if _, err = BuildReport(context.Background(), fr, Options{Entity: "x"}, time.Now()); !errors.Is(err, ErrUnknownEntity) {
		t.Errorf("unknown entity: %v", err)
	}
	now := time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC)
	rep, err := (&Runner{Store: fr, JobID: 9, Now: func() time.Time { return now }}).Run(context.Background())
	if err != nil || len(fr.saved) != 2 || rep.Date != "2026-10-16" || rep.Entities[EntityRelease] != 4 || fr.saved[1].Entity != EntityPerformer || fr.saved[0].JobID != 9 {
		t.Errorf("runner: %+v %+v %v", rep, fr.saved, err)
	}
}
