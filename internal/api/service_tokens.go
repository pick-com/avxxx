package api

// Service tokens (migration 044): scoped credentials for automation, so
// scripts such as the daily SEO check no longer need the break-glass
// ADMIN_TOKEN.
//
//	GET  /api/admin/service-tokens              tokens (never the token values) and the grantable scopes
//	POST /api/admin/service-tokens              {"name","description","scopes":[...],"expires_in_days"} → token, shown once
//	POST /api/admin/service-tokens/{id}/revoke  revokes at once
//
// Security design:
//   - The token is "svc_" + 32 random bytes (base64url). Only its SHA-256 is
//     stored (service_tokens.token_hash) plus the first characters to
//     recognise it; the value is returned once with Cache-Control: no-store.
//   - Only admins with admin_users write (super_admin) create or revoke them.
//   - A scope is an exact route pattern ("POST /api/admin/seo/report") or a
//     route group with an access level ("dashboard:read"). serviceAllowed
//     decides per request pattern; unknown patterns, dual-approval routes and
//     the admin_users, audit_log and self groups (sessions, the approvals
//     queue) are never reachable, whatever the scopes say, and such scopes
//     are refused at creation.
//   - Tokens expire (1-730 days, required) and can be revoked; lookups only
//     match live tokens. Every request made with one is in the op log
//     (auth "service", actor "service:<name>") with the hashed client IP.

import (
	"avxxx/internal/adminauth"
	"avxxx/internal/obs"
	"avxxx/internal/store"
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// serviceTokenStore backs service tokens; *store.Store implements it.
type serviceTokenStore interface {
	ServiceTokenByToken(ctx context.Context, token, ipHash string) (store.ServiceToken, error)
	CreateServiceToken(ctx context.Context, in store.ServiceTokenInput, actor string, actorID int64) (store.ServiceToken, string, error)
	ServiceTokens(ctx context.Context) ([]store.ServiceToken, error)
	RevokeServiceToken(ctx context.Context, id int64, actor string) (store.ServiceToken, error)
}

// Groups a service token can never reach: accounts and roles, the op log,
// and the signed-in admin's own routes (sessions, recovery codes, approvals).
var serviceDeniedGroups = map[adminauth.Group]bool{
	adminauth.GroupAdminUsers: true,
	adminauth.GroupAuditLog:   true,
	adminauth.GroupSelf:       true,
}

const (
	maxServiceScopes        = 50
	maxServiceTokenDays     = 730
	defaultServiceTokenDays = 365
)

// serviceRouteGrantable reports whether a route may be named in a service
// token scope at all.
func serviceRouteGrantable(pattern string) bool {
	rp, known := adminRoutePerm(pattern)
	return known && !rp.Dual && !serviceDeniedGroups[rp.Group]
}

// parseGroupScope splits "group:read|write".
func parseGroupScope(scope string) (adminauth.Group, adminauth.Access, bool) {
	g, a, ok := strings.Cut(scope, ":")
	if !ok {
		return "", adminauth.None, false
	}
	switch a {
	case "read":
		return adminauth.Group(g), adminauth.Read, true
	case "write":
		return adminauth.Group(g), adminauth.Write, true
	}
	return "", adminauth.None, false
}

// serviceAllowed reports whether a service token with scopes may call pattern.
func serviceAllowed(scopes []string, pattern string) bool {
	if !serviceRouteGrantable(pattern) {
		return false
	}
	rp, _ := adminRoutePerm(pattern)
	for _, s := range scopes {
		if s == pattern {
			return true
		}
		if g, a, ok := parseGroupScope(s); ok && g == rp.Group && a >= rp.Access && !serviceDeniedGroups[g] {
			return true
		}
	}
	return false
}

// validateServiceScopes checks, dedupes and sorts requested scopes.
func validateServiceScopes(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, badRequest("at least one scope is required", nil)
	}
	if len(in) > maxServiceScopes {
		return nil, badRequest("too many scopes", nil)
	}
	seen := map[string]bool{}
	for _, raw := range in {
		s := strings.Join(strings.Fields(raw), " ")
		if g, _, ok := parseGroupScope(s); ok && !strings.Contains(s, " ") {
			if !slices.Contains(adminauth.Groups, g) || serviceDeniedGroups[g] {
				return nil, badRequest("scope "+s+" cannot be granted to a service token", nil)
			}
		} else if !serviceRouteGrantable(s) {
			return nil, badRequest("scope "+s+" is not a grantable admin route (dual-approval, admin account and op-log routes never are)", nil)
		}
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}

// serviceToken authenticates a "svc_" bearer.
func (srv *Server) serviceToken(r *http.Request, tok string) (adminPrincipal, error) {
	deny := &adminAuthError{http.StatusUnauthorized, "unauthorized"}
	if srv.svcTokens == nil {
		return adminPrincipal{}, deny
	}
	t, err := srv.svcTokens.ServiceTokenByToken(r.Context(), tok, srv.ipHash(r))
	if errors.Is(err, pgx.ErrNoRows) {
		obs.L(r.Context()).Warn("admin service token rejected", "route", r.Pattern, "ip_hash", srv.ipHash(r))
		return adminPrincipal{}, deny
	}
	if err != nil {
		return adminPrincipal{}, err
	}
	return adminPrincipal{
		AdminPrincipal: store.AdminPrincipal{Username: "service:" + t.Name},
		Auth:           "service", Label: t.Name, Scopes: t.Scopes,
	}, nil
}

func (srv *Server) serviceTokenRoutes() []route {
	return []route{
		{"GET /api/admin/service-tokens", srv.admin(srv.withSvcTokens(srv.listServiceTokens))},
		{"POST /api/admin/service-tokens", srv.admin(srv.withSvcTokens(srv.createServiceToken))},
		{"POST /api/admin/service-tokens/{id}/revoke", srv.admin(srv.withSvcTokens(srv.revokeServiceToken))},
	}
}

func (srv *Server) withSvcTokens(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.svcTokens == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// grantableScopes lists what the create form offers.
func grantableScopes() map[string]any {
	type groupView struct {
		Group string `json:"group"`
		Label string `json:"label"`
	}
	groups := []groupView{}
	for _, g := range adminauth.Groups {
		if !serviceDeniedGroups[g] {
			groups = append(groups, groupView{string(g), adminauth.GroupLabels[g]})
		}
	}
	routes := []string{}
	for pattern := range adminRoutePerms {
		if serviceRouteGrantable(pattern) {
			routes = append(routes, pattern)
		}
	}
	sort.Strings(routes)
	return map[string]any{"groups": groups, "routes": routes}
}

func (srv *Server) listServiceTokens(w http.ResponseWriter, r *http.Request) {
	items, err := srv.svcTokens.ServiceTokens(r.Context())
	reply(w, map[string]any{"items": items, "grantable": grantableScopes(), "max_days": maxServiceTokenDays}, err)
}

func (srv *Server) createServiceToken(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	if p.Auth == "service" || (p.Auth == "session" && !adminauth.IsSuper(p.Roles)) {
		reply(w, nil, &httpError{status: http.StatusForbidden, msg: "only a super admin creates service tokens"})
		return
	}
	var in struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expires_in_days"`
	}
	if err := decodeAdminJSON(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	scopes, err := validateServiceScopes(in.Scopes)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if in.ExpiresInDays == 0 {
		in.ExpiresInDays = defaultServiceTokenDays
	}
	if in.ExpiresInDays < 1 || in.ExpiresInDays > maxServiceTokenDays {
		reply(w, nil, badRequest("expires_in_days must be 1 to 730", nil))
		return
	}
	t, token, err := srv.svcTokens.CreateServiceToken(r.Context(), store.ServiceTokenInput{
		Name: in.Name, Description: in.Description, Scopes: scopes,
		ExpiresAt: time.Now().Add(time.Duration(in.ExpiresInDays) * 24 * time.Hour),
	}, p.Actor(), p.ID)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	reply(w, map[string]any{"service_token": t, "token": token}, nil)
}

func (srv *Server) revokeServiceToken(w http.ResponseWriter, r *http.Request) {
	p, _ := principalFrom(r.Context())
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	t, err := srv.svcTokens.RevokeServiceToken(r.Context(), id, p.Actor())
	reply(w, t, err)
}
