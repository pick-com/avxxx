package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// TestSocialIntegration exercises migration 014 and the social / fan-out SQL
// against a real, disposable PostgreSQL database. It is skipped unless
// AVXXX_TEST_DATABASE_URL is set; the database is modified.
func TestSocialIntegration(t *testing.T) {
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
	if s, err = Open(ctx); err != nil { // migrations must be re-runnable
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()

	sfx := fmt.Sprint(time.Now().UnixNano())
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	var uid, other, lead, guest int64
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "a"+sfx+"@t").Scan(&uid)
	s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES($1,'x') RETURNING id`, "b"+sfx+"@t").Scan(&other)
	s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,'Lead') RETURNING id`, "lead-"+sfx).Scan(&lead)
	s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,'Guest') RETURNING id`, "guest-"+sfx).Scan(&guest)
	newRelease := func(slug, status string, date *time.Time, sched *time.Time) int64 {
		var id int64
		if err := s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date,scheduled_at) VALUES($1,$1,$2,$3,$4) RETURNING id`,
			slug, status, date, sched).Scan(&id); err != nil {
			t.Fatal(err)
		}
		mustExec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,0,true),($1,$3,1,false)`, id, lead, guest)
		return id
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	past := time.Now().Add(-time.Minute)
	newRelease("pub-"+sfx, "published", &today, nil)
	newRelease("up-"+sfx, "upcoming", &today, &past)
	newRelease("later-"+sfx, "upcoming", nil, nil)
	// Only our releases are pending; anything else in the DB was notified earlier.
	mustExec(`UPDATE releases SET notified_at=now() WHERE notified_at IS NULL AND status='published' AND slug NOT LIKE '%'||$1`, sfx)

	// Follows: user follows the guest with lead_only (no notice), the lead with all.
	if _, err := s.Follow(ctx, uid, "guest-"+sfx, NotifyLeadOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Follow(ctx, uid, "lead-"+sfx, NotifyAll); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Follow(ctx, other, "guest-"+sfx, NotifyLeadOnly); err != nil {
		t.Fatal(err)
	}
	if err := s.Reserve(ctx, other, "pub-"+sfx); !errors.Is(err, ErrReleaseNotUpcoming) {
		t.Fatalf("reserve published: %v", err)
	}
	if err := s.Reserve(ctx, other, "up-"+sfx); err != nil {
		t.Fatal(err)
	}
	feed, next, err := s.FollowingFeed(ctx, uid, nil, 2)
	if err != nil || len(feed) != 2 || next == "" {
		t.Fatalf("feed page 1: %d items next=%q err=%v", len(feed), next, err)
	}
	c, _ := DecodeCursor(next)
	feed2, _, err := s.FollowingFeed(ctx, uid, &c, 2)
	if err != nil || len(feed2) != 1 {
		t.Fatalf("feed page 2: %v %v", feed2, err)
	}
	// 按演员筛选: one followed performer; unknown slugs are ErrNoRows.
	if only, _, err := s.FollowingFeedFor(ctx, uid, "lead-"+sfx, nil, 10); err != nil || len(only) != 3 {
		t.Fatalf("feed for lead: %d items, err=%v", len(only), err)
	}
	if _, _, err := s.FollowingFeedFor(ctx, uid, "nobody-"+sfx, nil, 10); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("feed for unknown performer: %v", err)
	}

	res, err := s.NotifyFanout(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if res.Promoted < 1 || res.Releases < 2 {
		t.Fatalf("fanout = %+v", res)
	}
	again, err := s.NotifyFanout(ctx, 50)
	if err != nil || again.Releases != 0 {
		t.Fatalf("second fanout = %+v %v", again, err)
	}
	// uid: two releases merged into one notification; other: reservation only.
	items, _, err := s.Notifications(ctx, uid, nil, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("uid notifications = %v %v", items, err)
	}
	var payload struct {
		Count int `json:"count"`
	}
	json.Unmarshal(items[0]["payload"].(json.RawMessage), &payload)
	if payload.Count != 2 {
		t.Errorf("merged count = %d, want 2", payload.Count)
	}
	if n, _ := s.UnreadNotifications(ctx, other); n != 1 {
		t.Errorf("other unread = %d, want 1", n)
	}
	var outbox int
	s.QueryRow(ctx, `SELECT count(*) FROM notification_outbox WHERE user_id IN ($1,$2) AND channel='push'`, uid, other).Scan(&outbox)
	if outbox != 2 {
		t.Errorf("push outbox rows = %d, want 2", outbox)
	}
	if n, err := s.MarkNotificationsRead(ctx, uid, nil, true); err != nil || n != 1 {
		t.Errorf("mark all = %d %v", n, err)
	}
	if fl, err := s.Follows(ctx, uid); err != nil || len(fl) != 2 {
		t.Errorf("follows = %v %v", fl, err)
	}

	// Settings
	u, err := s.PatchSettings(ctx, uid, []byte(`{"history_paused":true,"theme":"dark","quiet_hours":null}`))
	if err != nil || !u.HistoryPaused || u.QuietHours != nil {
		t.Fatalf("patch settings = %+v %v", u, err)
	}
	if got, _ := s.Settings(ctx, uid); got.Theme != "dark" || got.QuietHours != nil {
		t.Errorf("settings = %+v", got)
	}
	if p, _ := s.HistoryPaused(ctx, uid); !p {
		t.Error("history not paused")
	}

	// Watch later
	newRelease("pub2-"+sfx, "published", &today, nil)
	for _, slug := range []string{"pub-" + sfx, "up-" + sfx, "pub2-" + sfx} {
		if err := s.AddWatchLater(ctx, uid, slug); err != nil {
			t.Fatalf("add %s: %v", slug, err)
		}
	}
	if err := s.ReorderWatchLater(ctx, uid, []string{"pub2-" + sfx}); err != nil {
		t.Fatal(err)
	}
	wl, _ := s.WatchLater(ctx, uid)
	if len(wl) != 3 || wl[0]["slug"] != "pub2-"+sfx {
		t.Errorf("watch later = %v", wl)
	}
	if err := s.SaveWatch(ctx, uid, "pub-"+sfx, 10, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteWatch(ctx, uid, "pub-"+sfx); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ClearWatchHistory(ctx, uid); err != nil || n != 0 {
		t.Errorf("clear = %d %v", n, err)
	}
}
