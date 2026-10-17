package store

import (
	"avxxx/internal/recs"
	"context"
	"errors"
	"slices"
	"testing"
)

// TestRecFeedbackClaimIntegration covers moving a visitor device's 推荐反馈
// to the account on sign-in (ClaimRecFeedback): new rows move, the unique
// (owner, kind, target) dedupes against the account's own rows (an active
// one is kept, one undone before the device gave it is re-activated, one
// undone after stays undone), undone device rows are dropped, the device
// is left with nothing, recall then applies the claimed rows for the
// account, and a second claim is a no-op.
func TestRecFeedbackClaimIntegration(t *testing.T) {
	openIsolatedTestDB(t, "recfbclaim")
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
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	ids := map[string]int64{}
	for _, slug := range []string{"cl-r1", "cl-r2", "cl-r3"} {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$1,'published',current_date - 5) RETURNING id`, slug).Scan(&id))
		ids[slug] = id
	}
	var pa int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES('cl-pa','cl-pa') RETURNING id`).Scan(&pa))
	ids["cl-pa"] = pa
	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('claim@t','x') RETURNING id`).Scan(&uid))
	user := RecFeedbackOwner{UserID: uid}
	device := RecFeedbackOwner{DeviceID: "claim-device-0123456789"}

	add := func(o RecFeedbackOwner, kind, target string) {
		t.Helper()
		_, err := s.AddRecFeedback(ctx, o, kind, target)
		must(err)
	}
	undo := func(o RecFeedbackOwner, kind, target string) {
		t.Helper()
		ok, err := s.UndoRecFeedback(ctx, o, kind, target)
		must(err)
		if !ok {
			t.Fatalf("undo %s %s: nothing active", kind, target)
		}
	}
	// at sets created_at / undone_at to `ago` before now, so the order of the
	// account's and the device's choices is explicit.
	at := func(o RecFeedbackOwner, kind, target, col, ago string) {
		t.Helper()
		var tag *string
		id := ids[target]
		if kind == recs.FeedbackTag {
			tag = &target
		}
		exec(`UPDATE rec_feedback SET `+col+`=now() - $5::interval
			WHERE owner_key=$1 AND kind=$2 AND COALESCE(target_id,0)=COALESCE($3::bigint,0) AND COALESCE(target_tag,'')=COALESCE($4::text,'')`,
			o.Key(), kind, idArg(id), tag, ago)
	}

	// The account: cl-r2 active (3 days old), comedy undone two days ago
	// (before the device gave it), cl-pa undone just now (after).
	add(user, recs.FeedbackRelease, "cl-r2")
	at(user, recs.FeedbackRelease, "cl-r2", "created_at", "3 days")
	add(user, recs.FeedbackTag, "comedy")
	undo(user, recs.FeedbackTag, "comedy")
	at(user, recs.FeedbackTag, "comedy", "undone_at", "2 days")
	add(user, recs.FeedbackPerformer, "cl-pa")
	undo(user, recs.FeedbackPerformer, "cl-pa")

	// The device: cl-r1 (new), cl-r2 (the account has it), comedy and cl-pa
	// (a day ago), cl-r3 given and undone.
	add(device, recs.FeedbackRelease, "cl-r1")
	add(device, recs.FeedbackRelease, "cl-r2")
	add(device, recs.FeedbackTag, "comedy")
	at(device, recs.FeedbackTag, "comedy", "created_at", "1 day")
	add(device, recs.FeedbackPerformer, "cl-pa")
	at(device, recs.FeedbackPerformer, "cl-pa", "created_at", "1 day")
	add(device, recs.FeedbackRelease, "cl-r3")
	undo(device, recs.FeedbackRelease, "cl-r3")

	n, err := s.ClaimRecFeedback(ctx, uid, device.DeviceID)
	must(err)
	if n != 2 { // cl-r1 moved, comedy re-activated
		t.Errorf("claimed %d, want 2", n)
	}
	list, err := s.ListRecFeedback(ctx, user)
	must(err)
	var got []string
	for _, f := range list {
		got = append(got, f.Kind+":"+f.Target)
	}
	slices.Sort(got)
	want := []string{recs.FeedbackRelease + ":cl-r1", recs.FeedbackRelease + ":cl-r2", recs.FeedbackTag + ":comedy"}
	if !slices.Equal(got, want) {
		t.Errorf("account feedback %v, want %v", got, want)
	}

	// One row per target (the unique index), and the account's older active
	// cl-r2 kept its own date.
	var rows, oldR2 int
	must(s.QueryRow(ctx, `SELECT count(*)::int, count(*) FILTER (WHERE target_id=$2 AND created_at < now() - interval '2 days')::int
		FROM rec_feedback WHERE owner_key=$1`, user.Key(), ids["cl-r2"]).Scan(&rows, &oldR2))
	if rows != 4 || oldR2 != 1 { // cl-r1, cl-r2, comedy, cl-pa (still undone)
		t.Errorf("account rows %d (old cl-r2 %d), want 4 (1)", rows, oldR2)
	}
	var paUndone bool
	must(s.QueryRow(ctx, `SELECT undone_at IS NOT NULL FROM rec_feedback WHERE owner_key=$1 AND target_id=$2 AND kind=$3`,
		user.Key(), pa, recs.FeedbackPerformer).Scan(&paUndone))
	if !paUndone {
		t.Error("the account's later 撤销 of cl-pa was overridden by the older device feedback")
	}
	var deviceRows int
	must(s.QueryRow(ctx, `SELECT count(*)::int FROM rec_feedback WHERE owner_key=$1`, device.Key()).Scan(&deviceRows))
	if deviceRows != 0 {
		t.Errorf("device keeps %d rows after the claim", deviceRows)
	}

	// Recall for the account now applies the claimed rows.
	res, err := s.Recall(ctx, RecQuery{Slot: recs.SlotHome, UserID: uid, Feedback: user})
	must(err)
	if !res.Feedback.Releases[ids["cl-r1"]] || !res.Feedback.Tags["comedy"] || res.Feedback.Performers[pa] || res.Feedback.Releases[ids["cl-r3"]] {
		t.Errorf("account feedback after claim = %+v", res.Feedback)
	}

	again, err := s.ClaimRecFeedback(ctx, uid, device.DeviceID)
	must(err)
	if again != 0 {
		t.Errorf("second claim moved %d", again)
	}
	if _, err := s.ClaimRecFeedback(ctx, 0, device.DeviceID); !errors.Is(err, ErrBadFeedback) {
		t.Errorf("claim without a user: %v", err)
	}
}
