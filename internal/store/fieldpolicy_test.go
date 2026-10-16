package store

import (
	"encoding/json"
	"testing"
)

func cand(src int64, kind, value string, policy ...int) fieldCandidate {
	c := fieldCandidate{SourceID: src, SourceRecordID: src * 10, SourceKind: kind, SourceRank: 100, Value: json.RawMessage(value)}
	if len(policy) > 0 {
		c.Policy = &policy[0]
	}
	return c
}

func TestDecideFieldTiers(t *testing.T) {
	d := decideField("release", "title", []fieldCandidate{
		cand(1, "aggregator", `"Guru Title"`),
		cand(2, "official_catalog", `"Official Title"`),
		cand(3, "structured_database", `"DB Title"`),
	})
	if !d.ok || d.Winner.SourceID != 2 || !d.Conflict {
		t.Fatalf("winner %+v conflict %v", d.Winner, d.Conflict)
	}
	if d.Ranked[0].SourceID != 2 || d.Ranked[1].SourceID != 3 || d.Ranked[2].SourceID != 1 {
		t.Fatalf("order %+v", d.Ranked)
	}
}

func TestDecideFieldPolicyOverridesTier(t *testing.T) {
	// A policy row lets the aggregator win this one field over the database.
	d := decideField("performer", "name_ja", []fieldCandidate{
		cand(1, "structured_database", `"波多野結衣"`),
		cand(2, "aggregator", `"波多野 結衣"`, 5),
	})
	if d.Winner.SourceID != 2 || d.Winner.Priority != 5 {
		t.Fatalf("winner %+v", d.Winner)
	}
}

func TestDecideFieldAgreementNoConflict(t *testing.T) {
	// Lower-priority sources that agree (after normalization) or have no
	// value open no conflict.
	d := decideField("performer", "height_cm", []fieldCandidate{
		cand(1, "official_maker", `"158"`),
		cand(2, "aggregator", `158`),
		cand(3, "aggregator", `""`),
		cand(4, "aggregator", `null`),
	})
	if !d.ok || d.Conflict || d.Winner.SourceID != 1 || len(d.Ranked) != 2 {
		t.Fatalf("decision %+v", d)
	}
	d = decideField("release", "dvd_code", []fieldCandidate{cand(1, "aggregator", `"JUQ-743"`), cand(2, "aggregator", `"juq-743"`)})
	if d.Conflict {
		t.Fatal("case-only difference is not a conflict")
	}
}

func TestDecideFieldSkipsUnusableWinner(t *testing.T) {
	// The best source's date does not parse: the column takes the next
	// usable value, and the disagreement is still a conflict.
	d := decideField("release", "release_date", []fieldCandidate{
		cand(1, "official_catalog", `"2024/05/24"`),
		cand(2, "structured_database", `"2024-05-24"`),
	})
	if d.Winner.SourceID != 2 || !d.Conflict {
		t.Fatalf("decision %+v", d)
	}
}

func TestDecideFieldTieBreak(t *testing.T) {
	a := cand(7, "aggregator", `"A"`)
	b := cand(3, "aggregator", `"B"`)
	if d := decideField("release", "title", []fieldCandidate{a, b}); d.Winner.SourceID != 3 {
		t.Fatalf("equal priority: lower source id wins, got %d", d.Winner.SourceID)
	}
	a.SourceRank = 50 // sources.priority breaks ties before the id
	if d := decideField("release", "title", []fieldCandidate{a, b}); d.Winner.SourceID != 7 {
		t.Fatalf("sources.priority tie-break, got %d", d.Winner.SourceID)
	}
}

func TestDecideFieldLists(t *testing.T) {
	d := decideField("release", "tags", []fieldCandidate{cand(1, "aggregator", `["a"]`), cand(2, "aggregator", `["b"]`)})
	if d.Conflict {
		t.Fatal("list fields are unions, never conflicts")
	}
	if d = decideField("release", "tags", []fieldCandidate{cand(1, "aggregator", `[]`)}); d.ok {
		t.Fatal("empty list is not a value")
	}
}

func TestFieldOverriddenSkipsPolicy(t *testing.T) {
	// applyFieldPolicy skips every field fieldOverridden reports: the admin's
	// value is never recomputed and opens no conflict.
	ov := map[string]bool{"canonical_title": true, "tags": true}
	for _, f := range []string{"title", "canonical_title", "tags", "genres"} {
		if !fieldOverridden("release", f, ov) {
			t.Errorf("%s should be covered by an override", f)
		}
	}
	if fieldOverridden("release", "maker", ov) {
		t.Error("maker has no override")
	}
}

func TestTierPriorityAndPolicyField(t *testing.T) {
	if TierPriority("official_catalog") >= TierPriority("official_maker") ||
		TierPriority("official_maker") >= TierPriority("structured_database") ||
		TierPriority("structured_database") >= TierPriority("aggregator") ||
		TierPriority("aggregator") >= TierPriority("third_party_api") ||
		TierPriority("third_party_api") >= TierPriority("fixture") {
		t.Fatal("tier order must follow FINAL_DATA_DESIGN 2.4")
	}
	for in, want := range map[string]string{"title": "canonical_title", "maker": "maker_name", "dvd_code": "dvd_code"} {
		if got, err := normalizePolicyField("release", in); err != nil || got != want {
			t.Errorf("%s -> %s %v", in, got, err)
		}
	}
	if got, _ := normalizePolicyField("performer", "name"); got != "canonical_name" {
		t.Errorf("performer name -> %s", got)
	}
	for _, bad := range [][2]string{{"studio", "title"}, {"release", "Title; drop"}, {"release", ""}} {
		if _, err := normalizePolicyField(bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
