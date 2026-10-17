package recs

import (
	"cmp"
	"math"
	"slices"
)

// StoredSimilar is one release_similar row as stored (score is a real).
type StoredSimilar struct {
	SimilarID int64
	Reason    string
	Score     float32
}

// SimilarDiff plans the nightly release_similar write one release at a
// time: which rows to insert, update (with the score to store) and delete
// so that the table is equivalent to a full rewrite of the new rows.
//
// Equivalent means, for every release:
//   - the same (reason, similar_id) pairs;
//   - every stored score within Eps of the new one (as a real);
//   - the same order: whenever a new score is strictly bigger than another,
//     its stored score is strictly bigger too, so ORDER BY score DESC (the
//     recall queries) returns the same rows in the same order, ties aside.
//
// A stored score is kept when it satisfies both. Otherwise the row gets its
// new score or, when that would break the order against the row before it
// (kept a little low), the next real below that row's, which is always
// within Eps of its own new score except at float rounding edges; there the
// whole release is written exactly. With Eps 0 only identical scores are
// kept, so the table equals a full rewrite.
type SimilarDiff struct {
	Eps float64
	// Upsert holds the rows to insert or update, Score set to the value to
	// store; Delete the rows to delete (Score unused).
	Upsert []ReleaseSimilarRow
	Delete []ReleaseSimilarRow
	Write  ReleaseSimilarWrite
	// UpsertSrc[k] is the index of Upsert[k] in the next slice of the
	// Release call that planned it (for callers with a payload to write).
	UpsertSrc []int
	// Changed, when set, reports whether a row present on both sides must
	// be rewritten whatever its score: next[ni] against stored[si] of the
	// current Release call (performer_similar's evidence). A changed row is
	// counted as updated and keeps the score the plan chose.
	Changed func(ni, si int) bool

	idx  map[similarKey]int
	used []bool
	rows []diffRow
}

type similarKey struct {
	id     int64
	reason string
}

type diffRow struct {
	similarKey
	next, old, final float32
	hasOld           bool
	ni, si           int
}

// Release plans one release: next are its new rows (any order, all with
// this ReleaseID), stored its current rows. Either may be empty.
func (d *SimilarDiff) Release(releaseID int64, next []ReleaseSimilarRow, stored []StoredSimilar) {
	if d.idx == nil {
		d.idx = map[similarKey]int{}
	}
	clear(d.idx)
	for i, s := range stored {
		d.idx[similarKey{s.SimilarID, s.Reason}] = i
	}
	d.used = slices.Grow(d.used[:0], len(stored))[:len(stored)]
	clear(d.used)
	d.rows = d.rows[:0]
	for ni, r := range next {
		x := diffRow{similarKey: similarKey{r.SimilarID, r.Reason}, next: float32(r.Score), ni: ni}
		if i, ok := d.idx[x.similarKey]; ok {
			x.old, x.hasOld, d.used[i], x.si = stored[i].Score, true, true, i
		}
		d.rows = append(d.rows, x)
	}
	for i, s := range stored {
		if !d.used[i] {
			d.Delete = append(d.Delete, ReleaseSimilarRow{ReleaseID: releaseID, SimilarID: s.SimilarID, Score: float64(s.Score), Reason: s.Reason})
			d.Write.Deleted++
		}
	}
	slices.SortFunc(d.rows, func(a, b diffRow) int {
		if c := cmp.Compare(b.next, a.next); c != 0 {
			return c
		}
		if c := cmp.Compare(a.reason, b.reason); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	exact := false
	for i := range d.rows {
		r := &d.rows[i]
		fits := func(v float32) bool {
			if v < 0 || math.Abs(float64(v)-float64(r.next)) > d.Eps {
				return false
			}
			if i == 0 {
				return true
			}
			p := d.rows[i-1]
			if r.next < p.next {
				return v < p.final
			}
			return v <= p.final
		}
		switch {
		case r.hasOld && fits(r.old):
			r.final = r.old
		case fits(r.next):
			r.final = r.next
		case i > 0 && fits(math.Nextafter32(d.rows[i-1].final, float32(math.Inf(-1)))):
			r.final = math.Nextafter32(d.rows[i-1].final, float32(math.Inf(-1)))
		default:
			exact = true
		}
		if exact {
			break
		}
	}
	if exact {
		d.Write.Exact++
	}
	for _, r := range d.rows {
		if exact {
			r.final = r.next
		}
		switch {
		case !r.hasOld:
			d.Write.Inserted++
		case r.old != r.final, d.Changed != nil && d.Changed(r.ni, r.si):
			d.Write.Updated++
		default:
			continue
		}
		d.Upsert = append(d.Upsert, ReleaseSimilarRow{ReleaseID: releaseID, SimilarID: r.id, Score: float64(r.final), Reason: r.reason})
		d.UpsertSrc = append(d.UpsertSrc, r.ni)
	}
}
