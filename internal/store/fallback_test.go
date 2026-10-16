package store

import "testing"

// ThePornDB-style fallback evidence (third_party_api, tier 50): it never
// beats a higher tier, fills a field nobody else has, and on its own never
// opens a conflict; it does join a conflict other sources open.
func TestDecideFieldFallbackTier(t *testing.T) {
	d := decideField("release", "maker", []fieldCandidate{
		cand(1, FallbackSourceKind, `"S1 NO.1 STYLE"`),
		cand(2, "official_catalog", `"エスワン ナンバーワンスタイル"`),
	})
	if !d.ok || d.Winner.SourceID != 2 || d.Conflict {
		t.Fatalf("official vs fallback: winner %+v conflict %v", d.Winner, d.Conflict)
	}
	if d.Ranked[1].SourceID != 1 || d.Ranked[1].Priority != 50 {
		t.Fatalf("fallback rank %+v", d.Ranked)
	}

	d = decideField("release", "release_date", []fieldCandidate{
		cand(1, FallbackSourceKind, `"2024-04-23"`),
		cand(2, "official_catalog", `""`),
	})
	if !d.ok || d.Winner.SourceID != 1 || d.Conflict {
		t.Fatalf("missing field not filled by fallback: %+v", d)
	}

	d = decideField("release", "title", []fieldCandidate{
		cand(1, FallbackSourceKind, `"English Title"`),
		cand(2, "official_catalog", `"Official"`),
		cand(3, "structured_database", `"Fan DB"`),
	})
	if !d.Conflict || len(d.Ranked) != 3 || d.Ranked[2].SourceID != 1 {
		t.Fatalf("fallback evidence should join an existing conflict: %+v", d)
	}

	// With an explicit policy row the source is no longer fallback-only.
	d = decideField("release", "maker", []fieldCandidate{
		cand(1, FallbackSourceKind, `"S1"`, 40),
		cand(2, "aggregator", `"Other"`),
	})
	if d.Winner.SourceID != 1 || !d.Conflict {
		t.Fatalf("policy row: %+v", d)
	}
}
