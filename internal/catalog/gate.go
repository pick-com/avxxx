package catalog

import (
	"fmt"
	"strings"
	"time"
)

// Phase separates the kinds of catalog crawl a source can run
// (FINAL_DATA_DESIGN 7, 10.3):
//
//   - baseline: the one-off full pass over a source's archive, resumable
//     page by page. Only enqueued on an explicit admin request.
//   - incremental: the daily pass that reads the newest-first listing from
//     the head and stops at the stored watermark. Enqueued by the scheduler on
//     the source's discovery interval.
//   - verify: a manual, page-capped probe that parses but never writes to the
//     catalog. The only phase a source without confirmed terms may run.
type Phase string

const (
	PhaseBaseline    Phase = "baseline"
	PhaseIncremental Phase = "incremental"
	PhaseVerify      Phase = "verify"
)

// ParsePhase accepts a phase name; "" means incremental.
func ParsePhase(v string) (Phase, error) {
	switch p := Phase(strings.ToLower(strings.TrimSpace(v))); p {
	case "":
		return PhaseIncremental, nil
	case PhaseBaseline, PhaseIncremental, PhaseVerify:
		return p, nil
	}
	return "", fmt.Errorf("phase must be baseline, incremental or verify")
}

// Automatic reports whether the phase is an automatic (non-manual) crawl.
func (p Phase) Automatic() bool { return p != PhaseVerify }

// License statuses (sources.license_status, migrations/024). A source may run
// automatic crawls only with one of AutomaticLicenses and a recorded terms
// review; every other status is limited to manual verify jobs, and denied
// sources run nothing at all.
const (
	LicenseReviewRequired = "review_required" // default: terms not reviewed yet
	LicenseManualOnly     = "manual_only"     // reviewed: manual verification only
	LicenseDenied         = "denied"          // terms forbid automated access
	LicenseTermsConfirmed = "terms_confirmed" // terms reviewed and permit automated metadata crawling
	LicenseLicensed       = "licensed"        // explicit license or agreement
	LicenseOfficialAPI    = "official_api"    // official API with documented scope and quota
	LicenseInternal       = "internal"        // first-party data we own (fixtures, demo)
)

// LicenseStatuses lists every valid license_status in display order.
var LicenseStatuses = []string{LicenseReviewRequired, LicenseManualOnly, LicenseDenied, LicenseTermsConfirmed, LicenseLicensed, LicenseOfficialAPI, LicenseInternal}

// AutomaticLicenses are the statuses that permit automatic crawls.
var AutomaticLicenses = map[string]bool{LicenseTermsConfirmed: true, LicenseLicensed: true, LicenseOfficialAPI: true, LicenseInternal: true}

// ValidLicenseStatus reports whether v is a known license_status.
func ValidLicenseStatus(v string) bool {
	for _, s := range LicenseStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Gate error codes recorded in jobs.error_code.
const (
	CodeLicenseGate    = "license_gate"
	CodeRobotsDisallow = "robots_disallow"
	CodeConfig         = "config_error"
)

// Verify jobs are small by construction.
const (
	// VerifyDefaultPages is the discovery page cap of a verify job when the
	// admin does not choose one; VerifyMaxPages is the hard ceiling.
	VerifyDefaultPages = 1
	VerifyMaxPages     = 3
	// VerifyMaxRequests bounds every request a verify job may make
	// (discovery pages plus their detail pages).
	VerifyMaxRequests = 60
)

// SourceGovernance is the subset of a source row the gate needs.
type SourceGovernance struct {
	Adapter         string
	LicenseStatus   string
	TermsURL        string
	TermsReviewedAt *time.Time
}

// GateDecision is the outcome of Gate. MaxPages is the discovery page cap the
// job must run with (0 = the phase's normal policy).
type GateDecision struct {
	Allowed  bool
	Code     string
	Reason   string
	MaxPages int
}

// IsFeed reports whether the source is a syndication feed (adapter rss)
// rather than a catalog adapter. Feeds are published for automated readers,
// so the catalog license gate does not apply to them.
func (g SourceGovernance) IsFeed() bool { return g.Adapter == "" || g.Adapter == "rss" }

// Gate decides whether a job of phase may run for a source
// (FINAL_DATA_DESIGN 2.4, 10.3): automatic phases need an automatic license
// status, a terms review timestamp and a terms URL (internal sources need no
// URL); verify jobs need only that the source is not denied, and are capped at
// VerifyMaxPages discovery pages. requestedPages is the admin's verify cap (0 =
// default).
func Gate(src SourceGovernance, phase Phase, requestedPages int) GateDecision {
	if src.IsFeed() {
		return GateDecision{Allowed: true}
	}
	status := src.LicenseStatus
	if status == LicenseDenied {
		return GateDecision{Code: CodeLicenseGate, Reason: "source license_status is denied: no crawling of any kind"}
	}
	if phase == PhaseVerify {
		pages := requestedPages
		if pages <= 0 {
			pages = VerifyDefaultPages
		}
		if pages > VerifyMaxPages {
			pages = VerifyMaxPages
		}
		return GateDecision{Allowed: true, MaxPages: pages}
	}
	if !AutomaticLicenses[status] {
		return GateDecision{Code: CodeLicenseGate, Reason: fmt.Sprintf("license_status %q does not permit automatic crawls; only manual verify jobs", status)}
	}
	if src.TermsReviewedAt == nil {
		return GateDecision{Code: CodeLicenseGate, Reason: "terms have not been reviewed (terms_reviewed_at is empty); only manual verify jobs"}
	}
	if strings.TrimSpace(src.TermsURL) == "" && status != LicenseInternal {
		return GateDecision{Code: CodeLicenseGate, Reason: "terms_url is empty; only manual verify jobs"}
	}
	return GateDecision{Allowed: true}
}

// GateError is a run that was refused by a governance gate. The job is not
// retried; Code goes to jobs.error_code.
type GateError struct {
	Code   string
	Reason string
}

func (e *GateError) Error() string { return e.Code + ": " + e.Reason }
