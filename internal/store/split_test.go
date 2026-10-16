package store

import (
	"avxxx/internal/identity"
	"reflect"
	"testing"
)

func TestPerformerSplitSlugCandidates(t *testing.T) {
	// Japanese name, a kana reading and a DMM id from the moved evidence: the
	// romaji reading first, then the source-scoped slug (#82 rules).
	got := performerSplitSlugCandidates("波多野結衣", []performerRef{{Name: "波多野結衣", Readings: []string{"はたのゆい"}, DMMID: "1234"}})
	if want := []string{"hatanoyui", "dmm-actress-1234"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	// Latin name: its slug first; other credited names become readings.
	got = performerSplitSlugCandidates("Airi S", []performerRef{{Name: "Airi Suzuki"}})
	if want := []string{"airi-s", "airi-suzuki"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	// Nothing usable: no candidate, the caller uses performer-<id>.
	if got = performerSplitSlugCandidates("結衣", nil); len(got) != 0 {
		t.Errorf("kanji-only candidates = %v", got)
	}
	if fallbackSlug(identity.PerformerType, 7) != "performer-7" || fallbackSlug(identity.ReleaseType, 7) != "release-7" {
		t.Error("fallback slugs")
	}
}

func TestReleaseSplitSlugCandidates(t *testing.T) {
	got := releaseSplitSlugCandidates("Other Title", []releaseSlugInput{{code: "ABC-123", source: "Site B", key: "k9"}})
	if want := []string{"abc-123", "other-title-site-b-k9"}; !reflect.DeepEqual(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	got = releaseSplitSlugCandidates("別タイトル", []releaseSlugInput{{source: "DMM", key: "x1"}})
	if want := []string{"release-dmm-x1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("non-ASCII title candidates = %v, want %v", got, want)
	}
	for _, c := range got {
		if c == "" {
			t.Error("empty slug candidate")
		}
	}
}

func TestPerformerEvidenceKeys(t *testing.T) {
	got := performerEvidenceKeys([]performerRef{{Name: "あいり", Readings: []string{"Airi S"}, DMMID: "55"}, {Name: "あいり"}})
	want := []string{identity.AliasKey("あいり"), identity.AliasKey("airi"), identity.AliasKey("airi s"), identity.ExternalIDKey("dmm_actress", "55")}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if k := performerEvidenceKeys([]performerRef{{Name: "X", DMMID: "abc"}}); !reflect.DeepEqual(k, []string{identity.AliasKey("x")}) {
		t.Errorf("non-numeric DMM id became a key: %v", k)
	}
}
