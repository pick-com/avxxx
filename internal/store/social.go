package store

// User-side social features: settings, follows and the following feed,
// reservations of upcoming releases, watch-later and watch-history management
// (PRD 7, 17, 20; migration 014). Pure helpers (settings validation, reorder,
// notify-mode filtering) are separate from the SQL so they can be unit tested.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	_ "time/tzdata" // the runtime image (debian slim) has no zoneinfo; settings.timezone needs it

	"github.com/jackc/pgx/v5"
)

var (
	// ErrInvalidSettings marks a rejected settings patch (unknown key, bad enum).
	ErrInvalidSettings = errors.New("invalid settings")
	// ErrReleaseNotUpcoming is returned when reserving a release that is not upcoming.
	ErrReleaseNotUpcoming = errors.New("release is not upcoming")
	// ErrInvalidOrder marks a watch-later reorder request with unknown or duplicate slugs.
	ErrInvalidOrder = errors.New("invalid order")
)

// Notify modes for follows (PRD 17: all new works / lead roles only / off).
const (
	NotifyAll      = "all"
	NotifyLeadOnly = "lead_only"
	NotifyOff      = "off"
)

// ValidNotifyMode reports whether m is a follows.notify_mode value.
func ValidNotifyMode(m string) bool { return m == NotifyAll || m == NotifyLeadOnly || m == NotifyOff }

// ---------------------------------------------------------------------------
// Settings

// Notification kinds (PRD 7) and delivery channels (PRD 17).
var (
	NotifyKinds    = []string{"new_work", "reservation", "comment_reply", "favorite_update"}
	NotifyChannels = []string{"in_app", "push", "email"}
	// Languages planned for launch (PRD 8); also valid for content and subtitles.
	SettingsLangs = []string{"zh-CN", "zh-TW", "en", "ja", "ko", "vi", "es", "th"}
	QualityPrefs  = []string{"auto", "2160p", "1080p", "720p", "480p", "360p"}
	Themes        = []string{"system", "dark", "light"}
	// When reservation holders hear about a release (migration 037).
	ReservationTimings = []string{"go_live", "day_before", "both"}
	// Allowed push / email deliveries per channel per local day.
	NotifyDailyCaps = []int{1, 2, 3, 5, 10}
)

// QuietHours is a daily do-not-disturb window in the user's timezone, as
// "HH:MM". Start after End means the window crosses midnight.
type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// NotifyPrefs maps kind -> channel -> enabled.
type NotifyPrefs map[string]map[string]bool

// Enabled reports whether kind is enabled on channel (defaults fill gaps).
func (p NotifyPrefs) Enabled(kind, channel string) bool {
	if ch, ok := p[kind]; ok {
		if v, ok := ch[channel]; ok {
			return v
		}
	}
	return defaultNotifyPrefs()[kind][channel]
}

// UserSettings is one user_settings row.
type UserSettings struct {
	UILang        string      `json:"ui_lang"`
	ContentLang   string      `json:"content_lang"`
	SubtitleLangs []string    `json:"subtitle_langs"`
	QualityPref   string      `json:"quality_pref"`
	DataSaver     bool        `json:"data_saver"`
	Autoplay      bool        `json:"autoplay"`
	RememberRate  bool        `json:"remember_rate"`
	HistoryPaused bool        `json:"history_paused"`
	Theme         string      `json:"theme"`
	Timezone      string      `json:"timezone"`
	NotifyPrefs   NotifyPrefs `json:"notify_prefs"`
	QuietHours    *QuietHours `json:"quiet_hours"`

	// LaterAutoRemove drops a watch-later entry at 90 % progress (migration 036).
	LaterAutoRemove bool `json:"later_auto_remove"`
	// Migration 037: daily push/email cap, reservation reminder timing and
	// whether watch history personalizes recommendations.
	NotifyDailyCap    int    `json:"notify_daily_cap"`
	ReservationTiming string `json:"reservation_timing"`
	PersonalizeRecs   bool   `json:"personalize_recs"`
}

// defaultNotifyPrefs: in-app for everything; push on by default only for the
// first two kinds (PRD 7); email opt-in.
func defaultNotifyPrefs() NotifyPrefs {
	return NotifyPrefs{
		"new_work":        {"in_app": true, "push": true, "email": false},
		"reservation":     {"in_app": true, "push": true, "email": false},
		"comment_reply":   {"in_app": true, "push": false, "email": false},
		"favorite_update": {"in_app": true, "push": false, "email": false},
	}
}

// DefaultSettings mirrors the column defaults of user_settings (migration 014).
func DefaultSettings() UserSettings {
	return UserSettings{
		UILang: "zh-CN", ContentLang: "zh-CN", SubtitleLangs: []string{},
		QualityPref: "auto", Autoplay: true, RememberRate: true, LaterAutoRemove: true,
		Theme: "system", Timezone: "UTC",
		NotifyPrefs:    defaultNotifyPrefs(),
		QuietHours:     &QuietHours{Start: "22:00", End: "08:00"},
		NotifyDailyCap: 1, ReservationTiming: "go_live", PersonalizeRecs: true,
	}
}

// normalize fills missing notify prefs with defaults so stored rows are fully expanded.
func (u *UserSettings) normalize() {
	full := defaultNotifyPrefs()
	for kind, chans := range u.NotifyPrefs {
		if _, ok := full[kind]; !ok {
			continue
		}
		for ch, v := range chans {
			if _, ok := full[kind][ch]; ok {
				full[kind][ch] = v
			}
		}
	}
	u.NotifyPrefs = full
	if u.SubtitleLangs == nil {
		u.SubtitleLangs = []string{}
	}
}

// Location returns the user's timezone, falling back to UTC.
func (u UserSettings) Location() *time.Location {
	if loc, err := time.LoadLocation(u.Timezone); err == nil && u.Timezone != "" {
		return loc
	}
	return time.UTC
}

func invalidSettings(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSettings, fmt.Sprintf(format, a...))
}

// parseClock parses "HH:MM" (24h) into minutes after midnight.
func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func decodeEnum(key string, raw json.RawMessage, allowed []string) (string, error) {
	var v string
	if err := json.Unmarshal(raw, &v); err != nil || !slices.Contains(allowed, v) {
		return "", invalidSettings("%s must be one of %s", key, strings.Join(allowed, ", "))
	}
	return v, nil
}

func decodeBool(key string, raw json.RawMessage) (bool, error) {
	var v *bool
	if err := json.Unmarshal(raw, &v); err != nil || v == nil {
		return false, invalidSettings("%s must be a boolean", key)
	}
	return *v, nil
}

// ApplySettingsPatch applies a JSON object of setting changes to cur. Unknown
// keys, bad types and out-of-range enum values are ErrInvalidSettings.
// notify_prefs is merged per kind and channel; quiet_hours null disables it.
func ApplySettingsPatch(cur UserSettings, body []byte) (UserSettings, error) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(body, &patch); err != nil || patch == nil {
		return cur, invalidSettings("body must be a JSON object")
	}
	out := cur
	out.SubtitleLangs = slices.Clone(cur.SubtitleLangs)
	out.normalize() // deep-copies NotifyPrefs
	var err error
	// Sorted keys give deterministic error messages.
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		raw := patch[key]
		switch key {
		case "ui_lang":
			out.UILang, err = decodeEnum(key, raw, SettingsLangs)
		case "content_lang":
			out.ContentLang, err = decodeEnum(key, raw, SettingsLangs)
		case "quality_pref":
			out.QualityPref, err = decodeEnum(key, raw, QualityPrefs)
		case "theme":
			out.Theme, err = decodeEnum(key, raw, Themes)
		case "data_saver":
			out.DataSaver, err = decodeBool(key, raw)
		case "autoplay":
			out.Autoplay, err = decodeBool(key, raw)
		case "remember_rate":
			out.RememberRate, err = decodeBool(key, raw)
		case "history_paused":
			out.HistoryPaused, err = decodeBool(key, raw)
		case "later_auto_remove":
			out.LaterAutoRemove, err = decodeBool(key, raw)
		case "personalize_recs":
			out.PersonalizeRecs, err = decodeBool(key, raw)
		case "reservation_timing":
			out.ReservationTiming, err = decodeEnum(key, raw, ReservationTimings)
		case "notify_daily_cap":
			var n int
			if json.Unmarshal(raw, &n) != nil || !slices.Contains(NotifyDailyCaps, n) {
				return cur, invalidSettings("notify_daily_cap must be one of 1, 2, 3, 5, 10")
			}
			out.NotifyDailyCap = n
		case "subtitle_langs":
			var langs []string
			if json.Unmarshal(raw, &langs) != nil || langs == nil {
				return cur, invalidSettings("subtitle_langs must be an array of languages")
			}
			seen := map[string]bool{}
			out.SubtitleLangs = []string{}
			for _, l := range langs {
				if !slices.Contains(SettingsLangs, l) {
					return cur, invalidSettings("subtitle_langs: unknown language %q", l)
				}
				if !seen[l] {
					seen[l] = true
					out.SubtitleLangs = append(out.SubtitleLangs, l)
				}
			}
		case "timezone":
			var tz string
			if json.Unmarshal(raw, &tz) != nil || tz == "" || strings.EqualFold(tz, "local") {
				return cur, invalidSettings("timezone must be an IANA zone name")
			}
			if _, lerr := time.LoadLocation(tz); lerr != nil {
				return cur, invalidSettings("unknown timezone %q", tz)
			}
			out.Timezone = tz
		case "quiet_hours":
			if string(raw) == "null" {
				out.QuietHours = nil
				continue
			}
			var qh map[string]json.RawMessage
			if json.Unmarshal(raw, &qh) != nil || qh == nil {
				return cur, invalidSettings("quiet_hours must be {start, end} or null")
			}
			var q QuietHours
			for k, v := range qh {
				var s string
				if json.Unmarshal(v, &s) != nil {
					return cur, invalidSettings("quiet_hours.%s must be HH:MM", k)
				}
				switch k {
				case "start":
					q.Start = s
				case "end":
					q.End = s
				default:
					return cur, invalidSettings("unknown key quiet_hours.%s", k)
				}
			}
			start, ok1 := parseClock(q.Start)
			end, ok2 := parseClock(q.End)
			if !ok1 || !ok2 {
				return cur, invalidSettings("quiet_hours start and end must be HH:MM")
			}
			if start == end {
				return cur, invalidSettings("quiet_hours start and end must differ")
			}
			out.QuietHours = &q
		case "notify_prefs":
			var np map[string]map[string]json.RawMessage
			if json.Unmarshal(raw, &np) != nil || np == nil {
				return cur, invalidSettings("notify_prefs must be {kind: {channel: bool}}")
			}
			for kind, chans := range np {
				if !slices.Contains(NotifyKinds, kind) {
					return cur, invalidSettings("unknown notification kind %q", kind)
				}
				for ch, v := range chans {
					if !slices.Contains(NotifyChannels, ch) {
						return cur, invalidSettings("unknown notification channel %q", ch)
					}
					b, berr := decodeBool("notify_prefs."+kind+"."+ch, v)
					if berr != nil {
						return cur, berr
					}
					out.NotifyPrefs[kind][ch] = b
				}
			}
		default:
			return cur, invalidSettings("unknown setting %q", key)
		}
		if err != nil {
			return cur, err
		}
	}
	return out, nil
}

const settingsColumns = `ui_lang,content_lang,subtitle_langs,quality_pref,data_saver,autoplay,remember_rate,history_paused,theme,timezone,notify_prefs,quiet_hours,later_auto_remove,notify_daily_cap,reservation_timing,personalize_recs`

type rowScanner interface{ Scan(dest ...any) error }

func scanSettings(row rowScanner) (UserSettings, error) {
	var u UserSettings
	var prefs, quiet []byte
	var dailyCap int16
	err := row.Scan(&u.UILang, &u.ContentLang, &u.SubtitleLangs, &u.QualityPref, &u.DataSaver, &u.Autoplay,
		&u.RememberRate, &u.HistoryPaused, &u.Theme, &u.Timezone, &prefs, &quiet, &u.LaterAutoRemove,
		&dailyCap, &u.ReservationTiming, &u.PersonalizeRecs)
	u.NotifyDailyCap = int(dailyCap)
	if err != nil {
		return u, err
	}
	if len(prefs) > 0 {
		_ = json.Unmarshal(prefs, &u.NotifyPrefs)
	}
	if len(quiet) > 0 && string(quiet) != "null" {
		u.QuietHours = &QuietHours{}
		if json.Unmarshal(quiet, u.QuietHours) != nil {
			u.QuietHours = nil
		}
	}
	u.normalize()
	return u, nil
}

// Settings returns the user's settings, or the defaults when no row exists.
func (s *Store) Settings(ctx context.Context, userID int64) (UserSettings, error) {
	u, err := scanSettings(s.QueryRow(ctx, `SELECT `+settingsColumns+` FROM user_settings WHERE user_id=$1`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultSettings(), nil
	}
	return u, err
}

// PatchSettings applies a JSON patch (see ApplySettingsPatch) atomically.
func (s *Store) PatchSettings(ctx context.Context, userID int64, body []byte) (UserSettings, error) {
	if _, err := ApplySettingsPatch(DefaultSettings(), body); err != nil {
		return UserSettings{}, err // reject before touching the database
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return UserSettings{}, err
	}
	defer tx.Rollback(ctx)
	cur, err := scanSettings(tx.QueryRow(ctx, `SELECT `+settingsColumns+` FROM user_settings WHERE user_id=$1 FOR UPDATE`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		cur, err = DefaultSettings(), nil
	}
	if err != nil {
		return UserSettings{}, err
	}
	next, err := ApplySettingsPatch(cur, body)
	if err != nil {
		return UserSettings{}, err
	}
	prefs, _ := json.Marshal(next.NotifyPrefs)
	var quiet any // nil -> SQL NULL
	if next.QuietHours != nil {
		quiet, _ = json.Marshal(next.QuietHours)
	}
	_, err = tx.Exec(ctx, `INSERT INTO user_settings(user_id,`+settingsColumns+`,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now())
		ON CONFLICT(user_id) DO UPDATE SET ui_lang=EXCLUDED.ui_lang,content_lang=EXCLUDED.content_lang,
		subtitle_langs=EXCLUDED.subtitle_langs,quality_pref=EXCLUDED.quality_pref,data_saver=EXCLUDED.data_saver,
		autoplay=EXCLUDED.autoplay,remember_rate=EXCLUDED.remember_rate,history_paused=EXCLUDED.history_paused,
		theme=EXCLUDED.theme,timezone=EXCLUDED.timezone,notify_prefs=EXCLUDED.notify_prefs,
		quiet_hours=EXCLUDED.quiet_hours,later_auto_remove=EXCLUDED.later_auto_remove,notify_daily_cap=EXCLUDED.notify_daily_cap,
		reservation_timing=EXCLUDED.reservation_timing,personalize_recs=EXCLUDED.personalize_recs,updated_at=now()`,
		userID, next.UILang, next.ContentLang, next.SubtitleLangs, next.QualityPref, next.DataSaver, next.Autoplay,
		next.RememberRate, next.HistoryPaused, next.Theme, next.Timezone, prefs, quiet, next.LaterAutoRemove,
		next.NotifyDailyCap, next.ReservationTiming, next.PersonalizeRecs)
	if err != nil {
		return UserSettings{}, err
	}
	return next, tx.Commit(ctx)
}

// HistoryPaused reports whether the user paused watch-history recording.
func (s *Store) HistoryPaused(ctx context.Context, userID int64) (bool, error) {
	var paused bool
	err := s.QueryRow(ctx, `SELECT history_paused FROM user_settings WHERE user_id=$1`, userID).Scan(&paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return paused, err
}

// ---------------------------------------------------------------------------
// Follows and the following feed

// publicPerformerID resolves an active, unmerged performer slug (ErrNoRows -> 404).
func (s *Store) publicPerformerID(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := s.QueryRow(ctx, `SELECT id FROM performers WHERE slug=$1 AND status='active' AND merged_into_id IS NULL`, slug).Scan(&id)
	return id, err
}

// Follow follows a performer or changes the notify mode of an existing follow.
func (s *Store) Follow(ctx context.Context, userID int64, performerSlug, mode string) (map[string]any, error) {
	if !ValidNotifyMode(mode) {
		return nil, fmt.Errorf("%w: notify_mode", ErrInvalidSettings)
	}
	pid, err := s.publicPerformerID(ctx, performerSlug)
	if err != nil {
		return nil, err
	}
	var created time.Time
	err = s.QueryRow(ctx, `INSERT INTO follows(user_id,performer_id,notify_mode) VALUES($1,$2,$3)
		ON CONFLICT(user_id,performer_id) DO UPDATE SET notify_mode=EXCLUDED.notify_mode RETURNING created_at`,
		userID, pid, mode).Scan(&created)
	if err != nil {
		return nil, err
	}
	return map[string]any{"performer_slug": performerSlug, "notify_mode": mode, "created_at": created}, nil
}

// Unfollow removes a follow; unfollowing a performer not followed is a no-op.
func (s *Store) Unfollow(ctx context.Context, userID int64, performerSlug string) error {
	pid, err := s.publicPerformerID(ctx, performerSlug)
	if err != nil {
		return err
	}
	_, err = s.Exec(ctx, `DELETE FROM follows WHERE user_id=$1 AND performer_id=$2`, userID, pid)
	return err
}

// Follows lists followed performers with new_count: releases that went live
// (fan-out ran) since the user last opened the following feed.
func (s *Store) Follows(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.Query(ctx, `SELECT p.slug,p.canonical_name,COALESCE(p.canonical_name_ja,''),f.notify_mode,f.created_at,f.last_seen_at,
		(SELECT count(*) FROM release_performers rp JOIN releases r ON r.id=rp.release_id
		 WHERE rp.performer_id=f.performer_id AND r.status='published' AND r.merged_into_id IS NULL
		   AND r.notified_at > COALESCE(f.last_seen_at,f.created_at))
		FROM follows f JOIN performers p ON p.id=f.performer_id
		WHERE f.user_id=$1 ORDER BY f.created_at DESC LIMIT 1000`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var slug, name, nameJA, mode string
		var created time.Time
		var seen *time.Time
		var n int64
		if err := rows.Scan(&slug, &name, &nameJA, &mode, &created, &seen, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"slug": slug, "name": name, "name_ja": nameJA, "notify_mode": mode,
			"created_at": created, "last_seen_at": seen, "new_count": n})
	}
	return out, rows.Err()
}

// FeedSort is the cursor sort tag of the following feed.
const FeedSort = "feed"

// feedKey is the feed's sort expression: the release date, else the scheduled
// date, so upcoming works sort by when they go live.
const feedKey = `COALESCE(r.release_date, (r.scheduled_at AT TIME ZONE 'UTC')::date, DATE '0001-01-01')`

// FollowingFeed returns published and upcoming releases of followed performers,
// newest first, keyset-paginated. The first page (no cursor) marks every follow
// as seen, which clears the sidebar new-work dot.
func (s *Store) FollowingFeed(ctx context.Context, userID int64, c *Cursor, limit int) ([]map[string]any, string, error) {
	return s.FollowingFeedFor(ctx, userID, "", c, limit)
}

// FollowingFeedFor is FollowingFeed narrowed to one followed performer
// (MobileFollowing 按演员筛选) when performerSlug is set; its first page marks
// only that follow as seen. A performer the user does not follow yields an
// empty feed; an unknown slug is pgx.ErrNoRows.
func (s *Store) FollowingFeedFor(ctx context.Context, userID int64, performerSlug string, c *Cursor, limit int) ([]map[string]any, string, error) {
	limit = pageLimit(limit)
	var pid int64
	if performerSlug != "" {
		var err error
		if pid, err = s.publicPerformerID(ctx, performerSlug); err != nil {
			return nil, "", err
		}
	}
	args := []any{userID, limit + 1, pid}
	after := ""
	if c != nil {
		if c.Sort != FeedSort {
			return nil, "", invalidQuery("cursor does not match feed")
		}
		d, err := time.Parse(time.DateOnly, c.Key)
		if err != nil {
			return nil, "", invalidQuery("malformed cursor")
		}
		args = append(args, d, c.ID)
		after = ` AND (` + feedKey + `, r.id) < ($4::date, $5::bigint)`
	}
	rows, err := s.Query(ctx, `SELECT r.id,r.slug,r.canonical_title,COALESCE(r.title_ja,''),r.release_date,r.scheduled_at,r.status,
		to_char(`+feedKey+`,'YYYY-MM-DD'),
		EXISTS(SELECT 1 FROM reservations rv WHERE rv.user_id=$1 AND rv.release_id=r.id),
		ARRAY(SELECT p.slug FROM release_performers rp JOIN follows f ON f.performer_id=rp.performer_id AND f.user_id=$1
		      JOIN performers p ON p.id=rp.performer_id WHERE rp.release_id=r.id ORDER BY rp.billing_order NULLS LAST, p.id)
		FROM releases r
		WHERE r.status IN ('published','upcoming') AND r.merged_into_id IS NULL
		  AND EXISTS(SELECT 1 FROM release_performers rp JOIN follows f ON f.performer_id=rp.performer_id
		             WHERE f.user_id=$1 AND rp.release_id=r.id AND ($3::bigint=0 OR rp.performer_id=$3))`+after+`
		ORDER BY `+feedKey+` DESC, r.id DESC LIMIT $2`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []map[string]any{}
	keys := []string{}
	ids := []int64{}
	for rows.Next() {
		var id int64
		var slug, title, titleJA, status, key string
		var rd, sched *time.Time
		var reserved bool
		var performers []string
		if err := rows.Scan(&id, &slug, &title, &titleJA, &rd, &sched, &status, &key, &reserved, &performers); err != nil {
			return nil, "", err
		}
		out = append(out, map[string]any{"slug": slug, "title": title, "title_ja": titleJA, "release_date": rd,
			"scheduled_at": sched, "status": status, "reserved": reserved, "followed_performers": performers})
		keys = append(keys, key)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: FeedSort, Key: keys[limit-1], ID: ids[limit-1]})
	}
	if c == nil {
		if _, err := s.Exec(ctx, `UPDATE follows SET last_seen_at=now() WHERE user_id=$1 AND ($2::bigint=0 OR performer_id=$2)`, userID, pid); err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

// ---------------------------------------------------------------------------
// Reservations

// Reserve reserves an upcoming release (ErrReleaseNotUpcoming otherwise).
func (s *Store) Reserve(ctx context.Context, userID int64, releaseSlug string) error {
	var id int64
	var status string
	err := s.QueryRow(ctx, `SELECT id,status FROM releases WHERE slug=$1 AND merged_into_id IS NULL AND status IN ('upcoming','published')`, releaseSlug).Scan(&id, &status)
	if err != nil {
		return err
	}
	if status != "upcoming" {
		return ErrReleaseNotUpcoming
	}
	_, err = s.Exec(ctx, `INSERT INTO reservations(user_id,release_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, userID, id)
	return err
}

// Unreserve cancels a reservation; only upcoming releases can be unreserved
// (after release the reservation is history and has already been notified).
func (s *Store) Unreserve(ctx context.Context, userID int64, releaseSlug string) error {
	var id int64
	var status string
	err := s.QueryRow(ctx, `SELECT id,status FROM releases WHERE slug=$1 AND merged_into_id IS NULL AND status IN ('upcoming','published')`, releaseSlug).Scan(&id, &status)
	if err != nil {
		return err
	}
	if status != "upcoming" {
		return ErrReleaseNotUpcoming
	}
	_, err = s.Exec(ctx, `DELETE FROM reservations WHERE user_id=$1 AND release_id=$2`, userID, id)
	return err
}

// Reservations lists the user's reservations, soonest first, with the
// release's current status (upcoming or published).
func (s *Store) Reservations(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.Query(ctx, `SELECT r.slug,r.canonical_title,COALESCE(r.title_ja,''),r.status,r.release_date,r.scheduled_at,rv.created_at
		FROM reservations rv JOIN releases r ON r.id=rv.release_id
		WHERE rv.user_id=$1 AND r.merged_into_id IS NULL AND r.status IN ('upcoming','published')
		ORDER BY (r.status='upcoming') DESC, r.scheduled_at NULLS LAST, rv.created_at DESC LIMIT 500`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var slug, title, titleJA, status string
		var rd, sched *time.Time
		var created time.Time
		if err := rows.Scan(&slug, &title, &titleJA, &status, &rd, &sched, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"slug": slug, "title": title, "title_ja": titleJA, "status": status,
			"release_date": rd, "scheduled_at": sched, "reserved_at": created})
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Watch later

// ReorderPositions returns the new watch-later order: the slugs in ordered
// first, then every other current entry in its current order. ordered must
// not repeat a slug or name one that is not in current (ErrInvalidOrder).
func ReorderPositions(current, ordered []string) ([]string, error) {
	inCurrent := make(map[string]bool, len(current))
	for _, s := range current {
		inCurrent[s] = true
	}
	seen := make(map[string]bool, len(ordered))
	out := make([]string, 0, len(current))
	for _, s := range ordered {
		if !inCurrent[s] {
			return nil, fmt.Errorf("%w: %q is not in watch later", ErrInvalidOrder, s)
		}
		if seen[s] {
			return nil, fmt.Errorf("%w: %q listed twice", ErrInvalidOrder, s)
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, s := range current {
		if !seen[s] {
			out = append(out, s)
		}
	}
	return out, nil
}

// WatchLater lists the queue in position order.
func (s *Store) WatchLater(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.Query(ctx, `SELECT r.slug,r.canonical_title,COALESCE(r.title_ja,''),COALESCE(r.runtime_minutes,0),wl.position,wl.created_at
		FROM watch_later wl JOIN releases r ON r.id=wl.release_id
		WHERE wl.user_id=$1 AND r.status='published' AND r.merged_into_id IS NULL
		ORDER BY wl.position, wl.created_at LIMIT 1000`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var slug, title, titleJA string
		var runtime, pos int
		var created time.Time
		if err := rows.Scan(&slug, &title, &titleJA, &runtime, &pos, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"slug": slug, "title": title, "title_ja": titleJA,
			"runtime_minutes": runtime, "position": pos, "added_at": created})
	}
	return out, rows.Err()
}

// AddWatchLater appends a published release to the end of the queue (no-op if present).
func (s *Store) AddWatchLater(ctx context.Context, userID int64, releaseSlug string) error {
	var id int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status='published' AND merged_into_id IS NULL`, releaseSlug).Scan(&id); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `INSERT INTO watch_later(user_id,release_id,position)
		SELECT $1,$2,COALESCE(max(position),0)+1 FROM watch_later WHERE user_id=$1
		ON CONFLICT(user_id,release_id) DO NOTHING`, userID, id)
	return err
}

// RemoveWatchLater removes a release from the queue (no-op if absent).
func (s *Store) RemoveWatchLater(ctx context.Context, userID int64, releaseSlug string) error {
	var id int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, releaseSlug).Scan(&id); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `DELETE FROM watch_later WHERE user_id=$1 AND release_id=$2`, userID, id)
	return err
}

// ReorderWatchLater rewrites positions 1..n following ReorderPositions.
func (s *Store) ReorderWatchLater(ctx context.Context, userID int64, ordered []string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT r.slug,r.id FROM watch_later wl JOIN releases r ON r.id=wl.release_id
		WHERE wl.user_id=$1 ORDER BY wl.position, wl.created_at FOR UPDATE OF wl`, userID)
	if err != nil {
		return err
	}
	var current []string
	ids := map[string]int64{}
	for rows.Next() {
		var slug string
		var id int64
		if err := rows.Scan(&slug, &id); err != nil {
			rows.Close()
			return err
		}
		current = append(current, slug)
		ids[slug] = id
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	order, err := ReorderPositions(current, ordered)
	if err != nil {
		return err
	}
	releaseIDs := make([]int64, len(order))
	positions := make([]int32, len(order))
	for i, slug := range order {
		releaseIDs[i] = ids[slug]
		positions[i] = int32(i + 1)
	}
	if _, err := tx.Exec(ctx, `UPDATE watch_later wl SET position=v.pos
		FROM unnest($2::bigint[], $3::int[]) AS v(release_id, pos)
		WHERE wl.user_id=$1 AND wl.release_id=v.release_id`, userID, releaseIDs, positions); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Watch history management

// DeleteWatch removes one release from the user's watch history (no-op if absent).
func (s *Store) DeleteWatch(ctx context.Context, userID int64, releaseSlug string) error {
	var id int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, releaseSlug).Scan(&id); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `DELETE FROM watch_history WHERE user_id=$1 AND release_id=$2`, userID, id)
	return err
}

// ClearWatchHistory deletes the user's whole watch history and returns the
// row count. 观看统计 (user_watch_daily) is cleared with it (PRD 20); later
// rebuilds only count plays whose release is back in the history.
func (s *Store) ClearWatchHistory(ctx context.Context, userID int64) (int64, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `DELETE FROM watch_history WHERE user_id=$1`, userID)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM user_watch_daily WHERE user_id=$1`, userID); err != nil {
		return 0, err
	}
	return tag.RowsAffected(), tx.Commit(ctx)
}
