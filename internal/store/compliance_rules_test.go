package store

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestConsentInputGPCForcesAdsOff(t *testing.T) {
	in := ConsentInput{Analytics: true, PersonalizedAds: true, PersonalizedRecs: true, GPC: true}
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if in.PersonalizedAds || in.Source != "preferences" {
		t.Errorf("normalized = %+v", in)
	}
	if got := in.Categories(); !slices.Equal(got, []string{"necessary", "analytics", "personalized_recs"}) {
		t.Errorf("categories = %v", got)
	}
	// Even an unnormalized input never lists ads under GPC.
	if got := (ConsentInput{PersonalizedAds: true, GPC: true}).Categories(); slices.Contains(got, ConsentAds) {
		t.Errorf("categories = %v", got)
	}
	for _, bad := range []ConsentInput{{Source: "cookie-wall"}, {DeviceID: "short"}, {DeviceID: strings.Repeat("a", 20) + " x"}} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v: want error", bad)
		}
	}
	st := consentStateOf([]string{"necessary", "personalized_ads"}, true, 2, "gpc", time.Now())
	if st.PersonalizedAds {
		t.Error("a GPC record must read back with ads off")
	}
}

func TestLegalLangChain(t *testing.T) {
	for lang, want := range map[string][]string{
		"en":    {"en", "zh-TW", "zh-CN"},
		"ja":    {"ja", "zh-CN", "en"},
		"zh-CN": {"zh-CN", "zh-TW", "en"},
		"th":    {"th", "zh-TW", "zh-CN", "en"},
	} {
		if got := LegalLangChain(lang); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", lang, got, want)
		}
	}
}

func TestLegalDraftNormalize(t *testing.T) {
	in := LegalDraftInput{Kind: "terms", Lang: "en-US", Title: " Terms ", Summary: []string{" a ", "", "b"}, Body: "## A\r\n\r\ntext"}
	if err := in.Normalize(true); err != nil {
		t.Fatal(err)
	}
	if in.Lang != "en" || in.Title != "Terms" || !slices.Equal(in.Summary, []string{"a", "b"}) || strings.Contains(in.Body, "\r") {
		t.Errorf("normalized = %+v", in)
	}
	for _, bad := range []LegalDraftInput{
		{Kind: "eula", Lang: "en", Title: "x", Body: "x"},
		{Kind: "terms", Lang: "xx", Title: "x", Body: "x"},
		{Kind: "terms", Lang: "en", Title: "", Body: "x"},
		{Kind: "terms", Lang: "en", Title: "x", Body: "  "},
		{Kind: "terms", Lang: "en", Title: "x", Body: "x", Version: -1},
	} {
		if err := bad.Normalize(true); err == nil {
			t.Errorf("%+v: want error", bad)
		}
	}
	// An edit does not look at kind or lang.
	edit := LegalDraftInput{Title: "x", Body: "y"}
	if err := edit.Normalize(false); err != nil {
		t.Errorf("edit: %v", err)
	}
}

func TestLineDiff(t *testing.T) {
	got := LineDiff("a\nb\nc\n", "a\nc\nd")
	want := []DiffLine{{"=", "a"}, {"-", "b"}, {"=", "c"}, {"+", "d"}}
	if !slices.Equal(got, want) {
		t.Errorf("diff = %v, want %v", got, want)
	}
	if got := LineDiff("", "x"); !slices.Equal(got, []DiffLine{{"+", "x"}}) {
		t.Errorf("from empty = %v", got)
	}
	if got := LineDiff("same", "same"); !slices.Equal(got, []DiffLine{{"=", "same"}}) {
		t.Errorf("same = %v", got)
	}
	// Oversized inputs degrade to replace-all instead of a huge table.
	big := strings.Repeat("x\n", 2100)
	if got := LineDiff(big, big+"y"); len(got) != 2100*2+1 {
		t.Errorf("big diff has %d lines", len(got))
	}
}

func TestFeedbackNormalize(t *testing.T) {
	in := FeedbackInput{Category: "playback", Message: " stalls ", PageURL: "/en/releases/x?q=secret#a", Lang: "EN"}
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if in.Message != "stalls" || in.PageURL != "/en/releases/x" || in.Lang != "en" || in.Email != "" {
		t.Errorf("normalized = %+v", in)
	}
	for _, bad := range []FeedbackInput{
		{Category: "x", Message: "m"},
		{Category: "bug", Message: strings.Repeat("m", maxFeedbackMessage+1)},
		{Category: "bug", Message: "m", Email: "Name <a@example.com>"},
		{Category: "bug", Message: "m", PageURL: "ftp://x/y"},
		{Category: "bug", Message: "m", PageURL: "relative/path"},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v: want error", bad)
		}
	}
}

func validNotice() TakedownInput {
	return TakedownInput{ClaimantName: "A. Lawyer", ClaimantEmail: "legal@studio.example", RightsOwner: "Studio", Relationship: "agent",
		WorkDescription: "Film X", InfringingURLs: []string{" https://reel.example/releases/x ", "https://reel.example/releases/x", ""},
		GoodFaith: true, Accuracy: true, Signature: "A. Lawyer"}
}

func TestTakedownNormalize(t *testing.T) {
	in := validNotice()
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(in.InfringingURLs, []string{"https://reel.example/releases/x"}) || len(in.OriginalURLs) != 0 {
		t.Errorf("urls = %v / %v", in.InfringingURLs, in.OriginalURLs)
	}
	breakers := []func(*TakedownInput){
		func(x *TakedownInput) { x.GoodFaith = false },
		func(x *TakedownInput) { x.Accuracy = false },
		func(x *TakedownInput) { x.Signature = " " },
		func(x *TakedownInput) { x.ClaimantEmail = "nope" },
		func(x *TakedownInput) { x.InfringingURLs = nil },
		func(x *TakedownInput) { x.InfringingURLs = []string{"javascript:alert(1)"} },
		func(x *TakedownInput) { x.Relationship = "fan" },
		func(x *TakedownInput) { x.RightsOwner = "" },
	}
	for i, b := range breakers {
		x := validNotice()
		b(&x)
		if err := x.Normalize(); err == nil {
			t.Errorf("breaker %d: want error", i)
		}
	}
	c := CounterNoticeInput{Name: "Distributor", Email: "d@example.com", Address: "Tokyo", Statement: "licensed", Consent: true, Signature: "D"}
	if err := c.Normalize(); err != nil {
		t.Errorf("counter: %v", err)
	}
	c.Consent = false
	if err := c.Normalize(); err == nil {
		t.Error("counter without consent must fail")
	}
}

func TestTicketNumbers(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		tk, err := NewTicket()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := NormalizeTicket(strings.ToLower(tk)); !ok {
			t.Fatalf("ticket %q does not round-trip", tk)
		}
		if seen[tk] {
			t.Fatalf("duplicate ticket %q", tk)
		}
		seen[tk] = true
	}
	for _, bad := range []string{"", "TD-1", "1", "TD-ABCD-EFGH-IJKL-MNOP" /* I and L are not Crockford */, "TD-ABCD-EFGH-JKMN-PQRS-TVWX"} {
		if _, ok := NormalizeTicket(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestTakedownTransitionsAndActions(t *testing.T) {
	if err := CheckTakedownTransition(TakedownReceived, TakedownReviewing); err != nil {
		t.Error(err)
	}
	var ce *ConflictError
	if err := CheckTakedownTransition(TakedownReceived, TakedownActioned); !errors.As(err, &ce) {
		t.Errorf("actioned is set by an action, not by hand: %v", err)
	}
	if err := CheckTakedownTransition(TakedownClosed, TakedownReviewing); !errors.As(err, &ce) {
		t.Errorf("closed is final: %v", err)
	}
	for _, in := range []TakedownActionInput{
		{Action: TakedownUnpublishRelease, ReleaseID: 1},
		{Action: TakedownUnpublishRelease, ReleaseSlug: "abc-001"},
		{Action: TakedownSuppressImage, MediaAssetID: 9},
	} {
		if err := in.Normalize(); err != nil {
			t.Errorf("%+v: %v", in, err)
		}
	}
	for _, in := range []TakedownActionInput{
		{Action: TakedownUnpublishRelease},
		{Action: TakedownUnpublishRelease, ReleaseID: 1, ReleaseSlug: "x"},
		{Action: TakedownSuppressImage, MediaAssetID: 9, ReleaseID: 1},
		{Action: "delete_release", ReleaseID: 1},
	} {
		if err := in.Normalize(); err == nil {
			t.Errorf("%+v: want error", in)
		}
	}
}
