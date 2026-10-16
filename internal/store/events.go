package store

import (
	"avxxx/internal/events"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// eventRow is the JSON shape fed to jsonb_to_recordset; omitted keys become NULL.
type eventRow struct {
	EventID   string          `json:"event_id"`
	Event     string          `json:"event"`
	SchemaVer int             `json:"schema_ver"`
	TSClient  time.Time       `json:"ts_client"`
	TSServer  time.Time       `json:"ts_server"`
	UserID    *int64          `json:"user_id,omitempty"`
	DeviceID  string          `json:"device_id,omitempty"`
	SessionID string          `json:"session_id"`
	PlayID    string          `json:"play_id,omitempty"`
	Platform  string          `json:"platform"`
	AppVer    string          `json:"app_ver"`
	UILang    string          `json:"ui_lang,omitempty"`
	Theme     string          `json:"theme,omitempty"`
	Page      string          `json:"page,omitempty"`
	RefPage   string          `json:"ref_page,omitempty"`
	Slot      string          `json:"slot,omitempty"`
	Consent   []string        `json:"consent"`
	Exp       []string        `json:"exp"`
	ReleaseID *int64          `json:"release_id,omitempty"`
	SourceID  *int64          `json:"source_id,omitempty"`
	Props     json.RawMessage `json:"props"`
}

const insertEventsSQL = `
WITH input AS (
  SELECT DISTINCT ON (event_id) *
  FROM jsonb_to_recordset($1::jsonb) AS x(
    event_id text, event text, schema_ver integer, ts_client timestamptz, ts_server timestamptz,
    user_id bigint, device_id text, session_id text, play_id text, platform text, app_ver text,
    ui_lang text, theme text, page text, ref_page text, slot text, consent text[], exp text[],
    release_id bigint, source_id bigint, props jsonb)
), fresh AS (
  INSERT INTO events_seen (event_id, ts_server)
  SELECT event_id, ts_server FROM input
  ON CONFLICT (event_id) DO NOTHING
  RETURNING event_id
)
INSERT INTO events (event_id, event, schema_ver, ts_client, ts_server, user_id, device_id, session_id,
  play_id, platform, app_ver, ui_lang, theme, page, ref_page, slot, consent, exp, release_id, source_id, props)
SELECT i.event_id, i.event, i.schema_ver, i.ts_client, i.ts_server, i.user_id, i.device_id, i.session_id,
  i.play_id, i.platform, i.app_ver, i.ui_lang, i.theme, i.page, i.ref_page, i.slot,
  COALESCE(i.consent, '{}'), COALESCE(i.exp, '{}'), i.release_id, i.source_id, COALESCE(i.props, '{}')
FROM input i JOIN fresh f USING (event_id)`

// InsertEvents stores evs, skipping any event_id already seen, and returns
// how many were newly inserted. A missing month partition is created on demand
// and the insert retried once (the statement is atomic, so nothing was written).
func (s *Store) InsertEvents(ctx context.Context, evs []events.Event) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	rows := make([]eventRow, len(evs))
	for i, e := range evs {
		props := json.RawMessage(`{}`)
		if e.Props != nil {
			b, err := json.Marshal(e.Props)
			if err != nil {
				return 0, err
			}
			props = b
		}
		rows[i] = eventRow{
			EventID: e.EventID, Event: e.Name, SchemaVer: e.SchemaVer, TSClient: e.TSClient, TSServer: e.TSServer,
			UserID: e.UserID, DeviceID: e.DeviceID, SessionID: e.SessionID, PlayID: e.PlayID,
			Platform: e.Platform, AppVer: e.AppVer, UILang: e.UILang, Theme: e.Theme,
			Page: e.Page, RefPage: e.RefPage, Slot: e.Slot, Consent: e.Consent, Exp: e.Exp,
			ReleaseID: e.ReleaseID, SourceID: e.SourceID, Props: props,
		}
	}
	payload, err := json.Marshal(rows)
	if err != nil {
		return 0, err
	}
	tag, err := s.Exec(ctx, insertEventsSQL, payload)
	if isMissingPartition(err) {
		months := map[time.Time]bool{}
		for _, e := range evs {
			months[monthStart(e.TSServer)] = true
		}
		for m := range months {
			if err := s.EnsureEventPartition(ctx, m); err != nil {
				return 0, err
			}
		}
		tag, err = s.Exec(ctx, insertEventsSQL, payload)
	}
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// QuarantineEvents stores events that failed validation.
func (s *Store) QuarantineEvents(ctx context.Context, rs []events.Rejected) error {
	if len(rs) == 0 {
		return nil
	}
	type row struct {
		EventID  string          `json:"event_id,omitempty"`
		Event    string          `json:"event,omitempty"`
		Reason   string          `json:"reason"`
		Raw      json.RawMessage `json:"raw"`
		TSServer time.Time       `json:"ts_server"`
	}
	out := make([]row, len(rs))
	for i, r := range rs {
		out[i] = row{r.EventID, r.Name, r.Reason, r.Raw, r.TSServer}
	}
	payload, err := json.Marshal(out)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `INSERT INTO events_quarantine (event_id, event, reason, raw, ts_server)
SELECT event_id, event, reason, raw, ts_server
FROM jsonb_to_recordset($1::jsonb) AS x(event_id text, event text, reason text, raw jsonb, ts_server timestamptz)`, payload)
	return err
}

// EnsureEventPartition creates the events partition for the UTC month
// containing month, if missing. Meant for a scheduler job that runs ahead of
// each month boundary; idempotent.
func (s *Store) EnsureEventPartition(ctx context.Context, month time.Time) error {
	_, err := s.Exec(ctx, `SELECT events_ensure_partition($1::date)`, monthStart(month).Format(time.DateOnly))
	return err
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// isMissingPartition matches "no partition of relation ... found for row" (23514).
func isMissingPartition(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}
