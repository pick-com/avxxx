package store

// Web Push subscriptions and the notification_outbox sender side
// (migration 018; PRD 17, tech plan 3.7). Rows are written by NotifyFanout
// and delivered by cmd/notifier (internal/notify).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Outbox statuses (notification_outbox.status).
const (
	OutboxPending   = "pending"
	OutboxSending   = "sending"
	OutboxSent      = "sent"
	OutboxFailed    = "failed"
	OutboxCancelled = "cancelled"
)

// PushSubscription is one push_subscriptions row.
type PushSubscription struct {
	ID       int64
	UserID   int64
	Endpoint string
	P256dh   string
	Auth     string
}

// SavePushSubscription stores (or refreshes) a subscription for userID. An
// endpoint is unique: subscribing again from another account moves it, and
// new keys reset the failure count.
func (s *Store) SavePushSubscription(ctx context.Context, userID int64, endpoint, p256dh, auth, userAgent string) error {
	_, err := s.Exec(ctx, `INSERT INTO push_subscriptions(user_id,endpoint,p256dh,auth,user_agent) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT (endpoint) DO UPDATE SET user_id=EXCLUDED.user_id, p256dh=EXCLUDED.p256dh, auth=EXCLUDED.auth,
		  user_agent=EXCLUDED.user_agent, failures=0`, userID, endpoint, p256dh, auth, userAgent)
	return err
}

// DeletePushSubscription removes the user's subscription for endpoint, or
// every subscription of the user when endpoint is "". Returns rows removed.
func (s *Store) DeletePushSubscription(ctx context.Context, userID int64, endpoint string) (int64, error) {
	tag, err := s.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id=$1 AND ($2='' OR endpoint=$2)`, userID, endpoint)
	return tag.RowsAffected(), err
}

// PushSubscriptions lists the user's subscriptions, oldest first.
func (s *Store) PushSubscriptions(ctx context.Context, userID int64) ([]PushSubscription, error) {
	return collectRows(ctx, s, `SELECT id,user_id,endpoint,p256dh,auth FROM push_subscriptions WHERE user_id=$1 ORDER BY id`,
		[]any{userID}, func(r pgx.Rows) (PushSubscription, error) {
			var p PushSubscription
			return p, r.Scan(&p.ID, &p.UserID, &p.Endpoint, &p.P256dh, &p.Auth)
		})
}

// PushDelivered records a successful delivery.
func (s *Store) PushDelivered(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `UPDATE push_subscriptions SET last_success_at=now(), failures=0 WHERE id=$1`, id)
	return err
}

// PushGone deletes a subscription the push service reported as expired (404/410).
func (s *Store) PushGone(ctx context.Context, id int64) error {
	_, err := s.Exec(ctx, `DELETE FROM push_subscriptions WHERE id=$1`, id)
	return err
}

// PushFailed counts a failed delivery and deletes the subscription once it
// reaches maxFailures consecutive failures. Reports whether it was deleted.
func (s *Store) PushFailed(ctx context.Context, id int64, maxFailures int) (bool, error) {
	var failures int
	err := s.QueryRow(ctx, `UPDATE push_subscriptions SET failures=failures+1 WHERE id=$1 RETURNING failures`, id).Scan(&failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil || maxFailures <= 0 || failures < maxFailures {
		return false, err
	}
	return true, s.PushGone(ctx, id)
}

// OutboxItem is one claimed notification_outbox row plus what the sender
// needs to know about the recipient.
type OutboxItem struct {
	ID        int64
	UserID    int64
	Channel   string
	Payload   json.RawMessage
	Attempt   int // including the current one
	CreatedAt time.Time
	ClaimedAt time.Time // lease token: Finish only applies while it matches

	Email       string
	DisplayName string
	Settings    UserSettings
}

// ReclaimStaleOutbox returns rows stuck in 'sending' for longer than lease
// (crashed sender) to 'pending'.
func (s *Store) ReclaimStaleOutbox(ctx context.Context, lease time.Duration) (int64, error) {
	tag, err := s.Exec(ctx, `UPDATE notification_outbox SET status='pending', claimed_at=NULL
		WHERE status='sending' AND claimed_at < now() - $1::interval`, fmt.Sprintf("%d seconds", int64(lease/time.Second)))
	return tag.RowsAffected(), err
}

// ClaimOutbox atomically moves up to limit due pending rows of the given
// channels to 'sending' (FOR UPDATE SKIP LOCKED: safe with several senders)
// and loads each recipient's email and settings.
func (s *Store) ClaimOutbox(ctx context.Context, channels []string, limit int) ([]OutboxItem, error) {
	if limit <= 0 {
		limit = 50
	}
	items, err := collectRows(ctx, s, `WITH due AS (
		  SELECT id FROM notification_outbox
		  WHERE status='pending' AND send_after<=now() AND channel=ANY($1)
		  ORDER BY send_after, id LIMIT $2 FOR UPDATE SKIP LOCKED)
		UPDATE notification_outbox o SET status='sending', attempt=o.attempt+1, claimed_at=clock_timestamp()
		FROM due WHERE o.id=due.id
		RETURNING o.id,o.user_id,o.channel,o.payload,o.attempt,o.created_at,o.claimed_at`,
		[]any{channels, limit}, func(r pgx.Rows) (OutboxItem, error) {
			var it OutboxItem
			return it, r.Scan(&it.ID, &it.UserID, &it.Channel, &it.Payload, &it.Attempt, &it.CreatedAt, &it.ClaimedAt)
		})
	if err != nil || len(items) == 0 {
		return items, err
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.UserID
	}
	type user struct{ email, name string }
	users := map[int64]user{}
	rows, err := s.Query(ctx, `SELECT id,email,display_name FROM users WHERE id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var u user
		if err := rows.Scan(&id, &u.email, &u.name); err != nil {
			rows.Close()
			return nil, err
		}
		users[id] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	settings := map[int64]UserSettings{}
	rows, err = s.Query(ctx, `SELECT user_id,`+settingsColumns+` FROM user_settings WHERE user_id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var uid int64
		u, err := scanSettings(prefixScanner{rows, &uid})
		if err != nil {
			rows.Close()
			return nil, err
		}
		settings[uid] = u
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range items {
		u := users[items[i].UserID]
		items[i].Email, items[i].DisplayName = u.email, u.name
		st, ok := settings[items[i].UserID]
		if !ok {
			st = DefaultSettings()
		}
		items[i].Settings = st
	}
	return items, nil
}

// OutboxResult is how a claimed row ends.
type OutboxResult struct {
	Status  string    // OutboxSent, OutboxPending (retry), OutboxFailed, OutboxCancelled
	Error   string    // last_error ("" for sent)
	RetryAt time.Time // send_after for a retry
}

// FinishOutbox applies r to a claimed row. It is a no-op (false) when the
// lease was lost (row reclaimed by ReclaimStaleOutbox and claimed again).
func (s *Store) FinishOutbox(ctx context.Context, it OutboxItem, r OutboxResult) (bool, error) {
	var sql string
	if r.Status == OutboxSent {
		r.Error = ""
	}
	args := []any{it.ID, it.ClaimedAt, truncate(r.Error, 500)}
	switch r.Status {
	case OutboxSent:
		sql = `UPDATE notification_outbox SET status='sent', sent_at=now(), last_error=$3, claimed_at=NULL`
	case OutboxPending:
		sql = `UPDATE notification_outbox SET status='pending', send_after=$4, last_error=$3, claimed_at=NULL`
		args = append(args, r.RetryAt)
	case OutboxFailed, OutboxCancelled:
		sql = `UPDATE notification_outbox SET status='` + r.Status + `', last_error=$3, claimed_at=NULL`
	default:
		return false, fmt.Errorf("finish outbox: bad status %q", r.Status)
	}
	tag, err := s.Exec(ctx, sql+` WHERE id=$1 AND status='sending' AND claimed_at=$2`, args...)
	return tag.RowsAffected() == 1, err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // do not split a UTF-8 sequence
		n--
	}
	return s[:n]
}
