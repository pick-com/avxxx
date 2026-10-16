package i18n

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"zh-TW": ZhTW, "zh-tw": ZhTW, "zh_Hant": ZhTW, "zh-Hant-HK": ZhTW, "zh-HK": ZhTW, "zh-MO": ZhTW,
		"zh": ZhCN, "zh-CN": ZhCN, "zh-Hans": ZhCN, "zh-SG": ZhCN,
		"en": En, "EN-us": En, "ja-JP": Ja, "ko": Ko, "vi-VN": Vi, "es-419": Es, "th": Th,
		"fr": "", "": "", "  ": "", "x": "",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromAcceptLanguage(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"fr-FR,fr;q=0.9":                     "",
		"zh-TW,zh;q=0.9,en;q=0.8":            ZhTW,
		"fr,en;q=0.5,ja;q=0.7":               Ja,
		"en;q=0.2, ko":                       Ko,
		"zh-Hant;q=0, en":                    En,
		"es;q=0.5,th;q=0.5":                  Es, // ties keep header order
		"*;q=0.1, vi;q=abc, ja":              Ja, // malformed q counts as 0
		"en-US,en;q=0.9,zh-CN;q=0.8,zh;q=.7": En,
	}
	for in, want := range cases {
		if got := FromAcceptLanguage(in); got != want {
			t.Errorf("FromAcceptLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFallbackChain(t *testing.T) {
	cases := map[string][]string{
		"":   nil,
		ZhTW: {ZhTW, ZhCN, En},
		ZhCN: {ZhCN, ZhTW, En},
		En:   {En, ZhTW, ZhCN},
		Ko:   {Ko, ZhTW, ZhCN, En},
		Ja:   {Ja},
	}
	for in, want := range cases {
		if got := FallbackChain(in); !reflect.DeepEqual(got, want) {
			t.Errorf("FallbackChain(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestResolve(t *testing.T) {
	r := Ref{EntityRelease, "7", "title"}
	other := Ref{EntityRelease, "8", "title"}
	set := Set{}
	set.Add(r, ZhCN, "简体标题")
	set.Add(r, En, "English title")
	set.Add(r, Ko, "") // empty never wins
	set.Add(other, Vi, "Tiêu đề")

	tests := []struct {
		name     string
		ref      Ref
		lang     string
		original string
		want     string
		wantLang string
	}{
		{"exact", r, ZhCN, "原題", "简体标题", ZhCN},
		{"zh-TW falls to zh-CN", r, ZhTW, "原題", "简体标题", ZhCN},
		{"ko skips empty, goes zh-TW then zh-CN", r, Ko, "原題", "简体标题", ZhCN},
		{"en exact", r, En, "原題", "English title", En},
		{"ja goes straight to original", r, Ja, "原題", "原題", Original},
		{"nothing translated", Ref{EntityRelease, "9", "title"}, ZhTW, "原題", "原題", Original},
		{"other entity in vi", other, Vi, "x", "Tiêu đề", Vi},
		{"other entity not in chain", other, ZhTW, "x", "x", Original},
		{"empty original and no translation", Ref{EntityRelease, "9", "synopsis"}, En, "", "", ""},
	}
	for _, tt := range tests {
		v, l := set.Resolve(tt.ref, FallbackChain(tt.lang), tt.original)
		if v != tt.want || l != tt.wantLang {
			t.Errorf("%s: got (%q,%q), want (%q,%q)", tt.name, v, l, tt.want, tt.wantLang)
		}
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		from      string
		submitted bool
		action    string
		matches   bool
		want      string // "" = ErrTransition
	}{
		{StatusMachine, false, ActionEdit, true, StatusDraft},
		{StatusReviewed, false, ActionEdit, true, StatusDraft},
		{StatusStale, false, ActionEdit, false, StatusDraft},
		{StatusMachine, false, ActionSubmit, true, StatusDraft},
		{StatusDraft, false, ActionSubmit, true, StatusDraft},
		{StatusDraft, true, ActionSubmit, true, StatusDraft}, // idempotent
		{StatusReviewed, false, ActionSubmit, true, ""},
		{StatusStale, false, ActionSubmit, false, ""},
		{StatusDraft, true, ActionReview, true, StatusReviewed},
		{StatusDraft, true, ActionReview, false, StatusStale}, // source moved on since the draft
		{StatusDraft, false, ActionReview, true, ""},          // must be submitted first
		{StatusMachine, false, ActionReview, true, ""},
		{StatusStale, false, ActionReview, false, StatusReviewed}, // re-confirm against new source
		{StatusReviewed, false, ActionReview, true, ""},
		{StatusDraft, false, "publish", true, ""},
	}
	for _, tt := range tests {
		got, err := Next(tt.from, tt.submitted, tt.action, tt.matches)
		if tt.want == "" {
			if err != ErrTransition {
				t.Errorf("Next(%s,%v,%s): got %q,%v want ErrTransition", tt.from, tt.submitted, tt.action, got, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("Next(%s,%v,%s,%v) = %q,%v want %q", tt.from, tt.submitted, tt.action, tt.matches, got, err, tt.want)
		}
	}
}

func TestPublicAndStatuses(t *testing.T) {
	for s, want := range map[string]bool{StatusMachine: false, StatusDraft: false, StatusReviewed: true, StatusStale: true, "x": false} {
		if Public(s) != want {
			t.Errorf("Public(%q) = %v", s, !want)
		}
		if ValidStatus(s) != (s != "x") {
			t.Errorf("ValidStatus(%q) wrong", s)
		}
	}
}

func TestSourceHashAndStale(t *testing.T) {
	// sha256("") and sha256("abc"): must match SQL i18n_hash.
	if got := SourceHash(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("SourceHash(\"\") = %s", got)
	}
	if got := SourceHash("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("SourceHash(abc) = %s", got)
	}
	h := SourceHash("元のタイトル")
	if IsStale(StatusReviewed, h, "元のタイトル") {
		t.Error("unchanged source must not be stale")
	}
	if !IsStale(StatusReviewed, h, "新しいタイトル") {
		t.Error("changed source must make a reviewed translation stale")
	}
	for _, s := range []string{StatusDraft, StatusMachine, StatusStale} {
		if IsStale(s, h, "新しいタイトル") {
			t.Errorf("%s must not be re-marked stale", s)
		}
	}
}

func TestRefValidate(t *testing.T) {
	ok := []Ref{{EntityRelease, "1", "title"}, {EntityRelease, "42", "synopsis"}, {EntityPerformer, "3", "name"}, {EntityPerformer, "3", "biography"}, {EntityTag, "big-tits", "name"}}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	bad := []Ref{{"video", "1", "title"}, {EntityRelease, "1", "name"}, {EntityRelease, "0", "title"}, {EntityRelease, "01", "title"}, {EntityRelease, "x", "title"}, {EntityPerformer, "-1", "name"}, {EntityTag, "", "name"}, {EntityTag, "a", "title"}}
	for _, r := range bad {
		if r.Validate() == nil {
			t.Errorf("%+v: want error", r)
		}
	}
}

// The migration's CHECK constraints must list exactly the Go languages.
func TestMigrationLanguagesMatch(t *testing.T) {
	b, err := os.ReadFile("../../migrations/015_i18n.sql")
	if err != nil {
		t.Fatal(err)
	}
	quoted := make([]string, len(Languages))
	for i, l := range Languages {
		quoted[i] = "'" + l + "'"
	}
	want := "lang IN (" + strings.Join(quoted, ",") + ")"
	if n := strings.Count(string(b), want); n != 3 {
		t.Errorf("migration has %d occurrences of %s, want 3 (translations, glossary_terms, ui_strings)", n, want)
	}
	for _, f := range []string{"'title'", "'synopsis'", "'name'", "'biography'"} {
		if !strings.Contains(string(b), f) {
			t.Errorf("migration does not mention field %s", f)
		}
	}
}
