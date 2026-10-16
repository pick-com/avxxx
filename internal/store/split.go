package store

// Splitting an entity that was never merged (FINAL_DATA_DESIGN 5.5): an
// admin picks some of a performer's or release's source evidence, and a new
// entity is created that owns it and everything derived only from it
// (identity.BuildSplitPlan). Source records are re-pointed, never deleted;
// the original keeps its slug, its overrides and its user data unless the
// admin explicitly moves specific rows. The split is written to an
// immutable review_events row and remembered in not_duplicate_pairs, so
// candidate generation never proposes the pair again and ingest keeps
// attaching the moved records to the new entity. UndoSplit re-merges the
// new entity into the original through the merge path.

import (
	"avxxx/internal/catalog"
	"avxxx/internal/identity"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SplitRequest is an admin's split selection.
type SplitRequest struct {
	EntityType      string
	EntityID        int64
	SourceRecordIDs []int64
	// MediaLinks (releases) are release_media_sources.source values to move.
	MediaLinks []string
	// MoveUserData are user-data rows to move: table -> key values (user
	// ids, or row ids for ratings / reviews / playback_sources).
	MoveUserData map[string][]int64
	// Version is the entity version the admin saw; required by SplitEntity,
	// checked by SplitPreview only when non-zero.
	Version       int64
	Actor, Reason string
}

// SplitSourceRecord is one piece of selectable evidence, for display.
type SplitSourceRecord struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	SourceID   int64     `json:"source_id"`
	SourceName string    `json:"source_name"`
	SourceKey  string    `json:"source_record_key"`
	URL        string    `json:"source_url"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// Name is the record's performer name or release title.
	Name string `json:"name,omitempty"`
	// ReleaseID and CreditedName describe a credit record (performers).
	ReleaseID    int64  `json:"release_id,omitempty"`
	CreditedName string `json:"credited_name,omitempty"`
}

// SplitPreview is what a split would do; Plan is nil without a selection.
type SplitPreview struct {
	EntityType string              `json:"entity_type"`
	EntityID   int64               `json:"entity_id"`
	Slug       string              `json:"slug"`
	Version    int64               `json:"version"`
	Records    []SplitSourceRecord `json:"source_records"`
	Plan       *identity.SplitPlan `json:"plan,omitempty"`
}

// SplitResult describes a completed split.
type SplitResult struct {
	ReviewEventID int64              `json:"review_event_id"`
	EntityType    string             `json:"entity_type"`
	OriginalID    int64              `json:"original_id"`
	OriginalSlug  string             `json:"original_slug"`
	NewID         int64              `json:"new_id"`
	NewSlug       string             `json:"new_slug"`
	Plan          identity.SplitPlan `json:"plan"`
}

// UndoSplitResult describes a split reverted by re-merging.
type UndoSplitResult struct {
	ReviewEventID   int64       `json:"review_event_id"`
	RevertedEventID int64       `json:"reverted_event_id"`
	EntityType      string      `json:"entity_type"`
	OriginalID      int64       `json:"original_id"`
	MergedID        int64       `json:"merged_id"`
	Merge           MergeResult `json:"merge"`
}

// splitTarget is the loaded state of one split.
type splitTarget struct {
	table   string
	ent     entityRow
	version int64
	records []SplitSourceRecord
	refs    map[int64][]performerRef // performer evidence refs per record
	releasy map[int64]releaseSlugInput
	in      identity.SplitInput
}

type releaseSlugInput struct{ code, source, key string }

func splitError(err error) error {
	var se *identity.SplitError
	if errors.As(err, &se) {
		return invalid("%s", se.Msg)
	}
	return err
}

// loadSplit locks the entity, checks it may be split and builds the plan
// input. The plan itself is built by the caller.
func loadSplit(ctx context.Context, tx pgx.Tx, req SplitRequest, needVersion bool) (*splitTarget, error) {
	table, _, ok := identity.Tables(req.EntityType)
	if !ok {
		return nil, invalid("entity type must be performer or release")
	}
	if len(req.SourceRecordIDs) > 500 {
		return nil, invalid("too many source records")
	}
	ents, err := lockEntities(ctx, tx, table, req.EntityID)
	if err != nil {
		return nil, err
	}
	t := &splitTarget{table: table, ent: ents[req.EntityID], refs: map[int64][]performerRef{}, releasy: map[int64]releaseSlugInput{}}
	if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT version FROM %s WHERE id=$1`, table), req.EntityID).Scan(&t.version); err != nil {
		return nil, err
	}
	switch {
	case t.ent.MergedTo != nil || t.ent.Status == "merged":
		return nil, conflict("%s %d is merged; undo the merge instead", req.EntityType, req.EntityID)
	case req.EntityType == identity.ReleaseType && t.ent.Status == "held_review":
		return nil, conflict("release %d is held for review", req.EntityID)
	case req.EntityType == identity.PerformerType && t.ent.Status == "hidden":
		return nil, conflict("performer %d is hidden", req.EntityID)
	case needVersion && req.Version <= 0:
		return nil, invalid("version is required")
	case req.Version > 0 && req.Version != t.version:
		return nil, conflict("%s %d is at version %d, not %d", req.EntityType, req.EntityID, t.version, req.Version)
	}
	in := identity.SplitInput{EntityType: req.EntityType, OriginalID: req.EntityID, Original: t.ent.Row, Selected: req.SourceRecordIDs,
		Relations: map[string][]json.RawMessage{}, MediaLinks: req.MediaLinks, UserMoves: req.MoveUserData, UserRows: map[string][]json.RawMessage{}}
	if err = t.loadRecords(ctx, tx, req); err != nil {
		return nil, err
	}
	for _, r := range t.records {
		rec := identity.SplitRecord{ID: r.ID, SourceID: r.SourceID, Kind: r.Kind, CreditedName: r.CreditedName}
		if req.EntityType == identity.PerformerType {
			rec.Keys = performerEvidenceKeys(t.refs[r.ID])
		} else if x := t.releasy[r.ID]; x.code != "" {
			rec.Keys = []string{identity.IdentifierKey("dvd_code", catalog.NormalizeIdentifier(x.code))}
		}
		in.Records = append(in.Records, rec)
	}
	if req.EntityType == identity.ReleaseType {
		if err = t.loadContentIDKeys(ctx, tx, &in); err != nil {
			return nil, err
		}
	}
	specs, _ := identity.SplitTables(req.EntityType)
	for _, spec := range specs {
		if in.Relations[spec.Table], err = relationRows(ctx, tx, identity.TableSpec{Table: spec.Table, FK: spec.FK}, req.EntityID); err != nil {
			return nil, err
		}
	}
	allowed := identity.SplitUserTables(req.EntityType)
	for name, ids := range req.MoveUserData {
		spec, ok := allowed[name]
		if !ok {
			continue // BuildSplitPlan reports it
		}
		r, err := tx.Query(ctx, fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE %s=$1 AND %s=ANY($2)`, spec.Table, spec.FK, spec.Key[0]), req.EntityID, ids)
		if err != nil {
			return nil, err
		}
		rows, err := pgx.CollectRows(r, pgx.RowTo[json.RawMessage])
		if err != nil {
			return nil, err
		}
		in.UserRows[name] = rows
	}
	if in.Overridden, err = overriddenFields(ctx, tx, req.EntityType, req.EntityID); err != nil {
		return nil, err
	}
	if req.EntityType == identity.PerformerType {
		in.LockedReleases = map[int64]bool{}
		r, err := tx.Query(ctx, `SELECT o.release_id FROM release_overrides o WHERE o.field_name=$2
			AND o.release_id IN (SELECT release_id FROM release_performers WHERE performer_id=$1)`, req.EntityID, OverridePerformers)
		if err != nil {
			return nil, err
		}
		ids, err := pgx.CollectRows(r, pgx.RowTo[int64])
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			in.LockedReleases[id] = true
		}
	}
	if err = t.loadFieldPolicy(ctx, tx, &in); err != nil {
		return nil, err
	}
	t.in = in
	return t, nil
}

// loadRecords reads the entity's owned source records and, for performers,
// the release records crediting it.
func (t *splitTarget) loadRecords(ctx context.Context, tx pgx.Tx, req SplitRequest) error {
	fk := "performer_id"
	if req.EntityType == identity.ReleaseType {
		fk = "release_id"
	}
	scan := func(q string, kind string) error {
		rows, err := tx.Query(ctx, q, req.EntityID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r := SplitSourceRecord{Kind: kind}
			var raw []byte
			if err = rows.Scan(&r.ID, &r.SourceID, &r.SourceName, &r.SourceKey, &r.URL, &r.LastSeenAt, &raw, &r.ReleaseID, &r.CreditedName); err != nil {
				return err
			}
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			switch {
			case kind == identity.RecordCredit:
				r.Name = r.CreditedName
				for _, ref := range performerRefs(fields) {
					if ref.Name == r.CreditedName {
						t.refs[r.ID] = append(t.refs[r.ID], ref)
					}
				}
				if len(t.refs[r.ID]) == 0 {
					t.refs[r.ID] = []performerRef{{Name: r.CreditedName}}
				}
			case req.EntityType == identity.PerformerType:
				r.Name = recordText(fields["name"])
				ref := recordPerformerRef(fields, r.Name)
				t.refs[r.ID] = append(t.refs[r.ID], ref)
				if ja := recordText(fields["name_ja"]); ja != "" {
					t.refs[r.ID] = append(t.refs[r.ID], performerRef{Name: ja})
				}
			default:
				r.Name = recordText(fields["title"])
				t.releasy[r.ID] = releaseSlugInput{code: recordText(fields["dvd_code"]), source: r.SourceName, key: r.SourceKey}
			}
			t.records = append(t.records, r)
		}
		return rows.Err()
	}
	if err := scan(fmt.Sprintf(`SELECT sr.id,sr.source_id,s.name,sr.source_record_key,sr.source_url,sr.last_seen_at,sr.raw_data,0::bigint,''
		FROM source_records sr JOIN sources s ON s.id=sr.source_id WHERE sr.entity_type=%s AND sr.%s=$1 ORDER BY sr.id`,
		quoteLiteral(req.EntityType), fk), identity.RecordOwned); err != nil {
		return err
	}
	if req.EntityType != identity.PerformerType {
		return nil
	}
	return scan(`SELECT DISTINCT ON (sr.id) sr.id,sr.source_id,s.name,sr.source_record_key,sr.source_url,sr.last_seen_at,sr.raw_data,rps.release_id,COALESCE(rps.credited_name,'')
		FROM release_performer_sources rps JOIN source_records sr ON sr.id=rps.source_record_id JOIN sources s ON s.id=sr.source_id
		WHERE rps.performer_id=$1 ORDER BY sr.id,rps.release_id`, identity.RecordCredit)
}

func quoteLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// loadContentIDKeys adds the content_id evidence keys of release records.
func (t *splitTarget) loadContentIDKeys(ctx context.Context, tx pgx.Tx, in *identity.SplitInput) error {
	rows, err := tx.Query(ctx, `SELECT id,COALESCE(raw_data->>'content_id','') FROM source_records WHERE release_id=$1 AND entity_type='release'`, in.OriginalID)
	if err != nil {
		return err
	}
	defer rows.Close()
	cid := map[int64]string{}
	for rows.Next() {
		var id int64
		var v string
		if err = rows.Scan(&id, &v); err != nil {
			return err
		}
		cid[id] = v
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i, r := range in.Records {
		if v := strings.TrimSpace(cid[r.ID]); v != "" {
			in.Records[i].Keys = append(in.Records[i].Keys, identity.IdentifierKey("content_id", catalog.NormalizeIdentifier(v)))
		}
	}
	return nil
}

// performerEvidenceKeys are the alias and external-id keys a performer
// record or credit is evidence for (the same forms linkPerformerIdentity
// writes).
func performerEvidenceKeys(refs []performerRef) []string {
	seen := map[string]bool{}
	var out []string
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, ref := range refs {
		for _, n := range append([]string{ref.Name}, ref.Readings...) {
			if f := identity.AliasForm(n); f != "" {
				add(identity.AliasKey(f))
			}
			if catalog.HasKana(n) {
				if r, ok := catalog.Romaji(n); ok {
					add(identity.AliasKey(identity.AliasForm(r)))
				}
			}
		}
		if catalog.DMMActressSlug(ref.DMMID) != "" {
			add(identity.ExternalIDKey(externalDMMActress, ref.DMMID))
		}
		if ref.TPDBID != "" {
			add(identity.ExternalIDKey(externalThePornDB, ref.TPDBID))
		}
	}
	return out
}

type evidenceMeta struct {
	sourceID int64
	kind     string
	rank     int
}

// loadFieldPolicy loads the current field evidence of the owned records and
// wires the field priority policy (fieldpolicy.go) into the plan input.
func (t *splitTarget) loadFieldPolicy(ctx context.Context, tx pgx.Tx, in *identity.SplitInput) error {
	typ := in.EntityType
	fields := releaseFields
	if typ == identity.PerformerType {
		fields = performerFields
	}
	for _, spec := range fields {
		in.Columns = append(in.Columns, identity.ColumnSpec{Name: spec.Column, Required: spec.Required})
	}
	var owned []int64
	for _, r := range t.records {
		if r.Kind == identity.RecordOwned {
			owned = append(owned, r.ID)
		}
	}
	rows, err := tx.Query(ctx, `SELECT sf.field_name,sf.normalized_value,sf.source_record_id,sr.source_id,s.source_kind,s.priority
		FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id JOIN sources s ON s.id=sr.source_id
		WHERE sf.is_current AND sf.source_record_id=ANY($1) ORDER BY sf.source_record_id,sf.field_name`, owned)
	if err != nil {
		return err
	}
	meta := map[int64]evidenceMeta{}
	for rows.Next() {
		var e identity.FieldEvidence
		var raw []byte
		var m evidenceMeta
		var rank int16
		if err = rows.Scan(&e.Field, &raw, &e.SourceRecordID, &m.sourceID, &m.kind, &rank); err != nil {
			rows.Close()
			return err
		}
		spec, ok := ConflictField(typ, e.Field)
		if !ok || raw == nil {
			continue
		}
		m.rank = int(rank)
		meta[e.SourceRecordID] = m
		e.Column, e.Value = spec.Column, json.RawMessage(raw)
		in.Evidence = append(in.Evidence, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	pol, err := loadPolicies(ctx, tx, typ)
	if err != nil {
		return err
	}
	in.Policy = func(column string, ev []identity.FieldEvidence) identity.FieldOutcome {
		cands := make([]fieldCandidate, 0, len(ev))
		for _, e := range ev {
			m := meta[e.SourceRecordID]
			cands = append(cands, fieldCandidate{SourceRecordID: e.SourceRecordID, SourceID: m.sourceID, SourceKind: m.kind,
				SourceRank: m.rank, Policy: pol.lookup(typ, e.Field, m.sourceID), Value: e.Value})
		}
		d := decideField(typ, column, cands)
		if !d.ok {
			return identity.FieldOutcome{}
		}
		spec, _ := ConflictField(typ, column)
		v, err := CoerceFieldValue(spec, d.Winner.Value)
		if err != nil || v == nil {
			return identity.FieldOutcome{}
		}
		raw, _ := json.Marshal(*v)
		return identity.FieldOutcome{Value: raw, SourceRecordID: d.Winner.SourceRecordID, OK: true, Conflict: d.Conflict}
	}
	return nil
}

// newSlugCandidates returns the slugs to try for the new entity, best first
// (FINAL_DATA_DESIGN 3.1 rules); the caller falls back to "<type>-<id>".
func (t *splitTarget) newSlugCandidates(plan identity.SplitPlan) []string {
	moved := map[int64]bool{}
	for _, id := range plan.MovedRecords {
		moved[id] = true
	}
	if plan.EntityType == identity.PerformerType {
		var name string
		if f, ok := plan.Field(false, "canonical_name"); ok {
			_ = json.Unmarshal(f.Value, &name)
		}
		return performerSplitSlugCandidates(name, t.movedRefs(moved))
	}
	var title string
	if f, ok := plan.Field(false, "canonical_title"); ok {
		_ = json.Unmarshal(f.Value, &title)
	}
	var recs []releaseSlugInput
	for _, r := range t.records {
		if moved[r.ID] {
			recs = append(recs, t.releasy[r.ID])
		}
	}
	return releaseSplitSlugCandidates(title, recs)
}

func (t *splitTarget) movedRefs(moved map[int64]bool) []performerRef {
	var out []performerRef
	for _, r := range t.records { // owned records first, then credits
		if moved[r.ID] {
			out = append(out, t.refs[r.ID]...)
		}
	}
	return out
}

// performerSplitSlugCandidates: the new performer's name, then every reading
// and DMM actress id of the moved evidence, as for a newly created performer.
func performerSplitSlugCandidates(name string, refs []performerRef) []string {
	in := catalog.PerformerSlugInput{Name: name}
	for _, r := range refs {
		if r.Name != name && r.Name != "" {
			in.Readings = append(in.Readings, r.Name)
		}
		in.Readings = append(in.Readings, r.Readings...)
		if in.SourceScoped == "" {
			in.SourceScoped = catalog.DMMActressSlug(r.DMMID)
		}
	}
	return catalog.PerformerSlugCandidates(in)
}

// releaseSplitSlugCandidates: each moved record's code slug, then its
// title-source-key slug (the code slug is usually the original's).
func releaseSplitSlugCandidates(title string, recs []releaseSlugInput) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if strings.TrimSpace(s) != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, r := range recs {
		add(catalog.ReleaseSlug(r.code, title, r.source, r.key))
	}
	for _, r := range recs {
		add(catalog.ReleaseSlug("", title, r.source, r.key))
	}
	return out
}

func fallbackSlug(entityType string, id int64) string {
	if entityType == identity.PerformerType {
		return catalog.FallbackPerformerSlug(id)
	}
	return "release-" + strconv.FormatInt(id, 10)
}

// slugTaken: a live or merged entity, or a redirect, owns slug.
func slugTaken(ctx context.Context, q dbtx, entityType, table, slug string) (bool, error) {
	var taken bool
	err := q.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE slug=$1)
		OR EXISTS(SELECT 1 FROM slug_redirects WHERE entity_type=$2 AND old_slug=$1)`, table), slug, entityType).Scan(&taken)
	return taken, err
}

// pickSlug returns the first free candidate, or "" when the id fallback is
// needed.
func (t *splitTarget) pickSlug(ctx context.Context, tx pgx.Tx, plan identity.SplitPlan) (string, error) {
	for _, c := range t.newSlugCandidates(plan) {
		taken, err := slugTaken(ctx, tx, plan.EntityType, t.table, c)
		if err != nil {
			return "", err
		}
		if !taken {
			return c, nil
		}
	}
	return "", nil
}

// SplitPreview shows what a split of req would do without changing anything.
// Without SourceRecordIDs it only lists the selectable evidence.
func (s *Store) SplitPreview(ctx context.Context, req SplitRequest) (SplitPreview, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return SplitPreview{}, err
	}
	defer tx.Rollback(ctx)
	t, err := loadSplit(ctx, tx, req, false)
	if err != nil {
		return SplitPreview{}, err
	}
	out := SplitPreview{EntityType: req.EntityType, EntityID: req.EntityID, Slug: t.ent.Slug, Version: t.version, Records: t.records}
	if out.Records == nil {
		out.Records = []SplitSourceRecord{}
	}
	if len(req.SourceRecordIDs) == 0 {
		return out, nil
	}
	plan, err := identity.BuildSplitPlan(t.in)
	if err != nil {
		return SplitPreview{}, splitError(err)
	}
	if plan.NewSlug, err = t.pickSlug(ctx, tx, plan); err != nil {
		return SplitPreview{}, err
	}
	if plan.NewSlug == "" {
		plan.NewSlug = req.EntityType + "-{new_id}" // the id fallback, known once created
	}
	out.Plan = &plan
	return out, nil
}

// SplitEntity applies a split in one transaction: it creates the new entity
// under a fresh slug, moves the selected evidence and what derives only from
// it, writes both sides' recomputed canonical fields, refreshes field
// conflicts, records the review event and the not-duplicate pair.
func (s *Store) SplitEntity(ctx context.Context, req SplitRequest) (SplitResult, error) {
	if len(req.Reason) > MaxReasonLen {
		return SplitResult{}, invalid("reason too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return SplitResult{}, err
	}
	defer tx.Rollback(ctx)
	t, err := loadSplit(ctx, tx, req, true)
	if err != nil {
		return SplitResult{}, err
	}
	plan, err := identity.BuildSplitPlan(t.in)
	if err != nil {
		return SplitResult{}, splitError(err)
	}
	if plan.NewSlug, err = t.pickSlug(ctx, tx, plan); err != nil {
		return SplitResult{}, err
	}
	typ, orig := req.EntityType, req.EntityID
	var explicitID *int64
	if plan.NewSlug == "" {
		var id int64
		if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT nextval(pg_get_serial_sequence('%s','id'))`, t.table)).Scan(&id); err != nil {
			return SplitResult{}, err
		}
		explicitID, plan.NewSlug = &id, fallbackSlug(typ, id)
	}
	if _, err = catalog.RequireSlug(plan.NewSlug); err != nil {
		return SplitResult{}, err
	}
	nameCol := "canonical_name"
	if typ == identity.ReleaseType {
		nameCol = "canonical_title"
	}
	var name string
	if f, ok := plan.Field(false, nameCol); ok {
		_ = json.Unmarshal(f.Value, &name)
	}
	if strings.TrimSpace(name) == "" {
		var row map[string]any
		_ = json.Unmarshal(t.ent.Row, &row)
		name = fmt.Sprint(row[nameCol])
	}
	if err = tx.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %[1]s(id,slug,%[2]s,status) VALUES(COALESCE($1::bigint,nextval(pg_get_serial_sequence('%[1]s','id'))),$2,$3,$4) RETURNING id`, t.table, nameCol),
		explicitID, plan.NewSlug, name, t.ent.Status).Scan(&plan.NewID); err != nil {
		return SplitResult{}, err
	}
	for _, tp := range plan.Tables {
		key := keyExpr(tp.Key)
		for _, k := range tp.Move {
			if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE %s=$2 AND %s=$3::jsonb`, tp.Table, tp.FK, tp.FK, key), plan.NewID, orig, string(k)); err != nil {
				return SplitResult{}, err
			}
		}
		for _, row := range tp.Copy {
			if _, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %[1]s SELECT * FROM jsonb_populate_record(NULL::%[1]s,$1::jsonb || jsonb_build_object($2::text,$3::bigint)) ON CONFLICT DO NOTHING`, tp.Table),
				string(row), tp.FK, plan.NewID); err != nil {
				return SplitResult{}, err
			}
		}
	}
	if err = writeSplitFields(ctx, tx, typ, t.table, orig, plan.Original); err != nil {
		return SplitResult{}, err
	}
	if err = writeSplitFields(ctx, tx, typ, t.table, plan.NewID, plan.New); err != nil {
		return SplitResult{}, err
	}
	note := fmt.Sprintf("superseded by the split of %s %d into %d", typ, orig, plan.NewID)
	if err = refreshFieldConflicts(ctx, tx, typ, orig, note); err != nil {
		return SplitResult{}, err
	}
	if err = refreshFieldConflicts(ctx, tx, typ, plan.NewID, note); err != nil {
		return SplitResult{}, err
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET version=version+1,updated_at=now() WHERE id=$1`, t.table), orig); err != nil {
		return SplitResult{}, err
	}
	var newRow json.RawMessage
	if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE id=$1`, t.table), plan.NewID).Scan(&newRow); err != nil {
		return SplitResult{}, err
	}
	pair := identity.CanonicalPair(orig, plan.NewID)
	before, err := json.Marshal(map[string]any{"original": t.ent.Row, "relations": t.in.Relations, "user_rows": t.in.UserRows,
		"overridden": t.in.Overridden, "source_records": t.records, "version": t.version})
	if err != nil {
		return SplitResult{}, err
	}
	after, err := json.Marshal(map[string]any{"plan": plan, "new_entity": newRow, "not_duplicate": map[string]int64{"a_id": pair.Lo, "b_id": pair.Hi}})
	if err != nil {
		return SplitResult{}, err
	}
	res := SplitResult{EntityType: typ, OriginalID: orig, OriginalSlug: t.ent.Slug, NewID: plan.NewID, NewSlug: plan.NewSlug, Plan: plan}
	if err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor) VALUES($1,$2,'split',$3::jsonb,$4::jsonb,NULLIF($5,''),$6) RETURNING id`,
		typ, orig, string(before), string(after), req.Reason, req.Actor).Scan(&res.ReviewEventID); err != nil {
		return SplitResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO not_duplicate_pairs(entity_type,a_id,b_id,review_event_id) VALUES($1,$2,$3,$4)
		ON CONFLICT(entity_type,a_id,b_id) DO UPDATE SET review_event_id=EXCLUDED.review_event_id`, typ, pair.Lo, pair.Hi, res.ReviewEventID); err != nil {
		return SplitResult{}, err
	}
	for _, id := range []int64{orig, plan.NewID} {
		if err = markStale(ctx, tx, typ, strconv.FormatInt(id, 10)); err != nil {
			return SplitResult{}, err
		}
	}
	return res, tx.Commit(ctx)
}

// writeSplitFields writes the plan's recomputed canonical columns of one
// side. Override, kept and empty results are not written.
func writeSplitFields(ctx context.Context, tx pgx.Tx, entityType, table string, id int64, fields []identity.FieldResult) error {
	for _, f := range fields {
		if !f.Write() {
			continue
		}
		spec, ok := ConflictField(entityType, f.Column)
		if !ok {
			continue
		}
		var v *string
		if err := json.Unmarshal(f.Value, &v); err != nil {
			return fmt.Errorf("split %s: %w", f.Column, err)
		}
		if v == nil && spec.Required {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s=$2::%[3]s WHERE id=$1 AND %[2]s IS DISTINCT FROM $2::%[3]s`,
			table, spec.Column, spec.Kind.sqlType()), id, v); err != nil {
			return err
		}
	}
	return nil
}

// refreshFieldConflicts supersedes an entity's pending field conflicts and
// reopens the ones its current evidence still has (applyFieldPolicy).
func refreshFieldConflicts(ctx context.Context, tx pgx.Tx, entityType string, id int64, note string) error {
	if _, err := tx.Exec(ctx, `UPDATE field_conflicts SET status='superseded',resolution_note=$3,updated_at=now()
		WHERE entity_type=$1 AND entity_id=$2 AND status='pending'`, entityType, id, note); err != nil {
		return err
	}
	fk := "performer_id"
	if entityType == identity.ReleaseType {
		fk = "release_id"
	}
	r, err := tx.Query(ctx, fmt.Sprintf(`SELECT DISTINCT sf.field_name FROM source_field_values sf JOIN source_records sr ON sr.id=sf.source_record_id
		WHERE sf.is_current AND sr.entity_type=$1 AND sr.%s=$2 AND sf.field_name<>$3 ORDER BY 1`, fk), entityType, id, catalog.FieldPageTitle)
	if err != nil {
		return err
	}
	fields, err := pgx.CollectRows(r, pgx.RowTo[string])
	if err != nil {
		return err
	}
	overridden, err := overriddenFields(ctx, tx, entityType, id)
	if err != nil {
		return err
	}
	return applyFieldPolicy(ctx, tx, entityType, id, fields, overridden)
}

// UndoSplit reverts a split by merging the new entity back into the original
// through the merge path: the merge's review event (action "undo_split")
// reverts the split event, the not-duplicate pair is removed, the new
// entity's slug redirects to the original, and the original's canonical
// fields are recomputed from all its evidence again. 409 when the split was
// already undone or either side has since been merged.
func (s *Store) UndoSplit(ctx context.Context, eventID int64, actor, reason string) (UndoSplitResult, error) {
	if len(reason) > MaxReasonLen {
		return UndoSplitResult{}, invalid("reason too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return UndoSplitResult{}, err
	}
	defer tx.Rollback(ctx)
	var typ, action string
	var afterRaw []byte
	if err = tx.QueryRow(ctx, `SELECT entity_type,action,after_value FROM review_events WHERE id=$1`, eventID).Scan(&typ, &action, &afterRaw); err != nil {
		return UndoSplitResult{}, err
	}
	if action != "split" {
		return UndoSplitResult{}, conflict("review event %d is a %s, not a split", eventID, action)
	}
	// Serialize concurrent undos of one split; the unique reverts index is
	// the final guard.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1,$2)`, int32(identityLock), int32(eventID)); err != nil {
		return UndoSplitResult{}, err
	}
	var undone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_events WHERE reverts_event_id=$1)`, eventID).Scan(&undone); err != nil {
		return UndoSplitResult{}, err
	}
	if undone {
		return UndoSplitResult{}, conflict("split %d was already undone", eventID)
	}
	var after struct {
		Plan identity.SplitPlan `json:"plan"`
	}
	if err = json.Unmarshal(afterRaw, &after); err != nil {
		return UndoSplitResult{}, err
	}
	plan := after.Plan
	table, _, ok := identity.Tables(typ)
	if !ok || plan.EntityType != typ || plan.NewID <= 0 || plan.OriginalID <= 0 {
		return UndoSplitResult{}, fmt.Errorf("review event %d has no usable split plan", eventID)
	}
	ents, err := lockEntities(ctx, tx, table, plan.OriginalID, plan.NewID)
	if err != nil {
		return UndoSplitResult{}, err
	}
	for _, id := range []int64{plan.OriginalID, plan.NewID} {
		if e := ents[id]; e.MergedTo != nil {
			return UndoSplitResult{}, conflict("%s %d was merged into %d after the split; undo that merge first", typ, id, *e.MergedTo)
		}
	}
	pair := identity.CanonicalPair(plan.OriginalID, plan.NewID)
	if _, err = tx.Exec(ctx, `DELETE FROM not_duplicate_pairs WHERE entity_type=$1 AND a_id=$2 AND b_id=$3`, typ, pair.Lo, pair.Hi); err != nil {
		return UndoSplitResult{}, err
	}
	mr, err := mergeEntities(ctx, tx, mergeOp{EntityType: typ, TargetID: plan.OriginalID, SourceID: plan.NewID, Action: "undo_split",
		Actor: actor, Reason: reason, RevertsEventID: &eventID})
	if err != nil {
		return UndoSplitResult{}, err
	}
	if typ == identity.PerformerType {
		// Not a merge table (readers follow merged_into_id), but the ids
		// belong to the original again.
		if _, err = tx.Exec(ctx, `UPDATE performer_external_ids SET performer_id=$1 WHERE performer_id=$2`, plan.OriginalID, plan.NewID); err != nil {
			return UndoSplitResult{}, err
		}
	}
	note := fmt.Sprintf("superseded by undoing split %d", eventID)
	if err = refreshFieldConflicts(ctx, tx, typ, plan.OriginalID, note); err != nil {
		return UndoSplitResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE field_conflicts SET status='superseded',resolution_note=$3,updated_at=now()
		WHERE entity_type=$1 AND entity_id=$2 AND status='pending'`, typ, plan.NewID, note); err != nil {
		return UndoSplitResult{}, err
	}
	if err = markStale(ctx, tx, typ, strconv.FormatInt(plan.OriginalID, 10)); err != nil {
		return UndoSplitResult{}, err
	}
	res := UndoSplitResult{ReviewEventID: mr.ReviewEventID, RevertedEventID: eventID, EntityType: typ, OriginalID: plan.OriginalID, MergedID: plan.NewID, Merge: mr}
	return res, tx.Commit(ctx)
}

// ---- Ingest: keep split evidence where the split put it.

// notDuplicate reports whether a and b are a recorded not-duplicate pair.
func notDuplicate(ctx context.Context, q dbtx, entityType string, a, b int64) (bool, error) {
	if a == b || a <= 0 || b <= 0 {
		return false, nil
	}
	p := identity.CanonicalPair(a, b)
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM not_duplicate_pairs WHERE entity_type=$1 AND a_id=$2 AND b_id=$3)`, entityType, p.Lo, p.Hi).Scan(&ok)
	return ok, err
}

// splitSide reports whether id is one side of a split (a not-duplicate pair
// recorded by a split review event).
func splitSide(ctx context.Context, q dbtx, entityType string, id int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM not_duplicate_pairs n JOIN review_events e ON e.id=n.review_event_id
		WHERE n.entity_type=$1 AND (n.a_id=$2 OR n.b_id=$2) AND e.action='split')`, entityType, id).Scan(&ok)
	return ok, err
}

// releaseRecordSlug is the slug a release record upserts under. Normally its
// code / title slug; but when the record already belongs to a release that
// was split from the release owning that slug (a not-duplicate pair), it
// keeps updating its own release.
func releaseRecordSlug(ctx context.Context, tx pgx.Tx, sourceID int64, key, slug string) (string, error) {
	var prev int64
	err := tx.QueryRow(ctx, `SELECT release_id FROM source_records WHERE source_id=$1 AND entity_type='release' AND source_record_key=$2 AND release_id IS NOT NULL`,
		sourceID, key).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return slug, nil
	}
	if err != nil {
		return "", err
	}
	var owner int64
	err = tx.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, slug).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return slug, nil
	}
	if err != nil {
		return "", err
	}
	if prev, err = survivingID(ctx, tx, "releases", prev); err != nil {
		return "", err
	}
	if owner, err = survivingID(ctx, tx, "releases", owner); err != nil {
		return "", err
	}
	nd, err := notDuplicate(ctx, tx, identity.ReleaseType, owner, prev)
	if err != nil || !nd {
		return slug, err
	}
	var own string
	err = tx.QueryRow(ctx, `SELECT slug FROM releases WHERE id=$1`, prev).Scan(&own)
	return own, err
}

// previousCredit is the (surviving) performer this release record credited
// under name before, or 0.
func previousCredit(ctx context.Context, tx pgx.Tx, sourceRecordID int64, name string) (int64, error) {
	var prev int64
	err := tx.QueryRow(ctx, `SELECT performer_id FROM release_performer_sources WHERE source_record_id=$1 AND credited_name=$2 ORDER BY performer_id LIMIT 1`,
		sourceRecordID, name).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return survivingID(ctx, tx, "performers", prev)
}

// stickyCredit reports whether a credit whose record credited prev before
// keeps prev without resolving the name again: a performer involved in a
// split keeps its credits, because name matching cannot tell the two people
// apart (it would find both, or create a third).
func stickyCredit(ctx context.Context, tx pgx.Tx, prev int64) (bool, error) {
	if prev == 0 {
		return false, nil
	}
	return splitSide(ctx, tx, identity.PerformerType, prev)
}

// keepPrevious reports whether candidate must be replaced by prev because the
// two are a not-duplicate pair.
func keepPrevious(ctx context.Context, q dbtx, entityType string, candidate, prev int64) (bool, error) {
	if prev == 0 || candidate == prev {
		return false, nil
	}
	return notDuplicate(ctx, q, entityType, candidate, prev)
}
