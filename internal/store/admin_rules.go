package store

// Pure validation for the admin video editor (AdminVideo / A3): release
// patches, status transitions, playback source input and release performer
// credits. No I/O; the DB side is admin_release.go.

import (
	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
)

// Release statuses. releases.status has no CHECK constraint (see 013/014/020).
const (
	ReleaseDraft       = "draft"
	ReleaseUpcoming    = "upcoming"
	ReleasePublished   = "published"
	ReleaseUnpublished = "unpublished"
	ReleaseHeld        = audit.HoldStatus // held_review: set by R-AGE
)

// ReleaseStatuses lists every status the admin API understands.
var ReleaseStatuses = []string{ReleaseDraft, ReleaseUpcoming, ReleasePublished, ReleaseUnpublished, ReleaseHeld}

// ValidReleaseStatus reports whether s is one of ReleaseStatuses.
func ValidReleaseStatus(s string) bool { return slices.Contains(ReleaseStatuses, s) }

// AgeRatings are the allowed releases.age_rating values (PRD 15: teen mode
// hides content by rating). Mirrors the CHECK in migration 020.
var AgeRatings = []string{"all", "13", "16", "18"}

// ---------------------------------------------------------------- transitions

// allowedTransitions is the release status machine. Publishing (to published
// or upcoming, which publishes itself when scheduled_at passes) is further
// gated by R-AGE in TransitionRelease. held_review can only be lifted to
// published (with an override reason) or taken down; it cannot be moved
// back to draft, so a hold is never laundered through another status.
var allowedTransitions = map[string][]string{
	ReleaseDraft:       {ReleasePublished, ReleaseUpcoming, ReleaseUnpublished},
	ReleaseUpcoming:    {ReleasePublished, ReleaseUpcoming, ReleaseDraft, ReleaseUnpublished},
	ReleasePublished:   {ReleaseUnpublished},
	ReleaseUnpublished: {ReleasePublished, ReleaseDraft},
	ReleaseHeld:        {ReleasePublished, ReleaseUnpublished},
}

// Transition is a requested status change.
type Transition struct {
	To          string     `json:"status"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	// OverrideReason is required (and only accepted) to publish a held
	// release; it is recorded in review_events and on the closed R-AGE issue.
	OverrideReason string `json:"override_reason"`
	Reason         string `json:"reason"`
	// Version is the optimistic lock (releases.version); 0 skips the check.
	Version int64 `json:"version"`
}

// NeedsAgeGate reports whether moving to status to must pass R-AGE first.
func NeedsAgeGate(to string) bool { return to == ReleasePublished || to == ReleaseUpcoming }

// CheckTransition validates a status change from the current status, apart
// from the R-AGE gate (which needs the release's data). Bad input is a
// *ValidationError (400); a change the state machine forbids is a
// *ConflictError (409).
func CheckTransition(from string, t Transition, now time.Time) error {
	if !ValidReleaseStatus(t.To) {
		return invalid("status must be one of %s", strings.Join(ReleaseStatuses, ", "))
	}
	if len(t.Reason) > MaxReasonLen || len(t.OverrideReason) > MaxReasonLen {
		return invalid("reason too long")
	}
	if t.To == ReleaseUpcoming {
		if t.ScheduledAt == nil {
			return invalid("scheduled_at is required for upcoming")
		}
		if !t.ScheduledAt.After(now) {
			return invalid("scheduled_at must be in the future")
		}
	} else if t.ScheduledAt != nil {
		return invalid("scheduled_at only applies to upcoming")
	}
	if strings.TrimSpace(t.OverrideReason) != "" && from != ReleaseHeld {
		return invalid("override_reason only applies to a held_review release")
	}
	if from == t.To && from != ReleaseUpcoming {
		return conflict("release is already %s", from)
	}
	if !slices.Contains(allowedTransitions[from], t.To) {
		return conflict("cannot move a %s release to %s", orUnknown(from), t.To)
	}
	if from == ReleaseHeld && t.To == ReleasePublished && strings.TrimSpace(t.OverrideReason) == "" {
		return invalid("override_reason is required to publish a held_review release")
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown status)"
	}
	return s
}

// ---------------------------------------------------------------- release patch

// ColumnEdit sets one releases column. Value is the text form cast with
// ::SQLType in SQL (nil = NULL); Array is used for text[] columns.
type ColumnEdit struct {
	Field   string // API name as sent
	Column  string
	SQLType string
	Value   *string
	IsArray bool
	Array   []string
}

// ReleasePatch is a validated PATCH /api/admin/releases/{id} body.
type ReleasePatch struct {
	Version int64
	Reason  string
	Columns []ColumnEdit
	Tags    *[]string // nil = unchanged; normalized, deduplicated
}

// Empty reports whether the patch changes nothing.
func (p ReleasePatch) Empty() bool { return len(p.Columns) == 0 && p.Tags == nil }

// patchExtra are the editable release fields that are not crawled columns.
var patchExtra = map[string]bool{"tags": true, "age_rating": true, "region_allow": true, "region_deny": true, "featured_note": true}

var regionRE = regexp.MustCompile(`^[A-Z]{2}$`)

// ParseReleasePatch validates a PATCH body. Accepted keys: title, title_ja,
// synopsis, release_date (YYYY-MM-DD), shoot_year, runtime_minutes (or
// runtime), series, director, maker, label, tags ([]string), age_rating
// (all|13|16|18), region_allow / region_deny ([]ISO alpha-2), featured_note,
// plus version (optimistic lock) and reason. null clears a nullable field.
func ParseReleasePatch(body map[string]json.RawMessage) (ReleasePatch, error) {
	var p ReleasePatch
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]string{}
	for _, k := range keys {
		raw := body[k]
		switch {
		case k == "version":
			if err := json.Unmarshal(raw, &p.Version); err != nil || p.Version < 0 {
				return p, invalid("version must be a non-negative integer")
			}
			continue
		case k == "reason":
			if err := json.Unmarshal(raw, &p.Reason); err != nil {
				return p, invalid("reason must be a string")
			}
			if len(p.Reason) > MaxReasonLen {
				return p, invalid("reason too long")
			}
			continue
		case k == "tags":
			tags, err := parseTags(raw)
			if err != nil {
				return p, err
			}
			p.Tags = &tags
			continue
		}
		var edit ColumnEdit
		var err error
		if patchExtra[k] {
			edit, err = parseExtraField(k, raw)
		} else {
			spec, ok := ConflictField("release", k)
			if !ok {
				return p, invalid("unknown or read-only field %q", k)
			}
			var v *string
			if v, err = CoerceFieldValue(spec, raw); err == nil {
				edit = ColumnEdit{Field: k, Column: spec.Column, SQLType: spec.Kind.sqlType(), Value: v}
			}
		}
		if err != nil {
			return p, err
		}
		if prev, dup := seen[edit.Column]; dup {
			return p, invalid("%q and %q set the same field", prev, k)
		}
		seen[edit.Column] = k
		p.Columns = append(p.Columns, edit)
	}
	if p.Empty() {
		return p, invalid("no fields to update")
	}
	return p, nil
}

func parseExtraField(k string, raw json.RawMessage) (ColumnEdit, error) {
	edit := ColumnEdit{Field: k, Column: k, SQLType: "text"}
	null := strings.TrimSpace(string(raw)) == "null"
	switch k {
	case "age_rating":
		if null {
			return edit, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			var n json.Number
			if json.Unmarshal(raw, &n) != nil {
				return edit, invalid("age_rating must be one of %s", strings.Join(AgeRatings, ", "))
			}
			s = n.String()
		}
		s = strings.ToLower(strings.TrimSpace(s))
		if !slices.Contains(AgeRatings, s) {
			return edit, invalid("age_rating must be one of %s", strings.Join(AgeRatings, ", "))
		}
		edit.Value = &s
	case "featured_note":
		if null {
			return edit, nil
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return edit, invalid("featured_note must be a string")
		}
		if len(s) > MaxReasonLen {
			return edit, invalid("featured_note too long")
		}
		if s = strings.TrimSpace(s); s != "" {
			edit.Value = &s
		}
	case "region_allow", "region_deny":
		edit.IsArray, edit.SQLType = true, "text[]"
		if null {
			return edit, nil
		}
		var list []string
		if err := json.Unmarshal(raw, &list); err != nil {
			return edit, invalid("%s must be a list of ISO 3166-1 alpha-2 codes", k)
		}
		regions, err := NormalizeRegions(k, list)
		if err != nil {
			return edit, err
		}
		edit.Array = regions
	}
	return edit, nil
}

// NormalizeRegions upper-cases, validates and deduplicates ISO 3166-1
// alpha-2 codes. An empty list is nil (no restriction).
func NormalizeRegions(field string, list []string) ([]string, error) {
	if len(list) > 250 {
		return nil, invalid("%s has too many entries", field)
	}
	var out []string
	for _, r := range list {
		r = strings.ToUpper(strings.TrimSpace(r))
		if !regionRE.MatchString(r) {
			return nil, invalid("%s: %q is not an ISO 3166-1 alpha-2 code", field, r)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

func parseTags(raw json.RawMessage) ([]string, error) {
	var list []string
	if strings.TrimSpace(string(raw)) != "null" {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, invalid("tags must be a list of strings")
		}
	}
	if len(list) > 100 {
		return nil, invalid("too many tags")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range list {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > 100 {
			return nil, invalid("tag too long: %q", t)
		}
		n := catalog.Slug(t)
		if n == "" {
			return nil, invalid("tag %q has no usable characters", t)
		}
		if !seen[n] {
			seen[n] = true
			out = append(out, t)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- age at filming

// FilmingAge is a performer's age at filming (PRD 6.4).
type FilmingAge struct {
	Age    int    `json:"age"`
	Basis  string `json:"basis"`  // shoot_year | release_year
	Approx bool   `json:"approx"` // only a release date: shown as 「约 N 岁」
}

// AgeAtFilming is shoot year − birth year, or release year − birth year as
// an approximation when there is no shoot year. nil when the birth date is
// unknown or neither year is known (the line is hidden, PRD 6.4.3).
func AgeAtFilming(birth *time.Time, shootYear int, releaseDate *time.Time) *FilmingAge {
	if birth == nil {
		return nil
	}
	switch {
	case shootYear > 0:
		return &FilmingAge{Age: shootYear - birth.Year(), Basis: "shoot_year"}
	case releaseDate != nil:
		return &FilmingAge{Age: releaseDate.Year() - birth.Year(), Basis: "release_year", Approx: true}
	}
	return nil
}

// ---------------------------------------------------------------- playback sources

// SourceInput is a playback source as the admin API creates or edits it.
type SourceInput struct {
	Kind            string   `json:"kind"`
	Provider        string   `json:"provider"`
	Label           string   `json:"label"`
	URL             *string  `json:"url"`
	StoragePrefix   *string  `json:"storage_prefix"`
	MaxHeight       *int     `json:"max_height"`
	HasHDR          bool     `json:"has_hdr"`
	DurationSeconds *int     `json:"duration_seconds"`
	AudioLangs      []string `json:"audio_langs"`
	SubtitleLangs   []string `json:"subtitle_langs"`
	VersionKind     string   `json:"version_kind"`
	Priority        *int     `json:"priority"`
	RegionAllow     []string `json:"region_allow"`
	RegionDeny      []string `json:"region_deny"`
	LicenseRef      *string  `json:"license_ref"`
}

// RequiresLicense reports whether a source of kind must carry a license_ref
// (PRD 15 版权凭证): everything except official previews and plain external
// links, which play on the licensor's own site.
func RequiresLicense(kind string) bool {
	return kind != KindOfficialPreview && kind != KindExternalLink
}

var langRE = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)

// Normalize validates in place and fills defaults (version_kind main,
// priority 100). Errors are *ValidationError.
func (in *SourceInput) Normalize() error {
	in.Kind = strings.TrimSpace(in.Kind)
	if !ValidPlaybackKind(in.Kind) {
		return invalid("kind must be official_embed, official_preview, external_link or hls_owned")
	}
	in.Provider, in.Label = strings.TrimSpace(in.Provider), strings.TrimSpace(in.Label)
	if in.Provider == "" || len(in.Provider) > 100 {
		return invalid("provider is required (at most 100 characters)")
	}
	if in.Label == "" || len(in.Label) > 100 {
		return invalid("label is required (at most 100 characters)")
	}
	in.URL = trimOptional(in.URL)
	in.StoragePrefix = trimOptional(in.StoragePrefix)
	in.LicenseRef = trimOptional(in.LicenseRef)
	if in.URL != nil {
		u, err := url.Parse(*in.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(*in.URL) > 2048 {
			return invalid("url must be an absolute http(s) URL")
		}
	}
	if in.Kind == KindHLSOwned {
		if in.StoragePrefix == nil {
			return invalid("storage_prefix is required for hls_owned")
		}
	} else if in.URL == nil {
		return invalid("url is required for %s", in.Kind)
	}
	if in.StoragePrefix != nil && len(*in.StoragePrefix) > 1024 {
		return invalid("storage_prefix too long")
	}
	if RequiresLicense(in.Kind) && in.LicenseRef == nil {
		return invalid("license_ref is required for %s sources", in.Kind)
	}
	if in.LicenseRef != nil && len(*in.LicenseRef) > 200 {
		return invalid("license_ref too long")
	}
	if in.MaxHeight != nil && (*in.MaxHeight < 1 || *in.MaxHeight > 4320) {
		return invalid("max_height must be between 1 and 4320")
	}
	if in.DurationSeconds != nil && (*in.DurationSeconds < 1 || *in.DurationSeconds > 86400) {
		return invalid("duration_seconds must be between 1 and 86400")
	}
	var err error
	if in.AudioLangs, err = normalizeLangs("audio_langs", in.AudioLangs); err != nil {
		return err
	}
	if in.SubtitleLangs, err = normalizeLangs("subtitle_langs", in.SubtitleLangs); err != nil {
		return err
	}
	if in.VersionKind == "" {
		in.VersionKind = VersionMain
	}
	if !ValidVersionKind(in.VersionKind) {
		return invalid("version_kind must be main, high_quality, dub or alt")
	}
	if in.Priority == nil {
		p := 100
		in.Priority = &p
	}
	if *in.Priority < 0 || *in.Priority > 100000 {
		return invalid("priority must be between 0 and 100000")
	}
	if in.RegionAllow, err = NormalizeRegions("region_allow", in.RegionAllow); err != nil {
		return err
	}
	if in.RegionDeny, err = NormalizeRegions("region_deny", in.RegionDeny); err != nil {
		return err
	}
	return nil
}

func trimOptional(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if v == "" {
		return nil
	}
	return &v
}

func normalizeLangs(field string, list []string) ([]string, error) {
	if len(list) > 20 {
		return nil, invalid("%s has too many entries", field)
	}
	out := []string{}
	for _, l := range list {
		l = strings.TrimSpace(l)
		if !langRE.MatchString(l) || len(l) > 35 {
			return nil, invalid("%s: %q is not a language tag", field, l)
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	return out, nil
}

// ApplySourcePatch overlays a partial JSON object on in (only the keys
// present change; null clears an optional field). Unknown keys — including
// status, which only enable / disable change — are rejected. The result is
// normalized again, so cross-field rules (license_ref for the kind) hold.
func ApplySourcePatch(in *SourceInput, patch json.RawMessage) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(patch, &keys); err != nil {
		return invalid("invalid JSON body")
	}
	if len(keys) == 0 {
		return invalid("no fields to update")
	}
	dec := json.NewDecoder(strings.NewReader(string(patch)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		return invalid("invalid source patch: %v", err)
	}
	return in.Normalize()
}

// ---------------------------------------------------------------- release performers

// Release performer roles. role=lead sets is_primary (主演); guest is a 客串.
const (
	RoleLead  = "lead"
	RoleGuest = "guest"
)

// ReleasePerformerInput credits a performer on a release.
type ReleasePerformerInput struct {
	PerformerID  int64   `json:"-"`
	Role         string  `json:"role"`
	BillingOrder *int    `json:"billing_order"`
	CreditedName *string `json:"credited_name"`
	Reason       string  `json:"reason"`
}

// Normalize validates the credit.
func (in *ReleasePerformerInput) Normalize() error {
	in.Role = strings.ToLower(strings.TrimSpace(in.Role))
	if in.Role != RoleLead && in.Role != RoleGuest {
		return invalid("role must be lead or guest")
	}
	if in.PerformerID <= 0 {
		return invalid("invalid performer id")
	}
	if in.BillingOrder != nil && (*in.BillingOrder < 0 || *in.BillingOrder > 1000) {
		return invalid("billing_order must be between 0 and 1000")
	}
	in.CreditedName = trimOptional(in.CreditedName)
	if in.CreditedName != nil && len(*in.CreditedName) > 200 {
		return invalid("credited_name too long")
	}
	if len(in.Reason) > MaxReasonLen {
		return invalid("reason too long")
	}
	return nil
}
