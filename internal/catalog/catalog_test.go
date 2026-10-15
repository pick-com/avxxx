package catalog

import "testing"

func TestReleaseSlug(t *testing.T) {
	for _, tt := range []struct{ code, want string }{{"JUQ-743", "juq-743"}, {"JUQ 743", "juq-743"}, {"", "title-x-123"}} {
		got := ReleaseSlug(tt.code, "Title", "x", "123")
		if got != tt.want {
			t.Fatalf("got %q want %q", got, tt.want)
		}
	}
}
func TestIdentifierAndFingerprint(t *testing.T) {
	if NormalizeIdentifier(" JUQ 743 ") != "juq743" {
		t.Fatal("identifier normalization")
	}
	if Fingerprint("A", "B") == Fingerprint("A", "C") {
		t.Fatal("fingerprints must differ")
	}
}
