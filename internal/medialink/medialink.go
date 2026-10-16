// Package medialink maps releases to playback provider media ids ahead of
// time (release_media_sources), instead of on the first page view.
//
// The 'media_link' job (cmd/scheduler enqueues it every MEDIA_LINK_EVERY,
// default 1h; the cmd/audit worker runs it, or `audit -media-link`) selects
// published, unmerged releases that have a DVD code, no link for the
// provider yet and no attempt that is still backing off, newest first. For
// each it searches the provider by every DVD code through the media Service
// (no HTTP to our own API), keeps exact code matches (media.ExactCodeCandidates)
// and stores a unique match. Every try is recorded in media_link_attempts,
// which the on-demand fallback in GET /api/releases/{slug}/playback reads and
// writes too, so the two paths never search the same release twice in a
// backoff window.
//
// A release ingested by the crawler needs nothing extra: the next hourly run
// picks it up.
package medialink

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"avxxx/internal/media"
)

// JobKind is jobs.kind of the offline linking job.
const JobKind = "media_link"

// Attempt statuses (media_link_attempts.status).
const (
	StatusFound     = "found"
	StatusNotFound  = "not_found"
	StatusAmbiguous = "ambiguous"
	StatusError     = "error"
)

// KnownStatus reports whether s is an attempt status.
func KnownStatus(s string) bool {
	switch s {
	case StatusFound, StatusNotFound, StatusAmbiguous, StatusError:
		return true
	}
	return false
}

// Providers maps each auto-linkable provider to the base URL its media ids
// are relative to ("video/59813/juq-743" -> https://javtiful.com/video/...).
var Providers = map[string]string{"javtiful": "https://javtiful.com/"}

// MediaURL is the public page URL of a provider media id ("" if unknown).
func MediaURL(provider, mediaID string) string {
	base, ok := Providers[provider]
	if !ok {
		return ""
	}
	return base + strings.TrimPrefix(mediaID, "/")
}

// Attempt is one media_link_attempts row.
type Attempt struct {
	ReleaseID      int64      `json:"release_id"`
	Provider       string     `json:"provider"`
	Status         string     `json:"status"`
	TriedAt        time.Time  `json:"tried_at"`
	NextTryAt      *time.Time `json:"next_try_at"` // nil = never automatically
	Attempts       int        `json:"attempts"`
	MatchedMediaID string     `json:"matched_media_id,omitempty"`
	Candidates     []string   `json:"candidates"`
	LastError      string     `json:"last_error,omitempty"`
}

// Due reports whether a release with attempt a (nil = never tried) may be
// searched again at now.
func Due(a *Attempt, now time.Time) bool {
	if a == nil {
		return true
	}
	return a.NextTryAt != nil && !a.NextTryAt.After(now)
}

// Candidate is a release due for linking.
type Candidate struct {
	ReleaseID int64
	Codes     []string // normalized DVD codes, e.g. "juq-743"
	Prev      *Attempt // previous attempt, nil if never tried
}

// Backoff schedule.
const (
	NotFoundRetry      = 7 * 24 * time.Hour
	NotFoundSlowRetry  = 30 * 24 * time.Hour
	NotFoundSlowAfter  = 3 // misses before the slow retry
	ErrorRetryBase     = 10 * time.Minute
	ErrorRetryMax      = 6 * time.Hour
	RateLimitBackoff   = time.Hour        // whole provider, after a 403/429
	ErrorStreakBackoff = 30 * time.Minute // whole provider, after MaxErrorStreak errors
	MaxErrorStreak     = 3
)

// NextTry is when a release whose latest attempt ended in status, for the
// streak-th consecutive time, is due again. nil = never automatically.
func NextTry(status string, streak int, now time.Time) *time.Time {
	streak = max(streak, 1)
	var d time.Duration
	switch status {
	case StatusNotFound:
		d = NotFoundRetry
		if streak >= NotFoundSlowAfter {
			d = NotFoundSlowRetry
		}
	case StatusError:
		d = ErrorRetryMax
		if streak <= 16 {
			d = min(ErrorRetryBase<<(streak-1), ErrorRetryMax)
		}
	default: // found: nothing to do; ambiguous: an admin decides
		return nil
	}
	t := now.Add(d)
	return &t
}

// Streak is the attempts value after a try ending in status.
func Streak(prev *Attempt, status string) int {
	if prev != nil && prev.Status == status {
		return prev.Attempts + 1
	}
	return 1
}

// CodeResult is one provider search: the exact-match ids for one code, or
// the search error.
type CodeResult struct {
	Code string
	IDs  []string
	Err  error
}

// Decide turns per-code results into an attempt status: the first code with
// exactly one exact match wins; otherwise any code with several makes the
// release ambiguous (candidates = their union); otherwise any search error
// is an error; otherwise not_found.
func Decide(results []CodeResult) (status, mediaID string, candidates []string, err error) {
	var errs []error
	for _, r := range results {
		switch {
		case r.Err != nil:
			errs = append(errs, r.Err)
		case len(r.IDs) == 1:
			return StatusFound, r.IDs[0], r.IDs, nil
		case len(r.IDs) > 1:
			for _, id := range r.IDs {
				if !slices.Contains(candidates, id) {
					candidates = append(candidates, id)
				}
			}
		}
	}
	switch {
	case len(candidates) > 0:
		return StatusAmbiguous, "", candidates, nil
	case len(errs) > 0:
		return StatusError, "", nil, errs[0]
	}
	return StatusNotFound, "", nil, nil
}

// Searcher is the provider search (*media.Service).
type Searcher interface {
	Search(ctx context.Context, source, q string, page int) ([]media.SearchResult, bool, error)
}

// LinkStore is what linking one release needs from the database.
type LinkStore interface {
	// ClaimMediaLink stores mediaID for the release unless another release
	// already holds it; it returns the release that holds it afterwards.
	ClaimMediaLink(ctx context.Context, releaseID int64, provider, mediaID, mediaURL string) (owner int64, err error)
	RecordMediaLinkAttempt(ctx context.Context, a Attempt) error
}

// ErrBusy is returned by Pacer.Wait when the next slot is too far away.
var ErrBusy = errors.New("media link: provider rate limit, try later")

// Pacer spaces provider requests at least Interval apart. It is safe for
// concurrent use.
type Pacer struct {
	Interval time.Duration
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error

	mu   sync.Mutex
	next time.Time
}

// NewPacer returns a pacer with the real clock.
func NewPacer(interval time.Duration) *Pacer { return &Pacer{Interval: interval} }

func (p *Pacer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Wait blocks until the next request slot. With maxWait > 0 it returns
// ErrBusy instead of waiting longer than maxWait (and reserves nothing).
func (p *Pacer) Wait(ctx context.Context, maxWait time.Duration) error {
	if p == nil {
		return ctx.Err()
	}
	p.mu.Lock()
	now := p.now()
	slot := p.next
	if slot.Before(now) {
		slot = now
	}
	wait := slot.Sub(now)
	if maxWait > 0 && wait > maxWait {
		p.mu.Unlock()
		return ErrBusy
	}
	p.next = slot.Add(p.Interval)
	p.mu.Unlock()
	sleep := p.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	return sleep(ctx, wait)
}

// Linker links one release; the job and the on-demand path share it.
type Linker struct {
	Store  LinkStore
	Search Searcher
	Pacer  *Pacer
	// MaxWait bounds how long one request waits for the pacer (0 = no
	// bound; the on-demand path uses a short one).
	MaxWait time.Duration
	Now     func() time.Time
}

func (l *Linker) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Outcome is the result of linking one release.
type Outcome struct {
	Attempt  Attempt
	Requests int   // provider requests made
	Err      error // provider error (status error); nil otherwise
}

// Linked reports whether the release now has a link for the provider.
func (o Outcome) Linked() bool { return o.Attempt.Status == StatusFound }

// RateLimited reports whether the provider answered 403/429.
func (o Outcome) RateLimited() bool { return o.Err != nil && media.IsRateLimited(o.Err) }

// maxErrorText bounds last_error.
const maxErrorText = 300

func errorText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > maxErrorText {
		s = s[:maxErrorText]
	}
	return s
}

// Link searches provider for each of c's codes, stores a unique exact match
// and records the attempt. The returned error is a store or context error
// (nothing recorded); provider failures are an Outcome with status error.
func (l *Linker) Link(ctx context.Context, provider string, c Candidate) (Outcome, error) {
	var out Outcome
	var results []CodeResult
	for _, code := range c.Codes {
		if err := l.Pacer.Wait(ctx, l.MaxWait); err != nil {
			return out, err
		}
		items, _, err := l.Search.Search(ctx, provider, code, 1)
		out.Requests++
		if err != nil && ctx.Err() != nil {
			return out, ctx.Err()
		}
		r := CodeResult{Code: code, Err: err}
		if err == nil {
			r.IDs = media.ExactCodeCandidates(items, code)
		}
		results = append(results, r)
		if len(r.IDs) == 1 || media.IsRateLimited(err) {
			break // found, or the provider wants us to stop
		}
	}
	status, mediaID, candidates, perr := Decide(results)
	out.Err = perr
	now := l.now()
	a := Attempt{ReleaseID: c.ReleaseID, Provider: provider, Status: status, TriedAt: now,
		MatchedMediaID: mediaID, Candidates: candidates, LastError: errorText(perr)}
	if status == StatusFound {
		owner, err := l.Store.ClaimMediaLink(ctx, c.ReleaseID, provider, mediaID, MediaURL(provider, mediaID))
		if err != nil {
			return out, fmt.Errorf("link release %d: %w", c.ReleaseID, err)
		}
		if owner != c.ReleaseID {
			// The provider page is already linked to another release (for
			// example a duplicate not merged yet): leave it to an admin.
			a.Status, a.MatchedMediaID = StatusAmbiguous, ""
			a.LastError = fmt.Sprintf("media id already linked to release %d", owner)
		}
	}
	if a.Candidates == nil {
		a.Candidates = []string{}
	}
	a.Attempts = Streak(c.Prev, a.Status)
	a.NextTryAt = NextTry(a.Status, a.Attempts, now)
	if err := l.Store.RecordMediaLinkAttempt(ctx, a); err != nil {
		return out, fmt.Errorf("record attempt for release %d: %w", c.ReleaseID, err)
	}
	out.Attempt = a
	return out, nil
}
