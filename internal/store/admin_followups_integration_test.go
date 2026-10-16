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

// TestAdminJobsIntegration covers the 夜间任务 queue, history, retry and
// run-now against an isolated database.
func TestAdminJobsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "adminjobs")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	id, enqueued, err := s.EnqueueKindNow(ctx, JobKindAudit)
	if err != nil || !enqueued {
		t.Fatalf("enqueue: %d %v %v", id, enqueued, err)
	}
	again, enqueued, err := s.EnqueueKindNow(ctx, JobKindAudit)
	if err != nil || enqueued || again != id {
		t.Fatalf("second enqueue: %d %v %v", again, enqueued, err)
	}
	// A failed job of another kind (attempts used up).
	var failed int64
	if err := s.QueryRow(ctx, `INSERT INTO jobs(kind,status,attempt,max_attempts,error,finished_at) VALUES($1,'failed',3,3,'boom',now()) RETURNING id`, JobKindWatchStats).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	kinds, items, err := s.AdminJobQueue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byKind := map[string]QueueKind{}
	for _, k := range kinds {
		byKind[k.Kind] = k
	}
	if byKind[JobKindAudit].Pending != 1 || byKind[JobKindWatchStats].Failed7d != 1 || len(items) != 2 {
		t.Fatalf("queue %+v %+v", kinds, items)
	}

	// Retry: back to pending with one more attempt; not twice.
	j, err := s.RetryJob(ctx, failed)
	if err != nil || j.Status != "pending" || j.MaxAttempts != 4 || j.Error != "" || j.FinishedAt != nil {
		t.Fatalf("retry %+v %v", j, err)
	}
	var ce *ConflictError
	if _, err := s.RetryJob(ctx, failed); !errors.As(err, &ce) {
		t.Errorf("retry of a pending job: %v", err)
	}
	if _, err := s.RetryJob(ctx, 99999999); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("retry unknown: %v", err)
	}
	// Retrying while another job of the kind is active is a conflict.
	var failedAudit int64
	if err := s.QueryRow(ctx, `INSERT INTO jobs(kind,status,attempt,finished_at) VALUES($1,'failed',3,now()) RETURNING id`, JobKindAudit).Scan(&failedAudit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryJob(ctx, failedAudit); !errors.As(err, &ce) {
		t.Errorf("retry beside an active job: %v", err)
	}

	// History: grouped by job, newest first, bounded by days.
	if _, err := s.Exec(ctx, `INSERT INTO nightly_runs(job,started_at,duration_ms,result) VALUES
		('audit.R-AGE',now()-interval '1 day',1200,'ok'),('audit.R-AGE',now()-interval '2 days',900,'issues'),
		('audit.R-AGE',now()-interval '40 days',10,'ok'),('recs.similarity',now()-interval '3 hours',5000,'failed')`); err != nil {
		t.Fatal(err)
	}
	series, err := s.AdminJobRuns(ctx, 30, "")
	if err != nil || len(series) != 2 || series[0].Job != "audit.R-AGE" || len(series[0].Runs) != 2 || series[0].Runs[0].DurationMS != 1200 {
		t.Fatalf("runs %+v %v", series, err)
	}
	one, err := s.AdminJobRuns(ctx, 90, "audit.R-AGE")
	if err != nil || len(one) != 1 || len(one[0].Runs) != 3 {
		t.Fatalf("one job %+v %v", one, err)
	}
	if _, err := s.AdminJobRuns(ctx, 0, ""); err == nil {
		t.Error("days 0 accepted")
	}
}

// TestReleaseRightsProofsIntegration covers 版权凭证: add, list, soft
// removal and the review_events rows, against an isolated database.
func TestReleaseRightsProofsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "rightsproofs")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var rel int64
	if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('rights-1','Rights',  'published') RETURNING id`).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddReleaseRightsProof(ctx, rel, RightsProofInput{}, "ed"); err == nil {
		t.Error("empty proof accepted")
	}
	if _, err := s.AddReleaseRightsProof(ctx, rel, RightsProofInput{Kind: "bogus", Reference: "x"}, "ed"); err == nil {
		t.Error("bad kind accepted")
	}
	if _, err := s.AddReleaseRightsProof(ctx, 99999999, RightsProofInput{Reference: "x"}, "ed"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown release: %v", err)
	}
	p, err := s.AddReleaseRightsProof(ctx, rel, RightsProofInput{Kind: "contract", Reference: " C-2026-01 ", URL: "https://docs.example/c", Note: "独家授权", ValidUntil: "2027-12-31"}, "ed")
	if err != nil || p.Reference != "C-2026-01" || p.ValidUntil == nil || *p.ValidUntil != "2027-12-31" || p.CreatedBy != "ed" {
		t.Fatalf("add %+v %v", p, err)
	}
	if _, err := s.AddReleaseRightsProof(ctx, rel, RightsProofInput{URL: "https://docs.example/inv"}, "ed"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ReleaseRightsProofs(ctx, rel, false)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	if _, err := s.RemoveReleaseRightsProof(ctx, rel, p.ID, "expired", "ed2"); err != nil {
		t.Fatal(err)
	}
	var ce *ConflictError
	if _, err := s.RemoveReleaseRightsProof(ctx, rel, p.ID, "", "ed2"); !errors.As(err, &ce) {
		t.Errorf("second removal: %v", err)
	}
	if list, _ := s.ReleaseRightsProofs(ctx, rel, false); len(list) != 1 {
		t.Errorf("after removal %d", len(list))
	}
	if list, _ := s.ReleaseRightsProofs(ctx, rel, true); len(list) != 2 || list[1].RemovedBy == nil || *list[1].RemovedBy != "ed2" {
		t.Errorf("with removed %+v", list)
	}
	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action IN ('add_rights_proof','remove_rights_proof')
		AND NOT (after_value::text LIKE '%C-2026%' OR before_value::text LIKE '%C-2026%')`, rel).Scan(&n); err != nil || n != 3 {
		t.Errorf("review events %d %v", n, err)
	}
}

// TestReleaseFromTermIntegration covers AdminSearch 补片 / 建预约页.
func TestReleaseFromTermIntegration(t *testing.T) {
	openIsolatedTestDB(t, "releasestub")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	draft, err := s.CreateReleaseFromTerm(ctx, ReleaseStubInput{Term: "abc-123", Title: "Some Title", Code: "ABC-123"}, "ed")
	if err != nil || draft.Status != ReleaseDraft || draft.Action != "add_release" || draft.Slug != "abc-123" || draft.EntityID == 0 {
		t.Fatalf("draft %+v %v", draft, err)
	}
	var ce *ConflictError
	if _, err := s.CreateReleaseFromTerm(ctx, ReleaseStubInput{Term: "abc123", Title: "Again", Code: "abc 123"}, "ed"); !errors.As(err, &ce) {
		t.Errorf("same code again: %v", err)
	}
	at := time.Now().Add(48 * time.Hour)
	up, err := s.CreateReleaseFromTerm(ctx, ReleaseStubInput{Term: "新作", Title: "新作", ScheduledAt: &at, Reason: "zero results"}, "ed")
	if err != nil || up.Status != ReleaseUpcoming || up.Action != "create_upcoming" || !strings.Contains(up.Slug, "-manual-") {
		t.Fatalf("upcoming %+v %v", up, err)
	}
	var status string
	var sched *time.Time
	if err := s.QueryRow(ctx, `SELECT status,scheduled_at FROM releases WHERE id=$1`, up.EntityID).Scan(&status, &sched); err != nil || status != "upcoming" || sched == nil {
		t.Fatalf("stored %q %v %v", status, sched, err)
	}
	past := time.Now().Add(-time.Hour)
	if _, err := s.CreateReleaseFromTerm(ctx, ReleaseStubInput{Term: "x", Title: "x", ScheduledAt: &past}, "ed"); err == nil {
		t.Error("past scheduled_at accepted")
	}
	var n int
	if err := s.QueryRow(ctx, `SELECT count(*) FROM review_events WHERE entity_type='release' AND action IN ('add_release','create_upcoming') AND actor='ed'`).Scan(&n); err != nil || n != 2 {
		t.Errorf("review events %d %v", n, err)
	}
	if err := s.QueryRow(ctx, `SELECT count(*) FROM release_identifiers WHERE release_id=$1 AND identifier_type='dvd_code'`, draft.EntityID).Scan(&n); err != nil || n != 1 {
		t.Errorf("identifier %d %v", n, err)
	}
}

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
