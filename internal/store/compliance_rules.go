package store

// Pure validation for the compliance features (PRD 15; migration 041):
// consent records, legal documents, feedback and copyright takedowns. No
// I/O; the DB side is consent.go, legal.go, feedback.go and takedown.go.

import (
	"avxxx/internal/i18n"
	"crypto/rand"
	"encoding/base32"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------- consent

// Consent categories (PRD 14.1 `consent` field; CookieBanner).
const (
	ConsentNecessary = "necessary"
	ConsentAnalytics = "analytics"
	ConsentAds       = "personalized_ads"
	ConsentRecs      = "personalized_recs"
)

// ConsentSources say where a choice was made: the first-visit banner, the
// preferences sheet, a cross-device sync of a logged-in user's choice, or a
// GPC signal applied on top of an earlier choice.
var ConsentSources = []string{"banner", "preferences", "sync", "gpc"}

// deviceIDPattern is the browser's random consent id (a UUID or similar);
// it is only ever stored as a keyed hash.
var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// ConsentInput is one consent choice as the browser sends it.
type ConsentInput struct {
	Analytics        bool   `json:"analytics"`
	PersonalizedAds  bool   `json:"personalized_ads"`
	PersonalizedRecs bool   `json:"personalized_recs"`
	GPC              bool   `json:"gpc"`
	Source           string `json:"source"`
	DeviceID         string `json:"device_id"`
}

// Normalize validates the input and applies GPC: a Global Privacy Control
// signal always turns personalized ads off (PRD 14.5), whatever was sent.
func (in *ConsentInput) Normalize() error {
	in.Source = strings.TrimSpace(in.Source)
	in.DeviceID = strings.TrimSpace(in.DeviceID)
	if in.Source == "" {
		in.Source = "preferences"
	}
	if !slices.Contains(ConsentSources, in.Source) {
		return invalid("source must be one of %s", strings.Join(ConsentSources, ", "))
	}
	if in.DeviceID != "" && !deviceIDPattern.MatchString(in.DeviceID) {
		return invalid("device_id must be 16-128 letters, digits, - or _")
	}
	if in.GPC {
		in.PersonalizedAds = false
	}
	return nil
}

// Categories is the stored category list; necessary is always included.
func (in ConsentInput) Categories() []string {
	out := []string{ConsentNecessary}
	if in.Analytics {
		out = append(out, ConsentAnalytics)
	}
	if in.PersonalizedAds && !in.GPC {
		out = append(out, ConsentAds)
	}
	if in.PersonalizedRecs {
		out = append(out, ConsentRecs)
	}
	return out
}

// ConsentState is a stored choice as returned to its owner.
type ConsentState struct {
	Analytics        bool      `json:"analytics"`
	PersonalizedAds  bool      `json:"personalized_ads"`
	PersonalizedRecs bool      `json:"personalized_recs"`
	GPC              bool      `json:"gpc"`
	Categories       []string  `json:"categories"`
	PolicyVersion    int       `json:"policy_version"`
	Source           string    `json:"source"`
	SavedAt          time.Time `json:"saved_at"`
}

// consentStateOf expands a stored category list.
func consentStateOf(categories []string, gpc bool, version int, source string, at time.Time) ConsentState {
	return ConsentState{
		Analytics:        slices.Contains(categories, ConsentAnalytics),
		PersonalizedAds:  slices.Contains(categories, ConsentAds) && !gpc,
		PersonalizedRecs: slices.Contains(categories, ConsentRecs),
		GPC:              gpc,
		Categories:       categories,
		PolicyVersion:    version,
		Source:           source,
		SavedAt:          at,
	}
}

// ---------------------------------------------------------------- legal documents

// LegalKinds are the document kinds (legal_documents.kind; LegalPage).
var LegalKinds = []string{"terms", "privacy", "cookies", "copyright", "community", "ads", "performer-info"}

// ValidLegalKind reports whether k is one of LegalKinds.
func ValidLegalKind(k string) bool { return slices.Contains(LegalKinds, k) }

// LegalDefaultLang is the language every version is written in first.
const LegalDefaultLang = i18n.ZhCN

// LegalLangChain is the order languages are tried for a document request:
// the requested language, its PRD 8 fallbacks, then zh-CN (the drafting
// language, which every version has) and en.
func LegalLangChain(lang string) []string {
	chain := i18n.FallbackChain(lang)
	for _, l := range []string{LegalDefaultLang, i18n.En} {
		if !slices.Contains(chain, l) {
			chain = append(chain, l)
		}
	}
	return chain
}

const (
	maxLegalTitle   = 200
	maxLegalBody    = 200_000
	maxLegalSummary = 10
)

// LegalDraftInput creates or edits a draft. On create, Version 0 means "the
// next version of this kind"; an existing version number adds a translation
// of that version.
type LegalDraftInput struct {
	Kind        string     `json:"kind"`
	Lang        string     `json:"lang"`
	Version     int        `json:"version"`
	Title       string     `json:"title"`
	Summary     []string   `json:"summary"`
	Body        string     `json:"body"`
	EffectiveAt *time.Time `json:"effective_at"`
	ChangeNote  string     `json:"change_note"`
}

// Normalize validates a draft. kind, lang and version are only read on
// create (an edit keeps the row's).
func (in *LegalDraftInput) Normalize(create bool) error {
	in.Title = strings.TrimSpace(in.Title)
	in.ChangeNote = strings.TrimSpace(in.ChangeNote)
	in.Body = strings.ReplaceAll(in.Body, "\r\n", "\n")
	if create {
		in.Kind = strings.TrimSpace(in.Kind)
		if !ValidLegalKind(in.Kind) {
			return invalid("kind must be one of %s", strings.Join(LegalKinds, ", "))
		}
		lang := i18n.Normalize(strings.TrimSpace(in.Lang))
		if lang == "" {
			return invalid("lang must be one of %s", strings.Join(i18n.Languages, ", "))
		}
		in.Lang = lang
		if in.Version < 0 {
			return invalid("version must be positive")
		}
	}
	summary := make([]string, 0, len(in.Summary))
	for _, s := range in.Summary {
		if s = strings.TrimSpace(s); s != "" {
			summary = append(summary, s)
		}
	}
	in.Summary = summary
	switch {
	case in.Title == "" || utf8.RuneCountInString(in.Title) > maxLegalTitle:
		return invalid("title is required (at most %d characters)", maxLegalTitle)
	case strings.TrimSpace(in.Body) == "" || len(in.Body) > maxLegalBody:
		return invalid("body is required (at most %d bytes)", maxLegalBody)
	case len(in.Summary) > maxLegalSummary:
		return invalid("at most %d summary lines", maxLegalSummary)
	case utf8.RuneCountInString(in.ChangeNote) > MaxReasonLen:
		return invalid("change_note too long")
	}
	for _, s := range in.Summary {
		if utf8.RuneCountInString(s) > 500 {
			return invalid("summary lines are at most 500 characters")
		}
	}
	return nil
}

// DiffLine is one line of a line diff: Op is "=", "-" (only in the base)
// or "+" (only in the new text).
type DiffLine struct {
	Op   string `json:"op"`
	Text string `json:"text"`
}

// maxDiffCells bounds the LCS table; larger inputs diff as replace-all.
const maxDiffCells = 4_000_000

// LineDiff is a longest-common-subsequence line diff of base -> next.
func LineDiff(base, next string) []DiffLine {
	a, b := splitLines(base), splitLines(next)
	n, m := len(a), len(b)
	out := []DiffLine{}
	if n*m > maxDiffCells {
		for _, l := range a {
			out = append(out, DiffLine{"-", l})
		}
		for _, l := range b {
			out = append(out, DiffLine{"+", l})
		}
		return out
	}
	// lcs[i][j] = LCS length of a[i:] and b[j:].
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, DiffLine{"=", a[i]})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"-", a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"+", b[j]})
	}
	return out
}

func splitLines(s string) []string {
	s = strings.TrimRight(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ---------------------------------------------------------------- feedback

// FeedbackCategories (feedback.category).
var FeedbackCategories = []string{"bug", "playback", "content", "account", "suggestion", "partnership", "other"}

// FeedbackStatuses (feedback.status).
var FeedbackStatuses = []string{"open", "in_progress", "resolved", "dismissed"}

const maxFeedbackMessage = 4000

// FeedbackInput is the public feedback form.
type FeedbackInput struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	Email    string `json:"email"`
	PageURL  string `json:"page_url"`
	Lang     string `json:"lang"`
}

// Normalize validates the form. The page URL keeps only scheme, host and
// path: query strings and fragments can carry tokens or search terms.
func (in *FeedbackInput) Normalize() error {
	in.Category = strings.TrimSpace(in.Category)
	in.Message = strings.TrimSpace(in.Message)
	in.Lang = i18n.Normalize(strings.TrimSpace(in.Lang))
	if !slices.Contains(FeedbackCategories, in.Category) {
		return invalid("category must be one of %s", strings.Join(FeedbackCategories, ", "))
	}
	if in.Message == "" || utf8.RuneCountInString(in.Message) > maxFeedbackMessage {
		return invalid("message is required (at most %d characters)", maxFeedbackMessage)
	}
	var err error
	if in.Email, err = optionalEmail(in.Email); err != nil {
		return err
	}
	if in.PageURL, err = pagePath(in.PageURL); err != nil {
		return err
	}
	return nil
}

// optionalEmail validates an optional bare email address.
func optionalEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	return requiredEmail(s)
}

func requiredEmail(s string) (string, error) {
	s = strings.TrimSpace(s)
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || len(s) > 254 {
		return "", invalid("email is not a valid address")
	}
	return s, nil
}

// pagePath reduces a page URL to scheme://host/path (or /path).
func pagePath(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || len(s) > 2000 {
		return "", invalid("page_url is not a valid URL")
	}
	switch {
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
		return u.EscapedPath(), nil
	case (u.Scheme == "http" || u.Scheme == "https") && u.Host != "":
		return u.Scheme + "://" + u.Host + u.EscapedPath(), nil
	}
	return "", invalid("page_url must be an http(s) URL or a path")
}

// ---------------------------------------------------------------- takedowns

// Takedown statuses (takedown_requests.status).
const (
	TakedownReceived  = "received"
	TakedownReviewing = "reviewing"
	TakedownActioned  = "actioned"
	TakedownRejected  = "rejected"
	TakedownCounter   = "counter_received"
	TakedownRestored  = "restored"
	TakedownClosed    = "closed"
)

// TakedownStatuses lists every status.
var TakedownStatuses = []string{TakedownReceived, TakedownReviewing, TakedownActioned, TakedownRejected, TakedownCounter, TakedownRestored, TakedownClosed}

// TakedownSLA is the PRD 15 deadline for the first action on a notice.
const TakedownSLA = 24 * time.Hour

// manualTakedownTransitions are the status changes an admin makes directly;
// actioned, counter_received and restored are set by the actions, the
// counter-notice form and restore.
var manualTakedownTransitions = map[string][]string{
	TakedownReceived:  {TakedownReviewing, TakedownRejected},
	TakedownReviewing: {TakedownRejected, TakedownClosed},
	TakedownActioned:  {TakedownClosed},
	TakedownCounter:   {TakedownClosed},
	TakedownRestored:  {TakedownClosed},
	TakedownRejected:  {TakedownReviewing},
}

// CheckTakedownTransition validates a manual status change.
func CheckTakedownTransition(from, to string) error {
	if !slices.Contains(TakedownStatuses, to) {
		return invalid("status must be one of %s", strings.Join(TakedownStatuses, ", "))
	}
	if !slices.Contains(manualTakedownTransitions[from], to) {
		return conflict("cannot move a %s ticket to %s", orUnknown(from), to)
	}
	return nil
}

// Takedown actions (POST /api/admin/takedowns/{id}/actions).
const (
	TakedownUnpublishRelease = "unpublish_release"
	TakedownSuppressImage    = "suppress_image"
)

// TakedownActionInput is one admin action on a ticket.
// The release is named by id or by slug.
type TakedownActionInput struct {
	Action       string `json:"action"`
	ReleaseID    int64  `json:"release_id"`
	ReleaseSlug  string `json:"release_slug"`
	MediaAssetID int64  `json:"media_asset_id"`
	Reason       string `json:"reason"`
}

// Normalize validates an action.
func (in *TakedownActionInput) Normalize() error {
	in.Reason = strings.TrimSpace(in.Reason)
	in.ReleaseSlug = strings.TrimSpace(in.ReleaseSlug)
	switch in.Action {
	case TakedownUnpublishRelease:
		if (in.ReleaseID <= 0) == (in.ReleaseSlug == "") || in.MediaAssetID != 0 || len(in.ReleaseSlug) > 300 {
			return invalid("unpublish_release needs release_id or release_slug")
		}
	case TakedownSuppressImage:
		if in.MediaAssetID <= 0 || in.ReleaseID != 0 || in.ReleaseSlug != "" {
			return invalid("suppress_image needs media_asset_id only")
		}
	default:
		return invalid("action must be unpublish_release or suppress_image")
	}
	if utf8.RuneCountInString(in.Reason) > MaxReasonLen {
		return invalid("reason too long")
	}
	return nil
}

const (
	maxTakedownShort = 200
	maxTakedownLong  = 4000
	maxTakedownURLs  = 20
)

// TakedownInput is the public copyright notice (CopyrightForm).
type TakedownInput struct {
	ClaimantName    string   `json:"claimant_name"`
	ClaimantEmail   string   `json:"claimant_email"`
	ClaimantPhone   string   `json:"claimant_phone"`
	ClaimantAddress string   `json:"claimant_address"`
	RightsOwner     string   `json:"rights_owner"`
	Relationship    string   `json:"relationship"`
	WorkDescription string   `json:"work_description"`
	OriginalURLs    []string `json:"original_urls"`
	InfringingURLs  []string `json:"infringing_urls"`
	GoodFaith       bool     `json:"good_faith"`
	Accuracy        bool     `json:"accuracy"`
	Signature       string   `json:"signature"`
	Lang            string   `json:"lang"`
}

// Normalize validates a notice. Every statement a valid notice needs (good
// faith, accuracy under penalty of perjury, a signature) is required.
func (in *TakedownInput) Normalize() error {
	for _, p := range []*string{&in.ClaimantName, &in.ClaimantPhone, &in.ClaimantAddress, &in.RightsOwner, &in.Relationship, &in.WorkDescription, &in.Signature} {
		*p = strings.TrimSpace(*p)
	}
	in.Lang = i18n.Normalize(strings.TrimSpace(in.Lang))
	var err error
	if in.ClaimantEmail, err = requiredEmail(in.ClaimantEmail); err != nil {
		return invalid("claimant_email is not a valid address")
	}
	if in.Relationship == "" {
		in.Relationship = "owner"
	}
	switch {
	case in.ClaimantName == "" || utf8.RuneCountInString(in.ClaimantName) > maxTakedownShort:
		return invalid("claimant_name is required")
	case in.RightsOwner == "" || utf8.RuneCountInString(in.RightsOwner) > maxTakedownShort:
		return invalid("rights_owner is required")
	case in.Relationship != "owner" && in.Relationship != "agent":
		return invalid("relationship must be owner or agent")
	case in.WorkDescription == "" || utf8.RuneCountInString(in.WorkDescription) > maxTakedownLong:
		return invalid("work_description is required")
	case utf8.RuneCountInString(in.ClaimantPhone) > 50 || utf8.RuneCountInString(in.ClaimantAddress) > 500:
		return invalid("phone or address too long")
	case !in.GoodFaith || !in.Accuracy:
		return invalid("the good-faith and accuracy statements are required")
	case in.Signature == "" || utf8.RuneCountInString(in.Signature) > maxTakedownShort:
		return invalid("signature is required")
	}
	if in.InfringingURLs, err = httpURLs(in.InfringingURLs, "infringing_urls", true); err != nil {
		return err
	}
	if in.OriginalURLs, err = httpURLs(in.OriginalURLs, "original_urls", false); err != nil {
		return err
	}
	return nil
}

func httpURLs(list []string, name string, required bool) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, l := range list {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		u, err := url.Parse(l)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(l) > 1000 {
			return nil, invalid("%s must be http(s) URLs", name)
		}
		if !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	if required && len(out) == 0 {
		return nil, invalid("%s needs at least one URL", name)
	}
	if len(out) > maxTakedownURLs {
		return nil, invalid("at most %d %s", maxTakedownURLs, name)
	}
	return out, nil
}

// CounterNoticeInput is the counter-notice on an actioned ticket.
type CounterNoticeInput struct {
	Name      string `json:"name"`
	Email     string `json:"email"`
	Address   string `json:"address"`
	Statement string `json:"statement"`
	Consent   bool   `json:"consent"`
	Signature string `json:"signature"`
}

// Normalize validates a counter-notice: a statement that the material was
// removed by mistake, consent to jurisdiction and a signature are required.
func (in *CounterNoticeInput) Normalize() error {
	for _, p := range []*string{&in.Name, &in.Address, &in.Statement, &in.Signature} {
		*p = strings.TrimSpace(*p)
	}
	var err error
	if in.Email, err = requiredEmail(in.Email); err != nil {
		return err
	}
	switch {
	case in.Name == "" || utf8.RuneCountInString(in.Name) > maxTakedownShort:
		return invalid("name is required")
	case in.Address == "" || utf8.RuneCountInString(in.Address) > 500:
		return invalid("address is required")
	case in.Statement == "" || utf8.RuneCountInString(in.Statement) > maxTakedownLong:
		return invalid("statement is required")
	case !in.Consent:
		return invalid("consent to jurisdiction is required")
	case in.Signature == "" || utf8.RuneCountInString(in.Signature) > maxTakedownShort:
		return invalid("signature is required")
	}
	return nil
}

// ticketAlphabet is Crockford base32 (no I, L, O, U): easy to read aloud.
var ticketEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// NewTicket returns a random, non-guessable ticket number (80 bits):
// TD-XXXX-XXXX-XXXX-XXXX.
func NewTicket() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	s := ticketEncoding.EncodeToString(b) // 16 chars
	return "TD-" + s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16], nil
}

var ticketPattern = regexp.MustCompile(`^TD-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}-[0-9A-HJKMNP-TV-Z]{4}$`)

// NormalizeTicket upper-cases a typed ticket number and reports whether it
// has the NewTicket shape.
func NormalizeTicket(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s, ticketPattern.MatchString(s)
}
