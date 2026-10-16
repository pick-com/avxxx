package store

// 推荐反馈 (migration 051 rec_feedback): 「不感兴趣」 / 「不推荐此演员」 /
// 「不推荐此类型」 from recommendation cards, for signed-in users (u:<id>) and
// visitors (d:<hash of X-Device-ID>). Recall loads the active rows with the
// watched / 踩 filter; only the owner ever reads them back.

import (
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecFeedbackOwner is who gave the feedback: a user, else a visitor device.
type RecFeedbackOwner struct {
	UserID   int64
	DeviceID string // raw X-Device-ID (visitors only); stored hashed
}

// Key is rec_feedback.owner_key, "" when there is no owner.
func (o RecFeedbackOwner) Key() string {
	switch {
	case o.UserID > 0:
		return "u:" + strconv.FormatInt(o.UserID, 10)
	case o.DeviceID != "":
		return "d:" + community.DeviceHash(o.DeviceID)
	}
	return ""
}

func (o RecFeedbackOwner) userArg() *int64 {
	if o.UserID > 0 {
		return &o.UserID
	}
	return nil
}

// RecFeedback is one active feedback row as its owner sees it. Target is
// the release / performer slug or the normalized tag; Name its title, name
// or the tag itself.
type RecFeedback struct {
	Kind      string    `json:"kind"`
	Target    string    `json:"target"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Limits: the newest recFeedbackLoadLimit active rows feed ranking; the
// settings list shows up to RecFeedbackListLimit.
const (
	recFeedbackLoadLimit = 1000
	RecFeedbackListLimit = 500
)

// ErrBadFeedback is an unknown kind or a malformed tag.
var ErrBadFeedback = errors.New("kind must be not_interested_release, not_interested_performer or not_interested_tag with a valid target")

// recFeedbackTarget resolves a target to (id, tag). Releases must be
// published and performers active when giving feedback (resolveLive);
// undo resolves any slug so feedback on a since-hidden title can be removed.
func (s *Store) recFeedbackTarget(ctx context.Context, kind, target string, resolveLive bool) (int64, *string, error) {
	switch kind {
	case recs.FeedbackRelease:
		q := `SELECT id FROM releases WHERE slug=$1`
		if resolveLive {
			q = `SELECT id FROM releases r WHERE slug=$1 AND ` + publishedR
		}
		var id int64
		err := s.QueryRow(ctx, q, target).Scan(&id)
		return id, nil, err
	case recs.FeedbackPerformer:
		q := `SELECT id FROM performers WHERE slug=$1`
		if resolveLive {
			q = `SELECT id FROM performers p WHERE slug=$1 AND ` + activeP
		}
		var id int64
		err := s.QueryRow(ctx, q, target).Scan(&id)
		return id, nil, err
	case recs.FeedbackTag:
		if !tagSlugRe.MatchString(target) {
			return 0, nil, ErrBadFeedback
		}
		return 0, &target, nil
	}
	return 0, nil, ErrBadFeedback
}

func idArg(id int64) *int64 {
	if id > 0 {
		return &id
	}
	return nil
}

// AddRecFeedback records (or re-activates) one feedback row. Unknown
// targets are pgx.ErrNoRows.
func (s *Store) AddRecFeedback(ctx context.Context, owner RecFeedbackOwner, kind, target string) (RecFeedback, error) {
	key := owner.Key()
	if key == "" {
		return RecFeedback{}, ErrBadFeedback
	}
	id, tag, err := s.recFeedbackTarget(ctx, kind, target, true)
	if err != nil {
		return RecFeedback{}, err
	}
	out := RecFeedback{Kind: kind, Target: target, Name: target}
	err = s.QueryRow(ctx, `INSERT INTO rec_feedback (owner_key, user_id, kind, target_id, target_tag)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (owner_key, kind, (COALESCE(target_id, 0)), (COALESCE(target_tag, '')))
		DO UPDATE SET undone_at=NULL, created_at=CASE WHEN rec_feedback.undone_at IS NULL THEN rec_feedback.created_at ELSE now() END
		RETURNING created_at`, key, owner.userArg(), kind, idArg(id), tag).Scan(&out.CreatedAt)
	if err != nil {
		return RecFeedback{}, err
	}
	switch kind {
	case recs.FeedbackRelease:
		err = s.QueryRow(ctx, `SELECT canonical_title FROM releases WHERE id=$1`, id).Scan(&out.Name)
	case recs.FeedbackPerformer:
		err = s.QueryRow(ctx, `SELECT canonical_name FROM performers WHERE id=$1`, id).Scan(&out.Name)
	}
	return out, err
}

// UndoRecFeedback (撤销) marks an active row undone; false when there was
// none.
func (s *Store) UndoRecFeedback(ctx context.Context, owner RecFeedbackOwner, kind, target string) (bool, error) {
	key := owner.Key()
	if key == "" {
		return false, ErrBadFeedback
	}
	id, tag, err := s.recFeedbackTarget(ctx, kind, target, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	t, err := s.Exec(ctx, `UPDATE rec_feedback SET undone_at=now()
		WHERE owner_key=$1 AND kind=$2 AND COALESCE(target_id,0)=COALESCE($3::bigint,0) AND COALESCE(target_tag,'')=COALESCE($4::text,'')
		  AND undone_at IS NULL`, key, kind, idArg(id), tag)
	return t.RowsAffected() > 0, err
}

// ListRecFeedback is the owner's active feedback, newest first (Settings ›
// 推荐反馈). Rows whose release or performer is gone are left out.
func (s *Store) ListRecFeedback(ctx context.Context, owner RecFeedbackOwner) ([]RecFeedback, error) {
	key := owner.Key()
	if key == "" {
		return []RecFeedback{}, nil
	}
	rows, err := s.Query(ctx, `SELECT f.kind, COALESCE(r.slug, p.slug, f.target_tag), COALESCE(r.canonical_title, p.canonical_name, f.target_tag), f.created_at
		FROM rec_feedback f
		LEFT JOIN releases r ON f.kind='`+recs.FeedbackRelease+`' AND r.id=f.target_id
		LEFT JOIN performers p ON f.kind='`+recs.FeedbackPerformer+`' AND p.id=f.target_id
		WHERE f.owner_key=$1 AND f.undone_at IS NULL AND COALESCE(r.slug, p.slug, f.target_tag) IS NOT NULL
		ORDER BY f.created_at DESC, f.id DESC LIMIT `+itoa(RecFeedbackListLimit), key)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RecFeedback, error) {
		var x RecFeedback
		err := row.Scan(&x.Kind, &x.Target, &x.Name, &x.CreatedAt)
		return x, err
	})
}

// recFeedbackSQL loads the active rows Rank applies ($1 = owner_key).
var recFeedbackSQL = `SELECT kind, COALESCE(target_id,0), COALESCE(target_tag,'') FROM rec_feedback
	WHERE owner_key=$1 AND undone_at IS NULL ORDER BY created_at DESC LIMIT ` + itoa(recFeedbackLoadLimit)

func scanRecFeedback(rows pgx.Rows) (recs.Feedback, error) {
	var fb recs.Feedback
	defer rows.Close()
	for rows.Next() {
		var kind, tag string
		var id int64
		if err := rows.Scan(&kind, &id, &tag); err != nil {
			return fb, err
		}
		fb.Add(kind, id, tag)
	}
	return fb, rows.Err()
}
