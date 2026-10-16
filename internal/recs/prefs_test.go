package recs

import (
	"slices"
	"testing"
)

func TestPreferenceFactor(t *testing.T) {
	p := Preference{Tags: map[string]bool{"drama": true, "romance": true, "comedy": true}, TagBoost: 0.1, MaxMinutes: 90, DurationBoost: 0.05}
	cases := []struct {
		info Info
		want float64
	}{
		{Info{}, 1},
		{Info{Tags: []string{"drama"}}, 1.1},
		{Info{Tags: []string{"drama", "romance", "comedy"}}, 1.1 * 1.1}, // capped at 2 matches
		{Info{Tags: []string{"drama"}, RuntimeMinutes: 60}, 1.1 * 1.05},
		{Info{RuntimeMinutes: 120}, 1},
		{Info{RuntimeMinutes: 0}, 1}, // unknown runtime is not boosted
	}
	for _, c := range cases {
		if got := p.Factor(c.info); !near(got, c.want) {
			t.Errorf("Factor(%+v) = %v, want %v", c.info, got, c.want)
		}
	}
	if f := (Preference{MinMinutes: 100, DurationBoost: 0.1}).Factor(Info{RuntimeMinutes: 120}); !near(f, 1.1) {
		t.Errorf("min window: %v", f)
	}
}

func TestInterestPreference(t *testing.T) {
	if _, ok := InterestPreference(nil); ok {
		t.Error("no interests should give no preference")
	}
	p, ok := InterestPreference([]string{"drama", "drama", "romance"})
	if !ok || len(p.Tags) != 2 || p.TagBoost != InterestTagBoost {
		t.Errorf("%+v %v", p, ok)
	}
}

func TestTonightPreferences(t *testing.T) {
	if got := TonightPreferences("", ""); len(got) != 0 {
		t.Errorf("defaults: %+v", got)
	}
	if got := TonightPreferences(MoodAny, CompanyFamily); len(got) != 0 {
		t.Errorf("family maps to nothing: %+v", got)
	}
	got := TonightPreferences(MoodTouching, CompanySelf)
	if len(got) != 1 || !got[0].Tags["drama"] || got[0].DurationBoost != 0 {
		t.Errorf("touching: %+v", got)
	}
	got = TonightPreferences(MoodLight, CompanyFriends)
	if len(got) != 2 || got[0].MaxMinutes != 90 || !got[1].Tags["comedy"] {
		t.Errorf("light + friends: %+v", got)
	}
	for _, m := range []string{"", MoodAny, MoodLight, MoodThrill, MoodTouching, MoodBrainy} {
		if !ValidMood(m) {
			t.Errorf("mood %q invalid", m)
		}
	}
	if ValidMood("sad") || ValidCompany("boss") || !ValidCompany("") || !ValidCompany(CompanyFriends) {
		t.Error("validation")
	}
}

// Soft preferences reorder but never filter: a matching release overtakes a
// slightly better one, and a non-matching release is still listed.
func TestRankSoftPreferences(t *testing.T) {
	cands := []Candidate{
		{ReleaseID: 1, Channel: ChanTrending, Raw: 10, Label: PeriodWeek},
		{ReleaseID: 2, Channel: ChanTrending, Raw: 9, Label: PeriodWeek},
		{ReleaseID: 3, Channel: ChanTrending, Raw: 8, Label: PeriodWeek},
	}
	in := infos(Info{ReleaseID: 1, Tags: []string{"action"}, RuntimeMinutes: 150},
		Info{ReleaseID: 2, Tags: []string{"comedy"}, RuntimeMinutes: 80},
		Info{ReleaseID: 3})
	if got := ids(Rank(cands, in, Options{})); !slices.Equal(got, []int64{1, 2, 3}) {
		t.Fatalf("baseline %v", got)
	}
	got := Rank(cands, in, Options{Prefer: TonightPreferences(MoodLight, CompanySelf)})
	if !slices.Equal(ids(got), []int64{2, 1, 3}) {
		t.Errorf("light mood order %v", ids(got))
	}
	// The reason still comes from the recall channel.
	if got[0].Reason.Code != "trending" {
		t.Errorf("reason %+v", got[0].Reason)
	}
}

// Cold start: the interests channel recalls tagged releases with its own
// reason, and the interest boost lifts trending releases that match.
func TestRankColdStartInterests(t *testing.T) {
	cands := []Candidate{
		{ReleaseID: 1, Channel: ChanTrending, Raw: 10, Label: PeriodWeek, BoardRank: 1},
		{ReleaseID: 2, Channel: ChanTrending, Raw: 9, Label: PeriodWeek, BoardRank: 2},
		{ReleaseID: 3, Channel: ChanInterests, Raw: 2.5, Label: "Drama"},
		{ReleaseID: 4, Channel: ChanNew, Raw: 1},
	}
	in := infos(Info{ReleaseID: 1}, Info{ReleaseID: 2, Tags: []string{"drama"}}, Info{ReleaseID: 3, Tags: []string{"drama", "romance"}}, Info{ReleaseID: 4})
	pref, _ := InterestPreference([]string{"drama", "romance"})
	got := Rank(cands, in, Options{Prefer: []Preference{pref}})
	if !slices.Equal(ids(got), []int64{3, 2, 1, 4}) {
		t.Fatalf("order %v (scores %v %v %v)", ids(got), got[0].Score, got[1].Score, got[2].Score)
	}
	// 3: interests 0.7 · 1 × 1.15² ≈ 0.926.
	if !near(got[0].Score, 0.926) || got[0].Reason.Code != "interests" || got[0].Reason.Text != "你选的兴趣 · Drama" {
		t.Errorf("interest rec %+v", got[0])
	}
	// Without the preference the interests channel still ranks above trending.
	if got := ids(Rank(cands, in, Options{})); got[0] != 3 || got[1] != 1 {
		t.Errorf("no boost %v", got)
	}
	if r := ReasonFor(Candidate{Channel: ChanInterests}, Info{}); r.Code != "interests" || r.Text == "" {
		t.Errorf("reason without label %+v", r)
	}
}
