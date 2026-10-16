package api

// Copyright takedowns (CopyrightForm; AdminOps 版权投诉; PRD 15). Public:
// file a notice (random ticket), look up its status with ticket + email,
// file a counter-notice. Admin: queue with the 24 h SLA, status changes,
// take content down through the existing release status and image
// suppression paths, and restore after a counter-notice. Every admin change
// writes review_events with the X-Admin-User actor.

import (
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (srv *Server) takedownRoutes() []route {
	return []route{
		{"POST /api/takedowns", srv.createTakedown},
		{"GET /api/takedowns/{ticket}", srv.takedownStatus},
		{"POST /api/takedowns/{ticket}/counter", srv.counterNotice},
		{"GET /api/admin/takedowns", srv.admin(srv.adminTakedownList)},
		{"GET /api/admin/takedowns/{id}", srv.admin(srv.adminTakedownGet)},
		{"POST /api/admin/takedowns/{id}/status", srv.admin(srv.adminTakedownStatus)},
		{"POST /api/admin/takedowns/{id}/actions", srv.admin(srv.adminTakedownAction)},
		{"POST /api/admin/takedowns/{id}/restore", srv.admin(srv.adminTakedownRestore)},
	}
}

// takedownEmailHeader carries the email for the status lookup, so the
// address stays out of URLs and access logs.
const takedownEmailHeader = "X-Takedown-Email"

// createTakedown: POST /api/takedowns {"claimant_name", "claimant_email",
// "claimant_phone"?, "claimant_address"?, "rights_owner", "relationship":
// owner|agent, "work_description", "original_urls"?, "infringing_urls",
// "good_faith": true, "accuracy": true, "signature", "lang"?}. 201 with
// {ticket, status, due_at, created_at}; 429 when rate limited.
func (srv *Server) createTakedown(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	var in store.TakedownInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowIntake(r, takedownLimit); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.CreateTakedown(r.Context(), in, time.Now())
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// takedownStatus: GET /api/takedowns/{ticket} with the X-Takedown-Email
// header (the claimant's or counter-notifier's address). Status only; any
// mismatch is 404.
func (srv *Server) takedownStatus(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if err := srv.allowIntake(r, takedownLookupLimit); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.TakedownStatus(r.Context(), r.PathValue("ticket"), r.Header.Get(takedownEmailHeader))
	reply(w, v, err)
}

// counterNotice: POST /api/takedowns/{ticket}/counter {"name", "email",
// "address", "statement", "consent": true, "signature"}; only on an
// actioned ticket (409 otherwise).
func (srv *Server) counterNotice(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	var in store.CounterNoticeInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.Normalize(); err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowIntake(r, counterNoticeLimit); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.SubmitCounterNotice(r.Context(), r.PathValue("ticket"), in)
	reply(w, v, err)
}

// adminTakedownList: GET /api/admin/takedowns?status=&open=1&limit=&cursor=,
// newest first, each with overdue (no action 24 h after filing), plus the
// open / overdue counts.
func (srv *Server) adminTakedownList(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items, next, err := srv.compliance.AdminTakedowns(r.Context(), store.TakedownFilter{
		Status: strings.TrimSpace(q.Get("status")), Open: q.Get("open") == "1" || q.Get("open") == "true", Limit: limit, Cursor: strings.TrimSpace(q.Get("cursor")),
	})
	if err != nil {
		reply(w, nil, err)
		return
	}
	counts, err := srv.compliance.AdminTakedownCounts(r.Context())
	out := page(items, next)
	out["counts"] = counts
	out["sla_hours"] = int(store.TakedownSLA / time.Hour)
	reply(w, out, err)
}

func (srv *Server) adminTakedownGet(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.AdminTakedown(r.Context(), id)
	reply(w, v, err)
}

// adminTakedownStatus: POST /api/admin/takedowns/{id}/status {"status":
// reviewing|rejected|closed, "note"} (a rejection needs a note).
func (srv *Server) adminTakedownStatus(w http.ResponseWriter, r *http.Request) {
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
	v, err := srv.compliance.SetTakedownStatus(r.Context(), id, strings.TrimSpace(in.Status), in.Note, actor)
	reply(w, v, err)
}

// adminTakedownAction: POST /api/admin/takedowns/{id}/actions {"action":
// unpublish_release, "release_id" | "release_slug", "reason"?} or
// {"action": suppress_image, "media_asset_id", "reason"?}.
func (srv *Server) adminTakedownAction(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in store.TakedownActionInput
	if err := decodeStrict(w, r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.TakedownAction(r.Context(), id, in, actor)
	reply(w, v, err)
}

// replyMissingRelease answers a release lookup that found nothing public:
// 451 Unavailable For Legal Reasons when an active copyright takedown holds
// the release down (RFC 7725; the body names no title, claimant or ticket),
// else the usual error mapping (404).
func (srv *Server) replyMissingRelease(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pgx.ErrNoRows) && srv.compliance != nil {
		down, lerr := srv.compliance.ReleaseTakenDown(r.Context(), r.PathValue("slug"))
		if lerr != nil {
			obs.L(r.Context()).Warn("takedown lookup failed", "err", lerr)
		} else if down {
			w.Header().Set("Cache-Control", "no-store")
			replyStatus(w, http.StatusUnavailableForLegalReasons, map[string]string{"error": "unavailable for legal reasons", "reason": "copyright"})
			return
		}
	}
	reply(w, nil, err)
}

// adminTakedownRestore: POST /api/admin/takedowns/{id}/restore {"reason"}:
// undo every active action of the ticket (counter-notice or invalid claim).
func (srv *Server) adminTakedownRestore(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	reason, err := decodeReason(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.RestoreTakedown(r.Context(), id, reason, actor)
	reply(w, v, err)
}
