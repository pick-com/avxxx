package store

// Admin video editor (AdminVideo / A3, PRD 11): release list / detail,
// field edits with manual overrides, status transitions, playback sources and
// performer credits. Every change writes review_events with the admin actor.
// Validation lives in admin_rules.go.

import (
	"avxxx/internal/audit"
	"avxxx/internal/catalog"
	"avxxx/internal/i18n"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------- list

// AdminReleaseFilter selects releases for the admin list.
type AdminReleaseFilter struct {
	Q      string // title, title_ja, slug, identifier (品番) or numeric id
	Status string // "" = every status
	Cursor string
	Limit  int
}

// AdminReleaseSummary is one row of the admin release list.
type AdminReleaseSummary struct {
	ID               int64      `json:"id"`
	Slug             string     `json:"slug"`
	Title            string     `json:"title"`
	TitleJA          *string    `json:"title_ja"`
	Status           string     `json:"status"`
	ReleaseDate      *string    `json:"release_date"`
	ScheduledAt      *time.Time `json:"scheduled_at"`
	Version          int64      `json:"version"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Identifiers      []string   `json:"identifiers"`
	SourceCount      int        `json:"source_count"`
	OpenIssues       int        `json:"open_issues"`
	PendingConflicts int        `json:"pending_conflicts"`
}

// AdminReleases lists unmerged releases of every status, newest id first,
// keyset paginated by id.
func (s *Store) AdminReleases(ctx context.Context, f AdminReleaseFilter) ([]AdminReleaseSummary, string, error) {
	if f.Status != "" && !ValidReleaseStatus(f.Status) {
		return nil, "", invalidQuery("unknown status %q", f.Status)
	}
	limit := pageLimit(f.Limit)
	var after int64
	if f.Cursor != "" {
		c, err := DecodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		if c.Sort != "admin_releases" {
			return nil, "", invalidQuery("cursor does not match this list")
		}
		after = c.ID
	}
	q := strings.TrimSpace(f.Q)
	rows, err := s.Query(ctx, `SELECT r.id,r.slug,r.canonical_title,r.title_ja,r.status,r.release_date::text,r.scheduled_at,r.version,r.updated_at,
		COALESCE((SELECT array_agg(identifier_value ORDER BY id) FROM release_identifiers WHERE release_id=r.id),'{}'),
		(SELECT count(*) FROM playback_sources ps WHERE ps.release_id=r.id),
		(SELECT count(*) FROM audit_issues ai WHERE ai.entity_type='release' AND ai.entity_id=r.id AND ai.status='open'),
		(SELECT count(*) FROM field_conflicts fc WHERE fc.entity_type='release' AND fc.entity_id=r.id AND fc.status='pending')
		FROM releases r
		WHERE r.merged_into_id IS NULL AND ($1='' OR r.status=$1)
		AND ($2='' OR r.canonical_title ILIKE '%'||$2||'%' OR r.title_ja ILIKE '%'||$2||'%' OR r.slug ILIKE '%'||$2||'%'
		  OR r.id::text=$2 OR EXISTS(SELECT 1 FROM release_identifiers ri WHERE ri.release_id=r.id AND ri.normalized_value=$3))
		AND ($4=0 OR r.id<$4)
		ORDER BY r.id DESC LIMIT $5`, f.Status, q, catalog.NormalizeIdentifier(q), after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []AdminReleaseSummary{}
	for rows.Next() {
		var x AdminReleaseSummary
		if err = rows.Scan(&x.ID, &x.Slug, &x.Title, &x.TitleJA, &x.Status, &x.ReleaseDate, &x.ScheduledAt, &x.Version, &x.UpdatedAt,
			&x.Identifiers, &x.SourceCount, &x.OpenIssues, &x.PendingConflicts); err != nil {
			return nil, "", err
		}
		out = append(out, x)
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = EncodeCursor(Cursor{Sort: "admin_releases", ID: out[limit-1].ID})
	}
	return out, next, nil
}

// ---------------------------------------------------------------- detail

// AdminRelease is everything the AdminVideo page shows for one release.
// Screenshots and chapters (PRD 5.3) are not part of it: they need the
// transcoding pipeline, which does not exist yet.
type AdminRelease struct {
	ID             int64      `json:"id"`
	Slug           string     `json:"slug"`
	Title          string     `json:"title"`
	TitleJA        *string    `json:"title_ja"`
	Synopsis       *string    `json:"synopsis"`
	ReleaseDate    *string    `json:"release_date"`
	ShootYear      *int       `json:"shoot_year"`
	RuntimeMinutes *int       `json:"runtime_minutes"`
	MakerName      *string    `json:"maker"`
	LabelName      *string    `json:"label"`
	SeriesName     *string    `json:"series"`
	DirectorName   *string    `json:"director"`
	CensorshipType *string    `json:"censorship_type"`
	EditionType    string     `json:"edition_type"`
	Status         string     `json:"status"`
	ScheduledAt    *time.Time `json:"scheduled_at"`
	NotifiedAt     *time.Time `json:"notified_at"`
	AgeRating      *string    `json:"age_rating"`
	RegionAllow    []string   `json:"region_allow"`
	RegionDeny     []string   `json:"region_deny"`
	FeaturedNote   *string    `json:"featured_note"`
	Version        int64      `json:"version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`

	Identifiers  []AdminIdentifier       `json:"identifiers"`
	Tags         []AdminTag              `json:"tags"`
	Overrides    []Override              `json:"overrides"`
	Sources      []AdminPlaybackSource   `json:"sources"`
	MediaSources []AdminMediaSource      `json:"media_sources"`
	Performers   []AdminReleasePerformer `json:"performers"`
	Translations TranslationSummary      `json:"translations"`
	AuditIssues  []audit.Issue           `json:"audit_issues"`
	Conflicts    []map[string]any        `json:"conflicts"`
	ReviewEvents []ReviewEvent           `json:"review_events"`
}

// AdminIdentifier is one release_identifiers row.
type AdminIdentifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
	Scope string `json:"scope"`
}

// AdminTag is one release_tags row.
type AdminTag struct {
	Tag  string `json:"tag"`
	Slug string `json:"slug"`
}

// Override is one release_overrides / performer_overrides row.
type Override struct {
	Field      string          `json:"field"`
	Value      json.RawMessage `json:"value"`
	Reason     *string         `json:"reason"`
	Actor      string          `json:"actor"`
	ConflictID *int64          `json:"conflict_id"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// AdminPlaybackSource is a playback_sources row with everything the admin
// sees: licensing, regions and health.
type AdminPlaybackSource struct {
	ID              int64      `json:"id"`
	ReleaseID       int64      `json:"release_id"`
	Kind            string     `json:"kind"`
	Provider        string     `json:"provider"`
	Label           string     `json:"label"`
	URL             *string    `json:"url"`
	StoragePrefix   *string    `json:"storage_prefix"`
	MaxHeight       *int       `json:"max_height"`
	HasHDR          bool       `json:"has_hdr"`
	DurationSeconds *int       `json:"duration_seconds"`
	AudioLangs      []string   `json:"audio_langs"`
	SubtitleLangs   []string   `json:"subtitle_langs"`
	VersionKind     string     `json:"version_kind"`
	IsAltVersion    bool       `json:"is_alt_version"`
	Priority        int        `json:"priority"`
	RegionAllow     []string   `json:"region_allow"`
	RegionDeny      []string   `json:"region_deny"`
	LicenseRef      *string    `json:"license_ref"`
	Status          string     `json:"status"`
	HealthScore     int        `json:"health_score"` // 0–100 points
	HealthBand      string     `json:"health_band"`  // PRD 11 band, or insufficient_data
	HealthCheckedAt *time.Time `json:"health_checked_at"`
	// StatusChangedBy is the actor of the latest status change ("health"
	// when the source health job set it), nil when never changed.
	StatusChangedBy *string   `json:"status_changed_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// HealthDisabled reports whether the source health job made the latest
// status change (re-enabling such a source needs a reason).
func (x AdminPlaybackSource) HealthDisabled() bool {
	return x.Status != SourceStatusActive && x.StatusChangedBy != nil && *x.StatusChangedBy == audit.HealthActor
}

// Playback source statuses (playback_sources.status).
const (
	SourceStatusActive   = "active"
	SourceStatusDegraded = "degraded"
	SourceStatusDisabled = "disabled"
)

// AdminMediaSource is a crawled release_media_sources row, a candidate for
// promotion into playback_sources.
type AdminMediaSource struct {
	Source     string  `json:"source"`
	MediaID    string  `json:"media_id"`
	MediaURL   *string `json:"media_url"`
	Confidence string  `json:"confidence"`
	PromotedAs *int64  `json:"promoted_source_id"`
}

// AdminReleasePerformer is one credit with age at filming.
type AdminReleasePerformer struct {
	ID        int64   `json:"id"`
	Slug      string  `json:"slug"`
	Name      string  `json:"name"`
	NameJA    *string `json:"name_ja"`
	BirthDate *string `json:"birth_date"`
	// BirthDateHidden: the birthday is not public (PRD 15); the editor still
	// sees it and R-AGE still uses it.
	BirthDateHidden bool        `json:"birth_date_hidden"`
	Role            string      `json:"role"` // lead | guest
	IsPrimary       bool        `json:"is_primary"`
	BillingOrder    *int        `json:"billing_order"`
	CreditedName    *string     `json:"credited_name"`
	AgeAtFilming    *FilmingAge `json:"age_at_filming"`
	// AgeFlag is the R-AGE reason when this performer fails the rule.
	AgeFlag string `json:"age_flag,omitempty"`
}

// TranslationSummary is the translation status of a release's fields.
type TranslationSummary struct {
	Counts map[string]int      `json:"counts"` // by status, plus "missing"
	Items  []TranslationStatus `json:"items"`
}

// TranslationStatus is one translations row (or a missing one).
type TranslationStatus struct {
	Field     string     `json:"field"`
	Lang      string     `json:"lang"`
	Status    string     `json:"status"` // machine|draft|reviewed|stale|missing
	UpdatedAt *time.Time `json:"updated_at"`
}

// ReviewEvent is one review_events row.
type ReviewEvent struct {
	ID         int64           `json:"id"`
	ConflictID *int64          `json:"conflict_id"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Action     string          `json:"action"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
	Reason     *string         `json:"reason"`
	Actor      *string         `json:"actor"`
	CreatedAt  time.Time       `json:"created_at"`
}

const adminSourceSelect = `SELECT ps.id,ps.release_id,ps.kind,ps.provider,ps.label,ps.url,ps.storage_prefix,ps.max_height,ps.has_hdr,
	ps.duration_seconds,ps.audio_langs,ps.subtitle_langs,ps.version_kind,ps.is_alt_version,ps.priority,ps.region_allow,ps.region_deny,
	ps.license_ref,ps.status,ps.health_score,ps.health_checked_at,ev.actor,ps.created_at,ps.updated_at
	FROM playback_sources ps
	LEFT JOIN LATERAL (SELECT actor FROM review_events WHERE entity_type='playback_source' AND entity_id=ps.id AND action='status_change'
		ORDER BY id DESC LIMIT 1) ev ON true`

func scanAdminSource(row pgx.Row) (AdminPlaybackSource, error) {
	var x AdminPlaybackSource
	var health float32
	err := row.Scan(&x.ID, &x.ReleaseID, &x.Kind, &x.Provider, &x.Label, &x.URL, &x.StoragePrefix, &x.MaxHeight, &x.HasHDR,
		&x.DurationSeconds, &x.AudioLangs, &x.SubtitleLangs, &x.VersionKind, &x.IsAltVersion, &x.Priority, &x.RegionAllow, &x.RegionDeny,
		&x.LicenseRef, &x.Status, &health, &x.HealthCheckedAt, &x.StatusChangedBy, &x.CreatedAt, &x.UpdatedAt)
	x.HealthScore = audit.ScorePoints(float64(health))
	x.HealthBand = audit.HealthBand(x.HealthScore)
	if x.HealthCheckedAt == nil {
		x.HealthBand = audit.BandInsufficient
	}
	if x.AudioLangs == nil {
		x.AudioLangs = []string{}
	}
	if x.SubtitleLangs == nil {
		x.SubtitleLangs = []string{}
	}
	return x, err
}

// collect drains the result of a Query through scan, returning a non-nil
// slice: collect[T](s.Query(ctx, sql, args...))(scan).
func collect[T any](rows pgx.Rows, err error) func(scan func(pgx.Rows) (T, error)) ([]T, error) {
	return func(scan func(pgx.Rows) (T, error)) ([]T, error) {
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []T{}
		for rows.Next() {
			x, err := scan(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, rows.Err()
	}
}

// AdminPlaybackSources lists every source of a release (any status) in
// priority order. Unknown release: pgx.ErrNoRows.
func (s *Store) AdminPlaybackSources(ctx context.Context, releaseID int64) ([]AdminPlaybackSource, error) {
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE id=$1`, releaseID).Scan(&releaseID); err != nil {
		return nil, err
	}
	rows, err := s.Query(ctx, adminSourceSelect+` WHERE ps.release_id=$1 ORDER BY ps.priority,ps.id`, releaseID)
	return collect[AdminPlaybackSource](rows, err)(func(r pgx.Rows) (AdminPlaybackSource, error) { return scanAdminSource(r) })
}

func (s *Store) adminSource(ctx context.Context, q pgx.Tx, releaseID, sourceID int64) (AdminPlaybackSource, error) {
	if q != nil {
		return scanAdminSource(q.QueryRow(ctx, adminSourceSelect+` WHERE ps.id=$1 AND ps.release_id=$2`, sourceID, releaseID))
	}
	return scanAdminSource(s.QueryRow(ctx, adminSourceSelect+` WHERE ps.id=$1 AND ps.release_id=$2`, sourceID, releaseID))
}

// AdminRelease loads the full editor view of one release, whatever its
// status. Merged releases are returned too (status and merged state visible
// through review_events); unknown id: pgx.ErrNoRows.
func (s *Store) AdminRelease(ctx context.Context, id int64) (AdminRelease, error) {
	var x AdminRelease
	var releaseDate *time.Time
	var shootYear, runtime *int
	err := s.QueryRow(ctx, `SELECT id,slug,canonical_title,title_ja,synopsis,release_date,shoot_year,runtime_minutes,maker_name,label_name,
		series_name,director_name,censorship_type,edition_type,status,scheduled_at,notified_at,age_rating,region_allow,region_deny,
		featured_note,version,created_at,updated_at FROM releases WHERE id=$1`, id).Scan(&x.ID, &x.Slug, &x.Title, &x.TitleJA, &x.Synopsis,
		&releaseDate, &shootYear, &runtime, &x.MakerName, &x.LabelName, &x.SeriesName, &x.DirectorName, &x.CensorshipType, &x.EditionType,
		&x.Status, &x.ScheduledAt, &x.NotifiedAt, &x.AgeRating, &x.RegionAllow, &x.RegionDeny, &x.FeaturedNote, &x.Version, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return x, err
	}
	x.ShootYear, x.RuntimeMinutes = shootYear, runtime
	if releaseDate != nil {
		d := releaseDate.Format(time.DateOnly)
		x.ReleaseDate = &d
	}
	if x.Identifiers, err = collect[AdminIdentifier](s.Query(ctx, `SELECT identifier_type,identifier_value,identifier_scope FROM release_identifiers WHERE release_id=$1 ORDER BY id`, id))(
		func(r pgx.Rows) (AdminIdentifier, error) {
			var v AdminIdentifier
			return v, r.Scan(&v.Type, &v.Value, &v.Scope)
		}); err != nil {
		return x, err
	}
	if x.Tags, err = collect[AdminTag](s.Query(ctx, `SELECT tag,normalized_tag FROM release_tags WHERE release_id=$1 ORDER BY normalized_tag`, id))(
		func(r pgx.Rows) (AdminTag, error) {
			var v AdminTag
			return v, r.Scan(&v.Tag, &v.Slug)
		}); err != nil {
		return x, err
	}
	if x.Overrides, err = s.overrides(ctx, "release", id); err != nil {
		return x, err
	}
	rows, err := s.Query(ctx, adminSourceSelect+` WHERE ps.release_id=$1 ORDER BY ps.priority,ps.id`, id)
	if x.Sources, err = collect[AdminPlaybackSource](rows, err)(func(r pgx.Rows) (AdminPlaybackSource, error) { return scanAdminSource(r) }); err != nil {
		return x, err
	}
	if x.MediaSources, err = collect[AdminMediaSource](s.Query(ctx, `SELECT rms.source,rms.media_id,rms.media_url,rms.confidence,
		(SELECT ps.id FROM playback_sources ps WHERE ps.release_id=rms.release_id AND ps.provider=rms.source AND ps.url=rms.media_url ORDER BY ps.id LIMIT 1)
		FROM release_media_sources rms WHERE rms.release_id=$1 ORDER BY rms.source`, id))(
		func(r pgx.Rows) (AdminMediaSource, error) {
			var v AdminMediaSource
			return v, r.Scan(&v.Source, &v.MediaID, &v.MediaURL, &v.Confidence, &v.PromotedAs)
		}); err != nil {
		return x, err
	}
	if x.Performers, err = s.releasePerformersAdmin(ctx, id, releaseDate, shootYear); err != nil {
		return x, err
	}
	if x.Translations, err = s.translationSummary(ctx, id, x.Title, x.Synopsis); err != nil {
		return x, err
	}
	if x.AuditIssues, err = collect[audit.Issue](s.Query(ctx, `SELECT id,rule,entity_type,entity_id,severity,detail,priority,status,created_at,last_seen_at,resolved_at
		FROM audit_issues WHERE (entity_type='release' AND entity_id=$1)
		  OR (entity_type='playback_source' AND entity_id IN (SELECT id FROM playback_sources WHERE release_id=$1))
		ORDER BY (status='open') DESC,id DESC LIMIT 50`, id))(scanIssue); err != nil {
		return x, err
	}
	if x.Conflicts, err = collect[map[string]any](s.Query(ctx, `SELECT id,field_name,current_value,candidates,severity,created_at FROM field_conflicts
		WHERE entity_type='release' AND entity_id=$1 AND status='pending' ORDER BY id`, id))(
		func(r pgx.Rows) (map[string]any, error) {
			var cid int64
			var field, severity string
			var current, candidates []byte
			var created time.Time
			if err := r.Scan(&cid, &field, &current, &candidates, &severity, &created); err != nil {
				return nil, err
			}
			spec, ok := ConflictField("release", field)
			return map[string]any{"id": cid, "field_name": field, "column": spec.Column, "reviewable": ok, "current_value": rawOrNull(current),
				"candidates": json.RawMessage(candidates), "severity": severity, "created_at": created}, nil
		}); err != nil {
		return x, err
	}
	x.ReviewEvents, err = s.releaseReviewEvents(ctx, id, 50)
	return x, err
}

func scanIssue(r pgx.Rows) (audit.Issue, error) {
	var x audit.Issue
	var rule, sev string
	var detail []byte
	if err := r.Scan(&x.ID, &rule, &x.EntityType, &x.EntityID, &sev, &detail, &x.Priority, &x.Status, &x.CreatedAt, &x.LastSeenAt, &x.ResolvedAt); err != nil {
		return x, err
	}
	x.Rule, x.Severity = audit.Rule(rule), audit.Severity(sev)
	return x, json.Unmarshal(detail, &x.Detail)
}

func (s *Store) overrides(ctx context.Context, entityType string, id int64) ([]Override, error) {
	table, fk := "release_overrides", "release_id"
	if entityType == "performer" {
		table, fk = "performer_overrides", "performer_id"
	}
	return collect[Override](s.Query(ctx, fmt.Sprintf(`SELECT field_name,value,reason,actor,conflict_id,updated_at FROM %s WHERE %s=$1 ORDER BY field_name`, table, fk), id))(
		func(r pgx.Rows) (Override, error) {
			var v Override
			var raw []byte
			err := r.Scan(&v.Field, &raw, &v.Reason, &v.Actor, &v.ConflictID, &v.UpdatedAt)
			v.Value = rawOrNull(raw)
			return v, err
		})
}

func (s *Store) releasePerformersAdmin(ctx context.Context, id int64, releaseDate *time.Time, shootYear *int) ([]AdminReleasePerformer, error) {
	ps, err := collect[AdminReleasePerformer](s.Query(ctx, `SELECT p.id,p.slug,p.canonical_name,p.canonical_name_ja,p.birth_date,COALESCE(rp.role,''),rp.is_primary,
		rp.billing_order,rp.credited_name,p.birth_date_hidden FROM release_performers rp JOIN performers p ON p.id=rp.performer_id
		WHERE rp.release_id=$1 ORDER BY rp.is_primary DESC,rp.billing_order NULLS LAST,p.id`, id))(
		func(r pgx.Rows) (AdminReleasePerformer, error) {
			var v AdminReleasePerformer
			var birth *time.Time
			var order *int16
			if err := r.Scan(&v.ID, &v.Slug, &v.Name, &v.NameJA, &birth, &v.Role, &v.IsPrimary, &order, &v.CreditedName, &v.BirthDateHidden); err != nil {
				return v, err
			}
			if order != nil {
				o := int(*order)
				v.BillingOrder = &o
			}
			if birth != nil {
				d := birth.Format(time.DateOnly)
				v.BirthDate = &d
			}
			if v.Role == "" {
				v.Role = RoleGuest
				if v.IsPrimary {
					v.Role = RoleLead
				}
			}
			sy := 0
			if shootYear != nil {
				sy = *shootYear
			}
			v.AgeAtFilming = AgeAtFilming(birth, sy, releaseDate)
			return v, nil
		})
	if err != nil {
		return nil, err
	}
	facts, err := s.ReleaseFacts(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, f := range audit.CheckAge(facts, audit.DefaultAgeConfig) {
		for i := range ps {
			if ps[i].ID == f.PerformerID {
				ps[i].AgeFlag = f.Reason
			}
		}
	}
	return ps, nil
}

func (s *Store) translationSummary(ctx context.Context, id int64, title string, synopsis *string) (TranslationSummary, error) {
	sum := TranslationSummary{Counts: map[string]int{}}
	items, err := collect[TranslationStatus](s.Query(ctx, `SELECT field,lang,status,updated_at FROM translations WHERE entity_type='release' AND entity_id=$1 ORDER BY field,lang`, strconv.FormatInt(id, 10)))(
		func(r pgx.Rows) (TranslationStatus, error) {
			var v TranslationStatus
			return v, r.Scan(&v.Field, &v.Lang, &v.Status, &v.UpdatedAt)
		})
	if err != nil {
		return sum, err
	}
	have := map[string]bool{}
	for _, it := range items {
		have[it.Field+"/"+it.Lang] = true
		sum.Counts[it.Status]++
	}
	fields := []string{}
	if title != "" {
		fields = append(fields, "title")
	}
	if synopsis != nil && *synopsis != "" {
		fields = append(fields, "synopsis")
	}
	for _, f := range fields {
		for _, l := range i18n.Languages {
			if l == i18n.SourceLang || have[f+"/"+l] {
				continue
			}
			items = append(items, TranslationStatus{Field: f, Lang: l, Status: "missing"})
			sum.Counts["missing"]++
		}
	}
	sum.Items = items
	return sum, nil
}

func (s *Store) releaseReviewEvents(ctx context.Context, id int64, limit int) ([]ReviewEvent, error) {
	return collect[ReviewEvent](s.Query(ctx, `SELECT id,conflict_id,entity_type,entity_id,action,before_value,after_value,reason,actor,created_at
		FROM review_events WHERE (entity_type='release' AND entity_id=$1)
		  OR (entity_type='playback_source' AND entity_id IN (SELECT id FROM playback_sources WHERE release_id=$1))
		ORDER BY id DESC LIMIT $2`, id, limit))(
		func(r pgx.Rows) (ReviewEvent, error) {
			var v ReviewEvent
			var before, after []byte
			err := r.Scan(&v.ID, &v.ConflictID, &v.EntityType, &v.EntityID, &v.Action, &before, &after, &v.Reason, &v.Actor, &v.CreatedAt)
			v.Before, v.After = rawOrNull(before), rawOrNull(after)
			return v, err
		})
}

// ---------------------------------------------------------------- edits

// PatchResult is the outcome of PatchRelease.
type PatchResult struct {
	ReleaseID int64                `json:"release_id"`
	Version   int64                `json:"version"`
	Changed   []string             `json:"changed"`
	Audit     *audit.InboundResult `json:"audit"`
	Status    string               `json:"status"` // after the audit re-check (R-AGE may hold)
}

// lockRelease locks an unmerged release row and checks the optimistic lock.
func lockRelease(ctx context.Context, tx pgx.Tx, id, version int64) (status string, err error) {
	var cur int64
	var merged *int64
	if err = tx.QueryRow(ctx, `SELECT status,version,merged_into_id FROM releases WHERE id=$1 FOR UPDATE`, id).Scan(&status, &cur, &merged); err != nil {
		return "", err
	}
	if merged != nil {
		return "", conflict("release was merged into %d", *merged)
	}
	if version > 0 && version != cur {
		return "", conflict("release was changed by someone else (version %d, now %d)", version, cur)
	}
	return status, nil
}

func bumpRelease(ctx context.Context, tx pgx.Tx, id int64) (int64, error) {
	var v int64
	err := tx.QueryRow(ctx, `UPDATE releases SET version=version+1,updated_at=now() WHERE id=$1 RETURNING version`, id).Scan(&v)
	return v, err
}

func writeReviewEvent(ctx context.Context, tx pgx.Tx, entityType string, id int64, action string, before, after any, reason, actor string) error {
	b, err := json.Marshal(before)
	if err != nil {
		return err
	}
	a, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO review_events(entity_type,entity_id,action,before_value,after_value,reason,actor)
		VALUES($1,$2,$3,$4::jsonb,$5::jsonb,NULLIF($6,''),$7)`, entityType, id, action, string(b), string(a), reason, actor)
	return err
}

func releaseTagsJSON(ctx context.Context, tx pgx.Tx, id int64) (json.RawMessage, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(tag ORDER BY normalized_tag),'[]'::jsonb) FROM release_tags WHERE release_id=$1`, id).Scan(&raw)
	return raw, err
}

// PatchRelease applies a validated patch: every edited column is written and
// recorded as a manual override (so later crawls keep it), tags are
// replaced as a set, translations are marked stale, a single review_events
// row ('edit') records before / after, and the inbound audit (R-AGE +
// R-META) re-runs afterwards — R-AGE may hold the release. Unknown release:
// pgx.ErrNoRows; stale version or merged release: *ConflictError.
func (s *Store) PatchRelease(ctx context.Context, id int64, p ReleasePatch, actor string) (PatchResult, error) {
	res := PatchResult{ReleaseID: id}
	if p.Empty() {
		return res, invalid("no fields to update")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockRelease(ctx, tx, id, p.Version); err != nil {
		return res, err
	}
	before, after := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for _, e := range p.Columns {
		var b, a []byte
		if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT to_jsonb(%s) FROM releases WHERE id=$1`, e.Column), id).Scan(&b); err != nil {
			return res, err
		}
		var v any = e.Value
		if e.IsArray {
			v = e.Array
		}
		if err = tx.QueryRow(ctx, fmt.Sprintf(`UPDATE releases SET %[1]s=$1::%[2]s WHERE id=$2 RETURNING to_jsonb(%[1]s)`, e.Column, e.SQLType), v, id).Scan(&a); err != nil {
			return res, err
		}
		before[e.Column], after[e.Column] = rawOrNull(b), rawOrNull(a)
		if err = upsertOverride(ctx, tx, "release", id, e.Column, a, p.Reason, actor, 0); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, e.Column)
	}
	if p.Tags != nil {
		b, err := releaseTagsJSON(ctx, tx, id)
		if err != nil {
			return res, err
		}
		norm := make([]string, len(*p.Tags))
		for i, t := range *p.Tags {
			norm[i] = catalog.Slug(t)
		}
		if _, err = tx.Exec(ctx, `DELETE FROM release_tags WHERE release_id=$1 AND NOT (normalized_tag = ANY($2::text[]))`, id, norm); err != nil {
			return res, err
		}
		for i, t := range *p.Tags {
			if _, err = tx.Exec(ctx, `INSERT INTO release_tags(release_id,tag,normalized_tag) VALUES($1,$2,$3)
				ON CONFLICT(release_id,normalized_tag) DO UPDATE SET tag=EXCLUDED.tag`, id, t, norm[i]); err != nil {
				return res, err
			}
			if err = markStale(ctx, tx, i18n.EntityTag, norm[i]); err != nil {
				return res, err
			}
		}
		a, err := releaseTagsJSON(ctx, tx, id)
		if err != nil {
			return res, err
		}
		before[OverrideTags], after[OverrideTags] = b, a
		if err = upsertOverride(ctx, tx, "release", id, OverrideTags, a, p.Reason, actor, 0); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, OverrideTags)
	}
	if res.Version, err = bumpRelease(ctx, tx, id); err != nil {
		return res, err
	}
	if err = markStale(ctx, tx, i18n.EntityRelease, strconv.FormatInt(id, 10)); err != nil {
		return res, err
	}
	if err = writeReviewEvent(ctx, tx, "release", id, "edit", before, after, p.Reason, actor); err != nil {
		return res, err
	}
	if err = tx.Commit(ctx); err != nil {
		return res, err
	}
	return s.finishWithAudit(ctx, res)
}

func (s *Store) finishWithAudit(ctx context.Context, res PatchResult) (PatchResult, error) {
	in, err := audit.CheckRelease(ctx, s, res.ReleaseID, audit.DefaultAgeConfig, time.Now())
	if err != nil {
		return res, err
	}
	res.Audit = &in
	err = s.QueryRow(ctx, `SELECT status,version FROM releases WHERE id=$1`, res.ReleaseID).Scan(&res.Status, &res.Version)
	return res, err
}

// ClearReleaseOverride removes one override explicitly (FINAL_DATA_DESIGN 6:
// clearing must be an explicit action). The column keeps its value until the
// next crawl writes it. No such override: pgx.ErrNoRows.
func (s *Store) ClearReleaseOverride(ctx context.Context, id int64, field, reason, actor string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var raw []byte
	if err = tx.QueryRow(ctx, `DELETE FROM release_overrides WHERE release_id=$1 AND field_name=$2 RETURNING value`, id, field).Scan(&raw); err != nil {
		return err
	}
	if err = writeReviewEvent(ctx, tx, "release", id, "clear_override", map[string]any{"field": field, "value": rawOrNull(raw)}, map[string]any{"field": field}, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------------------------------------------------------------- status

// TransitionResult is the outcome of TransitionRelease.
type TransitionResult struct {
	ReleaseID   int64      `json:"release_id"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	ScheduledAt *time.Time `json:"scheduled_at"`
	Version     int64      `json:"version"`
}

// AgeGateError is returned when a release cannot be published because R-AGE
// still fails on its data. It is a conflict (409); Findings say why.
type AgeGateError struct{ Findings []audit.AgeFinding }

func (e *AgeGateError) Error() string {
	names := make([]string, len(e.Findings))
	for i, f := range e.Findings {
		names[i] = fmt.Sprintf("%s (%d, %s)", f.Name, f.Age, f.Reason)
	}
	return "R-AGE still fails: age at filming may be under 18: " + strings.Join(names, ", ") +
		"; fix shoot_year, the release date or the performer's birth date first"
}

// TransitionRelease changes a release's status by the rules of
// CheckTransition. Publishing (published / upcoming) also requires R-AGE to
// pass on the current data — dismissed findings do not count — so a held
// release is never published silently; lifting a hold additionally needs an
// override_reason, closes the open R-AGE issue and is recorded. Every
// transition writes a review_events row ('status_change').
func (s *Store) TransitionRelease(ctx context.Context, id int64, t Transition, actor string) (TransitionResult, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return TransitionResult{ReleaseID: id, To: t.To}, err
	}
	defer tx.Rollback(ctx)
	res, err := transitionReleaseTx(ctx, tx, id, t, actor)
	if err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// transitionReleaseTx is TransitionRelease inside the caller's transaction
// (copyright takedowns unpublish and restore through it).
func transitionReleaseTx(ctx context.Context, tx pgx.Tx, id int64, t Transition, actor string) (TransitionResult, error) {
	res := TransitionResult{ReleaseID: id, To: t.To}
	var err error
	if res.From, err = lockRelease(ctx, tx, id, t.Version); err != nil {
		return res, err
	}
	var scheduledBefore *time.Time
	if err = tx.QueryRow(ctx, `SELECT scheduled_at FROM releases WHERE id=$1`, id).Scan(&scheduledBefore); err != nil {
		return res, err
	}
	if err = CheckTransition(res.From, t, time.Now()); err != nil {
		return res, err
	}
	if NeedsAgeGate(t.To) {
		facts, err := scanReleaseFacts(tx.QueryRow(ctx, releaseFactsSelect+` WHERE r.id=$1`, id))
		if err != nil {
			return res, err
		}
		if fs := audit.CheckAge(facts, audit.DefaultAgeConfig); len(fs) > 0 {
			return res, &AgeGateError{Findings: fs}
		}
	}
	if err = tx.QueryRow(ctx, `UPDATE releases SET status=$2,scheduled_at=CASE WHEN $2='upcoming' THEN $3 ELSE scheduled_at END,
		version=version+1,updated_at=now() WHERE id=$1 RETURNING scheduled_at,version`, id, t.To, t.ScheduledAt).Scan(&res.ScheduledAt, &res.Version); err != nil {
		return res, err
	}
	reason := strings.TrimSpace(t.Reason)
	after := map[string]any{"status": t.To, "scheduled_at": res.ScheduledAt}
	if res.From == ReleaseHeld {
		reason = strings.TrimSpace(t.OverrideReason)
		after["override_reason"] = reason
		after["rule"] = audit.RuleAge
		if t.Reason != "" {
			after["note"] = t.Reason
		}
		if t.To == ReleasePublished {
			if _, err = tx.Exec(ctx, `UPDATE audit_issues SET status='fixed',resolved_at=now(),resolution_note=$2
				WHERE rule=$3 AND entity_type='release' AND entity_id=$1 AND status='open'`, id, "hold lifted by "+actor+": "+reason, string(audit.RuleAge)); err != nil {
				return res, err
			}
		}
	}
	if err = writeReviewEvent(ctx, tx, "release", id, "status_change", map[string]any{"status": res.From, "scheduled_at": scheduledBefore}, after, reason, actor); err != nil {
		return res, err
	}
	return res, nil
}

// ---------------------------------------------------------------- playback sources

func (in SourceInput) auditJSON() map[string]any {
	return map[string]any{"kind": in.Kind, "provider": in.Provider, "label": in.Label, "url": in.URL, "storage_prefix": in.StoragePrefix,
		"max_height": in.MaxHeight, "has_hdr": in.HasHDR, "duration_seconds": in.DurationSeconds, "audio_langs": in.AudioLangs,
		"subtitle_langs": in.SubtitleLangs, "version_kind": in.VersionKind, "priority": in.Priority, "region_allow": in.RegionAllow,
		"region_deny": in.RegionDeny, "license_ref": in.LicenseRef}
}

func sourceInputOf(x AdminPlaybackSource) SourceInput {
	p := x.Priority
	return SourceInput{Kind: x.Kind, Provider: x.Provider, Label: x.Label, URL: x.URL, StoragePrefix: x.StoragePrefix, MaxHeight: x.MaxHeight,
		HasHDR: x.HasHDR, DurationSeconds: x.DurationSeconds, AudioLangs: x.AudioLangs, SubtitleLangs: x.SubtitleLangs, VersionKind: x.VersionKind,
		Priority: &p, RegionAllow: x.RegionAllow, RegionDeny: x.RegionDeny, LicenseRef: x.LicenseRef}
}

// altVersion applies the PRD 5.2 2% rule against the release's other main
// sources (or its runtime) and returns the stored version kind.
func altVersion(ctx context.Context, tx pgx.Tx, releaseID, selfID int64, in SourceInput) (string, error) {
	var runtime int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(runtime_minutes,0) FROM releases WHERE id=$1`, releaseID).Scan(&runtime); err != nil {
		return "", err
	}
	mains, err := collect[PlaybackSource](tx.Query(ctx, `SELECT id,duration_seconds,priority,status FROM playback_sources WHERE release_id=$1 AND version_kind='main' AND id<>$2`, releaseID, selfID))(
		func(r pgx.Rows) (PlaybackSource, error) {
			x := PlaybackSource{VersionKind: VersionMain}
			return x, r.Scan(&x.ID, &x.DurationSeconds, &x.Priority, &x.Status)
		})
	if err != nil {
		return "", err
	}
	src := PlaybackSource{VersionKind: in.VersionKind, DurationSeconds: in.DurationSeconds}
	ApplyAltVersionRule(&src, ReferenceDuration(mains, runtime))
	return src.VersionKind, nil
}

// CreatePlaybackSource adds a source to a release (status active).
func (s *Store) CreatePlaybackSource(ctx context.Context, releaseID int64, in SourceInput, actor string) (AdminPlaybackSource, error) {
	if err := in.Normalize(); err != nil {
		return AdminPlaybackSource{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminPlaybackSource{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockRelease(ctx, tx, releaseID, 0); err != nil {
		return AdminPlaybackSource{}, err
	}
	if in.VersionKind, err = altVersion(ctx, tx, releaseID, 0, in); err != nil {
		return AdminPlaybackSource{}, err
	}
	var id int64
	if err = tx.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,storage_prefix,max_height,has_hdr,duration_seconds,
		audio_langs,subtitle_langs,version_kind,is_alt_version,priority,region_allow,region_deny,license_ref)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12='alt',$13,$14,$15,$16) RETURNING id`,
		releaseID, in.Kind, in.Provider, in.Label, in.URL, in.StoragePrefix, in.MaxHeight, in.HasHDR, in.DurationSeconds,
		in.AudioLangs, in.SubtitleLangs, in.VersionKind, *in.Priority, in.RegionAllow, in.RegionDeny, in.LicenseRef).Scan(&id); err != nil {
		return AdminPlaybackSource{}, err
	}
	if err = writeReviewEvent(ctx, tx, "playback_source", id, "create", nil, in.auditJSON(), "", actor); err != nil {
		return AdminPlaybackSource{}, err
	}
	x, err := s.adminSource(ctx, tx, releaseID, id)
	if err != nil {
		return x, err
	}
	return x, tx.Commit(ctx)
}

// UpdatePlaybackSource applies a partial JSON patch (see ApplySourcePatch)
// to one source of the release. Status changes go through
// SetPlaybackSourceEnabled. Unknown source: pgx.ErrNoRows.
func (s *Store) UpdatePlaybackSource(ctx context.Context, releaseID, sourceID int64, patch json.RawMessage, actor string) (AdminPlaybackSource, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminPlaybackSource{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM playback_sources WHERE id=$1 AND release_id=$2 FOR UPDATE`, sourceID, releaseID); err != nil {
		return AdminPlaybackSource{}, err
	}
	cur, err := s.adminSource(ctx, tx, releaseID, sourceID)
	if err != nil {
		return cur, err
	}
	in := sourceInputOf(cur)
	before := in.auditJSON()
	if err = ApplySourcePatch(&in, patch); err != nil {
		return cur, err
	}
	if in.VersionKind, err = altVersion(ctx, tx, releaseID, sourceID, in); err != nil {
		return cur, err
	}
	if _, err = tx.Exec(ctx, `UPDATE playback_sources SET kind=$3,provider=$4,label=$5,url=$6,storage_prefix=$7,max_height=$8,has_hdr=$9,
		duration_seconds=$10,audio_langs=$11,subtitle_langs=$12,version_kind=$13,is_alt_version=($13='alt'),priority=$14,region_allow=$15,
		region_deny=$16,license_ref=$17,updated_at=now() WHERE id=$1 AND release_id=$2`, sourceID, releaseID, in.Kind, in.Provider, in.Label, in.URL,
		in.StoragePrefix, in.MaxHeight, in.HasHDR, in.DurationSeconds, in.AudioLangs, in.SubtitleLangs, in.VersionKind, *in.Priority,
		in.RegionAllow, in.RegionDeny, in.LicenseRef); err != nil {
		return cur, err
	}
	if err = writeReviewEvent(ctx, tx, "playback_source", sourceID, "edit", before, in.auditJSON(), "", actor); err != nil {
		return cur, err
	}
	x, err := s.adminSource(ctx, tx, releaseID, sourceID)
	if err != nil {
		return x, err
	}
	return x, tx.Commit(ctx)
}

// ReorderPlaybackSources sets priorities from the order of ids (10, 20, ...).
// ids must list every source of the release exactly once.
func (s *Store) ReorderPlaybackSources(ctx context.Context, releaseID int64, ids []int64, actor string) ([]AdminPlaybackSource, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockRelease(ctx, tx, releaseID, 0); err != nil {
		return nil, err
	}
	type prio struct {
		ID       int64 `json:"id"`
		Priority int   `json:"priority"`
	}
	cur, err := collect[prio](tx.Query(ctx, `SELECT id,priority FROM playback_sources WHERE release_id=$1 ORDER BY priority,id FOR UPDATE`, releaseID))(
		func(r pgx.Rows) (prio, error) {
			var p prio
			return p, r.Scan(&p.ID, &p.Priority)
		})
	if err != nil {
		return nil, err
	}
	if err = sameIDSet(ids, cur, func(p prio) int64 { return p.ID }); err != nil {
		return nil, err
	}
	next := make([]prio, len(ids))
	for i, id := range ids {
		next[i] = prio{ID: id, Priority: (i + 1) * 10}
		if _, err = tx.Exec(ctx, `UPDATE playback_sources SET priority=$2,updated_at=now() WHERE id=$1`, id, next[i].Priority); err != nil {
			return nil, err
		}
	}
	if err = writeReviewEvent(ctx, tx, "release", releaseID, "reorder_sources", cur, next, "", actor); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.AdminPlaybackSources(ctx, releaseID)
}

func sameIDSet[T any](ids []int64, cur []T, id func(T) int64) error {
	if len(ids) != len(cur) {
		return invalid("ids must list all %d sources of the release exactly once", len(cur))
	}
	want := map[int64]bool{}
	for _, c := range cur {
		want[id(c)] = true
	}
	seen := map[int64]bool{}
	for _, x := range ids {
		if !want[x] || seen[x] {
			return invalid("ids must list all %d sources of the release exactly once", len(cur))
		}
		seen[x] = true
	}
	return nil
}

// SetPlaybackSourceEnabled disables or (re-)enables a source. Re-enabling a
// source whose latest status change came from the source health job needs a
// reason; it also closes the open R-SOURCE issue. Same status: *ConflictError.
func (s *Store) SetPlaybackSourceEnabled(ctx context.Context, releaseID, sourceID int64, enable bool, reason, actor string) (AdminPlaybackSource, error) {
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxReasonLen {
		return AdminPlaybackSource{}, invalid("reason too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminPlaybackSource{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT 1 FROM playback_sources WHERE id=$1 AND release_id=$2 FOR UPDATE`, sourceID, releaseID); err != nil {
		return AdminPlaybackSource{}, err
	}
	cur, err := s.adminSource(ctx, tx, releaseID, sourceID)
	if err != nil {
		return cur, err
	}
	to := SourceStatusDisabled
	if enable {
		to = SourceStatusActive
		if cur.Status == SourceStatusActive {
			return cur, conflict("source is already active")
		}
		if cur.HealthDisabled() && reason == "" {
			return cur, invalid("reason is required to re-enable a source the health job %s", cur.Status)
		}
	} else if cur.Status == SourceStatusDisabled {
		return cur, conflict("source is already disabled")
	}
	if _, err = tx.Exec(ctx, `UPDATE playback_sources SET status=$2,updated_at=now() WHERE id=$1`, sourceID, to); err != nil {
		return cur, err
	}
	if err = writeReviewEvent(ctx, tx, "playback_source", sourceID, "status_change",
		map[string]any{"status": cur.Status, "health_score": cur.HealthScore, "set_by": cur.StatusChangedBy},
		map[string]any{"status": to}, reason, actor); err != nil {
		return cur, err
	}
	if enable {
		if _, err = tx.Exec(ctx, `UPDATE audit_issues SET status='fixed',resolved_at=now(),resolution_note=$3
			WHERE rule=$1 AND entity_type='playback_source' AND entity_id=$2 AND status='open'`,
			string(audit.RuleSourceHealth), sourceID, "re-enabled by "+actor+": "+reason); err != nil {
			return cur, err
		}
	}
	x, err := s.adminSource(ctx, tx, releaseID, sourceID)
	if err != nil {
		return x, err
	}
	return x, tx.Commit(ctx)
}

// AdminPromoteMediaSource validates and wraps PromoteMediaSource for the
// admin API: the kind may not be hls_owned, a license_ref is required unless
// the kind is a preview or an external link, and the crawled row must have a
// URL. Unknown media source: pgx.ErrNoRows.
func (s *Store) AdminPromoteMediaSource(ctx context.Context, in PromoteMediaSourceInput, actor string) (AdminPlaybackSource, error) {
	in.Kind, in.Label, in.LicenseRef = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Label), strings.TrimSpace(in.LicenseRef)
	switch {
	case !ValidPlaybackKind(in.Kind) || in.Kind == KindHLSOwned:
		return AdminPlaybackSource{}, invalid("kind must be official_embed, official_preview or external_link")
	case in.VersionKind != "" && !ValidVersionKind(in.VersionKind):
		return AdminPlaybackSource{}, invalid("version_kind must be main, high_quality, dub or alt")
	case in.Label == "" || len(in.Label) > 100:
		return AdminPlaybackSource{}, invalid("label is required (at most 100 characters)")
	case RequiresLicense(in.Kind) && in.LicenseRef == "":
		return AdminPlaybackSource{}, invalid("license_ref is required for %s sources", in.Kind)
	case in.Priority < 0 || in.Priority > 100000:
		return AdminPlaybackSource{}, invalid("priority must be between 0 and 100000")
	case in.MaxHeight != nil && (*in.MaxHeight < 1 || *in.MaxHeight > 4320):
		return AdminPlaybackSource{}, invalid("max_height must be between 1 and 4320")
	case in.DurationSeconds != nil && (*in.DurationSeconds < 1 || *in.DurationSeconds > 86400):
		return AdminPlaybackSource{}, invalid("duration_seconds must be between 1 and 86400")
	}
	var mediaURL string
	if err := s.QueryRow(ctx, `SELECT COALESCE(media_url,'') FROM release_media_sources WHERE release_id=$1 AND lower(source)=lower($2)`, in.ReleaseID, in.Source).Scan(&mediaURL); err != nil {
		return AdminPlaybackSource{}, err
	}
	if mediaURL == "" {
		return AdminPlaybackSource{}, invalid("media source has no url to promote")
	}
	id, err := s.PromoteMediaSource(ctx, in)
	if err != nil {
		return AdminPlaybackSource{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return AdminPlaybackSource{}, err
	}
	defer tx.Rollback(ctx)
	if err = writeReviewEvent(ctx, tx, "playback_source", id, "promote", nil,
		map[string]any{"from": "release_media_sources", "source": in.Source, "kind": in.Kind, "label": in.Label, "license_ref": in.LicenseRef}, "", actor); err != nil {
		return AdminPlaybackSource{}, err
	}
	x, err := s.adminSource(ctx, tx, in.ReleaseID, id)
	if err != nil {
		return x, err
	}
	return x, tx.Commit(ctx)
}

// ---------------------------------------------------------------- performers

func releasePerformersJSON(ctx context.Context, tx pgx.Tx, id int64) (json.RawMessage, error) {
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(jsonb_build_object('performer_id',performer_id,'role',role,'is_primary',is_primary,
		'billing_order',billing_order,'credited_name',credited_name) ORDER BY performer_id),'[]'::jsonb) FROM release_performers WHERE release_id=$1`, id).Scan(&raw)
	return raw, err
}

// SetReleasePerformer adds or updates a performer credit (role lead|guest;
// lead sets is_primary). The credit list becomes a manual override, so a
// crawl neither re-adds removed performers nor changes roles. R-AGE re-runs
// afterwards (a new performer may hold the release).
func (s *Store) SetReleasePerformer(ctx context.Context, releaseID int64, in ReleasePerformerInput, actor string) (PatchResult, error) {
	res := PatchResult{ReleaseID: releaseID, Changed: []string{OverridePerformers}}
	if err := in.Normalize(); err != nil {
		return res, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockRelease(ctx, tx, releaseID, 0); err != nil {
		return res, err
	}
	var merged *int64
	if err = tx.QueryRow(ctx, `SELECT merged_into_id FROM performers WHERE id=$1`, in.PerformerID).Scan(&merged); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, invalid("performer %d does not exist", in.PerformerID)
		}
		return res, err
	}
	if merged != nil {
		return res, invalid("performer %d was merged into %d", in.PerformerID, *merged)
	}
	before, err := releasePerformersJSON(ctx, tx, releaseID)
	if err != nil {
		return res, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO release_performers(release_id,performer_id,billing_order,role,credited_name,is_primary)
		VALUES($1,$2,$3,$4,$5,$4='lead') ON CONFLICT(release_id,performer_id) DO UPDATE SET role=EXCLUDED.role,is_primary=EXCLUDED.is_primary,
		billing_order=COALESCE(EXCLUDED.billing_order,release_performers.billing_order),
		credited_name=COALESCE(EXCLUDED.credited_name,release_performers.credited_name)`,
		releaseID, in.PerformerID, in.BillingOrder, in.Role, in.CreditedName); err != nil {
		return res, err
	}
	if err = s.finishPerformerEdit(ctx, tx, releaseID, before, "set_performer", in.Reason, actor); err != nil {
		return res, err
	}
	return s.finishWithAudit(ctx, res)
}

// RemoveReleasePerformer removes a credit (recorded as an override).
// Not credited: pgx.ErrNoRows.
func (s *Store) RemoveReleasePerformer(ctx context.Context, releaseID, performerID int64, reason, actor string) (PatchResult, error) {
	res := PatchResult{ReleaseID: releaseID, Changed: []string{OverridePerformers}}
	if len(reason) > MaxReasonLen {
		return res, invalid("reason too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockRelease(ctx, tx, releaseID, 0); err != nil {
		return res, err
	}
	before, err := releasePerformersJSON(ctx, tx, releaseID)
	if err != nil {
		return res, err
	}
	tag, err := tx.Exec(ctx, `DELETE FROM release_performers WHERE release_id=$1 AND performer_id=$2`, releaseID, performerID)
	if err != nil {
		return res, err
	}
	if tag.RowsAffected() == 0 {
		return res, pgx.ErrNoRows
	}
	if err = s.finishPerformerEdit(ctx, tx, releaseID, before, "remove_performer", reason, actor); err != nil {
		return res, err
	}
	return s.finishWithAudit(ctx, res)
}

func (s *Store) finishPerformerEdit(ctx context.Context, tx pgx.Tx, releaseID int64, before json.RawMessage, action, reason, actor string) error {
	after, err := releasePerformersJSON(ctx, tx, releaseID)
	if err != nil {
		return err
	}
	if err = upsertOverride(ctx, tx, "release", releaseID, OverridePerformers, after, reason, actor, 0); err != nil {
		return err
	}
	if _, err = bumpRelease(ctx, tx, releaseID); err != nil {
		return err
	}
	if err = writeReviewEvent(ctx, tx, "release", releaseID, action, map[string]any{"performers": before}, map[string]any{"performers": after}, reason, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
