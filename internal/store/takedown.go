package store

// Copyright takedowns and counter-notices (CopyrightForm, AdminOps 版权投诉;
// PRD 15; migration 041). The public side files a notice (ticket), looks
// up its status with ticket + email, and files a counter-notice on an
// actioned ticket. Admin actions reuse the existing paths: a release moves
// to unpublished through the release status machine, an image is suppressed
// like POST /api/admin/media/{id}/suppress (objects purged after 24 h).
// Every change writes review_events (entity_type 'takedown'), next to the
// release / media events the reused paths write.

import (
	"avxxx/internal/images"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Takedown is one ticket (admin view: contact details included).
type Takedown struct {
	ID                int64            `json:"id"`
	Ticket            string           `json:"ticket"`
	Kind              string           `json:"kind"`
	Status            string           `json:"status"`
	ClaimantName      string           `json:"claimant_name"`
	ClaimantEmail     string           `json:"claimant_email"`
	ClaimantPhone     string           `json:"claimant_phone"`
	ClaimantAddress   string           `json:"claimant_address"`
	RightsOwner       string           `json:"rights_owner"`
	Relationship      string           `json:"relationship"`
	WorkDescription   string           `json:"work_description"`
	OriginalURLs      []string         `json:"original_urls"`
	InfringingURLs    []string         `json:"infringing_urls"`
	Signature         string           `json:"signature"`
	Lang              string           `json:"lang"`
	DueAt             time.Time        `json:"due_at"`
	FirstActionAt     *time.Time       `json:"first_action_at"`
	Overdue           bool             `json:"overdue"`
	CounterName       *string          `json:"counter_name"`
	CounterEmail      *string          `json:"counter_email"`
	CounterAddress    *string          `json:"counter_address"`
	CounterStatement  *string          `json:"counter_statement"`
	CounterSignature  *string          `json:"counter_signature"`
	CounterReceivedAt *time.Time       `json:"counter_received_at"`
	ResolutionNote    string           `json:"resolution_note"`
	ActiveTargets     int              `json:"active_targets"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	Targets           []TakedownTarget `json:"targets,omitempty"`
	Events            []TakedownEvent  `json:"events,omitempty"`
}

// TakedownTarget is one action taken for a ticket.
type TakedownTarget struct {
	ID             int64           `json:"id"`
	TargetType     string          `json:"target_type"`
	TargetID       int64           `json:"target_id"`
	Action         string          `json:"action"`
	PreviousStatus *string         `json:"previous_status"`
	Detail         json.RawMessage `json:"detail"`
	State          string          `json:"state"`
	Actor          string          `json:"actor"`
	CreatedAt      time.Time       `json:"created_at"`
	RestoredAt     *time.Time      `json:"restored_at"`
	RestoredBy     *string         `json:"restored_by"`
}

// TakedownEvent is a review_events row of a ticket.
type TakedownEvent struct {
	ID        int64           `json:"id"`
	Action    string          `json:"action"`
	Before    json.RawMessage `json:"before"`
	After     json.RawMessage `json:"after"`
	Reason    string          `json:"reason"`
	Actor     string          `json:"actor"`
	CreatedAt time.Time       `json:"created_at"`
}

// TakedownReceipt is returned to the claimant after filing.
type TakedownReceipt struct {
	Ticket    string    `json:"ticket"`
	Status    string    `json:"status"`
	DueAt     time.Time `json:"due_at"`
	CreatedAt time.Time `json:"created_at"`
}

// TakedownPublicStatus is all the public lookup reveals: no names, URLs or
// notes.
type TakedownPublicStatus struct {
	Ticket         string    `json:"ticket"`
	Status         string    `json:"status"`
	ReceivedAt     time.Time `json:"received_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CounterAllowed bool      `json:"counter_allowed"`
}

// TakedownFilter selects the admin queue. Open lists tickets that still
// need work (received, reviewing, actioned, counter_received).
type TakedownFilter struct {
	Status string
	Open   bool
	Limit  int
	Cursor string
}

// takedownOverdueSQL: no first action yet and past the 24 h deadline.
const takedownOverdueSQL = `(t.first_action_at IS NULL AND t.status IN ('received','reviewing') AND t.due_at < now())`

const takedownColumns = `t.id,t.ticket,t.kind,t.status,t.claimant_name,t.claimant_email,t.claimant_phone,t.claimant_address,t.rights_owner,
	t.relationship,t.work_description,t.original_urls,t.infringing_urls,t.signature,COALESCE(t.lang,''),t.due_at,t.first_action_at,` + takedownOverdueSQL + `,
	t.counter_name,t.counter_email,t.counter_address,t.counter_statement,t.counter_signature,t.counter_received_at,t.resolution_note,
	(SELECT count(*) FROM takedown_targets x WHERE x.takedown_id=t.id AND x.state='active')::int,t.created_at,t.updated_at`

func scanTakedown(r pgx.Row) (Takedown, error) {
	var t Takedown
	err := r.Scan(&t.ID, &t.Ticket, &t.Kind, &t.Status, &t.ClaimantName, &t.ClaimantEmail, &t.ClaimantPhone, &t.ClaimantAddress, &t.RightsOwner,
		&t.Relationship, &t.WorkDescription, &t.OriginalURLs, &t.InfringingURLs, &t.Signature, &t.Lang, &t.DueAt, &t.FirstActionAt, &t.Overdue,
		&t.CounterName, &t.CounterEmail, &t.CounterAddress, &t.CounterStatement, &t.CounterSignature, &t.CounterReceivedAt, &t.ResolutionNote,
		&t.ActiveTargets, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// CreateTakedown files a notice with a fresh random ticket, due in 24 h.
func (s *Store) CreateTakedown(ctx context.Context, in TakedownInput, now time.Time) (TakedownReceipt, error) {
	if err := in.Normalize(); err != nil {
		return TakedownReceipt{}, err
	}
	for range 3 {
		ticket, err := NewTicket()
		if err != nil {
			return TakedownReceipt{}, err
		}
		out := TakedownReceipt{Ticket: ticket}
		err = s.QueryRow(ctx, `INSERT INTO takedown_requests(ticket,claimant_name,claimant_email,claimant_phone,claimant_address,rights_owner,relationship,
			work_description,original_urls,infringing_urls,good_faith,accuracy,signature,lang,due_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15) ON CONFLICT (ticket) DO NOTHING RETURNING status,due_at,created_at`,
			ticket, in.ClaimantName, in.ClaimantEmail, in.ClaimantPhone, in.ClaimantAddress, in.RightsOwner, in.Relationship, in.WorkDescription,
			in.OriginalURLs, in.InfringingURLs, in.GoodFaith, in.Accuracy, in.Signature, in.Lang, now.Add(TakedownSLA)).Scan(&out.Status, &out.DueAt, &out.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // 80-bit collision: draw again
		}
		return out, err
	}
	return TakedownReceipt{}, errors.New("could not allocate a ticket number")
}

// TakedownStatus is the public lookup: the ticket must match and email must
// be the claimant's or the counter-notifier's. Any mismatch is
// pgx.ErrNoRows, so a ticket's existence is not revealed.
func (s *Store) TakedownStatus(ctx context.Context, ticket, email string) (TakedownPublicStatus, error) {
	var out TakedownPublicStatus
	ticket, ok := NormalizeTicket(ticket)
	email = strings.TrimSpace(email)
	if !ok || email == "" {
		return out, pgx.ErrNoRows
	}
	err := s.QueryRow(ctx, `SELECT ticket,status,created_at,updated_at FROM takedown_requests
		WHERE ticket=$1 AND (lower(claimant_email)=lower($2) OR lower(COALESCE(counter_email,''))=lower($2))`, ticket, email).
		Scan(&out.Ticket, &out.Status, &out.ReceivedAt, &out.UpdatedAt)
	out.CounterAllowed = out.Status == TakedownActioned
	return out, err
}

// SubmitCounterNotice records a counter-notice on an actioned ticket (the
// ticket number is the capability; the affected party gets it from us).
// Unknown ticket: pgx.ErrNoRows; not actioned: conflict.
func (s *Store) SubmitCounterNotice(ctx context.Context, ticket string, in CounterNoticeInput) (TakedownPublicStatus, error) {
	var out TakedownPublicStatus
	ticket, ok := NormalizeTicket(ticket)
	if !ok {
		return out, pgx.ErrNoRows
	}
	if err := in.Normalize(); err != nil {
		return out, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var status string
	if err = tx.QueryRow(ctx, `SELECT id,status FROM takedown_requests WHERE ticket=$1 FOR UPDATE`, ticket).Scan(&id, &status); err != nil {
		return out, err
	}
	if status != TakedownActioned {
		return out, conflict("a counter-notice can only be filed on a ticket whose content was taken down")
	}
	if err = tx.QueryRow(ctx, `UPDATE takedown_requests SET status='counter_received',counter_name=$2,counter_email=$3,counter_address=$4,
		counter_statement=$5,counter_consent=$6,counter_signature=$7,counter_received_at=now(),updated_at=now()
		WHERE id=$1 RETURNING ticket,status,created_at,updated_at`, id, in.Name, in.Email, in.Address, in.Statement, in.Consent, in.Signature).
		Scan(&out.Ticket, &out.Status, &out.ReceivedAt, &out.UpdatedAt); err != nil {
		return out, err
	}
	// Contact details stay in the ticket row, not in the event.
	if err = writeReviewEvent(ctx, tx, "takedown", id, "takedown_counter_notice", map[string]any{"status": status},
		map[string]any{"status": TakedownCounter}, "", "public"); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// AdminTakedowns lists tickets newest first (cursor: last id).
func (s *Store) AdminTakedowns(ctx context.Context, f TakedownFilter) ([]Takedown, string, error) {
	if f.Status != "" && !slices.Contains(TakedownStatuses, f.Status) {
		return nil, "", invalid("status must be one of %s", strings.Join(TakedownStatuses, ", "))
	}
	limit := clampAdminLimit(f.Limit)
	var after int64
	if f.Cursor != "" {
		n, err := strconv.ParseInt(f.Cursor, 10, 64)
		if err != nil || n <= 0 {
			return nil, "", invalid("invalid cursor")
		}
		after = n
	}
	list, err := collect[Takedown](s.Query(ctx, `SELECT `+takedownColumns+` FROM takedown_requests t
		WHERE ($1='' OR t.status=$1) AND (NOT $2 OR t.status IN ('received','reviewing','actioned','counter_received')) AND ($3=0 OR t.id<$3)
		ORDER BY t.id DESC LIMIT $4`, f.Status, f.Open, after, limit+1))(func(r pgx.Rows) (Takedown, error) { return scanTakedown(r) })
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(list) > limit {
		list = list[:limit]
		next = strconv.FormatInt(list[limit-1].ID, 10)
	}
	return list, next, nil
}

// TakedownCounts is the admin badge: open tickets and those past the SLA.
type TakedownCounts struct {
	Open    int64 `json:"open"`
	Overdue int64 `json:"overdue"`
}

// AdminTakedownCounts counts open and overdue tickets.
func (s *Store) AdminTakedownCounts(ctx context.Context) (TakedownCounts, error) {
	var c TakedownCounts
	err := s.QueryRow(ctx, `SELECT count(*) FILTER (WHERE t.status IN ('received','reviewing','actioned','counter_received')),
		count(*) FILTER (WHERE `+takedownOverdueSQL+`) FROM takedown_requests t`).Scan(&c.Open, &c.Overdue)
	return c, err
}

// AdminTakedown returns a ticket with its targets and review events.
func (s *Store) AdminTakedown(ctx context.Context, id int64) (Takedown, error) {
	t, err := scanTakedown(s.QueryRow(ctx, `SELECT `+takedownColumns+` FROM takedown_requests t WHERE t.id=$1`, id))
	if err != nil {
		return t, err
	}
	if t.Targets, err = collect[TakedownTarget](s.Query(ctx, `SELECT id,target_type,target_id,action,previous_status,detail,state,actor,created_at,restored_at,restored_by
		FROM takedown_targets WHERE takedown_id=$1 ORDER BY id`, id))(func(r pgx.Rows) (TakedownTarget, error) {
		var x TakedownTarget
		return x, r.Scan(&x.ID, &x.TargetType, &x.TargetID, &x.Action, &x.PreviousStatus, &x.Detail, &x.State, &x.Actor, &x.CreatedAt, &x.RestoredAt, &x.RestoredBy)
	}); err != nil {
		return t, err
	}
	t.Events, err = collect[TakedownEvent](s.Query(ctx, `SELECT id,action,COALESCE(before_value,'null'::jsonb),COALESCE(after_value,'null'::jsonb),COALESCE(reason,''),COALESCE(actor,''),created_at
		FROM review_events WHERE entity_type='takedown' AND entity_id=$1 ORDER BY id`, id))(func(r pgx.Rows) (TakedownEvent, error) {
		var e TakedownEvent
		return e, r.Scan(&e.ID, &e.Action, &e.Before, &e.After, &e.Reason, &e.Actor, &e.CreatedAt)
	})
	return t, err
}

// lockTakedown locks a ticket row and returns its status.
func lockTakedown(ctx context.Context, tx pgx.Tx, id int64) (string, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM takedown_requests WHERE id=$1 FOR UPDATE`, id).Scan(&status)
	return status, err
}

// SetTakedownStatus is a manual status change (reviewing, rejected, closed)
// with an optional resolution note. A rejection counts as the first action
// for the SLA.
func (s *Store) SetTakedownStatus(ctx context.Context, id int64, to, note, actor string) (Takedown, error) {
	note = strings.TrimSpace(note)
	if len(note) > MaxReasonLen {
		return Takedown{}, invalid("note too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Takedown{}, err
	}
	defer tx.Rollback(ctx)
	from, err := lockTakedown(ctx, tx, id)
	if err != nil {
		return Takedown{}, err
	}
	if err = CheckTakedownTransition(from, to); err != nil {
		return Takedown{}, err
	}
	if to == TakedownRejected && note == "" {
		return Takedown{}, invalid("a note is required to reject a notice")
	}
	if _, err = tx.Exec(ctx, `UPDATE takedown_requests SET status=$2,resolution_note=CASE WHEN $3='' THEN resolution_note ELSE $3 END,
		first_action_at=CASE WHEN $2 IN ('rejected','closed') THEN COALESCE(first_action_at,now()) ELSE first_action_at END,updated_at=now()
		WHERE id=$1`, id, to, note); err != nil {
		return Takedown{}, err
	}
	if err = writeReviewEvent(ctx, tx, "takedown", id, "takedown_status", map[string]any{"status": from}, map[string]any{"status": to}, note, actor); err != nil {
		return Takedown{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Takedown{}, err
	}
	return s.AdminTakedown(ctx, id)
}

// TakedownAction takes content down for a ticket: unpublish a release or
// suppress an image, in one transaction with the ticket update. The ticket
// becomes actioned and its first action time is set (SLA).
func (s *Store) TakedownAction(ctx context.Context, id int64, in TakedownActionInput, actor string) (Takedown, error) {
	if err := in.Normalize(); err != nil {
		return Takedown{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Takedown{}, err
	}
	defer tx.Rollback(ctx)
	from, err := lockTakedown(ctx, tx, id)
	if err != nil {
		return Takedown{}, err
	}
	var ticket string
	if err = tx.QueryRow(ctx, `SELECT ticket FROM takedown_requests WHERE id=$1`, id).Scan(&ticket); err != nil {
		return Takedown{}, err
	}
	if !slices.Contains([]string{TakedownReceived, TakedownReviewing, TakedownActioned}, from) {
		return Takedown{}, conflict("cannot take content down on a %s ticket", from)
	}
	reason := "copyright takedown " + ticket
	if in.Reason != "" {
		reason += ": " + in.Reason
	}
	var (
		targetType, action string
		targetID           int64
		previous           *string
		detail             map[string]any
	)
	switch in.Action {
	case TakedownUnpublishRelease:
		targetType, action = "release", "unpublish"
		var slug, title, status string
		row := tx.QueryRow(ctx, `SELECT id,slug,canonical_title,status FROM releases WHERE (id=$1 OR ($1=0 AND slug=$2)) AND merged_into_id IS NULL`, in.ReleaseID, in.ReleaseSlug)
		if err = row.Scan(&targetID, &slug, &title, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Takedown{}, invalid("release not found (or merged into another)")
			}
			return Takedown{}, err
		}
		previous = &status
		detail = map[string]any{"slug": slug, "title": title}
		// Already down (another ticket, or by hand): record it without a
		// second transition; a restore then leaves the status alone.
		if status != ReleaseUnpublished {
			if _, err = transitionReleaseTx(ctx, tx, targetID, Transition{To: ReleaseUnpublished, Reason: reason}, actor); err != nil {
				return Takedown{}, err
			}
		}
	case TakedownSuppressImage:
		targetType, action = "media_asset", "suppress"
		orig, err := suppressMediaTx(ctx, tx, in.MediaAssetID, reason, actor)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Takedown{}, invalid("image not found (it may already be purged)")
			}
			return Takedown{}, err
		}
		targetID = orig.ID
		detail = map[string]any{"entity_type": orig.EntityType, "entity_id": orig.EntityID, "asset_type": orig.AssetType, "object_key": orig.ObjectKey}
	}
	var dup bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM takedown_targets WHERE takedown_id=$1 AND target_type=$2 AND target_id=$3 AND state='active')`,
		id, targetType, targetID).Scan(&dup); err != nil {
		return Takedown{}, err
	}
	if dup {
		return Takedown{}, conflict("this ticket already took that %s down", targetType)
	}
	d, err := json.Marshal(detail)
	if err != nil {
		return Takedown{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO takedown_targets(takedown_id,target_type,target_id,action,previous_status,detail,actor)
		VALUES($1,$2,$3,$4,$5,$6::jsonb,$7)`, id, targetType, targetID, action, previous, string(d), actor); err != nil {
		return Takedown{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE takedown_requests SET status='actioned',first_action_at=COALESCE(first_action_at,now()),updated_at=now() WHERE id=$1`, id); err != nil {
		return Takedown{}, err
	}
	after := map[string]any{"status": TakedownActioned, "target_type": targetType, "target_id": targetID, "action": action}
	if previous != nil {
		after["previous_status"] = *previous
	}
	if err = writeReviewEvent(ctx, tx, "takedown", id, "takedown_"+action, map[string]any{"status": from}, after, in.Reason, actor); err != nil {
		return Takedown{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Takedown{}, err
	}
	return s.AdminTakedown(ctx, id)
}

// RestoreTakedown undoes every active action of a ticket (after a
// counter-notice, or when the claim turns out invalid): a release goes back
// to published when it was published or upcoming before and no other
// ticket still holds it down; an image is restored, or, when the purge
// already deleted it, its download block is lifted so media_fetch fetches
// it again. Publishing still has to pass R-AGE.
func (s *Store) RestoreTakedown(ctx context.Context, id int64, reason, actor string) (Takedown, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return Takedown{}, invalid("reason is required")
	}
	if len(reason) > MaxReasonLen {
		return Takedown{}, invalid("reason too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Takedown{}, err
	}
	defer tx.Rollback(ctx)
	from, err := lockTakedown(ctx, tx, id)
	if err != nil {
		return Takedown{}, err
	}
	if from != TakedownActioned && from != TakedownCounter {
		return Takedown{}, conflict("only an actioned ticket or one with a counter-notice can be restored")
	}
	var ticket string
	if err = tx.QueryRow(ctx, `SELECT ticket FROM takedown_requests WHERE id=$1`, id).Scan(&ticket); err != nil {
		return Takedown{}, err
	}
	targets, err := collect[TakedownTarget](tx.Query(ctx, `SELECT id,target_type,target_id,action,previous_status,detail,state,actor,created_at,restored_at,restored_by
		FROM takedown_targets WHERE takedown_id=$1 AND state='active' ORDER BY id FOR UPDATE`, id))(func(r pgx.Rows) (TakedownTarget, error) {
		var x TakedownTarget
		return x, r.Scan(&x.ID, &x.TargetType, &x.TargetID, &x.Action, &x.PreviousStatus, &x.Detail, &x.State, &x.Actor, &x.CreatedAt, &x.RestoredAt, &x.RestoredBy)
	})
	if err != nil {
		return Takedown{}, err
	}
	note := "restore after copyright ticket " + ticket + ": " + reason
	restored := []map[string]any{}
	for _, x := range targets {
		outcome, err := restoreTarget(ctx, tx, id, x, note, actor)
		if err != nil {
			return Takedown{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE takedown_targets SET state='restored',restored_at=now(),restored_by=$2 WHERE id=$1`, x.ID, actor); err != nil {
			return Takedown{}, err
		}
		restored = append(restored, map[string]any{"target_type": x.TargetType, "target_id": x.TargetID, "outcome": outcome})
	}
	if _, err = tx.Exec(ctx, `UPDATE takedown_requests SET status='restored',updated_at=now() WHERE id=$1`, id); err != nil {
		return Takedown{}, err
	}
	if err = writeReviewEvent(ctx, tx, "takedown", id, "takedown_restore", map[string]any{"status": from},
		map[string]any{"status": TakedownRestored, "targets": restored}, reason, actor); err != nil {
		return Takedown{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Takedown{}, err
	}
	return s.AdminTakedown(ctx, id)
}

// restoreTarget undoes one action and says what happened.
func restoreTarget(ctx context.Context, tx pgx.Tx, ticketID int64, x TakedownTarget, note, actor string) (string, error) {
	switch x.TargetType {
	case "release":
		var others bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM takedown_targets WHERE target_type='release' AND target_id=$1 AND state='active' AND takedown_id<>$2)`,
			x.TargetID, ticketID).Scan(&others); err != nil {
			return "", err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM releases WHERE id=$1`, x.TargetID).Scan(&status); err != nil {
			return "", err
		}
		prev := ""
		if x.PreviousStatus != nil {
			prev = *x.PreviousStatus
		}
		switch {
		case others:
			return "kept_down_by_other_ticket", nil
		case status != ReleaseUnpublished:
			return "left_as_" + status, nil
		case prev != ReleasePublished && prev != ReleaseUpcoming:
			return "left_unpublished", nil
		}
		if _, err := transitionReleaseTx(ctx, tx, x.TargetID, Transition{To: ReleasePublished, Reason: note}, actor); err != nil {
			return "", err
		}
		return "republished", nil
	case "media_asset":
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM media_assets WHERE id=$1)`, x.TargetID).Scan(&exists); err != nil {
			return "", err
		}
		if exists {
			if _, err := restoreMediaTx(ctx, tx, x.TargetID, note, actor); err != nil {
				return "", err
			}
			return "image_restored", nil
		}
		var d struct {
			EntityType string `json:"entity_type"`
			EntityID   int64  `json:"entity_id"`
			AssetType  string `json:"asset_type"`
		}
		if err := json.Unmarshal(x.Detail, &d); err != nil || d.EntityType == "" {
			return "image_purged", nil
		}
		if d.EntityType == images.EntityPerformer {
			return "image_purged", nil // performer photo blocks follow performer requests
		}
		tag, err := tx.Exec(ctx, `DELETE FROM media_blocks WHERE entity_type=$1 AND entity_id=$2 AND asset_type=$3`, d.EntityType, d.EntityID, d.AssetType)
		if err != nil {
			return "", err
		}
		if tag.RowsAffected() > 0 {
			if err = writeReviewEvent(ctx, tx, d.EntityType, d.EntityID, "media_unblock", map[string]any{"asset_type": d.AssetType, "blocked": true},
				map[string]any{"asset_type": d.AssetType, "blocked": false}, note, actor); err != nil {
				return "", err
			}
		}
		return "image_purged_unblocked", nil
	}
	return "unknown_target", nil
}

// ReleaseTakenDown reports whether slug names a release that is unpublished
// because of an active copyright takedown (public page: 451, not 404).
func (s *Store) ReleaseTakenDown(ctx context.Context, slug string) (bool, error) {
	var down bool
	err := s.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM releases r JOIN takedown_targets t ON t.target_type='release' AND t.target_id=r.id AND t.state='active'
		WHERE r.slug=$1 AND r.status='unpublished' AND r.merged_into_id IS NULL)`, slug).Scan(&down)
	return down, err
}
