package store

// Admin performer editor (AdminActors / A9, PRD 6, 11, 15): performer list
// with completeness filters, detail, field edits as manual overrides,
// aliases, hide / unhide, personal-information requests and the similarity
// weights preview. Every change writes review_events with the admin actor.
// Validation lives in admin_performer_rules.go.

import (
	"avxxx/internal/audit"
	"avxxx/internal/i18n"
	"avxxx/internal/identity"
	"avxxx/internal/recs"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- list

// AdminPerformerFilter selects performers for the admin list.
type AdminPerformerFilter struct {
	Q       string   // name, name_ja, alias, slug or numeric id
	Filters []string // PerformerFilters, ANDed
	Status  string   // "" = every status
	Cursor  string
	Limit   int
}

// AdminPerformerSummary is one row of the admin performer list.
type AdminPerformerSummary struct {
	ID              int64     `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	NameJA          *string   `json:"name_ja"`
	Status          string    `json:"status"`
	HiddenReason    *string   `json:"hidden_reason"`
	BirthDate       *string   `json:"birth_date"` // the real date (admin only)
	BirthDateHidden bool      `json:"birth_date_hidden"`
	Works           int       `json:"works"`
	PublishedWorks  int       `json:"published_works"`
	UpcomingWorks   int       `json:"upcoming_works"`
	HasPhoto        bool      `json:"has_photo"`
	Completeness    int       `json:"completeness"` // 0–100 over CompletenessFields
	Missing         []string  `json:"missing"`
	OpenIssues      int       `json:"open_issues"`
	OpenRequests    int       `json:"open_requests"`
	DuplicateCands  int       `json:"duplicate_candidates"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

var adminPerformerSummarySelect = `SELECT p.id,p.slug,p.canonical_name,p.canonical_name_ja,p.status,p.hidden_reason,p.birth_date::text,` + BirthHiddenSQL("p") + `,
	p.birth_place IS NOT NULL,p.debut_date IS NOT NULL,p.height_cm IS NOT NULL,` + hasPhotoSQL + `,
	(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE rp.performer_id=p.id AND r.merged_into_id IS NULL),
	(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE rp.performer_id=p.id AND r.merged_into_id IS NULL AND r.status='published'),
	(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE rp.performer_id=p.id AND ` + upcomingReleaseSQL + `),
	(SELECT count(*) FROM audit_issues ai WHERE ai.entity_type='performer' AND ai.entity_id=p.id AND ai.status='open'),
	(SELECT count(*) FROM performer_requests pr WHERE pr.performer_id=p.id AND pr.status='open'),
	(SELECT count(*) FROM identity_candidates ic WHERE ic.entity_type='performer' AND ic.status='pending' AND (ic.left_id=p.id OR ic.right_id=p.id)),
	p.version,p.updated_at
	FROM performers p`

func scanAdminPerformerSummary(r pgx.Rows) (AdminPerformerSummary, error) {
	var x AdminPerformerSummary
	var hasPlace, hasDebut, hasHeight bool
	if err := r.Scan(&x.ID, &x.Slug, &x.Name, &x.NameJA, &x.Status, &x.HiddenReason, &x.BirthDate, &x.BirthDateHidden,
		&hasPlace, &hasDebut, &hasHeight, &x.HasPhoto, &x.Works, &x.PublishedWorks, &x.UpcomingWorks, &x.OpenIssues, &x.OpenRequests,
		&x.DuplicateCands, &x.Version, &x.UpdatedAt); err != nil {
		return x, err
	}
	x.Completeness, x.Missing = Completeness(map[string]bool{"canonical_name_ja": x.NameJA != nil && *x.NameJA != "",
		"birth_date": x.BirthDate != nil, "birth_place": hasPlace, "debut_date": hasDebut, "height_cm": hasHeight, "photo": x.HasPhoto})
	return x, nil
}

// adminPerformerWhere builds the list conditions shared by the page and the
// counts (filters excluded for counts).
func adminPerformerWhere(b *sqlBuilder, q, status string) {
	b.cond("p.merged_into_id IS NULL")
	if status != "" {
		b.cond("p.status=" + b.arg(status))
	}
	if q = strings.TrimSpace(q); q != "" {
		p := b.arg(q)
		b.cond(fmt.Sprintf(`(p.canonical_name ILIKE '%%'||%[1]s||'%%' OR p.canonical_name_ja ILIKE '%%'||%[1]s||'%%' OR p.slug ILIKE '%%'||%[1]s||'%%'
			OR p.id::text=%[1]s OR p.id IN (SELECT performer_id FROM performer_aliases WHERE normalized_name ILIKE '%%'||%[2]s||'%%'))`, p, b.arg(identity.AliasForm(q))))
	}
}

// AdminPerformers lists unmerged performers, newest id first, keyset
// paginated by id, with every filter ANDed.
func (s *Store) AdminPerformers(ctx context.Context, f AdminPerformerFilter) ([]AdminPerformerSummary, string, error) {
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	adminPerformerWhere(b, f.Q, f.Status)
	for _, name := range f.Filters {
		cond, ok := performerFilterSQL[name]
		if !ok {
			return nil, "", invalidQuery("unknown filter %q", name)
		}
		b.cond(cond)
	}
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		if c.Sort != "admin_performers" {
			return nil, "", invalidQuery("cursor does not match this list")
		}
		b.cond("p.id<" + b.arg(c.ID))
	}
	rows, err := s.Query(ctx, adminPerformerSummarySelect+b.whereSQL()+" ORDER BY p.id DESC LIMIT "+b.arg(limit+1), b.args...)
	out, err := collect[AdminPerformerSummary](rows, err)(scanAdminPerformerSummary)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: "admin_performers", ID: out[limit-1].ID})
	}
	return out, next, nil
}

// AdminPerformerCounts counts unmerged performers per filter (matching q and
// status, ignoring the selected filters) plus "all".
func (s *Store) AdminPerformerCounts(ctx context.Context, q, status string) (map[string]int64, error) {
	b := &sqlBuilder{}
	adminPerformerWhere(b, q, status)
	cols := []string{"count(*)"}
	for _, name := range PerformerFilters {
		cols = append(cols, "count(*) FILTER (WHERE "+performerFilterSQL[name]+")")
	}
	vals := make([]int64, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := s.QueryRow(ctx, "SELECT "+strings.Join(cols, ",")+" FROM performers p"+b.whereSQL(), b.args...).Scan(ptrs...); err != nil {
		return nil, err
	}
	out := map[string]int64{"all": vals[0]}
	for i, name := range PerformerFilters {
		out[name] = vals[i+1]
	}
	return out, nil
}

// ---------------------------------------------------------------- detail

// AdminPerformer is everything the AdminActors page shows for one performer.
// Unlike public responses it carries the real birth date even when hidden.
type AdminPerformer struct {
	ID              int64     `json:"id"`
	Slug            string    `json:"slug"`
	Name            string    `json:"name"`
	NameJA          *string   `json:"name_ja"`
	BirthDate       *string   `json:"birth_date"`
	BirthDateHidden bool      `json:"birth_date_hidden"`
	BirthPlace      *string   `json:"birth_place"`
	DebutDate       *string   `json:"debut_date"`
	DebutAge        *int      `json:"debut_age"`
	HeightCM        *float64  `json:"height_cm"`
	BustCM          *float64  `json:"bust_cm"`
	WaistCM         *float64  `json:"waist_cm"`
	HipCM           *float64  `json:"hip_cm"`
	CupSize         *string   `json:"cup_size"`
	ShoeSize        *float64  `json:"shoe_size"`
	Biography       *string   `json:"biography"`
	Status          string    `json:"status"`
	HiddenReason    *string   `json:"hidden_reason"`
	MergedIntoID    *int64    `json:"merged_into_id"`
	Version         int64     `json:"version"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Photos          int       `json:"photos"`
	Completeness    int       `json:"completeness"`
	Missing         []string  `json:"missing"`

	Overrides           []Override              `json:"overrides"`
	Aliases             []PerformerAlias        `json:"aliases"`
	Works               []AdminPerformerWork    `json:"works"`
	Upcoming            []AdminPerformerWork    `json:"upcoming"`
	Similar             []AdminSimilarPerformer `json:"similar"`
	DuplicateCandidates []map[string]any        `json:"duplicate_candidates"`
	AuditIssues         []audit.Issue           `json:"audit_issues"`
	Requests            []PerformerRequest      `json:"requests"`
	ReviewEvents        []ReviewEvent           `json:"review_events"`
}

// PerformerAlias is one performer_aliases row.
type PerformerAlias struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Language  *string `json:"language"`
	AliasType string  `json:"alias_type"`
}

// AdminPerformerWork is one credit of the performer.
type AdminPerformerWork struct {
	ReleaseID    int64       `json:"release_id"`
	Slug         string      `json:"slug"`
	Title        string      `json:"title"`
	Status       string      `json:"status"`
	ReleaseDate  *string     `json:"release_date"`
	ScheduledAt  *time.Time  `json:"scheduled_at"`
	Role         string      `json:"role"` // lead | guest
	IsPrimary    bool        `json:"is_primary"`
	BillingOrder *int        `json:"billing_order"`
	CreditedName *string     `json:"credited_name"`
	Upcoming     bool        `json:"upcoming"`
	AgeAtFilming *FilmingAge `json:"age_at_filming"` // admin only; computed from the real birth date
}

// AdminSimilarPerformer is one performer_similar neighbour with evidence.
type AdminSimilarPerformer struct {
	ID       int64         `json:"id"`
	Slug     string        `json:"slug"`
	Name     string        `json:"name"`
	Status   string        `json:"status"`
	Score    float64       `json:"score"`
	Match    int           `json:"match_percent"`
	Reason   string        `json:"reason"`
	Evidence recs.Evidence `json:"evidence"`
}

// PerformerRequest is one performer_requests row (admin view: evidence and
// contact included; never returned by public endpoints).
type PerformerRequest struct {
	ID               int64           `json:"id"`
	PerformerID      int64           `json:"performer_id"`
	PerformerSlug    string          `json:"performer_slug"`
	PerformerName    string          `json:"performer_name"`
	Kind             string          `json:"kind"`
	FieldName        *string         `json:"field_name"`
	ProposedValue    json.RawMessage `json:"proposed_value"`
	RequesterContact string          `json:"requester_contact"`
	Evidence         json.RawMessage `json:"evidence"`
	Status           string          `json:"status"`
	DueAt            time.Time       `json:"due_at"`
	Overdue          bool            `json:"overdue"`
	ResolvedBy       *string         `json:"resolved_by"`
	ResolutionNote   *string         `json:"resolution_note"`
	CreatedAt        time.Time       `json:"created_at"`
	ResolvedAt       *time.Time      `json:"resolved_at"`
}

const performerRequestSelect = `SELECT pr.id,pr.performer_id,p.slug,p.canonical_name,pr.kind,pr.field_name,pr.proposed_value,pr.requester_contact,pr.evidence,
	pr.status,pr.due_at,pr.status='open' AND pr.due_at<now(),pr.resolved_by,pr.resolution_note,pr.created_at,pr.resolved_at
	FROM performer_requests pr JOIN performers p ON p.id=pr.performer_id`

func scanPerformerRequest(r pgx.Row) (PerformerRequest, error) {
	var x PerformerRequest
	var proposed, evidence []byte
	err := r.Scan(&x.ID, &x.PerformerID, &x.PerformerSlug, &x.PerformerName, &x.Kind, &x.FieldName, &proposed, &x.RequesterContact, &evidence,
		&x.Status, &x.DueAt, &x.Overdue, &x.ResolvedBy, &x.ResolutionNote, &x.CreatedAt, &x.ResolvedAt)
	x.ProposedValue, x.Evidence = rawOrNull(proposed), rawOrNull(evidence)
	return x, err
}

func dateText(t *time.Time) *string {
	if t == nil {
		return nil
	}
	d := t.Format(time.DateOnly)
	return &d
}

// AdminPerformer loads the full editor view of one performer, whatever its
// status (merged performers too). Unknown id: pgx.ErrNoRows.
func (s *Store) AdminPerformer(ctx context.Context, id int64) (AdminPerformer, error) {
	var x AdminPerformer
	var birth, debut *time.Time
	var debutAge *int16
	err := s.QueryRow(ctx, `SELECT id,slug,canonical_name,canonical_name_ja,birth_date,`+BirthHiddenSQL("performers")+`,birth_place,debut_date,debut_age,
		height_cm::float8,bust_cm::float8,waist_cm::float8,hip_cm::float8,cup_size,shoe_size::float8,biography,status,hidden_reason,merged_into_id,version,created_at,updated_at,
		(SELECT count(*) FROM media_assets ma WHERE ma.entity_type='performer' AND ma.entity_id=performers.id AND ma.asset_type IN ('avatar','gallery') AND ma.suppressed_at IS NULL)
		FROM performers WHERE id=$1`, id).Scan(&x.ID, &x.Slug, &x.Name, &x.NameJA, &birth, &x.BirthDateHidden, &x.BirthPlace, &debut, &debutAge,
		&x.HeightCM, &x.BustCM, &x.WaistCM, &x.HipCM, &x.CupSize, &x.ShoeSize, &x.Biography, &x.Status, &x.HiddenReason, &x.MergedIntoID,
		&x.Version, &x.CreatedAt, &x.UpdatedAt, &x.Photos)
	if err != nil {
		return x, err
	}
	x.BirthDate, x.DebutDate = dateText(birth), dateText(debut)
	if debutAge != nil {
		a := int(*debutAge)
		x.DebutAge = &a
	}
	x.Completeness, x.Missing = Completeness(map[string]bool{"canonical_name_ja": x.NameJA != nil && *x.NameJA != "", "birth_date": birth != nil,
		"birth_place": x.BirthPlace != nil, "debut_date": debut != nil, "height_cm": x.HeightCM != nil, "photo": x.Photos > 0})
	if x.Overrides, err = s.overrides(ctx, "performer", id); err != nil {
		return x, err
	}
	if x.Aliases, err = collect[PerformerAlias](s.Query(ctx, `SELECT id,name,language,alias_type FROM performer_aliases WHERE performer_id=$1 ORDER BY id`, id))(
		func(r pgx.Rows) (PerformerAlias, error) {
			var v PerformerAlias
			return v, r.Scan(&v.ID, &v.Name, &v.Language, &v.AliasType)
		}); err != nil {
		return x, err
	}
	if x.Works, err = s.performerWorks(ctx, id, birth); err != nil {
		return x, err
	}
	x.Upcoming = []AdminPerformerWork{}
	for _, w := range x.Works {
		if w.Upcoming {
			x.Upcoming = append(x.Upcoming, w)
		}
	}
	if x.Similar, err = collect[AdminSimilarPerformer](s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,p.status,ps.score,ps.evidence
		FROM performer_similar ps JOIN performers p ON p.id=ps.similar_id WHERE ps.performer_id=$1 ORDER BY ps.score DESC,p.id LIMIT 20`, id))(
		func(r pgx.Rows) (AdminSimilarPerformer, error) {
			var v AdminSimilarPerformer
			var score float32
			if err := r.Scan(&v.ID, &v.Slug, &v.Name, &v.Status, &score, &v.Evidence); err != nil {
				return v, err
			}
			v.Score, v.Match, v.Reason = round3(float64(score)), matchPercent(float64(score)), recs.PerformerReason(v.Evidence)
			return v, nil
		}); err != nil {
		return x, err
	}
	if x.DuplicateCandidates, err = collect[map[string]any](s.Query(ctx, `SELECT ic.id,CASE WHEN ic.left_id=$1 THEN ic.right_id ELSE ic.left_id END other,p.slug,p.canonical_name,ic.score::float8,ic.evidence
		FROM identity_candidates ic JOIN performers p ON p.id=CASE WHEN ic.left_id=$1 THEN ic.right_id ELSE ic.left_id END
		WHERE ic.entity_type='performer' AND ic.status='pending' AND (ic.left_id=$1 OR ic.right_id=$1) ORDER BY ic.score DESC,ic.id LIMIT 20`, id))(
		func(r pgx.Rows) (map[string]any, error) {
			var cid, other int64
			var slug, name string
			var score float64
			var ev []byte
			if err := r.Scan(&cid, &other, &slug, &name, &score, &ev); err != nil {
				return nil, err
			}
			return map[string]any{"candidate_id": cid, "performer_id": other, "slug": slug, "name": name, "score": score, "evidence": rawOrNull(ev)}, nil
		}); err != nil {
		return x, err
	}
	if x.AuditIssues, err = collect[audit.Issue](s.Query(ctx, `SELECT id,rule,entity_type,entity_id,severity,detail,priority,status,created_at,last_seen_at,resolved_at
		FROM audit_issues WHERE entity_type='performer' AND entity_id=$1 ORDER BY (status='open') DESC,id DESC LIMIT 50`, id))(scanIssue); err != nil {
		return x, err
	}
	rows, err := s.Query(ctx, performerRequestSelect+` WHERE pr.performer_id=$1 ORDER BY pr.id DESC LIMIT 50`, id)
	if x.Requests, err = collect[PerformerRequest](rows, err)(func(r pgx.Rows) (PerformerRequest, error) { return scanPerformerRequest(r) }); err != nil {
		return x, err
	}
	x.ReviewEvents, err = collect[ReviewEvent](s.Query(ctx, `SELECT id,conflict_id,entity_type,entity_id,action,before_value,after_value,reason,actor,created_at
		FROM review_events WHERE entity_type='performer' AND entity_id=$1 ORDER BY id DESC LIMIT 50`, id))(
		func(r pgx.Rows) (ReviewEvent, error) {
			var v ReviewEvent
			var before, after []byte
			err := r.Scan(&v.ID, &v.ConflictID, &v.EntityType, &v.EntityID, &v.Action, &before, &after, &v.Reason, &v.Actor, &v.CreatedAt)
			v.Before, v.After = rawOrNull(before), rawOrNull(after)
			return v, err
		})
	return x, err
}

// performerWorks lists the performer's credits (unmerged releases, every
// status), newest first, with age at filming from the real birth date.
func (s *Store) performerWorks(ctx context.Context, id int64, birth *time.Time) ([]AdminPerformerWork, error) {
	return collect[AdminPerformerWork](s.Query(ctx, `SELECT r.id,r.slug,r.canonical_title,r.status,r.release_date,r.scheduled_at,COALESCE(r.shoot_year,0),
		COALESCE(rp.role,''),rp.is_primary,rp.billing_order,rp.credited_name,`+upcomingReleaseSQL+`
		FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		WHERE rp.performer_id=$1 AND r.merged_into_id IS NULL
		ORDER BY COALESCE(r.release_date,DATE '9999-12-31') DESC,r.id DESC LIMIT 500`, id))(
		func(r pgx.Rows) (AdminPerformerWork, error) {
			var v AdminPerformerWork
			var date *time.Time
			var shootYear int
			var order *int16
			if err := r.Scan(&v.ReleaseID, &v.Slug, &v.Title, &v.Status, &date, &v.ScheduledAt, &shootYear, &v.Role, &v.IsPrimary, &order, &v.CreditedName, &v.Upcoming); err != nil {
				return v, err
			}
			v.ReleaseDate = dateText(date)
			if order != nil {
				o := int(*order)
				v.BillingOrder = &o
			}
			if v.Role == "" {
				v.Role = RoleGuest
				if v.IsPrimary {
					v.Role = RoleLead
				}
			}
			v.AgeAtFilming = AgeAtFilming(birth, shootYear, date)
			return v, nil
		})
}

// ---------------------------------------------------------------- edits

// AgeRecheck reports R-AGE re-run on every release of a performer after
// its birth date changed.
type AgeRecheck struct {
	Releases      int              `json:"releases"`       // releases re-checked
	Flagged       int              `json:"flagged"`        // with an R-AGE finding now
	Held          int              `json:"held"`           // moved to held_review by this re-check
	StatusChanged int              `json:"status_changed"` // releases whose status changed
	StillHeld     int              `json:"still_held"`     // held releases that now pass; a human must lift the hold
	Items         []AgeRecheckItem `json:"items"`
}

// AgeRecheckItem is one re-checked release.
type AgeRecheckItem struct {
	ReleaseID int64              `json:"release_id"`
	Before    string             `json:"before"`
	After     string             `json:"after"`
	Findings  []audit.AgeFinding `json:"findings,omitempty"`
}

// PerformerPatchResult is the outcome of PatchPerformer.
type PerformerPatchResult struct {
	PerformerID int64       `json:"performer_id"`
	Version     int64       `json:"version"`
	Changed     []string    `json:"changed"`
	AgeRecheck  *AgeRecheck `json:"age_recheck,omitempty"`
}

// lockPerformer locks an unmerged performer row and checks the optimistic
// lock; it returns the status.
func lockPerformer(ctx context.Context, tx pgx.Tx, id, version int64) (string, error) {
	var status string
	var cur int64
	var merged *int64
	if err := tx.QueryRow(ctx, `SELECT status,version,merged_into_id FROM performers WHERE id=$1 FOR UPDATE`, id).Scan(&status, &cur, &merged); err != nil {
		return "", err
	}
	if merged != nil {
		return "", conflict("performer was merged into %d", *merged)
	}
	if version > 0 && version != cur {
		return "", conflict("performer was changed by someone else (version %d, now %d)", version, cur)
	}
	return status, nil
}

func bumpPerformer(ctx context.Context, tx pgx.Tx, id int64) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `UPDATE performers SET version=version+1,updated_at=now() WHERE id=$1 RETURNING version`, id).Scan(&v)
	return v, err
}

// applyPerformerColumns writes each edit and records it as a manual
// override; before / after collect the JSON values.
func applyPerformerColumns(ctx context.Context, tx pgx.Tx, id int64, edits []ColumnEdit, reason, actor string, before, after map[string]json.RawMessage) ([]string, error) {
	var changed []string
	for _, e := range edits {
		var b, a []byte
		if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(%s) FROM performers WHERE id=$1`, e.Column), id).Scan(&b); err != nil {
			return nil, err
		}
		if err := tx.QueryRow(ctx, fmt.Sprintf(`UPDATE performers SET %[1]s=$1::%[2]s WHERE id=$2 RETURNING to_jsonb(%[1]s)`, e.Column, e.SQLType), e.Value, id).Scan(&a); err != nil {
			return nil, err
		}
		before[e.Column], after[e.Column] = rawOrNull(b), rawOrNull(a)
		if err := upsertOverride(ctx, tx, "performer", id, e.Column, a, reason, actor, 0); err != nil {
			return nil, err
		}
		changed = append(changed, e.Column)
	}
	return changed, nil
}

// PatchPerformer applies a validated patch: every edited column is written
// and recorded as a manual override (later crawls keep it), translations
// are marked stale and one review_events row ('edit') records before /
// after. A birth_date change re-runs R-AGE on every release of the performer
// (AgeRecheck says how many changed). Unknown performer: pgx.ErrNoRows;
// stale version or merged performer: *ConflictError.
func (s *Store) PatchPerformer(ctx context.Context, id int64, p PerformerPatch, actor string) (PerformerPatchResult, error) {
	res := PerformerPatchResult{PerformerID: id}
	if p.Empty() {
		return res, invalid("no fields to update")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockPerformer(ctx, tx, id, p.Version); err != nil {
		return res, err
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	if res.Changed, err = applyPerformerColumns(ctx, tx, id, p.Columns, p.Reason, actor, before, after); err != nil {
		return res, err
	}
	if p.BirthDateHidden != nil {
		var was bool
		if err = tx.QueryRow(ctx, `SELECT birth_date_hidden FROM performers WHERE id=$1`, id).Scan(&was); err != nil {
			return res, err
		}
		if _, err = tx.Exec(ctx, `UPDATE performers SET birth_date_hidden=$2 WHERE id=$1`, id, *p.BirthDateHidden); err != nil {
			return res, err
		}
		before["birth_date_hidden"], after["birth_date_hidden"] = jsonBool(was), jsonBool(*p.BirthDateHidden)
		res.Changed = append(res.Changed, "birth_date_hidden")
	}
	if res.Version, err = bumpPerformer(ctx, tx, id); err != nil {
		return res, err
	}
	if err = markStale(ctx, tx, i18n.EntityPerformer, strconv.FormatInt(id, 10)); err != nil {
		return res, err
	}
	if err = writeReviewEvent(ctx, tx, "performer", id, "edit", before, after, p.Reason, actor); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	if p.TouchesBirthDate() {
		rc, err := s.RecheckPerformerAge(ctx, id)
		res.AgeRecheck = &rc
		return res, err
	}
	return res, nil
}

func jsonBool(b bool) json.RawMessage {
	if b {
		return json.RawMessage("true")
	}
	return json.RawMessage("false")
}

// RecheckPerformerAge re-runs R-AGE (and R-META) through audit.CheckRelease
// on every unmerged release crediting the performer or a performer merged
// into it. It always uses the real birth date, hidden or not. R-AGE only
// ever holds: a release held before that now passes stays held until an
// admin lifts it (counted in StillHeld).
func (s *Store) RecheckPerformerAge(ctx context.Context, id int64) (AgeRecheck, error) {
	rc := AgeRecheck{Items: []AgeRecheckItem{}}
	type rel struct {
		id     int64
		status string
	}
	rels, err := collect[rel](s.Query(ctx, `SELECT DISTINCT r.id,r.status FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		WHERE r.merged_into_id IS NULL AND (rp.performer_id=$1 OR rp.performer_id IN (SELECT id FROM performers WHERE merged_into_id=$1)) ORDER BY r.id`, id))(
		func(r pgx.Rows) (rel, error) {
			var v rel
			return v, r.Scan(&v.id, &v.status)
		})
	if err != nil {
		return rc, err
	}
	for _, r := range rels {
		in, err := audit.CheckRelease(ctx, s, r.id, audit.DefaultAgeConfig, time.Now())
		if err != nil {
			return rc, err
		}
		var after string
		if err = s.QueryRow(ctx, `SELECT status FROM releases WHERE id=$1`, r.id).Scan(&after); err != nil {
			return rc, err
		}
		rc.Releases++
		if len(in.Age) > 0 {
			rc.Flagged++
		}
		if in.Held {
			rc.Held++
		}
		if after != r.status {
			rc.StatusChanged++
		}
		if after == ReleaseHeld && len(in.Age) == 0 {
			rc.StillHeld++
		}
		rc.Items = append(rc.Items, AgeRecheckItem{ReleaseID: r.id, Before: r.status, After: after, Findings: in.Age})
	}
	return rc, nil
}

// ClearPerformerOverride removes one performer override explicitly; the
// next crawl may write the field again. No such override: pgx.ErrNoRows.
func (s *Store) ClearPerformerOverride(ctx context.Context, id int64, field, reason, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err = tx.QueryRow(ctx, `DELETE FROM performer_overrides WHERE performer_id=$1 AND field_name=$2 RETURNING value`, id, field).Scan(&raw); err != nil {
		return err
	}
	if err = writeReviewEvent(ctx, tx, "performer", id, "clear_override", map[string]any{"field": field, "value": rawOrNull(raw)}, map[string]any{"field": field}, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------- aliases

// AddPerformerAlias adds an alias; an alias with the same normalized form
// already on the performer is a *ConflictError.
func (s *Store) AddPerformerAlias(ctx context.Context, id int64, in AliasInput, actor string) (PerformerAlias, error) {
	var a PerformerAlias
	tx, err := s.Begin(ctx)
	if err != nil {
		return a, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockPerformer(ctx, tx, id, 0); err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO performer_aliases(performer_id,name,normalized_name,language,alias_type) VALUES($1,$2,$3,NULLIF($4,''),$5)
		ON CONFLICT(performer_id,normalized_name) DO NOTHING RETURNING id,name,language,alias_type`, id, in.Name, identity.AliasForm(in.Name), in.Language, in.AliasType).
		Scan(&a.ID, &a.Name, &a.Language, &a.AliasType)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, conflict("alias %q already exists", in.Name)
	}
	if err != nil {
		return a, err
	}
	if _, err = bumpPerformer(ctx, tx, id); err != nil {
		return a, err
	}
	if err = writeReviewEvent(ctx, tx, "performer", id, "alias_add", nil, a, in.Reason, actor); err != nil {
		return a, err
	}
	return a, tx.Commit(ctx)
}

// RemovePerformerAlias deletes one alias. Aliases created by an identity
// merge ('merged') belong to merge / undo and cannot be removed here.
// Unknown alias: pgx.ErrNoRows.
func (s *Store) RemovePerformerAlias(ctx context.Context, id, aliasID int64, reason, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockPerformer(ctx, tx, id, 0); err != nil {
		return err
	}
	var a PerformerAlias
	if err = tx.QueryRow(ctx, `SELECT id,name,language,alias_type FROM performer_aliases WHERE id=$1 AND performer_id=$2`, aliasID, id).
		Scan(&a.ID, &a.Name, &a.Language, &a.AliasType); err != nil {
		return err
	}
	if a.AliasType == "merged" {
		return conflict("alias %q comes from an identity merge; undo the merge instead", a.Name)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM performer_aliases WHERE id=$1`, aliasID); err != nil {
		return err
	}
	if _, err = bumpPerformer(ctx, tx, id); err != nil {
		return err
	}
	if err = writeReviewEvent(ctx, tx, "performer", id, "alias_remove", a, nil, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------- status

// PerformerStatusResult is the outcome of SetPerformerStatus.
type PerformerStatusResult struct {
	PerformerID  int64   `json:"performer_id"`
	From         string  `json:"from"`
	To           string  `json:"to"`
	HiddenReason *string `json:"hidden_reason"`
	Version      int64   `json:"version"`
	// ResolvedIssues counts open R-PERFORMER-NAME issues closed by hiding.
	ResolvedIssues int64 `json:"resolved_issues"`
}

// SetPerformerStatus hides or unhides a performer (never deletes). Hidden
// performers drop out of every public query (they filter status='active');
// hiding also closes the performer's open R-PERFORMER-NAME issues.
func (s *Store) SetPerformerStatus(ctx context.Context, id int64, c PerformerStatusChange, actor string) (PerformerStatusResult, error) {
	res := PerformerStatusResult{PerformerID: id, To: c.To}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if res.From, err = lockPerformer(ctx, tx, id, c.Version); err != nil {
		return res, err
	}
	if err = CheckPerformerStatus(res.From, c); err != nil {
		return res, err
	}
	var beforeReason *string
	if err = tx.QueryRow(ctx, `SELECT hidden_reason FROM performers WHERE id=$1`, id).Scan(&beforeReason); err != nil {
		return res, err
	}
	reason := strings.TrimSpace(c.Reason)
	if err = tx.QueryRow(ctx, `UPDATE performers SET status=$2,hidden_reason=CASE WHEN $2='hidden' THEN $3 ELSE NULL END,version=version+1,updated_at=now()
		WHERE id=$1 RETURNING hidden_reason,version`, id, c.To, reason).Scan(&res.HiddenReason, &res.Version); err != nil {
		return res, err
	}
	if c.To == PerformerHidden {
		tag, err := tx.Exec(ctx, `UPDATE audit_issues SET status='fixed',resolved_at=now(),resolution_note=$2
			WHERE rule=$3 AND entity_type='performer' AND entity_id=$1 AND status='open'`, id, "performer hidden by "+actor, string(audit.RulePerformerName))
		if err != nil {
			return res, err
		}
		res.ResolvedIssues = tag.RowsAffected()
	}
	if err = writeReviewEvent(ctx, tx, "performer", id, "status_change", map[string]any{"status": res.From, "hidden_reason": beforeReason},
		map[string]any{"status": c.To, "hidden_reason": res.HiddenReason}, reason, actor); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// ---------------------------------------------------------------- requests

// PerformerRequestFilter selects requests for the admin queue.
type PerformerRequestFilter struct {
	Status      string // "" = every status
	Kind        string
	PerformerID int64
	Cursor      string
	Limit       int
}

// PerformerRequests lists requests by due date (earliest first), keyset
// paginated on (due_at, id).
func (s *Store) PerformerRequests(ctx context.Context, f PerformerRequestFilter) ([]PerformerRequest, string, error) {
	limit := pageLimit(f.Limit)
	b := &sqlBuilder{}
	if f.Status != "" {
		b.cond("pr.status=" + b.arg(f.Status))
	}
	if f.Kind != "" {
		b.cond("pr.kind=" + b.arg(f.Kind))
	}
	if f.PerformerID > 0 {
		b.cond("pr.performer_id=" + b.arg(f.PerformerID))
	}
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		due, perr := time.Parse(time.RFC3339Nano, c.Key)
		if c.Sort != "performer_requests" || perr != nil {
			return nil, "", invalidQuery("cursor does not match this list")
		}
		kp, ip := b.arg(due), b.arg(c.ID)
		b.cond(fmt.Sprintf("(pr.due_at>%[1]s OR (pr.due_at=%[1]s AND pr.id>%[2]s))", kp, ip))
	}
	rows, err := s.Query(ctx, performerRequestSelect+b.whereSQL()+" ORDER BY pr.due_at,pr.id LIMIT "+b.arg(limit+1), b.args...)
	out, err := collect[PerformerRequest](rows, err)(func(r pgx.Rows) (PerformerRequest, error) { return scanPerformerRequest(r) })
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = EncodeCursor(Cursor{Sort: "performer_requests", Key: last.DueAt.UTC().Format(time.RFC3339Nano), ID: last.ID})
	}
	return out, next, nil
}

// PerformerRequestReceipt is what the public intake returns: no evidence,
// no contact, no performer data beyond what the requester sent.
type PerformerRequestReceipt struct {
	ID     int64     `json:"id"`
	Kind   string    `json:"kind"`
	Status string    `json:"status"`
	DueAt  time.Time `json:"due_at"`
}

// CreatePerformerRequest files a public request against the performer with
// slug in.Performer (following merges; active or hidden). The input must be
// normalized. Unknown performer: pgx.ErrNoRows.
func (s *Store) CreatePerformerRequest(ctx context.Context, in PerformerRequestInput, now time.Time) (PerformerRequestReceipt, error) {
	r := PerformerRequestReceipt{Kind: in.Kind, Status: RequestOpen, DueAt: now.Add(RequestSLA(in.Kind)).UTC()}
	tx, err := s.Begin(ctx)
	if err != nil {
		return r, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM performers WHERE slug=$1`, in.Performer).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT entity_id FROM slug_redirects WHERE entity_type='performer' AND old_slug=$1`, in.Performer).Scan(&id)
	}
	if err != nil {
		return r, err
	}
	if id, err = survivingID(ctx, tx, "performers", id); err != nil {
		return r, err
	}
	var proposed any
	if in.Kind == RequestCorrectField {
		b, _ := json.Marshal(in.value) // *string: JSON string or null
		proposed = string(b)
	}
	ev, err := json.Marshal(in.evidence())
	if err != nil {
		return r, err
	}
	var field any
	if in.column != "" {
		field = in.column
	}
	if err = tx.QueryRow(ctx, `INSERT INTO performer_requests(performer_id,kind,field_name,proposed_value,requester_contact,evidence,due_at,created_at)
		VALUES($1,$2,$3,$4::jsonb,$5,$6::jsonb,$7,$8) RETURNING id`, id, in.Kind, field, proposed, in.Contact, string(ev), r.DueAt, now).Scan(&r.ID); err != nil {
		return r, err
	}
	// The log names the request, never its evidence or contact.
	if err = writeReviewEvent(ctx, tx, "performer", id, "request_received", nil,
		map[string]any{"request_id": r.ID, "kind": in.Kind, "field": field}, "", "public"); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// RequestDecisionResult is the outcome of DecidePerformerRequest.
type RequestDecisionResult struct {
	Request PerformerRequest `json:"request"`
	// Applied describes the change an approval made (field / value,
	// birth_date_hidden, suppressed photos).
	Applied    map[string]any `json:"applied"`
	AgeRecheck *AgeRecheck    `json:"age_recheck,omitempty"`
}

// DecidePerformerRequest approves or rejects an open request (open is the
// only state that can be decided; approved / rejected are final, 409).
// Approving applies it in the same transaction:
//   - hide_birth_date sets performers.birth_date_hidden;
//   - correct_field writes the proposed value (or d.Value) as a manual
//     override, re-running R-AGE when it is birth_date;
//   - remove_photo suppresses the performer's avatar and gallery photos;
//   - other changes nothing (handled outside the system).
//
// Every decision writes a review_events row on the performer; the
// requester's evidence and contact are never copied into it.
func (s *Store) DecidePerformerRequest(ctx context.Context, id int64, to string, d RequestDecision, actor string) (RequestDecisionResult, error) {
	res := RequestDecisionResult{Applied: map[string]any{}}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	var performerID int64
	var kind, status string
	var field *string
	var proposed []byte
	if err = tx.QueryRow(ctx, `SELECT performer_id,kind,status,field_name,proposed_value FROM performer_requests WHERE id=$1 FOR UPDATE`, id).
		Scan(&performerID, &kind, &status, &field, &proposed); err != nil {
		return res, err
	}
	if err = CheckRequestTransition(status, to, d); err != nil {
		return res, err
	}
	if len(d.Value) > 0 && kind != RequestCorrectField {
		return res, invalid("value is only accepted for correct_field requests")
	}
	if performerID, err = survivingID(ctx, tx, "performers", performerID); err != nil {
		return res, err
	}
	reason := requestReason(id, kind, d.Note)
	recheck := false
	if to == RequestApproved {
		if _, err = lockPerformer(ctx, tx, performerID, 0); err != nil {
			return res, err
		}
		before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
		switch kind {
		case RequestHideBirthDate:
			var was bool
			if err = tx.QueryRow(ctx, `SELECT birth_date_hidden FROM performers WHERE id=$1`, performerID).Scan(&was); err != nil {
				return res, err
			}
			if _, err = tx.Exec(ctx, `UPDATE performers SET birth_date_hidden=true WHERE id=$1`, performerID); err != nil {
				return res, err
			}
			before["birth_date_hidden"], after["birth_date_hidden"] = jsonBool(was), jsonBool(true)
			res.Applied["birth_date_hidden"] = true
		case RequestCorrectField:
			value := json.RawMessage(proposed)
			if len(d.Value) > 0 {
				value = d.Value
			}
			if field == nil {
				return res, invalid("request has no field")
			}
			edit, err := performerColumnEdit(*field, value)
			if err != nil {
				return res, err
			}
			if _, err = applyPerformerColumns(ctx, tx, performerID, []ColumnEdit{edit}, reason, actor, before, after); err != nil {
				return res, err
			}
			if err = markStale(ctx, tx, i18n.EntityPerformer, strconv.FormatInt(performerID, 10)); err != nil {
				return res, err
			}
			res.Applied["field"], res.Applied["value"] = edit.Column, after[edit.Column]
			recheck = edit.Column == "birth_date"
		case RequestRemovePhoto:
			tag, err := tx.Exec(ctx, `UPDATE media_assets SET suppressed_at=now(),suppressed_reason=$2
				WHERE entity_type='performer' AND entity_id=$1 AND asset_type IN ('avatar','gallery') AND suppressed_at IS NULL`, performerID, reason)
			if err != nil {
				return res, err
			}
			res.Applied["suppressed_photos"] = tag.RowsAffected()
		}
		if _, err = bumpPerformer(ctx, tx, performerID); err != nil {
			return res, err
		}
		after["request_id"] = json.RawMessage(strconv.FormatInt(id, 10))
		if err = writeReviewEvent(ctx, tx, "performer", performerID, "request_approve", before, after, reason, actor); err != nil {
			return res, err
		}
	} else if err = writeReviewEvent(ctx, tx, "performer", performerID, "request_reject", nil,
		map[string]any{"request_id": id, "kind": kind}, reason, actor); err != nil {
		return res, err
	}
	if _, err = tx.Exec(ctx, `UPDATE performer_requests SET status=$2,resolved_by=$3,resolution_note=NULLIF($4,''),resolved_at=now() WHERE id=$1`,
		id, to, actor, strings.TrimSpace(d.Note)); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	if res.Request, err = scanPerformerRequest(s.QueryRow(ctx, performerRequestSelect+` WHERE pr.id=$1`, id)); err != nil {
		return res, err
	}
	if recheck {
		rc, err := s.RecheckPerformerAge(ctx, performerID)
		res.AgeRecheck = &rc
		return res, err
	}
	return res, nil
}

// ---------------------------------------------------------------- R-PERFORMER-NAME

// PerformerNames implements audit.Store: unmerged performers that are not
// hidden, with the <title>s of their source pages and the titles their
// source also used on other performers' pages (the site's own title).
func (s *Store) PerformerNames(ctx context.Context, afterID int64, limit int) ([]audit.PerformerNameFacts, error) {
	return collect[audit.PerformerNameFacts](s.Query(ctx, `SELECT p.id,p.canonical_name,COALESCE(p.canonical_name_ja,''),
		COALESCE((SELECT array_agg(DISTINCT sr.raw_data->>'page_title') FROM source_records sr
			WHERE sr.performer_id=p.id AND sr.entity_type='performer' AND sr.raw_data ? 'page_title'),'{}'),
		COALESCE((SELECT array_agg(DISTINCT sr.raw_data->>'page_title') FROM source_records sr
			WHERE sr.performer_id=p.id AND sr.entity_type='performer' AND sr.raw_data ? 'page_title'
			AND EXISTS (SELECT 1 FROM source_records o WHERE o.source_id=sr.source_id AND o.entity_type='performer'
				AND o.raw_data ? 'page_title' AND o.raw_data->>'page_title'=sr.raw_data->>'page_title' AND o.performer_id<>p.id)),'{}'),
		(SELECT count(DISTINCT rp.release_id) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
			WHERE rp.performer_id=p.id AND r.status='published' AND r.merged_into_id IS NULL)
		FROM performers p WHERE p.merged_into_id IS NULL AND p.status<>'hidden' AND p.id>$1 ORDER BY p.id LIMIT $2`, afterID, limit))(
		func(r pgx.Rows) (audit.PerformerNameFacts, error) {
			var v audit.PerformerNameFacts
			return v, r.Scan(&v.ID, &v.Name, &v.NameJA, &v.PageTitles, &v.SiteTitles, &v.Releases)
		})
}

// ---------------------------------------------------------------- similarity preview

// SimilaritySeed is a sample performer with its stored neighbours.
type SimilaritySeed struct {
	ID        int64
	Slug      string
	Name      string
	Neighbors []SimilarityNeighbor
}

// SimilarityNeighbor is one stored performer_similar row.
type SimilarityNeighbor struct {
	ID       int64
	Slug     string
	Name     string
	Score    float64
	Evidence recs.Evidence
}

// SimilaritySeeds returns the sample for the weights preview: ids when
// given (active, unmerged), else the n active performers with the most
// stored neighbours. Neighbours are every stored performer_similar row of
// each seed whose performer is active.
func (s *Store) SimilaritySeeds(ctx context.Context, ids []int64, n int) ([]SimilaritySeed, error) {
	type seed struct {
		id         int64
		slug, name string
	}
	var q string
	args := []any{}
	if len(ids) > 0 {
		q = `SELECT p.id,p.slug,p.canonical_name FROM performers p WHERE p.id=ANY($1) AND ` + activeP + ` ORDER BY p.id`
		args = append(args, ids)
	} else {
		q = `SELECT p.id,p.slug,p.canonical_name FROM performers p
			JOIN (SELECT performer_id,count(*) c FROM performer_similar GROUP BY 1) ps ON ps.performer_id=p.id
			WHERE ` + activeP + ` ORDER BY ps.c DESC,p.id LIMIT $1`
		args = append(args, n)
	}
	seeds, err := collect[seed](s.Query(ctx, q, args...))(func(r pgx.Rows) (seed, error) {
		var v seed
		return v, r.Scan(&v.id, &v.slug, &v.name)
	})
	if err != nil {
		return nil, err
	}
	out := make([]SimilaritySeed, 0, len(seeds))
	for _, sd := range seeds {
		nb, err := collect[SimilarityNeighbor](s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,ps.score,ps.evidence
			FROM performer_similar ps JOIN performers p ON p.id=ps.similar_id AND `+activeP+`
			WHERE ps.performer_id=$1 ORDER BY ps.score DESC,p.id`, sd.id))(func(r pgx.Rows) (SimilarityNeighbor, error) {
			var v SimilarityNeighbor
			var score float32
			err := r.Scan(&v.ID, &v.Slug, &v.Name, &score, &v.Evidence)
			v.Score = float64(score)
			return v, err
		})
		if err != nil {
			return nil, err
		}
		out = append(out, SimilaritySeed{ID: sd.id, Slug: sd.slug, Name: sd.name, Neighbors: nb})
	}
	return out, nil
}
