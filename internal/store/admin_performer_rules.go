package store

// Pure validation for the admin performer editor (AdminActors / A9) and the
// performer information requests (PRD 15 演员个人信息). No I/O; the DB side is
// admin_performer.go.

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------- privacy

// PublicBirthDate renders the birth date of the performers row aliased
// alias for PUBLIC responses: NULL while birth_date_hidden is set (PRD 15:
// a hidden birthday shows no birth date and no age at filming). Every public
// query that exposes, compares or derives anything from a birth date must
// use it, so a hidden birthday also falls out of any age filter (NULL never
// satisfies a comparison). Internal readers — R-AGE (releaseFactsSelect),
// identity scoring and the admin API — keep reading the real birth_date:
// the under-18 safety rule must never be weakened by a privacy request.
func PublicBirthDate(alias string) string {
	return "CASE WHEN " + BirthHiddenSQL(alias) + " THEN NULL ELSE " + alias + ".birth_date END"
}

// BirthHiddenSQL is true when the birthday of performer alias is hidden:
// its own flag, or the flag of a performer merged into it (an identity
// merge keeps the survivor's columns, so a duplicate whose birthday was
// hidden on request must keep it hidden on the survivor, and undoing the
// merge needs no column restore).
func BirthHiddenSQL(alias string) string {
	return "(" + alias + ".birth_date_hidden OR EXISTS (SELECT 1 FROM performers hm WHERE hm.merged_into_id=" + alias + ".id AND hm.birth_date_hidden))"
}

// publicDebutAge hides debut_age with the birthday (birth year = debut year
// − debut age).
func publicDebutAge(alias string) string {
	return "CASE WHEN " + BirthHiddenSQL(alias) + " THEN NULL ELSE " + alias + ".debut_age END"
}

// PublicFilmingAge is AgeAtFilming for public responses: nil while the
// performer's birthday is hidden.
func PublicFilmingAge(birth *time.Time, hidden bool, shootYear int, releaseDate *time.Time) *FilmingAge {
	if hidden {
		return nil
	}
	return AgeAtFilming(birth, shootYear, releaseDate)
}

// ---------------------------------------------------------------- status

// Performer statuses (performers.status has no CHECK constraint).
const (
	PerformerActive = "active"
	PerformerHidden = "hidden"
)

// PerformerStatusChange is a hide / unhide request.
type PerformerStatusChange struct {
	To      string `json:"status"`
	Reason  string `json:"reason"`
	Version int64  `json:"version"`
}

// CheckPerformerStatus validates a status change from the current status.
// Hiding needs a reason (it is shown in the admin list); a no-op change is a
// conflict.
func CheckPerformerStatus(from string, c PerformerStatusChange) error {
	if c.To != PerformerActive && c.To != PerformerHidden {
		return invalid("status must be %s or %s", PerformerActive, PerformerHidden)
	}
	if len(c.Reason) > MaxReasonLen {
		return invalid("reason too long")
	}
	if c.To == PerformerHidden && strings.TrimSpace(c.Reason) == "" {
		return invalid("reason is required to hide a performer")
	}
	if from == c.To {
		return conflict("performer is already %s", c.To)
	}
	return nil
}

// ---------------------------------------------------------------- patch

// PerformerPatch is a validated PATCH /api/admin/performers/{id} body.
type PerformerPatch struct {
	Version         int64
	Reason          string
	Columns         []ColumnEdit
	BirthDateHidden *bool // not a crawled field: no override
}

// Empty reports whether the patch changes nothing.
func (p PerformerPatch) Empty() bool { return len(p.Columns) == 0 && p.BirthDateHidden == nil }

// TouchesBirthDate reports whether the patch edits birth_date (R-AGE must
// re-run on every release of the performer).
func (p PerformerPatch) TouchesBirthDate() bool {
	return slices.ContainsFunc(p.Columns, func(c ColumnEdit) bool { return c.Column == "birth_date" })
}

// ParsePerformerPatch validates a PATCH body. Accepted keys: the reviewable
// performer columns (canonical_name or name, canonical_name_ja or name_ja,
// birth_date, birth_place, debut_date, height_cm, cup_size, shoe_size,
// biography), birth_date_hidden (bool), plus version and reason. null clears
// a nullable field.
func ParsePerformerPatch(body map[string]json.RawMessage) (PerformerPatch, error) {
	var p PerformerPatch
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	seen := map[string]string{}
	for _, k := range keys {
		raw := body[k]
		switch k {
		case "version":
			if err := json.Unmarshal(raw, &p.Version); err != nil || p.Version < 0 {
				return p, invalid("version must be a non-negative integer")
			}
			continue
		case "reason":
			if err := json.Unmarshal(raw, &p.Reason); err != nil {
				return p, invalid("reason must be a string")
			}
			if len(p.Reason) > MaxReasonLen {
				return p, invalid("reason too long")
			}
			continue
		case "birth_date_hidden":
			var b bool
			if err := json.Unmarshal(raw, &b); err != nil {
				return p, invalid("birth_date_hidden must be a boolean")
			}
			p.BirthDateHidden = &b
			continue
		}
		edit, err := performerColumnEdit(k, raw)
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

// performerColumnEdit validates one reviewable performer field.
func performerColumnEdit(field string, raw json.RawMessage) (ColumnEdit, error) {
	spec, ok := ConflictField("performer", field)
	if !ok {
		return ColumnEdit{}, invalid("unknown or read-only field %q", field)
	}
	v, err := CoerceFieldValue(spec, raw)
	if err != nil {
		return ColumnEdit{}, err
	}
	if spec.Column == "canonical_name" && v != nil && utf8.RuneCountInString(*v) > 200 {
		return ColumnEdit{}, invalid("canonical_name too long")
	}
	if spec.Column == "birth_date" && v != nil {
		t, _ := time.Parse(time.DateOnly, *v)
		if t.Year() < 1900 || t.After(time.Now()) {
			return ColumnEdit{}, invalid("birth_date must be between 1900-01-01 and today")
		}
	}
	return ColumnEdit{Field: field, Column: spec.Column, SQLType: spec.Kind.sqlType(), Value: v}, nil
}

// ---------------------------------------------------------------- aliases

// AliasInput is an alias to add to a performer.
type AliasInput struct {
	Name      string `json:"name"`
	Language  string `json:"language"`
	AliasType string `json:"alias_type"`
	Reason    string `json:"reason"`
}

// AliasTypes are the alias_type values the admin may set ('merged' is
// reserved for identity merges).
var AliasTypes = []string{"stage", "birth", "romanized", "former", "other"}

// Normalize trims and validates the input.
func (a *AliasInput) Normalize() error {
	a.Name = strings.Join(strings.Fields(a.Name), " ")
	a.Language = strings.TrimSpace(a.Language)
	a.AliasType = strings.TrimSpace(a.AliasType)
	if a.AliasType == "" {
		a.AliasType = "stage"
	}
	switch {
	case a.Name == "":
		return invalid("name is required")
	case utf8.RuneCountInString(a.Name) > 200:
		return invalid("name too long")
	case len(a.Language) > 16:
		return invalid("language too long")
	case !slices.Contains(AliasTypes, a.AliasType):
		return invalid("alias_type must be one of %s", strings.Join(AliasTypes, ", "))
	case len(a.Reason) > MaxReasonLen:
		return invalid("reason too long")
	}
	return nil
}

// ---------------------------------------------------------------- list filters

// Admin performer list filters (AdminActors).
const (
	PerformerFilterIncomplete   = "incomplete"          // 信息不全: any completeness field missing
	PerformerFilterMissingBirth = "missing_birth"       // 缺生日
	PerformerFilterMissingPhoto = "missing_photo"       // 缺照片
	PerformerFilterUpcoming     = "upcoming"            // 有即将上线作品
	PerformerFilterBirthHidden  = "birth_hidden"        // 生日已隐藏
	PerformerFilterDuplicate    = "suspected_duplicate" // 疑似重复: pending identity candidate
	PerformerFilterHidden       = "hidden"              // status hidden
	PerformerFilterNameIssue    = "name_issue"          // open R-PERFORMER-NAME issue
)

// PerformerFilters lists every filter, in the order the list counts them.
var PerformerFilters = []string{PerformerFilterIncomplete, PerformerFilterMissingBirth, PerformerFilterMissingPhoto,
	PerformerFilterUpcoming, PerformerFilterBirthHidden, PerformerFilterDuplicate, PerformerFilterHidden, PerformerFilterNameIssue}

// CompletenessFields are the fields 信息完整度 is measured on.
var CompletenessFields = []string{"canonical_name_ja", "birth_date", "birth_place", "debut_date", "height_cm", "photo"}

// performerFilterSQL is the WHERE condition of each filter on performers p.
var performerFilterSQL = map[string]string{
	PerformerFilterIncomplete:   "(p.canonical_name_ja IS NULL OR p.birth_date IS NULL OR p.birth_place IS NULL OR p.debut_date IS NULL OR p.height_cm IS NULL OR NOT " + hasPhotoSQL + ")",
	PerformerFilterMissingBirth: "p.birth_date IS NULL",
	PerformerFilterMissingPhoto: "NOT " + hasPhotoSQL,
	PerformerFilterUpcoming:     "EXISTS (SELECT 1 FROM release_performers rp JOIN releases r ON r.id=rp.release_id WHERE rp.performer_id=p.id AND " + upcomingReleaseSQL + ")",
	PerformerFilterBirthHidden:  BirthHiddenSQL("p"),
	PerformerFilterDuplicate:    "EXISTS (SELECT 1 FROM identity_candidates ic WHERE ic.entity_type='performer' AND ic.status='pending' AND (ic.left_id=p.id OR ic.right_id=p.id))",
	PerformerFilterHidden:       "p.status='" + PerformerHidden + "'",
	PerformerFilterNameIssue:    "EXISTS (SELECT 1 FROM audit_issues ai WHERE ai.entity_type='performer' AND ai.entity_id=p.id AND ai.rule='R-PERFORMER-NAME' AND ai.status='open')",
}

// hasPhotoSQL: an unsuppressed avatar or gallery photo exists.
const hasPhotoSQL = "EXISTS (SELECT 1 FROM media_assets ma WHERE ma.entity_type='performer' AND ma.entity_id=p.id AND ma.asset_type IN ('avatar','gallery') AND ma.suppressed_at IS NULL)"

// upcomingReleaseSQL: release r is scheduled (upcoming) or dated in the
// future and not taken down or held.
const upcomingReleaseSQL = "r.merged_into_id IS NULL AND (r.status='upcoming' OR (r.status IN ('published','draft') AND r.release_date > current_date))"

// ValidPerformerFilter reports whether f is a known list filter.
func ValidPerformerFilter(f string) bool { _, ok := performerFilterSQL[f]; return ok }

// Completeness returns the 0–100 completeness score and the missing fields.
func Completeness(have map[string]bool) (int, []string) {
	missing := []string{}
	for _, f := range CompletenessFields {
		if !have[f] {
			missing = append(missing, f)
		}
	}
	return (len(CompletenessFields) - len(missing)) * 100 / len(CompletenessFields), missing
}

// ---------------------------------------------------------------- requests

// Performer request kinds and statuses (migration 022).
const (
	RequestCorrectField  = "correct_field"
	RequestHideBirthDate = "hide_birth_date"
	RequestRemovePhoto   = "remove_photo"
	RequestOther         = "other"

	RequestOpen     = "open"
	RequestApproved = "approved"
	RequestRejected = "rejected"
)

// RequestKinds lists every request kind.
var RequestKinds = []string{RequestCorrectField, RequestHideBirthDate, RequestRemovePhoto, RequestOther}

// RequestStatuses lists every request status.
var RequestStatuses = []string{RequestOpen, RequestApproved, RequestRejected}

// RequesterRoles: 本人 (self) or 经纪方 (agency); other covers fans and
// third parties, whose requests usually need stronger evidence.
var RequesterRoles = []string{"self", "agency", "other"}

// RequestSLA is how long an admin has to decide a request: privacy requests
// (hide the birthday, remove a photo) within 3 days, corrections within 7.
func RequestSLA(kind string) time.Duration {
	if kind == RequestHideBirthDate || kind == RequestRemovePhoto {
		return 72 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// PerformerRequestInput is the public intake body (POST
// /api/performer-requests). Only performer, kind and contact are required.
type PerformerRequestInput struct {
	Performer string          `json:"performer"` // slug
	Kind      string          `json:"kind"`
	Field     string          `json:"field"` // correct_field only
	Value     json.RawMessage `json:"value"` // correct_field only
	Contact   string          `json:"contact"`
	Role      string          `json:"role"` // self | agency | other
	Message   string          `json:"message"`
	Links     []string        `json:"links"`

	// Set by Normalize for correct_field: the column and its coerced value.
	column string
	value  *string
}

// Normalize validates the intake body. Evidence (role, message, links)
// stays private to admins.
func (in *PerformerRequestInput) Normalize() error {
	in.Performer = strings.TrimSpace(in.Performer)
	in.Kind = strings.TrimSpace(in.Kind)
	in.Contact = strings.TrimSpace(in.Contact)
	in.Role = strings.TrimSpace(in.Role)
	in.Message = strings.TrimSpace(in.Message)
	if in.Role == "" {
		in.Role = "other"
	}
	switch {
	case in.Performer == "" || len(in.Performer) > 200:
		return invalid("performer is required")
	case !slices.Contains(RequestKinds, in.Kind):
		return invalid("kind must be one of %s", strings.Join(RequestKinds, ", "))
	case in.Contact == "" || utf8.RuneCountInString(in.Contact) > 200:
		return invalid("contact is required (at most 200 characters)")
	case !slices.Contains(RequesterRoles, in.Role):
		return invalid("role must be one of %s", strings.Join(RequesterRoles, ", "))
	case utf8.RuneCountInString(in.Message) > MaxReasonLen:
		return invalid("message too long")
	case len(in.Links) > 5:
		return invalid("at most 5 links")
	}
	if strings.Contains(in.Contact, "@") {
		if _, err := mail.ParseAddress(in.Contact); err != nil {
			return invalid("contact is not a valid email address")
		}
	}
	for i, l := range in.Links {
		l = strings.TrimSpace(l)
		u, err := url.Parse(l)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(l) > 500 {
			return invalid("links must be http(s) URLs")
		}
		in.Links[i] = l
	}
	if in.Kind == RequestCorrectField {
		in.Field = strings.TrimSpace(in.Field)
		if in.Field == "" {
			return invalid("field is required for correct_field")
		}
		edit, err := performerColumnEdit(in.Field, in.Value)
		if err != nil {
			return err
		}
		in.column, in.value = edit.Column, edit.Value
	} else if in.Field != "" || len(in.Value) > 0 {
		return invalid("field and value are only accepted for correct_field")
	}
	return nil
}

// evidence is the private evidence document stored with the request.
func (in *PerformerRequestInput) evidence() map[string]any {
	links := in.Links
	if links == nil {
		links = []string{}
	}
	return map[string]any{"role": in.Role, "message": in.Message, "links": links}
}

// RequestDecision is an approve / reject body.
type RequestDecision struct {
	Note string `json:"note"`
	// Value overrides the proposed value when approving correct_field.
	Value json.RawMessage `json:"value"`
}

// CheckRequestTransition validates a decision on a request in status from:
// only open requests are decided, once (approved and rejected are final).
// Rejecting needs a note (it is the answer to the requester).
func CheckRequestTransition(from, to string, d RequestDecision) error {
	if to != RequestApproved && to != RequestRejected {
		return invalid("decision must be %s or %s", RequestApproved, RequestRejected)
	}
	if len(d.Note) > MaxReasonLen {
		return invalid("note too long")
	}
	if to == RequestRejected && strings.TrimSpace(d.Note) == "" {
		return invalid("note is required to reject a request")
	}
	if to == RequestRejected && len(d.Value) > 0 {
		return invalid("value is only accepted when approving")
	}
	if from != RequestOpen {
		return conflict("request is already %s", from)
	}
	return nil
}

// requestReason is the review_events reason of a request decision.
func requestReason(id int64, kind, note string) string {
	r := fmt.Sprintf("performer request #%d (%s)", id, kind)
	if strings.TrimSpace(note) != "" {
		r += ": " + strings.TrimSpace(note)
	}
	return r
}
