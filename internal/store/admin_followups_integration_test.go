package store

import (
	"avxxx/internal/adminauth"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestServiceTokensIntegration covers migration 044 (re-runnable), service
// token storage (hash only, one live token per name, expiry, revocation,
// last use) and the op log accepting auth 'service'.
func TestServiceTokensIntegration(t *testing.T) {
	openIsolatedTestDB(t, "svctokens")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil { // migrations re-run on every start
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()

	in := ServiceTokenInput{Name: "SEO-check", Description: "daily SEO check", Scopes: []string{"POST /api/admin/seo/report"}, ExpiresAt: time.Now().Add(time.Hour)}
	tok, token, err := s.CreateServiceToken(ctx, in, "root", 0)
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "seo-check" || tok.Status != "active" || !strings.HasPrefix(token, ServiceTokenPrefix) || len(token) < 40 || !strings.HasPrefix(token, tok.TokenPrefix) {
		t.Fatalf("created %+v %q", tok, token)
	}
	var stored string
	if err := s.QueryRow(ctx, `SELECT token_hash FROM service_tokens WHERE id=$1`, tok.ID).Scan(&stored); err != nil || stored != adminauth.HashToken(token) || strings.Contains(stored, token) {
		t.Fatalf("stored hash %q %v", stored, err)
	}
	if _, _, err := s.CreateServiceToken(ctx, in, "root", 0); err == nil {
		t.Fatal("second live token with the same name accepted")
	}
	for _, bad := range []ServiceTokenInput{
		{Name: "x", Scopes: in.Scopes, ExpiresAt: in.ExpiresAt},
		{Name: "ok-name", ExpiresAt: in.ExpiresAt},
		{Name: "ok-name", Scopes: in.Scopes, ExpiresAt: time.Now().Add(-time.Minute)},
	} {
		if _, _, err := s.CreateServiceToken(ctx, bad, "root", 0); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	got, err := s.ServiceTokenByToken(ctx, token, "iphash")
	if err != nil || got.ID != tok.ID || got.Scopes[0] != in.Scopes[0] {
		t.Fatalf("lookup %+v %v", got, err)
	}
	var used *time.Time
	var ipHash string
	if err := s.QueryRow(ctx, `SELECT last_used_at,last_used_ip_hash FROM service_tokens WHERE id=$1`, tok.ID).Scan(&used, &ipHash); err != nil || used == nil || ipHash != "iphash" {
		t.Errorf("last use %v %q %v", used, ipHash, err)
	}
	if _, err := s.ServiceTokenByToken(ctx, token+"x", ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("wrong token: %v", err)
	}

	// Expired tokens stop working.
	if _, err := s.Exec(ctx, `UPDATE service_tokens SET expires_at=now()-interval '1 second' WHERE id=$1`, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ServiceTokenByToken(ctx, token, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expired token: %v", err)
	}
	if _, err := s.Exec(ctx, `UPDATE service_tokens SET expires_at=now()+interval '1 day' WHERE id=$1`, tok.ID); err != nil {
		t.Fatal(err)
	}

	// Revocation is immediate and frees the name.
	rv, err := s.RevokeServiceToken(ctx, tok.ID, "root")
	if err != nil || rv.Status != "revoked" || rv.RevokedBy == nil || *rv.RevokedBy != "root" {
		t.Fatalf("revoke %+v %v", rv, err)
	}
	if _, err := s.ServiceTokenByToken(ctx, token, ""); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("revoked token: %v", err)
	}
	var ce *ConflictError
	if _, err := s.RevokeServiceToken(ctx, tok.ID, "root"); !errors.As(err, &ce) {
		t.Errorf("second revoke: %v", err)
	}
	if _, err := s.RevokeServiceToken(ctx, 999999, "root"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown revoke: %v", err)
	}
	if _, _, err := s.CreateServiceToken(ctx, in, "root", 0); err != nil {
		t.Errorf("name reuse after revoke: %v", err)
	}
	list, err := s.ServiceTokens(ctx)
	if err != nil || len(list) != 2 || list[0].Status != "active" || list[1].Status != "revoked" {
		t.Fatalf("list %+v %v", list, err)
	}

	// The op log accepts service principals.
	if err := s.RecordAdminAudit(ctx, AdminAuditEntry{Actor: "service:seo-check", Auth: "service", Action: "ingest.post", Status: 200}); err != nil {
		t.Fatalf("service audit row: %v", err)
	}
	rows, _, err := s.AdminAuditLog(ctx, AdminAuditFilter{Auth: "service"})
	if err != nil || len(rows) != 1 || rows[0].Actor != "service:seo-check" {
		t.Fatalf("audit filter %+v %v", rows, err)
	}
}
