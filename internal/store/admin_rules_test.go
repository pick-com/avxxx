package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func isValidation(err error) bool {
	var ve *ValidationError
	return errors.As(err, &ve)
}

func isConflict(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

func TestConflictFieldMapping(t *testing.T) {
	tests := []struct {
		entity, field, column string
		kind                  fieldKind
		ok                    bool
	}{
		// Parser field names written by SaveCatalogRecord.
		{"release", "title", "canonical_title", fieldText, true},
		{"release", "maker", "maker_name", fieldText, true},
		{"release", "director", "director_name", fieldText, true},
		{"release", "label", "label_name", fieldText, true},
		{"release", "release_date", "release_date", fieldDate, true},
		{"release", "runtime_minutes", "runtime_minutes", fieldInt, true},
		// Column names (manual or legacy conflicts).
		{"release", "canonical_title", "canonical_title", fieldText, true},
		{"release", "title_ja", "title_ja", fieldText, true},
		{"performer", "name", "canonical_name", fieldText, true},
		{"performer", "name_ja", "canonical_name_ja", fieldText, true},
		{"performer", "canonical_name", "canonical_name", fieldText, true},
		{"performer", "birth_date", "birth_date", fieldDate, true},
		{"performer", "height_cm", "height_cm", fieldNumeric, true},
		// Not a single column: identifiers and relations.
		{"release", "dvd_code", "", 0, false},
		{"release", "content_id", "", 0, false},
		{"release", "tags", "", 0, false},
		{"release", "performers", "", 0, false},
		{"performer", "measurements", "", 0, false},
		{"performer", "title", "", 0, false},
		{"tag", "name", "", 0, false},
	}
	for _, tc := range tests {
		spec, ok := ConflictField(tc.entity, tc.field)
		if ok != tc.ok || spec.Column != tc.column || (ok && spec.Kind != tc.kind) {
			t.Errorf("ConflictField(%s,%s) = %+v,%v; want %s kind %d,%v", tc.entity, tc.field, spec, ok, tc.column, tc.kind, tc.ok)
		}
	}
}

func TestCoerceFieldValue(t *testing.T) {
	title, _ := ConflictField("release", "title")
	date, _ := ConflictField("release", "release_date")
	runtime, _ := ConflictField("release", "runtime_minutes")
	height, _ := ConflictField("performer", "height_cm")
	maker, _ := ConflictField("release", "maker")
	tests := []struct {
		name string
		spec FieldSpec
		raw  string
		want *string
		bad  bool
	}{
		{"json string unquoted", title, `"  New Title "`, ptr("New Title"), false},
		{"required null", title, `null`, nil, true},
		{"required empty", title, `""`, nil, true},
		{"text rejects number", title, `12`, nil, true},
		{"optional null", maker, `null`, nil, false},
		{"optional empty is null", maker, `""`, nil, false},
		{"date", date, `"2024-02-29"`, ptr("2024-02-29"), false},
		{"bad date", date, `"2024-02-30"`, nil, true},
		{"int from number", runtime, `120`, ptr("120"), false},
		{"int from string", runtime, `"120"`, ptr("120"), false},
		{"int out of range", runtime, `0`, nil, true},
		{"int not integer", runtime, `"12.5"`, nil, true},
		{"numeric", height, `"158.5"`, ptr("158.5"), false},
		{"numeric negative", height, `-3`, nil, true},
		{"object rejected", title, `{"a":1}`, nil, true},
		{"invalid json", title, `"unterminated`, nil, true},
	}
	for _, tc := range tests {
		got, err := CoerceFieldValue(tc.spec, json.RawMessage(tc.raw))
		if tc.bad {
			if !isValidation(err) {
				t.Errorf("%s: err %v, want ValidationError", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, deref(got), deref(tc.want))
		}
	}
}

// Regression: ResolveConflict built ”::jsonb from a NULL column (to_jsonb
// of NULL is SQL NULL). rawOrNull must turn it into JSON null.
func TestRawOrNull(t *testing.T) {
	if got := string(rawOrNull(nil)); got != "null" {
		t.Errorf("rawOrNull(nil) = %q", got)
	}
	if got := string(rawOrNull([]byte(`"x"`))); got != `"x"` {
		t.Errorf("rawOrNull(x) = %q", got)
	}
}

func ptr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func TestFieldOverridden(t *testing.T) {
	ov := map[string]bool{"canonical_title": true, "tags": true}
	for field, want := range map[string]bool{"title": true, "canonical_title": true, "tags": true, "genres": true, "maker": false, "performers": false} {
		if got := fieldOverridden("release", field, ov); got != want {
			t.Errorf("fieldOverridden(release,%s) = %v, want %v", field, got, want)
		}
	}
	if fieldOverridden("release", "title", nil) {
		t.Error("no overrides must not cover anything")
	}
	if !fieldOverridden("performer", "name", map[string]bool{"canonical_name": true}) {
		t.Error("performer name must map to canonical_name")
	}
}

func TestIngestUpdateGuardsOverrides(t *testing.T) {
	for _, col := range []string{"canonical_title", "maker_name"} {
		if !strings.Contains(releaseIngestUpdate, "o.field_name='"+col+"') THEN releases."+col) {
			t.Errorf("releaseIngestUpdate does not guard %s:\n%s", col, releaseIngestUpdate)
		}
	}
	for _, col := range []string{"canonical_name_ja", "birth_date", "birth_place", "debut_date", "height_cm", "cup_size"} {
		if !strings.Contains(performerIngestUpdate, "o.field_name='"+col+"') THEN performers."+col) {
			t.Errorf("performerIngestUpdate does not guard %s", col)
		}
	}
}

func TestParseReleasePatch(t *testing.T) {
	body := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	p, err := ParseReleasePatch(body(`{"title":" T ","title_ja":null,"release_date":"2020-05-01","shoot_year":2019,"runtime":95,
		"series":"S","director":"D","tags":["Big Tits"," big tits ","Drama"],"age_rating":18,"region_allow":["jp","us","JP"],
		"region_deny":[],"featured_note":"  ","version":3,"reason":"fix"}`))
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != 3 || p.Reason != "fix" {
		t.Errorf("version/reason = %d %q", p.Version, p.Reason)
	}
	got := map[string]ColumnEdit{}
	for _, c := range p.Columns {
		got[c.Column] = c
	}
	check := func(col, want string) {
		t.Helper()
		c, ok := got[col]
		if !ok {
			t.Errorf("%s missing", col)
			return
		}
		if deref(c.Value) != want {
			t.Errorf("%s = %s, want %s", col, deref(c.Value), want)
		}
	}
	check("canonical_title", "T")
	check("title_ja", "<nil>")
	check("release_date", "2020-05-01")
	check("shoot_year", "2019")
	check("runtime_minutes", "95")
	check("series_name", "S")
	check("director_name", "D")
	check("age_rating", "18")
	check("featured_note", "<nil>")
	if c := got["region_allow"]; !c.IsArray || strings.Join(c.Array, ",") != "JP,US" {
		t.Errorf("region_allow = %+v", c)
	}
	if c := got["region_deny"]; !c.IsArray || c.Array != nil {
		t.Errorf("region_deny = %+v, want NULL", c)
	}
	if p.Tags == nil || strings.Join(*p.Tags, "|") != "Big Tits|Drama" {
		t.Errorf("tags = %v", p.Tags)
	}
	if got["shoot_year"].SQLType != "integer" || got["release_date"].SQLType != "date" {
		t.Errorf("sql types: %+v", got)
	}

	for _, bad := range []string{
		`{}`,
		`{"version":1}`,
		`{"status":"published"}`,
		`{"slug":"x"}`,
		`{"dvd_code":"ABC-1"}`,
		`{"title":null}`,
		`{"shoot_year":1800}`,
		`{"shoot_year":"abc"}`,
		`{"release_date":"01/02/2020"}`,
		`{"age_rating":"21"}`,
		`{"age_rating":"R"}`,
		`{"region_allow":["USA"]}`,
		`{"region_allow":"JP"}`,
		`{"tags":"a"}`,
		`{"tags":["!!!"]}`,
		`{"title":"a","canonical_title":"b"}`,
		`{"version":-1,"title":"x"}`,
		`{"reason":` + `"` + strings.Repeat("x", MaxReasonLen+1) + `","title":"x"}`,
	} {
		if _, err := ParseReleasePatch(body(bad)); !isValidation(err) {
			t.Errorf("ParseReleasePatch(%s) err = %v, want ValidationError", bad, err)
		}
	}
}

func TestCheckTransition(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	future, past := now.Add(24*time.Hour), now.Add(-time.Hour)
	tests := []struct {
		name string
		from string
		t    Transition
		want string // "" ok, "400", "409"
	}{
		{"draft publish", ReleaseDraft, Transition{To: ReleasePublished}, ""},
		{"draft upcoming", ReleaseDraft, Transition{To: ReleaseUpcoming, ScheduledAt: &future}, ""},
		{"draft upcoming needs date", ReleaseDraft, Transition{To: ReleaseUpcoming}, "400"},
		{"upcoming in the past", ReleaseDraft, Transition{To: ReleaseUpcoming, ScheduledAt: &past}, "400"},
		{"scheduled_at only for upcoming", ReleaseDraft, Transition{To: ReleasePublished, ScheduledAt: &future}, "400"},
		{"draft unpublish", ReleaseDraft, Transition{To: ReleaseUnpublished}, ""},
		{"upcoming reschedule", ReleaseUpcoming, Transition{To: ReleaseUpcoming, ScheduledAt: &future}, ""},
		{"upcoming back to draft", ReleaseUpcoming, Transition{To: ReleaseDraft}, ""},
		{"published unpublish", ReleasePublished, Transition{To: ReleaseUnpublished}, ""},
		{"published to draft", ReleasePublished, Transition{To: ReleaseDraft}, "409"},
		{"published again", ReleasePublished, Transition{To: ReleasePublished}, "409"},
		{"unpublished republish", ReleaseUnpublished, Transition{To: ReleasePublished}, ""},
		{"held publish needs override", ReleaseHeld, Transition{To: ReleasePublished}, "400"},
		{"held publish blank override", ReleaseHeld, Transition{To: ReleasePublished, OverrideReason: "  "}, "400"},
		{"held publish with override", ReleaseHeld, Transition{To: ReleasePublished, OverrideReason: "shoot year verified"}, ""},
		{"held takedown", ReleaseHeld, Transition{To: ReleaseUnpublished}, ""},
		{"held to draft forbidden", ReleaseHeld, Transition{To: ReleaseDraft}, "409"},
		{"held to upcoming forbidden", ReleaseHeld, Transition{To: ReleaseUpcoming, ScheduledAt: &future}, "409"},
		{"admin cannot hold", ReleaseDraft, Transition{To: ReleaseHeld}, "409"},
		{"override outside hold", ReleaseDraft, Transition{To: ReleasePublished, OverrideReason: "x"}, "400"},
		{"unknown status", ReleaseDraft, Transition{To: "archived"}, "400"},
		{"unknown from", "weird", Transition{To: ReleasePublished}, "409"},
	}
	for _, tc := range tests {
		err := CheckTransition(tc.from, tc.t, now)
		switch tc.want {
		case "":
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
		case "400":
			if !isValidation(err) {
				t.Errorf("%s: err %v, want ValidationError", tc.name, err)
			}
		case "409":
			if !isConflict(err) {
				t.Errorf("%s: err %v, want ConflictError", tc.name, err)
			}
		}
	}
	for _, s := range []string{ReleasePublished, ReleaseUpcoming} {
		if !NeedsAgeGate(s) {
			t.Errorf("%s must pass R-AGE", s)
		}
	}
	for _, s := range []string{ReleaseDraft, ReleaseUnpublished, ReleaseHeld} {
		if NeedsAgeGate(s) {
			t.Errorf("%s must not need R-AGE", s)
		}
	}
}

func TestAgeAtFilming(t *testing.T) {
	birth := time.Date(2000, 8, 1, 0, 0, 0, 0, time.UTC)
	rel := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	if a := AgeAtFilming(&birth, 2020, &rel); a == nil || a.Age != 20 || a.Basis != "shoot_year" || a.Approx {
		t.Errorf("shoot year preferred: %+v", a)
	}
	if a := AgeAtFilming(&birth, 0, &rel); a == nil || a.Age != 19 || a.Basis != "release_year" || !a.Approx {
		t.Errorf("release year approx: %+v", a)
	}
	if a := AgeAtFilming(nil, 2020, &rel); a != nil {
		t.Errorf("no birth date: %+v", a)
	}
	if a := AgeAtFilming(&birth, 0, nil); a != nil {
		t.Errorf("no year: %+v", a)
	}
}

func TestSourceInputNormalize(t *testing.T) {
	u := "https://partner.example/embed/1"
	lic := "LIC-1"
	ok := func() SourceInput {
		return SourceInput{Kind: KindOfficialEmbed, Provider: " p ", Label: "Main", URL: &u, LicenseRef: &lic,
			AudioLangs: []string{"ja", "ja", "en-US"}, RegionAllow: []string{"jp"}}
	}
	in := ok()
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if in.Provider != "p" || in.VersionKind != VersionMain || *in.Priority != 100 || strings.Join(in.AudioLangs, ",") != "ja,en-US" ||
		strings.Join(in.RegionAllow, ",") != "JP" || in.SubtitleLangs == nil {
		t.Errorf("normalized = %+v", in)
	}
	empty := "  "
	bad := map[string]func(*SourceInput){
		"kind":            func(x *SourceInput) { x.Kind = "torrent" },
		"provider":        func(x *SourceInput) { x.Provider = "" },
		"label":           func(x *SourceInput) { x.Label = " " },
		"url missing":     func(x *SourceInput) { x.URL = nil },
		"url scheme":      func(x *SourceInput) { s := "ftp://x/y"; x.URL = &s },
		"license missing": func(x *SourceInput) { x.LicenseRef = nil },
		"license blank":   func(x *SourceInput) { x.LicenseRef = &empty },
		"hls no prefix":   func(x *SourceInput) { x.Kind = KindHLSOwned; x.URL = nil },
		"max_height":      func(x *SourceInput) { h := 0; x.MaxHeight = &h },
		"duration":        func(x *SourceInput) { d := -1; x.DurationSeconds = &d },
		"lang":            func(x *SourceInput) { x.SubtitleLangs = []string{"english language"} },
		"version":         func(x *SourceInput) { x.VersionKind = "remix" },
		"priority":        func(x *SourceInput) { p := -5; x.Priority = &p },
		"region":          func(x *SourceInput) { x.RegionDeny = []string{"EUR"} },
	}
	for name, mut := range bad {
		x := ok()
		mut(&x)
		if err := x.Normalize(); !isValidation(err) {
			t.Errorf("%s: err %v, want ValidationError", name, err)
		}
	}
	// Previews and external links need no license; hls_owned needs a prefix.
	for _, kind := range []string{KindOfficialPreview, KindExternalLink} {
		x := ok()
		x.Kind, x.LicenseRef = kind, nil
		if err := x.Normalize(); err != nil {
			t.Errorf("%s without license_ref: %v", kind, err)
		}
	}
	prefix := "s3://bucket/rel/1/"
	x := SourceInput{Kind: KindHLSOwned, Provider: "own", Label: "Main", StoragePrefix: &prefix, LicenseRef: &lic}
	if err := x.Normalize(); err != nil {
		t.Errorf("hls_owned: %v", err)
	}
	if RequiresLicense(KindOfficialPreview) || RequiresLicense(KindExternalLink) || !RequiresLicense(KindOfficialEmbed) || !RequiresLicense(KindHLSOwned) {
		t.Error("RequiresLicense")
	}
}

func TestApplySourcePatch(t *testing.T) {
	u, lic := "https://p.example/x", "L"
	in := SourceInput{Kind: KindOfficialEmbed, Provider: "p", Label: "Main", URL: &u, LicenseRef: &lic}
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if err := ApplySourcePatch(&in, json.RawMessage(`{"label":"4K","max_height":2160,"version_kind":"high_quality"}`)); err != nil {
		t.Fatal(err)
	}
	if in.Label != "4K" || *in.MaxHeight != 2160 || in.VersionKind != VersionHighQuality || in.Provider != "p" {
		t.Errorf("patched = %+v", in)
	}
	// Clearing license_ref on an embed breaks the license rule.
	cp := in
	if err := ApplySourcePatch(&cp, json.RawMessage(`{"license_ref":null}`)); !isValidation(err) {
		t.Errorf("clearing license_ref: %v", err)
	}
	for _, bad := range []string{`{}`, `{"status":"active"}`, `{"health_score":1}`, `[1]`} {
		cp := in
		if err := ApplySourcePatch(&cp, json.RawMessage(bad)); !isValidation(err) {
			t.Errorf("ApplySourcePatch(%s) = %v, want ValidationError", bad, err)
		}
	}
}

func TestReleasePerformerInput(t *testing.T) {
	in := ReleasePerformerInput{PerformerID: 3, Role: " Lead "}
	if err := in.Normalize(); err != nil || in.Role != RoleLead {
		t.Errorf("lead: %v %q", err, in.Role)
	}
	for _, x := range []ReleasePerformerInput{{PerformerID: 3, Role: "star"}, {PerformerID: 0, Role: "guest"}, {PerformerID: 1, Role: "guest", BillingOrder: new(int)}} {
		err := x.Normalize()
		if x.BillingOrder != nil {
			if err != nil {
				t.Errorf("billing 0 ok: %v", err)
			}
			continue
		}
		if !isValidation(err) {
			t.Errorf("%+v: %v", x, err)
		}
	}
}

func TestPlanPlaybackReleaseRegion(t *testing.T) {
	u := "https://x"
	srcs := []PlaybackSource{{ID: 1, Kind: KindExternalLink, URL: &u, Status: "active", VersionKind: VersionMain, Priority: 1}}
	rel := PlaybackRelease{RegionAllow: []string{"JP"}}
	if got := PlanPlayback(rel, srcs, "JP", 0).Sources; len(got) != 1 {
		t.Errorf("JP allowed: %d sources", len(got))
	}
	if got := PlanPlayback(rel, srcs, "US", 0).Sources; len(got) != 0 {
		t.Errorf("US outside release allow list: %d sources", len(got))
	}
	rel = PlaybackRelease{RegionDeny: []string{"DE"}}
	if got := PlanPlayback(rel, srcs, "DE", 0).Sources; len(got) != 0 {
		t.Errorf("DE denied at release level: %d sources", len(got))
	}
	if got := PlanPlayback(PlaybackRelease{}, srcs, "", 0).Sources; len(got) != 1 {
		t.Errorf("no release lists: %d sources", len(got))
	}
}
