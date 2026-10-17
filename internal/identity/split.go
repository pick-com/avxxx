package identity

// Splitting an entity that was never merged (FINAL_DATA_DESIGN 5.5): ingest
// sometimes attaches two different real people (same-name performers from
// different sources) or two different releases (a wrong code match) to ONE
// entity. A split creates a new entity and moves a chosen subset of the
// original's source evidence to it, together with everything derived only
// from that evidence. Undoing a merge is a different operation (UndoMerge).
//
// BuildSplitPlan is pure: the store loads the rows, supplies the field policy
// and applies the plan. Source records are never deleted, only re-pointed.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Kinds of evidence a split can move.
const (
	// RecordOwned is a source_records row owned by the entity (its
	// performer_id / release_id points at it).
	RecordOwned = "record"
	// RecordCredit is a release source record that credits the performer
	// (release_performer_sources); performer splits only. The record itself
	// stays on its release: only the credit evidence moves.
	RecordCredit = "credit"
)

// SplitRecord is one piece of selectable evidence of the original entity.
type SplitRecord struct {
	ID       int64  `json:"id"`
	SourceID int64  `json:"source_id"`
	Kind     string `json:"kind"`
	// Keys are the derived rows this record is evidence for, in the forms
	// "alias:<normalized_name>", "ext:<namespace>:<external_id>" and
	// "ident:<identifier_type>:<normalized_value>".
	Keys []string `json:"keys,omitempty"`
	// CreditedName is the name a credit record credits (RecordCredit only).
	CreditedName string `json:"credited_name,omitempty"`
}

// AliasKey, ExternalIDKey and IdentifierKey build SplitRecord.Keys.
func AliasKey(normalizedName string) string { return "alias:" + normalizedName }

// ExternalIDKey is the key of a performer_external_ids row.
func ExternalIDKey(namespace, id string) string { return "ext:" + namespace + ":" + id }

// IdentifierKey is the key of a release_identifiers row.
func IdentifierKey(typ, normalized string) string { return "ident:" + typ + ":" + normalized }

// FieldEvidence is one source record's current value of one canonical column.
type FieldEvidence struct {
	Column         string          `json:"column"`
	Field          string          `json:"field"`
	SourceRecordID int64           `json:"source_record_id"`
	Value          json.RawMessage `json:"value"`
}

// FieldOutcome is what the field policy decides for one column from a set of
// evidence: Value is the canonical column value (JSON string or null) when OK.
type FieldOutcome struct {
	Value          json.RawMessage
	SourceRecordID int64
	OK             bool
	Conflict       bool
}

// FieldPolicy decides one column from its evidence (the store's
// source_field_policies / source_kind tiers).
type FieldPolicy func(column string, evidence []FieldEvidence) FieldOutcome

// ColumnSpec is one canonical column recomputed by a split.
type ColumnSpec struct {
	Name     string
	Required bool
}

// SplitInput is everything a split plan is built from.
type SplitInput struct {
	EntityType string
	OriginalID int64
	// Original is the entity row (to_jsonb).
	Original json.RawMessage
	// Records is every selectable piece of evidence of the original.
	Records []SplitRecord
	// Selected are the Records ids that move to the new entity.
	Selected []int64
	// Relations holds, per table of SplitTables, the rows on the original.
	Relations map[string][]json.RawMessage
	// Overridden are the original's admin overrides: columns and the
	// relation names "performers" / "tags".
	Overridden map[string]bool
	// LockedReleases (performer splits) are releases whose performer list is
	// an admin override: their release_performers rows are never changed.
	LockedReleases map[int64]bool
	Columns        []ColumnSpec
	Evidence       []FieldEvidence
	Policy         FieldPolicy
	// MediaLinks (release splits) are release_media_sources.source values the
	// admin moves; media links are not derived from source records.
	MediaLinks []string
	// UserMoves are user-data rows the admin explicitly moves (table ->
	// key values, see SplitUserTables); UserRows the matching rows found on
	// the original. User data otherwise stays on the original.
	UserMoves map[string][]int64
	UserRows  map[string][]json.RawMessage
}

// SplitTable is what a split does to one table: Move holds the Key values
// (JSON arrays) of original rows re-pointed at the new entity; Copy holds
// whole original rows inserted again for the new entity (the original keeps
// its row, e.g. a credit still evidenced by a remaining record).
type SplitTable struct {
	Table    string            `json:"table"`
	FK       string            `json:"fk"`
	Key      []string          `json:"key"`
	Move     []json.RawMessage `json:"move"`
	Copy     []json.RawMessage `json:"copy"`
	UserData bool              `json:"user_data,omitempty"`
}

// Field origins in a FieldResult.
const (
	OriginEvidence = "evidence" // decided by the field policy
	OriginOverride = "override" // the original's admin override stands
	OriginCleared  = "cleared"  // the value came only from moved evidence
	OriginKept     = "kept"     // no evidence either way: unchanged
	OriginFallback = "fallback" // required column without evidence
	OriginEmpty    = "empty"    // no evidence: NULL on the new entity
)

// FieldResult is the resulting canonical value of one column.
type FieldResult struct {
	Column         string          `json:"column"`
	Before         json.RawMessage `json:"before,omitempty"`
	Value          json.RawMessage `json:"value"`
	Origin         string          `json:"origin"`
	SourceRecordID int64           `json:"source_record_id,omitempty"`
	Conflict       bool            `json:"conflict,omitempty"`
}

// Write reports whether applying r writes the column.
func (r FieldResult) Write() bool {
	return r.Origin == OriginEvidence || r.Origin == OriginCleared || r.Origin == OriginFallback
}

// SplitPlan is the full description of a split, stored in
// review_events.after_value. NewID and NewSlug are filled when it is applied.
type SplitPlan struct {
	EntityType   string        `json:"entity_type"`
	OriginalID   int64         `json:"original_id"`
	NewID        int64         `json:"new_id"`
	NewSlug      string        `json:"new_slug"`
	MovedRecords []int64       `json:"moved_records"`
	Tables       []SplitTable  `json:"tables"`
	Original     []FieldResult `json:"original_fields"`
	New          []FieldResult `json:"new_fields"`
}

// Field returns the result for column on the original (orig) or new side.
func (p SplitPlan) Field(orig bool, column string) (FieldResult, bool) {
	fs := p.New
	if orig {
		fs = p.Original
	}
	for _, f := range fs {
		if f.Column == column {
			return f, true
		}
	}
	return FieldResult{}, false
}

// SplitTableSpec names a table a split reads rows of (FK = the entity).
type SplitTableSpec struct {
	Table string
	FK    string
	Key   []string
}

// SplitTables lists the derived tables of an entity type, in apply order.
func SplitTables(entityType string) ([]SplitTableSpec, bool) {
	switch entityType {
	case PerformerType:
		return []SplitTableSpec{
			{"source_records", "performer_id", []string{"id"}},
			{"release_performer_sources", "performer_id", []string{"release_id", "source_record_id"}},
			{"release_performers", "performer_id", []string{"release_id"}},
			{"performer_aliases", "performer_id", []string{"normalized_name"}},
			{"performer_external_ids", "performer_id", []string{"namespace", "external_id"}},
			{"performer_source_metrics", "performer_id", []string{"source_id", "captured_at"}},
		}, true
	case ReleaseType:
		return []SplitTableSpec{
			{"source_records", "release_id", []string{"id"}},
			{"release_identifiers", "release_id", []string{"id"}},
			{"release_performer_sources", "release_id", []string{"performer_id", "source_record_id"}},
			{"release_performers", "release_id", []string{"performer_id"}},
			{"release_tag_sources", "release_id", []string{"normalized_tag", "source_record_id"}},
			{"release_tags", "release_id", []string{"normalized_tag"}},
			{"release_source_metrics", "release_id", []string{"source_id", "captured_at"}},
			{"release_media_sources", "release_id", []string{"source"}},
		}, true
	}
	return nil, false
}

// SplitUserTables lists the user-data tables an admin may move explicitly,
// keyed by table, with the column the selection is matched on. Everything
// else of users (and every row not selected) stays on the original.
func SplitUserTables(entityType string) map[string]SplitTableSpec {
	switch entityType {
	case PerformerType:
		return map[string]SplitTableSpec{
			"follows": {"follows", "performer_id", []string{"user_id"}},
		}
	case ReleaseType:
		return map[string]SplitTableSpec{
			"favorites":         {"favorites", "release_id", []string{"user_id"}},
			"watch_history":     {"watch_history", "release_id", []string{"user_id"}},
			"reservations":      {"reservations", "release_id", []string{"user_id"}},
			"watch_later":       {"watch_later", "release_id", []string{"user_id"}},
			"ratings":           {"ratings", "release_id", []string{"id"}},
			"reviews":           {"reviews", "release_id", []string{"id"}},
			"comments":          {"comments", "release_id", []string{"id"}},
			"clips":             {"clips", "release_id", []string{"id"}},
			"release_reactions": {"release_reactions", "release_id", []string{"id"}},
			"playback_sources":  {"playback_sources", "release_id", []string{"id"}},
		}
	}
	return nil
}

// splitState is the selection split into moved and kept evidence.
type splitState struct {
	in                    SplitInput
	moved                 map[int64]bool
	movedKeys, keptKeys   map[string]bool
	movedSrc, keptSrc     map[int64]bool
	movedOwned, keptOwned map[int64]bool
}

// BuildSplitPlan decides what moves from the original to the new entity and
// the canonical fields of both sides. It fails on an empty or unknown
// selection, or one that would leave the original without any evidence.
func BuildSplitPlan(in SplitInput) (SplitPlan, error) {
	specs, ok := SplitTables(in.EntityType)
	if !ok {
		return SplitPlan{}, fmt.Errorf("identity: unknown entity type %q", in.EntityType)
	}
	if in.OriginalID <= 0 {
		return SplitPlan{}, fmt.Errorf("identity: invalid entity id %d", in.OriginalID)
	}
	if in.Policy == nil {
		return SplitPlan{}, fmt.Errorf("identity: split needs a field policy")
	}
	st, err := newSplitState(in)
	if err != nil {
		return SplitPlan{}, err
	}
	plan := SplitPlan{EntityType: in.EntityType, OriginalID: in.OriginalID, MovedRecords: []int64{}, Tables: []SplitTable{}}
	for id := range st.moved {
		plan.MovedRecords = append(plan.MovedRecords, id)
	}
	sort.Slice(plan.MovedRecords, func(i, j int) bool { return plan.MovedRecords[i] < plan.MovedRecords[j] })
	for _, spec := range specs {
		tp, err := st.planTable(spec)
		if err != nil {
			return SplitPlan{}, fmt.Errorf("%s: %w", spec.Table, err)
		}
		plan.Tables = append(plan.Tables, tp)
	}
	users, err := st.planUserData()
	if err != nil {
		return SplitPlan{}, err
	}
	plan.Tables = append(plan.Tables, users...)
	if plan.Original, plan.New, err = st.planFields(); err != nil {
		return SplitPlan{}, err
	}
	return plan, nil
}

func newSplitState(in SplitInput) (*splitState, error) {
	st := &splitState{in: in, moved: map[int64]bool{}, movedKeys: map[string]bool{}, keptKeys: map[string]bool{},
		movedSrc: map[int64]bool{}, keptSrc: map[int64]bool{}, movedOwned: map[int64]bool{}, keptOwned: map[int64]bool{}}
	byID := map[int64]SplitRecord{}
	for _, r := range in.Records {
		if r.Kind != RecordOwned && r.Kind != RecordCredit {
			return nil, fmt.Errorf("identity: record %d has unknown kind %q", r.ID, r.Kind)
		}
		if r.Kind == RecordCredit && in.EntityType != PerformerType {
			return nil, fmt.Errorf("identity: credit records belong to performer splits")
		}
		byID[r.ID] = r
	}
	if len(in.Selected) == 0 {
		return nil, &SplitError{Msg: "select at least one source record to split off"}
	}
	for _, id := range in.Selected {
		if _, ok := byID[id]; !ok {
			return nil, &SplitError{Msg: fmt.Sprintf("source record %d is not evidence of %s %d", id, in.EntityType, in.OriginalID)}
		}
		st.moved[id] = true
	}
	if len(st.moved) == len(byID) {
		return nil, &SplitError{Msg: "a split must leave at least one source record on the original"}
	}
	for _, r := range in.Records {
		keys, src, owned := st.keptKeys, st.keptSrc, st.keptOwned
		if st.moved[r.ID] {
			keys, src, owned = st.movedKeys, st.movedSrc, st.movedOwned
		}
		for _, k := range r.Keys {
			keys[k] = true
		}
		src[r.SourceID] = true
		if r.Kind == RecordOwned {
			owned[r.ID] = true
		}
	}
	return st, nil
}

// SplitError is a selection the plan cannot be built from; the API maps it
// to 400.
type SplitError struct{ Msg string }

func (e *SplitError) Error() string { return e.Msg }

func (st *splitState) planTable(spec SplitTableSpec) (SplitTable, error) {
	tp := SplitTable{Table: spec.Table, FK: spec.FK, Key: spec.Key, Move: []json.RawMessage{}, Copy: []json.RawMessage{}}
	rows := st.in.Relations[spec.Table]
	add := func(row json.RawMessage, copyRow bool) error {
		if copyRow {
			tp.Copy = append(tp.Copy, row)
			return nil
		}
		k, err := KeyOf(row, spec.Key)
		if err != nil {
			return err
		}
		tp.Move = append(tp.Move, k)
		return nil
	}
	switch spec.Table {
	case "source_records":
		for _, row := range rows {
			m, err := decodeRow(row)
			if err != nil {
				return tp, err
			}
			if id := int64Of(m["id"]); st.movedOwned[id] {
				if err = add(row, false); err != nil {
					return tp, err
				}
			}
		}
	case "release_performer_sources", "release_tag_sources":
		for _, row := range rows {
			m, err := decodeRow(row)
			if err != nil {
				return tp, err
			}
			if st.moved[int64Of(m["source_record_id"])] {
				if err = add(row, false); err != nil {
					return tp, err
				}
			}
		}
	case "release_performers", "release_tags":
		evTable, groupCol := "release_performer_sources", "release_id"
		locked := func(m map[string]any) bool { return st.in.LockedReleases[int64Of(m["release_id"])] }
		lockCopies := false
		if st.in.EntityType == ReleaseType {
			groupCol = "performer_id"
			locked = func(map[string]any) bool { return st.in.Overridden["performers"] }
			lockCopies = true
			if spec.Table == "release_tags" {
				evTable, groupCol = "release_tag_sources", "normalized_tag"
				locked = func(map[string]any) bool { return st.in.Overridden["tags"] }
			}
		}
		groups, err := st.evidenceGroups(evTable, groupCol)
		if err != nil {
			return tp, err
		}
		for _, row := range rows {
			m, err := decodeRow(row)
			if err != nil {
				return tp, err
			}
			g := groups[fmt.Sprint(m[groupCol])]
			if g.moved == 0 {
				continue // no moved evidence (or a manual row): stays
			}
			switch {
			case locked(m) && !lockCopies:
				// The release's performer list is an admin override.
			case locked(m) || g.kept > 0:
				err = add(row, true)
			default:
				err = add(row, false)
			}
			if err != nil {
				return tp, err
			}
		}
	case "performer_aliases":
		err := st.byEvidence(rows, func(m map[string]any) string { return AliasKey(fmt.Sprint(m["normalized_name"])) }, add)
		if err != nil {
			return tp, err
		}
	case "performer_external_ids":
		err := st.byEvidence(rows, func(m map[string]any) string {
			return ExternalIDKey(fmt.Sprint(m["namespace"]), fmt.Sprint(m["external_id"]))
		}, add)
		if err != nil {
			return tp, err
		}
	case "release_identifiers":
		err := st.byEvidence(rows, func(m map[string]any) string {
			return IdentifierKey(fmt.Sprint(m["identifier_type"]), fmt.Sprint(m["normalized_value"]))
		}, add)
		if err != nil {
			return tp, err
		}
	case "performer_source_metrics", "release_source_metrics":
		if err := st.byEvidence(rows, func(map[string]any) string { return "" }, add); err != nil {
			return tp, err
		}
	case "release_media_sources":
		want := map[string]bool{}
		for _, s := range st.in.MediaLinks {
			want[strings.ToLower(strings.TrimSpace(s))] = true
		}
		found := map[string]bool{}
		for _, row := range rows {
			m, err := decodeRow(row)
			if err != nil {
				return tp, err
			}
			if src := fmt.Sprint(m["source"]); want[src] {
				found[src] = true
				if err = add(row, false); err != nil {
					return tp, err
				}
			}
		}
		for s := range want {
			if !found[s] {
				return tp, &SplitError{Msg: fmt.Sprintf("release %d has no %q media link", st.in.OriginalID, s)}
			}
		}
	default:
		return tp, fmt.Errorf("identity: no split rule for %s", spec.Table)
	}
	return tp, nil
}

type evidenceCount struct{ moved, kept int }

// evidenceGroups counts the moved and kept evidence rows of table per value of
// groupCol (the release of a performer's credit, the performer of a release's
// credit, a tag).
func (st *splitState) evidenceGroups(table, groupCol string) (map[string]evidenceCount, error) {
	out := map[string]evidenceCount{}
	for _, row := range st.in.Relations[table] {
		m, err := decodeRow(row)
		if err != nil {
			return nil, err
		}
		k := fmt.Sprint(m[groupCol])
		c := out[k]
		if st.moved[int64Of(m["source_record_id"])] {
			c.moved++
		} else {
			c.kept++
		}
		out[k] = c
	}
	return out, nil
}

// byEvidence moves a derived row when it is evidenced only by moved records:
// by its evidence key when any record carries that key, otherwise by the
// source that wrote it (moved only when no kept record comes from that
// source). Rows evidenced by both sides, by nothing, or by no source stay.
func (st *splitState) byEvidence(rows []json.RawMessage, key func(map[string]any) string, add func(json.RawMessage, bool) error) error {
	for _, row := range rows {
		m, err := decodeRow(row)
		if err != nil {
			return err
		}
		k := key(m)
		var move bool
		if k != "" && (st.movedKeys[k] || st.keptKeys[k]) {
			move = st.movedKeys[k] && !st.keptKeys[k]
		} else if src := int64Of(m["source_id"]); src > 0 {
			move = st.movedSrc[src] && !st.keptSrc[src]
		}
		if move {
			if err = add(row, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (st *splitState) planUserData() ([]SplitTable, error) {
	allowed := SplitUserTables(st.in.EntityType)
	names := make([]string, 0, len(st.in.UserMoves))
	for t := range st.in.UserMoves {
		names = append(names, t)
	}
	sort.Strings(names)
	out := []SplitTable{}
	for _, t := range names {
		spec, ok := allowed[t]
		if !ok {
			return nil, &SplitError{Msg: fmt.Sprintf("%s rows of a %s cannot be moved", t, st.in.EntityType)}
		}
		have := map[int64]bool{}
		for _, row := range st.in.UserRows[t] {
			m, err := decodeRow(row)
			if err != nil {
				return nil, err
			}
			have[int64Of(m[spec.Key[0]])] = true
		}
		tp := SplitTable{Table: spec.Table, FK: spec.FK, Key: spec.Key, Move: []json.RawMessage{}, Copy: []json.RawMessage{}, UserData: true}
		seen := map[int64]bool{}
		for _, v := range st.in.UserMoves[t] {
			if seen[v] {
				continue
			}
			seen[v] = true
			if !have[v] {
				return nil, &SplitError{Msg: fmt.Sprintf("%s %s=%d is not on %s %d", t, spec.Key[0], v, st.in.EntityType, st.in.OriginalID)}
			}
			k, _ := json.Marshal([]int64{v})
			tp.Move = append(tp.Move, k)
		}
		if len(tp.Move) > 0 {
			out = append(out, tp)
		}
	}
	return out, nil
}

// planFields recomputes every canonical column of both sides from their
// evidence. The original keeps its overrides; the new entity has none.
func (st *splitState) planFields() (orig, next []FieldResult, err error) {
	row, err := decodeRow(st.in.Original)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: original row: %w", err)
	}
	keptEv, movedEv := map[string][]FieldEvidence{}, map[string][]FieldEvidence{}
	for _, e := range st.in.Evidence {
		switch {
		case st.movedOwned[e.SourceRecordID]:
			movedEv[e.Column] = append(movedEv[e.Column], e)
		case st.keptOwned[e.SourceRecordID]:
			keptEv[e.Column] = append(keptEv[e.Column], e)
		}
	}
	creditName := ""
	for _, r := range st.in.Records {
		if st.moved[r.ID] && r.Kind == RecordCredit && strings.TrimSpace(r.CreditedName) != "" {
			creditName = strings.TrimSpace(r.CreditedName)
			break
		}
	}
	cols := append([]ColumnSpec(nil), st.in.Columns...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
	null := json.RawMessage("null")
	for _, c := range cols {
		before, _ := json.Marshal(row[c.Name])
		movedOut := st.in.Policy(c.Name, movedEv[c.Name])
		o := FieldResult{Column: c.Name, Before: before}
		switch keptOut := st.in.Policy(c.Name, keptEv[c.Name]); {
		case st.in.Overridden[c.Name]:
			o.Value, o.Origin = before, OriginOverride
		case keptOut.OK:
			o.Value, o.Origin, o.SourceRecordID, o.Conflict = keptOut.Value, OriginEvidence, keptOut.SourceRecordID, keptOut.Conflict
		case movedOut.OK && !c.Required && SameValue(before, movedOut.Value):
			o.Value, o.Origin = null, OriginCleared
		default:
			o.Value, o.Origin = before, OriginKept
		}
		orig = append(orig, o)

		n := FieldResult{Column: c.Name}
		switch {
		case movedOut.OK:
			n.Value, n.Origin, n.SourceRecordID, n.Conflict = movedOut.Value, OriginEvidence, movedOut.SourceRecordID, movedOut.Conflict
		case c.Required && creditName != "":
			n.Value, _ = json.Marshal(creditName)
			n.Origin = OriginFallback
		case c.Required:
			n.Value, n.Origin = before, OriginFallback
		default:
			n.Value, n.Origin = null, OriginEmpty
		}
		next = append(next, n)
	}
	return orig, next, nil
}

// SameValue compares two JSON column values loosely: numbers by value
// ("158" equals 158.00), strings case-sensitively after trimming, null only
// to null.
func SameValue(a, b json.RawMessage) bool {
	as, aok := scalarText(a)
	bs, bok := scalarText(b)
	if !aok || !bok {
		return !aok && !bok
	}
	if as == bs {
		return true
	}
	af, aerr := strconv.ParseFloat(as, 64)
	bf, berr := strconv.ParseFloat(bs, 64)
	return aerr == nil && berr == nil && math.Abs(af-bf) < 1e-9
}

// scalarText is the text of a JSON string or number; ok=false for null,
// empty or anything else.
func scalarText(raw json.RawMessage) (string, bool) {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return "", false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "", false
	}
	switch x := v.(type) {
	case string:
		x = strings.TrimSpace(x)
		return x, x != ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return s, true
}

// int64Of reads an integer column from a decoded row (json.Number).
func int64Of(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return n
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}
