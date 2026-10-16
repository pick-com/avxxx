package store

// AdminSearch 补片 / 建预约页 (PRD 4.5, 11): a zero-result search term
// becomes a minimal release — a draft for the editors to complete, or an
// upcoming release with a date (预约页: reservable, published by the
// notifier when scheduled_at passes, like any upcoming release). With a code
// the slug is the code's slug, so a later crawl of that code updates this
// release instead of creating a second one.

import (
	"avxxx/internal/catalog"
	"avxxx/internal/i18n"
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ReleaseStubInput creates a release from a search term.
type ReleaseStubInput struct {
	Term        string
	Title       string
	Code        string
	ScheduledAt *time.Time // set: upcoming (建预约页); nil: draft (补片)
	Reason      string
}

// ReleaseStub is the created release.
type ReleaseStub struct {
	TermFix
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// CreateReleaseFromTerm creates the release with a review_events row
// ('add_release' or 'create_upcoming'). A code or slug that already exists
// is a *ConflictError naming the existing release.
func (s *Store) CreateReleaseFromTerm(ctx context.Context, in ReleaseStubInput, actor string) (ReleaseStub, error) {
	in.Title, in.Code = strings.TrimSpace(in.Title), strings.TrimSpace(in.Code)
	out := ReleaseStub{TermFix: TermFix{EntityType: "release", Action: "add_release", Value: in.Title, Term: in.Term}, Status: ReleaseDraft}
	if in.Title == "" {
		return out, invalid("title is required")
	}
	if len([]rune(in.Title)) > 300 || len(in.Code) > 64 || len(in.Reason) > MaxReasonLen {
		return out, invalid("title is at most 300 and code at most 64 characters")
	}
	if in.ScheduledAt != nil {
		if !in.ScheduledAt.After(time.Now()) {
			return out, invalid("scheduled_at must be in the future")
		}
		out.Action, out.Status = "create_upcoming", ReleaseUpcoming
	}
	norm := catalog.NormalizeIdentifier(in.Code)
	key, err := randomToken()
	if err != nil {
		return out, err
	}
	out.Slug = catalog.ReleaseSlug(in.Code, in.Title, "manual", key[:8])
	if out.Slug == "" {
		return out, invalid("title or code must contain letters or digits")
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if norm != "" {
			var slug string
			err := tx.QueryRow(ctx, `SELECT r.slug FROM release_identifiers i JOIN releases r ON r.id=i.release_id
				WHERE i.identifier_type='dvd_code' AND code_match_key(i.normalized_value)=code_match_key($1) LIMIT 1`, norm).Scan(&slug)
			if err == nil {
				return conflict("code %s is already release %s", in.Code, slug)
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var id int64
		err := tx.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,scheduled_at) VALUES($1,$2,$3,$4)
			ON CONFLICT(slug) DO NOTHING RETURNING id`, out.Slug, in.Title, out.Status, in.ScheduledAt).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return conflict("release %s already exists", out.Slug)
		}
		if err != nil {
			return err
		}
		out.EntityID = id
		if norm != "" {
			if _, err := tx.Exec(ctx, `INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value,identifier_scope)
				VALUES($1,'dvd_code',$2,$3,'global')`, id, in.Code, norm); err != nil {
				return err
			}
		}
		if err := markStale(ctx, tx, i18n.EntityRelease, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		after := map[string]any{"title": in.Title, "status": out.Status, "search_term": in.Term}
		if in.Code != "" {
			after["code"] = in.Code
		}
		if in.ScheduledAt != nil {
			after["scheduled_at"] = in.ScheduledAt.UTC().Format(time.RFC3339)
		}
		return writeReviewEvent(ctx, tx, "release", id, out.Action, nil, after, in.Reason, actor)
	})
	return out, err
}
