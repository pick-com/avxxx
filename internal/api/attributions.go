package api

// Source credits for the public pages (sources.attribution_html,
// migrations/025_dmm_source.sql). DMM's affiliate terms require the credit
// "Powered by <a href="https://affiliate.dmm.com/api/">FANZA Webサービス</a>"
// wherever its data is displayed; the web app renders each html verbatim.
//
//	GET /api/attributions                       sources whose data appears anywhere
//	GET /api/attributions?release={slug}        ... behind a release page
//	GET /api/attributions?performer={slug}      ... behind a performer page
//	GET /api/attributions?series={slug}         ... behind a series page
//
// Answers {"items":[{"source","html"}]}; html is exactly as stored. Unknown
// slugs answer an empty list.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"strings"
)

type attributionStore interface {
	Attributions(ctx context.Context, scope store.AttributionScope) ([]store.Attribution, error)
}

func (srv *Server) attributionRoutes() []route {
	var st attributionStore
	if srv.store != nil {
		st = srv.store
	}
	return []route{{"GET /api/attributions", attributionsHandler(st)}}
}

func attributionsHandler(st attributionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var scope store.AttributionScope
		for _, kind := range []string{"release", "performer", "series"} {
			v := strings.TrimSpace(q.Get(kind))
			if v == "" {
				continue
			}
			if scope.Kind != "" {
				reply(w, nil, badRequest("give at most one of release, performer, series", nil))
				return
			}
			scope = store.AttributionScope{Kind: kind, Slug: v}
		}
		if st == nil {
			reply(w, nil, errUnavailable)
			return
		}
		items, err := st.Attributions(r.Context(), scope)
		if err == nil {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		reply(w, map[string]any{"items": items}, err)
	}
}
