package api

// Admin moderation (AdminOps / A7; PRD 5.5, 11, 19; tech plan 3.8): the
// queue (reports with SLA, shadowed danmaku and reviews, suspected rating
// floods), moderator actions, the keyword list and mutes. Every change writes
// review_events with the authenticated admin as actor (adminActor).

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// moderationStore is the subset of *store.Store used by the moderation
// handlers, as an interface so they can be tested without a database.
type moderationStore interface {
	ModerationCounts(ctx context.Context) (store.QueueCounts, error)
	ModerationQueue(ctx context.Context, f store.QueueFilter) (any, string, error)
	Moderate(ctx context.Context, a store.ModerationAction) (store.ModerationResult, error)
	ResolveReport(ctx context.Context, id int64, status, resolution, actor string) (store.Report, error)
	RemoveRatingFlood(ctx context.Context, releaseID int64, score int, windowStart time.Time, reason, actor string) (int64, error)
	ModerationWords(ctx context.Context) ([]community.Keyword, error)
	AddModerationWord(ctx context.Context, word, category, actor string) (community.Keyword, error)
	DeleteModerationWord(ctx context.Context, word, actor string) error
	Unmute(ctx context.Context, userID int64, reason, actor string) error
	CommentEdits(ctx context.Context, commentID int64) (store.CommentHistory, error)
}

const maxActorLen = 100

func (srv *Server) moderationRoutes() []route {
	return []route{
		{"GET /api/admin/moderation/queue", srv.admin(srv.moderationQueue)},
		{"POST /api/admin/moderation/danmaku/{id}/action", srv.admin(srv.moderate("danmaku"))},
		{"POST /api/admin/moderation/reviews/{id}/action", srv.admin(srv.moderate("review"))},
		{"POST /api/admin/moderation/ratings/{id}/action", srv.admin(srv.moderate("rating"))},
		{"POST /api/admin/moderation/reports/{id}/resolve", srv.admin(srv.resolveReport)},
		{"POST /api/admin/moderation/rating-floods/remove", srv.admin(srv.removeRatingFlood)},
		{"GET /api/admin/moderation/words", srv.admin(srv.listWords)},
		{"POST /api/admin/moderation/words", srv.admin(srv.addWord)},
		{"DELETE /api/admin/moderation/words/{word}", srv.admin(srv.deleteWord)},
		{"DELETE /api/admin/moderation/mutes/{user_id}", srv.admin(srv.unmute)},
		{"POST /api/admin/moderation/comments/{id}/action", srv.admin(srv.moderate("comment"))},
		{"GET /api/admin/moderation/comments/{id}/edits", srv.admin(srv.commentEdits)},
		{"POST /api/admin/moderation/clips/{id}/action", srv.admin(srv.moderate("clip"))},
	}
}

// adminActor is the review_events actor of an admin request: the signed-in
// admin's username, or "token:<label>" on the ADMIN_TOKEN break-glass path
// (admin_auth.go). The X-Admin-User header is never trusted as an actor; it
// is only the break-glass label.
func adminActor(r *http.Request) (string, error) {
	p, ok := principalFrom(r.Context())
	if !ok {
		return "", errUnauthorized
	}
	return p.Actor(), nil
}

// decodeAdminJSON reads an optional bounded JSON body (empty is allowed).
func decodeAdminJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return badRequest("invalid JSON body", err)
	}
	return nil
}

// withModeration resolves the actor and guards a missing store.
func (srv *Server) withModeration(w http.ResponseWriter, r *http.Request) (string, bool) {
	if srv.moderation == nil {
		reply(w, nil, errUnavailable)
		return "", false
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return "", false
	}
	return actor, true
}

// moderationQueue: GET /api/admin/moderation/queue?kind=reports|danmaku|reviews|rating_floods|comments|clips&status=&limit=&cursor=.
// Without kind: the counts of every queue plus the first page of each.
func (srv *Server) moderationQueue(w http.ResponseWriter, r *http.Request) {
	if _, ok := srv.withModeration(w, r); !ok {
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	kind := strings.TrimSpace(q.Get("kind"))
	ctx := r.Context()
	if kind == "" {
		counts, err := srv.moderation.ModerationCounts(ctx)
		if err != nil {
			reply(w, nil, err)
			return
		}
		out := map[string]any{"counts": counts}
		for _, k := range store.QueueKinds {
			items, next, err := srv.moderation.ModerationQueue(ctx, store.QueueFilter{Kind: k, Limit: limit})
			if err != nil {
				reply(w, nil, err)
				return
			}
			out[k] = page(items, next)
		}
		reply(w, out, nil)
		return
	}
	if !store.ValidQueueKind(kind) {
		reply(w, nil, badRequest("kind must be reports, danmaku, reviews, rating_floods, comments or clips", nil))
		return
	}
	items, next, err := srv.moderation.ModerationQueue(ctx, store.QueueFilter{
		Kind: kind, Status: strings.TrimSpace(q.Get("status")), Limit: limit, Cursor: strings.TrimSpace(q.Get("cursor")),
	})
	reply(w, page(items, next), err)
}

// moderate: POST /api/admin/moderation/{danmaku|reviews|comments|clips|ratings}/{id}/action
// {"action": "fold|delete|delete_mute|approve", "mute_days"?, "reason"?};
// clips also take "remove_title" (clear the title, keep the clip).
func (srv *Server) moderate(targetType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := srv.withModeration(w, r)
		if !ok {
			return
		}
		id, err := pathID(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		var in struct {
			Action   string `json:"action"`
			MuteDays int    `json:"mute_days"`
			Reason   string `json:"reason"`
		}
		if err := decodeAdminJSON(w, r, &in); err != nil {
			reply(w, nil, err)
			return
		}
		if err := validAction(targetType, in.Action, in.MuteDays); err != nil {
			reply(w, nil, err)
			return
		}
		if len(in.Reason) > 2000 {
			reply(w, nil, badRequest("reason too long", nil))
			return
		}
		res, err := srv.moderation.Moderate(r.Context(), store.ModerationAction{
			TargetType: targetType, ID: id, Action: in.Action, MuteDays: in.MuteDays, Reason: strings.TrimSpace(in.Reason), Actor: actor,
		})
		reply(w, res, communityErr(err))
	}
}

// validAction checks an action against its target before touching the store.
func validAction(targetType, action string, muteDays int) error {
	if targetType == "rating" {
		if action != community.ActionDelete && action != community.ActionApprove {
			return badRequest("action must be delete or approve", nil)
		}
		return nil
	}
	if targetType == "clip" && action == community.ActionRemoveTitle {
		if muteDays != 0 {
			return badRequest("mute_days only applies to delete_mute", nil)
		}
		return nil
	}
	if _, ok := community.ActionStatus(action); !ok {
		return badRequest("action must be fold, delete, delete_mute or approve", nil)
	}
	if muteDays < 0 || muteDays > community.MaxMuteDays {
		return badRequest("mute_days must be 0-365", nil)
	}
	if muteDays != 0 && action != community.ActionDeleteMute {
		return badRequest("mute_days only applies to delete_mute", nil)
	}
	return nil
}

// resolveReport: POST /api/admin/moderation/reports/{id}/resolve
// {"status": "resolved|dismissed", "resolution"?}. For danmaku, reviews and
// ratings prefer the target action, which closes every open report on it.
func (srv *Server) resolveReport(w http.ResponseWriter, r *http.Request) {
	actor, ok := srv.withModeration(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Status     string `json:"status"`
		Resolution string `json:"resolution"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Status != "resolved" && in.Status != "dismissed" {
		reply(w, nil, badRequest("status must be resolved or dismissed", nil))
		return
	}
	if len(in.Resolution) > 2000 {
		reply(w, nil, badRequest("resolution too long", nil))
		return
	}
	v, err := srv.moderation.ResolveReport(r.Context(), id, in.Status, strings.TrimSpace(in.Resolution), actor)
	reply(w, v, communityErr(err))
}

// removeRatingFlood: POST /api/admin/moderation/rating-floods/remove
// {"release_id", "score", "window_start" (RFC 3339, from the queue), "reason"?}.
func (srv *Server) removeRatingFlood(w http.ResponseWriter, r *http.Request) {
	actor, ok := srv.withModeration(w, r)
	if !ok {
		return
	}
	var in struct {
		ReleaseID   int64     `json:"release_id"`
		Score       int       `json:"score"`
		WindowStart time.Time `json:"window_start"`
		Reason      string    `json:"reason"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.ReleaseID <= 0 || in.Score < community.MinScore || in.Score > community.MaxScore || in.WindowStart.IsZero() {
		reply(w, nil, badRequest("release_id, score (1-5) and window_start required", nil))
		return
	}
	n, err := srv.moderation.RemoveRatingFlood(r.Context(), in.ReleaseID, in.Score, in.WindowStart, strings.TrimSpace(in.Reason), actor)
	reply(w, map[string]int64{"removed": n}, err)
}

// listWords: GET /api/admin/moderation/words.
func (srv *Server) listWords(w http.ResponseWriter, r *http.Request) {
	if _, ok := srv.withModeration(w, r); !ok {
		return
	}
	v, err := srv.moderation.ModerationWords(r.Context())
	reply(w, map[string]any{"items": v}, err)
}

// addWord: POST /api/admin/moderation/words {"word", "category"?}.
func (srv *Server) addWord(w http.ResponseWriter, r *http.Request) {
	actor, ok := srv.withModeration(w, r)
	if !ok {
		return
	}
	var in struct {
		Word     string `json:"word"`
		Category string `json:"category"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Category == "" {
		in.Category = "other"
	}
	if !community.ValidKeywordCategory(in.Category) {
		reply(w, nil, badRequest("category must be one of "+strings.Join(community.KeywordCategories, ", "), nil))
		return
	}
	if n := community.NormalizeKeyword(in.Word); n == "" || len([]rune(n)) > community.MaxKeywordRunes {
		reply(w, nil, badRequest("word must have 1-64 letters or digits", nil))
		return
	}
	v, err := srv.moderation.AddModerationWord(r.Context(), in.Word, in.Category, actor)
	reply(w, v, err)
}

// deleteWord: DELETE /api/admin/moderation/words/{word}.
func (srv *Server) deleteWord(w http.ResponseWriter, r *http.Request) {
	actor, ok := srv.withModeration(w, r)
	if !ok {
		return
	}
	err := srv.moderation.DeleteModerationWord(r.Context(), r.PathValue("word"), actor)
	reply(w, map[string]bool{"deleted": err == nil}, err)
}

// unmute: DELETE /api/admin/moderation/mutes/{user_id}.
func (srv *Server) unmute(w http.ResponseWriter, r *http.Request) {
	actor, ok := srv.withModeration(w, r)
	if !ok {
		return
	}
	id, err := pathInt64(r, "user_id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.moderation.Unmute(r.Context(), id, strings.TrimSpace(r.URL.Query().Get("reason")), actor)
	reply(w, map[string]bool{"deleted": err == nil}, err)
}

// commentEdits: GET /api/admin/moderation/comments/{id}/edits — a comment's
// current text and every earlier version (author edits, #102), oldest first.
func (srv *Server) commentEdits(w http.ResponseWriter, r *http.Request) {
	if _, ok := srv.withModeration(w, r); !ok {
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.moderation.CommentEdits(r.Context(), id)
	reply(w, v, err)
}
