package recs

// 推荐反馈 (migration 051 rec_feedback): YouTube-style 「不感兴趣」 /
// 「不推荐此演员」 / 「不推荐此类型」 on recommendation cards. Rank applies it
// on every personalized slot; the global trending boards ignore it.

// Feedback kinds (rec_feedback.kind).
const (
	FeedbackRelease   = "not_interested_release"
	FeedbackPerformer = "not_interested_performer"
	FeedbackTag       = "not_interested_tag"
)

// ValidFeedbackKind reports whether k is a rec_feedback kind.
func ValidFeedbackKind(k string) bool {
	switch k {
	case FeedbackRelease, FeedbackPerformer, FeedbackTag:
		return true
	}
	return false
}

// TagFeedbackFactor multiplies the score of a release carrying a tag marked
// 不感兴趣 (once, however many of its tags are marked): strong enough to push
// it below comparable releases, not a filter.
const TagFeedbackFactor = 0.35

// Feedback is a viewer's active 推荐反馈.
type Feedback struct {
	Releases   map[int64]bool  // never recommended
	Performers map[int64]bool  // releases led by them are dropped; their recall hits ignored
	Tags       map[string]bool // normalized tags: demoted by TagFeedbackFactor
}

// Empty reports whether there is nothing to apply.
func (f Feedback) Empty() bool {
	return len(f.Releases) == 0 && len(f.Performers) == 0 && len(f.Tags) == 0
}

// Add records one active feedback row.
func (f *Feedback) Add(kind string, id int64, tag string) {
	switch kind {
	case FeedbackRelease:
		if f.Releases == nil {
			f.Releases = map[int64]bool{}
		}
		f.Releases[id] = true
	case FeedbackPerformer:
		if f.Performers == nil {
			f.Performers = map[int64]bool{}
		}
		f.Performers[id] = true
	case FeedbackTag:
		if f.Tags == nil {
			f.Tags = map[string]bool{}
		}
		f.Tags[tag] = true
	}
}

// drops reports whether a hydrated release is filtered out: marked 不感兴趣
// itself, or led by a performer marked 不推荐.
func (f Feedback) drops(info Info) bool {
	return f.Releases[info.ReleaseID] || (info.PerformerID != 0 && f.Performers[info.PerformerID])
}

// ignoresHit reports whether a recall hit came through a performer marked
// 不推荐 (same_performer / followed_new on a co-star): it neither scores nor
// becomes the reason.
func (f Feedback) ignoresHit(c Candidate) bool {
	return c.PerformerID != 0 && f.Performers[c.PerformerID]
}

// factor is the tag demotion of a release.
func (f Feedback) factor(info Info) float64 {
	for _, t := range info.Tags {
		if f.Tags[t] {
			return TagFeedbackFactor
		}
	}
	return 1
}
