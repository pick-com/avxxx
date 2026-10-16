package recs

import (
	"slices"
	"testing"
)

func feedbackFixture() ([]Candidate, map[int64]Info) {
	cands := []Candidate{
		{ReleaseID: 1, Channel: ChanTrending, Raw: 10},
		{ReleaseID: 2, Channel: ChanTrending, Raw: 9},
		{ReleaseID: 3, Channel: ChanTrending, Raw: 8},
		{ReleaseID: 4, Channel: ChanTrending, Raw: 7},
		// 5 comes in only through performer 70 (a co-star, not its lead).
		{ReleaseID: 5, Channel: ChanSamePerformer, Raw: 1, PerformerID: 70, PerformerName: "Co"},
		{ReleaseID: 5, Channel: ChanNew, Raw: 0.1},
	}
	infos := map[int64]Info{
		1: {ReleaseID: 1, PerformerID: 100, Tags: []string{"drama"}},
		2: {ReleaseID: 2, PerformerID: 200, Tags: []string{"comedy"}},
		3: {ReleaseID: 3, PerformerID: 300, Tags: []string{"comedy", "office"}},
		4: {ReleaseID: 4},
		5: {ReleaseID: 5, PerformerID: 500},
	}
	return cands, infos
}

func TestRankFeedbackDropsRelease(t *testing.T) {
	cands, infos := feedbackFixture()
	var fb Feedback
	fb.Add(FeedbackRelease, 1, "")
	got := ids(Rank(cands, infos, Options{Feedback: fb}))
	for _, id := range got {
		if id == 1 {
			t.Fatalf("release marked 不感兴趣 still recommended: %v", got)
		}
	}
	if len(got) != 4 {
		t.Errorf("only the marked release should go, got %v", got)
	}
}

func TestRankFeedbackDropsLeadPerformer(t *testing.T) {
	cands, infos := feedbackFixture()
	var fb Feedback
	fb.Add(FeedbackPerformer, 200, "")
	got := ids(Rank(cands, infos, Options{Feedback: fb}))
	for _, id := range got {
		if id == 2 {
			t.Fatalf("release led by a 不推荐 performer still recommended: %v", got)
		}
	}
}

func TestRankFeedbackIgnoresHitsThroughPerformer(t *testing.T) {
	cands, infos := feedbackFixture()
	before := Rank(cands, infos, Options{})
	var fb Feedback
	fb.Add(FeedbackPerformer, 70, "")
	after := Rank(cands, infos, Options{Feedback: fb})
	find := func(rs []Rec) *Rec {
		for i := range rs {
			if rs[i].ReleaseID == 5 {
				return &rs[i]
			}
		}
		return nil
	}
	b, a := find(before), find(after)
	if b == nil || a == nil {
		t.Fatalf("release 5 should stay (its lead is not marked): before %v after %v", ids(before), ids(after))
	}
	if b.Reason.Code != "same_performer" || a.Reason.Code != "new" {
		t.Errorf("reason via the marked co-star should go: before %q after %q", b.Reason.Code, a.Reason.Code)
	}
	if a.Score >= b.Score {
		t.Errorf("score should drop without the co-star hit: %v -> %v", b.Score, a.Score)
	}
}

func TestRankFeedbackDemotesTag(t *testing.T) {
	cands, infos := feedbackFixture()
	var fb Feedback
	fb.Add(FeedbackTag, 0, "comedy")
	got := Rank(cands, infos, Options{Feedback: fb})
	pos := func(id int64) int { return slices.Index(ids(got), id) }
	if !(pos(1) < pos(4) && pos(4) < pos(2) && pos(4) < pos(3)) || len(got) != 5 {
		t.Errorf("comedy releases 2, 3 should fall below 4 and stay listed: got %v", ids(got))
	}
	if plain := ids(Rank(cands, infos, Options{})); !(slices.Index(plain, 2) < slices.Index(plain, 4)) {
		t.Fatalf("fixture: 2 should rank above 4 without feedback: %v", plain)
	}
	plain := Rank(cands, infos, Options{})
	for _, r := range got {
		if r.ReleaseID == 3 {
			for _, p := range plain {
				// Demoted once even with several matching tags.
				if p.ReleaseID == 3 && !near(r.Score, round3(p.Score*TagFeedbackFactor)) {
					t.Errorf("release 3 score %v, want %v × %v", r.Score, p.Score, TagFeedbackFactor)
				}
			}
		}
	}
}

func TestRankEmptyFeedbackChangesNothing(t *testing.T) {
	cands, infos := feedbackFixture()
	a, b := Rank(cands, infos, Options{}), Rank(cands, infos, Options{Feedback: Feedback{}})
	if !slices.Equal(ids(a), ids(b)) {
		t.Errorf("empty feedback changed the ranking: %v vs %v", ids(a), ids(b))
	}
	if !(Feedback{}).Empty() {
		t.Error("zero Feedback should be empty")
	}
}

func TestValidFeedbackKind(t *testing.T) {
	for _, k := range []string{FeedbackRelease, FeedbackPerformer, FeedbackTag} {
		if !ValidFeedbackKind(k) {
			t.Errorf("%q should be valid", k)
		}
	}
	for _, k := range []string{"", "dislike", "not_interested"} {
		if ValidFeedbackKind(k) {
			t.Errorf("%q should be invalid", k)
		}
	}
}
