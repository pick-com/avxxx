package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// TableSpec describes one table whose rows point at a merged entity through
// FK and therefore move from the source entity to the target.
type TableSpec struct {
	Table string
	FK    string
	// Key identifies a row among the rows sharing one FK value. When Dedupe is
	// set, a source row whose Key already exists on the target is dropped
	// (deleted, kept in the snapshot for undo) instead of moved.
	Key    []string
	Dedupe bool
	// SkipTarget leaves the target entity's own row alone; used when the
	// table is the entity table itself (releases.edition_of_release_id).
	SkipTarget bool
}

// PerformerTables lists every relation moved when merging performers.
// media_assets stay on the merged entity (it is hidden, and moving covers
// would change the target's artwork); source records are relinked, never
// deleted.
var PerformerTables = []TableSpec{
	{Table: "performer_aliases", FK: "performer_id", Key: []string{"normalized_name"}, Dedupe: true},
	{Table: "release_performers", FK: "performer_id", Key: []string{"release_id"}, Dedupe: true},
	{Table: "release_performer_sources", FK: "performer_id", Key: []string{"release_id", "source_record_id"}, Dedupe: true},
	{Table: "performer_source_metrics", FK: "performer_id", Key: []string{"source_id", "captured_at"}, Dedupe: true},
	{Table: "source_records", FK: "performer_id", Key: []string{"id"}},
}

// ReleaseTables lists every relation moved when merging releases.
var ReleaseTables = []TableSpec{
	{Table: "release_identifiers", FK: "release_id", Key: []string{"id"}},
	{Table: "release_performers", FK: "release_id", Key: []string{"performer_id"}, Dedupe: true},
	{Table: "release_performer_sources", FK: "release_id", Key: []string{"performer_id", "source_record_id"}, Dedupe: true},
	{Table: "release_tags", FK: "release_id", Key: []string{"normalized_tag"}, Dedupe: true},
	{Table: "release_tag_sources", FK: "release_id", Key: []string{"normalized_tag", "source_record_id"}, Dedupe: true},
	{Table: "release_media_sources", FK: "release_id", Key: []string{"source"}, Dedupe: true},
	{Table: "release_source_metrics", FK: "release_id", Key: []string{"source_id", "captured_at"}, Dedupe: true},
	{Table: "source_records", FK: "release_id", Key: []string{"id"}},
	{Table: "watch_history", FK: "release_id", Key: []string{"user_id"}, Dedupe: true},
	{Table: "favorites", FK: "release_id", Key: []string{"user_id"}, Dedupe: true},
	{Table: "releases", FK: "edition_of_release_id", Key: []string{"id"}, SkipTarget: true},
}

// Tables returns the relation specs and entity table for an entity type.
func Tables(entityType string) (entityTable string, specs []TableSpec, ok bool) {
	switch entityType {
	case PerformerType:
		return "performers", PerformerTables, true
	case ReleaseType:
		return "releases", ReleaseTables, true
	}
	return "", nil, false
}

// TablePlan is what a merge does to one table. Move holds the Key values
// (JSON arrays) of source rows re-pointed at the target; Drop holds whole
// source rows deleted as duplicates of a target row.
type TablePlan struct {
	Table string            `json:"table"`
	FK    string            `json:"fk"`
	Key   []string          `json:"key"`
	Move  []json.RawMessage `json:"move"`
	Drop  []json.RawMessage `json:"drop"`
}

// Alias is a name added to the target performer as alias_type 'merged'.
type Alias struct {
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
}

// MergePlan is the full, replayable description of a merge. It is stored in
// review_events.after_value and is what undo reverses.
type MergePlan struct {
	EntityType string      `json:"entity_type"`
	TargetID   int64       `json:"target_id"`
	SourceID   int64       `json:"source_id"`
	Tables     []TablePlan `json:"tables"`
	AddAliases []Alias     `json:"add_aliases"`
}

// MergeInput is everything a plan is built from: the two entity rows and,
// per table, the rows currently pointing at each of them (to_jsonb(row)).
type MergeInput struct {
	EntityType     string
	TargetID       int64
	SourceID       int64
	Target, Source json.RawMessage
	TargetRows     map[string][]json.RawMessage
	SourceRows     map[string][]json.RawMessage
}

// KeyOf extracts cols from a JSON row as a compact JSON array, the same shape
// SQL produces with jsonb_build_array(cols...).
func KeyOf(row json.RawMessage, cols []string) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(row))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	for i, c := range cols {
		v, ok := m[c]
		if !ok {
			return nil, fmt.Errorf("identity: row has no column %q", c)
		}
		vals[i] = v
	}
	return json.Marshal(vals)
}

// BuildMergePlan decides, per table, which source rows move to the target and
// which are dropped because the target already has an equivalent row, and
// which of the source performer's names become aliases of the target.
func BuildMergePlan(in MergeInput) (MergePlan, error) {
	if in.TargetID == in.SourceID || in.TargetID <= 0 || in.SourceID <= 0 {
		return MergePlan{}, fmt.Errorf("identity: invalid merge %d <- %d", in.TargetID, in.SourceID)
	}
	_, specs, ok := Tables(in.EntityType)
	if !ok {
		return MergePlan{}, fmt.Errorf("identity: unknown entity type %q", in.EntityType)
	}
	plan := MergePlan{EntityType: in.EntityType, TargetID: in.TargetID, SourceID: in.SourceID, Tables: []TablePlan{}, AddAliases: []Alias{}}
	for _, spec := range specs {
		tp := TablePlan{Table: spec.Table, FK: spec.FK, Key: spec.Key, Move: []json.RawMessage{}, Drop: []json.RawMessage{}}
		have := map[string]bool{}
		if spec.Dedupe {
			for _, row := range in.TargetRows[spec.Table] {
				k, err := KeyOf(row, spec.Key)
				if err != nil {
					return MergePlan{}, fmt.Errorf("%s: %w", spec.Table, err)
				}
				have[string(k)] = true
			}
		}
		targetKey, _ := json.Marshal([]int64{in.TargetID})
		for _, row := range in.SourceRows[spec.Table] {
			k, err := KeyOf(row, spec.Key)
			if err != nil {
				return MergePlan{}, fmt.Errorf("%s: %w", spec.Table, err)
			}
			if spec.SkipTarget && string(k) == string(targetKey) {
				continue
			}
			if spec.Dedupe && have[string(k)] {
				tp.Drop = append(tp.Drop, row)
				continue
			}
			have[string(k)] = true
			tp.Move = append(tp.Move, k)
		}
		plan.Tables = append(plan.Tables, tp)
	}
	if in.EntityType == PerformerType {
		aliases, err := aliasesToAdd(in)
		if err != nil {
			return MergePlan{}, err
		}
		plan.AddAliases = aliases
	}
	return plan, nil
}

// aliasesToAdd returns the source performer's canonical names that the target
// does not already carry, either as its own name or as an alias (including
// the aliases moving over from the source).
func aliasesToAdd(in MergeInput) ([]Alias, error) {
	var target, source struct {
		CanonicalName   string  `json:"canonical_name"`
		CanonicalNameJA *string `json:"canonical_name_ja"`
	}
	if err := json.Unmarshal(in.Target, &target); err != nil {
		return nil, fmt.Errorf("identity: target row: %w", err)
	}
	if err := json.Unmarshal(in.Source, &source); err != nil {
		return nil, fmt.Errorf("identity: source row: %w", err)
	}
	have := map[string]bool{AliasForm(target.CanonicalName): true}
	if target.CanonicalNameJA != nil {
		have[AliasForm(*target.CanonicalNameJA)] = true
	}
	for _, rows := range [][]json.RawMessage{in.TargetRows["performer_aliases"], in.SourceRows["performer_aliases"]} {
		for _, row := range rows {
			var a struct {
				NormalizedName string `json:"normalized_name"`
			}
			if err := json.Unmarshal(row, &a); err != nil {
				return nil, err
			}
			have[a.NormalizedName] = true
		}
	}
	names := []string{source.CanonicalName}
	if source.CanonicalNameJA != nil {
		names = append(names, *source.CanonicalNameJA)
	}
	out := []Alias{}
	for _, n := range names {
		f := AliasForm(n)
		if f == "" || have[f] {
			continue
		}
		have[f] = true
		out = append(out, Alias{Name: n, NormalizedName: f})
	}
	return out, nil
}
