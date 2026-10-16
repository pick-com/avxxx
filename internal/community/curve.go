// Package community holds the database-free rules of the V1 community
// features: the danmaku peak curve (PRD 5.5.1), text normalization and the
// keyword check, rating weights and the Bayesian score (PRD 19), posting rate
// limits and report SLAs (tech plan 3.8). internal/store runs the SQL around
// them; internal/api applies them to requests.
package community

import (
	"math"
	"strconv"
)

// Peak curve constants (PRD 5.5.1).
const (
	CurveBuckets     = 200  // fixed number of buckets per video
	CurveMax         = 255  // normalized curve values are 0..CurveMax
	MinCurveDanmaku  = 50   // fewer valid danmaku: no curve
	MinBucketMS      = 1000 // bucket width is duration/200, at least 1 s
	DefaultVersion   = "main"
	altVersionPrefix = "alt:"
)

// CurvePoint is one danmaku as seen by the curve job. Callers pass only
// visible danmaku of users who were never muted.
type CurvePoint struct {
	TMs    int
	UserID int64
	Body   string
}

// Curve is the result of ComputeCurve. Data is nil when Valid is below
// MinCurveDanmaku (the player then shows no curve).
type Curve struct {
	Data     []int16
	BucketMS int
	Valid    int
}

// BucketMS is the bucket width for a video of durationMS: duration / 200,
// at least MinBucketMS.
func BucketMS(durationMS int) int {
	w := durationMS / CurveBuckets
	if w < MinBucketMS {
		w = MinBucketMS
	}
	return w
}

// BucketOf maps a timestamp to its bucket; anything past the end lands in
// the last bucket.
func BucketOf(tMS, bucketMS int) int {
	if tMS < 0 || bucketMS <= 0 {
		return 0
	}
	b := tMS / bucketMS
	if b >= CurveBuckets {
		b = CurveBuckets - 1
	}
	return b
}

// DedupeCounts counts valid danmaku per bucket with the PRD 5.5.1 anti-spam
// rules: one per user per bucket, and identical or near-identical text
// (same DedupeKey) once per bucket. Points are taken in the given order.
func DedupeCounts(points []CurvePoint, bucketMS int) (counts [CurveBuckets]int, valid int) {
	type userBucket struct {
		user   int64
		bucket int
	}
	type textBucket struct {
		key    string
		bucket int
	}
	users := make(map[userBucket]bool, len(points))
	texts := make(map[textBucket]bool, len(points))
	for _, p := range points {
		b := BucketOf(p.TMs, bucketMS)
		ub := userBucket{p.UserID, b}
		if users[ub] {
			continue
		}
		tb := textBucket{DedupeKey(p.Body), b}
		if texts[tb] {
			continue
		}
		users[ub] = true
		texts[tb] = true
		counts[b]++
		valid++
	}
	return counts, valid
}

// Smooth3 is the 3-bucket moving average; edge buckets average the
// neighbours that exist.
func Smooth3(counts [CurveBuckets]int) [CurveBuckets]float64 {
	var out [CurveBuckets]float64
	for i := range counts {
		sum, n := 0, 0
		for j := i - 1; j <= i+1; j++ {
			if j >= 0 && j < CurveBuckets {
				sum += counts[j]
				n++
			}
		}
		out[i] = float64(sum) / float64(n)
	}
	return out
}

// Normalize divides by the highest bucket and scales to 0..CurveMax
// (rounded). An all-zero input stays all zero.
func Normalize(v [CurveBuckets]float64) []int16 {
	peak := 0.0
	for _, x := range v {
		peak = math.Max(peak, x)
	}
	out := make([]int16, CurveBuckets)
	if peak <= 0 {
		return out
	}
	for i, x := range v {
		out[i] = int16(math.Round(x / peak * CurveMax))
	}
	return out
}

// ComputeCurve runs dedupe -> 3-bucket smoothing -> normalization. durationMS
// is the version's duration; when unknown (<= 0) the latest danmaku bounds
// the timeline.
func ComputeCurve(points []CurvePoint, durationMS int) Curve {
	if durationMS <= 0 {
		for _, p := range points {
			durationMS = max(durationMS, p.TMs+1)
		}
	}
	c := Curve{BucketMS: BucketMS(durationMS)}
	counts, valid := DedupeCounts(points, c.BucketMS)
	c.Valid = valid
	if valid < MinCurveDanmaku {
		return c
	}
	c.Data = Normalize(Smooth3(counts))
	return c
}

// AltVersionKey is the version key of an alternate-version source.
func AltVersionKey(sourceID int64) string {
	return altVersionPrefix + strconv.FormatInt(sourceID, 10)
}
