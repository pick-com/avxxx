package api

// AdminSearch 补片 / 建预约页 from a zero-result term:
//
//	POST /api/admin/search/terms/release {term, title?, code?, scheduled_at?, reason?}
//
// Without scheduled_at it creates a draft release (补片) for the editors to
// complete; with a future scheduled_at (RFC 3339) an upcoming release
// (建预约页) that users can reserve and that publishes itself when the date
// passes. The title defaults to the term. It creates catalog data, so it
// needs catalog:write (admin_perms.go). 201 {entity_id, slug, status, ...};
// 409 when the code or slug already exists.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"strings"
	"time"
)

type releaseStubStore interface {
	CreateReleaseFromTerm(ctx context.Context, in store.ReleaseStubInput, actor string) (store.ReleaseStub, error)
}

func (srv *Server) releaseStubRoutes() []route {
	return []route{{"POST /api/admin/search/terms/release", srv.admin(srv.releaseFromTerm)}}
}

func (srv *Server) releaseFromTerm(w http.ResponseWriter, r *http.Request) {
	if srv.stubs == nil {
		reply(w, nil, errUnavailable)
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var b struct {
		Term        string `json:"term"`
		Title       string `json:"title"`
		Code        string `json:"code"`
		ScheduledAt string `json:"scheduled_at"`
		Reason      string `json:"reason"`
	}
	if err := decodeAdminJSON(w, r, &b); err != nil {
		reply(w, nil, err)
		return
	}
	term, err := termParam("term", b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	title, err := fixValue(b.Title, b.Term)
	if err != nil {
		reply(w, nil, err)
		return
	}
	in := store.ReleaseStubInput{Term: term, Title: title, Code: strings.TrimSpace(b.Code), Reason: strings.TrimSpace(b.Reason)}
	if v := strings.TrimSpace(b.ScheduledAt); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			reply(w, nil, badRequest("scheduled_at must be RFC 3339", err))
			return
		}
		in.ScheduledAt = &t
	}
	v, err := srv.stubs.CreateReleaseFromTerm(r.Context(), in, actor)
	replyCreated(w, v, err)
}
