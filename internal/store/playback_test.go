package store

import (
	"slices"
	"testing"
)

func ip(n int) *int { return &n }

func ids(srcs []PlaybackSource) []int64 {
	out := make([]int64, len(srcs))
	for i, x := range srcs {
		out[i] = x.ID
	}
	return out
}

func TestIsAltDuration(t *testing.T) {
	cases := []struct {
		d, ref int
		want   bool
	}{
		{7200, 7200, false},
		{7344, 7200, false}, // exactly +2% is not "more than 2%"
		{7056, 7200, false}, // exactly -2%
		{7345, 7200, true},
		{7055, 7200, true},
		{9000, 7200, true},
		{0, 7200, false},
		{7200, 0, false},
	}
	for _, c := range cases {
		if got := IsAltDuration(c.d, c.ref); got != c.want {
			t.Errorf("IsAltDuration(%d,%d)=%v want %v", c.d, c.ref, got, c.want)
		}
	}
}

func TestReferenceDuration(t *testing.T) {
	all := []PlaybackSource{
		{ID: 1, VersionKind: VersionMain, DurationSeconds: ip(7000), Priority: 1, Status: "disabled"},
		{ID: 2, VersionKind: VersionMain, DurationSeconds: ip(7200), Priority: 5, Status: "active"},
		{ID: 3, VersionKind: VersionDub, DurationSeconds: ip(5000), Priority: 0, Status: "active"},
		{ID: 4, VersionKind: VersionMain, Priority: 0, Status: "active"},
	}
	if got := ReferenceDuration(all, 100); got == nil || *got != 7200 {
		t.Fatalf("reference = %v, want 7200 (active main with duration)", got)
	}
	if got := ReferenceDuration(all[2:], 100); got == nil || *got != 6000 {
		t.Fatalf("fallback reference = %v, want runtime 6000", got)
	}
	if got := ReferenceDuration(nil, 0); got != nil {
		t.Fatalf("reference = %v, want nil", *got)
	}
}

func TestApplyAltVersionRule(t *testing.T) {
	ref := ip(7200)
	cases := []struct {
		name     string
		in       PlaybackSource
		ref      *int
		wantKind string
	}{
		{"dub within 2%", PlaybackSource{VersionKind: VersionDub, DurationSeconds: ip(7250)}, ref, VersionDub},
		{"4k beyond 2%", PlaybackSource{VersionKind: VersionHighQuality, DurationSeconds: ip(8000)}, ref, VersionAlt},
		{"stored alt kept without data", PlaybackSource{VersionKind: VersionAlt}, nil, VersionAlt},
		{"stored alt kept even when same length", PlaybackSource{VersionKind: VersionAlt, DurationSeconds: ip(7200)}, ref, VersionAlt},
		{"no duration keeps main", PlaybackSource{VersionKind: VersionMain}, ref, VersionMain},
		{"empty kind defaults main", PlaybackSource{DurationSeconds: ip(7200)}, ref, VersionMain},
		{"no reference keeps kind", PlaybackSource{VersionKind: VersionDub, DurationSeconds: ip(1)}, nil, VersionDub},
	}
	for _, c := range cases {
		x := c.in
		x.IsAltVersion = !(c.wantKind == VersionAlt) // ensure it is recomputed
		ApplyAltVersionRule(&x, c.ref)
		if x.VersionKind != c.wantKind || x.IsAltVersion != (c.wantKind == VersionAlt) {
			t.Errorf("%s: got kind=%s alt=%v, want kind=%s", c.name, x.VersionKind, x.IsAltVersion, c.wantKind)
		}
	}
}

func TestRegionAllowed(t *testing.T) {
	open := PlaybackSource{}
	deny := PlaybackSource{RegionDeny: []string{"CN", "kr"}}
	allow := PlaybackSource{RegionAllow: []string{"JP", "US"}}
	both := PlaybackSource{RegionAllow: []string{"JP", "US"}, RegionDeny: []string{"US"}}
	cases := []struct {
		name   string
		x      PlaybackSource
		region string
		want   bool
	}{
		{"open/any", open, "DE", true},
		{"open/unknown", open, "", true},
		{"deny hit", deny, "CN", false},
		{"deny case-insensitive", deny, "KR", false},
		{"deny miss", deny, "JP", true},
		{"deny/unknown region", deny, "", true},
		{"allow hit", allow, "jp", true},
		{"allow miss", allow, "DE", false},
		{"allow/unknown region", allow, "", false},
		{"deny wins over allow", both, "US", false},
		{"allow with deny elsewhere", both, "JP", true},
	}
	for _, c := range cases {
		if got := RegionAllowed(c.x, c.region); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestOrderPlaybackSources(t *testing.T) {
	srcs := []PlaybackSource{
		{ID: 1, MaxHeight: ip(720), HealthScore: 1.0, Priority: 1},
		{ID: 2, MaxHeight: ip(1080), HealthScore: 0.9, Priority: 50},
		{ID: 3, MaxHeight: ip(1080), HealthScore: 0.9, Priority: 10},
		{ID: 4, MaxHeight: ip(2160), HealthScore: 1.0, Priority: 100},
		{ID: 5, HealthScore: 1.0, Priority: 1},
		{ID: 6, MaxHeight: ip(1080), HealthScore: 0.95, Priority: 100},
		{ID: 7, MaxHeight: ip(1080), HealthScore: 1.0, Priority: 0, IsAltVersion: true},
	}
	got := slices.Clone(srcs)
	OrderPlaybackSources(got, 1080)
	// non-alt first: exact 1080 (by health, then priority) → higher than 1080 → lower/unknown; alt last
	if want := []int64{6, 3, 2, 4, 1, 5, 7}; !slices.Equal(ids(got), want) {
		t.Errorf("quality=1080 order %v, want %v", ids(got), want)
	}
	got = slices.Clone(srcs)
	OrderPlaybackSources(got, 0)
	if want := []int64{1, 5, 4, 6, 3, 2, 7}; !slices.Equal(ids(got), want) {
		t.Errorf("no preference order %v, want %v", ids(got), want)
	}
}

func TestPlanPlayback(t *testing.T) {
	rel := PlaybackRelease{ID: 9, Slug: "juq-743", RuntimeMinutes: 120}
	all := []PlaybackSource{
		{ID: 10, VersionKind: VersionMain, DurationSeconds: ip(7210), MaxHeight: ip(1080), HealthScore: 0.9, Priority: 1, Status: "active"},
		{ID: 11, VersionKind: VersionHighQuality, DurationSeconds: ip(7200), MaxHeight: ip(2160), HealthScore: 1, Priority: 2, Status: "active"},
		{ID: 12, VersionKind: VersionMain, DurationSeconds: ip(8100), MaxHeight: ip(1080), HealthScore: 1, Priority: 3, Status: "active"}, // director's cut stored as main
		{ID: 13, VersionKind: VersionMain, MaxHeight: ip(1080), HealthScore: 1, Priority: 0, Status: "disabled"},
		{ID: 14, VersionKind: VersionDub, MaxHeight: ip(1080), HealthScore: 1, Priority: 4, Status: "active", RegionAllow: []string{"US"}},
		{ID: 15, VersionKind: VersionMain, MaxHeight: ip(480), HealthScore: 1, Priority: 5, Status: "degraded"},
	}
	plan := PlanPlayback(rel, all, "JP", 1080)
	if plan.DurationSeconds == nil || *plan.DurationSeconds != 7210 {
		t.Fatalf("duration %v, want 7210", plan.DurationSeconds)
	}
	// 12 is a director's cut (>2% longer): it goes below shared-progress sources despite better health.
	if want := []int64{10, 11, 12}; !slices.Equal(ids(plan.Sources), want) {
		t.Fatalf("sources %v, want %v", ids(plan.Sources), want)
	}
	alt := map[int64]bool{}
	for _, x := range plan.Sources {
		alt[x.ID] = x.IsAltVersion
	}
	if !alt[12] || alt[10] || alt[11] {
		t.Errorf("alt flags %v, want only 12", alt)
	}
	if all[2].VersionKind != VersionMain {
		t.Error("PlanPlayback mutated its input")
	}
	if id, ok := ResumeSource(plan.Sources); !ok || id != 10 {
		t.Errorf("resume source %d,%v want 10", id, ok)
	}
	us := PlanPlayback(rel, all, "US", 0)
	if !slices.Contains(ids(us.Sources), 14) {
		t.Errorf("US plan %v should include allow-listed 14", ids(us.Sources))
	}
	if _, ok := ResumeSource([]PlaybackSource{{ID: 1, IsAltVersion: true}}); ok {
		t.Error("resume source for alt-only list should be absent")
	}
}
