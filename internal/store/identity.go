package store

import (
	"avxxx/internal/identity"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ConflictError reports a review decision that no longer applies to the
// current state (candidate already decided, entity already merged, merge
// already undone). Its message is safe to show to clients; the API maps it
// to 409.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

func conflict(format string, a ...any) error { return &ConflictError{Msg: fmt.Sprintf(format, a...)} }

// ErrInvalidMergeTarget is returned when the requested surviving entity is not
// one side of the candidate pair. The API maps it to 400.
var ErrInvalidMergeTarget = errors.New("target_id must be the candidate's left_id or right_id")

// identityLock serializes candidate generation runs.
const identityLock = 842012

// GenerateResult holds per-entity-type generation statistics.
type GenerateResult map[string]identity.Stats

// GenerateIdentityCandidates scores every non-merged performer and/or release
// pair that shares a blocking key and stores new candidates. It is idempotent:
// pairs already in identity_candidates (either order, any status) or in
// not_duplicate_pairs are skipped. entityType "" runs both types.
func (s *Store) GenerateIdentityCandidates(ctx context.Context, entityType string) (GenerateResult, error) {
	types := []string{identity.PerformerType, identity.ReleaseType}
	if entityType != "" {
		if _, _, ok := identity.Tables(entityType); !ok {
			return nil, fmt.Errorf("unknown entity type %q", entityType)
		}
		types = []string{entityType}
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", identityLock); err != nil {
		return nil, err
	}
	out := GenerateResult{}
	for _, typ := range types {
		known, err := knownPairs(ctx, tx, typ)
		if err != nil {
			return nil, err
		}
		var cands []identity.Candidate
		var st identity.Stats
		if typ == identity.PerformerType {
			ps, err := performerFeatures(ctx, tx)
			if err != nil {
				return nil, err
			}
			cands, st = identity.PerformerCandidates(ps, known)
		} else {
			rs, err := releaseFeatures(ctx, tx)
			if err != nil {
				return nil, err
			}
			cands, st = identity.ReleaseCandidates(rs, known)
		}
		for _, c := range cands {
			ev, err := json.Marshal(c.Evidence)
			if err != nil {
				return nil, err
			}
			tag, err := tx.Exec(ctx, `INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence) VALUES($1,$2,$3,$4,$5::jsonb) ON CONFLICT DO NOTHING`, typ, c.Lo, c.Hi, c.Evidence.Score, string(ev))
			if err != nil {
				return nil, err
			}
			st.Inserted += int(tag.RowsAffected())
		}
		out[typ] = st
	}
	return out, tx.Commit(ctx)
}

func knownPairs(ctx context.Context, tx pgx.Tx, typ string) (identity.Known, error) {
	k := identity.Known{Existing: map[identity.Pair]bool{}, NotDuplicate: map[identity.Pair]bool{}}
	load := func(q string, m map[identity.Pair]bool) error {
		r, err := tx.Query(ctx, q, typ)
		if err != nil {
			return err
		}
		defer r.Close()
		for r.Next() {
			var a, b int64
			if err = r.Scan(&a, &b); err != nil {
				return err
			}
			m[identity.CanonicalPair(a, b)] = true
		}
		return r.Err()
	}
	if err := load(`SELECT left_id,right_id FROM identity_candidates WHERE entity_type=$1`, k.Existing); err != nil {
		return k, err
	}
	return k, load(`SELECT a_id,b_id FROM not_duplicate_pairs WHERE entity_type=$1`, k.NotDuplicate)
}

func performerFeatures(ctx context.Context, tx pgx.Tx) ([]identity.Performer, error) {
	r, err := tx.Query(ctx, `SELECT p.id,p.canonical_name,COALESCE(p.canonical_name_ja,''),COALESCE(p.birth_date::text,''),
		COALESCE((SELECT array_agg(a.name) FROM performer_aliases a WHERE a.performer_id=p.id),'{}'),
		COALESCE((SELECT array_agg(rp.release_id) FROM release_performers rp WHERE rp.performer_id=p.id),'{}')
		FROM performers p WHERE p.merged_into_id IS NULL ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []identity.Performer{}
	for r.Next() {
		var p identity.Performer
		var name, nameJA string
		var aliases []string
		if err = r.Scan(&p.ID, &name, &nameJA, &p.BirthDate, &aliases, &p.ReleaseIDs); err != nil {
			return nil, err
		}
		p.Names = append([]string{name, nameJA}, aliases...)
		out = append(out, p)
	}
	return out, r.Err()
}

func releaseFeatures(ctx context.Context, tx pgx.Tx) ([]identity.Release, error) {
	r, err := tx.Query(ctx, `SELECT r.id,r.canonical_title,COALESCE(r.title_ja,''),COALESCE(r.runtime_minutes,0),
		COALESCE((SELECT array_agg(i.identifier_value) FROM release_identifiers i WHERE i.release_id=r.id),'{}'),
		COALESCE((SELECT array_agg(rp.performer_id) FROM release_performers rp WHERE rp.release_id=r.id),'{}')
		FROM releases r WHERE r.merged_into_id IS NULL ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []identity.Release{}
	for r.Next() {
		var x identity.Release
		var title, titleJA string
		var runtime int16
		if err = r.Scan(&x.ID, &title, &titleJA, &runtime, &x.Codes, &x.PerformerIDs); err != nil {
			return nil, err
		}
		x.RuntimeMinutes = int(runtime)
		x.Titles = []string{title}
		if titleJA != "" {
			x.Titles = append(x.Titles, titleJA)
		}
		out = append(out, x)
	}
	return out, r.Err()
}

// MergeResult describes a completed merge.
type MergeResult struct {
	ReviewEventID int64            `json:"review_event_id"`
	EntityType    string           `json:"entity_type"`
	TargetID      int64            `json:"target_id"`
	SourceID      int64            `json:"source_id"`
	TargetSlug    string           `json:"target_slug"`
	SourceSlug    string           `json:"source_slug"`
	Moved         map[string]int   `json:"moved"`
	Deduplicated  map[string]int   `json:"deduplicated"`
	AddedAliases  []identity.Alias `json:"added_aliases"`
}

// DecisionResult describes a not-duplicate decision.
type DecisionResult struct {
	ReviewEventID int64  `json:"review_event_id"`
	CandidateID   int64  `json:"candidate_id"`
	Status        string `json:"status"`
}

// UndoResult describes a reverted merge.
type UndoResult struct {
	ReviewEventID   int64  `json:"review_event_id"`
	RevertedEventID int64  `json:"reverted_event_id"`
	EntityType      string `json:"entity_type"`
	RestoredID      int64  `json:"restored_id"`
	RestoredSlug    string `json:"restored_slug"`
	TargetID        int64  `json:"target_id"`
}

type candidateRow struct {
	ID          int64
	EntityType  string
	Left, Right int64
	Status      string
}

func lockCandidate(ctx context.Context, tx pgx.Tx, id int64) (candidateRow, error) {
	c := candidateRow{ID: id}
	err := tx.QueryRow(ctx, `SELECT entity_type,left_id,right_id,status FROM identity_candidates WHERE id=$1 FOR UPDATE`, id).Scan(&c.EntityType, &c.Left, &c.Right, &c.Status)
	if err != nil {
		return c, err
	}
	if c.Status != "pending" {
		return c, conflict("candidate %d is already %s", id, c.Status)
	}
	return c, nil
}

type entityRow struct {
	ID       int64
	Slug     string
	Status   string
	MergedTo *int64
	Row      json.RawMessage
}

// lockEntities row-locks ids in id order (deadlock-free) and returns them by id.
func lockEntities(ctx context.Context, tx pgx.Tx, table string, ids ...int64) (map[int64]entityRow, error) {
	r, err := tx.Query(ctx, fmt.Sprintf(`SELECT id,slug,status,merged_into_id,to_jsonb(t) FROM %s t WHERE id=ANY($1) ORDER BY id FOR UPDATE`, table), ids)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := map[int64]entityRow{}
	for r.Next() {
		var e entityRow
		if err = r.Scan(&e.ID, &e.Slug, &e.Status, &e.MergedTo, &e.Row); err != nil {
			return nil, err
		}
		out[e.ID] = e
	}
	if err = r.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := out[id]; !ok {
			return nil, pgx.ErrNoRows
		}
	}
	return out, nil
}

func relationRows(ctx context.Context, tx pgx.Tx, spec identity.TableSpec, id int64) ([]json.RawMessage, error) {
	where := ""
	if spec.Where != "" {
		where = " AND (" + spec.Where + ")"
	}
	r, err := tx.Query(ctx, fmt.Sprintf(`SELECT to_jsonb(t) FROM %s t WHERE %s=$1%s`, spec.Table, spec.FK, where), id)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	out := []json.RawMessage{}
	for r.Next() {
		var row json.RawMessage
		if err = r.Scan(&row); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, r.Err()
}

// keyExpr is the SQL form of identity.KeyOf for spec's key columns.
func keyExpr(cols []string) string { return "jsonb_build_array(" + strings.Join(cols, ",") + ")" }

// applyPatch writes values (a JSON object of columns) onto the row of
// tp.Table whose FK is fkValue and whose key is p.Key. values is p.Set on
// merge and p.Before on undo; jsonb_populate_record casts each value to the
// column's type.
func applyPatch(ctx context.Context, tx pgx.Tx, tp identity.TablePlan, p identity.RowPatch, fkValue int64, values json.RawMessage) error {
	cols, err := identity.PatchColumns(values)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = fmt.Sprintf("%[1]s=r.%[1]s", c)
	}
	keyCols := make([]string, len(tp.Key))
	for i, c := range tp.Key {
		keyCols[i] = "t." + c
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s t SET %[2]s FROM jsonb_populate_record(NULL::%[1]s,$1::jsonb) r WHERE t.%[3]s=$2 AND %[4]s=$3::jsonb`,
		tp.Table, strings.Join(sets, ","), tp.FK, keyExpr(keyCols)), string(values), fkValue, string(p.Key))
	return err
}

// patchFK returns the FK value of the entity whose row p updates.
func patchFK(p identity.RowPatch, targetID, sourceID int64) int64 {
	if p.Side == identity.PatchSource {
		return sourceID
	}
	return targetID
}

// MergeCandidate merges one side of a pending candidate into the other in a
// single transaction: relations move to the target without duplicates, the
// source's names become aliases (performers), the source is marked merged,
// its slug redirects to the target, and an immutable review_events row keeps
// the before snapshot and the replayable plan. targetID 0 keeps left_id.
func (s *Store) MergeCandidate(ctx context.Context, candidateID, targetID int64, actor, reason string) (MergeResult, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return MergeResult{}, err
	}
	defer tx.Rollback(ctx)
	c, err := lockCandidate(ctx, tx, candidateID)
	if err != nil {
		return MergeResult{}, err
	}
	sourceID := c.Right
	switch targetID {
	case 0, c.Left:
		targetID = c.Left
	case c.Right:
		sourceID = c.Left
	default:
		return MergeResult{}, ErrInvalidMergeTarget
	}
	res, err := mergeEntities(ctx, tx, mergeOp{EntityType: c.EntityType, TargetID: targetID, SourceID: sourceID,
		Action: "merge", Actor: actor, Reason: reason, CandidateID: &candidateID})
	if err != nil {
		return MergeResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity_candidates SET status='merged' WHERE id=$1`, candidateID); err != nil {
		return MergeResult{}, err
	}
	return res, tx.Commit(ctx)
}

// mergeOp is one merge of SourceID into TargetID. Action is the review event
// action: "merge", or "undo_split" when a split is reverted, with
// RevertsEventID naming the split event.
type mergeOp struct {
	EntityType         string
	TargetID, SourceID int64
	Action             string
	Actor, Reason      string
	CandidateID        *int64
	RevertsEventID     *int64
}

// mergeEntities is the merge path shared by MergeCandidate and UndoSplit; it
// runs inside the caller's transaction.
func mergeEntities(ctx context.Context, tx pgx.Tx, op mergeOp) (MergeResult, error) {
	targetID, sourceID := op.TargetID, op.SourceID
	table, specs, ok := identity.Tables(op.EntityType)
	if !ok {
		return MergeResult{}, fmt.Errorf("unknown entity type %q", op.EntityType)
	}
	ents, err := lockEntities(ctx, tx, table, targetID, sourceID)
	if err != nil {
		return MergeResult{}, err
	}
	target, source := ents[targetID], ents[sourceID]
	if target.MergedTo != nil || source.MergedTo != nil {
		return MergeResult{}, conflict("%s %d or %d is already merged", op.EntityType, targetID, sourceID)
	}
	in := identity.MergeInput{EntityType: op.EntityType, TargetID: targetID, SourceID: sourceID, Target: target.Row, Source: source.Row,
		TargetRows: map[string][]json.RawMessage{}, SourceRows: map[string][]json.RawMessage{}}
	for _, spec := range specs {
		if in.TargetRows[spec.Table], err = relationRows(ctx, tx, spec, targetID); err != nil {
			return MergeResult{}, err
		}
		if in.SourceRows[spec.Table], err = relationRows(ctx, tx, spec, sourceID); err != nil {
			return MergeResult{}, err
		}
	}
	plan, err := identity.BuildMergePlan(in)
	if err != nil {
		return MergeResult{}, err
	}
	res := MergeResult{EntityType: op.EntityType, TargetID: targetID, SourceID: sourceID, TargetSlug: target.Slug, SourceSlug: source.Slug,
		Moved: map[string]int{}, Deduplicated: map[string]int{}, AddedAliases: plan.AddAliases}
	for _, tp := range plan.Tables {
		key := keyExpr(tp.Key)
		for _, row := range tp.Drop {
			k, err := identity.KeyOf(row, tp.Key)
			if err != nil {
				return MergeResult{}, err
			}
			if _, err = tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s=$1 AND %s=$2::jsonb`, tp.Table, tp.FK, key), sourceID, string(k)); err != nil {
				return MergeResult{}, err
			}
		}
		for _, k := range tp.Move {
			if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE %s=$2 AND %s=$3::jsonb`, tp.Table, tp.FK, tp.FK, key), targetID, sourceID, string(k)); err != nil {
				return MergeResult{}, err
			}
		}
		for _, p := range tp.Patch {
			if err = applyPatch(ctx, tx, tp, p, patchFK(p, targetID, sourceID), p.Set); err != nil {
				return MergeResult{}, err
			}
		}
		res.Moved[tp.Table], res.Deduplicated[tp.Table] = len(tp.Move), len(tp.Drop)
		for _, p := range tp.Patch {
			if p.Side == identity.PatchSource { // kept but closed as a duplicate
				res.Deduplicated[tp.Table]++
			}
		}
	}
	for _, a := range plan.AddAliases {
		if _, err = tx.Exec(ctx, `INSERT INTO performer_aliases(performer_id,name,normalized_name,alias_type) VALUES($1,$2,$3,'merged') ON CONFLICT DO NOTHING`, targetID, a.Name, a.NormalizedName); err != nil {
			return MergeResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET merged_into_id=$1,status='merged',version=version+1,updated_at=now() WHERE id=$2`, table), targetID, sourceID); err != nil {
		return MergeResult{}, err
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET version=version+1,updated_at=now() WHERE id=$1`, table), targetID); err != nil {
		return MergeResult{}, err
	}
	before, err := json.Marshal(map[string]any{"target": target.Row, "source": source.Row, "source_relations": in.SourceRows})
	if err != nil {
		return MergeResult{}, err
	}
	after, err := json.Marshal(map[string]any{"plan": plan, "source_merged_into_id": targetID, "redirect": map[string]any{"old_slug": source.Slug, "target_slug": target.Slug}})
	if err != nil {
		return MergeResult{}, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor,candidate_id,reverts_event_id) VALUES($1,$2,$3,$4::jsonb,$5::jsonb,NULLIF($6,''),$7,$8,$9) RETURNING id`,
		op.EntityType, sourceID, op.Action, string(before), string(after), op.Reason, op.Actor, op.CandidateID, op.RevertsEventID).Scan(&res.ReviewEventID); err != nil {
		return MergeResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO slug_redirects(entity_type,old_slug,entity_id,review_event_id) VALUES($1,$2,$3,$4) ON CONFLICT(entity_type,old_slug) DO UPDATE SET entity_id=EXCLUDED.entity_id,review_event_id=EXCLUDED.review_event_id,created_at=now()`,
		op.EntityType, source.Slug, targetID, res.ReviewEventID); err != nil {
		return MergeResult{}, err
	}
	return res, nil
}

// MarkNotDuplicate records that a pending candidate's two entities are
// different. The pair is remembered in not_duplicate_pairs so generation
// never suggests it again.
func (s *Store) MarkNotDuplicate(ctx context.Context, candidateID int64, actor, reason string) (DecisionResult, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return DecisionResult{}, err
	}
	defer tx.Rollback(ctx)
	c, err := lockCandidate(ctx, tx, candidateID)
	if err != nil {
		return DecisionResult{}, err
	}
	p := identity.CanonicalPair(c.Left, c.Right)
	before, _ := json.Marshal(map[string]any{"candidate_id": c.ID, "status": c.Status, "left_id": c.Left, "right_id": c.Right})
	after, _ := json.Marshal(map[string]any{"candidate_id": c.ID, "status": "not_duplicate", "a_id": p.Lo, "b_id": p.Hi})
	res := DecisionResult{CandidateID: c.ID, Status: "not_duplicate"}
	if err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor,candidate_id) VALUES($1,$2,'not_duplicate',$3::jsonb,$4::jsonb,NULLIF($5,''),$6,$7) RETURNING id`,
		c.EntityType, p.Lo, string(before), string(after), reason, actor, c.ID).Scan(&res.ReviewEventID); err != nil {
		return DecisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO not_duplicate_pairs(entity_type,a_id,b_id,review_event_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.EntityType, p.Lo, p.Hi, res.ReviewEventID); err != nil {
		return DecisionResult{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE identity_candidates SET status='not_duplicate' WHERE id=$1`, c.ID); err != nil {
		return DecisionResult{}, err
	}
	return res, tx.Commit(ctx)
}

// UndoMerge splits a merged entity back out using the plan stored on the
// merge's review event: moved rows go back, deduplicated rows are restored
// from the snapshot, added aliases and the slug redirect are removed, and the
// source's merged_into_id and status are restored. It refuses (409) when the
// merge was already undone or the target has since been merged elsewhere
// (undo that later merge first).
func (s *Store) UndoMerge(ctx context.Context, eventID int64, actor, reason string) (UndoResult, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return UndoResult{}, err
	}
	defer tx.Rollback(ctx)
	var typ, action string
	var candidateID *int64
	var beforeRaw, afterRaw []byte
	if err = tx.QueryRow(ctx, `SELECT entity_type,action,before_value,after_value,candidate_id FROM review_events WHERE id=$1`, eventID).Scan(&typ, &action, &beforeRaw, &afterRaw, &candidateID); err != nil {
		return UndoResult{}, err
	}
	if action != "merge" {
		return UndoResult{}, conflict("review event %d is a %s, not a merge", eventID, action)
	}
	var undone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM review_events WHERE reverts_event_id=$1)`, eventID).Scan(&undone); err != nil {
		return UndoResult{}, err
	}
	if undone {
		return UndoResult{}, conflict("merge %d was already undone", eventID)
	}
	var after struct {
		Plan identity.MergePlan `json:"plan"`
	}
	var before struct {
		Source struct {
			Status string `json:"status"`
		} `json:"source"`
	}
	if err = json.Unmarshal(afterRaw, &after); err != nil {
		return UndoResult{}, err
	}
	if err = json.Unmarshal(beforeRaw, &before); err != nil {
		return UndoResult{}, err
	}
	plan := after.Plan
	table, _, ok := identity.Tables(plan.EntityType)
	if !ok || plan.EntityType != typ {
		return UndoResult{}, fmt.Errorf("review event %d has no usable merge plan", eventID)
	}
	ents, err := lockEntities(ctx, tx, table, plan.TargetID, plan.SourceID)
	if err != nil {
		return UndoResult{}, err
	}
	target, source := ents[plan.TargetID], ents[plan.SourceID]
	if source.MergedTo == nil || *source.MergedTo != plan.TargetID {
		return UndoResult{}, conflict("%s %d is no longer merged into %d", typ, plan.SourceID, plan.TargetID)
	}
	if target.MergedTo != nil {
		return UndoResult{}, conflict("%s %d was merged into %d afterwards; undo that merge first", typ, plan.TargetID, *target.MergedTo)
	}
	for _, tp := range plan.Tables {
		key := keyExpr(tp.Key)
		for _, k := range tp.Move {
			if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s=$1 WHERE %s=$2 AND %s=$3::jsonb`, tp.Table, tp.FK, tp.FK, key), plan.SourceID, plan.TargetID, string(k)); err != nil {
				return UndoResult{}, err
			}
		}
		for _, row := range tp.Drop {
			if _, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %[1]s SELECT * FROM jsonb_populate_record(NULL::%[1]s,$1::jsonb) ON CONFLICT DO NOTHING`, tp.Table), string(row)); err != nil {
				return UndoResult{}, err
			}
		}
		for _, p := range tp.Patch {
			if err = applyPatch(ctx, tx, tp, p, patchFK(p, plan.TargetID, plan.SourceID), p.Before); err != nil {
				return UndoResult{}, err
			}
		}
	}
	for _, a := range plan.AddAliases {
		if _, err = tx.Exec(ctx, `DELETE FROM performer_aliases WHERE performer_id=$1 AND normalized_name=$2 AND alias_type='merged'`, plan.TargetID, a.NormalizedName); err != nil {
			return UndoResult{}, err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM slug_redirects WHERE review_event_id=$1`, eventID); err != nil {
		return UndoResult{}, err
	}
	status := before.Source.Status
	if status == "" || status == "merged" {
		status = map[string]string{"performer": "active", "release": "draft"}[typ]
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET merged_into_id=NULL,status=$1,version=version+1,updated_at=now() WHERE id=$2`, table), status, plan.SourceID); err != nil {
		return UndoResult{}, err
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET version=version+1,updated_at=now() WHERE id=$1`, table), plan.TargetID); err != nil {
		return UndoResult{}, err
	}
	res := UndoResult{RevertedEventID: eventID, EntityType: typ, RestoredID: plan.SourceID, RestoredSlug: source.Slug, TargetID: plan.TargetID}
	beforeUndo, _ := json.Marshal(map[string]any{"target": target.Row, "source": source.Row})
	afterUndo, _ := json.Marshal(map[string]any{"source_id": plan.SourceID, "merged_into_id": nil, "status": status})
	if err = tx.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor,candidate_id,reverts_event_id) VALUES($1,$2,'undo_merge',$3::jsonb,$4::jsonb,NULLIF($5,''),$6,$7,$8) RETURNING id`,
		typ, plan.SourceID, string(beforeUndo), string(afterUndo), reason, actor, candidateID, eventID).Scan(&res.ReviewEventID); err != nil {
		return UndoResult{}, err
	}
	if candidateID != nil {
		if _, err = tx.Exec(ctx, `UPDATE identity_candidates SET status='split' WHERE id=$1`, *candidateID); err != nil {
			return UndoResult{}, err
		}
	}
	return res, tx.Commit(ctx)
}

// survivingID follows merged_into_id from id to the entity that survives, so
// ingest keeps attaching new source records and relations to the merge
// target instead of the hidden merged entity that still owns the slug.
func survivingID(ctx context.Context, tx pgx.Tx, table string, id int64) (int64, error) {
	var out int64
	err := tx.QueryRow(ctx, fmt.Sprintf(`WITH RECURSIVE chain AS (
		SELECT id,merged_into_id,0 AS depth FROM %[1]s WHERE id=$1
		UNION ALL SELECT t.id,t.merged_into_id,chain.depth+1 FROM %[1]s t JOIN chain ON t.id=chain.merged_into_id WHERE chain.depth<16
	) SELECT id FROM chain ORDER BY depth DESC LIMIT 1`, table), id).Scan(&out)
	return out, err
}

// ResolveMergedSlug returns the current slug of the entity that slug now
// belongs to, when slug names a merged entity or an old slug in
// slug_redirects. Merge chains (A into B into C) are followed to the end.
// ok is false when slug is live or unknown.
func (s *Store) ResolveMergedSlug(ctx context.Context, entityType, slug string) (string, bool, error) {
	table, _, ok := identity.Tables(entityType)
	if !ok {
		return "", false, fmt.Errorf("unknown entity type %q", entityType)
	}
	var target string
	err := s.QueryRow(ctx, fmt.Sprintf(`WITH RECURSIVE start AS (
		SELECT id FROM %[1]s WHERE slug=$1 AND merged_into_id IS NOT NULL
		UNION SELECT entity_id FROM slug_redirects WHERE entity_type=$2 AND old_slug=$1
		  AND NOT EXISTS (SELECT 1 FROM %[1]s WHERE slug=$1 AND merged_into_id IS NULL)
	), chain AS (
		SELECT t.id,t.slug,t.merged_into_id,0 AS depth FROM %[1]s t JOIN start ON start.id=t.id
		UNION ALL SELECT t.id,t.slug,t.merged_into_id,chain.depth+1 FROM %[1]s t JOIN chain ON t.id=chain.merged_into_id WHERE chain.depth<16
	) SELECT slug FROM chain WHERE merged_into_id IS NULL AND slug<>$1 ORDER BY depth LIMIT 1`, table), slug, entityType).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}
