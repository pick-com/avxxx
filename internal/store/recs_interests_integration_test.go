package store

import (
	"avxxx/internal/recs"
	"context"
	"fmt"
	"testing"
)

// TestRecsInterestsIntegration covers the cold-start interests recall: a
// signed-in user with little history gets releases tagged with their
// onboarding interests (more matches first, with the matched tag as label),
// the cold-start flag and interests come back with the recall, release
// infos carry their tags, and the channel stops once the user has history.
func TestRecsInterestsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "recsinterests")
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
	release := func(slug, status string, tags ...string) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$1,$2,current_date - 10) RETURNING id`, slug, status).Scan(&id))
		for _, tg := range tags {
			exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,lower($2))`, id, tg)
		}
		return id
	}
	both := release("ri-both", "published", "Drama", "Romance")
	drama := release("ri-drama", "published", "Drama")
	release("ri-other", "published", "Action")
	release("ri-draft", "draft", "Drama")

	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('ri@t','x') RETURNING id`).Scan(&uid))
	_, err = s.SetInterests(ctx, uid, []string{"drama", "romance"}, true)
	must(err)

	res, err := s.Recall(ctx, RecQuery{Slot: recs.SlotHome, UserID: uid})
	must(err)
	var got []recs.Candidate
	for _, c := range res.Candidates {
		if c.Channel == recs.ChanInterests {
			got = append(got, c)
		}
	}
	if len(got) != 2 || got[0].ReleaseID != both || got[1].ReleaseID != drama || got[0].Raw <= got[1].Raw || got[1].Label != "Drama" {
		t.Fatalf("interest candidates %+v", got)
	}
	if !res.ColdStart || fmt.Sprint(res.Interests) != "[drama romance]" {
		t.Errorf("cold start %v interests %v", res.ColdStart, res.Interests)
	}
	infos, err := s.RecInfos(ctx, []int64{both}, 0, "")
	must(err)
	if fmt.Sprint(infos[both].Tags) != "[drama romance]" {
		t.Errorf("info tags %v", infos[both].Tags)
	}
	// Related is seed-driven: no interests.
	rel, err := s.Recall(ctx, RecQuery{Slot: recs.SlotRelated, UserID: uid, SeedID: both})
	must(err)
	for _, c := range rel.Candidates {
		if c.Channel == recs.ChanInterests {
			t.Errorf("related recalled interests: %+v", c)
		}
	}

	// Enough history: no longer cold.
	for i := 0; i < recs.ColdStartHistory; i++ {
		id := release(fmt.Sprintf("ri-seen-%d", i), "published")
		exec(`INSERT INTO watch_history(user_id,release_id) VALUES($1,$2)`, uid, id)
	}
	res, err = s.Recall(ctx, RecQuery{Slot: recs.SlotTonight, UserID: uid})
	must(err)
	if res.ColdStart {
		t.Error("user with history still cold")
	}
	for _, c := range res.Candidates {
		if c.Channel == recs.ChanInterests {
			t.Errorf("warm user recalled interests: %+v", c)
		}
	}
}
