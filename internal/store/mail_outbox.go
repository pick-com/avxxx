package store

// Transactional mail to people who are not users (migration 048
// mail_outbox): today the claimant and the counter-notifier of a copyright
// takedown (docs/COMPLIANCE.md 4). Rows are written in the same transaction
// as the ticket change and name a recipient role, never an address; the
// address, name and language are read from takedown_requests when the row is
// claimed, so personal data lives only in the ticket row and follows its
// retention. cmd/notifier delivers the rows (internal/notify/claimant.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// mail_outbox kinds and recipient roles.
const (
	MailTakedownReceived = "takedown_received" // receipt with the ticket, to the claimant
	MailTakedownStatus   = "takedown_status"   // the ticket's status changed
	MailTakedownCounter  = "takedown_counter"  // counter-notice receipt, to the counter-notifier

	RecipientClaimant = "claimant"
	RecipientCounter  = "counter"
)

func mailPayload(status string) string {
	b, _ := json.Marshal(map[string]string{"status": status})
	return string(b)
}

// enqueueTakedownMail queues one mail for a ticket's recipient role.
func enqueueTakedownMail(ctx context.Context, tx execer, takedownID int64, recipient, kind, status string) error {
	_, err := tx.Exec(ctx, `INSERT INTO mail_outbox(takedown_id,recipient,kind,payload) VALUES($1,$2,$3,$4::jsonb)`,
		takedownID, recipient, kind, mailPayload(status))
	return err
}

// enqueueTakedownStatusMail tells the claimant, and the counter-notifier
// when there is one, that the ticket moved to status. counter_received is
// claimant-only (the counter-notifier gets its own receipt).
func enqueueTakedownStatusMail(ctx context.Context, tx execer, takedownID int64, status string) error {
	_, err := tx.Exec(ctx, `INSERT INTO mail_outbox(takedown_id,recipient,kind,payload)
		SELECT id,'claimant',$2,$3::jsonb FROM takedown_requests WHERE id=$1
		UNION ALL
		SELECT id,'counter',$2,$3::jsonb FROM takedown_requests
		WHERE id=$1 AND COALESCE(counter_email,'')<>'' AND $4<>'counter_received'`,
		takedownID, MailTakedownStatus, mailPayload(status), status)
	return err
}

// MailOutboxItem is one claimed mail_outbox row with its recipient resolved
// from the ticket (Email "" when the address is gone).
type MailOutboxItem struct {
	ID         int64
	TakedownID int64
	Recipient  string
	Kind       string
	Status     string // the status the mail reports (payload.status)
	Attempt    int    // including the current one
	CreatedAt  time.Time
	ClaimedAt  time.Time // lease token, as OutboxItem

	Ticket string
	Email  string
	Name   string
	Lang   string // the recipient's submission language ("" unknown)
}

// ReclaimStaleMailOutbox returns rows stuck in 'sending' for longer than
// lease to 'pending'.
func (s *Store) ReclaimStaleMailOutbox(ctx context.Context, lease time.Duration) (int64, error) {
	tag, err := s.Exec(ctx, `UPDATE mail_outbox SET status='pending', claimed_at=NULL
		WHERE status='sending' AND claimed_at < now() - $1::interval`, fmt.Sprintf("%d seconds", int64(lease/time.Second)))
	return tag.RowsAffected(), err
}

// ClaimMailOutbox moves up to limit due pending rows to 'sending' (FOR
// UPDATE SKIP LOCKED) and resolves each recipient from its ticket.
func (s *Store) ClaimMailOutbox(ctx context.Context, limit int) ([]MailOutboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	return collectRows(ctx, s, `WITH due AS (
		  SELECT id FROM mail_outbox WHERE status='pending' AND send_after<=now()
		  ORDER BY send_after, id LIMIT $1 FOR UPDATE SKIP LOCKED
		), claimed AS (
		  UPDATE mail_outbox o SET status='sending', attempt=o.attempt+1, claimed_at=clock_timestamp()
		  FROM due WHERE o.id=due.id
		  RETURNING o.id,o.takedown_id,o.recipient,o.kind,COALESCE(o.payload->>'status',''),o.attempt,o.created_at,o.claimed_at
		)
		SELECT c.*, COALESCE(t.ticket,''),
		  CASE c.recipient WHEN 'counter' THEN COALESCE(t.counter_email,'') ELSE COALESCE(t.claimant_email,'') END,
		  CASE c.recipient WHEN 'counter' THEN COALESCE(t.counter_name,'') ELSE COALESCE(t.claimant_name,'') END,
		  CASE c.recipient WHEN 'counter' THEN COALESCE(t.counter_lang,t.lang,'') ELSE COALESCE(t.lang,'') END
		FROM claimed c LEFT JOIN takedown_requests t ON t.id=c.takedown_id ORDER BY c.id`,
		[]any{limit}, func(r pgx.Rows) (MailOutboxItem, error) {
			var it MailOutboxItem
			return it, r.Scan(&it.ID, &it.TakedownID, &it.Recipient, &it.Kind, &it.Status, &it.Attempt, &it.CreatedAt, &it.ClaimedAt,
				&it.Ticket, &it.Email, &it.Name, &it.Lang)
		})
}

// FinishMailOutbox applies r to a claimed row; false when the lease was lost.
func (s *Store) FinishMailOutbox(ctx context.Context, it MailOutboxItem, r OutboxResult) (bool, error) {
	if r.Status == OutboxSent {
		r.Error = ""
	}
	args := []any{it.ID, it.ClaimedAt, truncate(r.Error, 500)}
	var sql string
	switch r.Status {
	case OutboxSent:
		sql = `UPDATE mail_outbox SET status='sent', sent_at=now(), last_error=$3, claimed_at=NULL`
	case OutboxPending:
		sql = `UPDATE mail_outbox SET status='pending', send_after=$4, last_error=$3, claimed_at=NULL`
		args = append(args, r.RetryAt)
	case OutboxFailed, OutboxCancelled:
		sql = `UPDATE mail_outbox SET status='` + r.Status + `', last_error=$3, claimed_at=NULL`
	default:
		return false, fmt.Errorf("finish mail outbox: bad status %q", r.Status)
	}
	tag, err := s.Exec(ctx, sql+` WHERE id=$1 AND status='sending' AND claimed_at=$2`, args...)
	return tag.RowsAffected() == 1, err
}
