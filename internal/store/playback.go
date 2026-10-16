package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Playback source kinds (tech plan 3.2).
const (
	KindOfficialEmbed   = "official_embed"
	KindOfficialPreview = "official_preview"
	KindExternalLink    = "external_link"
	KindHLSOwned        = "hls_owned"
)

// Version kinds (PRD 5.2). Only VersionAlt keeps its own playback progress.
const (
	VersionMain        = "main"
	VersionHighQuality = "high_quality"
	VersionDub         = "dub"
	VersionAlt         = "alt"
)

// AltDurationTolerance is the PRD 5.2 rule: a source whose duration differs
// from the main version by more than 2% is an alternate version.
const AltDurationTolerance = 0.02

// PlaybackRelease is the release summary returned with a play config.
type PlaybackRelease struct {
	ID              int64  `json:"-"`
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	RuntimeMinutes  int    `json:"-"`
	DurationSeconds *int   `json:"duration_seconds"`
}

// PlaybackSource is one row of playback_sources.
type PlaybackSource struct {
	ID              int64    `json:"id"`
	ReleaseID       int64    `json:"-"`
	Kind            string   `json:"kind"`
	Provider        string   `json:"provider"`
	Label           string   `json:"label"`
	URL             *string  `json:"url"`
	StoragePrefix   *string  `json:"-"`
	MaxHeight       *int     `json:"max_height"`
	HasHDR          bool     `json:"has_hdr"`
	DurationSeconds *int     `json:"duration_seconds"`
	AudioLangs      []string `json:"audio_langs"`
	SubtitleLangs   []string `json:"subtitle_langs"`
	VersionKind     string   `json:"version_kind"`
	IsAltVersion    bool     `json:"is_alt_version"`
	Priority        int      `json:"priority"`
	RegionAllow     []string `json:"-"`
	RegionDeny      []string `json:"-"`
	LicenseRef      *string  `json:"-"`
	HealthScore     float32  `json:"-"`
	Status          string   `json:"-"`
}

// PlaybackForRelease loads a published release and all of its playback
// sources (any status). Callers run PlanPlayback on the result. An unknown or
// unpublished slug returns pgx.ErrNoRows.
func (s *Store) PlaybackForRelease(ctx context.Context, slug string) (PlaybackRelease, []PlaybackSource, error) {
	var rel PlaybackRelease
	err := s.QueryRow(ctx, `SELECT id,slug,canonical_title,COALESCE(runtime_minutes,0) FROM releases
		WHERE slug=$1 AND status='published' AND merged_into_id IS NULL`, slug).Scan(&rel.ID, &rel.Slug, &rel.Title, &rel.RuntimeMinutes)
	if err != nil {
		return rel, nil, err
	}
	rows, err := s.Query(ctx, `SELECT id,release_id,kind,provider,label,url,storage_prefix,max_height,has_hdr,
		duration_seconds,audio_langs,subtitle_langs,version_kind,is_alt_version,priority,region_allow,region_deny,
		license_ref,health_score,status FROM playback_sources WHERE release_id=$1 ORDER BY priority,id`, rel.ID)
	if err != nil {
		return rel, nil, err
	}
	defer rows.Close()
	var out []PlaybackSource
	for rows.Next() {
		var x PlaybackSource
		if err = rows.Scan(&x.ID, &x.ReleaseID, &x.Kind, &x.Provider, &x.Label, &x.URL, &x.StoragePrefix, &x.MaxHeight, &x.HasHDR,
			&x.DurationSeconds, &x.AudioLangs, &x.SubtitleLangs, &x.VersionKind, &x.IsAltVersion, &x.Priority, &x.RegionAllow, &x.RegionDeny,
			&x.LicenseRef, &x.HealthScore, &x.Status); err != nil {
			return rel, nil, err
		}
		out = append(out, x)
	}
	return rel, out, rows.Err()
}

// ResumePosition returns the user's saved position for a release from
// watch_history. ok is false when there is nothing to resume.
func (s *Store) ResumePosition(ctx context.Context, userID, releaseID int64) (int, bool, error) {
	var pos int
	err := s.QueryRow(ctx, `SELECT progress_seconds FROM watch_history WHERE user_id=$1 AND release_id=$2`, userID, releaseID).Scan(&pos)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return pos, pos > 0, nil
}

// PlaybackPlan is the outcome of PlanPlayback.
type PlaybackPlan struct {
	// DurationSeconds is the reference (main version) duration, if known.
	DurationSeconds *int
	// Sources are playable for the region, in the order the player should try them.
	Sources []PlaybackSource
}

// PlanPlayback applies the tech plan 3.3 selection rules to all sources of a
// release: derive the reference duration and alt-version flags, keep active
// sources allowed in region, then order by quality match, health and priority.
// region is an ISO 3166-1 alpha-2 code ("" when unknown); quality is the
// preferred max height (0 for no preference).
func PlanPlayback(rel PlaybackRelease, all []PlaybackSource, region string, quality int) PlaybackPlan {
	ref := ReferenceDuration(all, rel.RuntimeMinutes)
	srcs := make([]PlaybackSource, len(all))
	copy(srcs, all)
	for i := range srcs {
		ApplyAltVersionRule(&srcs[i], ref)
	}
	srcs = FilterPlayable(srcs, region)
	OrderPlaybackSources(srcs, quality)
	return PlaybackPlan{DurationSeconds: ref, Sources: srcs}
}

// ReferenceDuration is the duration other versions are compared against: the
// main-version source with a known duration (active first, then by priority),
// falling back to the catalog runtime. nil when neither is known.
func ReferenceDuration(all []PlaybackSource, runtimeMinutes int) *int {
	var best *PlaybackSource
	for i := range all {
		x := &all[i]
		if x.VersionKind != VersionMain || x.DurationSeconds == nil || *x.DurationSeconds <= 0 {
			continue
		}
		if best == nil || refBetter(x, best) {
			best = x
		}
	}
	if best != nil {
		d := *best.DurationSeconds
		return &d
	}
	if runtimeMinutes > 0 {
		d := runtimeMinutes * 60
		return &d
	}
	return nil
}

func refBetter(a, b *PlaybackSource) bool {
	aa, ba := a.Status == "active", b.Status == "active"
	if aa != ba {
		return aa
	}
	if a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	return a.ID < b.ID
}

// IsAltDuration reports whether duration differs from reference by more than
// AltDurationTolerance. Unknown or non-positive values never count as alt.
func IsAltDuration(duration, reference int) bool {
	if duration <= 0 || reference <= 0 {
		return false
	}
	diff := duration - reference
	if diff < 0 {
		diff = -diff
	}
	// Integer form of diff/reference > AltDurationTolerance (1/50), exact at the boundary.
	return diff*50 > reference
}

// ApplyAltVersionRule sets VersionKind / IsAltVersion consistently: a source
// stored as alt stays alt; any other source whose duration differs from ref by
// more than 2% becomes alt. Without duration data the stored value is kept.
func ApplyAltVersionRule(x *PlaybackSource, ref *int) {
	if x.VersionKind == "" {
		x.VersionKind = VersionMain
	}
	if x.VersionKind != VersionAlt && ref != nil && x.DurationSeconds != nil && IsAltDuration(*x.DurationSeconds, *ref) {
		x.VersionKind = VersionAlt
	}
	x.IsAltVersion = x.VersionKind == VersionAlt
}

// RegionAllowed applies region_allow / region_deny. An unknown region ("")
// is only allowed for sources without an allow list.
func RegionAllowed(x PlaybackSource, region string) bool {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region != "" && containsFold(x.RegionDeny, region) {
		return false
	}
	if len(x.RegionAllow) > 0 {
		return region != "" && containsFold(x.RegionAllow, region)
	}
	return true
}

func containsFold(list []string, v string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(strings.TrimSpace(s), v) })
}

// FilterPlayable keeps active sources allowed in region, preserving order.
func FilterPlayable(srcs []PlaybackSource, region string) []PlaybackSource {
	out := make([]PlaybackSource, 0, len(srcs))
	for _, x := range srcs {
		if x.Status == "active" && RegionAllowed(x, region) {
			out = append(out, x)
		}
	}
	return out
}

// qualityRank: 0 exact match of the preferred height, 1 higher than preferred
// (can still deliver it), 2 lower or unknown. Without a preference all rank 0.
func qualityRank(x PlaybackSource, quality int) int {
	if quality <= 0 {
		return 0
	}
	switch {
	case x.MaxHeight == nil:
		return 2
	case *x.MaxHeight == quality:
		return 0
	case *x.MaxHeight > quality:
		return 1
	default:
		return 2
	}
}

// OrderPlaybackSources sorts in place. Alternate versions go after every
// shared-progress source (PRD 5.2: the default is the main version and the
// automatic fallback chain must keep progress), then tech plan 3.3: quality
// preference match, health_score (high first), priority (low first), id.
// Call after ApplyAltVersionRule.
func OrderPlaybackSources(srcs []PlaybackSource, quality int) {
	slices.SortStableFunc(srcs, func(a, b PlaybackSource) int {
		if a.IsAltVersion != b.IsAltVersion {
			if b.IsAltVersion {
				return -1
			}
			return 1
		}
		if d := qualityRank(a, quality) - qualityRank(b, quality); d != 0 {
			return d
		}
		if a.HealthScore != b.HealthScore {
			if a.HealthScore > b.HealthScore {
				return -1
			}
			return 1
		}
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
}

// ResumeSource picks the source a shared-progress resume applies to: the first
// non-alt source in play order. ok is false when every source is an alt version.
func ResumeSource(ordered []PlaybackSource) (int64, bool) {
	for _, x := range ordered {
		if !x.IsAltVersion {
			return x.ID, true
		}
	}
	return 0, false
}

// PromoteMediaSourceInput describes a reviewed mapping of a crawled
// release_media_sources row into playback_sources.
type PromoteMediaSourceInput struct {
	ReleaseID       int64
	Source          string // release_media_sources.source; becomes provider
	Kind            string
	Label           string
	VersionKind     string // defaults to main
	Priority        int    // defaults to 100
	MaxHeight       *int
	DurationSeconds *int
	LicenseRef      string
}

// ValidPlaybackKind reports whether kind is an allowed playback_sources.kind.
func ValidPlaybackKind(kind string) bool {
	switch kind {
	case KindOfficialEmbed, KindOfficialPreview, KindExternalLink, KindHLSOwned:
		return true
	}
	return false
}

// ValidVersionKind reports whether v is an allowed playback_sources.version_kind.
func ValidVersionKind(v string) bool {
	switch v {
	case VersionMain, VersionHighQuality, VersionDub, VersionAlt:
		return true
	}
	return false
}

// PromoteMediaSource copies one release_media_sources row into
// playback_sources. It is meant to be called from a reviewed admin action
// (no endpoint yet) and is idempotent: if a source with the same release,
// provider and URL already exists its id is returned unchanged. The 2% rule
// is applied against the release's existing main version.
func (s *Store) PromoteMediaSource(ctx context.Context, in PromoteMediaSourceInput) (int64, error) {
	if !ValidPlaybackKind(in.Kind) || in.Kind == KindHLSOwned {
		return 0, fmt.Errorf("promote media source: invalid kind %q", in.Kind)
	}
	if in.VersionKind == "" {
		in.VersionKind = VersionMain
	}
	if !ValidVersionKind(in.VersionKind) {
		return 0, fmt.Errorf("promote media source: invalid version kind %q", in.VersionKind)
	}
	if strings.TrimSpace(in.Label) == "" {
		return 0, errors.New("promote media source: label required")
	}
	if in.Priority == 0 {
		in.Priority = 100
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var provider, mediaURL string
	var runtime int
	err = tx.QueryRow(ctx, `SELECT rms.source,COALESCE(rms.media_url,''),COALESCE(r.runtime_minutes,0)
		FROM release_media_sources rms JOIN releases r ON r.id=rms.release_id
		WHERE rms.release_id=$1 AND lower(rms.source)=lower($2) FOR UPDATE OF rms`, in.ReleaseID, in.Source).Scan(&provider, &mediaURL, &runtime)
	if err != nil {
		return 0, err
	}
	if mediaURL == "" {
		return 0, errors.New("promote media source: media source has no url")
	}
	var id int64
	err = tx.QueryRow(ctx, `SELECT id FROM playback_sources WHERE release_id=$1 AND provider=$2 AND url=$3 ORDER BY id LIMIT 1`,
		in.ReleaseID, provider, mediaURL).Scan(&id)
	if err == nil {
		return id, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}

	var mains []PlaybackSource
	rows, err := tx.Query(ctx, `SELECT id,duration_seconds,priority,status FROM playback_sources
		WHERE release_id=$1 AND version_kind='main'`, in.ReleaseID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		x := PlaybackSource{VersionKind: VersionMain}
		if err = rows.Scan(&x.ID, &x.DurationSeconds, &x.Priority, &x.Status); err != nil {
			rows.Close()
			return 0, err
		}
		mains = append(mains, x)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, err
	}
	src := PlaybackSource{VersionKind: in.VersionKind, DurationSeconds: in.DurationSeconds}
	ApplyAltVersionRule(&src, ReferenceDuration(mains, runtime))

	err = tx.QueryRow(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,max_height,duration_seconds,
		version_kind,is_alt_version,priority,license_ref) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')) RETURNING id`,
		in.ReleaseID, in.Kind, provider, in.Label, mediaURL, in.MaxHeight, in.DurationSeconds,
		src.VersionKind, src.IsAltVersion, in.Priority, in.LicenseRef).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}
