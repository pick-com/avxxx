package recs

import (
	"math"
	"testing"
)

// ScoreEvidence on stored evidence reproduces the nightly score (up to the
// three-decimal rounding of the evidence parts).
func TestScoreEvidenceMatchesPerformerSimilarity(t *testing.T) {
	docs := []ReleaseDoc{
		{ID: 1, Tags: []string{"a", "b"}, Maker: "m", Performers: []int64{10, 11}},
		{ID: 2, Tags: []string{"a"}, Maker: "m", Performers: []int64{10, 12}},
		{ID: 3, Tags: []string{"b", "c"}, Maker: "n", Performers: []int64{11, 12}},
		{ID: 4, Tags: []string{"a", "c"}, Maker: "m", Performers: []int64{10, 11}},
	}
	audience := []Interaction{{UserID: 1, ItemID: 1, Weight: 1}, {UserID: 1, ItemID: 3, Weight: 1}, {UserID: 2, ItemID: 2, Weight: 1}, {UserID: 2, ItemID: 4, Weight: 1}}
	w := Weights{Metadata: 0.5, CoStar: 0.3, CoWatch: 0.2}
	ms := PerformerSimilarity(docs, audience, w, PerformerConfig{})
	if len(ms) == 0 {
		t.Fatal("no matches")
	}
	for _, m := range ms {
		if got := ScoreEvidence(m.Evidence, w); math.Abs(got-m.Score) > 0.002 {
			t.Errorf("%d→%d: ScoreEvidence %.4f, stored %.4f", m.PerformerID, m.SimilarID, got, m.Score)
		}
	}
}

func TestRerank(t *testing.T) {
	cands := []StoredNeighbor{
		{ID: 2, Stored: 0.5, Evidence: Evidence{CoWatch: 1}},
		{ID: 3, Stored: 0.3, Evidence: Evidence{Metadata: 0.8}},
		{ID: 4, Stored: 0.01, Evidence: Evidence{Metadata: 0.01}},
	}
	got := Rerank(cands, Weights{Metadata: 1}, 5, 0.05)
	if len(got) != 1 || got[0].ID != 3 || got[0].Rank != 1 || got[0].StoredRank != 2 || got[0].Match != 80 {
		t.Fatalf("rerank %+v", got)
	}
	got = Rerank(cands, Weights{Metadata: 0.5, CoWatch: 0.5}, 1, 0.05)
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("top 1 %+v", got)
	}
	// Invalid weights fall back to the defaults instead of dividing by zero.
	if s := ScoreEvidence(Evidence{Metadata: 1}, Weights{}); math.IsNaN(s) || s != DefaultWeights.Metadata/(DefaultWeights.Metadata+DefaultWeights.CoStar+DefaultWeights.CoWatch) {
		t.Fatalf("zero weights score %v", s)
	}
}
