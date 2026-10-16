package api

// Feedback (LegalPage footer 反馈 / 帮助 / 合作; tech plan 3.13): public
// intake, admin list and status. Nothing of a submission is ever returned
// by a public route.

import (
	"avxxx/internal/store"
	"net/http"
	"strings"
)

func (srv *Server) feedbackRoutes() []route {
	return []route{
		{"POST /api/feedback", srv.createFeedback},
		{"GET /api/admin/feedback", srv.admin(srv.adminFeedbackList)},
		{"POST /api/admin/feedback/{id}/status", srv.admin(srv.adminFeedbackStatus)},
	}
}

// createFeedback: POST /api/feedback {"category", "message", "email"?,
// "page_url"?, "lang"?}. 201 with {id, status, created_at}; 429 when rate
// limited. page_url keeps only scheme, host and path.
func (srv *Server) createFeedback(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	var in store.FeedbackInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowIntake(r, feedbackLimit); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.CreateFeedback(r.Context(), in)
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// adminFeedbackList: GET /api/admin/feedback?status=&category=&limit=&cursor=,
// newest first, with the open count for the nav badge.
func (srv *Server) adminFeedbackList(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, next, err := srv.compliance.AdminFeedback(r.Context(), store.FeedbackFilter{
		Status: strings.TrimSpace(q.Get("status")), Category: strings.TrimSpace(q.Get("category")), Limit: limit, Cursor: strings.TrimSpace(q.Get("cursor")),
	})
	if err != nil {
		reply(w, nil, err)
		return
	}
	open, err := srv.compliance.FeedbackOpenCount(r.Context())
	out := page(items, next)
	out["open"] = open
	reply(w, out, err)
}

// adminFeedbackStatus: POST /api/admin/feedback/{id}/status {"status":
// open|in_progress|resolved|dismissed, "note"?}.
func (srv *Server) adminFeedbackStatus(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.SetFeedbackStatus(r.Context(), id, strings.TrimSpace(in.Status), in.Note, actor)
	reply(w, v, err)
}
