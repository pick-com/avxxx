package store

// Feedback (LegalPage footer: 帮助、反馈、合作; tech plan 3.13; migration
// 041). Public intake, admin-only reads; status changes write review_events
// (entity_type 'feedback').

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Feedback is one feedback row (admin view).
type Feedback struct {
	ID        int64      `json:"id"`
	Category  string     `json:"category"`
	Message   string     `json:"message"`
	Email     string     `json:"email"`
	PageURL   string     `json:"page_url"`
	Lang      string     `json:"lang"`
	Status    string     `json:"status"`
	AdminNote string     `json:"admin_note"`
	HandledBy *string    `json:"handled_by"`
	HandledAt *time.Time `json:"handled_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// FeedbackReceipt is what the submitter gets back.
type FeedbackReceipt struct {
	ID        int64     `json:"id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// FeedbackFilter selects the admin list.
type FeedbackFilter struct {
	Status   string
	Category string
	Limit    int
	Cursor   string
}

const feedbackColumns = `id,category,message,COALESCE(email,''),COALESCE(page_url,''),COALESCE(lang,''),status,admin_note,handled_by,handled_at,created_at`

func scanFeedback(r pgx.Row) (Feedback, error) {
	var f Feedback
	return f, r.Scan(&f.ID, &f.Category, &f.Message, &f.Email, &f.PageURL, &f.Lang, &f.Status, &f.AdminNote, &f.HandledBy, &f.HandledAt, &f.CreatedAt)
}

// CreateFeedback stores a validated submission.
func (s *Store) CreateFeedback(ctx context.Context, in FeedbackInput) (FeedbackReceipt, error) {
	if err := in.Normalize(); err != nil {
		return FeedbackReceipt{}, err
	}
	var out FeedbackReceipt
	err := s.QueryRow(ctx, `INSERT INTO feedback(category,message,email,page_url,lang) VALUES($1,$2,NULLIF($3,''),NULLIF($4,''),NULLIF($5,''))
		RETURNING id,status,created_at`, in.Category, in.Message, in.Email, in.PageURL, in.Lang).Scan(&out.ID, &out.Status, &out.CreatedAt)
	return out, err
}

// AdminFeedback lists feedback newest first; the cursor is the last id.
func (s *Store) AdminFeedback(ctx context.Context, f FeedbackFilter) ([]Feedback, string, error) {
	if f.Status != "" && !slices.Contains(FeedbackStatuses, f.Status) {
		return nil, "", invalid("status must be one of %s", strings.Join(FeedbackStatuses, ", "))
	}
	if f.Category != "" && !slices.Contains(FeedbackCategories, f.Category) {
		return nil, "", invalid("category must be one of %s", strings.Join(FeedbackCategories, ", "))
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
	list, err := collect[Feedback](s.Query(ctx, `SELECT `+feedbackColumns+` FROM feedback
		WHERE ($1='' OR status=$1) AND ($2='' OR category=$2) AND ($3=0 OR id<$3) ORDER BY id DESC LIMIT $4`,
		f.Status, f.Category, after, limit+1))(func(r pgx.Rows) (Feedback, error) { return scanFeedback(r) })
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

// FeedbackOpenCount counts open and in-progress feedback (admin badge).
func (s *Store) FeedbackOpenCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.QueryRow(ctx, `SELECT count(*) FROM feedback WHERE status IN ('open','in_progress')`).Scan(&n)
	return n, err
}

// SetFeedbackStatus changes the status and admin note.
func (s *Store) SetFeedbackStatus(ctx context.Context, id int64, status, note, actor string) (Feedback, error) {
	note = strings.TrimSpace(note)
	if !slices.Contains(FeedbackStatuses, status) {
		return Feedback{}, invalid("status must be one of %s", strings.Join(FeedbackStatuses, ", "))
	}
	if len(note) > MaxReasonLen {
		return Feedback{}, invalid("note too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Feedback{}, err
	}
	defer tx.Rollback(ctx)
	before, err := scanFeedback(tx.QueryRow(ctx, `SELECT `+feedbackColumns+` FROM feedback WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return before, err
	}
	after, err := scanFeedback(tx.QueryRow(ctx, `UPDATE feedback SET status=$2,admin_note=CASE WHEN $3='' THEN admin_note ELSE $3 END,
		handled_by=$4,handled_at=now() WHERE id=$1 RETURNING `+feedbackColumns, id, status, note, actor))
	if err != nil {
		return after, err
	}
	// The message and contact stay in the row; the event records the decision.
	if err = writeReviewEvent(ctx, tx, "feedback", id, "feedback_status", map[string]any{"status": before.Status},
		map[string]any{"status": after.Status}, note, actor); err != nil {
		return after, err
	}
	return after, tx.Commit(ctx)
}

// clampAdminLimit is the admin list page size: default 50, at most 200.
func clampAdminLimit(n int) int {
	switch {
	case n <= 0:
		return 50
	case n > 200:
		return 200
	}
	return n
}
