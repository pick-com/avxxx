package store

import (
	"errors"
	"testing"
)

func TestTimelineFor(t *testing.T) {
	d := func(v int) *int { return &v }
	rel := PlaybackRelease{ID: 1, RuntimeMinutes: 120}
	srcs := []PlaybackSource{
		{ID: 1, VersionKind: VersionMain, DurationSeconds: d(7200), Status: "active"},
		{ID: 2, VersionKind: VersionHighQuality, DurationSeconds: d(7210), Status: "active"}, // within 2%: shared
		{ID: 3, VersionKind: VersionMain, DurationSeconds: d(8000), Status: "active"},        // > 2%: alt
		{ID: 4, VersionKind: VersionAlt, Status: "active"},                                   // stored alt, no duration
	}
	cases := []struct {
		source int64
		key    string
		dur    int
	}{
		{0, "main", 7_200_000}, {1, "main", 7_200_000}, {2, "main", 7_200_000}, {3, "alt:3", 8_000_000}, {4, "alt:4", 0},
	}
	for _, c := range cases {
		tl, err := TimelineFor(rel, srcs, c.source)
		if err != nil || tl.VersionKey != c.key || tl.DurationMS != c.dur || tl.ReleaseID != 1 {
			t.Errorf("source %d: %+v %v, want %s %d", c.source, tl, err, c.key, c.dur)
		}
	}
	if _, err := TimelineFor(rel, srcs, 99); !errors.Is(err, ErrUnknownSource) {
		t.Errorf("unknown source: %v", err)
	}
	// No source durations: the catalog runtime is the reference.
	tl, _ := TimelineFor(PlaybackRelease{ID: 2, RuntimeMinutes: 10}, nil, 0)
	if tl.DurationMS != 600_000 {
		t.Errorf("runtime fallback %d", tl.DurationMS)
	}
}
