package store

// Sitemap source lists (PRD 13 SEO; web/app/lib/sitemap.server.ts). Each list
// is keyset paginated by a stable key so the web server can walk the whole
// catalog in large pages:
//
//   - releases:   published, unmerged and playable (an active or degraded
//     playback_sources row, or a provider mapping in release_media_sources);
//   - performers: active, unmerged, credited on at least one published release;
//   - tags:       tags of published, unmerged releases (the /discover?tag= pages);
//   - playlists:  public 收藏夹 holding at least one published release, by
//     share_token (/playlists/{token}). Link-only and private playlists are
//     never listed: a link-only URL is a capability the owner hands out.
//
// Held releases (held_review), drafts and hidden performers never appear.

import (
	"avxxx/internal/audit"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// SitemapEntry is one URL source: the entity slug and when it last changed.
type SitemapEntry struct {
	Slug      string    `json:"slug"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SitemapCounts are the sizes of the three lists, for coverage checks
// (sitemap URLs found vs. entities that should be listed).
type SitemapCounts struct {
	Releases          int64 `json:"releases"`
	PublishedReleases int64 `json:"published_releases"`
	Performers        int64 `json:"performers"`
	Tags              int64 `json:"tags"`
	Playlists         int64 `json:"playlists"`
}

const (
	defaultSitemapLimit = 5000
	maxSitemapLimit     = 10000
)

func sitemapLimit(n int) int {
	switch {
	case n <= 0:
		return defaultSitemapLimit
	case n > maxSitemapLimit:
		return maxSitemapLimit
	}
	return n
}

func encodeSitemapCursor(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

func decodeSitemapCursor(c string) (string, error) {
	if c == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return "", invalidQuery("bad cursor")
	}
	return string(b), nil
}

func decodeIDCursor(c string) (int64, error) {
	key, err := decodeSitemapCursor(c)
	if err != nil || key == "" {
		return 0, err
	}
	id, err := strconv.ParseInt(key, 10, 64)
	if err != nil || id < 0 {
		return 0, invalidQuery("bad cursor")
	}
	return id, nil
}

const playableReleaseSQL = `r.status='published' AND r.merged_into_id IS NULL AND (
	EXISTS (SELECT 1 FROM playback_sources ps WHERE ps.release_id=r.id AND ps.status IN ('active','degraded'))
	OR EXISTS (SELECT 1 FROM release_media_sources ms WHERE ms.release_id=r.id))`

const sitemapPerformerSQL = `p.status='active' AND p.merged_into_id IS NULL AND EXISTS (
	SELECT 1 FROM release_performers rp JOIN releases r ON r.id=rp.release_id
	WHERE rp.performer_id=p.id AND r.status='published' AND r.merged_into_id IS NULL)`

func (s *Store) sitemapByID(ctx context.Context, sql, cursor string, limit int) ([]SitemapEntry, string, error) {
	after, err := decodeIDCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = sitemapLimit(limit)
	rows, err := s.Query(ctx, sql, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []SitemapEntry{}
	var lastID int64
	for rows.Next() {
		var e SitemapEntry
		var id int64
		if err = rows.Scan(&id, &e.Slug, &e.UpdatedAt); err != nil {
			return nil, "", err
		}
		if len(out) == limit {
			return out, encodeSitemapCursor(strconv.FormatInt(lastID, 10)), rows.Err()
		}
		out = append(out, e)
		lastID = id
	}
	return out, "", rows.Err()
}

// SitemapReleases lists playable releases in id order. It returns the next
// cursor, or "" on the last page.
func (s *Store) SitemapReleases(ctx context.Context, cursor string, limit int) ([]SitemapEntry, string, error) {
	return s.sitemapByID(ctx, `SELECT r.id,r.slug,r.updated_at FROM releases r
		WHERE r.id>$1 AND `+playableReleaseSQL+` ORDER BY r.id LIMIT $2`, cursor, limit)
}

// SitemapPerformers lists performers with a published release in id order.
func (s *Store) SitemapPerformers(ctx context.Context, cursor string, limit int) ([]SitemapEntry, string, error) {
	return s.sitemapByID(ctx, `SELECT p.id,p.slug,p.updated_at FROM performers p
		WHERE p.id>$1 AND `+sitemapPerformerSQL+` ORDER BY p.id LIMIT $2`, cursor, limit)
}

const sitemapPlaylistSQL = `p.visibility='public' AND EXISTS (
	SELECT 1 FROM playlist_items i JOIN releases r ON r.id=i.release_id
	WHERE i.playlist_id=p.id AND r.status='published' AND r.merged_into_id IS NULL)`

// SitemapPlaylists lists public playlists with a published release in id
// order; Slug is the share_token.
func (s *Store) SitemapPlaylists(ctx context.Context, cursor string, limit int) ([]SitemapEntry, string, error) {
	return s.sitemapByID(ctx, `SELECT p.id,p.share_token,p.updated_at FROM playlists p
		WHERE p.id>$1 AND `+sitemapPlaylistSQL+` ORDER BY p.id LIMIT $2`, cursor, limit)
}

// SitemapTags lists the tags of published releases in tag order; UpdatedAt
// is the newest change among their releases.
func (s *Store) SitemapTags(ctx context.Context, cursor string, limit int) ([]SitemapEntry, string, error) {
	after, err := decodeSitemapCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	limit = sitemapLimit(limit)
	rows, err := s.Query(ctx, `SELECT t.normalized_tag,max(r.updated_at) FROM release_tags t
		JOIN releases r ON r.id=t.release_id
		WHERE r.status='published' AND r.merged_into_id IS NULL AND t.normalized_tag>$1 AND t.normalized_tag<>''
		GROUP BY t.normalized_tag ORDER BY t.normalized_tag LIMIT $2`, after, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	out := []SitemapEntry{}
	for rows.Next() {
		var e SitemapEntry
		if err = rows.Scan(&e.Slug, &e.UpdatedAt); err != nil {
			return nil, "", err
		}
		if len(out) == limit {
			return out, encodeSitemapCursor(out[len(out)-1].Slug), rows.Err()
		}
		out = append(out, e)
	}
	return out, "", rows.Err()
}

// sitemapCountsTTL matches the public Cache-Control of the sitemap lists
// (internal/api/sitemap.go) and the web's sitemap cache.
const sitemapCountsTTL = 5 * time.Minute

var sitemapCountsCaches sync.Map // *Store -> *staleCache[SitemapCounts]

// SitemapCounts counts what the sitemap lists should contain. The counts
// read every published release, credit and tag (~0.9 s at 300k releases)
// and the route is public, so they are cached for sitemapCountsTTL, one
// reload at a time (staleCache).
func (s *Store) SitemapCounts(ctx context.Context) (SitemapCounts, error) {
	c, _ := sitemapCountsCaches.LoadOrStore(s, &staleCache[SitemapCounts]{})
	return c.(*staleCache[SitemapCounts]).get(sitemapCountsTTL, time.Now, func() (SitemapCounts, error) {
		return s.sitemapCountsNow(ctx)
	})
}

// sitemapCountsNow computes SitemapCounts. Distinct tags are grouped
// rather than count(DISTINCT ...), which sorted all 1.75M tag rows.
func (s *Store) sitemapCountsNow(ctx context.Context) (SitemapCounts, error) {
	var c SitemapCounts
	err := s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM releases r WHERE `+playableReleaseSQL+`),
		(SELECT count(*) FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL),
		(SELECT count(*) FROM performers p WHERE `+sitemapPerformerSQL+`),
		(SELECT count(*) FROM (SELECT t.normalized_tag FROM release_tags t JOIN releases r ON r.id=t.release_id
			WHERE r.status='published' AND r.merged_into_id IS NULL AND t.normalized_tag<>'' GROUP BY 1) tags),
		(SELECT count(*) FROM playlists p WHERE `+sitemapPlaylistSQL+`)`).
		Scan(&c.Releases, &c.PublishedReleases, &c.Performers, &c.Tags, &c.Playlists)
	return c, err
}

// LatestRun returns the newest nightly_runs row of job, or ok=false.
func (s *Store) LatestRun(ctx context.Context, job string) (audit.Run, bool, error) {
	var x audit.Run
	var detail []byte
	err := s.QueryRow(ctx, `SELECT job,COALESCE(job_id,0),started_at,duration_ms,checked,fixed,issues,result,detail
		FROM nightly_runs WHERE job=$1 ORDER BY started_at DESC,id DESC LIMIT 1`, job).
		Scan(&x.Job, &x.JobID, &x.StartedAt, &x.DurationMS, &x.Checked, &x.Fixed, &x.Issues, &x.Result, &detail)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, false, nil
	}
	if err != nil {
		return x, false, err
	}
	if err = json.Unmarshal(detail, &x.Detail); err != nil {
		return x, false, err
	}
	return x, true, nil
}
