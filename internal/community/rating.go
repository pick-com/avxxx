package community

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"time"
)

// Rating rules (PRD 19, tech plan 3.8).
const (
	MinScore = 1
	MaxScore = 5
	// MinRatingVotes: fewer active votes than this and no score is shown.
	MinRatingVotes = 50
	// PriorWeight is C in the Bayesian average: how many full-weight votes
	// the site-wide mean counts as.
	PriorWeight = 25.0
	// DefaultPriorMean (1-5 stars) is used until the site has any votes.
	DefaultPriorMean = 3.0
	// NewAccountAge: accounts younger than this when voting are down-weighted.
	NewAccountAge = 7 * 24 * time.Hour

	WeightFull       = 1.0
	WeightNewAccount = 0.5
	WeightAnonymous  = 0.3
	WeightAbnormal   = 0.1

	// Rating-flood heuristic for the moderation queue: at least this many
	// identical scores from low-trust voters (weight < 1) in one hour.
	FloodMinVotes = 20
	FloodWindow   = time.Hour
)

// Voter describes who casts a vote, for RatingWeight.
type Voter struct {
	UserID     int64         // 0 for an anonymous device vote
	DeviceID   string        // raw X-Device-ID, anonymous votes only
	AccountAge time.Duration // at vote time; logged-in voters only
	Abnormal   bool          // muted now or before, or had votes struck by a moderator
}

// RatingWeight is the weight of one vote: anonymous 0.3, abnormal account
// 0.1, account younger than 7 days 0.5, otherwise 1. Abnormal wins over age.
func RatingWeight(v Voter) float64 {
	switch {
	case v.UserID == 0:
		return WeightAnonymous
	case v.Abnormal:
		return WeightAbnormal
	case v.AccountAge < NewAccountAge:
		return WeightNewAccount
	}
	return WeightFull
}

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// ValidDeviceID reports whether s is an acceptable X-Device-ID value.
func ValidDeviceID(s string) bool { return deviceIDPattern.MatchString(s) }

// DeviceHash is the stored form of a device id: the raw value never reaches
// the database.
func DeviceHash(deviceID string) string {
	sum := sha256.Sum256([]byte("avxxx-device:" + deviceID))
	return hex.EncodeToString(sum[:16])
}

// VoterKey is ratings.voter_key: "u:<user id>" or "d:<device hash>".
func VoterKey(v Voter) string {
	if v.UserID != 0 {
		return "u:" + strconv.FormatInt(v.UserID, 10)
	}
	return "d:" + DeviceHash(v.DeviceID)
}

// RatingAggregate is the weighted sum over a release's active votes.
type RatingAggregate struct {
	Count     int     // number of active votes (unweighted)
	WeightSum float64 // sum of weights
	ScoreSum  float64 // sum of weight * score (1-5)
}

// BayesianScore returns the 1-5 star Bayesian average, shrunk towards
// priorMean with PriorWeight: (C*m + sum(w*s)) / (C + sum(w)).
func BayesianScore(a RatingAggregate, priorMean float64) float64 {
	if priorMean < MinScore || priorMean > MaxScore {
		priorMean = DefaultPriorMean
	}
	return (PriorWeight*priorMean + a.ScoreSum) / (PriorWeight + a.WeightSum)
}

// TenPoint converts a 1-5 star score to the displayed 10-point scale, one
// decimal (PRD 19: stars shown as a 10-point score).
func TenPoint(stars float64) float64 { return math.Round(stars*20) / 10 }

// DisplayScore is the public score: the 10-point Bayesian average, or nil
// when fewer than MinRatingVotes voted.
func DisplayScore(a RatingAggregate, priorMean float64) *float64 {
	if a.Count < MinRatingVotes {
		return nil
	}
	v := TenPoint(BayesianScore(a, priorMean))
	return &v
}
