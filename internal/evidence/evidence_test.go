package evidence

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func field(ev Evidence, name string) *FieldEvidence {
	for i := range ev.Fields {
		if ev.Fields[i].Field == name {
			return &ev.Fields[i]
		}
	}
	return nil
}

func TestBuildAttributesBestMatchingSource(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	in := Input{
		EntityType: "release", Slug: "abc-001", UpdatedAt: t0,
		Canonical: map[string]json.RawMessage{"canonical_title": raw(`"Alpha"`), "maker_name": raw(`"Maker A"`), "runtime_minutes": raw(`120`), "release_date": raw(`"2024-02-03"`)},
		Records: []Record{
			{ID: 10, SourceID: 1, SourceName: "agg", SourceKind: "aggregator", Priority: 100, LastSeenAt: t0.Add(time.Hour)},
			{ID: 11, SourceID: 2, SourceName: "official", SourceKind: "official_catalog", Priority: 100, LastSeenAt: t0},
		},
		Values: []Value{
			{RecordID: 10, Field: "title", JSON: raw(`"alpha "`), CapturedAt: t0.Add(2 * time.Hour)},
			{RecordID: 11, Field: "title", JSON: raw(`"Alpha"`), CapturedAt: t0},
			{RecordID: 10, Field: "maker", JSON: raw(`"Maker B"`), CapturedAt: t0},
			{RecordID: 10, Field: "runtime", JSON: raw(`"120.0"`), CapturedAt: t0},
			{RecordID: 11, Field: "release_date", JSON: raw(`"2024-02-03T00:00:00Z"`), CapturedAt: t0},
			{RecordID: 10, Field: "tags", JSON: raw(`["x"]`), CapturedAt: t0},                 // not a reported field
			{RecordID: 10, Field: "synopsis", JSON: raw(`{"raw":"payload"}`), CapturedAt: t0}, // non-scalar: dropped
			{RecordID: 99, Field: "title_ja", JSON: raw(`"orphan"`), CapturedAt: t0},          // unknown record: dropped
		},
	}
	ev := Build(in)
	if len(ev.Sources) != 2 || ev.Sources[0].Name != "official" || ev.Sources[0].Tier != 1 {
		t.Fatalf("sources: %+v", ev.Sources)
	}
	title := field(ev, "canonical_title")
	if title.ProvidedBy == nil || title.ProvidedBy.Source != "official" || len(title.Sources) != 2 || !title.Sources[1].Matches {
		t.Errorf("title: %+v", title)
	}
	if title.UpdatedAt == nil || !title.UpdatedAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("title updated_at: %v", title.UpdatedAt)
	}
	if maker := field(ev, "maker_name"); maker.ProvidedBy != nil || maker.Sources[0].Matches {
		t.Errorf("maker should be unattributed: %+v", maker)
	}
	if rt := field(ev, "runtime_minutes"); rt.ProvidedBy == nil || rt.ProvidedBy.Source != "agg" {
		t.Errorf("runtime numeric match: %+v", rt)
	}
	if d := field(ev, "release_date"); d.ProvidedBy == nil {
		t.Errorf("date match: %+v", d)
	}
	if syn := field(ev, "synopsis"); len(syn.Sources) != 0 || syn.Value != nil {
		t.Errorf("synopsis: %+v", syn)
	}
	if tj := field(ev, "title_ja"); len(tj.Sources) != 0 {
		t.Errorf("orphan value kept: %+v", tj)
	}
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), "payload") || strings.Contains(string(b), "raw") {
		t.Errorf("payload leaked: %s", b)
	}
}

func TestBuildOverrideAndPolicy(t *testing.T) {
	t0 := time.Now()
	in := Input{
		EntityType: "release",
		Canonical:  map[string]json.RawMessage{"canonical_title": raw(`"Fixed"`), "maker_name": raw(`"M"`)},
		Overridden: map[string]bool{"canonical_title": true},
		Records: []Record{
			{ID: 1, SourceID: 1, SourceName: "a", SourceKind: "official_catalog"},
			{ID: 2, SourceID: 2, SourceName: "b", SourceKind: "aggregator"},
		},
		Values: []Value{
			{RecordID: 1, Field: "title", JSON: raw(`"Fixed"`), CapturedAt: t0},
			{RecordID: 1, Field: "maker", JSON: raw(`"M"`), CapturedAt: t0},
			{RecordID: 2, Field: "maker", JSON: raw(`"m"`), CapturedAt: t0},
		},
		// Field policy prefers the aggregator for maker_name.
		Policies: map[string]int{"2/maker_name": 1, "1/maker_name": 5},
	}
	ev := Build(in)
	title := field(ev, "canonical_title")
	if !title.Overridden || title.ProvidedBy == nil || *title.ProvidedBy != ManualProvider {
		t.Errorf("override: %+v", title)
	}
	if m := field(ev, "maker_name"); m.ProvidedBy == nil || m.ProvidedBy.Source != "b" {
		t.Errorf("policy: %+v", m)
	}
}

func TestBuildHidesHiddenFields(t *testing.T) {
	in := Input{
		EntityType: "performer",
		Canonical:  map[string]json.RawMessage{"canonical_name": raw(`"A"`), "birth_date": raw(`"2000-01-02"`)},
		Hidden:     map[string]bool{"birth_date": true},
		Records:    []Record{{ID: 1, SourceName: "s", SourceKind: "aggregator"}},
		Values:     []Value{{RecordID: 1, Field: "birth_date", JSON: raw(`"2000-01-02"`)}},
	}
	ev := Build(in)
	if field(ev, "birth_date") != nil {
		t.Fatal("hidden birth_date present")
	}
	b, _ := json.Marshal(ev)
	if strings.Contains(string(b), "2000-01-02") {
		t.Errorf("hidden birthday leaked: %s", b)
	}
}

func TestSame(t *testing.T) {
	yes := [][2]any{{"Abc", " abc "}, {json.Number("160"), "160.00"}, {"2001-02-03", "2001-02-03T00:00:00Z"}}
	for _, c := range yes {
		if !Same(c[0], c[1]) {
			t.Errorf("Same(%v,%v) = false", c[0], c[1])
		}
	}
	no := [][2]any{{nil, "a"}, {"a", nil}, {"a", "b"}, {"2001-02-03", "2001-02-04"}}
	for _, c := range no {
		if Same(c[0], c[1]) {
			t.Errorf("Same(%v,%v) = true", c[0], c[1])
		}
	}
	if Tier("official_catalog") >= Tier("aggregator") || Tier("unknown") != 9 {
		t.Error("tier order")
	}
	if len(ParserNames(ReleaseFields)) < len(ReleaseFields) {
		t.Error("parser names")
	}
}
