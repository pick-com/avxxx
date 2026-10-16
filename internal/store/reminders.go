package store

// Notification timing settings (PRD 17, migration 037):
//   - reservation_timing day_before | both: a reminder one day before an
//     upcoming release's scheduled_at (in-app kind "reservation", plus
//     push / email per the reservation prefs). day_before then skips the
//     go-live notice (notifications.go).
//   - notify_daily_cap: the fan-out opens a new outbox slot per channel and
//     local day once the previous slot was sent, up to the cap (SlotKey).
//   - users with a pending account deletion get no notices.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReasonReminder marks a day-before reservation reminder item.
const ReasonReminder = "reservation_reminder"

// reminderLead is how long before scheduled_at the reminder goes out.
const reminderLead = 24 * time.Hour

// SlotKey is the outbox merged_key of a daily slot: slot 0 is the day key
// itself (unchanged from before the cap existed), later slots get "#n".
func SlotKey(dayKey string, slot int) string {
	if slot <= 0 {
		return dayKey
	}
	return fmt.Sprintf("%s#%d", dayKey, slot)
}

// dropDeletingUsers removes recipients whose account deletion is pending.
func dropDeletingUsers(ctx context.Context, tx pgx.Tx, recipients map[int64]*Recipient) error {
	if len(recipients) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(recipients))
	for uid := range recipients {
		ids = append(ids, uid)
	}
	gone, err := collectRows(ctx, tx, `SELECT id FROM users WHERE id=ANY($1) AND deletion_requested_at IS NOT NULL`, []any{ids},
		func(r pgx.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
	for _, id := range gone {
		delete(recipients, id)
	}
	return err
}

// remindedUsers are the users who already got the day-before reminder for a release.
func remindedUsers(ctx context.Context, tx pgx.Tx, releaseID int64) (map[int64]bool, error) {
	ids, err := collectRows(ctx, tx, `SELECT user_id FROM reservation_reminders WHERE release_id=$1`, []any{releaseID},
		func(r pgx.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, err
}

// RemindReservations sends up to limit day-before reminders for upcoming
// releases going live within reminderLead of now. Each (user, release) is
// reminded once (reservation_reminders); it is safe to run concurrently.
func (s *Store) RemindReservations(ctx context.Context, now time.Time, limit int) (int, error) {
	type due struct {
		uid, rid    int64
		slug, title string
	}
	rows, err := collectRows(ctx, s, `SELECT x.user_id, r.id, r.slug, r.canonical_title
		FROM reservations x
		JOIN releases r ON r.id=x.release_id
		JOIN user_settings us ON us.user_id=x.user_id
		JOIN users u ON u.id=x.user_id
		WHERE r.status='upcoming' AND r.merged_into_id IS NULL AND r.scheduled_at IS NOT NULL
		  AND r.scheduled_at > $1 AND r.scheduled_at <= $1 + $2::interval
		  AND us.reservation_timing IN ('day_before','both')
		  AND u.deletion_requested_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM reservation_reminders rr WHERE rr.user_id=x.user_id AND rr.release_id=x.release_id)
		ORDER BY r.scheduled_at, x.user_id LIMIT $3`, []any{now, fmt.Sprintf("%d seconds", int64(reminderLead/time.Second)), limit},
		func(r pgx.Rows) (due, error) {
			var d due
			return d, r.Scan(&d.uid, &d.rid, &d.slug, &d.title)
		})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, d := range rows {
		ok, err := s.remindOne(ctx, d.uid, d.rid, d.slug, d.title, now)
		if err != nil {
			return sent, err
		}
		if ok {
			sent++
		}
	}
	return sent, nil
}

func (s *Store) remindOne(ctx context.Context, uid, rid int64, slug, title string, now time.Time) (bool, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO reservation_reminders(user_id,release_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, uid, rid)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	u, err := scanSettings(tx.QueryRow(ctx, `SELECT `+settingsColumns+` FROM user_settings WHERE user_id=$1`, uid))
	if err != nil {
		return false, err
	}
	item, _ := json.Marshal(map[string]any{"release_id": rid, "slug": slug, "title": title, "reason": ReasonReminder, "performers": []any{}})
	if u.NotifyPrefs.Enabled("reservation", "in_app") {
		if _, err = tx.Exec(ctx, `INSERT INTO notifications(user_id,kind,payload) VALUES($1,'reservation',jsonb_build_object('items',jsonb_build_array($2::jsonb),'count',1))`,
			uid, item); err != nil {
			return false, err
		}
	}
	sendAfter := QuietUntil(now, u.Location(), u.QuietHours)
	for _, ch := range []string{"push", "email"} {
		if !u.NotifyPrefs.Enabled("reservation", ch) {
			continue
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notification_outbox(user_id,channel,payload,send_after)
			VALUES($1,$2,jsonb_build_object('kind',$3::text,'items',jsonb_build_array($4::jsonb),'count',1),$5)`,
			uid, ch, ReasonReminder, item, sendAfter); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}
