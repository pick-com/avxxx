package store

// Moderation (PRD 5.5, 11 AdminOps "弹幕与短评审核、举报（含时限）、刷分识别";
// tech plan 3.8 / A7): the queue, moderator actions, the keyword list and
// mutes. Every change writes a review_events row with the admin actor.

import (
	"avxxx/internal/community"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrInvalidAction: the action does not apply to the target.
	ErrInvalidAction = errors.New("invalid moderation action")
	// ErrReportClosed: the report is already resolved or dismissed.
	ErrReportClosed = errors.New("report already closed")
)

// Queue kinds.
const (
	QueueReports      = "reports"
	QueueDanmaku      = "danmaku"
	QueueReviews      = "reviews"
	QueueRatingFloods = "rating_floods"
	QueueComments     = "comments"
)

// ValidQueueKind reports whether k is a moderation queue.
func ValidQueueKind(k string) bool {
	return k == QueueReports || k == QueueDanmaku || k == QueueReviews || k == QueueRatingFloods || k == QueueComments
}

const (
	defaultQueueLimit = 50
	maxQueueLimit     = 200
	maxQueueOffset    = 10000
)

// QueueFilter selects one page of a moderation queue.
type QueueFilter struct {
	Kind   string
	Status string // danmaku / reviews: content status (default shadow); reports: open (default), resolved, dismissed
	Limit  int
	Cursor string // opaque; offset-based
}

// QueueCounts is the size of each queue (the AdminOps tab badges).
type QueueCounts struct {
	Reports        int `json:"reports"`
	ReportsOverdue int `json:"reports_overdue"`
	Danmaku        int `json:"danmaku"`
	Reviews        int `json:"reviews"`
	RatingFloods   int `json:"rating_floods"`
	Comments       int `json:"comments"`
}

// ModerationCounts counts open work in every queue.
func (s *Store) ModerationCounts(ctx context.Context) (QueueCounts, error) {
	var c QueueCounts
	err := s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM reports WHERE status='open'),
		(SELECT count(*) FROM reports WHERE status='open' AND due_at<now()),
		(SELECT count(*) FROM danmaku WHERE status='shadow'),
		(SELECT count(*) FROM reviews WHERE status='shadow'),
		(SELECT count(*) FROM (`+ratingFloodSQL+`) f),
		(SELECT count(*) FROM comments WHERE status='shadow')`,
		community.FloodMinVotes).Scan(&c.Reports, &c.ReportsOverdue, &c.Danmaku, &c.Reviews, &c.RatingFloods, &c.Comments)
	return c, err
}

// ratingFloodSQL finds suspected rating floods (tech plan 3.8: many identical
// scores from new or anonymous voters in a short window): low-trust votes
// (weight < 1) on one release with one score in one hour, at least $1 of them.
const ratingFloodSQL = `SELECT ra.release_id, ra.score, date_trunc('hour', ra.created_at) AS window_start,
		count(*) AS votes, count(*) FILTER (WHERE ra.user_id IS NULL) AS anonymous
	FROM ratings ra
	WHERE ra.status='active' AND ra.weight<1 AND ra.created_at > now() - interval '30 days'
	GROUP BY 1,2,3 HAVING count(*) >= $1`

// RatingFlood is one suspected flood.
type RatingFlood struct {
	ReleaseID   int64     `json:"release_id"`
	ReleaseSlug string    `json:"release_slug"`
	Score       int       `json:"score"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	Votes       int       `json:"votes"`
	Anonymous   int       `json:"anonymous"`
}

// QueuedDanmaku is a danmaku in the moderation queue.
type QueuedDanmaku struct {
	ID          int64     `json:"id"`
	ReleaseID   int64     `json:"release_id"`
	ReleaseSlug string    `json:"release_slug"`
	VersionKey  string    `json:"version_key"`
	TMs         int       `json:"t_ms"`
	Body        string    `json:"body"`
	UserID      int64     `json:"user_id"`
	Status      string    `json:"status"`
	Peak        *int      `json:"peak"` // curve value (0-255) at this moment, if the release has a curve
	OpenReports int       `json:"open_reports"`
	CreatedAt   time.Time `json:"created_at"`
}

// QueuedComment is a comment in the moderation queue.
type QueuedComment struct {
	ID          int64     `json:"id"`
	ReleaseID   int64     `json:"release_id"`
	ReleaseSlug string    `json:"release_slug"`
	ParentID    *int64    `json:"parent_id"`
	UserID      int64     `json:"user_id"`
	Body        string    `json:"body"`
	Spoiler     bool      `json:"spoiler"`
	Status      string    `json:"status"`
	Likes       int       `json:"likes"`
	OpenReports int       `json:"open_reports"`
	CreatedAt   time.Time `json:"created_at"`
}

// QueuedReview is a review in the moderation queue.
type QueuedReview struct {
	ID          int64     `json:"id"`
	ReleaseID   int64     `json:"release_id"`
	ReleaseSlug string    `json:"release_slug"`
	UserID      int64     `json:"user_id"`
	Body        string    `json:"body"`
	Spoiler     bool      `json:"spoiler"`
	Status      string    `json:"status"`
	Likes       int       `json:"likes"`
	OpenReports int       `json:"open_reports"`
	CreatedAt   time.Time `json:"created_at"`
}

func queueOffset(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	c, err := DecodeCursor(cursor)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(c.Key)
	if err != nil || n < 0 || n > maxQueueOffset || c.Sort != "queue" {
		return 0, invalidQuery("malformed cursor")
	}
	return n, nil
}

func queueNext(offset, limit, got int) string {
	if got <= limit || offset+limit > maxQueueOffset {
		return ""
	}
	return EncodeCursor(Cursor{Sort: "queue", Key: strconv.Itoa(offset + limit), ID: 1})
}

// ModerationQueue lists one queue page:
//   - reports: open reports by SLA deadline (overdue first), with a target preview;
//   - danmaku: shadowed danmaku, highest peak-curve moment first (PRD 5.5.1:
//     review the busiest moments first), then oldest;
//   - reviews: shadowed reviews, oldest first;
//   - comments: shadowed comments, oldest first;
//   - rating_floods: suspected floods, largest first.
func (s *Store) ModerationQueue(ctx context.Context, f QueueFilter) (any, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultQueueLimit
	}
	limit = min(limit, maxQueueLimit)
	offset, err := queueOffset(f.Cursor)
	if err != nil {
		return nil, "", err
	}
	switch f.Kind {
	case QueueReports:
		status := f.Status
		if status == "" {
			status = "open"
		}
		if status != "open" && status != "resolved" && status != "dismissed" {
			return nil, "", invalidQuery("status must be open, resolved or dismissed")
		}
		rows, err := s.Query(ctx, `SELECT `+reportColumns+` FROM reports WHERE status=$1
			ORDER BY CASE WHEN status='open' THEN due_at END, created_at DESC, id LIMIT $2 OFFSET $3`, status, limit+1, offset)
		if err != nil {
			return nil, "", err
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Report, error) { return scanReport(r) })
		if err != nil {
			return nil, "", err
		}
		next := queueNext(offset, limit, len(items))
		items = items[:min(len(items), limit)]
		for i := range items {
			items[i].Target = s.reportTarget(ctx, items[i].TargetType, items[i].TargetID)
		}
		return nonNil(items), next, nil
	case QueueDanmaku:
		status, err := contentStatus(f.Status)
		if err != nil {
			return nil, "", err
		}
		rows, err := s.Query(ctx, `SELECT d.id,d.release_id,r.slug,d.version_key,d.t_ms,d.body,d.user_id,d.status,
				c.data[LEAST(d.t_ms / c.bucket_ms, 199) + 1],
				(SELECT count(*) FROM reports p WHERE p.target_type='danmaku' AND p.target_id=d.id AND p.status='open'),
				d.created_at
			FROM danmaku d JOIN releases r ON r.id=d.release_id
			LEFT JOIN danmaku_curves c ON c.release_id=d.release_id AND c.version_key=d.version_key AND c.data IS NOT NULL
			WHERE d.status=$1
			ORDER BY c.data[LEAST(d.t_ms / c.bucket_ms, 199) + 1] DESC NULLS LAST, d.created_at, d.id
			LIMIT $2 OFFSET $3`, status, limit+1, offset)
		if err != nil {
			return nil, "", err
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (QueuedDanmaku, error) {
			var x QueuedDanmaku
			var peak *int16
			err := r.Scan(&x.ID, &x.ReleaseID, &x.ReleaseSlug, &x.VersionKey, &x.TMs, &x.Body, &x.UserID, &x.Status, &peak, &x.OpenReports, &x.CreatedAt)
			if peak != nil {
				v := int(*peak)
				x.Peak = &v
			}
			return x, err
		})
		if err != nil {
			return nil, "", err
		}
		next := queueNext(offset, limit, len(items))
		return nonNil(items[:min(len(items), limit)]), next, nil
	case QueueReviews:
		status, err := contentStatus(f.Status)
		if err != nil {
			return nil, "", err
		}
		rows, err := s.Query(ctx, `SELECT rv.id,rv.release_id,r.slug,rv.user_id,rv.body,rv.spoiler,rv.status,rv.likes,
				(SELECT count(*) FROM reports p WHERE p.target_type='review' AND p.target_id=rv.id AND p.status='open'),
				rv.created_at
			FROM reviews rv JOIN releases r ON r.id=rv.release_id WHERE rv.status=$1
			ORDER BY rv.created_at, rv.id LIMIT $2 OFFSET $3`, status, limit+1, offset)
		if err != nil {
			return nil, "", err
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (QueuedReview, error) {
			var x QueuedReview
			return x, r.Scan(&x.ID, &x.ReleaseID, &x.ReleaseSlug, &x.UserID, &x.Body, &x.Spoiler, &x.Status, &x.Likes, &x.OpenReports, &x.CreatedAt)
		})
		if err != nil {
			return nil, "", err
		}
		next := queueNext(offset, limit, len(items))
		return nonNil(items[:min(len(items), limit)]), next, nil
	case QueueComments:
		status, err := contentStatus(f.Status)
		if err != nil {
			return nil, "", err
		}
		rows, err := s.Query(ctx, `SELECT c.id,c.release_id,r.slug,c.parent_id,c.user_id,c.body,c.spoiler,c.status,c.likes,
				(SELECT count(*) FROM reports p WHERE p.target_type='comment' AND p.target_id=c.id AND p.status='open'),
				c.created_at
			FROM comments c JOIN releases r ON r.id=c.release_id WHERE c.status=$1
			ORDER BY c.created_at, c.id LIMIT $2 OFFSET $3`, status, limit+1, offset)
		if err != nil {
			return nil, "", err
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (QueuedComment, error) {
			var x QueuedComment
			return x, r.Scan(&x.ID, &x.ReleaseID, &x.ReleaseSlug, &x.ParentID, &x.UserID, &x.Body, &x.Spoiler, &x.Status, &x.Likes, &x.OpenReports, &x.CreatedAt)
		})
		if err != nil {
			return nil, "", err
		}
		next := queueNext(offset, limit, len(items))
		return nonNil(items[:min(len(items), limit)]), next, nil
	case QueueRatingFloods:
		rows, err := s.Query(ctx, `SELECT f.release_id,r.slug,f.score,f.window_start,f.votes,f.anonymous
			FROM (`+ratingFloodSQL+`) f JOIN releases r ON r.id=f.release_id
			ORDER BY f.votes DESC, f.window_start DESC, f.release_id LIMIT $2 OFFSET $3`, community.FloodMinVotes, limit+1, offset)
		if err != nil {
			return nil, "", err
		}
		items, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (RatingFlood, error) {
			var x RatingFlood
			err := r.Scan(&x.ReleaseID, &x.ReleaseSlug, &x.Score, &x.WindowStart, &x.Votes, &x.Anonymous)
			x.WindowEnd = x.WindowStart.Add(community.FloodWindow)
			return x, err
		})
		if err != nil {
			return nil, "", err
		}
		next := queueNext(offset, limit, len(items))
		return nonNil(items[:min(len(items), limit)]), next, nil
	}
	return nil, "", invalidQuery("unknown queue")
}

func contentStatus(s string) (string, error) {
	switch s {
	case "":
		return community.StatusShadow, nil
	case community.StatusShadow, community.StatusVisible, community.StatusFolded, community.StatusDeleted:
		return s, nil
	}
	return "", invalidQuery("status must be shadow, visible, folded or deleted")
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// reportTarget is a small preview of a report target for the queue; nil when
// the target is gone or has no preview.
func (s *Store) reportTarget(ctx context.Context, targetType string, id int64) any {
	var q string
	switch targetType {
	case "danmaku":
		q = `SELECT json_build_object('body',d.body,'status',d.status,'user_id',d.user_id,'t_ms',d.t_ms,'release_slug',r.slug)
			FROM danmaku d JOIN releases r ON r.id=d.release_id WHERE d.id=$1`
	case "review":
		q = `SELECT json_build_object('body',v.body,'status',v.status,'user_id',v.user_id,'spoiler',v.spoiler,'release_slug',r.slug)
			FROM reviews v JOIN releases r ON r.id=v.release_id WHERE v.id=$1`
	case "comment":
		q = `SELECT json_build_object('body',c.body,'status',c.status,'user_id',c.user_id,'spoiler',c.spoiler,'parent_id',c.parent_id,'release_slug',r.slug)
			FROM comments c JOIN releases r ON r.id=c.release_id WHERE c.id=$1`
	case "rating":
		q = `SELECT json_build_object('score',a.score,'status',a.status,'weight',a.weight,'release_slug',r.slug)
			FROM ratings a JOIN releases r ON r.id=a.release_id WHERE a.id=$1`
	case "release":
		q = `SELECT json_build_object('slug',slug,'title',canonical_title,'status',status) FROM releases WHERE id=$1`
	case "performer_info":
		q = `SELECT json_build_object('slug',slug,'name',canonical_name) FROM performers WHERE id=$1`
	case "source":
		q = `SELECT json_build_object('provider',provider,'label',label,'status',status,'release_id',release_id) FROM playback_sources WHERE id=$1`
	default:
		return nil
	}
	var raw json.RawMessage
	if err := s.QueryRow(ctx, q, id).Scan(&raw); err != nil {
		return nil
	}
	return raw
}

// ---------------------------------------------------------------------------
// Actions

// ModerationAction is one moderator decision on a danmaku, review or rating.
type ModerationAction struct {
	TargetType string // danmaku | review | comment | rating
	ID         int64
	Action     string // community.Action*
	MuteDays   int    // delete_mute only
	Reason     string
	Actor      string
}

// ModerationResult reports what an action changed.
type ModerationResult struct {
	TargetType      string     `json:"target_type"`
	ID              int64      `json:"id"`
	Before          string     `json:"before"`
	After           string     `json:"after"`
	ReportsResolved int64      `json:"reports_resolved"`
	MutedUntil      *time.Time `json:"muted_until,omitempty"`
	EventID         int64      `json:"event_id"`
}

func insertEvent(ctx context.Context, q dbtx, entityType string, entityID int64, action string, before, after any, reason, actor string) (int64, error) {
	var id int64
	b, a := jsonOrNil(before), jsonOrNil(after)
	err := q.QueryRow(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7) RETURNING id`, entityType, entityID, action, b, a, reason, actor).Scan(&id)
	return id, err
}

// jsonOrNil marshals v, or returns nil (SQL NULL) for a nil v.
func jsonOrNil(v any) []byte {
	if v == nil {
		return nil
	}
	b, _ := json.Marshal(v)
	return b
}

// Moderate applies a PRD 5.5 action: fold (spoiler), delete, delete + mute
// N days, approve. On ratings, delete strikes the vote and approve restores
// it. Open reports on the target are closed (approve dismisses them, other
// actions resolve them) and the curve of the release is queued.
func (s *Store) Moderate(ctx context.Context, a ModerationAction) (ModerationResult, error) {
	res := ModerationResult{TargetType: a.TargetType, ID: a.ID}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)

	var userID *int64
	var releaseID int64
	switch a.TargetType {
	case "danmaku", "review", "comment":
		after, ok := community.ActionStatus(a.Action)
		if !ok {
			return res, ErrInvalidAction
		}
		table := map[string]string{"danmaku": "danmaku", "review": "reviews", "comment": "comments"}[a.TargetType]
		var uid int64
		if err = tx.QueryRow(ctx, `SELECT status,user_id,release_id FROM `+table+` WHERE id=$1 FOR UPDATE`, a.ID).Scan(&res.Before, &uid, &releaseID); err != nil {
			return res, err
		}
		userID = &uid
		res.After = after
		extra := ""
		if a.TargetType != "danmaku" && a.Action == community.ActionFold {
			extra = ",spoiler=true"
		}
		if _, err = tx.Exec(ctx, `UPDATE `+table+` SET status=$2,updated_at=now()`+extra+` WHERE id=$1`, a.ID, after); err != nil {
			return res, err
		}
		if a.TargetType == "danmaku" {
			if err = markCurveDirty(ctx, tx, releaseID); err != nil {
				return res, err
			}
		}
	case "rating":
		var after string
		switch a.Action {
		case community.ActionDelete:
			after = "removed"
		case community.ActionApprove:
			after = "active"
		default:
			return res, ErrInvalidAction
		}
		if err = tx.QueryRow(ctx, `SELECT status,user_id,release_id FROM ratings WHERE id=$1 FOR UPDATE`, a.ID).Scan(&res.Before, &userID, &releaseID); err != nil {
			return res, err
		}
		res.After = after
		if _, err = tx.Exec(ctx, `UPDATE ratings SET status=$2,updated_at=now() WHERE id=$1`, a.ID, after); err != nil {
			return res, err
		}
	default:
		return res, ErrInvalidAction
	}

	if a.Action == community.ActionDeleteMute {
		if userID == nil {
			return res, ErrInvalidAction
		}
		days := a.MuteDays
		if days <= 0 {
			days = community.DefaultMuteDays
		}
		days = min(days, community.MaxMuteDays)
		until, err := muteUser(ctx, tx, *userID, days, a.Reason, a.Actor)
		if err != nil {
			return res, err
		}
		res.MutedUntil = &until
	}

	resolution, status := a.Action, "resolved"
	if a.Action == community.ActionApprove {
		status = "dismissed"
	}
	ct, err := tx.Exec(ctx, `UPDATE reports SET status=$3,resolution=$4,resolved_at=now(),resolved_by=$5
		WHERE target_type=$1 AND target_id=$2 AND status='open'`, a.TargetType, a.ID, status, resolution, a.Actor)
	if err != nil {
		return res, err
	}
	res.ReportsResolved = ct.RowsAffected()
	after := map[string]any{"status": res.After, "reports_closed": res.ReportsResolved}
	if res.MutedUntil != nil {
		after["muted_until"] = res.MutedUntil
		after["user_id"] = *userID
	}
	if res.EventID, err = insertEvent(ctx, tx, a.TargetType, a.ID, "moderation."+a.Action,
		map[string]any{"status": res.Before}, after, a.Reason, a.Actor); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// muteUser mutes for days (extending, never shortening, an active mute),
// lowers the user's votes to the abnormal weight and queues the curves of
// every release they commented on (muted users' history leaves the curve).
func muteUser(ctx context.Context, tx pgx.Tx, userID int64, days int, reason, actor string) (time.Time, error) {
	var until time.Time
	err := tx.QueryRow(ctx, `INSERT INTO user_mutes(user_id,until,reason,created_by) VALUES($1, now() + make_interval(days => $2), $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET until=GREATEST(user_mutes.until, EXCLUDED.until), reason=EXCLUDED.reason,
		created_by=EXCLUDED.created_by RETURNING until`, userID, days, reason, actor).Scan(&until)
	if err != nil {
		return until, err
	}
	if _, err = tx.Exec(ctx, `UPDATE ratings SET weight=LEAST(weight,$2),updated_at=now() WHERE user_id=$1`, userID, community.WeightAbnormal); err != nil {
		return until, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO danmaku_curve_queue(release_id,dirty_since)
		SELECT r, clock_timestamp() FROM (SELECT DISTINCT release_id AS r FROM danmaku WHERE user_id=$1) d
		ON CONFLICT (release_id) DO UPDATE SET dirty_since=EXCLUDED.dirty_since`, userID)
	return until, err
}

// Unmute lifts a mute entirely: the user's history counts for the curve
// again (vote weights stay lowered).
func (s *Store) Unmute(ctx context.Context, userID int64, reason, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var until time.Time
	if err = tx.QueryRow(ctx, `DELETE FROM user_mutes WHERE user_id=$1 RETURNING until`, userID).Scan(&until); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO danmaku_curve_queue(release_id,dirty_since)
		SELECT r, clock_timestamp() FROM (SELECT DISTINCT release_id AS r FROM danmaku WHERE user_id=$1) d
		ON CONFLICT (release_id) DO UPDATE SET dirty_since=EXCLUDED.dirty_since`, userID); err != nil {
		return err
	}
	if _, err = insertEvent(ctx, tx, "user", userID, "moderation.unmute", map[string]any{"muted_until": until}, nil, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResolveReport closes a report without touching its target (for targets
// without a content action, or to dismiss a report). status is resolved or
// dismissed.
func (s *Store) ResolveReport(ctx context.Context, id int64, status, resolution, actor string) (Report, error) {
	if status != "resolved" && status != "dismissed" {
		return Report{}, invalidQuery("status must be resolved or dismissed")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback(ctx)
	var cur string
	if err = tx.QueryRow(ctx, `SELECT status FROM reports WHERE id=$1 FOR UPDATE`, id).Scan(&cur); err != nil {
		return Report{}, err
	}
	if cur != "open" {
		return Report{}, ErrReportClosed
	}
	r, err := scanReport(tx.QueryRow(ctx, `UPDATE reports SET status=$2,resolution=$3,resolved_at=now(),resolved_by=$4
		WHERE id=$1 RETURNING `+reportColumns, id, status, resolution, actor))
	if err != nil {
		return r, err
	}
	if _, err = insertEvent(ctx, tx, "report", id, "report."+status, map[string]any{"status": cur},
		map[string]any{"status": status, "target_type": r.TargetType, "target_id": r.TargetID}, resolution, actor); err != nil {
		return r, err
	}
	return r, tx.Commit(ctx)
}

// RemoveRatingFlood strikes the low-trust votes of one suspected flood
// (release, score, the hour starting at windowStart) after a moderator
// confirmed it (PRD 19: 刷分由审核员确认后剔除).
func (s *Store) RemoveRatingFlood(ctx context.Context, releaseID int64, score int, windowStart time.Time, reason, actor string) (int64, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	ct, err := tx.Exec(ctx, `UPDATE ratings SET status='removed',updated_at=now()
		WHERE release_id=$1 AND score=$2 AND status='active' AND weight<1 AND created_at>=$3 AND created_at<$4`,
		releaseID, score, windowStart, windowStart.Add(community.FloodWindow))
	if err != nil {
		return 0, err
	}
	n := ct.RowsAffected()
	if n == 0 {
		return 0, pgx.ErrNoRows
	}
	if _, err = insertEvent(ctx, tx, "release", releaseID, "moderation.rating_flood_remove", nil,
		map[string]any{"score": score, "window_start": windowStart, "removed": n}, reason, actor); err != nil {
		return 0, err
	}
	return n, tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Keyword list

// ModerationWords lists the keyword list.
func (s *Store) ModerationWords(ctx context.Context) ([]community.Keyword, error) {
	rows, err := s.Query(ctx, `SELECT word,category FROM moderation_words ORDER BY word`)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (community.Keyword, error) {
		var k community.Keyword
		return k, r.Scan(&k.Word, &k.Category)
	})
	return nonNil(out), err
}

// AddModerationWord adds or recategorizes a word (normalized first).
func (s *Store) AddModerationWord(ctx context.Context, word, category, actor string) (community.Keyword, error) {
	k := community.Keyword{Word: community.NormalizeKeyword(word), Category: category}
	if k.Word == "" || len([]rune(k.Word)) > community.MaxKeywordRunes || !community.ValidKeywordCategory(category) {
		return k, invalidQuery("invalid word or category")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return k, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO moderation_words(word,category) VALUES($1,$2)
		ON CONFLICT (word) DO UPDATE SET category=EXCLUDED.category`, k.Word, k.Category); err != nil {
		return k, err
	}
	if _, err = insertEvent(ctx, tx, "moderation_word", 0, "moderation.word_add", nil, k, "", actor); err != nil {
		return k, err
	}
	if err = tx.Commit(ctx); err != nil {
		return k, err
	}
	s.invalidateMatcher()
	return k, nil
}

// DeleteModerationWord removes a word (pgx.ErrNoRows when absent).
func (s *Store) DeleteModerationWord(ctx context.Context, word, actor string) error {
	w := community.NormalizeKeyword(word)
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var k community.Keyword
	if err = tx.QueryRow(ctx, `DELETE FROM moderation_words WHERE word=$1 RETURNING word,category`, w).Scan(&k.Word, &k.Category); err != nil {
		return err
	}
	if _, err = insertEvent(ctx, tx, "moderation_word", 0, "moderation.word_delete", k, nil, "", actor); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	s.invalidateMatcher()
	return nil
}
