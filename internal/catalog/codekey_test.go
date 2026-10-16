package catalog

import "testing"

func TestCodeMatchKey(t *testing.T) {
	for in, want := range map[string]string{
		// zero padding
		"HEYZO-0123": "HEYZO-123",
		"HEYZO-123":  "HEYZO-123",
		"TPB-02000":  "TPB-2000",
		"TPB-2000":   "TPB-2000",
		"ABC-001":    "ABC-1",
		"ABC-1":      "ABC-1",
		"ABC-000":    "ABC-0",
		"SONE-156":   "SONE-156",
		"SONE-1560":  "SONE-1560",
		// separators, width and case
		"heyzo 00123": "HEYZO-123",
		"heyzo0123":   "HEYZO-123",
		"Sone_156":    "SONE-156",
		"ＳＯＮＥ－１５６":    "SONE-156",
		// suffixes are kept
		"ABC-0123-A":  "ABC-123-A",
		"abc-123a":    "ABC-123-A",
		"ABC-0123-DL": "ABC-123-DL",
		"ABC-123DL":   "ABC-123-DL",
		// numeric label prefix stays part of the prefix
		"300MIUM-0123": "300MIUM-123",
		// letters only / other shapes: bare alphanumerics
		"Heyzo":           "HEYZO",
		"FC2-PPV-1234567": "FC2PPV1234567",
		"1234":            "1234",
		"":                "",
		" - ":             "",
	} {
		if got := CodeMatchKey(in); got != want {
			t.Errorf("CodeMatchKey(%q) = %q, want %q", in, got, want)
		}
	}
	if CodeMatchKey("ABC-123") == CodeMatchKey("ABC-1230") || CodeMatchKey("ABC-123-A") == CodeMatchKey("ABC-123") {
		t.Fatal("distinct numbers or suffixes share a key")
	}
}
