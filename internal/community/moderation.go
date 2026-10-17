package community

import (
	"math"
	"regexp"
	"time"
)

// Content statuses shared by danmaku and reviews.
const (
	StatusVisible = "visible"
	StatusShadow  = "shadow" // only the author sees it; waits for a moderator
	StatusFolded  = "folded" // folded as a spoiler by a moderator
	StatusDeleted = "deleted"
)

// Danmaku display modes and the default color.
const (
	ModeScroll   = "scroll"
	ModeTop      = "top"
	ModeBottom   = "bottom"
	DefaultColor = "#FFFFFF"
)

// ValidMode reports whether m is a danmaku mode.
func ValidMode(m string) bool { return m == ModeScroll || m == ModeTop || m == ModeBottom }

var (
	colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	langPattern  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)
)

// ValidColor reports whether c is "#RRGGBB".
func ValidColor(c string) bool { return colorPattern.MatchString(c) }

// ValidLang reports whether l is empty or a short BCP 47 tag ("zh-CN", "en").
func ValidLang(l string) bool { return l == "" || langPattern.MatchString(l) }

// Report targets and reasons (tech plan 3.8: one inbox for everything).
var (
	ReportTargets = []string{"danmaku", "review", "rating", "release", "performer_info", "source", "comment", "clip"}
	ReportReasons = []string{"spoiler", "spam", "abuse", "illegal", "underage", "privacy", "copyright", "misinfo", "broken", "other"}
)

// ValidReportTarget reports whether t is a reports.target_type.
func ValidReportTarget(t string) bool { return contains(ReportTargets, t) }

// ValidReportReason reports whether r is an accepted report reason.
func ValidReportReason(r string) bool { return contains(ReportReasons, r) }

// ReportSLA is how long a report may wait before it is overdue in the
// moderation queue: illegal content, minors and privacy within 2 hours,
// copyright within 24 hours (complaint flow), everything else 48 hours.
func ReportSLA(reason string) time.Duration {
	switch reason {
	case "illegal", "underage", "privacy":
		return 2 * time.Hour
	case "copyright", "abuse":
		return 24 * time.Hour
	}
	return 48 * time.Hour
}

// ReportDue is the due_at of a report created at now.
func ReportDue(reason string, now time.Time) time.Time { return now.Add(ReportSLA(reason)) }

// Moderation actions (PRD 5.5: fold as spoiler, delete and mute, delete,
// approve). Ratings accept delete (strike the vote) and approve.
const (
	ActionFold       = "fold"
	ActionDelete     = "delete"
	ActionDeleteMute = "delete_mute"
	ActionApprove    = "approve"
	// ActionRemoveTitle (clips only): clear a clip's title, keep the clip.
	ActionRemoveTitle = "remove_title"
	MaxMuteDays       = 365
	DefaultMuteDays   = 7
)

// ActionStatus is the content status a moderation action leads to.
func ActionStatus(action string) (string, bool) {
	switch action {
	case ActionFold:
		return StatusFolded, true
	case ActionDelete, ActionDeleteMute:
		return StatusDeleted, true
	case ActionApprove:
		return StatusVisible, true
	}
	return "", false
}

// Comment sorts (PRD 5.4: 评论按最热 / 最新排序).
const (
	CommentSortTop    = "top"    // most liked first
	CommentSortNewest = "newest" // newest first
)

// ValidCommentSort reports whether s is a comment sort.
func ValidCommentSort(s string) bool { return s == CommentSortTop || s == CommentSortNewest }

// Review sorts (PRD 19).
const (
	ReviewSortHelpful  = "helpful"  // likes x time decay
	ReviewSortNewest   = "newest"   // newest first
	ReviewSortFinished = "finished" // only reviews by authors who finished, newest first
	// FinishedRatio is the watch ratio from which the author counts as
	// having finished ("看完").
	FinishedRatio = 0.9
	// HelpfulHalfLife: a review's helpfulness halves every this long.
	HelpfulHalfLife = 7 * 24 * time.Hour
)

// ValidReviewSort reports whether s is a review sort.
func ValidReviewSort(s string) bool {
	return s == ReviewSortHelpful || s == ReviewSortNewest || s == ReviewSortFinished
}

// HelpfulScore is the "最有用" key: (likes + 1) halved every HelpfulHalfLife
// of age, so a fresh review with no likes is not buried forever and old
// popular reviews slowly make room. The +1 keeps age ordering among
// zero-like reviews.
func HelpfulScore(likes int, age time.Duration) float64 {
	if age < 0 {
		age = 0
	}
	return float64(likes+1) * math.Exp2(-age.Hours()/HelpfulHalfLife.Hours())
}

// WatchBadge is the watch-progress label next to a review (PRD 19: finished
// / watched to N% / not watched): "finished", "partial" or "none", with the
// percentage for partial.
func WatchBadge(ratio float64) (badge string, percent int) {
	switch {
	case ratio >= FinishedRatio:
		return "finished", 100
	case ratio > 0:
		return "partial", int(math.Floor(ratio * 100))
	}
	return "none", 0
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
