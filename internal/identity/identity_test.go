package identity

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Yui Hatano", "yuihatano"},
		{"  yui   hatano ", "yuihatano"},
		{"YUI-HATANO", "yuihatano"},
		{"Ｙｕｉ　Ｈａｔａｎｏ", "yuihatano"}, // full-width Latin and ideographic space
		{"波多野 結衣", "波多野結衣"},
		{"ﾊﾀﾉﾕｲ", "ハタノユイ"}, // half-width katakana composes under NFKC
		{"Ai.Uehara!", "aiuehara"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeName(tt.in); got != tt.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestReorderedNameKeyAndAliasForm(t *testing.T) {
	if a, b := ReorderedNameKey("Hatano Yui"), ReorderedNameKey("yui  HATANO"); a != b || a == "" {
		t.Errorf("reordered keys differ: %q vs %q", a, b)
	}
	if k := ReorderedNameKey("Yui"); k != "" {
		t.Errorf("single token key = %q, want empty", k)
	}
	if f := AliasForm("  Ｙｕｉ　 Hatano "); f != "yui hatano" {
		t.Errorf("AliasForm = %q", f)
	}
}

func TestCodeKeys(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"JUQ-743", []string{"juq-743"}},
		{"juq743", []string{"juq-743"}},
		{"JUQ 743", []string{"juq-743"}},
		{"juq00743", []string{"juq-743"}},
		{"ＪＵＱ－７４３", []string{"juq-743"}},
		{"ABP-5", []string{"abp-005"}},
		{"118abp00123", []string{"118abp-123", "abp-123"}},
		{"300MIUM-123", []string{"300mium-123", "mium-123"}},
		{"SSIS-001z", []string{"ssis-001z"}},
		{"heyzo_1234", []string{"heyzo-1234"}},
		{"123456_789", []string{"123456789"}},
		{"--", nil},
	}
	for _, tt := range tests {
		if got := CodeKeys(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("CodeKeys(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestTitleSimilarity(t *testing.T) {
	if s := TitleSimilarity("Summer Story", "summer  story!"); s != 1 {
		t.Errorf("identical titles = %v", s)
	}
	if s := TitleSimilarity("abc", ""); s != 0 {
		t.Errorf("empty = %v", s)
	}
	near := TitleSimilarity("新人NO.1 STYLE 波多野結衣 AVデビュー", "新人 NO.1 STYLE 波多野結衣AVデビュー 特典")
	far := TitleSimilarity("新人NO.1 STYLE 波多野結衣 AVデビュー", "完全主観 温泉旅行")
	if near < 0.8 || far > 0.3 || near <= far {
		t.Errorf("near=%v far=%v", near, far)
	}
	if a, b := TitleSimilarity("x y z w", "w z y x q"), TitleSimilarity("w z y x q", "x y z w"); a != b {
		t.Errorf("not symmetric: %v vs %v", a, b)
	}
}

func TestCanonicalPair(t *testing.T) {
	if CanonicalPair(7, 3) != CanonicalPair(3, 7) || CanonicalPair(7, 3) != (Pair{3, 7}) {
		t.Fatal("CanonicalPair is not order-insensitive")
	}
}

func rules(e Evidence) []string {
	out := []string{}
	for _, r := range e.Rules {
		out = append(out, r.Rule)
	}
	return out
}

func TestScorePerformers(t *testing.T) {
	base := Performer{ID: 1, Names: []string{"Yui Hatano", "波多野結衣"}, BirthDate: "1988-05-24", ReleaseIDs: []int64{10, 11, 12}}
	tests := []struct {
		name  string
		b     Performer
		score float64
		rules []string
	}{
		{"name+birth+2 shared", Performer{ID: 2, Names: []string{"yui-hatano"}, BirthDate: "1988-05-24", ReleaseIDs: []int64{11, 12}}, 1, []string{"name_match", "birth_date_match", "shared_releases"}},
		{"japanese alias only", Performer{ID: 2, Names: []string{"Hatano Y.", "波多野 結衣"}}, 0.5, []string{"name_match"}},
		{"reordered name", Performer{ID: 2, Names: []string{"Hatano Yui"}}, 0.35, []string{"name_reordered"}},
		{"name but birth differs", Performer{ID: 2, Names: []string{"Yui Hatano"}, BirthDate: "1990-01-01"}, 0, []string{"name_match", "birth_date_differ"}},
		{"co-star only", Performer{ID: 2, Names: []string{"Someone Else"}, ReleaseIDs: []int64{10}}, 0.1, []string{"shared_releases"}},
		{"shared releases capped", Performer{ID: 2, Names: []string{"Yui Hatano"}, ReleaseIDs: []int64{10, 11, 12}}, 0.7, []string{"name_match", "shared_releases"}},
	}
	for _, tt := range tests {
		e := ScorePerformers(base, tt.b)
		if e.Score != tt.score || !reflect.DeepEqual(rules(e), tt.rules) {
			t.Errorf("%s: score %v rules %v, want %v %v", tt.name, e.Score, rules(e), tt.score, tt.rules)
		}
		if r := ScorePerformers(tt.b, base); r.Score != e.Score {
			t.Errorf("%s: asymmetric score %v vs %v", tt.name, e.Score, r.Score)
		}
	}
}

func TestScoreReleases(t *testing.T) {
	base := Release{ID: 1, Codes: []string{"JUQ-743"}, Titles: []string{"Summer Affair Story"}, RuntimeMinutes: 120, PerformerIDs: []int64{5, 6}}
	tests := []struct {
		name  string
		b     Release
		score float64
		rules []string
	}{
		{"same everything", Release{ID: 2, Codes: []string{"juq00743"}, Titles: []string{"summer affair story"}, RuntimeMinutes: 121, PerformerIDs: []int64{5, 6}}, 1, []string{"code_match", "title_similarity", "runtime_match", "shared_performers"}},
		{"code only", Release{ID: 2, Codes: []string{"JUQ743"}}, 0.6, []string{"code_match"}},
		{"loose code", Release{ID: 2, Codes: []string{"1juq00743"}}, 0.45, []string{"code_loose_match"}},
		{"title only", Release{ID: 2, Titles: []string{"Summer Affair Story"}}, 0.25, []string{"title_similarity"}},
		{"different codes same title", Release{ID: 2, Codes: []string{"JUQ-744"}, Titles: []string{"Summer Affair Story"}}, 0, []string{"code_differ", "title_similarity"}},
		{"runtime off by 3%", Release{ID: 2, Codes: []string{"JUQ-743"}, RuntimeMinutes: 124}, 0.6, []string{"code_match"}},
		{"runtime far", Release{ID: 2, Codes: []string{"JUQ-743"}, RuntimeMinutes: 60}, 0.45, []string{"code_match", "runtime_differ"}},
	}
	for _, tt := range tests {
		e := ScoreReleases(base, tt.b)
		if e.Score != tt.score || !reflect.DeepEqual(rules(e), tt.rules) {
			t.Errorf("%s: score %v rules %v, want %v %v", tt.name, e.Score, rules(e), tt.score, tt.rules)
		}
	}
}

func TestPerformerCandidates(t *testing.T) {
	ps := []Performer{
		{ID: 1, Names: []string{"Yui Hatano"}, BirthDate: "1988-05-24"},
		{ID: 2, Names: []string{"YUI HATANO"}, BirthDate: "1988-05-24"},
		{ID: 3, Names: []string{"Hatano Yui"}},
		{ID: 4, Names: []string{"Ai Uehara"}},
		{ID: 5, Names: []string{"Ai Uehara"}},
		{ID: 6, Names: []string{"Ai Uehara"}, BirthDate: "1992-11-12"},
		{ID: 7, Names: []string{"Ai Uehara"}, BirthDate: "1999-01-01"},
	}
	known := Known{Existing: map[Pair]bool{{4, 5}: true}, NotDuplicate: map[Pair]bool{{4, 6}: true}}
	got, st := PerformerCandidates(ps, known)
	pairs := map[Pair]float64{}
	for _, c := range got {
		pairs[c.Pair] = c.Evidence.Score
	}
	want := map[Pair]float64{{1, 2}: 0.8, {4, 7}: 0.5, {5, 6}: 0.5, {5, 7}: 0.5}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("candidates %v, want %v", pairs, want)
	}
	if got[0].Pair != (Pair{1, 2}) {
		t.Errorf("not sorted by score: first %v", got[0].Pair)
	}
	// (1,3), (2,3) reordered only = 0.35; (6,7) birth dates differ.
	if st.SkippedExisting != 1 || st.SkippedNotDuplicate != 1 || st.BelowThreshold != 3 || st.Generated != 4 || st.Entities != 7 {
		t.Errorf("stats %+v", st)
	}
	// A second run with everything known generates nothing (idempotent).
	for _, c := range got {
		known.Existing[CanonicalPair(c.Hi, c.Lo)] = true
	}
	if again, _ := PerformerCandidates(ps, known); len(again) != 0 {
		t.Errorf("second run generated %d", len(again))
	}
}

func TestReleaseCandidates(t *testing.T) {
	rs := []Release{
		{ID: 10, Codes: []string{"JUQ-743"}, Titles: []string{"Summer Story"}},
		{ID: 11, Codes: []string{"juq00743"}, Titles: []string{"Summer Story (HD)"}},
		{ID: 12, Titles: []string{"Summer Story"}, RuntimeMinutes: 100},
		{ID: 13, Codes: []string{"ABP-1"}, Titles: []string{"Summer Story"}},
	}
	got, st := ReleaseCandidates(rs, Known{})
	pairs := map[Pair]bool{}
	for _, c := range got {
		pairs[c.Pair] = true
	}
	if !pairs[Pair{10, 11}] || len(pairs) != 1 {
		t.Errorf("candidates %v, want only {10 11}", pairs)
	}
	// 10-11 via code; 10-12, 10-13, 12-13 via the shared title. Title-only
	// (0.25) and different-code pairs stay below the threshold.
	if st.Compared != 4 || st.BelowThreshold != 3 {
		t.Errorf("stats %+v", st)
	}
}

func TestBuildMergePlanPerformer(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	in := MergeInput{
		EntityType: PerformerType, TargetID: 1, SourceID: 2,
		Target: raw(`{"id":1,"canonical_name":"Yui Hatano","canonical_name_ja":"波多野結衣"}`),
		Source: raw(`{"id":2,"canonical_name":"Hatano Yui","canonical_name_ja":"波多野結衣"}`),
		TargetRows: map[string][]json.RawMessage{
			"performer_aliases":  {raw(`{"id":100,"performer_id":1,"normalized_name":"yui"}`)},
			"release_performers": {raw(`{"release_id":10,"performer_id":1,"billing_order":0}`)},
		},
		SourceRows: map[string][]json.RawMessage{
			"performer_aliases": {
				raw(`{"id":200,"performer_id":2,"normalized_name":"yui"}`),
				raw(`{"id":201,"performer_id":2,"normalized_name":"hatano"}`),
			},
			"release_performers": {
				raw(`{"release_id":10,"performer_id":2,"billing_order":1}`),
				raw(`{"release_id":11,"performer_id":2,"billing_order":0}`),
			},
			"source_records": {raw(`{"id":900,"performer_id":2}`)},
			"performer_source_metrics": {
				raw(`{"performer_id":2,"source_id":3,"captured_at":"2026-01-01T00:00:00+00:00"}`),
			},
		},
	}
	plan, err := BuildMergePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	byTable := map[string]TablePlan{}
	for _, tp := range plan.Tables {
		byTable[tp.Table] = tp
	}
	if len(plan.Tables) != len(PerformerTables) {
		t.Errorf("plan covers %d tables, want %d", len(plan.Tables), len(PerformerTables))
	}
	check := func(table string, move []string, drop int) {
		t.Helper()
		tp := byTable[table]
		got := []string{}
		for _, k := range tp.Move {
			got = append(got, string(k))
		}
		if !reflect.DeepEqual(got, move) || len(tp.Drop) != drop {
			t.Errorf("%s: move %v drop %d, want %v %d", table, got, len(tp.Drop), move, drop)
		}
	}
	check("performer_aliases", []string{`["hatano"]`}, 1)
	check("release_performers", []string{`[11]`}, 1)
	check("source_records", []string{`[900]`}, 0)
	check("performer_source_metrics", []string{`[3,"2026-01-01T00:00:00+00:00"]`}, 0)
	check("release_performer_sources", []string{}, 0)
	if string(byTable["release_performers"].Drop[0]) != `{"release_id":10,"performer_id":2,"billing_order":1}` {
		t.Errorf("dropped row not kept verbatim: %s", byTable["release_performers"].Drop[0])
	}
	// "Hatano Yui" is new; 波多野結衣 is already the target's Japanese name.
	if want := []Alias{{Name: "Hatano Yui", NormalizedName: "hatano yui"}}; !reflect.DeepEqual(plan.AddAliases, want) {
		t.Errorf("aliases %v, want %v", plan.AddAliases, want)
	}
}

func TestBuildMergePlanRelease(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	in := MergeInput{
		EntityType: ReleaseType, TargetID: 10, SourceID: 11,
		Target: raw(`{"id":10}`), Source: raw(`{"id":11}`),
		TargetRows: map[string][]json.RawMessage{
			"release_tags":          {raw(`{"release_id":10,"normalized_tag":"drama","tag":"Drama"}`)},
			"favorites":             {raw(`{"user_id":7,"release_id":10}`)},
			"release_media_sources": {raw(`{"release_id":10,"source":"javtiful","media_id":"a"}`)},
		},
		SourceRows: map[string][]json.RawMessage{
			"release_identifiers":   {raw(`{"id":1,"release_id":11}`), raw(`{"id":2,"release_id":11}`)},
			"release_tags":          {raw(`{"release_id":11,"normalized_tag":"drama","tag":"drama"}`), raw(`{"release_id":11,"normalized_tag":"vr","tag":"VR"}`)},
			"favorites":             {raw(`{"user_id":7,"release_id":11}`), raw(`{"user_id":8,"release_id":11}`)},
			"release_media_sources": {raw(`{"release_id":11,"source":"javtiful","media_id":"b"}`), raw(`{"release_id":11,"source":"eporner","media_id":"c"}`)},
			// The target is an edition of the source: its own row must not move.
			"releases": {raw(`{"id":10,"edition_of_release_id":11}`), raw(`{"id":12,"edition_of_release_id":11}`)},
		},
	}
	plan, err := BuildMergePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string][2]int{}
	for _, tp := range plan.Tables {
		counts[tp.Table] = [2]int{len(tp.Move), len(tp.Drop)}
	}
	want := map[string][2]int{
		"release_identifiers": {2, 0}, "release_tags": {1, 1}, "favorites": {1, 1},
		"release_media_sources": {1, 1}, "releases": {1, 0},
	}
	for table, w := range want {
		if counts[table] != w {
			t.Errorf("%s: move/drop %v, want %v", table, counts[table], w)
		}
	}
	if len(plan.AddAliases) != 0 {
		t.Errorf("releases get no aliases: %v", plan.AddAliases)
	}
}

func TestBuildMergePlanRejects(t *testing.T) {
	for _, in := range []MergeInput{
		{EntityType: PerformerType, TargetID: 1, SourceID: 1},
		{EntityType: "studio", TargetID: 1, SourceID: 2},
		{EntityType: ReleaseType, TargetID: 1, SourceID: 2, SourceRows: map[string][]json.RawMessage{"release_tags": {json.RawMessage(`{"release_id":2}`)}}},
	} {
		if _, err := BuildMergePlan(in); err == nil {
			t.Errorf("BuildMergePlan(%+v) succeeded", in)
		}
	}
}

func TestMergePlanRoundTrip(t *testing.T) {
	plan := MergePlan{EntityType: ReleaseType, TargetID: 1, SourceID: 2,
		Tables:     []TablePlan{{Table: "release_tags", FK: "release_id", Key: []string{"normalized_tag"}, Move: []json.RawMessage{json.RawMessage(`["vr"]`)}, Drop: []json.RawMessage{}}},
		AddAliases: []Alias{}}
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var back MergePlan
	if err = json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, back) {
		t.Errorf("round trip changed plan:\n%+v\n%+v", plan, back)
	}
}

func planByTable(t *testing.T, in MergeInput) map[string]TablePlan {
	t.Helper()
	plan, err := BuildMergePlan(in)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]TablePlan{}
	for _, tp := range plan.Tables {
		out[tp.Table] = tp
	}
	// Plans are stored as JSON and replayed by undo: patches must survive.
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var back MergePlan
	if err = json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(plan, back) {
		t.Fatalf("round trip changed plan: %v", err)
	}
	return out
}

func keys(ms []json.RawMessage) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, string(m))
	}
	return out
}

func checkPatch(t *testing.T, got RowPatch, side, key, set, before string) {
	t.Helper()
	if got.Side != side || string(got.Key) != key || string(got.Set) != set || string(got.Before) != before {
		t.Errorf("patch = {%s %s %s %s}, want {%s %s %s %s}", got.Side, got.Key, got.Set, got.Before, side, key, set, before)
	}
}

func TestBuildMergePlanFollows(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	in := MergeInput{
		EntityType: PerformerType, TargetID: 1, SourceID: 2,
		Target: raw(`{"id":1,"canonical_name":"A"}`), Source: raw(`{"id":2,"canonical_name":"A"}`),
		TargetRows: map[string][]json.RawMessage{"follows": {
			raw(`{"user_id":7,"performer_id":1,"notify_mode":"lead_only","created_at":"2026-03-01T00:00:00+00:00"}`),
			raw(`{"user_id":8,"performer_id":1,"notify_mode":"all","created_at":"2026-01-01T00:00:00+00:00"}`),
		}},
		SourceRows: map[string][]json.RawMessage{"follows": {
			// Earlier and more permissive: both columns fold into the target.
			raw(`{"user_id":7,"performer_id":2,"notify_mode":"all","created_at":"2026-02-01T00:00:00+09:00"}`),
			// Later and less permissive: dropped, target unchanged.
			raw(`{"user_id":8,"performer_id":2,"notify_mode":"off","created_at":"2026-05-01T00:00:00+00:00"}`),
			raw(`{"user_id":9,"performer_id":2,"notify_mode":"off","created_at":"2026-05-01T00:00:00+00:00"}`),
		}},
	}
	tp := planByTable(t, in)["follows"]
	if got := keys(tp.Move); !reflect.DeepEqual(got, []string{`[9]`}) || len(tp.Drop) != 2 {
		t.Fatalf("follows: move %v drop %d", got, len(tp.Drop))
	}
	if len(tp.Patch) != 1 {
		t.Fatalf("follows patches = %+v, want 1", tp.Patch)
	}
	checkPatch(t, tp.Patch[0], PatchTarget, `[7]`,
		`{"created_at":"2026-02-01T00:00:00+09:00","notify_mode":"all"}`,
		`{"created_at":"2026-03-01T00:00:00+00:00","notify_mode":"lead_only"}`)
}

func TestBuildMergePlanReleaseUserTables(t *testing.T) {
	raw := func(s string) json.RawMessage { return json.RawMessage(s) }
	now := time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC)
	in := MergeInput{
		EntityType: ReleaseType, TargetID: 10, SourceID: 11, Target: raw(`{"id":10}`), Source: raw(`{"id":11}`), Now: now,
		TargetRows: map[string][]json.RawMessage{
			"reservations":          {raw(`{"user_id":7,"release_id":10}`)},
			"watch_later":           {raw(`{"user_id":7,"release_id":10,"position":5}`), raw(`{"user_id":8,"release_id":10,"position":1}`)},
			"notification_receipts": {raw(`{"user_id":7,"release_id":10,"reason":"follow"}`)},
			"playback_sources":      {raw(`{"id":1,"release_id":10}`)},
			"audit_issues":          {raw(`{"id":50,"entity_id":10,"rule":"R-META","status":"open","resolved_at":null,"resolution_note":null}`)},
		},
		SourceRows: map[string][]json.RawMessage{
			"reservations":          {raw(`{"user_id":7,"release_id":11}`), raw(`{"user_id":8,"release_id":11}`)},
			"watch_later":           {raw(`{"user_id":7,"release_id":11,"position":2}`), raw(`{"user_id":8,"release_id":11,"position":3}`)},
			"notification_receipts": {raw(`{"user_id":7,"release_id":11,"reason":"reservation"}`), raw(`{"user_id":9,"release_id":11,"reason":"follow"}`)},
			"playback_sources":      {raw(`{"id":2,"release_id":11}`), raw(`{"id":3,"release_id":11}`)},
			"audit_issues": {
				raw(`{"id":60,"entity_id":11,"rule":"R-META","status":"open","resolved_at":null,"resolution_note":null}`),
				raw(`{"id":61,"entity_id":11,"rule":"R-AGE","status":"open","resolved_at":null,"resolution_note":null}`),
			},
		},
	}
	by := planByTable(t, in)
	for table, want := range map[string]struct {
		move  []string
		drop  int
		patch int
	}{
		"reservations":          {[]string{`[8]`}, 1, 0},
		"watch_later":           {[]string{}, 2, 1},
		"notification_receipts": {[]string{`[9]`}, 1, 0},
		"playback_sources":      {[]string{`[2]`, `[3]`}, 0, 0},
		"audit_issues":          {[]string{`[61]`}, 0, 1},
	} {
		tp := by[table]
		if got := keys(tp.Move); !reflect.DeepEqual(got, want.move) || len(tp.Drop) != want.drop || len(tp.Patch) != want.patch {
			t.Errorf("%s: move %v drop %d patch %d, want %v %d %d", table, got, len(tp.Drop), len(tp.Patch), want.move, want.drop, want.patch)
		}
	}
	// User 7 keeps the lower position; user 8's target position is already lower.
	checkPatch(t, by["watch_later"].Patch[0], PatchTarget, `[7]`, `{"position":2}`, `{"position":5}`)
	// The duplicate open issue stays on the merged release, closed as fixed.
	checkPatch(t, by["audit_issues"].Patch[0], PatchSource, `[60]`,
		`{"resolution_note":"merged into release 10, which has the same open issue","resolved_at":"2026-10-15T01:02:03Z","status":"fixed"}`,
		`{"resolution_note":null,"resolved_at":null,"status":"open"}`)
	if cols, err := PatchColumns(by["audit_issues"].Patch[0].Set); err != nil || !reflect.DeepEqual(cols, []string{"resolution_note", "resolved_at", "status"}) {
		t.Errorf("PatchColumns = %v %v", cols, err)
	}
}

// Every table with a column pointing at a mergeable entity must be listed,
// or its rows would be stranded on the hidden merged entity.
func TestMergeTablesCoverUserTables(t *testing.T) {
	has := func(specs []TableSpec, table, fk string) bool {
		for _, s := range specs {
			if s.Table == table && s.FK == fk {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ table, fk string }{{"follows", "performer_id"}, {"audit_issues", "entity_id"}} {
		if !has(PerformerTables, c.table, c.fk) {
			t.Errorf("PerformerTables misses %s.%s", c.table, c.fk)
		}
	}
	for _, c := range []struct{ table, fk string }{{"reservations", "release_id"}, {"watch_later", "release_id"}, {"playlist_items", "release_id"}, {"notification_receipts", "release_id"}, {"playback_sources", "release_id"}, {"audit_issues", "entity_id"}} {
		if !has(ReleaseTables, c.table, c.fk) {
			t.Errorf("ReleaseTables misses %s.%s", c.table, c.fk)
		}
	}
}
