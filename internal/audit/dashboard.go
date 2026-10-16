package audit

import "time"

// IssueFilter selects audit issues for the admin list.
type IssueFilter struct {
	Rule   Rule   // "" = all rules
	Status string // open (default), fixed or dismissed
	Cursor string // opaque, from the previous page's next_cursor
	Limit  int
}

// JobCounts is the queue state of one job kind.
type JobCounts struct {
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Failed24h int64 `json:"failed_24h"`
}

// CatalogHealth is the 收录健康分 on AdminDashboard: the share of published
// releases with no open audit issue, and its PRD 11 band.
type CatalogHealth struct {
	Score          *int   `json:"score"` // nil when nothing is published
	Band           string `json:"band,omitempty"`
	Published      int64  `json:"published"`
	WithOpenIssues int64  `json:"with_open_issues"`
}

// Dashboard is the AdminDashboard (A1) overview payload.
type Dashboard struct {
	GeneratedAt time.Time `json:"generated_at"`
	Releases    struct {
		Total    int64            `json:"total"`
		Held     int64            `json:"held"`
		ByStatus map[string]int64 `json:"by_status"`
	} `json:"releases"`
	Performers struct {
		Total    int64            `json:"total"`
		ByStatus map[string]int64 `json:"by_status"`
	} `json:"performers"`
	Issues struct {
		Open           int64                       `json:"open"`
		ByRule         map[string]int64            `json:"by_rule"`
		BySeverity     map[string]int64            `json:"by_severity"`
		ByRuleSeverity map[string]map[string]int64 `json:"by_rule_severity"`
	} `json:"issues"`
	Jobs                   map[string]JobCounts `json:"jobs"`
	NightlyRuns            []Run                `json:"nightly_runs"`
	IdentityCandidatesOpen int64                `json:"identity_candidates_open"`
	ConflictsOpen          int64                `json:"conflicts_open"`
	CatalogHealth          CatalogHealth        `json:"catalog_health"`
	SourceHealth           SourceHealthSummary  `json:"source_health"`
}

// NewDashboard returns a Dashboard with every map initialized.
func NewDashboard(now time.Time) Dashboard {
	var d Dashboard
	d.GeneratedAt = now
	d.Releases.ByStatus = map[string]int64{}
	d.Performers.ByStatus = map[string]int64{}
	d.Issues.ByRule = map[string]int64{}
	d.Issues.BySeverity = map[string]int64{}
	d.Issues.ByRuleSeverity = map[string]map[string]int64{}
	d.Jobs = map[string]JobCounts{}
	d.NightlyRuns = []Run{}
	d.SourceHealth.ByBand = map[string]int64{BandSevere: 0, BandDegraded: 0, BandHealthy: 0, BandExcellent: 0, BandInsufficient: 0}
	d.SourceHealth.ByStatus = map[string]int64{SourceActive: 0, SourceDegraded: 0, SourceDisabled: 0}
	return d
}

// AddIssueCount folds one (rule, severity, count) group into the totals.
func (d *Dashboard) AddIssueCount(rule, severity string, n int64) {
	d.Issues.Open += n
	d.Issues.ByRule[rule] += n
	d.Issues.BySeverity[severity] += n
	if d.Issues.ByRuleSeverity[rule] == nil {
		d.Issues.ByRuleSeverity[rule] = map[string]int64{}
	}
	d.Issues.ByRuleSeverity[rule][severity] += n
}

// SetHealth fills CatalogHealth from the published and affected counts.
func (d *Dashboard) SetHealth(published, withIssues int64) {
	d.CatalogHealth.Published, d.CatalogHealth.WithOpenIssues = published, withIssues
	if s, ok := HealthScore(published, withIssues); ok {
		d.CatalogHealth.Score = &s
		d.CatalogHealth.Band = HealthBand(s)
	}
}
