package api

import (
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
)

// mergeRedirectRoutes maps public slug routes (and their sub-resources) to
// the entity type whose merges they follow.
var mergeRedirectRoutes = []struct{ prefix, entityType string }{
	{"GET /api/performers/{slug}", "performer"},
	{"GET /api/releases/{slug}", "release"},
}

// followMerges wraps every public performer/release slug route so a merged
// entity's old slug answers 301 to the surviving entity's slug.
//
// Why 301 and not a JSON redirect field: fetch() follows same-origin
// redirects on GET transparently, so the SPA receives the surviving entity's
// JSON (which carries its own slug; the SPA can history.replaceState to it),
// while crawlers and server-side rendering get a real 301 for SEO, as
// AdminDedupe specifies. The response also sets Cache-Control: no-store
// because an undo-merge must be able to take the redirect back, and carries a
// small JSON body for clients that use redirect: "manual".
func (srv *Server) followMerges(rs []route) []route {
	for i, rt := range rs {
		for _, m := range mergeRedirectRoutes {
			if rt.pattern == m.prefix || strings.HasPrefix(rt.pattern, m.prefix+"/") {
				rs[i].handler = srv.mergeRedirect(m.entityType, rt.handler)
			}
		}
	}
	return rs
}

func (srv *Server) mergeRedirect(entityType string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.identity == nil {
			h(w, r)
			return
		}
		slug := r.PathValue("slug")
		target, ok, err := srv.identity.ResolveMergedSlug(r.Context(), entityType, slug)
		if err != nil {
			// Fail open: the redirect lookup is an optimization over a 404.
			log.Printf("merge redirect lookup %s %q: %v", entityType, slug, err)
		}
		if err != nil || !ok {
			h(w, r)
			return
		}
		loc := redirectLocation(r.URL, target)
		w.Header().Set("Location", loc)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMovedPermanently)
		json.NewEncoder(w).Encode(map[string]string{"redirect": loc, "slug": target, "entity_type": entityType})
	}
}

// redirectLocation replaces the slug segment of /api/{kind}/{slug}[/...] with
// target and keeps the query string.
func redirectLocation(u *url.URL, target string) string {
	parts := strings.Split(u.EscapedPath(), "/")
	if len(parts) > 3 {
		parts[3] = url.PathEscape(target)
	}
	loc := strings.Join(parts, "/")
	if u.RawQuery != "" {
		loc += "?" + u.RawQuery
	}
	return loc
}
