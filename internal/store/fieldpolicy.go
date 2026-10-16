package store

// Field priority policy (FINAL_DATA_DESIGN 2.4, 5.4): every source's value is
// kept as evidence in source_field_values; the canonical column of a
// performer/release takes the value of the highest-priority source that has a
// usable one. Priority per (entity type, field, source) comes from
// source_field_policies; without a row the source_kind tier applies. Lower
// numbers win. Admin overrides always win and are never recomputed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultTierPriority is the default priority of a source_kind (FINAL_DATA_DESIGN
// 2.4 tiers). Policy rows use the same scale, so an explicit priority of 15
// slots a source between official_catalog and official_maker for one field.
var DefaultTierPriority = map[string]int{
	"official_catalog":    10,
	"official_maker":      20,
	"structured_database": 30,
	"aggregator":          40,
	"third_party_api":     50,
}

// unknownTierPriority applies to any other source_kind (fixtures, unknown).
const unknownTierPriority = 60

// TierPriority returns the default priority of a source_kind.
func TierPriority(kind string) int {
	if p, ok := DefaultTierPriority[kind]; ok {
		return p
	}
	return unknownTierPriority
}

// fieldCandidate is one source's current value of one field.
type fieldCandidate struct {
	SourceRecordID int64           `json:"source_record_id"`
	SourceID       int64           `json:"source_id"`
	SourceKind     string          `json:"source_kind"`
	SourceRank     int             `json:"-"` // sources.priority, tie-break
	Policy         *int            `json:"-"` // source_field_policies.priority
	Priority       int             `json:"priority"`
	Value          json.RawMessage `json:"value"`
}

// effectivePriority is the policy row's priority, else the tier default.
func (c fieldCandidate) effectivePriority() int {
	if c.Policy != nil {
		return *c.Policy
	}
	return TierPriority(c.SourceKind)
}

// fallbackOnly reports whether c is missing-field fallback evidence: a
// third_party_api source without an explicit policy row for the field.
func (c fieldCandidate) fallbackOnly() bool {
	return c.SourceKind == FallbackSourceKind && c.Policy == nil
}

// isEmptyValue reports whether a parser value carries no information
// (null, "", whitespace, [] or {}): such values are evidence of absence, not
// candidates for the canonical field or conflicts.
func isEmptyValue(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	if len(v) == 0 || string(v) == "null" || string(v) == "[]" || string(v) == "{}" {
		return true
	}
	var s string
	if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) == "" {
		return true
	}
	return false
}

// isListValue reports whether raw is a JSON array. List fields (tags,
// genres, performers, release_refs) are merged as unions (FINAL_DATA_DESIGN
// 5.4), so they never pick a winner or open a conflict.
func isListValue(raw json.RawMessage) bool {
	v := bytes.TrimSpace(raw)
	return len(v) > 0 && v[0] == '['
}

// compareKey normalizes a value for agreement checks: for a canonical
// column the coerced SQL text (so "158" and 158 agree), else compact JSON.
func compareKey(entityType, field string, raw json.RawMessage) string {
	if spec, ok := ConflictField(entityType, field); ok {
		if v, err := CoerceFieldValue(spec, raw); err == nil && v != nil {
			return strings.ToLower(*v)
		}
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) == nil {
		return strings.ToLower(buf.String())
	}
	return strings.ToLower(string(raw))
}

// rankCandidates drops empty values and orders the rest best first:
// effective priority, then sources.priority, then source id.
func rankCandidates(cands []fieldCandidate) []fieldCandidate {
	out := make([]fieldCandidate, 0, len(cands))
	for _, c := range cands {
		if isEmptyValue(c.Value) {
			continue
		}
		c.Priority = c.effectivePriority()
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.SourceRank != b.SourceRank {
			return a.SourceRank < b.SourceRank
		}
		return a.SourceID < b.SourceID
	})
	return out
}

// fieldDecision is the outcome for one field of one entity.
type fieldDecision struct {
	// Winner is the best usable candidate; ok=false when every candidate
	// is empty (the canonical column is left alone).
	Winner fieldCandidate
	ok     bool
	// Conflict is true when some candidate disagrees with the winner.
	Conflict bool
	Ranked   []fieldCandidate
}

// decideField picks the canonical value of field from cands. For a
// canonical column the winner is the best candidate whose value coerces into
// the column type (an unparsable date from a better source does not blank the
// column). A conflict opens only when a candidate disagrees with the winner.
func decideField(entityType, field string, cands []fieldCandidate) fieldDecision {
	ranked := rankCandidates(cands)
	d := fieldDecision{Ranked: ranked}
	spec, canonical := ConflictField(entityType, field)
	for _, c := range ranked {
		if canonical {
			if v, err := CoerceFieldValue(spec, c.Value); err != nil || v == nil {
				continue
			}
		}
		d.Winner, d.ok = c, true
		break
	}
	if !d.ok {
		return d
	}
	if isListValue(d.Winner.Value) {
		return d
	}
	want := compareKey(entityType, field, d.Winner.Value)
	for _, c := range ranked {
		if c.fallbackOnly() {
			// Fallback evidence (third_party_api without a policy row) joins
			// a conflict other sources open, but never opens one by itself.
			continue
		}
		if compareKey(entityType, field, c.Value) != want {
			d.Conflict = true
			break
		}
	}
	return d
}

// applyFieldPolicy recomputes the canonical value and the conflict state of
// every field in fields for one entity, after this record's evidence was
// written. Overridden fields keep the admin's value and open no conflict.
func applyFieldPolicy(ctx context.Context, tx pgx.Tx, entityType string, entityID int64, fields []string, overridden map[string]bool) error {
	if len(fields) == 0 {
		return nil
	}
	pol, err := loadPolicies(ctx, tx, entityType)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT sf.field_name, sf.normalized_value, sf.source_record_id, sr.source_id, s.source_kind, s.priority
		FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id JOIN sources s ON s.id=sr.source_id
		WHERE sf.is_current AND sf.field_name = ANY($3::text[]) AND sr.entity_type=$1
		  AND (($1='performer' AND sr.performer_id=$2) OR ($1='release' AND sr.release_id=$2))`,
		entityType, entityID, fields)
	if err != nil {
		return err
	}
	byField := map[string][]fieldCandidate{}
	for rows.Next() {
		var f string
		var c fieldCandidate
		var raw []byte
		var rank int16
		if err = rows.Scan(&f, &raw, &c.SourceRecordID, &c.SourceID, &c.SourceKind, &rank); err != nil {
			rows.Close()
			return err
		}
		c.Value, c.SourceRank = json.RawMessage(raw), int(rank)
		c.Policy = pol.lookup(entityType, f, c.SourceID)
		byField[f] = append(byField[f], c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	table, overrideTable, fk := "releases", "release_overrides", "release_id"
	if entityType == "performer" {
		table, overrideTable, fk = "performers", "performer_overrides", "performer_id"
	}
	sort.Strings(fields)
	for _, field := range fields {
		if fieldOverridden(entityType, field, overridden) {
			continue // the admin's value stands; no recompute, no conflict
		}
		d := decideField(entityType, field, byField[field])
		if !d.ok {
			continue
		}
		if spec, ok := ConflictField(entityType, field); ok {
			v, _ := CoerceFieldValue(spec, d.Winner.Value)
			if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s=$2::%[3]s WHERE id=$1 AND %[2]s IS DISTINCT FROM $2::%[3]s
				AND NOT EXISTS (SELECT 1 FROM %[4]s o WHERE o.%[5]s=$1 AND o.field_name='%[2]s')`, table, spec.Column, spec.Kind.sqlType(), overrideTable, fk),
				entityID, *v); err != nil {
				return err
			}
		}
		if !d.Conflict {
			continue
		}
		candidates, err := json.Marshal(d.Ranked)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO field_conflicts(entity_type,entity_id,field_name,current_value,candidates,severity,status)
			VALUES($1,$2,$3,$4::jsonb,$5::jsonb,'medium','pending')
			ON CONFLICT (entity_type,entity_id,field_name) WHERE status='pending'
			DO UPDATE SET current_value=EXCLUDED.current_value,candidates=EXCLUDED.candidates,updated_at=now()`,
			entityType, entityID, field, string(d.Winner.Value), string(candidates)); err != nil {
			return err
		}
	}
	return nil
}

// policySet is the source_field_policies rows of one entity type, keyed by
// stored field name and source.
type policySet map[string]map[int64]int

func (p policySet) lookup(entityType, field string, sourceID int64) *int {
	names := []string{field}
	if spec, ok := ConflictField(entityType, field); ok && spec.Column != field {
		names = append(names, spec.Column)
	}
	for _, n := range names {
		if v, ok := p[n][sourceID]; ok {
			return &v
		}
	}
	return nil
}

func loadPolicies(ctx context.Context, q querier, entityType string) (policySet, error) {
	rows, err := q.Query(ctx, `SELECT field_name, source_id, priority FROM source_field_policies WHERE entity_type=$1`, entityType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := policySet{}
	for rows.Next() {
		var f string
		var src int64
		var prio int16
		if err = rows.Scan(&f, &src, &prio); err != nil {
			return nil, err
		}
		if out[f] == nil {
			out[f] = map[int64]int{}
		}
		out[f][src] = int(prio)
	}
	return out, rows.Err()
}

// ---- Admin API.

// FieldPolicy is one source_field_policies row.
type FieldPolicy struct {
	EntityType string     `json:"entity_type"`
	FieldName  string     `json:"field_name"`
	SourceID   int64      `json:"source_id"`
	SourceName string     `json:"source_name,omitempty"`
	Priority   int        `json:"priority"`
	UpdatedBy  string     `json:"updated_by,omitempty"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
}

// EffectiveSource is one source in the effective order of a field.
type EffectiveSource struct {
	SourceID   int64  `json:"source_id"`
	SourceName string `json:"source_name"`
	SourceKind string `json:"source_kind"`
	Priority   int    `json:"priority"`
	FromPolicy bool   `json:"from_policy"`
}

var fieldNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// normalizePolicyField validates a policy's entity type and field name and
// maps parser aliases (title, maker, name, ...) to the canonical column, so
// one field has one name in source_field_policies.
func normalizePolicyField(entityType, field string) (string, error) {
	if entityType != "performer" && entityType != "release" {
		return "", invalid("entity_type must be performer or release")
	}
	field = strings.ToLower(strings.TrimSpace(field))
	if !fieldNamePattern.MatchString(field) {
		return "", invalid("field_name must be a lower-case identifier")
	}
	if spec, ok := ConflictField(entityType, field); ok {
		return spec.Column, nil
	}
	return field, nil
}

// FieldPolicies lists every policy row.
func (s *Store) FieldPolicies(ctx context.Context) ([]FieldPolicy, error) {
	rows, err := s.Query(ctx, `SELECT p.entity_type,p.field_name,p.source_id,s.name,p.priority,COALESCE(p.updated_by,''),p.updated_at
		FROM source_field_policies p JOIN sources s ON s.id=p.source_id ORDER BY p.entity_type,p.field_name,p.priority,p.source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FieldPolicy{}
	for rows.Next() {
		var p FieldPolicy
		var prio int16
		if err = rows.Scan(&p.EntityType, &p.FieldName, &p.SourceID, &p.SourceName, &prio, &p.UpdatedBy, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Priority = int(prio)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetFieldPolicy upserts one policy row and audits it in review_events.
func (s *Store) SetFieldPolicy(ctx context.Context, p FieldPolicy, reason, actor string) (FieldPolicy, error) {
	field, err := normalizePolicyField(p.EntityType, p.FieldName)
	if err != nil {
		return p, err
	}
	if p.Priority < 0 || p.Priority > 1000 {
		return p, invalid("priority must be between 0 and 1000 (lower wins)")
	}
	if len(reason) > MaxReasonLen {
		return p, invalid("reason too long")
	}
	p.FieldName = field
	tx, err := s.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var before *int16
	err = tx.QueryRow(ctx, `SELECT priority FROM source_field_policies WHERE entity_type=$1 AND field_name=$2 AND source_id=$3`, p.EntityType, field, p.SourceID).Scan(&before)
	if err != nil && err != pgx.ErrNoRows {
		return p, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO source_field_policies(entity_type,field_name,source_id,priority,updated_by,updated_at)
		SELECT $1,$2,s.id,$4,$5,now() FROM sources s WHERE s.id=$3
		ON CONFLICT(entity_type,field_name,source_id) DO UPDATE SET priority=EXCLUDED.priority,updated_by=EXCLUDED.updated_by,updated_at=now()
		RETURNING updated_at`, p.EntityType, field, p.SourceID, p.Priority, actor).Scan(&p.UpdatedAt); err != nil {
		return p, err // pgx.ErrNoRows: unknown source -> 404
	}
	p.UpdatedBy = actor
	ev := map[string]any{"entity_type": p.EntityType, "field_name": field}
	after := map[string]any{"entity_type": p.EntityType, "field_name": field, "priority": p.Priority}
	if before != nil {
		ev["priority"] = *before
	}
	if err = sourceEvent(ctx, tx, p.SourceID, "field_policy_set", ev, after, reason, actor); err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}

// DeleteFieldPolicy removes one policy row (the tier default applies again)
// and audits it. pgx.ErrNoRows when there is no such row.
func (s *Store) DeleteFieldPolicy(ctx context.Context, entityType, fieldName string, sourceID int64, reason, actor string) error {
	field, err := normalizePolicyField(entityType, fieldName)
	if err != nil {
		return err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var before int16
	if err = tx.QueryRow(ctx, `DELETE FROM source_field_policies WHERE entity_type=$1 AND field_name=$2 AND source_id=$3 RETURNING priority`, entityType, field, sourceID).Scan(&before); err != nil {
		return err
	}
	if err = sourceEvent(ctx, tx, sourceID, "field_policy_clear", map[string]any{"entity_type": entityType, "field_name": field, "priority": before},
		map[string]any{"entity_type": entityType, "field_name": field}, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// EffectiveFieldOrder returns every source in the order it would win field
// of entityType: policy rows first by their priority, others by tier.
func (s *Store) EffectiveFieldOrder(ctx context.Context, entityType, fieldName string) ([]EffectiveSource, error) {
	field, err := normalizePolicyField(entityType, fieldName)
	if err != nil {
		return nil, err
	}
	pol, err := loadPolicies(ctx, s, entityType)
	if err != nil {
		return nil, err
	}
	rows, err := s.Query(ctx, `SELECT id,name,source_kind,priority FROM sources WHERE adapter<>'rss' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cands []fieldCandidate
	names := map[int64]string{}
	for rows.Next() {
		var c fieldCandidate
		var rank int16
		var name string
		if err = rows.Scan(&c.SourceID, &name, &c.SourceKind, &rank); err != nil {
			return nil, err
		}
		c.SourceRank, c.Value, c.Policy = int(rank), json.RawMessage(`"x"`), pol.lookup(entityType, field, c.SourceID)
		names[c.SourceID] = name
		cands = append(cands, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	out := []EffectiveSource{}
	for _, c := range rankCandidates(cands) {
		out = append(out, EffectiveSource{SourceID: c.SourceID, SourceName: names[c.SourceID], SourceKind: c.SourceKind, Priority: c.Priority, FromPolicy: c.Policy != nil})
	}
	return out, nil
}
