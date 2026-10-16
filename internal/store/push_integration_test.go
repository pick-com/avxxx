package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

// openIntegration opens AVXXX_TEST_DATABASE_URL (twice: migrations must be
// re-runnable) or skips. Never DATABASE_URL.
func openIntegration(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = Open(ctx); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// TestPushOutboxIntegration exercises migration 018, push subscriptions and
// the outbox claim / finish / lease SQL against a disposable database.
func TestPushOutboxIntegration(t *testing.T) {
	s := openIntegration(t)
	ctx := context.Background()
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	sfx := fmt.Sprint(time.Now().UnixNano())
	var u1, u2 int64
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES($1,'x','One') RETURNING id`, "p1"+sfx+"@t").Scan(&u1)
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "p2"+sfx+"@t").Scan(&u2)
	// Only this test's rows are claimable.
	mustExec(`UPDATE notification_outbox SET status='cancelled' WHERE status IN ('pending','sending')`)

	// --- subscriptions ---
	ep := "https://fcm.googleapis.com/fcm/send/" + sfx
	if err := s.SavePushSubscription(ctx, u2, ep, "k1", "a1", "ua"); err != nil {
		t.Fatal(err)
	}
	if err := s.SavePushSubscription(ctx, u1, ep, "k2", "a2", "ua2"); err != nil { // moves to u1
		t.Fatal(err)
	}
	subs, _ := s.PushSubscriptions(ctx, u2)
	if len(subs) != 0 {
		t.Fatalf("endpoint must move to the new user: %v", subs)
	}
	subs, _ = s.PushSubscriptions(ctx, u1)
	if len(subs) != 1 || subs[0].P256dh != "k2" || subs[0].Auth != "a2" {
		t.Fatalf("u1 subs = %+v", subs)
	}
	id := subs[0].ID
	if dropped, err := s.PushFailed(ctx, id, 2); dropped || err != nil {
		t.Fatalf("first failure: %v %v", dropped, err)
	}
	if err := s.PushDelivered(ctx, id); err != nil { // resets failures
		t.Fatal(err)
	}
	var failures int
	var lastOK *time.Time
	s.QueryRow(ctx, `SELECT failures,last_success_at FROM push_subscriptions WHERE id=$1`, id).Scan(&failures, &lastOK)
	if failures != 0 || lastOK == nil {
		t.Fatalf("after success failures=%d last_success_at=%v", failures, lastOK)
	}
	s.PushFailed(ctx, id, 2)
	if dropped, _ := s.PushFailed(ctx, id, 2); !dropped {
		t.Fatal("second consecutive failure must drop the subscription")
	}
	if subs, _ = s.PushSubscriptions(ctx, u1); len(subs) != 0 {
		t.Fatalf("dropped sub still listed: %v", subs)
	}
	s.SavePushSubscription(ctx, u1, ep, "k", "a", "")
	s.SavePushSubscription(ctx, u1, ep+"-b", "k", "a", "")
	if n, _ := s.DeletePushSubscription(ctx, u2, ep); n != 0 {
		t.Fatal("deleting another user's subscription must be a no-op")
	}
	if n, _ := s.DeletePushSubscription(ctx, u1, ep); n != 1 {
		t.Fatalf("delete one = %d", n)
	}
	if n, _ := s.DeletePushSubscription(ctx, u1, ""); n != 1 {
		t.Fatalf("delete all = %d", n)
	}
	s.SavePushSubscription(ctx, u1, ep, "k", "a", "")
	subs, _ = s.PushSubscriptions(ctx, u1)
	if err := s.PushGone(ctx, subs[0].ID); err != nil {
		t.Fatal(err)
	}

	// --- outbox ---
	mustExec(`INSERT INTO user_settings(user_id,ui_lang) VALUES($1,'en')`, u1)
	newRow := func(uid int64, channel, status string, sendAfter time.Duration) int64 {
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO notification_outbox(user_id,channel,payload,send_after,status)
			VALUES($1,$2,'{"kind":"new_work","items":[{"slug":"x","title":"X","reason":"follow"}],"count":1}',now()+$3::interval,$4) RETURNING id`,
			uid, channel, fmt.Sprintf("%d seconds", int64(sendAfter/time.Second)), status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	email := newRow(u1, "email", "pending", -time.Minute)
	future := newRow(u1, "push", "pending", time.Hour)
	newRow(u1, "push", "sent", -time.Minute)
	var push []int64
	for i := 0; i < 11; i++ {
		uid := u1
		if i%2 == 1 {
			uid = u2
		}
		push = append(push, newRow(uid, "push", "pending", -time.Minute))
	}

	items, err := s.ClaimOutbox(ctx, []string{"email"}, 10)
	if err != nil || len(items) != 1 || items[0].ID != email {
		t.Fatalf("claim email = %+v, %v", items, err)
	}
	it := items[0]
	if it.Attempt != 1 || it.Email != "p1"+sfx+"@t" || it.DisplayName != "One" || it.Settings.UILang != "en" || it.ClaimedAt.IsZero() {
		t.Fatalf("claimed item = %+v", it)
	}
	if ok, err := s.FinishOutbox(ctx, it, OutboxResult{Status: OutboxSent, Error: "ignored"}); !ok || err != nil {
		t.Fatalf("finish sent: %v %v", ok, err)
	}
	var status, lastErr string
	var sentAt *time.Time
	s.QueryRow(ctx, `SELECT status,last_error,sent_at FROM notification_outbox WHERE id=$1`, email).Scan(&status, &lastErr, &sentAt)
	if status != "sent" || lastErr != "" || sentAt == nil {
		t.Fatalf("sent row: %s %q %v", status, lastErr, sentAt)
	}
	if ok, _ := s.FinishOutbox(ctx, it, OutboxResult{Status: OutboxFailed}); ok {
		t.Fatal("finishing a row twice must be a no-op")
	}

	// Two concurrent claimers never get the same row (FOR UPDATE SKIP LOCKED).
	var mu sync.Mutex
	var claimed []OutboxItem
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.ClaimOutbox(ctx, []string{"push", "email"}, 4)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			claimed = append(claimed, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	// A claimer can come up short when a row it saw was taken concurrently;
	// a final sequential claim picks up whatever is left.
	rest, err := s.ClaimOutbox(ctx, []string{"push"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	claimed = append(claimed, rest...)
	ids := map[int64]OutboxItem{}
	for _, c := range claimed {
		if _, dup := ids[c.ID]; dup {
			t.Fatalf("row %d claimed twice", c.ID)
		}
		ids[c.ID] = c
	}
	if len(ids) != len(push) {
		t.Fatalf("claimed %d rows, want %d (future/sent rows must not be claimed)", len(ids), len(push))
	}
	if _, ok := ids[future]; ok {
		t.Fatal("future row claimed")
	}
	if more, _ := s.ClaimOutbox(ctx, []string{"push"}, 10); len(more) != 0 {
		t.Fatalf("rows claimed while all are sending: %v", more)
	}
	sort.Slice(push, func(i, j int) bool { return push[i] < push[j] })

	// Retry: back to pending with a later send_after and last_error.
	retry := ids[push[0]]
	if ok, err := s.FinishOutbox(ctx, retry, OutboxResult{Status: OutboxPending, Error: "push x: HTTP 429",
		RetryAt: time.Now().Add(time.Hour)}); !ok || err != nil {
		t.Fatalf("finish retry: %v %v", ok, err)
	}
	var attempt int
	var after time.Time
	s.QueryRow(ctx, `SELECT status,last_error,attempt,send_after FROM notification_outbox WHERE id=$1`, retry.ID).Scan(&status, &lastErr, &attempt, &after)
	if status != "pending" || lastErr != "push x: HTTP 429" || attempt != 1 || after.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("retry row: %s %q %d %v", status, lastErr, attempt, after)
	}

	// Failed and cancelled keep last_error.
	failed := ids[push[1]]
	s.FinishOutbox(ctx, failed, OutboxResult{Status: OutboxFailed, Error: "push x: HTTP 403"})
	cancelled := ids[push[2]]
	s.FinishOutbox(ctx, cancelled, OutboxResult{Status: OutboxCancelled, Error: "no push subscriptions"})
	for id, want := range map[int64]string{failed.ID: "failed", cancelled.ID: "cancelled"} {
		s.QueryRow(ctx, `SELECT status,last_error FROM notification_outbox WHERE id=$1`, id).Scan(&status, &lastErr)
		if status != want || lastErr == "" {
			t.Errorf("row %d: %s %q", id, status, lastErr)
		}
	}
	if _, err := s.FinishOutbox(ctx, failed, OutboxResult{Status: "bogus"}); err == nil {
		t.Error("bad status must fail")
	}

	// Lease expiry: a stuck row is reclaimed; the old holder's Finish is dropped.
	stuck := ids[push[4]] // even index: owned by u1 (ui_lang en)
	mustExec(`UPDATE notification_outbox SET claimed_at=now()-interval '20 minutes' WHERE id=$1`, stuck.ID)
	if n, err := s.ReclaimStaleOutbox(ctx, 10*time.Minute); n != 1 || err != nil {
		t.Fatalf("reclaim = %d, %v", n, err)
	}
	if ok, _ := s.FinishOutbox(ctx, stuck, OutboxResult{Status: OutboxSent}); ok {
		t.Fatal("finish after losing the lease must be a no-op")
	}
	again, _ := s.ClaimOutbox(ctx, []string{"push"}, 10)
	if len(again) != 1 || again[0].ID != stuck.ID || again[0].Attempt != 2 {
		t.Fatalf("reclaimed row claim = %+v", again)
	}
	if again[0].Settings.UILang != "en" {
		t.Errorf("settings not loaded: %+v", again[0].Settings)
	}
	if n, _ := s.ReclaimStaleOutbox(ctx, 10*time.Minute); n != 0 {
		t.Fatal("fresh leases must not be reclaimed")
	}
	if truncate("你好世界", 4) != "你" {
		t.Errorf("truncate splits UTF-8: %q", truncate("你好世界", 4))
	}
}
