// Package audit is the catalog data-quality audit (AdminAudit / A5, PRD 11).
//
// It has three layers:
//   - pure rule functions (rules.go) that look at plain structs and return
//     findings, with no I/O;
//   - a Runner (runner.go) that walks the catalog through the narrow Store
//     interface below, opens issues, applies auto-fixes and holds, and records
//     one nightly_runs row per rule;
//   - CheckRelease, the inbound check that catalog ingest can call for a single
//     release (R-AGE + R-META).
//
// The PostgreSQL implementation of Store lives in internal/store/audit.go.
package audit

import (
	"context"
	"errors"
	"time"
)

// Rule identifies an audit rule. The PRD letter is noted for the rules the
// AdminAudit artboard lists (PRD 11: 8 rules A–H); R-AGE is the compliance
// rule from the tech plan 2.2 item 3 and runs ahead of all of them.
type Rule string

const (
	RuleAge          Rule = "R-AGE"        // age at filming < 18 → hold (tech plan 2.2.3, PRD 6.4)
	RuleMeta         Rule = "R-META"       // C: missing metadata (PRD 6.4.4)
	RuleTitle        Rule = "R-TITLE"      // E: 繁简与标题 – whitespace normalization
	RuleStats        Rule = "R-STATS"      // G: statistics rebuild
	RuleSourceHealth Rule = "R-SOURCE"     // A: playback source health (sourcehealth.go)
	RuleWatermark    Rule = "R-WATERMARK"  // B: watermark (stub)
	RuleSubtitles    Rule = "R-SUBTITLE"   // D: subtitles (stub)
	RuleScreenshots  Rule = "R-SCREENSHOT" // F: screenshots (stub)
	RuleSEO          Rule = "R-SEO"        // H: 收录检查 / SEO coverage (stub)
)

// AllRules is the run order. R-AGE always runs first.
var AllRules = []Rule{RuleAge, RuleMeta, RuleTitle, RuleStats, RuleSourceHealth, RuleWatermark, RuleSubtitles, RuleScreenshots, RuleSEO}

// KnownRule reports whether r is one of AllRules.
func KnownRule(r Rule) bool {
	for _, x := range AllRules {
		if x == r {
			return true
		}
	}
	return false
}

// Severity of an issue. Blocking issues have already removed the entity from
// public endpoints.
type Severity string

const (
	SeverityBlocking Severity = "blocking"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

// Issue statuses.
const (
	StatusOpen      = "open"
	StatusFixed     = "fixed"
	StatusDismissed = "dismissed"
)

// Run results written to nightly_runs.result.
const (
	ResultOK            = "ok"             // ran, nothing found
	ResultIssues        = "issues"         // ran, found or fixed something
	ResultFailed        = "failed"         // returned an error
	ResultNotApplicable = "not_applicable" // stub, or the data it needs does not exist yet
)

// HoldStatus is the release status a blocking R-AGE finding sets. Every public
// query filters on status='published', so a held release disappears from them.
// The audit never moves a release out of this status.
const HoldStatus = "held_review"

// Actor is written to review_events.actor for every audit change.
const Actor = "audit"

// ErrIssueNotOpen is returned when dismissing an issue that is already fixed
// or dismissed.
var ErrIssueNotOpen = errors.New("audit issue is not open")

// PerformerFacts is what the rules need to know about one credited performer.
type PerformerFacts struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	BirthDate *time.Time `json:"birth_date,omitempty"`
}

// ReleaseFacts is what the rules need to know about one release.
type ReleaseFacts struct {
	ID             int64
	Title          string
	Status         string
	ReleaseDate    *time.Time
	ShootYear      int // 0 = unknown; JAV sources almost never provide it
	RuntimeMinutes int // 0 = unknown
	Popularity     int64
	Performers     []PerformerFacts
}

// Issue is one audit_issues row.
type Issue struct {
	ID          int64          `json:"id"`
	Rule        Rule           `json:"rule"`
	EntityType  string         `json:"entity_type"`
	EntityID    int64          `json:"entity_id"`
	Severity    Severity       `json:"severity"`
	Detail      map[string]any `json:"detail"`
	Fingerprint string         `json:"-"`
	Priority    int64          `json:"priority"`
	Status      string         `json:"status"`
	CreatedAt   time.Time      `json:"created_at"`
	LastSeenAt  time.Time      `json:"last_seen_at"`
	ResolvedAt  *time.Time     `json:"resolved_at,omitempty"`
}

// OpenResult says what OpenIssue did.
type OpenResult int

const (
	IssueCreated    OpenResult = iota // new open issue
	IssueRefreshed                    // an open issue already existed; detail/priority/last_seen updated
	IssueSuppressed                   // an admin dismissed this exact finding (same fingerprint) before
)

// TextField is one text column value the R-TITLE rule may normalize.
type TextField struct {
	EntityType string // "release" or "performer"
	EntityID   int64
	Field      string // canonical_title, title_ja, canonical_name, canonical_name_ja
	Value      string
}

// Run is one nightly_runs row.
type Run struct {
	Job        string         `json:"job"`
	JobID      int64          `json:"job_id,omitempty"`
	StartedAt  time.Time      `json:"started_at"`
	DurationMS int64          `json:"duration_ms"`
	Checked    int            `json:"checked"`
	Fixed      int            `json:"fixed"`
	Issues     int            `json:"issues"`
	Result     string         `json:"result"`
	Detail     map[string]any `json:"detail,omitempty"`
}

// Store is everything the runner needs from the database.
type Store interface {
	// PublishedReleases returns published, unmerged releases with id > afterID
	// in id order, at most limit of them, performers resolved through merges.
	PublishedReleases(ctx context.Context, afterID int64, limit int) ([]ReleaseFacts, error)
	// ReleaseFacts loads one release regardless of status.
	ReleaseFacts(ctx context.Context, id int64) (ReleaseFacts, error)
	// HoldRelease moves the release to HoldStatus if it is publishable
	// (published, draft or upcoming) and writes a review_events row with
	// actor 'audit' in the same transaction. It reports whether it changed.
	HoldRelease(ctx context.Context, releaseID int64, reason string, detail map[string]any) (bool, error)
	// OpenIssue upserts an open issue keyed by (rule, entity_type, entity_id).
	OpenIssue(ctx context.Context, is Issue) (OpenResult, error)
	// ResolveStale marks open issues of rule not seen since before as fixed.
	ResolveStale(ctx context.Context, rule Rule, before time.Time) (int, error)
	// TextFieldsNeedingTrim returns up to limit text values that may contain
	// leading/trailing or repeated whitespace (a cheap SQL prefilter; the
	// runner decides with NormalizeSpace).
	TextFieldsNeedingTrim(ctx context.Context, limit int) ([]TextField, error)
	// FixTextField replaces f.Value with value if the column still holds
	// f.Value, writing a review_events row with actor 'audit'.
	FixTextField(ctx context.Context, f TextField, value string) (bool, error)
	// RecordRun inserts a nightly_runs row.
	RecordRun(ctx context.Context, r Run) error
	// SourceHealthIssues returns the playback sources that are degraded or
	// disabled with the source health job (actor HealthActor) as the author
	// of their latest status change, with their latest scored run. hasRuns
	// is false while the source health job has never recorded a run.
	SourceHealthIssues(ctx context.Context) (facts []SourceHealthFacts, hasRuns bool, err error)
}
