package api

// In-app notification routes. Notifications are written by the new-work
// fan-out (store.NotifyFanout, run by cmd/scheduler).

import (
	"avxxx/internal/store"
	"net/http"
)

// maxReadIDs bounds POST /api/me/notifications/read.
const maxReadIDs = 500

func (srv *Server) notificationRoutes() []route {
	return []route{
		{"GET /api/me/notifications", srv.user(srv.listNotifications)},
		{"GET /api/me/notifications/unread-count", srv.user(srv.unreadCount)},
		{"POST /api/me/notifications/read", srv.user(srv.markRead)},
	}
}

// listNotifications: ?cursor=&limit=, newest first.
func (srv *Server) listNotifications(w http.ResponseWriter, r *http.Request, uid int64) {
	q := r.URL.Query()
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	c, err := cursorParam(q, store.NotificationSort)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, next, e := srv.social.Notifications(r.Context(), uid, c, limit)
	reply(w, page(v, next), e)
}

func (srv *Server) unreadCount(w http.ResponseWriter, r *http.Request, uid int64) {
	n, e := srv.social.UnreadNotifications(r.Context(), uid)
	reply(w, map[string]int64{"unread": n}, e)
}

// markRead takes {"ids": [..]} or {"all": true}; exactly one is required.
func (srv *Server) markRead(w http.ResponseWriter, r *http.Request, uid int64) {
	var in struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	switch {
	case in.All && len(in.IDs) > 0:
		reply(w, nil, badRequest("send either ids or all, not both", nil))
		return
	case !in.All && len(in.IDs) == 0:
		reply(w, nil, badRequest("ids or all is required", nil))
		return
	case len(in.IDs) > maxReadIDs:
		reply(w, nil, badRequest("too many ids", nil))
		return
	}
	n, e := srv.social.MarkNotificationsRead(r.Context(), uid, in.IDs, in.All)
	reply(w, map[string]int64{"updated": n}, e)
}
