package store

import (
	"slices"
	"strings"
	"testing"
)

// 默认片源: the preferred provider goes first within its group, never ahead
// of a better group (main before alternate, active before degraded).
func TestOrderPlaybackSourcesForProvider(t *testing.T) {
	h := func(n int) *int { return &n }
	srcs := []PlaybackSource{
		{ID: 1, Provider: "a", Status: "active", HealthScore: 1, Priority: 1, MaxHeight: h(1080)},
		{ID: 2, Provider: "b", Status: "active", HealthScore: 0.5, Priority: 2, MaxHeight: h(720)},
		{ID: 3, Provider: "b", Status: "degraded", HealthScore: 1, Priority: 0},
		{ID: 4, Provider: "b", IsAltVersion: true, Status: "active", HealthScore: 1, Priority: 0},
		{ID: 5, Provider: "c", Status: "active", HealthScore: 0.9, Priority: 3},
	}
	ids := func(xs []PlaybackSource) []int64 {
		out := make([]int64, len(xs))
		for i, x := range xs {
			out[i] = x.ID
		}
		return out
	}
	auto := slices.Clone(srcs)
	OrderPlaybackSources(auto, 1080)
	if got := ids(auto); !slices.Equal(got, []int64{1, 5, 2, 3, 4}) {
		t.Fatalf("automatic order %v", got)
	}
	pref := slices.Clone(srcs)
	OrderPlaybackSourcesFor(pref, 1080, "b")
	if got := ids(pref); !slices.Equal(got, []int64{2, 1, 5, 3, 4}) {
		t.Errorf("preferred b %v", got)
	}
	none := slices.Clone(srcs)
	OrderPlaybackSourcesFor(none, 1080, "missing")
	if !slices.Equal(ids(none), ids(auto)) {
		t.Errorf("unknown provider changed the order: %v", ids(none))
	}
	plan := PlanPlaybackFor(PlaybackRelease{ID: 1}, []PlaybackSource{
		{ID: 1, Provider: "a", Status: "active", HealthScore: 1, Priority: 1, VersionKind: VersionMain},
		{ID: 2, Provider: "b", Status: "active", HealthScore: 1, Priority: 2, VersionKind: VersionMain},
	}, "", 0, "b")
	if plan.Sources[0].ID != 2 {
		t.Errorf("plan %v", ids(plan.Sources))
	}
}

func TestSettingsPreferredProvider(t *testing.T) {
	def := DefaultSettings()
	if def.PreferredProvider != "" {
		t.Errorf("default %q", def.PreferredProvider)
	}
	u, err := ApplySettingsPatch(def, []byte(`{"preferred_provider":"javtiful"}`))
	if err != nil || u.PreferredProvider != "javtiful" {
		t.Fatalf("%v %q", err, u.PreferredProvider)
	}
	if u, err = ApplySettingsPatch(u, []byte(`{"preferred_provider":""}`)); err != nil || u.PreferredProvider != "" {
		t.Errorf("reset: %v %q", err, u.PreferredProvider)
	}
	for _, bad := range []string{`{"preferred_provider":1}`, `{"preferred_provider":"a b"}`, `{"preferred_provider":"<x>"}`,
		`{"preferred_provider":"` + strings.Repeat("a", 101) + `"}`} {
		if _, err := ApplySettingsPatch(def, []byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
