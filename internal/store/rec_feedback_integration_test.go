package store

import (
	"avxxx/internal/recs"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestRecFeedbackIntegration covers 推荐反馈 end to end in the store: giving,
// re-giving and undoing feedback for a user and a visitor device, the
// active rows Recall hands the ranker (and what Rank then drops / demotes),
// the performers slot, the settings list and the account export.
func TestRecFeedbackIntegration(t *testing.T) {
	openIsolatedTestDB(t, "recfeedback")
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
	performer := func(slug string) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name) VALUES($1,$1) RETURNING id`, slug).Scan(&id))
		return id
	}
	release := func(slug string, lead int64, tags ...string) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$1,'published',current_date - 5) RETURNING id`, slug).Scan(&id))
		if lead > 0 {
			exec(`INSERT INTO release_performers(release_id,performer_id,billing_order,is_primary) VALUES($1,$2,0,true)`, id, lead)
		}
		for _, tg := range tags {
			exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,lower($2))`, id, tg)
		}
		return id
	}
	pa, pb := performer("fb-pa"), performer("fb-pb")
	r1 := release("fb-r1", 0, "Drama")
	r2 := release("fb-r2", pa)
	r3 := release("fb-r3", 0, "Comedy")
	r4 := release("fb-r4", pb)
	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('fb@t','x') RETURNING id`).Scan(&uid))
	user := RecFeedbackOwner{UserID: uid}
	device := RecFeedbackOwner{DeviceID: "fb-device-0123456789"}

	// Give feedback.
	got, err := s.AddRecFeedback(ctx, user, recs.FeedbackRelease, "fb-r1")
	must(err)
	if got.Name != "fb-r1" || got.Target != "fb-r1" || got.CreatedAt.IsZero() {
		t.Errorf("added = %+v", got)
	}
	_, err = s.AddRecFeedback(ctx, user, recs.FeedbackPerformer, "fb-pa")
	must(err)
	_, err = s.AddRecFeedback(ctx, user, recs.FeedbackTag, "comedy")
	must(err)
	again, err := s.AddRecFeedback(ctx, user, recs.FeedbackRelease, "fb-r1") // idempotent
	must(err)
	if !again.CreatedAt.Equal(got.CreatedAt) {
		t.Errorf("re-giving active feedback moved created_at: %v -> %v", got.CreatedAt, again.CreatedAt)
	}
	if _, err = s.AddRecFeedback(ctx, user, recs.FeedbackRelease, "fb-missing"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown release: %v, want ErrNoRows", err)
	}
	if _, err = s.AddRecFeedback(ctx, user, recs.FeedbackTag, "a/b"); !errors.Is(err, ErrBadFeedback) {
		t.Errorf("bad tag: %v, want ErrBadFeedback", err)
	}
	if _, err = s.AddRecFeedback(ctx, RecFeedbackOwner{}, recs.FeedbackTag, "x"); !errors.Is(err, ErrBadFeedback) {
		t.Errorf("no owner: %v, want ErrBadFeedback", err)
	}
	_, err = s.AddRecFeedback(ctx, device, recs.FeedbackPerformer, "fb-pb")
	must(err)
	var stored string
	must(s.QueryRow(ctx, `SELECT owner_key FROM rec_feedback WHERE user_id IS NULL`).Scan(&stored))
	if stored != device.Key() || bytes.Contains([]byte(stored), []byte(device.DeviceID)) {
		t.Errorf("device owner stored as %q, want the hash %q", stored, device.Key())
	}

	// Recall carries each owner's active feedback; the ranker applies it.
	res, err := s.Recall(ctx, RecQuery{Slot: recs.SlotHome, UserID: uid, Feedback: user})
	must(err)
	fb := res.Feedback
	if !fb.Releases[r1] || !fb.Performers[pa] || !fb.Tags["comedy"] || fb.Performers[pb] {
		t.Fatalf("user feedback = %+v", fb)
	}
	infos, err := s.RecInfos(ctx, []int64{r1, r2, r3, r4}, 0, "")
	must(err)
	var cands []recs.Candidate
	for _, id := range []int64{r1, r2, r3, r4} {
		cands = append(cands, recs.Candidate{ReleaseID: id, Channel: recs.ChanNew, Raw: 1})
	}
	ranked := recs.Rank(cands, infos, recs.Options{Feedback: fb})
	var order []int64
	for _, r := range ranked {
		order = append(order, r.ReleaseID)
	}
	if !slices.Equal(order, []int64{r4, r3}) {
		t.Errorf("ranked = %v, want r4 then the demoted r3 (r1 and pa's r2 dropped)", order)
	}
	devRes, err := s.Recall(ctx, RecQuery{Slot: recs.SlotRelated, SeedID: r1, Feedback: device})
	must(err)
	if !devRes.Feedback.Performers[pb] || devRes.Feedback.Releases[r1] {
		t.Errorf("device feedback = %+v", devRes.Feedback)
	}
	if none, err := s.Recall(ctx, RecQuery{Slot: recs.SlotHome}); err != nil || !none.Feedback.Empty() {
		t.Errorf("visitor without device: %+v, %v", none.Feedback, err)
	}

	// The performers slot leaves out performers marked 不推荐.
	var seed int64 = performer("fb-seed")
	exec(`INSERT INTO follows(user_id,performer_id) VALUES($1,$2)`, uid, seed)
	exec(`INSERT INTO performer_similar(performer_id,similar_id,score,evidence) VALUES($1,$2,0.9,'{}'),($1,$3,0.8,'{}')`, seed, pa, pb)
	prs, err := s.RecommendedPerformers(ctx, uid, 0, 10)
	must(err)
	if len(prs) != 1 || prs[0].ID != pb {
		t.Errorf("recommended performers = %+v, want only fb-pb", prs)
	}

	// Settings list, newest first; undo; the export keeps undone rows.
	list, err := s.ListRecFeedback(ctx, user)
	must(err)
	if len(list) != 3 || list[0].Kind != recs.FeedbackTag || list[2].Target != "fb-r1" {
		t.Errorf("list = %+v", list)
	}
	ok, err := s.UndoRecFeedback(ctx, user, recs.FeedbackPerformer, "fb-pa")
	must(err)
	if !ok {
		t.Error("undo should report a change")
	}
	if ok, err = s.UndoRecFeedback(ctx, user, recs.FeedbackPerformer, "fb-pa"); err != nil || ok {
		t.Errorf("second undo = %v, %v; want false", ok, err)
	}
	if ok, err = s.UndoRecFeedback(ctx, user, recs.FeedbackRelease, "fb-nope"); err != nil || ok {
		t.Errorf("undo unknown = %v, %v; want false", ok, err)
	}
	res, err = s.Recall(ctx, RecQuery{Slot: recs.SlotHome, UserID: uid, Feedback: user})
	must(err)
	if res.Feedback.Performers[pa] {
		t.Error("undone performer feedback still applied")
	}
	if prs, err = s.RecommendedPerformers(ctx, uid, 0, 10); err != nil || len(prs) != 2 {
		t.Errorf("after undo, performers = %+v, %v", prs, err)
	}
	if list, _ = s.ListRecFeedback(ctx, user); len(list) != 2 {
		t.Errorf("list after undo = %+v", list)
	}
	if dl, _ := s.ListRecFeedback(ctx, device); len(dl) != 1 || dl[0].Name != "fb-pb" {
		t.Errorf("device list = %+v", dl)
	}
	// Re-giving undone feedback re-activates it.
	_, err = s.AddRecFeedback(ctx, user, recs.FeedbackPerformer, "fb-pa")
	must(err)
	if list, _ = s.ListRecFeedback(ctx, user); len(list) != 3 || list[0].Target != "fb-pa" {
		t.Errorf("list after re-give = %+v", list)
	}
	must(func() error { _, err := s.UndoRecFeedback(ctx, user, recs.FeedbackPerformer, "fb-pa"); return err }())

	var buf bytes.Buffer
	must(s.WriteDataExport(ctx, uid, &buf))
	var export map[string]json.RawMessage
	must(json.Unmarshal(buf.Bytes(), &export))
	var rows []struct {
		Kind     string  `json:"kind"`
		Target   string  `json:"target"`
		UndoneAt *string `json:"undone_at"`
	}
	must(json.Unmarshal(export["rec_feedback"], &rows))
	if len(rows) != 3 {
		t.Fatalf("export rec_feedback = %s", export["rec_feedback"])
	}
	undone := 0
	for _, r := range rows {
		if r.UndoneAt != nil {
			undone++
			if r.Target != "fb-pa" {
				t.Errorf("unexpected undone row %+v", r)
			}
		}
	}
	if undone != 1 || bytes.Contains(export["rec_feedback"], []byte("fb-device")) {
		t.Errorf("export rec_feedback = %s", export["rec_feedback"])
	}

	// Deleting the account removes its feedback; the device's stays.
	exec(`DELETE FROM users WHERE id=$1`, uid)
	var n int
	must(s.QueryRow(ctx, `SELECT count(*) FROM rec_feedback`).Scan(&n))
	if n != 1 {
		t.Errorf("after account deletion %d rows, want the device's 1", n)
	}
}
