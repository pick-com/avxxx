package store

import (
	"strings"
	"testing"
)

func TestValidateClipRange(t *testing.T) {
	for _, c := range []struct {
		start, end, dur int
		ok              bool
	}{
		{0, 5000, 0, true},
		{0, 60000, 0, true},
		{0, 4999, 0, false},
		{0, 60001, 0, false},
		{-1, 9000, 0, false},
		{9000, 9000, 0, false},
		{9000, 1000, 0, false},
		{100000, 115000, 120000, true},
		{110000, 125000, 120000, false}, // past a known end
		{110000, 125000, 0, true},       // unknown duration
		{105000, 120000, 120000, true},  // ends exactly at the end
	} {
		err := ValidateClipRange(c.start, c.end, c.dur)
		if (err == nil) != c.ok {
			t.Errorf("ValidateClipRange(%d,%d,%d) = %v, want ok=%v", c.start, c.end, c.dur, err, c.ok)
		}
	}
}

func TestCleanClipTitle(t *testing.T) {
	if got, err := CleanClipTitle("  名场面 "); err != nil || got != "名场面" {
		t.Errorf("CleanClipTitle = %q %v", got, err)
	}
	if _, err := CleanClipTitle(strings.Repeat("长", 100)); err != nil {
		t.Errorf("100 runes: %v", err)
	}
	if _, err := CleanClipTitle(strings.Repeat("长", 101)); err == nil {
		t.Error("101 runes accepted")
	}
}

func TestClipIDs(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id, err := newClipID()
		if err != nil {
			t.Fatal(err)
		}
		if !ValidClipID(id) {
			t.Fatalf("newClipID() = %q is not valid", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
	for _, bad := range []string{"", "abc", "abcdefghijk", "abcdefghi-", "abcdefghi/", "ÄbcdefghiJ"} {
		if ValidClipID(bad) {
			t.Errorf("ValidClipID(%q) = true", bad)
		}
	}
}
