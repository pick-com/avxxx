// Package evidence assembles the public, per-field source evidence of a
// release or performer (FINAL_DATA_DESIGN 9 "公开 API 返回本站合并实体，同时
// 提供来源证据"; GET /api/releases/{slug}/evidence and
// GET /api/performers/{slug}/evidence).
//
// For each canonical field it reports the site's value, which sources
// supplied a value (tier, name, value, when it was captured and when the
// record was last seen), which source the canonical value is attributed to,
// and whether a manual override is in effect. It never carries raw_data,
// whole source payloads, override reasons or actors: inputs are limited to
// scalar current field values, and Build drops anything else.
package evidence

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Field maps one canonical field to the parser field names (as written to
// source_field_values.field_name) that carry it.
type Field struct {
	Name    string
	Parsers []string
}

// ReleaseFields are the release fields evidence is reported for, in order.
// dvd_code / content_id come from release_identifiers.
var ReleaseFields = []Field{
	{"canonical_title", []string{"title", "canonical_title"}},
	{"title_ja", []string{"title_ja"}},
	{"dvd_code", []string{"dvd_code"}},
	{"content_id", []string{"content_id"}},
	{"release_date", []string{"release_date"}},
	{"runtime_minutes", []string{"runtime", "runtime_minutes"}},
	{"maker_name", []string{"maker", "maker_name"}},
	{"label_name", []string{"label", "label_name"}},
	{"series_name", []string{"series", "series_name"}},
	{"director_name", []string{"director", "director_name"}},
	{"synopsis", []string{"synopsis"}},
}

// PerformerFields are the performer fields evidence is reported for.
var PerformerFields = []Field{
	{"canonical_name", []string{"name", "canonical_name"}},
	{"canonical_name_ja", []string{"name_ja", "canonical_name_ja"}},
	{"birth_date", []string{"birth_date"}},
	{"birth_place", []string{"birth_place"}},
	{"debut_date", []string{"debut_date"}},
	{"blood_type", []string{"blood_type"}},
	{"height_cm", []string{"height_cm", "height"}},
	{"bust_cm", []string{"bust_cm", "bust"}},
	{"waist_cm", []string{"waist_cm", "waist"}},
	{"hip_cm", []string{"hip_cm", "hip"}},
	{"cup_size", []string{"cup_size", "cup"}},
	{"shoe_size", []string{"shoe_size"}},
	{"biography", []string{"biography"}},
}

// Fields returns the field list of an entity type ("release" / "performer").
func Fields(entityType string) []Field {
	if entityType == "performer" {
		return PerformerFields
	}
	return ReleaseFields
}

// ParserNames returns every parser field name of fields (for SQL filters).
func ParserNames(fields []Field) []string {
	var out []string
	for _, f := range fields {
		out = append(out, f.Parsers...)
	}
	return out
}

// Tier ranks a source_kind (FINAL_DATA_DESIGN 2.4): lower is more trusted.
func Tier(kind string) int {
	switch kind {
	case "manual_review":
		return 0
	case "official_catalog":
		return 1
	case "official_maker":
		return 2
	case "structured_database":
		return 3
	case "aggregator":
		return 4
	case "third_party_api":
		return 5
	}
	return 9
}

// Record is one source record linked to the entity (no raw_data).
type Record struct {
	ID          int64
	SourceID    int64
	SourceName  string
	SourceKind  string
	Priority    int // sources.priority, lower first
	URL         string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
}

// Value is one current source_field_values row: the parser field and its
// normalized value as JSON.
type Value struct {
	RecordID   int64
	Field      string // parser field name
	JSON       json.RawMessage
	CapturedAt time.Time
}

// Input is everything Build needs. Canonical maps a canonical field to its
// site value as JSON (absent or null when empty). Hidden fields are left out
// of the result entirely, canonical and source values alike (a hidden
// birthday, PRD 15). Policies maps "sourceID/field" to a
// source_field_policies priority (lower first).
type Input struct {
	EntityType string
	Slug       string
	UpdatedAt  time.Time
	Canonical  map[string]json.RawMessage
	Overridden map[string]bool
	Hidden     map[string]bool
	Records    []Record
	Values     []Value
	Policies   map[string]int
}

// Source is a source record as shown publicly.
type Source struct {
	Name        string    `json:"name"`
	SourceKind  string    `json:"source_kind"`
	Tier        int       `json:"tier"`
	URL         string    `json:"url,omitempty"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// SourceValue is one source's current value of a field.
type SourceValue struct {
	Source     string    `json:"source"`
	SourceKind string    `json:"source_kind"`
	Tier       int       `json:"tier"`
	Value      any       `json:"value"`
	UpdatedAt  time.Time `json:"updated_at"`   // when this value was captured
	LastSeenAt time.Time `json:"last_seen_at"` // when the record was last crawled
	Matches    bool      `json:"matches"`      // equals the site value
}

// Provider says where the site value of a field came from.
type Provider struct {
	Source     string `json:"source"`
	SourceKind string `json:"source_kind"`
	Tier       int    `json:"tier"`
}

// ManualProvider is the provider of an overridden field. Who set it and why
// stay admin-only.
var ManualProvider = Provider{Source: "manual_override", SourceKind: "manual_review", Tier: 0}

// FieldEvidence is the evidence of one canonical field.
type FieldEvidence struct {
	Field      string        `json:"field"`
	Value      any           `json:"value"`
	Overridden bool          `json:"overridden"`
	ProvidedBy *Provider     `json:"provided_by"` // nil when no source matches the site value
	UpdatedAt  *time.Time    `json:"updated_at"`  // latest capture among sources
	Sources    []SourceValue `json:"sources"`
}

// Evidence is the response of the evidence endpoints.
type Evidence struct {
	EntityType string          `json:"entity_type"`
	Slug       string          `json:"slug"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Sources    []Source        `json:"sources"`
	Fields     []FieldEvidence `json:"fields"`
}

// Build assembles the evidence. Source values that are not JSON scalars
// (objects, arrays) are dropped, so no source payload can pass through.
func Build(in Input) Evidence {
	ev := Evidence{EntityType: in.EntityType, Slug: in.Slug, UpdatedAt: in.UpdatedAt, Sources: []Source{}, Fields: []FieldEvidence{}}
	records := map[int64]Record{}
	recs := append([]Record(nil), in.Records...)
	sort.SliceStable(recs, func(a, b int) bool {
		ta, tb := Tier(recs[a].SourceKind), Tier(recs[b].SourceKind)
		if ta != tb {
			return ta < tb
		}
		if recs[a].Priority != recs[b].Priority {
			return recs[a].Priority < recs[b].Priority
		}
		return recs[a].ID < recs[b].ID
	})
	for _, r := range recs {
		records[r.ID] = r
		ev.Sources = append(ev.Sources, Source{Name: r.SourceName, SourceKind: r.SourceKind, Tier: Tier(r.SourceKind), URL: r.URL, FirstSeenAt: r.FirstSeenAt, LastSeenAt: r.LastSeenAt})
	}
	byParser := map[string][]Value{}
	for _, v := range in.Values {
		byParser[v.Field] = append(byParser[v.Field], v)
	}
	for _, f := range Fields(in.EntityType) {
		if in.Hidden[f.Name] {
			continue
		}
		fe := FieldEvidence{Field: f.Name, Value: scalar(in.Canonical[f.Name]), Overridden: in.Overridden[f.Name], Sources: []SourceValue{}}
		type cand struct {
			sv     SourceValue
			policy int
			prio   int
		}
		var cands []cand
		for _, p := range f.Parsers {
			for _, v := range byParser[p] {
				r, ok := records[v.RecordID]
				val := scalar(v.JSON)
				if !ok || val == nil {
					continue
				}
				sv := SourceValue{Source: r.SourceName, SourceKind: r.SourceKind, Tier: Tier(r.SourceKind), Value: val,
					UpdatedAt: v.CapturedAt, LastSeenAt: r.LastSeenAt, Matches: Same(fe.Value, val)}
				policy, has := in.Policies[strconv.FormatInt(r.SourceID, 10)+"/"+f.Name]
				if !has {
					policy, has = in.Policies[strconv.FormatInt(r.SourceID, 10)+"/"+p]
				}
				if !has {
					policy = 1 << 30
				}
				cands = append(cands, cand{sv, policy, r.Priority})
				if fe.UpdatedAt == nil || v.CapturedAt.After(*fe.UpdatedAt) {
					t := v.CapturedAt
					fe.UpdatedAt = &t
				}
			}
		}
		sort.SliceStable(cands, func(a, b int) bool {
			x, y := cands[a], cands[b]
			if x.policy != y.policy {
				return x.policy < y.policy
			}
			if x.sv.Tier != y.sv.Tier {
				return x.sv.Tier < y.sv.Tier
			}
			if x.prio != y.prio {
				return x.prio < y.prio
			}
			return x.sv.UpdatedAt.After(y.sv.UpdatedAt)
		})
		for _, c := range cands {
			fe.Sources = append(fe.Sources, c.sv)
			if fe.ProvidedBy == nil && !fe.Overridden && c.sv.Matches {
				fe.ProvidedBy = &Provider{Source: c.sv.Source, SourceKind: c.sv.SourceKind, Tier: c.sv.Tier}
			}
		}
		if fe.Overridden {
			p := ManualProvider
			fe.ProvidedBy = &p
		}
		ev.Fields = append(ev.Fields, fe)
	}
	return ev
}

// scalar decodes a JSON scalar (string, number, bool); null, empty strings,
// objects and arrays yield nil.
func scalar(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil
	}
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return x
	case json.Number, bool:
		return x
	}
	return nil
}

// Same reports whether a source value equals the site value, ignoring case,
// surrounding space, numeric formatting (160 = "160.00") and a time part
// on dates ("2001-02-03" = "2001-02-03T00:00:00Z").
func Same(site, source any) bool {
	if site == nil || source == nil {
		return false
	}
	a, b := text(site), text(source)
	if a == b {
		return true
	}
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	if ea == nil && eb == nil {
		return fa == fb
	}
	if len(a) >= 10 && len(b) >= 10 && isDate(a[:10]) && a[:10] == b[:10] {
		return true
	}
	return false
}

func text(v any) string {
	switch x := v.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(x))
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	return ""
}

func isDate(s string) bool {
	_, err := time.Parse(time.DateOnly, s)
	return err == nil
}
