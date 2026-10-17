package store

// 片段 (clips, migration 054): a 5–60 s segment of a published release,
// shared under a short public id. Only signed-in users create them; the
// author deletes the row. Moderators act on them through the shared report
// inbox (status visible | shadow | folded | deleted, like comments). A title
// that hits a moderation keyword is shadowed (migration 055 title_status):
// the clip stays public, but only its author sees the title until a
// moderator approves or removes it (the 待审片段 queue, moderation.go).

import (
	"avxxx/internal/model"
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Clip limits (shared with web/app/lib/clipRange.ts).
const (
	ClipMinMs       = 5_000
	ClipMaxMs       = 60_000
	ClipTitleMax    = 100
	clipIDLen       = 10
	clipIDAlphabet  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	clipInsertTries = 5
)

// Clip title statuses (clips.title_status, migration 055).
const (
	ClipTitleVisible = "visible"
	ClipTitleShadow  = "shadow"  // keyword hit: only the author sees the title
	ClipTitleRemoved = "removed" // a moderator cleared it
)

// clipTitleSQL is the title a viewer ($2) may see: a shadowed title only
// reaches its author.
const clipTitleSQL = `CASE WHEN c.title_status='visible' OR c.user_id=$2 THEN c.title ELSE '' END`

// Clip is a clip as the API returns it.
type Clip struct {
	ID           string `json:"id"` // public id
	ReleaseSlug  string `json:"release_slug"`
	ReleaseTitle string `json:"release_title"`
	StartMs      int    `json:"start_ms"`
	EndMs        int    `json:"end_ms"`
	// Title is empty for other viewers while it is shadowed.
	Title  string `json:"title"`
	Status string `json:"status"`
	// TitleStatus is only told to the author (visible | shadow | removed).
	TitleStatus string    `json:"title_status,omitempty"`
	AuthorName  string    `json:"author_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// Mine: the viewer made it (may delete it).
	Mine bool `json:"mine"`
	// ReleaseCover is the release's stored cover, set by the API (the clip
	// page's og:image without a second request).
	ReleaseCover *model.Image `json:"release_cover"`
	// internal ids (reports.target_id; the cover lookup); never in JSON.
	RowID     int64 `json:"-"`
	ReleaseID int64 `json:"-"`
}

// ClipInput is POST /api/me/clips.
type ClipInput struct {
	Release string `json:"release"` // slug
	StartMs int    `json:"start_ms"`
	EndMs   int    `json:"end_ms"`
	Title   string `json:"title"`
}

// ValidateClipRange checks a clip segment: start >= 0, 5–60 s long, and,
// when the release duration is known (durationMs > 0), inside it.
func ValidateClipRange(startMs, endMs, durationMs int) error {
	if startMs < 0 || endMs <= startMs {
		return invalid("clip range must start at 0 or later and end after it starts")
	}
	if n := endMs - startMs; n < ClipMinMs || n > ClipMaxMs {
		return invalid("clip must be 5 to 60 seconds long")
	}
	if durationMs > 0 && endMs > durationMs {
		return invalid("clip ends after the video")
	}
	return nil
}

// CleanClipTitle trims the title and checks its length (runes).
func CleanClipTitle(title string) (string, error) {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > ClipTitleMax {
		return "", invalid("title must be at most %d characters", ClipTitleMax)
	}
	return title, nil
}

// ValidClipID reports whether id looks like a clip public id.
func ValidClipID(id string) bool {
	if len(id) != clipIDLen {
		return false
	}
	for _, c := range id {
		if !strings.ContainsRune(clipIDAlphabet, c) {
			return false
		}
	}
	return true
}

func newClipID() (string, error) {
	b := make([]byte, clipIDLen)
	max := big.NewInt(int64(len(clipIDAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = clipIDAlphabet[n.Int64()]
	}
	return string(b), nil
}

// clipReleaseDurationSQL: the main version's longest known source duration,
// else the catalog runtime rounded up to the next minute; 0 when unknown.
const clipReleaseDurationSQL = `COALESCE(
	(SELECT max(duration_seconds) FROM playback_sources ps WHERE ps.release_id=r.id AND NOT ps.is_alt_version AND ps.duration_seconds > 0) * 1000,
	r.runtime_minutes * 60000 + 59999, 0)`

// CreateClip stores a visible clip of a published release for userID.
// titleStatus is what the caller decided for the title (visible, or shadow
// on a keyword hit); an empty title is always visible. An unknown or
// unpublished release is pgx.ErrNoRows.
func (s *Store) CreateClip(ctx context.Context, userID int64, in ClipInput, titleStatus string) (Clip, error) {
	title, err := CleanClipTitle(in.Title)
	if err != nil {
		return Clip{}, err
	}
	switch {
	case title == "":
		titleStatus = ClipTitleVisible
	case titleStatus != ClipTitleVisible && titleStatus != ClipTitleShadow:
		return Clip{}, invalid("title status must be visible or shadow")
	}
	const status = "visible"
	var releaseID int64
	var slug, releaseTitle string
	var durationMs int
	err = s.QueryRow(ctx, `SELECT r.id,r.slug,r.canonical_title,`+clipReleaseDurationSQL+` FROM releases r
		WHERE r.slug=$1 AND r.status='published' AND r.merged_into_id IS NULL`, in.Release).Scan(&releaseID, &slug, &releaseTitle, &durationMs)
	if err != nil {
		return Clip{}, err
	}
	if err = ValidateClipRange(in.StartMs, in.EndMs, durationMs); err != nil {
		return Clip{}, err
	}
	for try := 0; ; try++ {
		id, err := newClipID()
		if err != nil {
			return Clip{}, err
		}
		c := Clip{ID: id, ReleaseID: releaseID, ReleaseSlug: slug, ReleaseTitle: releaseTitle, StartMs: in.StartMs, EndMs: in.EndMs, Title: title, Status: status, TitleStatus: titleStatus, Mine: true}
		err = s.QueryRow(ctx, `INSERT INTO clips(public_id,release_id,user_id,start_ms,end_ms,title,status,title_status) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			RETURNING id,created_at`, id, releaseID, userID, in.StartMs, in.EndMs, title, status, titleStatus).Scan(&c.RowID, &c.CreatedAt)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && try < clipInsertTries {
			continue // public id collision: draw again
		}
		return c, err
	}
}

// ClipByPublicID returns a clip of a published release. Deleted clips are
// pgx.ErrNoRows; shadowed clips, and shadowed titles, only reach their
// author (viewerID). TitleStatus is set for the author only.
func (s *Store) ClipByPublicID(ctx context.Context, publicID string, viewerID int64) (Clip, error) {
	if !ValidClipID(publicID) {
		return Clip{}, pgx.ErrNoRows
	}
	var c Clip
	var owner int64
	var titleStatus string
	err := s.QueryRow(ctx, `SELECT c.id,c.public_id,r.id,r.slug,r.canonical_title,c.start_ms,c.end_ms,`+clipTitleSQL+`,c.title_status,c.status,COALESCE(u.display_name,''),c.created_at,c.user_id
		FROM clips c JOIN releases r ON r.id=c.release_id JOIN users u ON u.id=c.user_id
		WHERE c.public_id=$1 AND r.status='published' AND r.merged_into_id IS NULL
		  AND (c.status IN ('visible','folded') OR (c.status='shadow' AND c.user_id=$2))`, publicID, viewerID).
		Scan(&c.RowID, &c.ID, &c.ReleaseID, &c.ReleaseSlug, &c.ReleaseTitle, &c.StartMs, &c.EndMs, &c.Title, &titleStatus, &c.Status, &c.AuthorName, &c.CreatedAt, &owner)
	c.Mine = viewerID != 0 && owner == viewerID
	if c.Mine {
		c.TitleStatus = titleStatus
	}
	return c, err
}

// DeleteClip removes the user's own clip; another user's or an unknown
// clip is pgx.ErrNoRows.
func (s *Store) DeleteClip(ctx context.Context, userID int64, publicID string) error {
	if !ValidClipID(publicID) {
		return pgx.ErrNoRows
	}
	tag, err := s.Exec(ctx, `DELETE FROM clips WHERE public_id=$1 AND user_id=$2`, publicID, userID)
	if err == nil && tag.RowsAffected() == 0 {
		err = pgx.ErrNoRows
	}
	return err
}

// ClipSort is the cursor sort of GET /api/me/clips.
const ClipSort = "clips"

// MyClip is one row of the author's clip list (GET /api/me/clips).
type MyClip struct {
	Clip
}

// MyClips lists userID's clips, newest first, keyset-paginated on
// (created_at, id). Only clips that open are listed: published, unmerged
// releases, not deleted by a moderator.
func (s *Store) MyClips(ctx context.Context, userID int64, c *Cursor, limit int) ([]MyClip, string, error) {
	limit = pageLimit(limit)
	var after *time.Time
	var afterID int64
	if c != nil {
		if c.Sort != ClipSort {
			return nil, "", invalidQuery("cursor does not match clips")
		}
		t, err := time.Parse(time.RFC3339Nano, c.Key)
		if err != nil || c.ID <= 0 {
			return nil, "", invalidQuery("malformed cursor")
		}
		after, afterID = &t, c.ID
	}
	rows, err := s.Query(ctx, `SELECT c.id,c.public_id,r.id,r.slug,r.canonical_title,c.start_ms,c.end_ms,c.title,c.title_status,c.status,c.created_at
		FROM clips c JOIN releases r ON r.id=c.release_id
		WHERE c.user_id=$1 AND c.status<>'deleted' AND r.status='published' AND r.merged_into_id IS NULL
		  AND ($2::timestamptz IS NULL OR (c.created_at,c.id) < ($2::timestamptz,$3::bigint))
		ORDER BY c.created_at DESC, c.id DESC LIMIT $4`, userID, after, afterID, limit+1)
	if err != nil {
		return nil, "", err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MyClip, error) {
		x := MyClip{Clip: Clip{Mine: true}}
		return x, r.Scan(&x.RowID, &x.ID, &x.ReleaseID, &x.ReleaseSlug, &x.ReleaseTitle, &x.StartMs, &x.EndMs, &x.Title, &x.TitleStatus, &x.Status, &x.CreatedAt)
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		next = EncodeCursor(Cursor{Sort: ClipSort, Key: last.CreatedAt.UTC().Format(time.RFC3339Nano), ID: last.RowID})
	}
	return nonNil(out), next, nil
}
