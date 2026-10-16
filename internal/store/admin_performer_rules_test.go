package store

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPublicBirthDateShaping(t *testing.T) {
	if got := PublicBirthDate("p"); !strings.HasPrefix(got, "CASE WHEN (p.birth_date_hidden OR EXISTS") || !strings.HasSuffix(got, "THEN NULL ELSE p.birth_date END") {
		t.Fatalf("PublicBirthDate = %q", got)
	}
	// The public performer list never selects the raw column.
	sql, _, _, err := buildPerformerQuery(PerformerFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, PublicBirthDate("p")) || strings.Contains(sql, "''),p.birth_date,") {
		t.Fatalf("performer list SQL does not hide birthdays: %s", sql)
	}
	birth := time.Date(2000, 5, 1, 0, 0, 0, 0, time.UTC)
	rel := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if PublicFilmingAge(&birth, true, 0, &rel) != nil {
		t.Fatal("hidden birthday must not produce an age at filming")
	}
	if a := PublicFilmingAge(&birth, false, 0, &rel); a == nil || a.Age != 24 || !a.Approx {
		t.Fatalf("visible age = %+v", a)
	}
	// R-AGE reads the real birth date.
	if !strings.Contains(releaseFactsSelect, "'birth_date',p.birth_date") || strings.Contains(releaseFactsSelect, "birth_date_hidden") {
		t.Fatal("releaseFactsSelect must use the real birth_date")
	}
}

func TestParsePerformerPatch(t *testing.T) {
	body := func(s string) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	p, err := ParsePerformerPatch(body(`{"name":"Sena","birth_date":"1999-02-03","height_cm":158,"birth_date_hidden":true,"version":2,"reason":"agency"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Columns) != 3 || !p.TouchesBirthDate() || p.BirthDateHidden == nil || !*p.BirthDateHidden || p.Version != 2 {
		t.Fatalf("patch %+v", p)
	}
	if p2, _ := ParsePerformerPatch(body(`{"birth_date_hidden":false}`)); p2.TouchesBirthDate() || p2.Empty() {
		t.Fatalf("hidden-only patch %+v", p2)
	}
	for _, bad := range []string{
		`{}`, `{"version":3}`, `{"status":"hidden"}`, `{"name":null}`, `{"birth_date":"1999/02/03"}`,
		`{"birth_date":"1850-01-01"}`, `{"birth_date":"2999-01-01"}`, `{"name":"A","canonical_name":"B"}`,
		`{"birth_date_hidden":"yes"}`, `{"height_cm":"tall"}`, `{"version":-1,"name":"A"}`,
	} {
		var ve *ValidationError
		if _, err := ParsePerformerPatch(body(bad)); !errors.As(err, &ve) {
			t.Errorf("%s: err %v, want ValidationError", bad, err)
		}
	}
}

func TestCheckPerformerStatus(t *testing.T) {
	var ve *ValidationError
	var ce *ConflictError
	if err := CheckPerformerStatus("active", PerformerStatusChange{To: "hidden", Reason: "junk name"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckPerformerStatus("hidden", PerformerStatusChange{To: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckPerformerStatus("active", PerformerStatusChange{To: "hidden"}); !errors.As(err, &ve) {
		t.Errorf("hide without reason: %v", err)
	}
	if err := CheckPerformerStatus("active", PerformerStatusChange{To: "deleted", Reason: "x"}); !errors.As(err, &ve) {
		t.Errorf("unknown status: %v", err)
	}
	if err := CheckPerformerStatus("hidden", PerformerStatusChange{To: "hidden", Reason: "x"}); !errors.As(err, &ce) {
		t.Errorf("no-op: %v", err)
	}
}

// The request state machine: open → approved | rejected, both final.
func TestCheckRequestTransition(t *testing.T) {
	var ve *ValidationError
	var ce *ConflictError
	cases := []struct {
		from, to string
		d        RequestDecision
		ok       bool
		conflict bool
	}{
		{RequestOpen, RequestApproved, RequestDecision{}, true, false},
		{RequestOpen, RequestApproved, RequestDecision{Value: json.RawMessage(`"1999-01-01"`)}, true, false},
		{RequestOpen, RequestRejected, RequestDecision{Note: "no evidence"}, true, false},
		{RequestOpen, RequestRejected, RequestDecision{}, false, false},
		{RequestOpen, RequestRejected, RequestDecision{Note: "n", Value: json.RawMessage(`1`)}, false, false},
		{RequestOpen, RequestOpen, RequestDecision{}, false, false},
		{RequestApproved, RequestApproved, RequestDecision{}, false, true},
		{RequestApproved, RequestRejected, RequestDecision{Note: "n"}, false, true},
		{RequestRejected, RequestApproved, RequestDecision{}, false, true},
	}
	for _, c := range cases {
		err := CheckRequestTransition(c.from, c.to, c.d)
		switch {
		case c.ok && err != nil:
			t.Errorf("%s→%s: %v", c.from, c.to, err)
		case !c.ok && c.conflict && !errors.As(err, &ce):
			t.Errorf("%s→%s: err %v, want ConflictError", c.from, c.to, err)
		case !c.ok && !c.conflict && !errors.As(err, &ve):
			t.Errorf("%s→%s: err %v, want ValidationError", c.from, c.to, err)
		}
	}
}

func TestPerformerRequestInput(t *testing.T) {
	in := PerformerRequestInput{Performer: " sena ", Kind: RequestCorrectField, Field: "birth_date", Value: json.RawMessage(`"1999-01-02"`),
		Contact: "agent@agency.example", Role: "agency", Message: "per agency profile", Links: []string{" https://agency.example/sena "}}
	if err := in.Normalize(); err != nil {
		t.Fatal(err)
	}
	if in.column != "birth_date" || in.value == nil || *in.value != "1999-01-02" || in.Links[0] != "https://agency.example/sena" {
		t.Fatalf("normalized %+v", in)
	}
	ev := in.evidence()
	if ev["role"] != "agency" || ev["message"] != "per agency profile" {
		t.Fatalf("evidence %+v", ev)
	}
	if _, ok := ev["contact"]; ok {
		t.Fatal("contact belongs in requester_contact, not evidence")
	}
	minimal := PerformerRequestInput{Performer: "x", Kind: RequestHideBirthDate, Contact: "+81 90 0000 0000"}
	if err := minimal.Normalize(); err != nil || minimal.Role != "other" {
		t.Fatalf("minimal: %v %+v", err, minimal)
	}
	if RequestSLA(RequestHideBirthDate) != 72*time.Hour || RequestSLA(RequestCorrectField) != 7*24*time.Hour {
		t.Fatal("SLA")
	}
}

func TestCompletenessAndFilters(t *testing.T) {
	score, missing := Completeness(map[string]bool{"canonical_name_ja": true, "birth_date": true, "birth_place": true})
	if score != 50 || strings.Join(missing, ",") != "debut_date,height_cm,photo" {
		t.Fatalf("completeness %d %v", score, missing)
	}
	if s, m := Completeness(map[string]bool{"canonical_name_ja": true, "birth_date": true, "birth_place": true, "debut_date": true, "height_cm": true, "photo": true}); s != 100 || len(m) != 0 {
		t.Fatalf("complete %d %v", s, m)
	}
	for _, f := range PerformerFilters {
		if !ValidPerformerFilter(f) {
			t.Errorf("filter %q has no SQL", f)
		}
	}
	if ValidPerformerFilter("pretty") {
		t.Error("unknown filter accepted")
	}
	// Photos removed on request no longer count.
	if !strings.Contains(performerFilterSQL[PerformerFilterMissingPhoto], "suppressed_at IS NULL") {
		t.Error("missing_photo must ignore suppressed photos")
	}
}

func TestAliasInput(t *testing.T) {
	a := AliasInput{Name: "  Sena   H. "}
	if err := a.Normalize(); err != nil || a.Name != "Sena H." || a.AliasType != "stage" {
		t.Fatalf("%v %+v", err, a)
	}
	for _, bad := range []AliasInput{{Name: ""}, {Name: "x", AliasType: "merged"}, {Name: strings.Repeat("x", 201)}} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}
