package store

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestTakedownMailIntegration covers migration 048's mail_outbox: every
// takedown step queues the claimant / counter-notifier mail in its
// transaction, no address is stored in the outbox, the claim resolves the
// recipient from the ticket in the submission's language, and rows go with
// the ticket.
func TestTakedownMailIntegration(t *testing.T) {
	openIsolatedTestDB(t, "takedown_mail")
	ctx := context.Background()
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
	type row struct{ recipient, kind, status string }
	rows := func(id int64) []row {
		t.Helper()
		out, err := collectRows(ctx, s, `SELECT recipient,kind,payload->>'status' FROM mail_outbox WHERE takedown_id=$1 ORDER BY id`, []any{id},
			func(r pgx.Rows) (row, error) {
				var x row
				return x, r.Scan(&x.recipient, &x.kind, &x.status)
			})
		must(err)
		return out
	}
	_, err = s.Exec(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES('tm-001','T','published')`)
	must(err)

	receipt, err := s.CreateTakedown(ctx, TakedownInput{ClaimantName: "Claimant", ClaimantEmail: "claim@studio.example", RightsOwner: "Studio",
		WorkDescription: "Film", InfringingURLs: []string{"https://reel.example/releases/tm-001"}, GoodFaith: true, Accuracy: true, Signature: "C", Lang: "ja"}, time.Now())
	must(err)
	var id int64
	must(s.QueryRow(ctx, `SELECT id FROM takedown_requests WHERE ticket=$1`, receipt.Ticket).Scan(&id))
	if got := rows(id); !slices.Equal(got, []row{{"claimant", MailTakedownReceived, "received"}}) {
		t.Fatalf("after filing: %v", got)
	}

	// Claim resolves the address, name and language from the ticket.
	items, err := s.ClaimMailOutbox(ctx, 10)
	must(err)
	if len(items) != 1 || items[0].Email != "claim@studio.example" || items[0].Name != "Claimant" || items[0].Lang != "ja" ||
		items[0].Ticket != receipt.Ticket || items[0].Attempt != 1 || items[0].Status != "received" {
		t.Fatalf("claimed = %+v", items)
	}
	if again, _ := s.ClaimMailOutbox(ctx, 10); len(again) != 0 {
		t.Errorf("claimed twice: %+v", again)
	}
	ok, err := s.FinishMailOutbox(ctx, items[0], OutboxResult{Status: OutboxSent})
	must(err)
	if !ok {
		t.Error("finish lost its lease")
	}

	_, err = s.SetTakedownStatus(ctx, id, TakedownReviewing, "", "dave")
	must(err)
	_, err = s.TakedownAction(ctx, id, TakedownActionInput{Action: TakedownUnpublishRelease, ReleaseSlug: "tm-001"}, "dave")
	must(err)
	_, err = s.SubmitCounterNotice(ctx, receipt.Ticket, CounterNoticeInput{Name: "Dist", Email: "dist@example.com", Address: "Tokyo",
		Statement: "licensed", Consent: true, Signature: "D", Lang: "es"})
	must(err)
	_, err = s.RestoreTakedown(ctx, id, "no suit filed", "erin")
	must(err)
	_, err = s.SetTakedownStatus(ctx, id, TakedownClosed, "done", "erin")
	must(err)
	want := []row{
		{"claimant", MailTakedownReceived, "received"},
		{"claimant", MailTakedownStatus, "reviewing"},
		{"claimant", MailTakedownStatus, "actioned"},
		{"counter", MailTakedownCounter, "counter_received"},
		{"claimant", MailTakedownStatus, "counter_received"},
		{"claimant", MailTakedownStatus, "restored"},
		{"counter", MailTakedownStatus, "restored"},
		{"claimant", MailTakedownStatus, "closed"},
		{"counter", MailTakedownStatus, "closed"},
	}
	if got := rows(id); !slices.Equal(got, want) {
		t.Fatalf("mail rows =\n%v\nwant\n%v", got, want)
	}
	// No contact details are copied into the outbox.
	var leaked int
	must(s.QueryRow(ctx, `SELECT count(*) FROM mail_outbox WHERE row_to_json(mail_outbox)::text ~* '@|claimant name|dist'`).Scan(&leaked))
	if leaked != 0 {
		t.Errorf("%d outbox rows carry contact details", leaked)
	}
	items, err = s.ClaimMailOutbox(ctx, 20)
	must(err)
	for _, it := range items {
		wantEmail, wantLang := "claim@studio.example", "ja"
		if it.Recipient == RecipientCounter {
			wantEmail, wantLang = "dist@example.com", "es"
		}
		if it.Email != wantEmail || it.Lang != wantLang {
			t.Errorf("claimed %s %s -> %q %q", it.Recipient, it.Status, it.Email, it.Lang)
		}
	}
	// A retry goes back to pending with its send_after; a lost lease is a no-op.
	retry := items[0]
	ok, err = s.FinishMailOutbox(ctx, retry, OutboxResult{Status: OutboxPending, Error: strings.Repeat("x", 600), RetryAt: time.Now().Add(time.Hour)})
	must(err)
	if !ok {
		t.Error("retry finish failed")
	}
	if ok, _ = s.FinishMailOutbox(ctx, retry, OutboxResult{Status: OutboxSent}); ok {
		t.Error("finish after the lease was released must be a no-op")
	}
	var lastErr string
	must(s.QueryRow(ctx, `SELECT last_error FROM mail_outbox WHERE id=$1`, retry.ID).Scan(&lastErr))
	if len(lastErr) != 500 {
		t.Errorf("last_error length %d, want 500", len(lastErr))
	}
	// Stale leases come back.
	_, err = s.Exec(ctx, `UPDATE mail_outbox SET claimed_at=now()-interval '1 hour' WHERE status='sending'`)
	must(err)
	n, err := s.ReclaimStaleMailOutbox(ctx, 10*time.Minute)
	must(err)
	if n != int64(len(items)-1) {
		t.Errorf("reclaimed %d, want %d", n, len(items)-1)
	}

	// The rows go with the ticket (retention follows takedown_requests).
	_, err = s.Exec(ctx, `DELETE FROM takedown_requests WHERE id=$1`, id)
	must(err)
	if got := rows(id); len(got) != 0 {
		t.Errorf("mail rows after the ticket was deleted: %v", got)
	}
}
