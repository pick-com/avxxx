package recs

import (
	"math"
	"slices"
)

// PerformerSimilarEpsilon is how far a stored performer_similar score, and
// each number in its evidence, may drift from the newly computed one before
// the nightly job rewrites the row (PerformerSimilarDiff). As for
// release_similar, each night's releases and plays move idf and the
// co-watch norms, so most scores move a little every night
// (docs/PERFORMANCE.md, "performer_similar write"). Each performer's
// neighbour order stays exact, ties by similar_id included.
const PerformerSimilarEpsilon = 1e-3

// StoredPerformerSimilar is one performer_similar row as stored.
type StoredPerformerSimilar struct {
	SimilarID int64
	Score     float32
	Evidence  Evidence
	// EvidenceOK is false when the stored evidence did not decode strictly
	// as an Evidence (unknown keys, bad JSON): such a row is always
	// rewritten.
	EvidenceOK bool
}

// SameEvidence is the evidence tolerance rule of the performer_similar diff
// write. Stored evidence a is kept against new evidence b when:
//   - co_star (the shared-work count) is equal;
//   - shared_tags and shared_makers hold the same names in the same order
//     (absent and empty are the same: both are omitted from the JSON);
//   - metadata, co_star_score and co_watch each differ by at most eps (they
//     are rounded to 3 decimals, so 1e-3 is one rounding step);
//   - PerformerReason gives the same text, so no threshold (co_watch 0.1 /
//     0.3, metadata 0.2) lies between the two.
//
// With eps 0 every field must be equal, so the stored JSON is the one a full
// rewrite would write. Within eps, ScoreEvidence (the weights preview) of
// the stored evidence is within eps of the new one under any weights, since
// it is a weighted mean of the three numbers.
func SameEvidence(a, b Evidence, eps float64) bool {
	if a.CoStar != b.CoStar || !slices.Equal(a.SharedTags, b.SharedTags) || !slices.Equal(a.SharedMakers, b.SharedMakers) {
		return false
	}
	if eps == 0 {
		return a.Metadata == b.Metadata && a.CoStarScore == b.CoStarScore && a.CoWatch == b.CoWatch
	}
	// The slack absorbs the binary error of two 3-decimal values one step
	// apart (0.124-0.123 is 0.0010000000000000009).
	near := func(x, y float64) bool { return math.Abs(x-y) <= eps+1e-9 }
	return near(a.Metadata, b.Metadata) && near(a.CoStarScore, b.CoStarScore) && near(a.CoWatch, b.CoWatch) &&
		PerformerReason(a) == PerformerReason(b)
}

// PerformerSimilarDiff plans the nightly performer_similar write one
// performer at a time. It reuses SimilarDiff's score and order rules, with
// similar_id as the tie-break, and adds SameEvidence: the table after the
// write has the same pairs as a full rewrite, every score within Eps, the
// same per-performer order (ORDER BY score DESC, similar_id), and evidence
// SameEvidence accepts. With Eps 0 it is bit-identical to a full rewrite.
type PerformerSimilarDiff struct {
	Eps float64
	// Upsert holds the rows to insert or update, Score set to the value to
	// store and Evidence to the new evidence; Delete the rows to delete.
	Upsert []PerformerMatch
	Delete []PerformerMatch
	Write  ReleaseSimilarWrite

	d      SimilarDiff
	next   []ReleaseSimilarRow
	stored []StoredSimilar
	cur    []PerformerMatch
	curOld []StoredPerformerSimilar
}

// Performer plans one performer: next are its new rows (all with this
// PerformerID, any order), stored its current rows. Either may be empty.
func (p *PerformerSimilarDiff) Performer(id int64, next []PerformerMatch, stored []StoredPerformerSimilar) {
	if p.d.Changed == nil {
		p.d.Changed = func(ni, si int) bool {
			old := p.curOld[si]
			return !old.EvidenceOK || !SameEvidence(old.Evidence, p.cur[ni].Evidence, p.Eps)
		}
	}
	p.d.Eps = p.Eps
	p.cur, p.curOld = next, stored
	p.next = p.next[:0]
	for _, m := range next {
		p.next = append(p.next, ReleaseSimilarRow{ReleaseID: id, SimilarID: m.SimilarID, Score: m.Score})
	}
	p.stored = p.stored[:0]
	for _, s := range stored {
		p.stored = append(p.stored, StoredSimilar{SimilarID: s.SimilarID, Score: s.Score})
	}
	p.d.Upsert, p.d.UpsertSrc, p.d.Delete = p.d.Upsert[:0], p.d.UpsertSrc[:0], p.d.Delete[:0]
	p.d.Write = ReleaseSimilarWrite{}
	p.d.Release(id, p.next, p.stored)
	for k, r := range p.d.Upsert {
		p.Upsert = append(p.Upsert, PerformerMatch{PerformerID: id, SimilarID: r.SimilarID, Score: r.Score, Evidence: next[p.d.UpsertSrc[k]].Evidence})
	}
	for _, r := range p.d.Delete {
		p.Delete = append(p.Delete, PerformerMatch{PerformerID: id, SimilarID: r.SimilarID})
	}
	p.Write.Inserted += p.d.Write.Inserted
	p.Write.Updated += p.d.Write.Updated
	p.Write.Deleted += p.d.Write.Deleted
	p.Write.Exact += p.d.Write.Exact
}
