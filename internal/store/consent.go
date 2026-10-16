package store

// Cookie consent records (CookieBanner / MobileConsent; PRD 15, 14.5;
// migration 041). Append-only: every saved choice is a new row.

import (
	"context"
	"time"
)

// ConsentRecord is one choice to store. DeviceHash is a keyed hash of the
// browser's random consent id (computed by the API, never the raw id);
// UserID is set when the request carried a valid user token.
type ConsentRecord struct {
	UserID     *int64
	DeviceHash string
	Input      ConsentInput
}

// RecordConsent appends a choice, stamped with the current published
// cookies policy version, and returns it as stored.
func (s *Store) RecordConsent(ctx context.Context, rec ConsentRecord) (ConsentState, error) {
	if err := rec.Input.Normalize(); err != nil {
		return ConsentState{}, err
	}
	if rec.UserID == nil && rec.DeviceHash == "" {
		return ConsentState{}, invalid("device_id is required without a login")
	}
	cats := rec.Input.Categories()
	var (
		version int
		at      time.Time
	)
	err := s.QueryRow(ctx, `INSERT INTO consent_records(user_id,device_hash,categories,policy_version,gpc,source)
		VALUES($1,NULLIF($2,''),$3,COALESCE((SELECT max(version) FROM legal_documents WHERE kind='cookies' AND status='published' AND effective_at<=now()),0),$4,$5)
		RETURNING policy_version,created_at`, rec.UserID, rec.DeviceHash, cats, rec.Input.GPC, rec.Input.Source).Scan(&version, &at)
	if err != nil {
		return ConsentState{}, err
	}
	return consentStateOf(cats, rec.Input.GPC, version, rec.Input.Source, at), nil
}

// LatestConsent is the user's most recent choice on any device (cross-device
// sync). pgx.ErrNoRows when the user has never chosen.
func (s *Store) LatestConsent(ctx context.Context, userID int64) (ConsentState, error) {
	var (
		cats    []string
		gpc     bool
		version int
		source  string
		at      time.Time
	)
	err := s.QueryRow(ctx, `SELECT categories,gpc,policy_version,source,created_at FROM consent_records
		WHERE user_id=$1 ORDER BY created_at DESC, id DESC LIMIT 1`, userID).Scan(&cats, &gpc, &version, &source, &at)
	if err != nil { // pgx.ErrNoRows: no choice yet
		return ConsentState{}, err
	}
	return consentStateOf(cats, gpc, version, source, at), nil
}
