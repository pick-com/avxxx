package api

// AdminUsers routes (artboard AdminUsers, PRD 11):
//
//	GET  /api/admin/admin-users                    accounts with roles, 2FA and lock state
//	POST /api/admin/admin-users                    invite {"username","email","display_name","roles"} → enrollment link
//	POST /api/admin/admin-users/{id}/invite        new enrollment link (invited accounts)
//	POST /api/admin/admin-users/{id}/disable       {"reason"} immediate; revokes sessions
//	PUT  /api/admin/admin-users/{id}/roles         {"roles":[...]}       dual approval
//	POST /api/admin/admin-users/{id}/enable        {"reason"}            dual approval
//	POST /api/admin/admin-users/{id}/reset-2fa     {"reason"}            dual approval → enrollment link
//	GET  /api/admin/approvals?status=pending|executed|failed|rejected|cancelled|expired|all
//	POST /api/admin/approvals/{id}/approve         runs the stored request as its requester
//	POST /api/admin/approvals/{id}/reject          {"note"}; the requester's own reject is a cancel
//	GET  /api/admin/audit-log?q=&actor=&auth=&entity_type=&entity_id=&from=&to=&cursor=&limit=
//	GET  /api/admin/site-users?q=&status=banned|muted
//	GET  /api/admin/site-users/{id}
//	POST /api/admin/site-users/{id}/ban            {"reason","days"} (days 0 = permanent)
//	POST /api/admin/site-users/{id}/unban          {"reason"}
//
// Enrollment links are "/admin/enroll#<token>": the token stays in the URL
// fragment, which browsers never send to a server.

import (
	"avxxx/internal/store"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (srv *Server) adminUserRoutes() []route {
	return []route{
		{"GET /api/admin/admin-users", srv.admin(srv.withAccts(srv.listAdminUsers))},
		{"POST /api/admin/admin-users", srv.admin(srv.withAccts(srv.inviteAdmin))},
		{"POST /api/admin/admin-users/{id}/invite", srv.admin(srv.withAccts(srv.reissueInvite))},
		{"POST /api/admin/admin-users/{id}/disable", srv.admin(srv.withAccts(srv.setAdminDisabled(true)))},
		{"POST /api/admin/admin-users/{id}/enable", srv.admin(srv.withAccts(srv.setAdminDisabled(false)))},
		{"PUT /api/admin/admin-users/{id}/roles", srv.admin(srv.withAccts(srv.setAdminRoles))},
		{"POST /api/admin/admin-users/{id}/reset-2fa", srv.admin(srv.withAccts(srv.resetAdmin2FA))},
		{"GET /api/admin/approvals", srv.admin(srv.withAccts(srv.listApprovals))},
		{"POST /api/admin/approvals/{id}/approve", srv.admin(srv.withAccts(srv.approve))},
		{"POST /api/admin/approvals/{id}/reject", srv.admin(srv.withAccts(srv.rejectApproval))},
		{"GET /api/admin/audit-log", srv.admin(srv.withAccts(srv.auditLog))},
		{"GET /api/admin/site-users", srv.admin(srv.withAccts(srv.searchSiteUsers))},
		{"GET /api/admin/site-users/{id}", srv.admin(srv.withAccts(srv.siteUser))},
		{"POST /api/admin/site-users/{id}/ban", srv.admin(srv.withAccts(srv.banSiteUser))},
		{"POST /api/admin/site-users/{id}/unban", srv.admin(srv.withAccts(srv.unbanSiteUser))},
	}
}

func (srv *Server) withAccts(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.adminAccts == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

func enrollLink(token string) string { return "/admin/enroll#" + token }

func (srv *Server) listAdminUsers(w http.ResponseWriter, r *http.Request) {
	v, err := srv.adminAccts.AdminUsers(r.Context())
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) inviteAdmin(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	var in struct {
		Username    string   `json:"username"`
		Email       string   `json:"email"`
		DisplayName string   `json:"display_name"`
		Roles       []string `json:"roles"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	u, token, err := srv.adminAccts.InviteAdmin(r.Context(), store.AdminUserInput{Username: in.Username, Email: in.Email, DisplayName: in.DisplayName, Roles: in.Roles}, p.Actor())
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"admin": u, "enroll_url": enrollLink(token)}, nil)
}

func (srv *Server) reissueInvite(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	token, err := srv.adminAccts.ReissueAdminInvite(r.Context(), id, p.Actor())
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"enroll_url": enrollLink(token)}, nil)
}

type reasonInput struct {
	Reason string `json:"reason"`
}

func (srv *Server) setAdminDisabled(disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, _ := principalFrom(r.Context())
		id, err := pathID(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		var in reasonInput
		if err := decodeAdminJSON(w, r, &in); err != nil {
			reply(w, nil, err)
			return
		}
		u, err := srv.adminAccts.SetAdminDisabled(r.Context(), id, disabled, strings.TrimSpace(in.Reason), p.Actor(), p.ID)
		reply(w, u, err)
	}
}

func (srv *Server) setAdminRoles(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Roles  []string `json:"roles"`
		Reason string   `json:"reason"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	u, err := srv.adminAccts.SetAdminRoles(r.Context(), id, in.Roles, p.Actor(), p.ID)
	reply(w, u, err)
}

func (srv *Server) resetAdmin2FA(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in reasonInput
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	token, err := srv.adminAccts.ResetAdminTOTP(r.Context(), id, strings.TrimSpace(in.Reason), p.Actor(), p.ID)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"enroll_url": enrollLink(token)}, nil)
}

var approvalStatuses = map[string]bool{"": true, "pending": true, "executed": true, "failed": true, "rejected": true, "cancelled": true, "expired": true, "all": true}

func (srv *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	status := r.URL.Query().Get("status")
	if !approvalStatuses[status] {
		reply(w, nil, badRequest("invalid status", nil))
		return
	}
	list, err := srv.adminAccts.AdminApprovals(r.Context(), status, 100)
	if err != nil {
		reply(w, nil, err)
		return
	}
	items := make([]approvalItem, 0, len(list))
	for _, a := range list {
		items = append(items, approvalView(a, p))
	}
	reply(w, map[string]any{"items": items}, nil)
}

// approve: the approver needs the stored route's permission and must not be
// the requester; the request then runs as the requester (who must still be
// active and allowed). The response carries the replayed result.
func (srv *Server) approve(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	a, err := srv.adminAccts.GetAdminApproval(r.Context(), id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	rp, known := adminRoutePerm(a.Route)
	if !p.can(rp, known) {
		reply(w, nil, &httpError{status: http.StatusForbidden, msg: "you cannot approve this request"})
		return
	}
	a, err = srv.adminAccts.ClaimAdminApproval(r.Context(), id, p.ID, p.Actor())
	if err != nil {
		reply(w, nil, err)
		return
	}
	status, result, err := srv.executeApproval(r, a, p)
	if err != nil {
		code := statusOf(err)
		_ = srv.adminAccts.FinishAdminApproval(r.Context(), id, false, code)
		reply(w, nil, err)
		return
	}
	ok := status >= 200 && status < 300
	if err := srv.adminAccts.FinishAdminApproval(r.Context(), id, ok, status); err != nil {
		reply(w, nil, err)
		return
	}
	a, err = srv.adminAccts.GetAdminApproval(r.Context(), id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store") // the result may hold an enrollment link
	reply(w, map[string]any{"approval": approvalView(a, p), "result_status": status, "result": result}, nil)
}

func (srv *Server) rejectApproval(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	a, err := srv.adminAccts.GetAdminApproval(r.Context(), id)
	if err != nil {
		reply(w, nil, err)
		return
	}
	rp, known := adminRoutePerm(a.Route)
	mine := p.Auth == "session" && p.ID == a.RequestedBy
	if !mine && !p.can(rp, known) {
		reply(w, nil, &httpError{status: http.StatusForbidden, msg: "you cannot reject this request"})
		return
	}
	a, err = srv.adminAccts.DecideAdminApproval(r.Context(), id, p.ID, p.Actor(), strings.TrimSpace(in.Note))
	if err != nil {
		reply(w, nil, err)
		return
	}
	reply(w, map[string]any{"approval": approvalView(a, p)}, nil)
}

func parseAuditTime(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return &t, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return nil, badRequest("from / to must be RFC 3339 or YYYY-MM-DD", err)
	}
	return &t, nil
}

func (srv *Server) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := parseAuditTime(q.Get("from"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	to, err := parseAuditTime(q.Get("to"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	auth := q.Get("auth")
	if auth != "" && auth != "session" && auth != "token" && auth != "service" && auth != "anonymous" {
		reply(w, nil, badRequest("auth must be session, token, service or anonymous", nil))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	items, next, err := srv.adminAccts.AdminAuditLog(r.Context(), store.AdminAuditFilter{Q: q.Get("q"), Actor: q.Get("actor"), Auth: auth,
		EntityType: q.Get("entity_type"), EntityID: q.Get("entity_id"), From: from, To: to, Cursor: q.Get("cursor"), Limit: limit})
	reply(w, map[string]any{"items": items, "next_cursor": nullable(next)}, err)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (srv *Server) searchSiteUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	v, err := srv.adminAccts.SearchSiteUsers(r.Context(), q.Get("q"), q.Get("status"), limit)
	reply(w, map[string]any{"items": v}, err)
}

func (srv *Server) siteUser(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.adminAccts.SiteUserByID(r.Context(), id)
	reply(w, v, err)
}

// maxBanDays bounds a timed ban; longer means permanent (days 0).
const maxBanDays = 3650

func (srv *Server) banSiteUser(w http.ResponseWriter, r *http.Request) {
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Reason string `json:"reason"`
		Days   int    `json:"days"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Days < 0 || in.Days > maxBanDays {
		reply(w, nil, badRequest("days must be 0 (permanent) to 3650", nil))
		return
	}
	var until *time.Time
	if in.Days > 0 {
		t := time.Now().Add(time.Duration(in.Days) * 24 * time.Hour)
		until = &t
	}
	v, err := srv.adminAccts.BanUser(r.Context(), id, until, in.Reason, actor)
	reply(w, v, err)
}

func (srv *Server) unbanSiteUser(w http.ResponseWriter, r *http.Request) {
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in reasonInput
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.adminAccts.UnbanUser(r.Context(), id, in.Reason, actor)
	reply(w, v, err)
}
