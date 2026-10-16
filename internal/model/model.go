package model

import "time"

type Item struct {
	ID        int64     `json:"id"`
	SourceID  int64     `json:"source_id"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Source struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Enabled       bool   `json:"enabled"`
	Version       int64  `json:"version"`
	Adapter       string `json:"adapter"`
	SourceKind    string `json:"source_kind"`
	LicenseStatus string `json:"license_status"`
	EgressPolicy  string `json:"egress_policy"`
	// Governance (migrations/024): terms review and the automatic-crawl gate.
	TermsURL            string     `json:"terms_url,omitempty"`
	TermsReviewedAt     *time.Time `json:"terms_reviewed_at,omitempty"`
	TermsReviewedBy     string     `json:"terms_reviewed_by,omitempty"`
	LicenseNote         string     `json:"license_note,omitempty"`
	BaselineRequestedAt *time.Time `json:"baseline_requested_at,omitempty"`
	AutomaticAllowed    bool       `json:"automatic_allowed"`
	GateReason          string     `json:"gate_reason,omitempty"`
}
type Job struct {
	ID               int64      `json:"id"`
	SourceID         int64      `json:"source_id"`
	Phase            string     `json:"phase"`
	PageCap          *int       `json:"page_cap,omitempty"`
	Status           string     `json:"status"`
	Error            string     `json:"error"`
	ErrorCode        string     `json:"error_code"`
	CreatedAt        time.Time  `json:"created_at"`
	Attempt          int        `json:"attempt"`
	LeaseUntil       *time.Time `json:"lease_until,omitempty"`
	ExecutionVersion int64      `json:"execution_version"`
}

type Performer struct {
	ID              int64      `json:"id"`
	Slug            string     `json:"slug"`
	CanonicalName   string     `json:"canonical_name"`
	CanonicalNameJA string     `json:"canonical_name_ja,omitempty"`
	BirthDate       *time.Time `json:"birth_date,omitempty"`
	HeightCM        float64    `json:"height_cm,omitempty"`
	BustCM          float64    `json:"bust_cm,omitempty"`
	WaistCM         float64    `json:"waist_cm,omitempty"`
	HipCM           float64    `json:"hip_cm,omitempty"`
	Status          string     `json:"status"`
}

type Release struct {
	ID             int64      `json:"id"`
	Slug           string     `json:"slug"`
	CanonicalTitle string     `json:"canonical_title"`
	TitleJA        string     `json:"title_ja,omitempty"`
	ReleaseDate    *time.Time `json:"release_date,omitempty"`
	RuntimeMinutes int        `json:"runtime_minutes,omitempty"`
	MakerName      string     `json:"maker_name,omitempty"`
	LabelName      string     `json:"label_name,omitempty"`
	SeriesName     string     `json:"series_name,omitempty"`
	Status         string     `json:"status"`
}
