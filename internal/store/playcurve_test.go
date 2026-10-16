package store

import "testing"

func TestCurveTimelines(t *testing.T) {
	d := func(v int) *int { return &v }
	srcs := []PlaybackSource{
		{ID: 1, VersionKind: VersionMain, DurationSeconds: d(600), Priority: 1, Status: "active"},
		{ID: 2, VersionKind: VersionHighQuality, DurationSeconds: d(605), Priority: 2, Status: "active"},
		{ID: 3, VersionKind: VersionDub, Priority: 3, Status: "active"},
		{ID: 4, VersionKind: VersionMain, DurationSeconds: d(700), Priority: 4, Status: "active"}, // > 2% off: alt
		{ID: 5, VersionKind: VersionAlt, Priority: 5, Status: "active"},                           // alt, duration unknown
	}
	keys, durations := CurveTimelines(srcs, 0)
	want := map[int64]string{1: "main", 2: "main", 3: "main", 4: "alt:4", 5: "alt:5"}
	for id, k := range want {
		if keys[id] != k {
			t.Errorf("source %d → %q, want %q", id, keys[id], k)
		}
	}
	if durations["main"] != 600_000 || durations["alt:4"] != 700_000 {
		t.Errorf("durations = %v", durations)
	}
	if _, ok := durations["alt:5"]; ok {
		t.Error("unknown alt duration present")
	}
	// Without source durations the catalog runtime is the main duration.
	_, durations = CurveTimelines([]PlaybackSource{{ID: 1, VersionKind: VersionMain}}, 12)
	if durations["main"] != 720_000 {
		t.Errorf("runtime fallback = %v", durations)
	}
}
