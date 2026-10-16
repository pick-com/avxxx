package api

// Compliance (PRD 15; migration 041): the store interface and the intake
// rate limits shared by consent.go, legal.go, feedback.go and takedowns.go.

import (
	"avxxx/internal/store"
	"context"
	"net/http"
	"time"
)

// complianceStore is the subset of *store.Store behind the compliance
// routes, as an interface for database-free tests.
type complianceStore interface {
	RecordConsent(ctx context.Context, rec store.ConsentRecord) (store.ConsentState, error)
	LatestConsent(ctx context.Context, userID int64) (store.ConsentState, error)

	CurrentLegalVersion(ctx context.Context, kind string) (store.LegalVersion, error)
	LegalDocument(ctx context.Context, kind, lang string, version int) (store.PublicLegalDocument, error)
	LegalVersions(ctx context.Context, kind, lang string) ([]store.LegalVersionInfo, error)
	AdminLegalDocuments(ctx context.Context, kind, lang string) ([]store.LegalDocument, error)
	LegalDocumentByID(ctx context.Context, id int64) (store.LegalDocument, error)
	CreateLegalDraft(ctx context.Context, in store.LegalDraftInput, actor string) (store.LegalDocument, error)
	UpdateLegalDraft(ctx context.Context, id int64, in store.LegalDraftInput, actor string) (store.LegalDocument, error)
	PublishLegalDocument(ctx context.Context, id int64, effectiveAt *time.Time, note, actor string) (store.LegalDocument, error)
	LegalDocumentDiff(ctx context.Context, id, baseID int64) (store.LegalDiff, error)

	CreateFeedback(ctx context.Context, in store.FeedbackInput) (store.FeedbackReceipt, error)
	AdminFeedback(ctx context.Context, f store.FeedbackFilter) ([]store.Feedback, string, error)
	FeedbackOpenCount(ctx context.Context) (int64, error)
	SetFeedbackStatus(ctx context.Context, id int64, status, note, actor string) (store.Feedback, error)

	CreateTakedown(ctx context.Context, in store.TakedownInput, now time.Time) (store.TakedownReceipt, error)
	TakedownStatus(ctx context.Context, ticket, email string) (store.TakedownPublicStatus, error)
	SubmitCounterNotice(ctx context.Context, ticket string, in store.CounterNoticeInput) (store.TakedownPublicStatus, error)
	AdminTakedowns(ctx context.Context, f store.TakedownFilter) ([]store.Takedown, string, error)
	AdminTakedownCounts(ctx context.Context) (store.TakedownCounts, error)
	AdminTakedown(ctx context.Context, id int64) (store.Takedown, error)
	SetTakedownStatus(ctx context.Context, id int64, to, note, actor string) (store.Takedown, error)
	TakedownAction(ctx context.Context, id int64, in store.TakedownActionInput, actor string) (store.Takedown, error)
	RestoreTakedown(ctx context.Context, id int64, reason, actor string) (store.Takedown, error)
	ReleaseTakenDown(ctx context.Context, slug string) (bool, error)
}

// intakeLimit is one public form's per-address and global hourly limits.
type intakeLimit struct {
	name          string
	perIP, global int
}

const intakeWindow = time.Hour

var (
	consentLimit        = intakeLimit{"consent", 60, 20000}
	feedbackLimit       = intakeLimit{"feedback", 5, 300}
	takedownLimit       = intakeLimit{"takedown", 5, 200}
	takedownLookupLimit = intakeLimit{"takedown_lookup", 30, 3000}
	counterNoticeLimit  = intakeLimit{"counter_notice", 5, 200}
)

// allowIntake applies l to r (429 when exceeded). The address is only a
// limiter key in memory; it is never stored.
func (srv *Server) allowIntake(r *http.Request, l intakeLimit) error {
	if srv.limiter == nil {
		return nil
	}
	ip := srv.clientIP.IP(r)
	for _, k := range []struct {
		key   string
		limit int
	}{{l.name + ":ip:" + ip, l.perIP}, {l.name + ":all", l.global}} {
		ok, err := srv.limiter.Allow(r.Context(), k.key, k.limit, intakeWindow)
		if err != nil {
			return err
		}
		if !ok {
			return tooManyRequests("too many requests, try again later")
		}
	}
	return nil
}

// withCompliance guards a missing store (tests without a database).
func (srv *Server) withCompliance(w http.ResponseWriter) bool {
	if srv.compliance == nil {
		reply(w, nil, errUnavailable)
		return false
	}
	return true
}
