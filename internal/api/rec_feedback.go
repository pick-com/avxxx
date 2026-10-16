package api

// 推荐反馈 (YouTube-style 「不感兴趣」 / 「不推荐此演员」 on recommendation
// cards; migration 051):
//
//	POST   /api/recs/feedback {kind, target}            give feedback (rate-limited)
//	DELETE /api/recs/feedback?kind=&target=             撤销
//	GET    /api/me/rec-feedback                         the caller's active feedback
//
// kind: not_interested_release (target = release slug),
// not_interested_performer (performer slug), not_interested_tag
// (normalized tag). The caller is a valid bearer token, else a visitor's
// X-Device-ID (stored hashed, like ratings and 赞 / 踩). Feedback only ever
// goes back to its owner; GET /api/recs applies it (recs.go).
//
// Visitor feedback is not moved to the account on sign-in: there is no
// device-to-user merge for ratings or reactions either.

import (
	"avxxx/internal/community"
	"avxxx/internal/recs"
	"avxxx/internal/store"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// recFeedbackStore is the subset of *store.Store behind the feedback routes.
type recFeedbackStore interface {
	AddRecFeedback(ctx context.Context, owner store.RecFeedbackOwner, kind, target string) (store.RecFeedback, error)
	UndoRecFeedback(ctx context.Context, owner store.RecFeedbackOwner, kind, target string) (bool, error)
	ListRecFeedback(ctx context.Context, owner store.RecFeedbackOwner) ([]store.RecFeedback, error)
}

var (
	limitRecFeedbackOwner = rateRule{60, 10 * time.Minute}
	limitRecFeedbackIP    = rateRule{200, 10 * time.Minute}
)

func (srv *Server) withRecFeedback(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.recFeedback == nil {
			reply(w, nil, errUnavailable)
			return
		}
		w.Header().Set("Cache-Control", "private, no-store")
		h(w, r)
	}
}

// feedbackOwner identifies the caller: a token (must be valid when sent),
// else a valid X-Device-ID.
func (srv *Server) feedbackOwner(r *http.Request) (store.RecFeedbackOwner, error) {
	if bearerToken(r) != "" {
		if srv.accounts == nil {
			return store.RecFeedbackOwner{}, errUnauthorized
		}
		uid, err := srv.userID(r)
		if err != nil {
			return store.RecFeedbackOwner{}, err
		}
		return store.RecFeedbackOwner{UserID: uid}, nil
	}
	d := strings.TrimSpace(r.Header.Get(deviceHeader))
	if !community.ValidDeviceID(d) {
		return store.RecFeedbackOwner{}, badRequest("sign in or send a valid "+deviceHeader+" header", nil)
	}
	return store.RecFeedbackOwner{DeviceID: d}, nil
}

// recsFeedbackOwner is whose feedback GET /api/recs applies, without
// failing: the signed-in viewer (even with personalize_recs off), else a
// valid X-Device-ID, else nobody. uid is recsViewerID's answer (0 also when
// personalize_recs is off, so the token is looked up again only then).
func (srv *Server) recsFeedbackOwner(r *http.Request, uid int64) store.RecFeedbackOwner {
	if srv.recFeedback == nil {
		return store.RecFeedbackOwner{}
	}
	if uid == 0 && bearerToken(r) != "" {
		uid = srv.viewerID(r)
	}
	if uid != 0 {
		return store.RecFeedbackOwner{UserID: uid}
	}
	if d := strings.TrimSpace(r.Header.Get(deviceHeader)); community.ValidDeviceID(d) {
		return store.RecFeedbackOwner{DeviceID: d}
	}
	return store.RecFeedbackOwner{}
}

func feedbackInput(kind, target string) (string, string, error) {
	kind, target = strings.TrimSpace(kind), strings.TrimSpace(target)
	if !recs.ValidFeedbackKind(kind) {
		return "", "", badRequest("kind must be not_interested_release, not_interested_performer or not_interested_tag", nil)
	}
	if target == "" || len(target) > 200 {
		return "", "", badRequest("target required", nil)
	}
	return kind, target, nil
}

func feedbackErr(err error) error {
	if errors.Is(err, store.ErrBadFeedback) {
		return badRequest(store.ErrBadFeedback.Error(), nil)
	}
	return err
}

// postRecFeedback: POST /api/recs/feedback {"kind":…,"target":…} → 201 with
// the stored row (re-sending an active one is a no-op).
func (srv *Server) postRecFeedback(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Kind   string `json:"kind"`
		Target string `json:"target"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	kind, target, err := feedbackInput(in.Kind, in.Target)
	if err != nil {
		reply(w, nil, err)
		return
	}
	owner, err := srv.feedbackOwner(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.allow(r, "recfb-ip:"+srv.ip(r), limitRecFeedbackIP) || !srv.allow(r, "recfb:"+owner.Key(), limitRecFeedbackOwner) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	fb, err := srv.recFeedback.AddRecFeedback(r.Context(), owner, kind, target)
	if err != nil {
		reply(w, nil, feedbackErr(err))
		return
	}
	replyStatus(w, http.StatusCreated, fb)
}

// deleteRecFeedback: DELETE /api/recs/feedback?kind=&target= (撤销).
// {"undone": false} when there was nothing active to undo.
func (srv *Server) deleteRecFeedback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, target, err := feedbackInput(q.Get("kind"), q.Get("target"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	owner, err := srv.feedbackOwner(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if !srv.allow(r, "recfb-ip:"+srv.ip(r), limitRecFeedbackIP) {
		reply(w, nil, tooManyRequests("too many requests"))
		return
	}
	ok, err := srv.recFeedback.UndoRecFeedback(r.Context(), owner, kind, target)
	reply(w, map[string]any{"undone": ok}, feedbackErr(err))
}

// listRecFeedback: GET /api/me/rec-feedback → {"items": [...]}, newest
// first (Settings › 推荐反馈).
func (srv *Server) listRecFeedback(w http.ResponseWriter, r *http.Request) {
	owner, err := srv.feedbackOwner(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, err := srv.recFeedback.ListRecFeedback(r.Context(), owner)
	if items == nil {
		items = []store.RecFeedback{}
	}
	reply(w, map[string]any{"items": items}, err)
}
