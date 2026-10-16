package store

import (
	"errors"
	"strings"
	"testing"
)

func TestLaterFinished(t *testing.T) {
	for _, tc := range []struct {
		progress, duration int
		want               bool
	}{
		{90, 100, true},
		{89, 100, false},
		{100, 100, true},
		{5000, 0, false}, // unknown duration never counts as finished
		{10, -1, false},
	} {
		if got := LaterFinished(tc.progress, tc.duration); got != tc.want {
			t.Errorf("LaterFinished(%d, %d) = %v, want %v", tc.progress, tc.duration, got, tc.want)
		}
	}
}

func TestPlaylistInputNormalize(t *testing.T) {
	str := func(s string) *string { return &s }
	in := PlaylistInput{Name: str("  夜里看  "), Description: str(" d "), Visibility: str("link")}
	if err := in.Normalize(true); err != nil || *in.Name != "夜里看" || *in.Description != "d" {
		t.Fatalf("normalize: %v %q %q", err, *in.Name, *in.Description)
	}
	var ve *ValidationError
	for name, bad := range map[string]PlaylistInput{
		"missing name":   {},
		"blank name":     {Name: str(" ")},
		"long name":      {Name: str(strings.Repeat("长", 61))},
		"long desc":      {Name: str("a"), Description: str(strings.Repeat("x", 301))},
		"bad visibility": {Name: str("a"), Visibility: str("friends")},
	} {
		if err := bad.Normalize(true); !errors.As(err, &ve) {
			t.Errorf("%s: err = %v, want ValidationError", name, err)
		}
	}
	// Updates may omit the name.
	if err := (&PlaylistInput{Visibility: str("public")}).Normalize(false); err != nil {
		t.Errorf("update without name: %v", err)
	}
	if !ValidVisibility("private") || ValidVisibility("") {
		t.Error("ValidVisibility")
	}
}
