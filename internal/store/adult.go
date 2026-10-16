package store

// Account 18+ confirmation (DESIGN_ALIGNMENT #8, PRD 15; migration 046).
//
// The site is adults-only. Visitors confirm their age at the site-entry gate
// (internal/api/agegate.go, a signed cookie; nothing stored). Accounts keep
// the confirmation: users.adult_confirmed_at, plus the birth year-month only
// when the user typed one at that moment. The API decides whether a request
// counts as confirmed (checkbox + ok gate cookie, or checkbox + a birth
// year-month of 18 or over); the store records it and refuses to create an
// OAuth account without it.

import (
	"context"
	"errors"
	"regexp"
)

// ErrAdultRequired: a new account needs the 18+ confirmation first.
var ErrAdultRequired = errors.New("18+ confirmation required")

// Adult is an 18+ confirmation. BirthYM is "YYYY-MM" or "" (not collected:
// the visitor had already passed the site-entry gate).
type Adult struct {
	Confirmed bool
	BirthYM   string
}

var birthYMRe = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

// birthYM is the value stored in users.adult_birth_ym (NULL for "").
func (a Adult) birthYM() (any, error) {
	if a.BirthYM == "" {
		return nil, nil
	}
	if !birthYMRe.MatchString(a.BirthYM) {
		return nil, invalid("invalid birth year-month")
	}
	return a.BirthYM, nil
}

// ConfirmAdult records the one-time 18+ confirmation for an existing account.
// The first confirmation time is kept; a birth year-month already on file is
// not replaced by an empty one.
func (s *Store) ConfirmAdult(ctx context.Context, userID int64, a Adult) error {
	if !a.Confirmed {
		return ErrAdultRequired
	}
	ym, err := a.birthYM()
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `UPDATE users SET adult_confirmed_at=COALESCE(adult_confirmed_at, now()),
		adult_birth_ym=COALESCE($2, adult_birth_ym) WHERE id=$1`, userID, ym)
	return err
}
