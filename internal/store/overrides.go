package store

// Manual overrides and reviewable fields (FINAL_DATA_DESIGN 2.2 / 6,
// migrations/020_admin_edit.sql). A field an admin set by hand — through a
// conflict resolution or the admin release editor — gets a row in
// release_overrides / performer_overrides keyed by its column name, and
// catalog ingest leaves it alone until the override is cleared explicitly.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ValidationError is a client mistake in an admin edit (unknown field, bad
// value, missing reason). Its message is safe to show; the API maps it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// fieldKind is the SQL type of a reviewable column.
type fieldKind int

const (
	fieldText fieldKind = iota
	fieldDate
	fieldInt
	fieldNumeric
)

func (k fieldKind) sqlType() string {
	switch k {
	case fieldDate:
		return "date"
	case fieldInt:
		return "integer"
	case fieldNumeric:
		return "numeric"
	}
	return "text"
}

// FieldSpec maps a field name to the canonical column it edits.
type FieldSpec struct {
	Column   string
	Kind     fieldKind
	Required bool // NOT NULL / must not be empty
	Min, Max int  // inclusive bounds for fieldInt (0,0 = unbounded)
}

// Release columns, keyed by every name a conflict or the editor may use: the
// parser field names that SaveCatalogRecord writes into source_field_values
// (title, maker, director, label, ...) and the column names themselves.
var releaseFields = map[string]FieldSpec{
	"canonical_title": {Column: "canonical_title", Kind: fieldText, Required: true},
	"title_ja":        {Column: "title_ja", Kind: fieldText},
	"synopsis":        {Column: "synopsis", Kind: fieldText},
	"release_date":    {Column: "release_date", Kind: fieldDate},
	"shoot_year":      {Column: "shoot_year", Kind: fieldInt, Min: 1900, Max: 2100},
	"runtime_minutes": {Column: "runtime_minutes", Kind: fieldInt, Min: 1, Max: 1440},
	"maker_name":      {Column: "maker_name", Kind: fieldText},
	"label_name":      {Column: "label_name", Kind: fieldText},
	"series_name":     {Column: "series_name", Kind: fieldText},
	"director_name":   {Column: "director_name", Kind: fieldText},
}

var releaseFieldAliases = map[string]string{
	"title": "canonical_title", "runtime": "runtime_minutes", "maker": "maker_name",
	"label": "label_name", "series": "series_name", "director": "director_name",
}

var performerFields = map[string]FieldSpec{
	"canonical_name":    {Column: "canonical_name", Kind: fieldText, Required: true},
	"canonical_name_ja": {Column: "canonical_name_ja", Kind: fieldText},
	"birth_date":        {Column: "birth_date", Kind: fieldDate},
	"birth_place":       {Column: "birth_place", Kind: fieldText},
	"debut_date":        {Column: "debut_date", Kind: fieldDate},
	"height_cm":         {Column: "height_cm", Kind: fieldNumeric},
	"cup_size":          {Column: "cup_size", Kind: fieldText},
	"shoe_size":         {Column: "shoe_size", Kind: fieldNumeric},
	"biography":         {Column: "biography", Kind: fieldText},
}

var performerFieldAliases = map[string]string{"name": "canonical_name", "name_ja": "canonical_name_ja"}

// ConflictField maps a field_conflicts.field_name (a parser field name such
// as "title" or "maker", or a column name) to the column it resolves into.
// Identifiers (dvd_code, content_id) and relation lists (tags, performers,
// genres) are not resolvable as a single column: identifiers belong to
// identity review, relations to the admin release editor.
func ConflictField(entityType, field string) (FieldSpec, bool) {
	fields, aliases := releaseFields, releaseFieldAliases
	if entityType == "performer" {
		fields, aliases = performerFields, performerFieldAliases
	} else if entityType != "release" {
		return FieldSpec{}, false
	}
	if c, ok := aliases[field]; ok {
		field = c
	}
	spec, ok := fields[field]
	return spec, ok
}

// CoerceFieldValue turns a JSON value into the text form of spec's column
// (cast with ::<type> in SQL), or nil for SQL NULL. JSON null and "" are NULL
// unless the field is required. Numbers may be given as JSON numbers or
// numeric strings (source_field_values stores parser output as strings).
func CoerceFieldValue(spec FieldSpec, raw json.RawMessage) (*string, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		if spec.Required {
			return nil, invalid("%s is required", spec.Column)
		}
		return nil, nil
	}
	var text string
	var num json.Number
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, invalid("%s: invalid JSON value", spec.Column)
	}
	switch x := v.(type) {
	case string:
		text = strings.TrimSpace(x)
	case json.Number:
		num = x
		text = x.String()
	default:
		return nil, invalid("%s: value must be a string or number", spec.Column)
	}
	if text == "" {
		if spec.Required {
			return nil, invalid("%s is required", spec.Column)
		}
		return nil, nil
	}
	switch spec.Kind {
	case fieldText:
		if num != "" {
			return nil, invalid("%s must be a string", spec.Column)
		}
		if len(text) > 10000 {
			return nil, invalid("%s too long", spec.Column)
		}
	case fieldDate:
		t, err := time.Parse(time.DateOnly, text)
		if err != nil {
			return nil, invalid("%s must be YYYY-MM-DD", spec.Column)
		}
		text = t.Format(time.DateOnly)
	case fieldInt:
		n, err := strconv.Atoi(text)
		if err != nil {
			return nil, invalid("%s must be an integer", spec.Column)
		}
		if (spec.Min != 0 || spec.Max != 0) && (n < spec.Min || n > spec.Max) {
			return nil, invalid("%s must be between %d and %d", spec.Column, spec.Min, spec.Max)
		}
		text = strconv.Itoa(n)
	case fieldNumeric:
		f, err := strconv.ParseFloat(text, 64)
		if err != nil || f < 0 || f > 1000 {
			return nil, invalid("%s must be a number between 0 and 1000", spec.Column)
		}
	}
	return &text, nil
}

// Override field names for the release relations edited as a whole.
const (
	OverrideTags       = "tags"
	OverridePerformers = "performers"
)

// relationFields maps parser fields to the relation override that covers them.
var relationFields = map[string]string{"tags": OverrideTags, "genres": OverrideTags, "performers": OverridePerformers}

// fieldOverridden reports whether a parser field of an entity is covered by
// one of its overrides (by column or by relation).
func fieldOverridden(entityType, field string, overridden map[string]bool) bool {
	if len(overridden) == 0 {
		return false
	}
	if overridden[field] {
		return true
	}
	if spec, ok := ConflictField(entityType, field); ok && overridden[spec.Column] {
		return true
	}
	if entityType == "release" {
		if rel, ok := relationFields[field]; ok && overridden[rel] {
			return true
		}
	}
	return false
}

// SET lists of the SaveCatalogRecord upserts. Every column the crawl writes
// on an existing row keeps its value while the row has an override for it.
var (
	releaseIngestUpdate = strings.Join([]string{
		overrideGuard("releases", "release_overrides", "release_id", "canonical_title", "EXCLUDED.canonical_title"),
		overrideGuard("releases", "release_overrides", "release_id", "maker_name", "COALESCE(NULLIF(EXCLUDED.maker_name,''),releases.maker_name)"),
	}, ",")
	performerIngestUpdate = strings.Join([]string{
		overrideGuard("performers", "performer_overrides", "performer_id", "canonical_name_ja", "COALESCE(NULLIF(EXCLUDED.canonical_name_ja,''),performers.canonical_name_ja)"),
		overrideGuard("performers", "performer_overrides", "performer_id", "birth_date", "COALESCE(EXCLUDED.birth_date,performers.birth_date)"),
		overrideGuard("performers", "performer_overrides", "performer_id", "birth_place", "COALESCE(NULLIF(EXCLUDED.birth_place,''),performers.birth_place)"),
		overrideGuard("performers", "performer_overrides", "performer_id", "debut_date", "COALESCE(EXCLUDED.debut_date,performers.debut_date)"),
		overrideGuard("performers", "performer_overrides", "performer_id", "height_cm", "COALESCE(EXCLUDED.height_cm,performers.height_cm)"),
		overrideGuard("performers", "performer_overrides", "performer_id", "cup_size", "COALESCE(NULLIF(EXCLUDED.cup_size,''),performers.cup_size)"),
	}, ",")
)

// overrideGuard returns "col=<expr>" for an ON CONFLICT DO UPDATE SET list,
// keeping the current value when the row has an override for col.
func overrideGuard(table, overrideTable, overrideFK, col, expr string) string {
	return fmt.Sprintf("%[3]s=CASE WHEN EXISTS(SELECT 1 FROM %[2]s o WHERE o.%[4]s=%[1]s.id AND o.field_name='%[3]s') THEN %[1]s.%[3]s ELSE %[5]s END",
		table, overrideTable, col, overrideFK, expr)
}

// overriddenFields returns the field names with an override on one entity.
func overriddenFields(ctx context.Context, q querier, entityType string, id int64) (map[string]bool, error) {
	table, fk := "release_overrides", "release_id"
	if entityType == "performer" {
		table, fk = "performer_overrides", "performer_id"
	}
	rows, err := q.Query(ctx, fmt.Sprintf(`SELECT field_name FROM %s WHERE %s=$1`, table, fk), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var f string
		if err = rows.Scan(&f); err != nil {
			return nil, err
		}
		out[f] = true
	}
	return out, rows.Err()
}

// upsertOverride records (or refreshes) one override row.
func upsertOverride(ctx context.Context, q execer, entityType string, id int64, field string, value []byte, reason, actor string, conflictID int64) error {
	table, fk := "release_overrides", "release_id"
	if entityType == "performer" {
		table, fk = "performer_overrides", "performer_id"
	}
	var v any
	if len(value) > 0 {
		v = string(value)
	}
	_, err := q.Exec(ctx, fmt.Sprintf(`INSERT INTO %[1]s(%[2]s,field_name,value,reason,actor,conflict_id)
		VALUES($1,$2,$3::jsonb,NULLIF($4,''),$5,NULLIF($6,0))
		ON CONFLICT(%[2]s,field_name) DO UPDATE SET value=EXCLUDED.value,reason=EXCLUDED.reason,actor=EXCLUDED.actor,
		conflict_id=EXCLUDED.conflict_id,updated_at=now()`, table, fk), id, field, v, reason, actor, conflictID)
	return err
}

// MaxReasonLen bounds free-text reasons and notes.
const MaxReasonLen = 2000

// rawOrNull turns a scanned jsonb that may be SQL NULL (to_jsonb(NULL) is
// NULL, not JSON null) into JSON, so it is never cast from an empty string to jsonb.
func rawOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}

// ErrNotReviewable is returned by ResolveConflict for a conflict whose field
// has no single canonical column (identifiers, relation lists).
var ErrNotReviewable = errors.New("field is not reviewable")
