package identity

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// firstPolicy is a toy field policy: the evidence with the lowest source
// record id wins; any disagreement is a conflict.
func firstPolicy(_ string, ev []FieldEvidence) FieldOutcome {
	var best *FieldEvidence
	for i := range ev {
		if string(ev[i].Value) == "null" || string(ev[i].Value) == `""` {
			continue
		}
		if best == nil || ev[i].SourceRecordID < best.SourceRecordID {
			best = &ev[i]
		}
	}
	if best == nil {
		return FieldOutcome{}
	}
	out := FieldOutcome{Value: best.Value, SourceRecordID: best.SourceRecordID, OK: true}
	for _, e := range ev {
		if string(e.Value) != "null" && string(e.Value) != string(best.Value) {
			out.Conflict = true
		}
	}
	return out
}

func rows(js ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(js))
	for i, j := range js {
		out[i] = json.RawMessage(j)
	}
	return out
}

func rawKeys(raw []json.RawMessage) []string {
	out := make([]string, len(raw))
	for i, r := range raw {
		out[i] = string(r)
	}
	sort.Strings(out)
	return out
}

func table(t *testing.T, p SplitPlan, name string) SplitTable {
	t.Helper()
	for _, tp := range p.Tables {
		if tp.Table == name {
			return tp
		}
	}
	return SplitTable{}
}

func field(t *testing.T, p SplitPlan, orig bool, col string) FieldResult {
	t.Helper()
	f, ok := p.Field(orig, col)
	if !ok {
		t.Fatalf("no field %s (orig=%v)", col, orig)
	}
	return f
}

// performerSplitInput: performer 1 "Airi" mixes two people. Records 10 (src
// 1) and 11 (src 2) are performer records; 20-23 are release records
// crediting her: 20 -> release 500, 21 and 22 -> release 501, 23 -> 502.
// The second person is source 2's: records 11, 21 and 23 move.
func performerSplitInput() SplitInput {
	return SplitInput{
		EntityType: PerformerType, OriginalID: 1,
		Original: json.RawMessage(`{"id":1,"slug":"airi","canonical_name":"Airi","canonical_name_ja":"あいり","birth_date":"1990-01-01","height_cm":158.00,"birth_place":"Tokyo","cup_size":"C"}`),
		Records: []SplitRecord{
			{ID: 10, SourceID: 1, Kind: RecordOwned, Keys: []string{AliasKey("airi"), ExternalIDKey("dmm_actress", "100")}},
			{ID: 11, SourceID: 2, Kind: RecordOwned, Keys: []string{AliasKey("airi"), AliasKey("airi s"), ExternalIDKey("dmm_actress", "200")}},
			{ID: 20, SourceID: 1, Kind: RecordCredit, CreditedName: "Airi"},
			{ID: 21, SourceID: 2, Kind: RecordCredit, CreditedName: "Airi S"},
			{ID: 22, SourceID: 1, Kind: RecordCredit, CreditedName: "Airi"},
			{ID: 23, SourceID: 2, Kind: RecordCredit, CreditedName: "Airi S"},
		},
		Selected: []int64{11, 21, 23},
		Relations: map[string][]json.RawMessage{
			"source_records": rows(`{"id":10,"source_id":1}`, `{"id":11,"source_id":2}`),
			"release_performer_sources": rows(
				`{"release_id":500,"performer_id":1,"source_record_id":20}`,
				`{"release_id":501,"performer_id":1,"source_record_id":21}`,
				`{"release_id":501,"performer_id":1,"source_record_id":22}`,
				`{"release_id":502,"performer_id":1,"source_record_id":23}`),
			"release_performers": rows(
				`{"release_id":500,"performer_id":1,"billing_order":0}`,
				`{"release_id":501,"performer_id":1,"billing_order":1}`,
				`{"release_id":502,"performer_id":1,"billing_order":0}`,
				`{"release_id":503,"performer_id":1,"billing_order":0}`),
			"performer_aliases": rows(
				`{"normalized_name":"airi","source_id":1}`,
				`{"normalized_name":"airi s","source_id":2}`,
				`{"normalized_name":"merged name","source_id":null}`,
				`{"normalized_name":"src2 only","source_id":2}`),
			"performer_external_ids": rows(
				`{"namespace":"dmm_actress","external_id":"100","source_id":1}`,
				`{"namespace":"dmm_actress","external_id":"200","source_id":2}`),
			"performer_source_metrics": rows(`{"source_id":2,"captured_at":"2026-01-01T00:00:00Z"}`, `{"source_id":1,"captured_at":"2026-01-01T00:00:00Z"}`),
		},
		Overridden: map[string]bool{"birth_place": true},
		Columns: []ColumnSpec{{"canonical_name", true}, {"canonical_name_ja", false}, {"birth_date", false},
			{"height_cm", false}, {"birth_place", false}, {"cup_size", false}},
		Evidence: []FieldEvidence{
			{Column: "canonical_name", SourceRecordID: 10, Value: json.RawMessage(`"Airi"`)},
			{Column: "canonical_name", SourceRecordID: 11, Value: json.RawMessage(`"Airi S"`)},
			{Column: "birth_date", SourceRecordID: 11, Value: json.RawMessage(`"1990-01-01"`)},
			{Column: "height_cm", SourceRecordID: 10, Value: json.RawMessage(`"160"`)},
			{Column: "height_cm", SourceRecordID: 11, Value: json.RawMessage(`"158"`)},
			{Column: "birth_place", SourceRecordID: 11, Value: json.RawMessage(`"Osaka"`)},
			{Column: "cup_size", SourceRecordID: 11, Value: json.RawMessage(`"D"`)},
		},
		Policy:    firstPolicy,
		UserMoves: map[string][]int64{"follows": {7}},
		UserRows:  map[string][]json.RawMessage{"follows": rows(`{"user_id":7,"performer_id":1}`)},
	}
}

func TestBuildSplitPlanPerformer(t *testing.T) {
	p, err := BuildSplitPlan(performerSplitInput())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.MovedRecords, []int64{11, 21, 23}) {
		t.Errorf("moved records = %v", p.MovedRecords)
	}
	check := func(tbl string, wantMove, wantCopy []string) {
		t.Helper()
		tp := table(t, p, tbl)
		if got := rawKeys(tp.Move); !reflect.DeepEqual(got, append([]string{}, wantMove...)) {
			t.Errorf("%s move = %v, want %v", tbl, got, wantMove)
		}
		if got := rawKeys(tp.Copy); !reflect.DeepEqual(got, append([]string{}, wantCopy...)) {
			t.Errorf("%s copy = %v, want %v", tbl, got, wantCopy)
		}
	}
	check("source_records", []string{"[11]"}, nil) // credits are not owned records
	check("release_performer_sources", []string{"[501,21]", "[502,23]"}, nil)
	// 502: evidence only from moved records -> moves; 501: shared -> copy;
	// 500: kept evidence only; 503: manual credit without evidence -> stays.
	check("release_performers", []string{"[502]"}, []string{`{"release_id":501,"performer_id":1,"billing_order":1}`})
	// "airi" is evidenced by both sides; "airi s" only by moved; the merged
	// alias has no evidence and no source; "src2 only" falls back to its
	// source, and source 2 has only moved records.
	check("performer_aliases", []string{`["airi s"]`, `["src2 only"]`}, nil)
	check("performer_external_ids", []string{`["dmm_actress","200"]`}, nil)
	check("performer_source_metrics", []string{`[2,"2026-01-01T00:00:00Z"]`}, nil)
	check("follows", []string{"[7]"}, nil)
	if !table(t, p, "follows").UserData {
		t.Error("follows not marked as user data")
	}

	// Canonical fields: the original keeps its override, recomputes from
	// kept evidence, clears values that came only from moved evidence and
	// keeps values nobody evidences.
	want := map[string][2]string{ // column -> origin, value
		"canonical_name":    {OriginEvidence, `"Airi"`},
		"canonical_name_ja": {OriginKept, `"あいり"`},
		"birth_date":        {OriginCleared, `null`},
		"height_cm":         {OriginEvidence, `"160"`},
		"birth_place":       {OriginOverride, `"Tokyo"`},
		"cup_size":          {OriginKept, `"C"`}, // moved value "D" differs: not ours to clear
	}
	for col, w := range want {
		f := field(t, p, true, col)
		if f.Origin != w[0] || string(f.Value) != w[1] {
			t.Errorf("original %s = %s %s, want %s %s", col, f.Origin, f.Value, w[0], w[1])
		}
	}
	wantNew := map[string][2]string{
		"canonical_name":    {OriginEvidence, `"Airi S"`},
		"canonical_name_ja": {OriginEmpty, `null`},
		"birth_date":        {OriginEvidence, `"1990-01-01"`},
		"height_cm":         {OriginEvidence, `"158"`},
		"birth_place":       {OriginEvidence, `"Osaka"`}, // no override on the new entity
		"cup_size":          {OriginEvidence, `"D"`},
	}
	for col, w := range wantNew {
		f := field(t, p, false, col)
		if f.Origin != w[0] || string(f.Value) != w[1] {
			t.Errorf("new %s = %s %s, want %s %s", col, f.Origin, f.Value, w[0], w[1])
		}
	}
}

func TestBuildSplitPlanUserDataStays(t *testing.T) {
	in := performerSplitInput()
	in.UserMoves, in.UserRows = nil, nil
	p, err := BuildSplitPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range p.Tables {
		if tp.UserData || tp.Table == "follows" {
			t.Errorf("user data table %s in plan without an explicit selection", tp.Table)
		}
	}
	// A follow the original does not have cannot be moved.
	in.UserMoves = map[string][]int64{"follows": {8}}
	if _, err = BuildSplitPlan(in); !isSplitError(err) {
		t.Errorf("unknown follow: %v", err)
	}
	in.UserMoves = map[string][]int64{"favorites": {7}}
	if _, err = BuildSplitPlan(in); !isSplitError(err) {
		t.Errorf("favorites of a performer: %v", err)
	}
}

func TestBuildSplitPlanCreditFallbackName(t *testing.T) {
	in := performerSplitInput()
	in.Selected = []int64{21} // a credit only: no performer record moves
	p, err := BuildSplitPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	if f := field(t, p, false, "canonical_name"); f.Origin != OriginFallback || string(f.Value) != `"Airi S"` {
		t.Errorf("new name = %+v", f)
	}
	if len(table(t, p, "source_records").Move) != 0 {
		t.Error("a credit moved its release record")
	}
}

func TestBuildSplitPlanLockedRelease(t *testing.T) {
	in := performerSplitInput()
	in.LockedReleases = map[int64]bool{502: true}
	p, err := BuildSplitPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	tp := table(t, p, "release_performers")
	for _, k := range append(rawKeys(tp.Move), rawKeys(tp.Copy)...) {
		if strings.Contains(k, "502") {
			t.Errorf("credit on release 502 (performers override) changed: %s", k)
		}
	}
	// Evidence still moves.
	if got := rawKeys(table(t, p, "release_performer_sources").Move); !reflect.DeepEqual(got, []string{"[501,21]", "[502,23]"}) {
		t.Errorf("rps move = %v", got)
	}
}

func isSplitError(err error) bool {
	var se *SplitError
	return errors.As(err, &se)
}

func TestBuildSplitPlanSelection(t *testing.T) {
	in := performerSplitInput()
	for _, sel := range [][]int64{nil, {99}, {10, 11, 20, 21, 22, 23}} {
		in.Selected = sel
		if _, err := BuildSplitPlan(in); !isSplitError(err) {
			t.Errorf("selection %v: err = %v, want SplitError", sel, err)
		}
	}
	in = performerSplitInput()
	in.EntityType = "maker"
	if _, err := BuildSplitPlan(in); err == nil {
		t.Error("unknown entity type accepted")
	}
}

func TestBuildSplitPlanRelease(t *testing.T) {
	in := SplitInput{
		EntityType: ReleaseType, OriginalID: 5,
		Original: json.RawMessage(`{"id":5,"slug":"abc-123","canonical_title":"Right Title","maker_name":"M","runtime_minutes":120,"release_date":"2026-01-02"}`),
		Records: []SplitRecord{
			{ID: 30, SourceID: 1, Kind: RecordOwned, Keys: []string{IdentifierKey("dvd_code", "abc-123"), IdentifierKey("content_id", "abc00123")}},
			{ID: 31, SourceID: 3, Kind: RecordOwned, Keys: []string{IdentifierKey("dvd_code", "abc-123"), IdentifierKey("content_id", "zzz00123")}},
		},
		Selected: []int64{31},
		Relations: map[string][]json.RawMessage{
			"source_records": rows(`{"id":30}`, `{"id":31}`),
			"release_identifiers": rows(
				`{"id":1,"identifier_type":"dvd_code","normalized_value":"abc-123","source_id":1}`,
				`{"id":2,"identifier_type":"content_id","normalized_value":"abc00123","source_id":1}`,
				`{"id":3,"identifier_type":"content_id","normalized_value":"zzz00123","source_id":3}`,
				`{"id":4,"identifier_type":"isbn","normalized_value":"manual","source_id":null}`),
			"release_performer_sources": rows(
				`{"release_id":5,"performer_id":70,"source_record_id":30}`,
				`{"release_id":5,"performer_id":71,"source_record_id":31}`,
				`{"release_id":5,"performer_id":72,"source_record_id":30}`,
				`{"release_id":5,"performer_id":72,"source_record_id":31}`),
			"release_performers": rows(`{"release_id":5,"performer_id":70}`, `{"release_id":5,"performer_id":71}`, `{"release_id":5,"performer_id":72}`),
			"release_tag_sources": rows(
				`{"release_id":5,"normalized_tag":"drama","source_record_id":30}`,
				`{"release_id":5,"normalized_tag":"vr","source_record_id":31}`),
			"release_tags":          rows(`{"release_id":5,"normalized_tag":"drama","tag":"Drama"}`, `{"release_id":5,"normalized_tag":"vr","tag":"VR"}`),
			"release_media_sources": rows(`{"release_id":5,"source":"javtiful","media_id":"m1"}`),
		},
		Overridden: map[string]bool{"tags": true, "canonical_title": true},
		Columns:    []ColumnSpec{{"canonical_title", true}, {"maker_name", false}, {"runtime_minutes", false}, {"release_date", false}},
		Evidence: []FieldEvidence{
			{Column: "canonical_title", SourceRecordID: 30, Value: json.RawMessage(`"Wrong Title"`)},
			{Column: "canonical_title", SourceRecordID: 31, Value: json.RawMessage(`"Other Title"`)},
			{Column: "runtime_minutes", SourceRecordID: 31, Value: json.RawMessage(`"120"`)},
			{Column: "maker_name", SourceRecordID: 30, Value: json.RawMessage(`"M"`)},
			{Column: "maker_name", SourceRecordID: 31, Value: json.RawMessage(`"N"`)},
		},
		Policy:     firstPolicy,
		MediaLinks: []string{"Javtiful"},
		UserMoves:  map[string][]int64{"reviews": {9}},
		UserRows:   map[string][]json.RawMessage{"reviews": rows(`{"id":9,"release_id":5}`)},
	}
	p, err := BuildSplitPlan(in)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2][]string{
		"source_records":            {{"[31]"}, nil},
		"release_identifiers":       {{"[3]"}, nil}, // the shared dvd_code stays
		"release_performer_sources": {{"[71,31]", "[72,31]"}, nil},
		"release_performers":        {{"[71]"}, {`{"release_id":5,"performer_id":72}`}},
		"release_tag_sources":       {{`["vr",31]`}, nil},
		// tags are an override on the original: copied, never taken away.
		"release_tags":          {nil, {`{"release_id":5,"normalized_tag":"vr","tag":"VR"}`}},
		"release_media_sources": {{`["javtiful"]`}, nil},
		"reviews":               {{"[9]"}, nil},
	}
	for tbl, w := range want {
		tp := table(t, p, tbl)
		if got := rawKeys(tp.Move); !reflect.DeepEqual(got, append([]string{}, w[0]...)) {
			t.Errorf("%s move = %v, want %v", tbl, got, w[0])
		}
		if got := rawKeys(tp.Copy); !reflect.DeepEqual(got, append([]string{}, w[1]...)) {
			t.Errorf("%s copy = %v, want %v", tbl, got, w[1])
		}
	}
	if f := field(t, p, true, "canonical_title"); f.Origin != OriginOverride || string(f.Value) != `"Right Title"` {
		t.Errorf("original title = %+v", f)
	}
	if f := field(t, p, true, "runtime_minutes"); f.Origin != OriginCleared {
		t.Errorf("original runtime = %+v (120 came only from the moved record)", f)
	}
	if f := field(t, p, true, "release_date"); f.Origin != OriginKept || string(f.Value) != `"2026-01-02"` {
		t.Errorf("original release_date = %+v", f)
	}
	if f := field(t, p, false, "canonical_title"); f.Origin != OriginEvidence || string(f.Value) != `"Other Title"` {
		t.Errorf("new title = %+v", f)
	}
	if f := field(t, p, false, "maker_name"); string(f.Value) != `"N"` {
		t.Errorf("new maker = %+v", f)
	}
	in.MediaLinks = []string{"other"}
	if _, err = BuildSplitPlan(in); !isSplitError(err) {
		t.Errorf("unknown media link: %v", err)
	}
}

func TestSameValue(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{`"158"`, `158.00`, true}, {`"a"`, `"a "`, true}, {`null`, `null`, true},
		{`null`, `""`, true}, {`"a"`, `null`, false}, {`"1990-01-01"`, `"1990-01-02"`, false},
	} {
		if got := SameValue(json.RawMessage(c.a), json.RawMessage(c.b)); got != c.want {
			t.Errorf("SameValue(%s,%s) = %v", c.a, c.b, got)
		}
	}
}
