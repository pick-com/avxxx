package store

// Community features (PRD 5.5, 5.5.1, 19; tech plan 3.8; migration 017):
// danmaku and the peak curve, ratings, short reviews and reports. The rules
// (curve math, weights, limits, keyword check) live in internal/community;
// this file is the SQL around them. Moderation is in moderation.go.

import (
	"avxxx/internal/community"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrUnknownSource: a danmaku request names a source that is not a
	// playback source of the release.
	ErrUnknownSource = errors.New("unknown source")
	// ErrNotOwner: the review exists but belongs to someone else.
	ErrNotOwner = errors.New("not your review")
	// ErrTargetNotFound: the report target does not exist.
	ErrTargetNotFound = errors.New("report target not found")
)

// communityCache holds the per-Store caches: the keyword matcher and the
// site-wide rating mean (the Bayesian prior). Kept outside Store so the
// struct (store.go) is unchanged.
type communityCache struct {
	mu        sync.Mutex
	matcher   *community.Matcher
	matcherAt time.Time
	prior     float64
	priorAt   time.Time
}

const (
	matcherTTL = 30 * time.Second
	priorTTL   = 10 * time.Minute
)

// dbtx is satisfied by *Store (the pool) and pgx.Tx.
type dbtx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var communityCaches sync.Map // *Store -> *communityCache

func (s *Store) cache() *communityCache {
	if c, ok := communityCaches.Load(s); ok {
		return c.(*communityCache)
	}
	c, _ := communityCaches.LoadOrStore(s, &communityCache{})
	return c.(*communityCache)
}

// ReleaseIDBySlug resolves a published, unmerged release (pgx.ErrNoRows otherwise).
func (s *Store) ReleaseIDBySlug(ctx context.Context, slug string) (int64, error) {
	var id int64
	err := s.QueryRow(ctx, `SELECT id FROM releases WHERE slug=$1 AND status='published' AND merged_into_id IS NULL`, slug).Scan(&id)
	return id, err
}

// ---------------------------------------------------------------------------
// Posters

// Poster is what the posting checks need to know about a user.
type Poster struct {
	UserID     int64
	AccountAge time.Duration
	MutedUntil *time.Time // set while a mute is active
	EverMuted  bool       // a user_mutes row exists (active or expired)
	StruckVote bool       // a moderator removed one of the user's votes
}

// Muted reports whether the poster is muted at now.
func (p Poster) Muted(now time.Time) bool { return p.MutedUntil != nil && p.MutedUntil.After(now) }

// Abnormal is the PRD 19 "异常账号" signal used for rating weights.
func (p Poster) Abnormal() bool { return p.EverMuted || p.StruckVote }

// PosterInfo loads account age, mute and abnormal flags for a user.
func (s *Store) PosterInfo(ctx context.Context, userID int64) (Poster, error) {
	p := Poster{UserID: userID}
	var created time.Time
	var until *time.Time
	err := s.QueryRow(ctx, `SELECT u.created_at, m.until,
		EXISTS(SELECT 1 FROM ratings r WHERE r.user_id=u.id AND r.status='removed')
		FROM users u LEFT JOIN user_mutes m ON m.user_id=u.id WHERE u.id=$1`, userID).Scan(&created, &until, &p.StruckVote)
	if err != nil {
		return p, err
	}
	p.AccountAge = time.Since(created)
	p.EverMuted = until != nil
	if until != nil && until.After(time.Now()) {
		p.MutedUntil = until
	}
	return p, nil
}

// ModerationMatcher returns the keyword matcher, cached for matcherTTL.
func (s *Store) ModerationMatcher(ctx context.Context) (*community.Matcher, error) {
	c := s.cache()
	c.mu.Lock()
	if c.matcher != nil && time.Since(c.matcherAt) < matcherTTL {
		m := c.matcher
		c.mu.Unlock()
		return m, nil
	}
	c.mu.Unlock()
	words, err := s.ModerationWords(ctx)
	if err != nil {
		return nil, err
	}
	m := community.NewMatcher(words)
	c.mu.Lock()
	c.matcher, c.matcherAt = m, time.Now()
	c.mu.Unlock()
	return m, nil
}

func (s *Store) invalidateMatcher() {
	c := s.cache()
	c.mu.Lock()
	c.matcher = nil
	c.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Danmaku

// Danmaku is one danmaku as returned to clients.
type Danmaku struct {
	ID        int64     `json:"id"`
	TMs       int       `json:"t_ms"`
	Body      string    `json:"body"`
	Color     string    `json:"color"`
	Mode      string    `json:"mode"`
	Lang      string    `json:"lang"`
	Spoiler   bool      `json:"spoiler"` // folded as a spoiler by a moderator
	Mine      bool      `json:"mine"`
	CreatedAt time.Time `json:"created_at"`
}

// NewDanmaku is a validated danmaku to insert.
type NewDanmaku struct {
	ReleaseID  int64
	VersionKey string
	TMs        int
	Body       string
	Color      string
	Mode       string
	Lang       string
	UserID     int64
	Status     string // visible or shadow
}

// DanmakuTimeline is the release and timeline a danmaku request refers to.
type DanmakuTimeline struct {
	ReleaseID  int64
	VersionKey string
	DurationMS int // 0 when unknown
}

// DanmakuTimelineFor resolves slug and an optional playback source id to a
// timeline: sourceID 0 (or a main / high-quality / dub source) is the shared
// "main" timeline; an alternate version has its own (PRD 5.5.1).
func (s *Store) DanmakuTimelineFor(ctx context.Context, slug string, sourceID int64) (DanmakuTimeline, error) {
	rel, srcs, err := s.PlaybackForRelease(ctx, slug)
	if err != nil {
		return DanmakuTimeline{}, err
	}
	return TimelineFor(rel, srcs, sourceID)
}

// TimelineFor is the database-free part of DanmakuTimelineFor.
func TimelineFor(rel PlaybackRelease, srcs []PlaybackSource, sourceID int64) (DanmakuTimeline, error) {
	tl := DanmakuTimeline{ReleaseID: rel.ID, VersionKey: community.DefaultVersion}
	ref := ReferenceDuration(srcs, rel.RuntimeMinutes)
	if ref != nil {
		tl.DurationMS = *ref * 1000
	}
	if sourceID == 0 {
		return tl, nil
	}
	for _, x := range srcs {
		if x.ID != sourceID {
			continue
		}
		ApplyAltVersionRule(&x, ref)
		if x.IsAltVersion {
			tl.VersionKey = community.AltVersionKey(x.ID)
			tl.DurationMS = 0
			if x.DurationSeconds != nil {
				tl.DurationMS = *x.DurationSeconds * 1000
			}
		}
		return tl, nil
	}
	return tl, ErrUnknownSource
}

// markCurveDirty queues releases for the curve job.
func markCurveDirty(ctx context.Context, q dbtx, releaseIDs ...int64) error {
	if len(releaseIDs) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `INSERT INTO danmaku_curve_queue(release_id,dirty_since)
		SELECT r, clock_timestamp() FROM (SELECT DISTINCT unnest($1::bigint[]) AS r) d
		ON CONFLICT (release_id) DO UPDATE SET dirty_since=EXCLUDED.dirty_since`, releaseIDs)
	return err
}

// CreateDanmaku inserts a validated danmaku and queues the curve.
func (s *Store) CreateDanmaku(ctx context.Context, d NewDanmaku) (Danmaku, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return Danmaku{}, err
	}
	defer tx.Rollback(ctx)
	out := Danmaku{TMs: d.TMs, Body: d.Body, Color: d.Color, Mode: d.Mode, Lang: d.Lang, Mine: true}
	err = tx.QueryRow(ctx, `INSERT INTO danmaku(release_id,version_key,t_ms,body,color,mode,lang,user_id,status)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,created_at`,
		d.ReleaseID, d.VersionKey, d.TMs, d.Body, d.Color, d.Mode, d.Lang, d.UserID, d.Status).Scan(&out.ID, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	if d.Status == community.StatusVisible {
		if err = markCurveDirty(ctx, tx, d.ReleaseID); err != nil {
			return out, err
		}
	}
	return out, tx.Commit(ctx)
}

// MaxDanmakuPerRequest bounds one danmaku read.
const MaxDanmakuPerRequest = 3000

// DanmakuRange returns danmaku in [fromMS, toMS) on a timeline: visible and
// spoiler-folded ones for everyone, plus viewerID's own shadow ones.
func (s *Store) DanmakuRange(ctx context.Context, tl DanmakuTimeline, fromMS, toMS int, viewerID int64) ([]Danmaku, error) {
	rows, err := s.Query(ctx, `SELECT id,t_ms,body,color,mode,lang,status='folded',user_id=$5,created_at FROM danmaku
		WHERE release_id=$1 AND version_key=$2 AND t_ms>=$3 AND t_ms<$4
		  AND (status IN ('visible','folded') OR (status='shadow' AND user_id=$5))
		ORDER BY t_ms,id LIMIT $6`, tl.ReleaseID, tl.VersionKey, fromMS, toMS, viewerID, MaxDanmakuPerRequest)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Danmaku, error) {
		var d Danmaku
		err := r.Scan(&d.ID, &d.TMs, &d.Body, &d.Color, &d.Mode, &d.Lang, &d.Spoiler, &d.Mine, &d.CreatedAt)
		return d, err
	})
}

// ---------------------------------------------------------------------------
// Peak curve

// DanmakuCurves returns the computed curves of a release by version key;
// versions without a curve (fewer than 50 valid danmaku) are absent.
func (s *Store) DanmakuCurves(ctx context.Context, releaseID int64) (map[string][]int16, error) {
	rows, err := s.Query(ctx, `SELECT version_key,data FROM danmaku_curves WHERE release_id=$1 AND data IS NOT NULL`, releaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]int16{}
	for rows.Next() {
		var k string
		var data []int16
		if err := rows.Scan(&k, &data); err != nil {
			return nil, err
		}
		out[k] = data
	}
	return out, rows.Err()
}

// CurveRun summarizes one RecomputeDanmakuCurves pass.
type CurveRun struct {
	Releases int // releases recomputed
	Curves   int // versions with a curve (>= 50 valid danmaku)
}

// RecomputeDanmakuCurves recomputes the curves of queued releases that are
// due (PRD 5.5.1: released within 7 days every minute, others every 10
// minutes; only releases with changes are queued), at most limit per call.
// Re-running is harmless: results are upserted and a release leaves the
// queue only if nothing changed while it was being computed.
func (s *Store) RecomputeDanmakuCurves(ctx context.Context, limit int) (CurveRun, error) {
	var run CurveRun
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.Query(ctx, `SELECT q.release_id FROM danmaku_curve_queue q JOIN releases r ON r.id=q.release_id
		WHERE NOT EXISTS (SELECT 1 FROM danmaku_curves c WHERE c.release_id=q.release_id AND c.computed_at > now() -
			CASE WHEN COALESCE(r.notified_at, r.created_at) > now() - interval '7 days'
			     THEN interval '50 seconds' ELSE interval '9 minutes 50 seconds' END)
		ORDER BY q.dirty_since LIMIT $1`, limit)
	if err != nil {
		return run, err
	}
	type due struct{ id int64 }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id)
	})
	if err != nil {
		return run, err
	}
	for _, d := range list {
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		n, err := s.recomputeRelease(ctx, d.id)
		if err != nil {
			return run, fmt.Errorf("release %d: %w", d.id, err)
		}
		run.Releases++
		run.Curves += n
	}
	return run, nil
}

// RecomputeReleaseCurves recomputes one release now, regardless of cadence.
func (s *Store) RecomputeReleaseCurves(ctx context.Context, releaseID int64) (int, error) {
	return s.recomputeRelease(ctx, releaseID)
}

func (s *Store) recomputeRelease(ctx context.Context, releaseID int64) (int, error) {
	// The queue mark as of now (database clock): only this mark is cleared
	// at the end, so changes made while computing keep the release queued.
	var dirtySince *time.Time
	err := s.QueryRow(ctx, `SELECT dirty_since FROM danmaku_curve_queue WHERE release_id=$1`, releaseID).Scan(&dirtySince)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	// Durations: main timeline from the reference duration, alt versions from
	// their own source.
	var runtime int
	if err := s.QueryRow(ctx, `SELECT COALESCE(runtime_minutes,0) FROM releases WHERE id=$1`, releaseID).Scan(&runtime); err != nil {
		return 0, err
	}
	rows, err := s.Query(ctx, `SELECT id,version_kind,duration_seconds,priority,status FROM playback_sources WHERE release_id=$1`, releaseID)
	if err != nil {
		return 0, err
	}
	srcs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (PlaybackSource, error) {
		var x PlaybackSource
		return x, r.Scan(&x.ID, &x.VersionKind, &x.DurationSeconds, &x.Priority, &x.Status)
	})
	if err != nil {
		return 0, err
	}
	durations := map[string]int{}
	if ref := ReferenceDuration(srcs, runtime); ref != nil {
		durations[community.DefaultVersion] = *ref * 1000
	}
	for _, x := range srcs {
		if x.DurationSeconds != nil {
			durations[community.AltVersionKey(x.ID)] = *x.DurationSeconds * 1000
		}
	}

	// Valid danmaku: visible, from users never muted.
	rows, err = s.Query(ctx, `SELECT version_key,t_ms,user_id,body FROM danmaku d
		WHERE release_id=$1 AND status='visible' AND NOT EXISTS (SELECT 1 FROM user_mutes m WHERE m.user_id=d.user_id)
		ORDER BY id`, releaseID)
	if err != nil {
		return 0, err
	}
	points := map[string][]community.CurvePoint{}
	for rows.Next() {
		var k string
		var p community.CurvePoint
		if err := rows.Scan(&k, &p.TMs, &p.UserID, &p.Body); err != nil {
			rows.Close()
			return 0, err
		}
		points[k] = append(points[k], p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Versions that had a curve but lost every valid danmaku are reset too.
	var existing []string
	if err := s.QueryRow(ctx, `SELECT COALESCE(array_agg(version_key),'{}') FROM danmaku_curves WHERE release_id=$1`, releaseID).Scan(&existing); err != nil {
		return 0, err
	}
	for _, k := range existing {
		if _, ok := points[k]; !ok {
			points[k] = nil
		}
	}

	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	curves := 0
	for k, pts := range points {
		c := community.ComputeCurve(pts, durations[k])
		if c.Data != nil {
			curves++
		}
		if _, err := tx.Exec(ctx, `INSERT INTO danmaku_curves(release_id,version_key,data,bucket_ms,valid_count,computed_at)
			VALUES($1,$2,$3,$4,$5,now()) ON CONFLICT (release_id,version_key) DO UPDATE
			SET data=EXCLUDED.data,bucket_ms=EXCLUDED.bucket_ms,valid_count=EXCLUDED.valid_count,computed_at=now()`,
			releaseID, k, c.Data, c.BucketMS, c.Valid); err != nil {
			return 0, err
		}
	}
	if dirtySince != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM danmaku_curve_queue WHERE release_id=$1 AND dirty_since<=$2`, releaseID, *dirtySince); err != nil {
			return 0, err
		}
	}
	return curves, tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Ratings

// RatingSummary is the public rating panel of a release.
type RatingSummary struct {
	Score        *float64 `json:"score"` // 10-point scale; null when fewer than 50 votes
	Count        int      `json:"count"`
	Insufficient bool     `json:"insufficient"`
	Distribution []int    `json:"distribution"` // votes per star 1-5; null while the score is hidden
	MyScore      *int     `json:"my_score"`
}

// RatingPrior is the site-wide weighted mean (1-5), cached for priorTTL.
func (s *Store) RatingPrior(ctx context.Context) (float64, error) {
	c := s.cache()
	c.mu.Lock()
	if !c.priorAt.IsZero() && time.Since(c.priorAt) < priorTTL {
		p := c.prior
		c.mu.Unlock()
		return p, nil
	}
	c.mu.Unlock()
	var mean *float64
	if err := s.QueryRow(ctx, `SELECT sum(weight*score)/NULLIF(sum(weight),0) FROM ratings WHERE status='active'`).Scan(&mean); err != nil {
		return 0, err
	}
	p := community.DefaultPriorMean
	if mean != nil {
		p = *mean
	}
	c.mu.Lock()
	c.prior, c.priorAt = p, time.Now()
	c.mu.Unlock()
	return p, nil
}

// RatingSummary aggregates the active votes of a release. voterKey ("" for
// none) selects my_score.
func (s *Store) RatingSummary(ctx context.Context, releaseID int64, voterKey string) (RatingSummary, error) {
	var out RatingSummary
	var agg community.RatingAggregate
	dist := make([]int, 5)
	rows, err := s.Query(ctx, `SELECT score,count(*),COALESCE(sum(weight),0),COALESCE(sum(weight*score),0)
		FROM ratings WHERE release_id=$1 AND status='active' GROUP BY score`, releaseID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var score, n int
		var w, ws float64
		if err := rows.Scan(&score, &n, &w, &ws); err != nil {
			rows.Close()
			return out, err
		}
		if score >= 1 && score <= 5 {
			dist[score-1] = n
		}
		agg.Count += n
		agg.WeightSum += w
		agg.ScoreSum += ws
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	prior, err := s.RatingPrior(ctx)
	if err != nil {
		return out, err
	}
	out.Count = agg.Count
	out.Score = community.DisplayScore(agg, prior)
	out.Insufficient = out.Score == nil
	if out.Score != nil {
		out.Distribution = dist
	}
	if voterKey != "" {
		var my int
		err := s.QueryRow(ctx, `SELECT score FROM ratings WHERE release_id=$1 AND voter_key=$2`, releaseID, voterKey).Scan(&my)
		if err == nil {
			out.MyScore = &my
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	return out, nil
}

// RateRelease records or changes a vote. The weight can only go down on a
// re-vote, so waiting out the new-account period does not upgrade old votes;
// a struck (removed) vote stays removed.
func (s *Store) RateRelease(ctx context.Context, releaseID int64, v community.Voter, score int) error {
	if score < community.MinScore || score > community.MaxScore {
		return invalidQuery("score must be 1-5")
	}
	var userID *int64
	var device *string
	if v.UserID != 0 {
		userID = &v.UserID
	} else {
		h := community.DeviceHash(v.DeviceID)
		device = &h
	}
	_, err := s.Exec(ctx, `INSERT INTO ratings(release_id,voter_key,user_id,device_id,score,weight) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (release_id,voter_key) DO UPDATE SET score=EXCLUDED.score,
		weight=LEAST(ratings.weight,EXCLUDED.weight),updated_at=now()`,
		releaseID, community.VoterKey(v), userID, device, score, community.RatingWeight(v))
	return err
}

// ---------------------------------------------------------------------------
// Reviews

// ReviewAuthor is the public author of a review (PRD 19: reviews show the
// nickname; ratings stay anonymous).
type ReviewAuthor struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
}

// Review is one short review as returned to clients.
type Review struct {
	ID           int64        `json:"id"`
	Body         string       `json:"body"`
	Spoiler      bool         `json:"spoiler"`
	Likes        int          `json:"likes"`
	LikedByMe    bool         `json:"liked_by_me"`
	Mine         bool         `json:"mine"`
	Pending      bool         `json:"pending"` // shadow: only the author sees it
	Author       ReviewAuthor `json:"author"`
	WatchRatio   float64      `json:"watch_ratio"`
	WatchBadge   string       `json:"watch_badge"` // finished | partial | none
	WatchPercent int          `json:"watch_percent"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	status       string
}

// ReviewFilter selects a page of reviews.
type ReviewFilter struct {
	ReleaseID int64
	Sort      string // community.ReviewSort*
	Cursor    *Cursor
	Limit     int
	ViewerID  int64 // 0 for anonymous
}

const (
	defaultReviewLimit = 20
	maxReviewLimit     = 50
	// maxHelpfulOffset bounds the offset-paged "helpful" sort.
	maxHelpfulOffset = 1000
)

const reviewColumns = `rv.id,rv.body,rv.spoiler OR rv.status='folded',rv.likes,
	EXISTS(SELECT 1 FROM review_likes l WHERE l.review_id=rv.id AND l.user_id=$1),
	rv.user_id=$1,rv.status='shadow',u.id,u.display_name,rv.watch_ratio,rv.created_at,rv.updated_at,rv.status`

func scanReview(r rowScanner) (Review, error) {
	var x Review
	var ratio float32
	err := r.Scan(&x.ID, &x.Body, &x.Spoiler, &x.Likes, &x.LikedByMe, &x.Mine, &x.Pending,
		&x.Author.ID, &x.Author.DisplayName, &ratio, &x.CreatedAt, &x.UpdatedAt, &x.status)
	x.WatchRatio = math.Round(float64(ratio)*100) / 100
	x.WatchBadge, x.WatchPercent = community.WatchBadge(float64(ratio))
	return x, err
}

// Reviews lists a page of reviews. "newest" and "finished" use a keyset
// cursor; "helpful" depends on the current time, so its cursor is an offset.
func (s *Store) Reviews(ctx context.Context, f ReviewFilter) ([]Review, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = defaultReviewLimit
	}
	limit = min(limit, maxReviewLimit)
	if !community.ValidReviewSort(f.Sort) {
		return nil, "", invalidQuery("invalid sort")
	}
	if f.Cursor != nil && f.Cursor.Sort != f.Sort {
		return nil, "", invalidQuery("cursor does not match sort")
	}
	args := []any{f.ViewerID, f.ReleaseID}
	where := `rv.release_id=$2 AND (rv.status IN ('visible','folded') OR (rv.status='shadow' AND rv.user_id=$1))`
	var order string
	offset := 0
	switch f.Sort {
	case community.ReviewSortHelpful:
		order = fmt.Sprintf(`(rv.likes+1)*power(2, -extract(epoch FROM now()-rv.created_at)/%d) DESC, rv.id DESC`,
			int64(community.HelpfulHalfLife.Seconds()))
		if f.Cursor != nil {
			n, err := strconv.Atoi(f.Cursor.Key)
			if err != nil || n < 0 || n > maxHelpfulOffset {
				return nil, "", invalidQuery("malformed cursor")
			}
			offset = n
		}
	default:
		if f.Sort == community.ReviewSortFinished {
			where += fmt.Sprintf(` AND rv.watch_ratio >= %g`, community.FinishedRatio)
		}
		order = `rv.created_at DESC, rv.id DESC`
		if f.Cursor != nil {
			t, err := time.Parse(time.RFC3339Nano, f.Cursor.Key)
			if err != nil {
				return nil, "", invalidQuery("malformed cursor")
			}
			args = append(args, t, f.Cursor.ID)
			where += ` AND (rv.created_at, rv.id) < ($3, $4)`
		}
	}
	args = append(args, limit+1, offset)
	q := fmt.Sprintf(`SELECT %s FROM reviews rv JOIN users u ON u.id=rv.user_id WHERE %s ORDER BY %s LIMIT $%d OFFSET $%d`,
		reviewColumns, where, order, len(args)-1, len(args))
	rows, err := s.Query(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Review, error) { return scanReview(r) })
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[limit-1]
		c := Cursor{Sort: f.Sort, ID: last.ID}
		if f.Sort == community.ReviewSortHelpful {
			if offset+limit <= maxHelpfulOffset {
				c.Key = strconv.Itoa(offset + limit)
				next = EncodeCursor(c)
			}
		} else {
			c.Key = last.CreatedAt.UTC().Format(time.RFC3339Nano)
			next = EncodeCursor(c)
		}
	}
	if out == nil {
		out = []Review{}
	}
	return out, next, nil
}

// watchRatio is the user's progress on a release from watch_history (0-1).
func watchRatio(ctx context.Context, q dbtx, userID, releaseID int64) (float64, error) {
	var r float64
	err := q.QueryRow(ctx, `SELECT COALESCE(LEAST(1, GREATEST(0, progress_seconds::float8 / NULLIF(duration_seconds,0))), 0)
		FROM watch_history WHERE user_id=$1 AND release_id=$2`, userID, releaseID).Scan(&r)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return r, err
}

func (s *Store) reviewByID(ctx context.Context, q dbtx, id, viewerID int64) (Review, error) {
	return scanReview(q.QueryRow(ctx, `SELECT `+reviewColumns+` FROM reviews rv JOIN users u ON u.id=rv.user_id WHERE rv.id=$2`, viewerID, id))
}

// CreateReview writes the user's review of a release (one live review per
// user and release: a second one is a unique violation, 409). status is
// visible, or shadow after a keyword hit.
func (s *Store) CreateReview(ctx context.Context, releaseID, userID int64, body string, spoiler bool, status string) (Review, error) {
	ratio, err := watchRatio(ctx, s, userID, releaseID)
	if err != nil {
		return Review{}, err
	}
	var id int64
	err = s.QueryRow(ctx, `INSERT INTO reviews(release_id,user_id,body,spoiler,watch_ratio,status) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
		releaseID, userID, body, spoiler, ratio, status).Scan(&id)
	if err != nil {
		return Review{}, err
	}
	return s.reviewByID(ctx, s, id, userID)
}

// ReviewEdit is a partial update of one's own review.
type ReviewEdit struct {
	Body       *string
	Spoiler    *bool
	KeywordHit bool // the new body hit the keyword list: the review goes to shadow
}

// UpdateReview edits the user's own live review and refreshes the watch
// badge. A keyword hit moves it to shadow; an edit never lifts a shadow or a
// moderator's fold.
func (s *Store) UpdateReview(ctx context.Context, reviewID, userID int64, e ReviewEdit) (Review, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback(ctx)
	var owner, releaseID int64
	var status string
	err = tx.QueryRow(ctx, `SELECT user_id,release_id,status FROM reviews WHERE id=$1 AND status<>'deleted' FOR UPDATE`, reviewID).Scan(&owner, &releaseID, &status)
	if err != nil {
		return Review{}, err
	}
	if owner != userID {
		return Review{}, ErrNotOwner
	}
	if e.KeywordHit && status == community.StatusVisible {
		status = community.StatusShadow
	}
	ratio, err := watchRatio(ctx, tx, userID, releaseID)
	if err != nil {
		return Review{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE reviews SET body=COALESCE($2,body),spoiler=COALESCE($3,spoiler),status=$4,
		watch_ratio=$5,updated_at=now() WHERE id=$1`, reviewID, e.Body, e.Spoiler, status, ratio); err != nil {
		return Review{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Review{}, err
	}
	return s.reviewByID(ctx, s, reviewID, userID)
}

// DeleteReview soft-deletes the user's own review.
func (s *Store) DeleteReview(ctx context.Context, reviewID, userID int64) error {
	var owner int64
	err := s.QueryRow(ctx, `SELECT user_id FROM reviews WHERE id=$1 AND status<>'deleted'`, reviewID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != userID {
		return ErrNotOwner
	}
	_, err = s.Exec(ctx, `UPDATE reviews SET status='deleted',updated_at=now() WHERE id=$1`, reviewID)
	return err
}

// SetReviewLike likes or unlikes a public review (once per user, can be
// undone) and returns the new like count. Shadow and deleted reviews are 404.
func (s *Store) SetReviewLike(ctx context.Context, reviewID, userID int64, like bool) (int, error) {
	tx, err := s.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var likes int
	if err = tx.QueryRow(ctx, `SELECT likes FROM reviews WHERE id=$1 AND status IN ('visible','folded') FOR UPDATE`, reviewID).Scan(&likes); err != nil {
		return 0, err
	}
	var tag string
	if like {
		tag = `INSERT INTO review_likes(user_id,review_id) VALUES($1,$2) ON CONFLICT DO NOTHING`
	} else {
		tag = `DELETE FROM review_likes WHERE user_id=$1 AND review_id=$2`
	}
	ct, err := tx.Exec(ctx, tag, userID, reviewID)
	if err != nil {
		return 0, err
	}
	if ct.RowsAffected() > 0 {
		delta := 1
		if !like {
			delta = -1
		}
		if err = tx.QueryRow(ctx, `UPDATE reviews SET likes=GREATEST(0,likes+$2) WHERE id=$1 RETURNING likes`, reviewID, delta).Scan(&likes); err != nil {
			return 0, err
		}
	}
	return likes, tx.Commit(ctx)
}

// ---------------------------------------------------------------------------
// Reports

// Report is one row of the report inbox.
type Report struct {
	ID         int64      `json:"id"`
	TargetType string     `json:"target_type"`
	TargetID   int64      `json:"target_id"`
	ReporterID *int64     `json:"reporter_id"`
	Reason     string     `json:"reason"`
	Note       string     `json:"note"`
	Status     string     `json:"status"`
	DueAt      time.Time  `json:"due_at"`
	Overdue    bool       `json:"overdue"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
	Resolution *string    `json:"resolution"`
	Target     any        `json:"target,omitempty"` // preview for the moderation queue
}

// reportTargetExists checks the target of a report. Users can only report
// content they can see: hidden danmaku and reviews count as missing.
var reportTargetExists = map[string]string{
	"danmaku":        `SELECT EXISTS(SELECT 1 FROM danmaku WHERE id=$1 AND status IN ('visible','folded'))`,
	"review":         `SELECT EXISTS(SELECT 1 FROM reviews WHERE id=$1 AND status IN ('visible','folded'))`,
	"rating":         `SELECT EXISTS(SELECT 1 FROM ratings WHERE id=$1)`,
	"release":        `SELECT EXISTS(SELECT 1 FROM releases WHERE id=$1)`,
	"performer_info": `SELECT EXISTS(SELECT 1 FROM performers WHERE id=$1)`,
	"source":         `SELECT EXISTS(SELECT 1 FROM playback_sources WHERE id=$1)`,
}

// CreateReport files a report with its SLA deadline. A second open report
// by the same user on the same target returns the existing one
// (created=false).
func (s *Store) CreateReport(ctx context.Context, targetType string, targetID, reporterID int64, reason, note string) (Report, bool, error) {
	q, ok := reportTargetExists[targetType]
	if !ok || !community.ValidReportReason(reason) {
		return Report{}, false, invalidQuery("invalid report")
	}
	var exists bool
	if err := s.QueryRow(ctx, q, targetID).Scan(&exists); err != nil {
		return Report{}, false, err
	}
	if !exists {
		return Report{}, false, ErrTargetNotFound
	}
	now := time.Now()
	row := s.QueryRow(ctx, `INSERT INTO reports(target_type,target_id,reporter_id,reason,note,due_at) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT (target_type,target_id,reporter_id) WHERE status='open' DO NOTHING
		RETURNING `+reportColumns, targetType, targetID, reporterID, reason, note, community.ReportDue(reason, now))
	r, err := scanReport(row)
	if errors.Is(err, pgx.ErrNoRows) {
		r, err = scanReport(s.QueryRow(ctx, `SELECT `+reportColumns+` FROM reports
			WHERE target_type=$1 AND target_id=$2 AND reporter_id=$3 AND status='open'`, targetType, targetID, reporterID))
		return r, false, err
	}
	return r, err == nil, err
}

const reportColumns = `id,target_type,target_id,reporter_id,reason,note,status,due_at,created_at,resolved_at,resolution`

func scanReport(r rowScanner) (Report, error) {
	var x Report
	err := r.Scan(&x.ID, &x.TargetType, &x.TargetID, &x.ReporterID, &x.Reason, &x.Note, &x.Status, &x.DueAt, &x.CreatedAt, &x.ResolvedAt, &x.Resolution)
	x.Overdue = x.Status == "open" && time.Now().After(x.DueAt)
	return x, err
}
