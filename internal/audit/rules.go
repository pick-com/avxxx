package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------- R-AGE

// MonthDay is a day of the year, independent of the year.
type MonthDay struct {
	Month time.Month
	Day   int
}

func (m MonthDay) before(o MonthDay) bool {
	return m.Month < o.Month || (m.Month == o.Month && m.Day < o.Day)
}

func monthDay(t time.Time) MonthDay { return MonthDay{t.Month(), t.Day()} }

// AgeConfig tunes R-AGE.
type AgeConfig struct {
	// LateBirthday: a computed age of exactly 18 is also held when the
	// performer's birthday falls on or after this day of the year, because
	// the year difference is only an estimate ("约") and filming in the first
	// part of that year would make the performer 17.
	LateBirthday MonthDay
}

// DefaultAgeConfig flags 18-year-olds born on or after 1 July.
var DefaultAgeConfig = AgeConfig{LateBirthday: MonthDay{time.July, 1}}

// Age finding reasons.
const (
	ReasonUnder18          = "under_18"                  // year difference < 18
	ReasonUnder18AtRelease = "under_18_at_release"       // 18 by year, but the birthday falls after the release day
	ReasonLateBirthday18   = "possibly_17_late_birthday" // 18 by year, birthday late in the year (conservative)
)

// AgeFinding is one performer whose age at filming may be under 18.
type AgeFinding struct {
	PerformerID int64  `json:"performer_id"`
	Name        string `json:"name"`
	BirthDate   string `json:"birth_date"`
	Age         int    `json:"age"`   // year difference (PRD 6.4: shoot year − birth year)
	Basis       string `json:"basis"` // "shoot_year" or "release_year"
	Reason      string `json:"reason"`
}

// CheckAge returns the performers of r whose age at filming is, or may be,
// under 18. Performers without a birth date, and releases with neither a
// shoot year nor a release date, cannot be computed and are left to R-META.
//
// Age is the year difference, as PRD 6.4 defines it. With only a release
// date (no shoot year, the normal case for JAV data) the age is an estimate
// and filming happened on or before the release date, so:
//   - year difference < 18 → hold;
//   - year difference 18 but the performer was still 17 on the release date
//     (birthday after the release day of the year) → hold;
//   - year difference 18 and the birthday is on/after cfg.LateBirthday → hold
//     (conservative: the performer was 17 for most of that year).
//
// The late-birthday case also applies with a shoot year, since a year alone
// does not say whether filming happened before the birthday.
func CheckAge(r ReleaseFacts, cfg AgeConfig) []AgeFinding {
	var out []AgeFinding
	for _, p := range r.Performers {
		if p.BirthDate == nil {
			continue
		}
		birth := *p.BirthDate
		var year int
		var basis string
		switch {
		case r.ShootYear > 0:
			year, basis = r.ShootYear, "shoot_year"
		case r.ReleaseDate != nil:
			year, basis = r.ReleaseDate.Year(), "release_year"
		default:
			continue
		}
		age := year - birth.Year()
		reason := ""
		switch {
		case age < 18:
			reason = ReasonUnder18
		case age == 18 && basis == "release_year" && monthDay(*r.ReleaseDate).before(monthDay(birth)):
			reason = ReasonUnder18AtRelease
		case age == 18 && !monthDay(birth).before(cfg.LateBirthday):
			reason = ReasonLateBirthday18
		}
		if reason != "" {
			out = append(out, AgeFinding{PerformerID: p.ID, Name: p.Name, BirthDate: birth.Format(time.DateOnly), Age: age, Basis: basis, Reason: reason})
		}
	}
	return out
}

// ---------------------------------------------------------------- R-META

// MetaFindings lists what one release is missing.
type MetaFindings struct {
	// Release lists missing release fields: release_date, runtime, performers.
	Release []string
	// MissingBirth lists credited performers without a birth date.
	MissingBirth []PerformerFacts
}

// Empty reports whether nothing is missing.
func (m MetaFindings) Empty() bool { return len(m.Release) == 0 && len(m.MissingBirth) == 0 }

// CheckMeta returns the metadata R-META requires but r lacks. A missing
// release date only counts when there is no shoot year either, since either
// one is enough to compute age at filming (PRD 6.4.4).
func CheckMeta(r ReleaseFacts) MetaFindings {
	var m MetaFindings
	if r.ReleaseDate == nil && r.ShootYear == 0 {
		m.Release = append(m.Release, "release_date")
	}
	if r.RuntimeMinutes <= 0 {
		m.Release = append(m.Release, "runtime")
	}
	if len(r.Performers) == 0 {
		m.Release = append(m.Release, "performers")
	}
	for _, p := range r.Performers {
		if p.BirthDate == nil {
			m.MissingBirth = append(m.MissingBirth, p)
		}
	}
	return m
}

// ---------------------------------------------------------------- R-TITLE

// NormalizeSpace trims leading and trailing whitespace (including the
// ideographic space U+3000 and NBSP) and collapses every inner whitespace run
// to one ASCII space. A single ideographic space between words is kept as is,
// since Japanese titles use it as a deliberate separator.
func NormalizeSpace(s string) string {
	s = strings.TrimFunc(s, unicode.IsSpace)
	var b strings.Builder
	b.Grow(len(s))
	runes := []rune(s)
	for i := 0; i < len(runes); {
		if !unicode.IsSpace(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && unicode.IsSpace(runes[j]) {
			j++
		}
		if j-i == 1 && runes[i] == '　' {
			b.WriteRune('　')
		} else {
			b.WriteByte(' ')
		}
		i = j
	}
	return b.String()
}

// ---------------------------------------------------------------- helpers

// Fingerprint is a short stable hash of the parts that make up a finding. A
// dismissed issue suppresses later findings only while the fingerprint is
// the same, so changed data is raised again.
func Fingerprint(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

func ageFingerprint(fs []AgeFinding) string {
	parts := make([]string, 0, len(fs))
	for _, f := range fs {
		parts = append(parts, fmt.Sprintf("%d:%s:%d:%s", f.PerformerID, f.BirthDate, f.Age, f.Reason))
	}
	sort.Strings(parts)
	return Fingerprint(parts...)
}

// Health bands (PRD 11): 0–59 severe (fix today), 60–79 degraded (this
// week), 80–94 healthy, 95–100 excellent.
const (
	BandSevere    = "severe"
	BandDegraded  = "degraded"
	BandHealthy   = "healthy"
	BandExcellent = "excellent"
)

// HealthBand maps a 0–100 health score to its PRD band.
func HealthBand(score int) string {
	switch {
	case score < 60:
		return BandSevere
	case score < 80:
		return BandDegraded
	case score < 95:
		return BandHealthy
	}
	return BandExcellent
}

// HealthScore is the share of published releases without an open issue, as
// an integer percentage rounded down (so 99.9% is not shown as 100).
// ok is false when there are no published releases.
func HealthScore(published, withIssues int64) (score int, ok bool) {
	if published <= 0 {
		return 0, false
	}
	if withIssues > published {
		withIssues = published
	}
	return int((published - withIssues) * 100 / published), true
}

// ParseClock parses "HH:MM" (24-hour).
func ParseClock(s string) (hour, minute int, err error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if ok {
		hour, err = strconv.Atoi(h)
		if err == nil {
			minute, err = strconv.Atoi(m)
		}
	}
	if !ok || err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid time of day %q, want HH:MM", s)
	}
	return hour, minute, nil
}

// LastSlot returns the most recent daily hour:minute at or before now, in
// now's location. The scheduler enqueues one audit job per slot.
func LastSlot(now time.Time, hour, minute int) time.Time {
	slot := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if now.Before(slot) {
		slot = slot.AddDate(0, 0, -1)
	}
	return slot
}
