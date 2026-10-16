package audit

import (
	"testing"
	"time"
)

func d(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func TestCheckAge(t *testing.T) {
	tests := []struct {
		name      string
		release   *time.Time
		shootYear int
		birth     *time.Time
		reason    string // "" = no finding
		age       int
	}{
		{"adult, comfortably", d("2020-05-01"), 0, d("1995-03-01"), "", 0},
		{"19 by year", d("2020-05-01"), 0, d("2001-12-31"), "", 0},
		{"17 by year", d("2020-05-01"), 0, d("2003-01-01"), ReasonUnder18, 17},
		{"born after release", d("2020-05-01"), 0, d("2021-01-01"), ReasonUnder18, -1},
		{"18 early birthday, before release day", d("2020-05-01"), 0, d("2002-01-15"), "", 0},
		{"18 birthday on release day", d("2020-05-01"), 0, d("2002-05-01"), "", 0},
		{"18 but 17 on release day", d("2020-05-01"), 0, d("2002-05-02"), ReasonUnder18AtRelease, 18},
		{"18 birthday 30 June, release in December", d("2020-12-20"), 0, d("2002-06-30"), "", 0},
		{"18 birthday 1 July (cutoff), release in December", d("2020-12-20"), 0, d("2002-07-01"), ReasonLateBirthday18, 18},
		{"18 late birthday before release day", d("2020-12-20"), 0, d("2002-11-30"), ReasonLateBirthday18, 18},
		{"18 late birthday after release day", d("2020-12-20"), 0, d("2002-12-21"), ReasonUnder18AtRelease, 18},
		{"shoot year wins over release year", d("2021-03-01"), 2019, d("2002-02-01"), ReasonUnder18, 17},
		{"shoot year 18, late birthday", d("2021-03-01"), 2020, d("2002-09-01"), ReasonLateBirthday18, 18},
		{"shoot year 18, early birthday (release day not used)", d("2021-03-01"), 2020, d("2002-02-01"), "", 0},
		{"no birth date", d("2020-05-01"), 0, nil, "", 0},
		{"no release date or shoot year", nil, 0, d("2010-01-01"), "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := ReleaseFacts{ID: 1, ReleaseDate: tt.release, ShootYear: tt.shootYear, Performers: []PerformerFacts{{ID: 7, Name: "A", BirthDate: tt.birth}}}
			got := CheckAge(r, DefaultAgeConfig)
			if tt.reason == "" {
				if len(got) != 0 {
					t.Fatalf("got %+v, want no finding", got)
				}
				return
			}
			if len(got) != 1 || got[0].Reason != tt.reason || got[0].Age != tt.age || got[0].PerformerID != 7 {
				t.Fatalf("got %+v, want reason %s age %d", got, tt.reason, tt.age)
			}
		})
	}
}

func TestCheckAgeOnlyFlagsMinors(t *testing.T) {
	r := ReleaseFacts{ReleaseDate: d("2020-05-01"), Performers: []PerformerFacts{
		{ID: 1, BirthDate: d("1990-01-01")},
		{ID: 2, BirthDate: d("2004-01-01")},
		{ID: 3},
	}}
	got := CheckAge(r, DefaultAgeConfig)
	if len(got) != 1 || got[0].PerformerID != 2 || got[0].Basis != "release_year" || got[0].BirthDate != "2004-01-01" {
		t.Fatalf("got %+v", got)
	}
}

func TestCheckAgeCustomCutoff(t *testing.T) {
	r := ReleaseFacts{ReleaseDate: d("2020-12-31"), Performers: []PerformerFacts{{ID: 1, BirthDate: d("2002-03-01")}}}
	if got := CheckAge(r, AgeConfig{LateBirthday: MonthDay{time.March, 1}}); len(got) != 1 {
		t.Fatalf("cutoff 1 March: got %+v, want a finding", got)
	}
	if got := CheckAge(r, AgeConfig{LateBirthday: MonthDay{time.March, 2}}); len(got) != 0 {
		t.Fatalf("cutoff 2 March: got %+v, want none", got)
	}
}

func TestCheckMeta(t *testing.T) {
	full := ReleaseFacts{ReleaseDate: d("2020-01-01"), RuntimeMinutes: 120, Performers: []PerformerFacts{{ID: 1, BirthDate: d("1990-01-01")}}}
	if m := CheckMeta(full); !m.Empty() {
		t.Fatalf("complete release: got %+v", m)
	}
	m := CheckMeta(ReleaseFacts{})
	if want := []string{"release_date", "runtime", "performers"}; !equal(m.Release, want) {
		t.Fatalf("empty release: got %v, want %v", m.Release, want)
	}
	m = CheckMeta(ReleaseFacts{ShootYear: 2019, RuntimeMinutes: 90, Performers: []PerformerFacts{{ID: 1}, {ID: 2, BirthDate: d("1990-01-01")}, {ID: 3}}})
	if len(m.Release) != 0 {
		t.Fatalf("shoot year should stand in for release date: %v", m.Release)
	}
	if len(m.MissingBirth) != 2 || m.MissingBirth[0].ID != 1 || m.MissingBirth[1].ID != 3 {
		t.Fatalf("missing birth: %+v", m.MissingBirth)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNormalizeSpace(t *testing.T) {
	tests := map[string]string{
		"Title":                 "Title",
		"  Title  ":             "Title",
		"A  B":                  "A B",
		"A\tB\nC":               "A B C",
		"　タイトル　":                "タイトル",
		"美人　妻":                  "美人　妻",
		"美人　　妻":                 "美人 妻",
		"A  B":                  "A B",
		"   ":                   "",
		"":                      "",
		"no  change  needed  ?": "no change needed ?",
	}
	for in, want := range tests {
		if got := NormalizeSpace(in); got != want {
			t.Errorf("NormalizeSpace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHealthBand(t *testing.T) {
	tests := []struct {
		score int
		want  string
	}{{0, BandSevere}, {59, BandSevere}, {60, BandDegraded}, {79, BandDegraded}, {80, BandHealthy}, {94, BandHealthy}, {95, BandExcellent}, {100, BandExcellent}}
	for _, tt := range tests {
		if got := HealthBand(tt.score); got != tt.want {
			t.Errorf("HealthBand(%d) = %s, want %s", tt.score, got, tt.want)
		}
	}
}

func TestHealthScore(t *testing.T) {
	if _, ok := HealthScore(0, 0); ok {
		t.Error("no published releases should have no score")
	}
	if s, _ := HealthScore(1000, 1); s != 99 {
		t.Errorf("999/1000 = %d, want 99 (rounded down)", s)
	}
	if s, _ := HealthScore(10, 20); s != 0 {
		t.Errorf("more issues than releases = %d, want 0", s)
	}
	if s, _ := HealthScore(4, 0); s != 100 {
		t.Errorf("no issues = %d, want 100", s)
	}
}

func TestParseClock(t *testing.T) {
	if h, m, err := ParseClock("04:30"); err != nil || h != 4 || m != 30 {
		t.Fatalf("04:30 = %d %d %v", h, m, err)
	}
	for _, bad := range []string{"", "4", "24:00", "12:60", "ab:cd", "-1:00"} {
		if _, _, err := ParseClock(bad); err == nil {
			t.Errorf("ParseClock(%q) should fail", bad)
		}
	}
}

func TestLastSlot(t *testing.T) {
	loc := time.FixedZone("X", 8*3600)
	before := time.Date(2026, 10, 15, 4, 29, 0, 0, loc)
	at := time.Date(2026, 10, 15, 4, 30, 0, 0, loc)
	after := time.Date(2026, 10, 15, 23, 0, 0, 0, loc)
	if got := LastSlot(before, 4, 30); !got.Equal(time.Date(2026, 10, 14, 4, 30, 0, 0, loc)) {
		t.Errorf("before slot: %v", got)
	}
	if got := LastSlot(at, 4, 30); !got.Equal(at) {
		t.Errorf("at slot: %v", got)
	}
	if got := LastSlot(after, 4, 30); !got.Equal(at) {
		t.Errorf("after slot: %v", got)
	}
}

func TestKnownRule(t *testing.T) {
	if !KnownRule(RuleAge) || !KnownRule(RuleSEO) || KnownRule("R-NOPE") {
		t.Fatal("KnownRule mismatch")
	}
}
