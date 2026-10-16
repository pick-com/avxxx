package store

// In-app notifications and the new-work fan-out (PRD 17, tech plan 3.7).
//
// NotifyFanout is the single entry point the scheduler calls every tick:
//  1. upcoming releases whose scheduled_at has passed become published;
//  2. every published release with notified_at IS NULL is processed once,
//     each in its own transaction claimed with FOR UPDATE SKIP LOCKED (safe to
//     run from several schedulers); followers (per notify_mode) and
//     reservation holders get one notice per release (notification_receipts),
//     merged into one in-app notification per user per local day
//     (merged_key = "<user>:<YYYY-MM-DD>"), plus one pending outbox row per
//     enabled push/email channel per day, delayed past quiet hours.
//
// In-app notices are immediate; quiet hours only move outbox send_after.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notification kinds stored in notifications.kind.
const KindNewWork = "new_work"

// Reasons a user is told about a release.
const (
	ReasonFollow      = "follow"
	ReasonReservation = "reservation"
)

// fanoutFreshness: follow notices are only sent for releases dated within this
// window (or undated), so publishing back-catalog drafts does not spam
// followers. Reservation holders are always notified.
const fanoutFreshness = 30 * 24 * time.Hour

// maxMergedItems caps the release list kept in one merged notification; the
// count keeps growing.
const maxMergedItems = 20

// Credit is one release_performers row as the fan-out sees it.
type Credit struct {
	PerformerID int64
	Slug, Name  string
	IsPrimary   bool
	Role        string
}

// FollowRow is one follows row for a credited performer.
type FollowRow struct {
	UserID, PerformerID int64
	NotifyMode          string
}

// Recipient is a user to notify about one release.
type Recipient struct {
	UserID int64
	Reason string  // ReasonReservation wins over ReasonFollow
	Via    []int64 // followed performer ids that triggered a follow notice
}

// Delivery is what to send one recipient.
type Delivery struct {
	InApp     bool
	Channels  []string // "push", "email"
	SendAfter time.Time
	MergedKey string
}

var leadRoles = map[string]bool{"lead": true, "main": true, "starring": true, "primary": true, "主演": true}

// IsLeadCredit reports whether a credit is a lead role: is_primary, or a role
// naming a lead.
func IsLeadCredit(c Credit) bool {
	return c.IsPrimary || leadRoles[strings.ToLower(strings.TrimSpace(c.Role))]
}

// LeadPerformers returns the performer ids that count as leads for lead_only
// follows. release_performers has is_primary and role; when no credit on the
// release is marked as a lead (unknown billing), every performer is treated as
// a lead rather than silently dropping lead_only followers.
func LeadPerformers(credits []Credit) map[int64]bool {
	leads := map[int64]bool{}
	for _, c := range credits {
		if IsLeadCredit(c) {
			leads[c.PerformerID] = true
		}
	}
	if len(leads) == 0 {
		for _, c := range credits {
			leads[c.PerformerID] = true
		}
	}
	return leads
}

// ShouldNotifyFollow applies a follow's notify_mode to one credit.
func ShouldNotifyFollow(mode string, isLead bool) bool {
	switch mode {
	case NotifyAll:
		return true
	case NotifyLeadOnly:
		return isLead
	}
	return false
}

// FanoutRecipients merges follow- and reservation-based recipients for one
// release: one entry per user. followsFresh=false (old release) drops follow
// notices but keeps reservations.
func FanoutRecipients(credits []Credit, follows []FollowRow, reservers []int64, followsFresh bool) map[int64]*Recipient {
	out := map[int64]*Recipient{}
	if followsFresh {
		leads := LeadPerformers(credits)
		credited := map[int64]bool{}
		for _, c := range credits {
			credited[c.PerformerID] = true
		}
		for _, f := range follows {
			if !credited[f.PerformerID] || !ShouldNotifyFollow(f.NotifyMode, leads[f.PerformerID]) {
				continue
			}
			r := out[f.UserID]
			if r == nil {
				r = &Recipient{UserID: f.UserID, Reason: ReasonFollow}
				out[f.UserID] = r
			}
			r.Via = append(r.Via, f.PerformerID)
		}
	}
	for _, uid := range reservers {
		r := out[uid]
		if r == nil {
			r = &Recipient{UserID: uid}
			out[uid] = r
		}
		r.Reason = ReasonReservation
	}
	return out
}

// FreshForFollowers reports whether a release is recent enough for follow notices.
func FreshForFollowers(releaseDate *time.Time, now time.Time) bool {
	return releaseDate == nil || !releaseDate.Before(now.Add(-fanoutFreshness))
}

// MergedKey is the per-user, per-local-day merge key "<user>:<YYYY-MM-DD>".
func MergedKey(userID int64, now time.Time, loc *time.Location) string {
	return fmt.Sprintf("%d:%s", userID, now.In(loc).Format(time.DateOnly))
}

// QuietUntil returns now if now is outside the quiet window (in loc), else the
// end of the window. A nil window never delays.
func QuietUntil(now time.Time, loc *time.Location, qh *QuietHours) time.Time {
	if qh == nil {
		return now
	}
	start, ok1 := parseClock(qh.Start)
	end, ok2 := parseClock(qh.End)
	if !ok1 || !ok2 || start == end {
		return now
	}
	t := now.In(loc)
	m := t.Hour()*60 + t.Minute()
	var in bool
	if start < end {
		in = m >= start && m < end
	} else {
		in = m >= start || m < end
	}
	if !in {
		return now
	}
	day := t.Day()
	if start > end && m >= start { // window crosses midnight; it ends tomorrow
		day++
	}
	return time.Date(t.Year(), t.Month(), day, end/60, end%60, 0, 0, loc)
}

// PlanDelivery decides channels for one recipient from their settings.
// Reservation notices use the "reservation" prefs, follow notices "new_work".
func PlanDelivery(u UserSettings, userID int64, reason string, now time.Time) Delivery {
	kind := "new_work"
	if reason == ReasonReservation {
		kind = "reservation"
	}
	loc := u.Location()
	d := Delivery{
		InApp:     u.NotifyPrefs.Enabled(kind, "in_app"),
		SendAfter: QuietUntil(now, loc, u.QuietHours),
		MergedKey: MergedKey(userID, now, loc),
	}
	for _, ch := range []string{"push", "email"} {
		if u.NotifyPrefs.Enabled(kind, ch) {
			d.Channels = append(d.Channels, ch)
		}
	}
	return d
}

// FanoutResult summarizes one NotifyFanout run.
type FanoutResult struct {
	Promoted      int64 `json:"promoted"`
	Releases      int   `json:"releases"`
	Notifications int   `json:"notifications"`
	Outbox        int   `json:"outbox"`
	Reminders     int   `json:"reminders"` // day-before reservation reminders (reminders.go)
}

// PromoteDueReleases publishes upcoming releases whose scheduled_at has passed.
func (s *Store) PromoteDueReleases(ctx context.Context) (int64, error) {
	tag, err := s.Exec(ctx, `UPDATE releases SET status='published', updated_at=now()
		WHERE status='upcoming' AND scheduled_at IS NOT NULL AND scheduled_at<=now() AND merged_into_id IS NULL`)
	return tag.RowsAffected(), err
}

// NotifyFanout promotes due releases and fans out up to maxReleases pending
// releases. It is idempotent and safe to call concurrently.
func (s *Store) NotifyFanout(ctx context.Context, maxReleases int) (FanoutResult, error) {
	var res FanoutResult
	var err error
	if res.Promoted, err = s.PromoteDueReleases(ctx); err != nil {
		return res, fmt.Errorf("promote due releases: %w", err)
	}
	if res.Reminders, err = s.RemindReservations(ctx, time.Now(), 500); err != nil {
		return res, fmt.Errorf("reservation reminders: %w", err)
	}
	if maxReleases <= 0 {
		maxReleases = 100
	}
	for res.Releases < maxReleases && ctx.Err() == nil {
		done, n, o, err := s.fanoutOne(ctx)
		if err != nil {
			return res, err
		}
		if !done {
			break
		}
		res.Releases++
		res.Notifications += n
		res.Outbox += o
	}
	return res, nil
}

// fanoutOne claims and processes one pending release. done=false means none was pending.
func (s *Store) fanoutOne(ctx context.Context) (done bool, notified, outbox int, err error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return false, 0, 0, err
	}
	defer tx.Rollback(ctx)
	var releaseID int64
	var slug, title string
	var releaseDate *time.Time
	err = tx.QueryRow(ctx, `SELECT id,slug,canonical_title,release_date FROM releases
		WHERE status='published' AND notified_at IS NULL AND merged_into_id IS NULL
		ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&releaseID, &slug, &title, &releaseDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, 0, 0, nil
	}
	if err != nil {
		return false, 0, 0, fmt.Errorf("claim release: %w", err)
	}
	now := time.Now()

	credits, err := collectRows(ctx, tx, `SELECT rp.performer_id,p.slug,p.canonical_name,rp.is_primary,COALESCE(rp.role,'')
		FROM release_performers rp JOIN performers p ON p.id=rp.performer_id WHERE rp.release_id=$1`, []any{releaseID},
		func(r pgx.Rows) (Credit, error) {
			var c Credit
			return c, r.Scan(&c.PerformerID, &c.Slug, &c.Name, &c.IsPrimary, &c.Role)
		})
	if err != nil {
		return false, 0, 0, fmt.Errorf("credits: %w", err)
	}
	pids := make([]int64, len(credits))
	byID := map[int64]Credit{}
	for i, c := range credits {
		pids[i] = c.PerformerID
		byID[c.PerformerID] = c
	}
	follows, err := collectRows(ctx, tx, `SELECT user_id,performer_id,notify_mode FROM follows WHERE performer_id=ANY($1)`, []any{pids},
		func(r pgx.Rows) (FollowRow, error) {
			var f FollowRow
			return f, r.Scan(&f.UserID, &f.PerformerID, &f.NotifyMode)
		})
	if err != nil {
		return false, 0, 0, fmt.Errorf("follows: %w", err)
	}
	reservers, err := collectRows(ctx, tx, `SELECT user_id FROM reservations WHERE release_id=$1`, []any{releaseID},
		func(r pgx.Rows) (int64, error) {
			var id int64
			return id, r.Scan(&id)
		})
	if err != nil {
		return false, 0, 0, fmt.Errorf("reservations: %w", err)
	}

	recipients := FanoutRecipients(credits, follows, reservers, FreshForFollowers(releaseDate, now))
	if err := dropDeletingUsers(ctx, tx, recipients); err != nil {
		return false, 0, 0, fmt.Errorf("deleting users: %w", err)
	}
	settings, err := s.settingsFor(ctx, tx, recipients)
	if err != nil {
		return false, 0, 0, fmt.Errorf("settings: %w", err)
	}
	reminded, err := remindedUsers(ctx, tx, releaseID)
	if err != nil {
		return false, 0, 0, fmt.Errorf("reminders: %w", err)
	}
	for uid, rc := range recipients {
		var performers []map[string]string
		for _, pid := range rc.Via {
			performers = append(performers, map[string]string{"slug": byID[pid].Slug, "name": byID[pid].Name})
		}
		item, _ := json.Marshal(map[string]any{"release_id": releaseID, "slug": slug, "title": title,
			"reason": rc.Reason, "performers": performers})
		tag, err := tx.Exec(ctx, `INSERT INTO notification_receipts(user_id,release_id,reason) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, uid, releaseID, rc.Reason)
		if err != nil {
			return false, 0, 0, fmt.Errorf("receipt: %w", err)
		}
		if tag.RowsAffected() == 0 {
			continue // already told about this release
		}
		// reservation_timing=day_before: the day-before reminder replaces the
		// go-live notice (when one was sent; undated releases still notify).
		if rc.Reason == ReasonReservation && settings[uid].ReservationTiming == "day_before" && reminded[uid] {
			continue
		}
		d := PlanDelivery(settings[uid], uid, rc.Reason, now)
		if d.InApp {
			var nid int64
			err = tx.QueryRow(ctx, `INSERT INTO notifications(user_id,kind,payload,merged_key)
				VALUES($1,$2,jsonb_build_object('items',jsonb_build_array($3::jsonb),'count',1),$4)
				ON CONFLICT (user_id,merged_key) WHERE merged_key IS NOT NULL DO UPDATE SET
				  payload=jsonb_build_object(
				    'items', CASE WHEN jsonb_array_length(COALESCE(notifications.payload->'items','[]'::jsonb)) < $5
				                  THEN COALESCE(notifications.payload->'items','[]'::jsonb) || jsonb_build_array($3::jsonb)
				                  ELSE notifications.payload->'items' END,
				    'count', COALESCE((notifications.payload->>'count')::int,0)+1),
				  read_at=NULL, updated_at=now()
				RETURNING id`, uid, KindNewWork, item, d.MergedKey, maxMergedItems).Scan(&nid)
			if err != nil {
				return false, 0, 0, fmt.Errorf("notification: %w", err)
			}
			if _, err = tx.Exec(ctx, `UPDATE notification_receipts SET notification_id=$3 WHERE user_id=$1 AND release_id=$2`, uid, releaseID, nid); err != nil {
				return false, 0, 0, err
			}
			notified++
		}
		for _, ch := range d.Channels {
			// notify_daily_cap: slot 0 is the day's merged row; once a slot was
			// sent (no longer pending) the next slot opens, up to the cap.
			for slot := 0; slot < max(settings[uid].NotifyDailyCap, 1); slot++ {
				tag, err := tx.Exec(ctx, `INSERT INTO notification_outbox(user_id,channel,payload,merged_key,send_after)
					VALUES($1,$2,jsonb_build_object('kind',$3::text,'items',jsonb_build_array($4::jsonb),'count',1),$5,$6)
					ON CONFLICT (user_id,channel,merged_key) WHERE merged_key IS NOT NULL DO UPDATE SET
					  payload=jsonb_build_object('kind',$3::text,
					    'items', CASE WHEN jsonb_array_length(COALESCE(notification_outbox.payload->'items','[]'::jsonb)) < $7
					                  THEN COALESCE(notification_outbox.payload->'items','[]'::jsonb) || jsonb_build_array($4::jsonb)
					                  ELSE notification_outbox.payload->'items' END,
					    'count', COALESCE((notification_outbox.payload->>'count')::int,0)+1)
					WHERE notification_outbox.status='pending'`, uid, ch, KindNewWork, item, SlotKey(d.MergedKey, slot), d.SendAfter, maxMergedItems)
				if err != nil {
					return false, 0, 0, fmt.Errorf("outbox: %w", err)
				}
				if n := tag.RowsAffected(); n > 0 {
					outbox += int(n)
					break
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE releases SET notified_at=now() WHERE id=$1`, releaseID); err != nil {
		return false, 0, 0, err
	}
	return true, notified, outbox, tx.Commit(ctx)
}

// settingsFor loads settings for every recipient, defaulting missing rows.
func (s *Store) settingsFor(ctx context.Context, tx pgx.Tx, recipients map[int64]*Recipient) (map[int64]UserSettings, error) {
	out := make(map[int64]UserSettings, len(recipients))
	ids := make([]int64, 0, len(recipients))
	for uid := range recipients {
		ids = append(ids, uid)
		out[uid] = DefaultSettings()
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT user_id,`+settingsColumns+` FROM user_settings WHERE user_id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid int64
		u, err := scanSettings(prefixScanner{rows, &uid})
		if err != nil {
			return nil, err
		}
		out[uid] = u
	}
	return out, rows.Err()
}

// prefixScanner scans a leading column into first, then the rest into dest.
type prefixScanner struct {
	row   rowScanner
	first any
}

func (p prefixScanner) Scan(dest ...any) error {
	return p.row.Scan(append([]any{p.first}, dest...)...)
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// collectRows runs sql on q and drains the rows through scan.
func collectRows[T any](ctx context.Context, q querier, sql string, args []any, scan func(pgx.Rows) (T, error)) ([]T, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// In-app notification API

// NotificationSort is the cursor sort tag of the notification list.
const NotificationSort = "notif"

// Notifications lists the user's notifications, newest first.
func (s *Store) Notifications(ctx context.Context, userID int64, c *Cursor, limit int) ([]map[string]any, string, error) {
	limit = pageLimit(limit)
	var before int64
	if c != nil {
		if c.Sort != NotificationSort {
			return nil, "", invalidQuery("cursor does not match notifications")
		}
		before = c.ID
	}
	rows, err := s.Query(ctx, `SELECT id,kind,payload,read_at,created_at,updated_at FROM notifications
		WHERE user_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT $3`, userID, before, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []map[string]any{}
	var ids []int64
	for rows.Next() {
		var id int64
		var kind string
		var payload json.RawMessage
		var readAt *time.Time
		var created, updated time.Time
		if err := rows.Scan(&id, &kind, &payload, &readAt, &created, &updated); err != nil {
			return nil, "", err
		}
		out = append(out, map[string]any{"id": id, "kind": kind, "payload": payload, "read": readAt != nil,
			"read_at": readAt, "created_at": created, "updated_at": updated})
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: NotificationSort, ID: ids[limit-1]})
	}
	return out, next, nil
}

// UnreadNotifications counts unread notifications.
func (s *Store) UnreadNotifications(ctx context.Context, userID int64) (int64, error) {
	var n int64
	err := s.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id=$1 AND read_at IS NULL`, userID).Scan(&n)
	return n, err
}

// MarkNotificationsRead marks ids (or every unread notification when all) as
// read and returns how many changed. Ids of other users are ignored.
func (s *Store) MarkNotificationsRead(ctx context.Context, userID int64, ids []int64, all bool) (int64, error) {
	if all {
		tag, err := s.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`, userID)
		return tag.RowsAffected(), err
	}
	tag, err := s.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE user_id=$1 AND id=ANY($2) AND read_at IS NULL`, userID, ids)
	return tag.RowsAffected(), err
}
