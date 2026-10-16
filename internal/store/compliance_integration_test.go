package store

// Compliance tables end to end (migration 041): the legal v1 seed and its
// idempotence, versioning with language fallback, frozen published rows,
// append-only consent with GPC, feedback, and a copyright ticket through
// unpublish / suppress, counter-notice and restore with review_events.
// Runs only under `make test-integration`.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestComplianceIntegration(t *testing.T) {
	openIsolatedTestDB(t, "compliance")
	ctx := context.Background()
	for range 2 { // the second Open re-applies every migration (seed must not duplicate)
		s, err := Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) (n int64) {
		t.Helper()
		must(s.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}

	// ---- legal documents: seed
	if n := count(`SELECT count(*) FROM legal_documents WHERE version=1 AND lang='zh-CN' AND status='published'`); n != int64(len(LegalKinds)) {
		t.Fatalf("seeded %d v1 documents, want %d", n, len(LegalKinds))
	}
	cur, err := s.CurrentLegalVersion(ctx, "terms")
	must(err)
	if cur.Version != 1 {
		t.Errorf("terms current version = %+v", cur)
	}
	doc, err := s.LegalDocument(ctx, "privacy", "en", 0)
	must(err)
	if doc.Lang != "zh-CN" || !doc.Fallback || doc.Version != 1 || !strings.Contains(doc.Body, "{#collect}") || len(doc.Summary) != 3 {
		t.Errorf("privacy en = %+v", doc)
	}
	if _, err := s.LegalDocument(ctx, "privacy", "en", 9); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown version: %v", err)
	}
	// Published rows are frozen in the database itself.
	if _, err := s.Exec(ctx, `UPDATE legal_documents SET body='x' WHERE kind='terms' AND version=1`); err == nil {
		t.Error("a published document must not be editable")
	}

	// ---- legal documents: a new version and a translation
	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	v2, err := s.CreateLegalDraft(ctx, LegalDraftInput{Kind: "terms", Lang: "zh-CN", Title: "用户协议", Body: "## 账号规则 {#account}\n\n新的正文。\n", ChangeNote: "v2"}, "alice")
	must(err)
	if v2.Version != 2 || v2.Status != "draft" {
		t.Fatalf("draft = %+v", v2)
	}
	v2, err = s.UpdateLegalDraft(ctx, v2.ID, LegalDraftInput{Title: "用户协议", Summary: []string{"更新"}, Body: "## 账号规则 {#account}\n\n修订后的正文。\n"}, "alice")
	must(err)
	diff, err := s.LegalDocumentDiff(ctx, v2.ID, 0)
	must(err)
	if diff.Base == nil || diff.Base.Version != 1 || !slices.Contains(diff.Lines, DiffLine{"+", "修订后的正文。"}) {
		t.Errorf("diff = %+v", diff)
	}
	if _, err = s.PublishLegalDocument(ctx, v2.ID, &future, "", "alice"); err != nil {
		t.Fatal(err)
	}
	// Published for the future: v1 stays in force, v2 is listed as upcoming.
	if cur, _ = s.CurrentLegalVersion(ctx, "terms"); cur.Version != 1 {
		t.Errorf("a future version must not be current yet: %+v", cur)
	}
	versions, err := s.LegalVersions(ctx, "terms", "en")
	must(err)
	if len(versions) != 2 || versions[0].Version != 2 || versions[0].IsCurrent || !versions[1].IsCurrent {
		t.Errorf("versions = %+v", versions)
	}
	en, err := s.CreateLegalDraft(ctx, LegalDraftInput{Kind: "terms", Lang: "en", Version: 2, Title: "Terms", Body: "## Accounts {#account}\n\nText.\n"}, "bob")
	must(err)
	if _, err = s.PublishLegalDocument(ctx, en.ID, nil, "", "bob"); err != nil {
		t.Fatal(err)
	}
	got, err := s.LegalDocument(ctx, "terms", "en", 2)
	must(err)
	if got.Lang != "en" || got.Fallback || !got.EffectiveAt.Equal(future) {
		t.Errorf("translation = %+v (want effective %s)", got, future)
	}
	old, err := s.CreateLegalDraft(ctx, LegalDraftInput{Kind: "terms", Lang: "ja", Version: 1, Title: "利用規約", Body: "x"}, "bob")
	must(err)
	var ce *ConflictError
	if _, err = s.CreateLegalDraft(ctx, LegalDraftInput{Kind: "terms", Lang: "en", Version: 2, Title: "dup", Body: "x"}, "bob"); !errors.As(err, &ce) {
		t.Errorf("duplicate (kind, lang, version): %v", err)
	}
	if _, err = s.CreateLegalDraft(ctx, LegalDraftInput{Kind: "terms", Lang: "en", Version: 9, Title: "skip", Body: "x"}, "bob"); err == nil {
		t.Error("skipping versions must fail")
	}
	if _, err = s.PublishLegalDocument(ctx, old.ID, nil, "", "bob"); err != nil {
		t.Errorf("a translation of v1 publishes with v1's date: %v", err)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='legal_document' AND actor IN ('alice','bob')`); n < 6 {
		t.Errorf("legal review_events = %d", n)
	}

	// ---- consent
	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('consent@example.com','x') RETURNING id`).Scan(&uid))
	st, err := s.RecordConsent(ctx, ConsentRecord{DeviceHash: "abc123", Input: ConsentInput{Analytics: true, PersonalizedAds: true, GPC: true, Source: "banner"}})
	must(err)
	if st.PersonalizedAds || st.PolicyVersion != 1 || slices.Contains(st.Categories, ConsentAds) {
		t.Errorf("GPC consent = %+v", st)
	}
	if _, err = s.LatestConsent(ctx, uid); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("no consent yet: %v", err)
	}
	_, err = s.RecordConsent(ctx, ConsentRecord{UserID: &uid, Input: ConsentInput{Analytics: false, PersonalizedRecs: true, Source: "preferences"}})
	must(err)
	_, err = s.RecordConsent(ctx, ConsentRecord{UserID: &uid, DeviceHash: "dev2", Input: ConsentInput{Analytics: true, Source: "sync"}})
	must(err)
	latest, err := s.LatestConsent(ctx, uid)
	must(err)
	if !latest.Analytics || latest.PersonalizedRecs || latest.Source != "sync" {
		t.Errorf("latest = %+v", latest)
	}
	if _, err = s.Exec(ctx, `UPDATE consent_records SET categories='{necessary}'`); err == nil {
		t.Error("consent_records must be append-only")
	}
	if _, err = s.Exec(ctx, `INSERT INTO consent_records(device_hash,categories,gpc,source) VALUES('x','{necessary,personalized_ads}',true,'gpc')`); err == nil {
		t.Error("the database must refuse personalized_ads under GPC")
	}
	if n := count(`SELECT count(*) FROM consent_records WHERE device_hash LIKE '%.%' OR device_hash ~ '^[0-9.:]+$'`); n != 0 {
		t.Errorf("%d consent rows look like addresses", n)
	}

	// ---- feedback
	fb, err := s.CreateFeedback(ctx, FeedbackInput{Category: "bug", Message: "stalls", Email: "fb@example.com", PageURL: "/releases/x?t=1"})
	must(err)
	list, _, err := s.AdminFeedback(ctx, FeedbackFilter{Status: "open"})
	must(err)
	if len(list) != 1 || list[0].PageURL != "/releases/x" || list[0].Email != "fb@example.com" {
		t.Errorf("feedback list = %+v", list)
	}
	if _, err = s.SetFeedbackStatus(ctx, fb.ID, "resolved", "fixed", "carol"); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM review_events WHERE entity_type='feedback' AND entity_id=$1 AND actor='carol' AND NOT (after_value::text LIKE '%fb@example.com%')`, fb.ID); n != 1 {
		t.Errorf("feedback events = %d", n)
	}

	// ---- takedown: notice -> unpublish + suppress -> counter -> restore
	var relID, assetID int64
	must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('td-001','Takedown title','published') RETURNING id`).Scan(&relID))
	must(s.QueryRow(ctx, `INSERT INTO media_assets(entity_type,entity_id,asset_type,object_key,content_sha256,mime_type) VALUES('release',$1,'cover','td-cover','td-cover','image/jpeg') RETURNING id`, relID).Scan(&assetID))
	now := time.Now()
	receipt, err := s.CreateTakedown(ctx, TakedownInput{ClaimantName: "Claimant Name", ClaimantEmail: "Claim@Studio.example", RightsOwner: "Studio",
		Relationship: "agent", WorkDescription: "Film", InfringingURLs: []string{"https://reel.example/releases/td-001"}, GoodFaith: true, Accuracy: true, Signature: "CN"}, now)
	must(err)
	if _, ok := NormalizeTicket(receipt.Ticket); !ok || receipt.DueAt.Sub(now) != TakedownSLA {
		t.Errorf("receipt = %+v", receipt)
	}
	if _, err = s.TakedownStatus(ctx, receipt.Ticket, "someone@else.example"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("wrong email must look like an unknown ticket: %v", err)
	}
	pub, err := s.TakedownStatus(ctx, strings.ToLower(receipt.Ticket), "claim@studio.example")
	must(err)
	if pub.Status != TakedownReceived || pub.CounterAllowed {
		t.Errorf("public status = %+v", pub)
	}
	var tdID int64
	must(s.QueryRow(ctx, `SELECT id FROM takedown_requests WHERE ticket=$1`, receipt.Ticket).Scan(&tdID))
	if _, err = s.SubmitCounterNotice(ctx, receipt.Ticket, CounterNoticeInput{Name: "D", Email: "d@example.com", Address: "A", Statement: "S", Consent: true, Signature: "D"}); !errors.As(err, &ce) {
		t.Errorf("counter-notice before any action: %v", err)
	}
	// Overdue once past the SLA without action.
	must(execErr(s.Exec(ctx, `UPDATE takedown_requests SET due_at=now()-interval '1 hour' WHERE id=$1`, tdID)))
	counts, err := s.AdminTakedownCounts(ctx)
	must(err)
	if counts.Open != 1 || counts.Overdue != 1 {
		t.Errorf("counts = %+v", counts)
	}
	td, err := s.TakedownAction(ctx, tdID, TakedownActionInput{Action: TakedownUnpublishRelease, ReleaseSlug: "td-001", Reason: "notice"}, "dave")
	must(err)
	if td.Status != TakedownActioned || td.Overdue || td.FirstActionAt == nil || td.ActiveTargets != 1 {
		t.Errorf("after unpublish = %+v", td)
	}
	if _, err = s.TakedownAction(ctx, tdID, TakedownActionInput{Action: TakedownUnpublishRelease, ReleaseID: relID}, "dave"); !errors.As(err, &ce) {
		t.Errorf("a second identical action: %v", err)
	}
	td, err = s.TakedownAction(ctx, tdID, TakedownActionInput{Action: TakedownSuppressImage, MediaAssetID: assetID}, "dave")
	must(err)
	var status string
	must(s.QueryRow(ctx, `SELECT status FROM releases WHERE id=$1`, relID).Scan(&status))
	if status != ReleaseUnpublished || count(`SELECT count(*) FROM media_assets WHERE id=$1 AND suppressed_at IS NOT NULL`, assetID) != 1 {
		t.Errorf("release %s / asset not suppressed", status)
	}
	down, err := s.ReleaseTakenDown(ctx, "td-001")
	must(err)
	if !down {
		t.Error("release must report taken down")
	}
	// The reused paths wrote their own events next to the ticket's.
	if count(`SELECT count(*) FROM review_events WHERE entity_type='release' AND entity_id=$1 AND action IN ('status_change','media_suppress') AND actor='dave'`, relID) != 2 {
		t.Error("release / media review_events missing")
	}
	pub, err = s.SubmitCounterNotice(ctx, receipt.Ticket, CounterNoticeInput{Name: "Distributor", Email: "dist@example.com", Address: "Tokyo", Statement: "licensed", Consent: true, Signature: "D"})
	must(err)
	if pub.Status != TakedownCounter {
		t.Errorf("after counter = %+v", pub)
	}
	if _, err = s.TakedownStatus(ctx, receipt.Ticket, "DIST@example.com"); err != nil {
		t.Errorf("the counter-notifier can look the ticket up: %v", err)
	}
	td, err = s.RestoreTakedown(ctx, tdID, "counter-notice, no suit filed", "erin")
	must(err)
	must(s.QueryRow(ctx, `SELECT status FROM releases WHERE id=$1`, relID).Scan(&status))
	if td.Status != TakedownRestored || td.ActiveTargets != 0 || status != ReleasePublished || count(`SELECT count(*) FROM media_assets WHERE id=$1 AND suppressed_at IS NULL`, assetID) != 1 {
		t.Errorf("after restore: ticket %s, release %s, targets %d", td.Status, status, td.ActiveTargets)
	}
	if down, _ = s.ReleaseTakenDown(ctx, "td-001"); down {
		t.Error("restored release must not report taken down")
	}
	actions := []string{}
	for _, e := range td.Events {
		actions = append(actions, e.Action)
	}
	if !slices.Equal(actions, []string{"takedown_unpublish", "takedown_suppress", "takedown_counter_notice", "takedown_restore"}) {
		t.Errorf("ticket events = %v", actions)
	}
	if _, err = s.SetTakedownStatus(ctx, tdID, TakedownClosed, "done", "erin"); err != nil {
		t.Errorf("close: %v", err)
	}
}

func execErr(_ any, err error) error { return err }
