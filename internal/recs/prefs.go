package recs

// Soft preferences: onboarding interests for cold-start users and Tonight's
// 想要 (mood) / 和谁看 (company). Unlike TonightKeep's duration bucket they
// never filter anything out; they only raise the score of releases that
// carry one of the preferred tags (release_tags.normalized_tag) or whose
// known runtime falls in the preferred window. A release without tags or
// runtime is simply not boosted.

// ChanInterests recalls releases tagged with the user's onboarding interests
// (user_interests, welcome.tsx) while the user has little watch history.
const ChanInterests = "interests"

// ColdStartHistory: a signed-in user with fewer watch_history rows than
// this is "cold": interests are recalled and boosted.
const ColdStartHistory = 5

// Preference is one soft preference.
type Preference struct {
	// Tags are normalized tags; each match multiplies the score by
	// (1 + TagBoost), at most MaxTagMatches matches.
	Tags     map[string]bool
	TagBoost float64
	// MinMinutes / MaxMinutes (0 = open) is the preferred runtime; a known
	// runtime inside it multiplies the score by (1 + DurationBoost).
	MinMinutes, MaxMinutes int
	DurationBoost          float64
}

// MaxTagMatches caps how many matching tags of one preference count.
const MaxTagMatches = 2

// Boosts of the built-in preferences.
const (
	InterestTagBoost = 0.15
	TonightTagBoost  = 0.12
	TonightDurBoost  = 0.08
)

// Factor is the multiplier the preference gives a release.
func (p Preference) Factor(info Info) float64 {
	f := 1.0
	if len(p.Tags) > 0 && p.TagBoost > 0 {
		n := 0
		for _, t := range info.Tags {
			if p.Tags[t] {
				n++
			}
		}
		for range min(n, MaxTagMatches) {
			f *= 1 + p.TagBoost
		}
	}
	if p.DurationBoost > 0 && (p.MinMinutes > 0 || p.MaxMinutes > 0) && info.RuntimeMinutes > 0 {
		if (p.MinMinutes == 0 || info.RuntimeMinutes >= p.MinMinutes) && (p.MaxMinutes == 0 || info.RuntimeMinutes <= p.MaxMinutes) {
			f *= 1 + p.DurationBoost
		}
	}
	return f
}

// InterestPreference boosts the user's onboarding interest tags.
func InterestPreference(tags []string) (Preference, bool) {
	if len(tags) == 0 {
		return Preference{}, false
	}
	set := map[string]bool{}
	for _, t := range tags {
		set[t] = true
	}
	return Preference{Tags: set, TagBoost: InterestTagBoost}, true
}

// Tonight 想要 (mood) and 和谁看 (company) values (routes/tonight.tsx).
const (
	MoodAny      = "any"
	MoodLight    = "light"
	MoodThrill   = "thrill"
	MoodTouching = "touching"
	MoodBrainy   = "brainy"

	CompanySelf    = "self"
	CompanyFriends = "friends"
	CompanyFamily  = "family"
)

// moodTags maps a mood to genre tags the catalog sources use (JAV Database
// genre names through catalog.Slug). The mapping is deliberately narrow:
// only genres that plainly mean the mood. Tags missing from the catalog
// match nothing.
var moodTags = map[string][]string{
	MoodLight:    {"comedy", "parody"},
	MoodThrill:   {"horror", "thriller", "suspense"},
	MoodTouching: {"drama", "romance"},
	MoodBrainy:   {"mystery", "documentary"},
}

// ValidMood reports whether m is a Tonight mood ("" = any).
func ValidMood(m string) bool {
	_, ok := moodTags[m]
	return ok || m == "" || m == MoodAny
}

// ValidCompany reports whether c is a Tonight company ("" = self).
func ValidCompany(c string) bool {
	switch c {
	case "", CompanySelf, CompanyFriends, CompanyFamily:
		return true
	}
	return false
}

// TonightPreferences turns 想要 / 和谁看 into soft preferences:
//
//	mood     → its genre tags (moodTags); light also prefers ≤ 90 min
//	friends  → comedy / parody and a runtime of at most 90 min (one sitting)
//	self, family, any → nothing (no catalog field says what suits them)
func TonightPreferences(mood, company string) []Preference {
	var out []Preference
	if tags := moodTags[mood]; len(tags) > 0 {
		p := Preference{Tags: map[string]bool{}, TagBoost: TonightTagBoost}
		for _, t := range tags {
			p.Tags[t] = true
		}
		if mood == MoodLight {
			p.MaxMinutes, p.DurationBoost = 90, TonightDurBoost
		}
		out = append(out, p)
	}
	if company == CompanyFriends {
		out = append(out, Preference{Tags: map[string]bool{"comedy": true, "parody": true}, TagBoost: TonightTagBoost,
			MaxMinutes: 90, DurationBoost: TonightDurBoost})
	}
	return out
}
