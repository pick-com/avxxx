package identity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// TableSpec describes one table whose rows point at a merged entity through
// FK and therefore move from the source entity to the target.
type TableSpec struct {
	Table string
	FK    string
	// Key identifies a row among the rows sharing one FK value. When Dedupe is
	// set, a source row whose Key (or DedupeOn) already exists on the target
	// is dropped (deleted, kept in the snapshot for undo) instead of moved.
	Key    []string
	Dedupe bool
	// SkipTarget leaves the target entity's own row alone; used when the
	// table is the entity table itself (releases.edition_of_release_id).
	SkipTarget bool
	// Where further restricts the rows that belong to an entity, for tables
	// whose FK column is shared by several entity types (audit_issues). It is
	// a constant SQL condition, never user input.
	Where string
	// DedupeOn names the columns that decide whether a source row duplicates
	// a target row; Key when empty. Key must still identify the row exactly.
	DedupeOn []string
	// Combine, on a Dedupe table, folds a dropped source row into the target
	// row it duplicates: it returns the target columns to change (nil for
	// none). The old values are kept in the plan so undo restores them.
	Combine func(target, source map[string]any) map[string]any
	// Close, on a Dedupe table, keeps duplicate source rows instead of
	// deleting them and updates them with the returned columns; undo
	// restores the old values.
	Close func(entityType string, targetID int64, now time.Time) map[string]any
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
	{Table: "follows", FK: "performer_id", Key: []string{"user_id"}, Dedupe: true, Combine: combineFollow},
	// Personal-information requests (migration 022) follow the person.
	{Table: "performer_requests", FK: "performer_id", Key: []string{"id"}},
	{Table: "audit_issues", FK: "entity_id", Key: []string{"id"}, DedupeOn: []string{"rule"}, Dedupe: true,
		Where: "entity_type='performer' AND status='open'", Close: closeAuditIssue},
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
	{Table: "reservations", FK: "release_id", Key: []string{"user_id"}, Dedupe: true},
	{Table: "watch_later", FK: "release_id", Key: []string{"user_id"}, Dedupe: true, Combine: combineWatchLater},
	{Table: "notification_receipts", FK: "release_id", Key: []string{"user_id"}, Dedupe: true},
	// Every playback source is a distinct way to play the release: keep all.
	{Table: "playback_sources", FK: "release_id", Key: []string{"id"}},
	{Table: "audit_issues", FK: "entity_id", Key: []string{"id"}, DedupeOn: []string{"rule"}, Dedupe: true,
		Where: "entity_type='release' AND status='open'", Close: closeAuditIssue},
	{Table: "releases", FK: "edition_of_release_id", Key: []string{"id"}, SkipTarget: true},
}

// notifyRank orders follows.notify_mode from least to most permissive.
var notifyRank = map[string]int{"off": 0, "lead_only": 1, "all": 2}

// combineFollow keeps the earlier follow date and the more permissive
// notification mode when a user followed both merged performers.
func combineFollow(target, source map[string]any) map[string]any {
	set := map[string]any{}
	if ts, ss := timeOf(target["created_at"]), timeOf(source["created_at"]); !ss.IsZero() && (ts.IsZero() || ss.Before(ts)) {
		set["created_at"] = source["created_at"]
	}
	tm, _ := target["notify_mode"].(string)
	sm, _ := source["notify_mode"].(string)
	if notifyRank[sm] > notifyRank[tm] {
		set["notify_mode"] = sm
	}
	return set
}

// combineWatchLater keeps the lower (earlier in the queue) position.
func combineWatchLater(target, source map[string]any) map[string]any {
	tp, terr := numberOf(target["position"])
	sp, serr := numberOf(source["position"])
	if serr == nil && (terr != nil || sp < tp) {
		return map[string]any{"position": source["position"]}
	}
	return nil
}

// closeAuditIssue resolves a merged entity's open audit issue when the target
// already has an open issue for the same rule.
func closeAuditIssue(entityType string, targetID int64, now time.Time) map[string]any {
	return map[string]any{
		"status":          "fixed",
		"resolved_at":     now.UTC().Format(time.RFC3339Nano),
		"resolution_note": fmt.Sprintf("merged into %s %d, which has the same open issue", entityType, targetID),
	}
}

func timeOf(v any) time.Time {
	s, _ := v.(string)
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

func numberOf(v any) (float64, error) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("identity: %v is not a number", v)
	}
	return n.Float64()
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
// source rows deleted as duplicates of a target row; Patch holds in-place
// column updates (Combine on a target row, Close on a source row).
type TablePlan struct {
	Table string            `json:"table"`
	FK    string            `json:"fk"`
	Key   []string          `json:"key"`
	Move  []json.RawMessage `json:"move"`
	Drop  []json.RawMessage `json:"drop"`
	Patch []RowPatch        `json:"patch,omitempty"`
}

// PatchTarget and PatchSource name which entity's row a RowPatch updates.
const (
	PatchTarget = "target"
	PatchSource = "source"
)

// RowPatch is an in-place update of one row, identified by its Key value
// among the rows of the Side entity: the merge writes Set, undo writes Before
// (the same columns' values before the merge).
type RowPatch struct {
	Side   string          `json:"side"`
	Key    json.RawMessage `json:"key"`
	Set    json.RawMessage `json:"set"`
	Before json.RawMessage `json:"before"`
}

// PatchColumns returns the column names of a RowPatch's Set (or Before)
// object in sorted order.
func PatchColumns(values json.RawMessage) ([]string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(values, &m); err != nil {
		return nil, err
	}
	cols := make([]string, 0, len(m))
	for c := range m {
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols, nil
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
	// Now stamps Close updates (resolved_at); time.Now when zero.
	Now time.Time
}

func decodeRow(row json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(row))
	dec.UseNumber()
	var m map[string]any
	err := dec.Decode(&m)
	return m, err
}

// KeyOf extracts cols from a JSON row as a compact JSON array, the same shape
// SQL produces with jsonb_build_array(cols...).
func KeyOf(row json.RawMessage, cols []string) (json.RawMessage, error) {
	m, err := decodeRow(row)
	if err != nil {
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

// newPatch builds a RowPatch writing set over row, whose current values of
// the same columns become Before.
func newPatch(side string, key json.RawMessage, row, set map[string]any) (RowPatch, error) {
	before := map[string]any{}
	for c := range set {
		v, ok := row[c]
		if !ok {
			return RowPatch{}, fmt.Errorf("identity: row has no column %q", c)
		}
		before[c] = v
	}
	s, err := json.Marshal(set)
	if err != nil {
		return RowPatch{}, err
	}
	b, err := json.Marshal(before)
	if err != nil {
		return RowPatch{}, err
	}
	return RowPatch{Side: side, Key: key, Set: s, Before: b}, nil
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
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	plan := MergePlan{EntityType: in.EntityType, TargetID: in.TargetID, SourceID: in.SourceID, Tables: []TablePlan{}, AddAliases: []Alias{}}
	for _, spec := range specs {
		tp, err := planTable(in, spec, now)
		if err != nil {
			return MergePlan{}, fmt.Errorf("%s: %w", spec.Table, err)
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

func planTable(in MergeInput, spec TableSpec, now time.Time) (TablePlan, error) {
	tp := TablePlan{Table: spec.Table, FK: spec.FK, Key: spec.Key, Move: []json.RawMessage{}, Drop: []json.RawMessage{}}
	dedupeOn := spec.DedupeOn
	if len(dedupeOn) == 0 {
		dedupeOn = spec.Key
	}
	// have maps a dedupe key to the target row holding it, or to nil when a
	// source row moving over claimed it.
	have := map[string]json.RawMessage{}
	if spec.Dedupe {
		for _, row := range in.TargetRows[spec.Table] {
			k, err := KeyOf(row, dedupeOn)
			if err != nil {
				return tp, err
			}
			have[string(k)] = row
		}
	}
	targetKey, _ := json.Marshal([]int64{in.TargetID})
	for _, row := range in.SourceRows[spec.Table] {
		k, err := KeyOf(row, spec.Key)
		if err != nil {
			return tp, err
		}
		if spec.SkipTarget && string(k) == string(targetKey) {
			continue
		}
		dk := k
		if len(spec.DedupeOn) > 0 {
			if dk, err = KeyOf(row, dedupeOn); err != nil {
				return tp, err
			}
		}
		targetRow, dup := have[string(dk)]
		if !spec.Dedupe || !dup {
			have[string(dk)] = nil
			tp.Move = append(tp.Move, k)
			continue
		}
		src, err := decodeRow(row)
		if err != nil {
			return tp, err
		}
		if spec.Close != nil {
			p, err := newPatch(PatchSource, k, src, spec.Close(in.EntityType, in.TargetID, now))
			if err != nil {
				return tp, err
			}
			tp.Patch = append(tp.Patch, p)
			continue
		}
		tp.Drop = append(tp.Drop, row)
		if spec.Combine == nil || targetRow == nil {
			continue
		}
		tgt, err := decodeRow(targetRow)
		if err != nil {
			return tp, err
		}
		if set := spec.Combine(tgt, src); len(set) > 0 {
			tk, err := KeyOf(targetRow, spec.Key)
			if err != nil {
				return tp, err
			}
			p, err := newPatch(PatchTarget, tk, tgt, set)
			if err != nil {
				return tp, err
			}
			tp.Patch = append(tp.Patch, p)
		}
	}
	return tp, nil
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
