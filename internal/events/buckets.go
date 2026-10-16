package events

import (
	"errors"
	"fmt"
	"strings"
)

// Replay table wire format (PRD 14.3, client web/app/lib/playAnalytics.ts).
//
// The `buckets` prop of heartbeat (every 5 minutes, delta since the last
// report) and play_end (the remainder) is a run-length encoded table of the
// 200 timeline buckets of one play: how many times each bucket was at least
// 50% played in normal playback. Per play_id the tables sum to the full table.
//
//	buckets = "r1:" token *( "," token )
//	token   = count [ "*" run ]
//	count   = 1*3DIGIT            ; 0..255
//	run     = 1*3DIGIT            ; 1..200, default 1
//
// e.g. "r1:0*40,1*150,2*10". The runs must expand to exactly 200 buckets.
// Leading zeros, signs, spaces and empty tokens are rejected. The client
// omits the prop when the delta is all zeros; an all-zero table is accepted.
const (
	ReplayBuckets  = 200
	ReplayCountMax = 255
	BucketsVersion = "r1:"
	BucketsMaxLen  = 1024 // the worst case ("255," × 200) is 802 bytes
)

// ReplayTable is one decoded replay table (or delta).
type ReplayTable [ReplayBuckets]uint8

var errBucketsEmpty = errors.New("empty")

// DecodeBuckets parses the r1 run-length format. It never allocates more
// than the fixed table, whatever the input.
func DecodeBuckets(s string) (ReplayTable, error) {
	var t ReplayTable
	if len(s) > BucketsMaxLen {
		return t, fmt.Errorf("longer than %d bytes", BucketsMaxLen)
	}
	body, ok := strings.CutPrefix(s, BucketsVersion)
	if !ok {
		return t, fmt.Errorf("must start with %q", BucketsVersion)
	}
	if body == "" {
		return t, errBucketsEmpty
	}
	n := 0
	for tok := range strings.SplitSeq(body, ",") {
		countStr, runStr, hasRun := strings.Cut(tok, "*")
		count, err := smallUint(countStr, ReplayCountMax)
		if err != nil {
			return t, fmt.Errorf("token %q: count %v", tok, err)
		}
		run := 1
		if hasRun {
			if run, err = smallUint(runStr, ReplayBuckets); err != nil {
				return t, fmt.Errorf("token %q: run %v", tok, err)
			}
			if run == 0 {
				return t, fmt.Errorf("token %q: run must be at least 1", tok)
			}
		}
		if n+run > ReplayBuckets {
			return t, fmt.Errorf("expands to more than %d buckets", ReplayBuckets)
		}
		for i := n; i < n+run; i++ {
			t[i] = uint8(count)
		}
		n += run
	}
	if n != ReplayBuckets {
		return t, fmt.Errorf("expands to %d buckets, want %d", n, ReplayBuckets)
	}
	return t, nil
}

// smallUint parses 1-3 decimal digits without sign or leading zeros, at most max.
func smallUint(s string, max int) (int, error) {
	if s == "" {
		return 0, errBucketsEmpty
	}
	if len(s) > 3 {
		return 0, errors.New("too long")
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, errors.New("has a leading zero")
	}
	v := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("is not a decimal number")
		}
		v = v*10 + int(c-'0')
	}
	if v > max {
		return 0, fmt.Errorf("above %d", max)
	}
	return v, nil
}

// EncodeBuckets is the inverse of DecodeBuckets (same output as the client's
// encodeBuckets, except that an all-zero table encodes as "r1:0*200").
func EncodeBuckets(t ReplayTable) string {
	var b strings.Builder
	b.WriteString(BucketsVersion)
	for i := 0; i < ReplayBuckets; {
		run := 1
		for i+run < ReplayBuckets && t[i+run] == t[i] {
			run++
		}
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprint(&b, t[i])
		if run > 1 {
			fmt.Fprintf(&b, "*%d", run)
		}
		i += run
	}
	return b.String()
}

// checkBuckets is the Field.Format check for the `buckets` prop.
func checkBuckets(s string) string {
	if _, err := DecodeBuckets(s); err != nil {
		return "invalid replay table: " + err.Error()
	}
	return ""
}
