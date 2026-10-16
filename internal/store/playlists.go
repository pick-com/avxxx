package store

// 收藏夹 (playlists, PRD 7 NavFavorites; tech plan 3.13; migration 036) and
// the watch-later auto-remove rule (NavLater 看完自动移出).
//
// Every owner method scopes its SQL by user_id, so another user's playlist
// id behaves exactly like a missing one (pgx.ErrNoRows -> 404): a non-owner
// can neither read nor change it, and cannot learn that it exists. The only
// non-owner read is PublicPlaylist, by share_token, for public and link
// playlists.

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Playlist visibilities.
const (
	VisibilityPrivate = "private"
	VisibilityPublic  = "public"
	VisibilityLink    = "link"
)

// DefaultPlaylistName is the stored name of the playlist created for the
// quick 收藏 action (the web shows a localized label while it is unchanged).
const DefaultPlaylistName = "默认收藏夹"

// Limits on user-entered playlist data (mirrored by CHECKs in migration 036).
const (
	MaxPlaylists           = 100
	MaxPlaylistItems       = 2000
	maxPlaylistName        = 60
	maxPlaylistDescription = 300
)

// ValidVisibility reports whether v is a playlists.visibility value.
func ValidVisibility(v string) bool {
	return v == VisibilityPrivate || v == VisibilityPublic || v == VisibilityLink
}

// Playlist is one 收藏夹. ShareToken is only filled for the owner.
type Playlist struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Visibility  string    `json:"visibility"`
	ShareToken  string    `json:"share_token,omitempty"`
	IsDefault   bool      `json:"is_default"`
	ItemCount   int       `json:"item_count"`
	CoverSlugs  []string  `json:"cover_slugs"`
	Contains    *bool     `json:"contains,omitempty"`
	OwnerName   string    `json:"owner_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// PlaylistItem is one release in a playlist (published, unmerged releases only).
type PlaylistItem struct {
	Slug           string    `json:"slug"`
	Title          string    `json:"title"`
	TitleJA        string    `json:"title_ja"`
	RuntimeMinutes int       `json:"runtime_minutes"`
	AddedAt        time.Time `json:"added_at"`
}

// PlaylistInput is a create or update request; nil fields are left unchanged
// (on create: name is required, visibility defaults to private).
type PlaylistInput struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Visibility  *string `json:"visibility"`
}

// Normalize trims and validates the input (ValidationError -> 400).
func (in *PlaylistInput) Normalize(create bool) error {
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || utf8.RuneCountInString(n) > maxPlaylistName {
			return invalid("name must be 1-%d characters", maxPlaylistName)
		}
		in.Name = &n
	} else if create {
		return invalid("name is required")
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if utf8.RuneCountInString(d) > maxPlaylistDescription {
			return invalid("description must be at most %d characters", maxPlaylistDescription)
		}
		in.Description = &d
	}
	if in.Visibility != nil && !ValidVisibility(*in.Visibility) {
		return invalid("visibility must be private, public or link")
	}
	return nil
}

// ErrDefaultPlaylist refuses deleting the default playlist.
var ErrDefaultPlaylist = &ConflictError{Msg: "the default playlist cannot be deleted"}

// playlistColumns selects a Playlist for scanPlaylist ($1 = owner, or 0;
// the release id for contains is passed as $2, 0 for none).
const playlistColumns = `p.id,p.name,p.description,p.visibility,p.share_token,p.is_default,p.created_at,p.updated_at,
	(SELECT count(*) FROM playlist_items i JOIN releases r ON r.id=i.release_id
	 WHERE i.playlist_id=p.id AND r.status='published' AND r.merged_into_id IS NULL),
	ARRAY(SELECT r.slug FROM playlist_items i JOIN releases r ON r.id=i.release_id
	      WHERE i.playlist_id=p.id AND r.status='published' AND r.merged_into_id IS NULL
	      ORDER BY i.created_at DESC, r.id DESC LIMIT 4)`

func scanPlaylist(row rowScanner, extra ...any) (Playlist, error) {
	var p Playlist
	var n int64
	dest := append([]any{&p.ID, &p.Name, &p.Description, &p.Visibility, &p.ShareToken, &p.IsDefault,
		&p.CreatedAt, &p.UpdatedAt, &n, &p.CoverSlugs}, extra...)
	err := row.Scan(dest...)
	p.ItemCount = int(n)
	if p.CoverSlugs == nil {
		p.CoverSlugs = []string{}
	}
	return p, err
}

// ensureDefaultPlaylist creates the user's default playlist when missing and
// returns its id.
func ensureDefaultPlaylist(ctx context.Context, q pgxQuerier, userID int64) (int64, error) {
	var id int64
	err := q.QueryRow(ctx, `SELECT id FROM playlists WHERE user_id=$1 AND is_default`, userID).Scan(&id)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	err = q.QueryRow(ctx, `INSERT INTO playlists(user_id,name,is_default) VALUES($1,$2,true)
		ON CONFLICT (user_id) WHERE is_default DO UPDATE SET is_default=true RETURNING id`, userID, DefaultPlaylistName).Scan(&id)
	return id, err
}

// pgxQuerier is satisfied by *Store (pgxpool) and pgx.Tx.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Playlists lists the user's playlists, default first. With releaseSlug set,
// each playlist carries contains (for the release page's 收藏 picker); an
// unknown slug is pgx.ErrNoRows.
func (s *Store) Playlists(ctx context.Context, userID int64, releaseSlug string) ([]Playlist, error) {
	if _, err := ensureDefaultPlaylist(ctx, s, userID); err != nil {
		return nil, err
	}
	var releaseID int64
	if releaseSlug != "" {
		if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, releaseSlug).Scan(&releaseID); err != nil {
			return nil, err
		}
	}
	rows, err := s.Query(ctx, `SELECT `+playlistColumns+`,
		EXISTS(SELECT 1 FROM playlist_items i WHERE i.playlist_id=p.id AND i.release_id=$2)
		FROM playlists p WHERE p.user_id=$1
		ORDER BY p.is_default DESC, p.position, p.id LIMIT `+itoa(MaxPlaylists+1), userID, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Playlist{}
	for rows.Next() {
		var contains bool
		p, err := scanPlaylist(rows, &contains)
		if err != nil {
			return nil, err
		}
		if releaseSlug != "" {
			p.Contains = &contains
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ownedPlaylist loads one of the user's playlists (pgx.ErrNoRows when it does
// not exist or belongs to someone else).
func (s *Store) ownedPlaylist(ctx context.Context, q pgxQuerier, userID, id int64) (Playlist, error) {
	return scanPlaylist(q.QueryRow(ctx, `SELECT `+playlistColumns+` FROM playlists p WHERE p.id=$1 AND p.user_id=$2`, id, userID))
}

// CreatePlaylist creates a playlist at the end of the user's list.
func (s *Store) CreatePlaylist(ctx context.Context, userID int64, in PlaylistInput) (Playlist, error) {
	if err := in.Normalize(true); err != nil {
		return Playlist{}, err
	}
	vis, desc := VisibilityPrivate, ""
	if in.Visibility != nil {
		vis = *in.Visibility
	}
	if in.Description != nil {
		desc = *in.Description
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return Playlist{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := ensureDefaultPlaylist(ctx, tx, userID); err != nil {
		return Playlist{}, err
	}
	// Serialize creates per user so the limit holds under concurrency.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM playlists WHERE user_id=$1 AND is_default FOR UPDATE`, userID); err != nil {
		return Playlist{}, err
	}
	var n, pos int
	if err := tx.QueryRow(ctx, `SELECT count(*), COALESCE(max(position),0)+1 FROM playlists WHERE user_id=$1`, userID).Scan(&n, &pos); err != nil {
		return Playlist{}, err
	}
	if n >= MaxPlaylists {
		return Playlist{}, conflict("at most %d playlists", MaxPlaylists)
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO playlists(user_id,name,description,visibility,position) VALUES($1,$2,$3,$4,$5) RETURNING id`,
		userID, *in.Name, desc, vis, pos).Scan(&id); err != nil {
		return Playlist{}, err
	}
	p, err := s.ownedPlaylist(ctx, tx, userID, id)
	if err != nil {
		return Playlist{}, err
	}
	return p, tx.Commit(ctx)
}

// UpdatePlaylist renames a playlist or changes its description or visibility.
func (s *Store) UpdatePlaylist(ctx context.Context, userID, id int64, in PlaylistInput) (Playlist, error) {
	if err := in.Normalize(false); err != nil {
		return Playlist{}, err
	}
	tag, err := s.Exec(ctx, `UPDATE playlists SET name=COALESCE($3,name), description=COALESCE($4,description),
		visibility=COALESCE($5,visibility), updated_at=now() WHERE id=$1 AND user_id=$2`,
		id, userID, in.Name, in.Description, in.Visibility)
	if err != nil {
		return Playlist{}, err
	}
	if tag.RowsAffected() == 0 {
		return Playlist{}, pgx.ErrNoRows
	}
	return s.ownedPlaylist(ctx, s, userID, id)
}

// RotatePlaylistToken issues a new share_token, which revokes the old link.
func (s *Store) RotatePlaylistToken(ctx context.Context, userID, id int64) (Playlist, error) {
	tag, err := s.Exec(ctx, `UPDATE playlists SET share_token=replace(gen_random_uuid()::text,'-',''), updated_at=now()
		WHERE id=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return Playlist{}, err
	}
	if tag.RowsAffected() == 0 {
		return Playlist{}, pgx.ErrNoRows
	}
	return s.ownedPlaylist(ctx, s, userID, id)
}

// DeletePlaylist deletes a playlist and its items (never the default one).
func (s *Store) DeletePlaylist(ctx context.Context, userID, id int64) error {
	var isDefault bool
	if err := s.QueryRow(ctx, `SELECT is_default FROM playlists WHERE id=$1 AND user_id=$2`, id, userID).Scan(&isDefault); err != nil {
		return err
	}
	if isDefault {
		return ErrDefaultPlaylist
	}
	_, err := s.Exec(ctx, `DELETE FROM playlists WHERE id=$1 AND user_id=$2 AND NOT is_default`, id, userID)
	return err
}

// playlistItems lists a playlist's published releases, newest addition first.
func (s *Store) playlistItems(ctx context.Context, playlistID int64) ([]PlaylistItem, error) {
	rows, err := s.Query(ctx, `SELECT r.slug,r.canonical_title,COALESCE(r.title_ja,''),COALESCE(r.runtime_minutes,0),i.created_at
		FROM playlist_items i JOIN releases r ON r.id=i.release_id
		WHERE i.playlist_id=$1 AND r.status='published' AND r.merged_into_id IS NULL
		ORDER BY i.created_at DESC, r.id DESC LIMIT `+itoa(MaxPlaylistItems), playlistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PlaylistItem{}
	for rows.Next() {
		var it PlaylistItem
		if err := rows.Scan(&it.Slug, &it.Title, &it.TitleJA, &it.RuntimeMinutes, &it.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// PlaylistWithItems returns one of the user's playlists with its items.
func (s *Store) PlaylistWithItems(ctx context.Context, userID, id int64) (Playlist, []PlaylistItem, error) {
	p, err := s.ownedPlaylist(ctx, s, userID, id)
	if err != nil {
		return Playlist{}, nil, err
	}
	items, err := s.playlistItems(ctx, id)
	return p, items, err
}

// PublicPlaylist returns a public or link-only playlist by share_token, for
// anyone. Private playlists and unknown tokens are pgx.ErrNoRows. The owner's
// token is not echoed back, only their display name.
func (s *Store) PublicPlaylist(ctx context.Context, token string) (Playlist, []PlaylistItem, error) {
	if token == "" || len(token) > 64 {
		return Playlist{}, nil, pgx.ErrNoRows
	}
	var owner string
	p, err := scanPlaylist(s.QueryRow(ctx, `SELECT `+playlistColumns+`, u.display_name
		FROM playlists p JOIN users u ON u.id=p.user_id
		WHERE p.share_token=$1 AND p.visibility IN ('public','link')`, token), &owner)
	if err != nil {
		return Playlist{}, nil, err
	}
	p.ShareToken = ""
	p.OwnerName = owner
	items, err := s.playlistItems(ctx, p.ID)
	return p, items, err
}

// AddPlaylistItem adds a published release to one of the user's playlists
// (no-op when already there). id 0 means the default playlist.
func (s *Store) AddPlaylistItem(ctx context.Context, userID, id int64, releaseSlug string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := addPlaylistItem(ctx, tx, userID, id, releaseSlug); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func addPlaylistItem(ctx context.Context, tx pgx.Tx, userID, id int64, releaseSlug string) error {
	var releaseID int64
	if err := tx.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status='published' AND merged_into_id IS NULL`, releaseSlug).Scan(&releaseID); err != nil {
		return err
	}
	if id == 0 {
		var err error
		if id, err = ensureDefaultPlaylist(ctx, tx, userID); err != nil {
			return err
		}
	}
	// Lock the owner's playlist row: proves ownership and serializes the size check.
	var n int
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM playlist_items WHERE playlist_id=p.id)
		FROM playlists p WHERE p.id=$1 AND p.user_id=$2 FOR UPDATE`, id, userID).Scan(&n); err != nil {
		return err
	}
	if n >= MaxPlaylistItems {
		return conflict("a playlist holds at most %d videos", MaxPlaylistItems)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO playlist_items(playlist_id,release_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, releaseID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE playlists SET updated_at=now() WHERE id=$1`, id)
	return err
}

// RemovePlaylistItem removes a release from one of the user's playlists
// (no-op when absent). id 0 means the default playlist.
func (s *Store) RemovePlaylistItem(ctx context.Context, userID, id int64, releaseSlug string) error {
	var releaseID int64
	if err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1`, releaseSlug).Scan(&releaseID); err != nil {
		return err
	}
	if id == 0 {
		var err error
		if id, err = ensureDefaultPlaylist(ctx, s, userID); err != nil {
			return err
		}
	}
	var owned bool
	if err := s.QueryRow(ctx, `SELECT true FROM playlists WHERE id=$1 AND user_id=$2`, id, userID).Scan(&owned); err != nil {
		return err
	}
	_, err := s.Exec(ctx, `DELETE FROM playlist_items WHERE playlist_id=$1 AND release_id=$2`, id, releaseID)
	return err
}

// MoveWatchLaterToPlaylist adds a release to one of the user's playlists and
// removes it from watch-later, atomically (移到收藏夹).
func (s *Store) MoveWatchLaterToPlaylist(ctx context.Context, userID, id int64, releaseSlug string) error {
	tx, err := s.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := addPlaylistItem(ctx, tx, userID, id, releaseSlug); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM watch_later wl USING releases r
		WHERE wl.user_id=$1 AND wl.release_id=r.id AND r.slug=$2`, userID, releaseSlug); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LaterFinishRatio is the share of a video that counts as watched for the
// watch-later auto-remove rule (tech plan 3.13: 看完 90% 自动移出).
const LaterFinishRatio = 0.9

// LaterFinished reports whether progress counts as finished for duration
// seconds (pure helper; durations ≤ 0 are unknown, never finished).
func LaterFinished(progress, duration int) bool {
	return duration > 0 && float64(progress) >= LaterFinishRatio*float64(duration)
}

// LaterAutoRemove removes a release from the user's watch-later once progress
// reaches 90 % of the duration (the reported one, else the catalog runtime),
// unless the user turned user_settings.later_auto_remove off. It reports
// whether an entry was removed. It runs whether or not history is paused.
func (s *Store) LaterAutoRemove(ctx context.Context, userID int64, releaseSlug string, progress, duration int) (bool, error) {
	if progress <= 0 {
		return false, nil
	}
	tag, err := s.Exec(ctx, `DELETE FROM watch_later wl USING releases r
		WHERE wl.user_id=$1 AND wl.release_id=r.id AND r.slug=$2
		  AND COALESCE((SELECT later_auto_remove FROM user_settings WHERE user_id=$1), true)
		  AND $3::float8 >= $5::float8 * COALESCE(NULLIF($4::int,0), NULLIF(r.runtime_minutes,0)*60)`,
		userID, releaseSlug, progress, duration, LaterFinishRatio)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
