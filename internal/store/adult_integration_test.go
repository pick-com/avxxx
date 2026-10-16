package store

import (
	"context"
	"errors"
	"testing"
)

// TestAdultConfirmationIntegration covers migration 046: the 18+
// confirmation on registration, OAuth sign-up (refused without it, nothing
// written), the one-time confirmation for older accounts and the OAuth
// state round trip.
func TestAdultConfirmationIntegration(t *testing.T) {
	openIsolatedTestDB(t, "adult")
	ctx := context.Background()
	for i := 0; i < 2; i++ { // migration 046 is idempotent
		s, err := Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	type row struct {
		confirmed bool
		ym        *string
	}
	stored := func(id int64) row {
		t.Helper()
		var r row
		must(s.QueryRow(ctx, `SELECT adult_confirmed_at IS NOT NULL, adult_birth_ym FROM users WHERE id=$1`, id).Scan(&r.confirmed, &r.ym))
		return r
	}
	uidOf := func(m map[string]any) int64 { return m["user"].(map[string]any)["id"].(int64) }

	// Registration with a typed birth year-month keeps it; with an ok gate
	// cookie (no birth date) only the confirmation time.
	ann, err := s.RegisterSession(ctx, "ann@example.com", "password-1", "Ann", "", Adult{Confirmed: true, BirthYM: "1990-04"}, SessionMeta{Remember: true})
	must(err)
	if r := stored(uidOf(ann)); !r.confirmed || r.ym == nil || *r.ym != "1990-04" {
		t.Errorf("ann = %+v", r)
	}
	me, err := s.UserByToken(ctx, ann["token"].(string))
	must(err)
	if me["adult_confirmed"] != true {
		t.Errorf("me.adult_confirmed = %v", me["adult_confirmed"])
	}
	if _, err := s.RegisterSession(ctx, "bad@example.com", "password-1", "", "", Adult{Confirmed: true, BirthYM: "1990-13"}, SessionMeta{}); err == nil {
		t.Error("invalid birth year-month accepted")
	}
	gate, err := s.RegisterSession(ctx, "gate@example.com", "password-1", "", "", Adult{Confirmed: true}, SessionMeta{})
	must(err)
	if r := stored(uidOf(gate)); !r.confirmed || r.ym != nil {
		t.Errorf("gate-ok registration = %+v", r)
	}

	// An account from before migration 046: no confirmation, then the prompt.
	old, err := s.RegisterSession(ctx, "old@example.com", "password-1", "", "", Adult{}, SessionMeta{Remember: true})
	must(err)
	oldID := uidOf(old)
	me, err = s.UserByToken(ctx, old["token"].(string))
	must(err)
	if me["adult_confirmed"] != false {
		t.Errorf("unconfirmed me.adult_confirmed = %v", me["adult_confirmed"])
	}
	if err := s.ConfirmAdult(ctx, oldID, Adult{}); !errors.Is(err, ErrAdultRequired) {
		t.Errorf("confirm without checkbox: %v", err)
	}
	must(s.ConfirmAdult(ctx, oldID, Adult{Confirmed: true, BirthYM: "1985-01"}))
	must(s.ConfirmAdult(ctx, oldID, Adult{Confirmed: true})) // keeps the year-month
	if r := stored(oldID); !r.confirmed || r.ym == nil || *r.ym != "1985-01" {
		t.Errorf("old = %+v", r)
	}

	// OAuth: no confirmation, no account.
	if _, _, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-dan", Email: "dan@example.com", EmailVerified: true}, Adult{}); !errors.Is(err, ErrAdultRequired) {
		t.Errorf("unconfirmed OAuth sign-up: %v", err)
	}
	var n int
	must(s.QueryRow(ctx, `SELECT count(*) FROM users WHERE email='dan@example.com'`).Scan(&n))
	if n != 0 {
		t.Error("unconfirmed OAuth sign-up created a user")
	}
	dan, created, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-dan", Email: "dan@example.com", EmailVerified: true}, Adult{Confirmed: true})
	must(err)
	if !created || !stored(dan).confirmed {
		t.Errorf("confirmed OAuth sign-up: created=%v %+v", created, stored(dan))
	}
	// An existing identity signs in without a new confirmation.
	if again, created, err := s.OAuthSignIn(ctx, OAuthProfile{Provider: "google", Subject: "g-dan"}, Adult{}); err != nil || again != dan || created {
		t.Errorf("existing identity = %d %v %v", again, created, err)
	}

	// The confirmation rides on the authorization request.
	must(s.SaveOAuthState(ctx, OAuthState{StateHash: "a1", Provider: "google", CodeVerifier: "v", Nonce: "n", BindHash: "b", NextPath: "/",
		Adult: Adult{Confirmed: true, BirthYM: "2000-02"}}))
	st, err := s.TakeOAuthState(ctx, "google", "a1")
	must(err)
	if st.Adult != (Adult{Confirmed: true, BirthYM: "2000-02"}) {
		t.Errorf("state adult = %+v", st.Adult)
	}
	must(s.SaveOAuthState(ctx, OAuthState{StateHash: "a2", Provider: "google", CodeVerifier: "v", Nonce: "n", BindHash: "b", NextPath: "/"}))
	if st, err = s.TakeOAuthState(ctx, "google", "a2"); err != nil || st.Adult != (Adult{}) {
		t.Errorf("state without adult = %+v %v", st.Adult, err)
	}
}
