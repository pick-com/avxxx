package api

// Community routes (PRD 5.5, 19; tech plan 3.8): danmaku, ratings, short
// reviews and reports. Reading is public; posting needs a user token except
// ratings, which also accept an anonymous device vote (X-Device-ID).

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// communityStore is the subset of *store.Store used by the community
// handlers, as an interface so they can be tested without a database.
type communityStore interface {
	ReleaseIDBySlug(ctx context.Context, slug string) (int64, error)
	PosterInfo(ctx context.Context, userID int64) (store.Poster, error)
	ModerationMatcher(ctx context.Context) (*community.Matcher, error)

	DanmakuTimelineFor(ctx context.Context, slug string, sourceID int64) (store.DanmakuTimeline, error)
	DanmakuRange(ctx context.Context, tl store.DanmakuTimeline, fromMS, toMS int, viewerID int64) ([]store.Danmaku, error)
	CreateDanmaku(ctx context.Context, d store.NewDanmaku) (store.Danmaku, error)

	RatingSummary(ctx context.Context, releaseID int64, voterKey string) (store.RatingSummary, error)
	RateRelease(ctx context.Context, releaseID int64, v community.Voter, score int) error

	Reviews(ctx context.Context, f store.ReviewFilter) ([]store.Review, string, error)
	CreateReview(ctx context.Context, releaseID, userID int64, body string, spoiler bool, status string) (store.Review, error)
	UpdateReview(ctx context.Context, reviewID, userID int64, e store.ReviewEdit) (store.Review, error)
	DeleteReview(ctx context.Context, reviewID, userID int64) error
	SetReviewLike(ctx context.Context, reviewID, userID int64, like bool) (int, error)

	CreateReport(ctx context.Context, targetType string, targetID, reporterID int64, reason, note string) (store.Report, bool, error)
}

// Danmaku reads are served in 60-second segments (tech plan 3.8) so the CDN
// can cache them; one request may span a few segments.
const (
	danmakuSegmentMS   = 60_000
	danmakuMaxSegments = 5
	danmakuMaxTMS      = 24 * 3600 * 1000
	danmakuTailSlackMS = 5_000 // allowed past the known duration (rounding)
	danmakuCacheMaxAge = 30    // seconds, anonymous reads
	maxReportNoteRunes = 500
	deviceHeader       = "X-Device-ID"
)

func (srv *Server) communityRoutes() []route {
	return []route{
		{"GET /api/releases/{slug}/danmaku", srv.withCommunity(srv.listDanmaku)},
		{"POST /api/releases/{slug}/danmaku", srv.user(srv.postDanmaku)},
		{"POST /api/danmaku/{id}/report", srv.user(srv.reportTarget("danmaku"))},

		{"GET /api/releases/{slug}/rating", srv.withCommunity(srv.getRating)},
		{"PUT /api/releases/{slug}/rating", srv.withCommunity(srv.putRating)},

		{"GET /api/releases/{slug}/reviews", srv.withCommunity(srv.listReviews)},
		{"POST /api/releases/{slug}/reviews", srv.user(srv.createReview)},
		{"PATCH /api/reviews/{id}", srv.user(srv.updateReview)},
		{"DELETE /api/reviews/{id}", srv.user(srv.deleteReview)},
		{"PUT /api/reviews/{id}/like", srv.user(srv.likeReview(true))},
		{"DELETE /api/reviews/{id}/like", srv.user(srv.likeReview(false))},
		{"POST /api/reviews/{id}/report", srv.user(srv.reportTarget("review"))},

		{"POST /api/reports", srv.user(srv.createReport)},
	}
}

// Extra statuses used by the community routes.
func forbidden(msg string) error {
	return &httpError{status: http.StatusForbidden, msg: msg}
}

func tooManyRequests(msg string) error {
	return &httpError{status: http.StatusTooManyRequests, msg: msg}
}

var errUnavailable = &httpError{status: http.StatusServiceUnavailable, msg: statusMessage(http.StatusServiceUnavailable)}

// replyStatus writes v as JSON with a non-200 success status.
func replyStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// communityErr maps community store errors to client errors.
func communityErr(e error) error {
	switch {
	case e == nil:
		return nil
	case errors.Is(e, store.ErrUnknownSource):
		return badRequest(store.ErrUnknownSource.Error(), e)
	case errors.Is(e, store.ErrNotOwner):
		return &httpError{status: http.StatusForbidden, msg: store.ErrNotOwner.Error(), err: e}
	case errors.Is(e, store.ErrTargetNotFound):
		return &httpError{status: http.StatusNotFound, msg: store.ErrTargetNotFound.Error(), err: e}
	case errors.Is(e, store.ErrInvalidAction):
		return badRequest(store.ErrInvalidAction.Error(), e)
	case errors.Is(e, store.ErrReportClosed):
		return conflict(store.ErrReportClosed.Error(), e)
	}
	return e
}

// withCommunity guards public community routes when there is no store.
func (srv *Server) withCommunity(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if srv.community == nil {
			reply(w, nil, errUnavailable)
			return
		}
		h(w, r)
	}
}

// viewerID is optionalUserID as a plain id (0 for anonymous readers).
func (srv *Server) viewerID(r *http.Request) int64 {
	if id := srv.optionalUserID(r); id != nil {
		return *id
	}
	return 0
}

func pathInt64(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid "+name, err)
	}
	return id, nil
}

// allowPost applies the posting checks shared by danmaku and reviews:
// muted users are 403, then the per-minute limit (PRD 5.5: accounts younger
// than 3 days at most 3 per minute) is 429.
func (srv *Server) allowPost(ctx context.Context, kind string, uid int64) (store.Poster, error) {
	p, err := srv.community.PosterInfo(ctx, uid)
	if err != nil {
		return p, err
	}
	if p.Muted(time.Now()) {
		return p, forbidden("muted until " + p.MutedUntil.UTC().Format(time.RFC3339))
	}
	if srv.limiter != nil {
		ok, err := srv.limiter.Allow(ctx, kind+":"+strconv.FormatInt(uid, 10), community.PostingLimit(kind, p.AccountAge), community.PostingLimitWindow)
		if err != nil {
			return p, err
		}
		if !ok {
			return p, tooManyRequests("too many posts, try again in a minute")
		}
	}
	return p, nil
}

// moderationStatus runs the keyword check: a hit is stored as shadow.
func (srv *Server) moderationStatus(ctx context.Context, text string) (string, error) {
	m, err := srv.community.ModerationMatcher(ctx)
	if err != nil {
		return "", err
	}
	if _, hit := m.Match(text); hit {
		return community.StatusShadow, nil
	}
	return community.StatusVisible, nil
}

// --- danmaku ---------------------------------------------------------------

// danmakuWindow parses from_ms / to_ms and widens them to whole segments.
func danmakuWindow(q url.Values) (from, to int, err error) {
	from, err = intParam(q, "from_ms", danmakuMaxTMS)
	if err != nil {
		return 0, 0, err
	}
	from = from / danmakuSegmentMS * danmakuSegmentMS
	to = from + danmakuSegmentMS
	if raw := strings.TrimSpace(q.Get("to_ms")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= from || n > danmakuMaxTMS+danmakuSegmentMS {
			return 0, 0, badRequest("invalid to_ms", err)
		}
		to = (n + danmakuSegmentMS - 1) / danmakuSegmentMS * danmakuSegmentMS
	}
	if to-from > danmakuMaxSegments*danmakuSegmentMS {
		return 0, 0, badRequest("range too large: at most 5 segments of 60 s", nil)
	}
	return from, to, nil
}

func sourceParam(q url.Values) (int64, error) {
	raw := strings.TrimSpace(q.Get("source_id"))
	if raw == "" {
		raw = strings.TrimSpace(q.Get("source"))
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return 0, badRequest("invalid source_id", err)
	}
	return n, nil
}

// listDanmaku: GET /api/releases/{slug}/danmaku?from_ms=&to_ms=&source_id=.
// Anonymous responses are public and cacheable for 30 s; a signed-in reader
// also gets their own shadowed danmaku, so the response is private.
func (srv *Server) listDanmaku(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, to, err := danmakuWindow(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	sourceID, err := sourceParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	tl, err := srv.community.DanmakuTimelineFor(r.Context(), r.PathValue("slug"), sourceID)
	if err != nil {
		reply(w, nil, communityErr(err))
		return
	}
	viewer := srv.viewerID(r)
	items, err := srv.community.DanmakuRange(r.Context(), tl, from, to, viewer)
	if items == nil {
		items = []store.Danmaku{}
	}
	w.Header().Set("Vary", "Authorization")
	if err == nil {
		if viewer == 0 {
			w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(danmakuCacheMaxAge))
		} else {
			w.Header().Set("Cache-Control", "private, no-store")
		}
	}
	reply(w, map[string]any{"version_key": tl.VersionKey, "segment_ms": danmakuSegmentMS, "from_ms": from, "to_ms": to, "items": items}, err)
}

type danmakuInput struct {
	TMs      *int   `json:"t_ms"`
	Body     string `json:"body"`
	Color    string `json:"color"`
	Mode     string `json:"mode"`
	Lang     string `json:"lang"`
	SourceID int64  `json:"source_id"`
}

// validate normalizes the input (defaults, upper-case color).
func (in *danmakuInput) validate() error {
	if in.TMs == nil || *in.TMs < 0 || *in.TMs > danmakuMaxTMS {
		return badRequest("t_ms required (0 to 24h)", nil)
	}
	body, err := community.CleanBody(in.Body, community.MaxDanmakuRunes, false)
	if err != nil {
		return badRequest("body: "+err.Error()+" (1-50 characters)", nil)
	}
	in.Body = body
	if in.Color == "" {
		in.Color = community.DefaultColor
	}
	if !community.ValidColor(in.Color) {
		return badRequest("color must be #RRGGBB", nil)
	}
	in.Color = strings.ToUpper(in.Color)
	if in.Mode == "" {
		in.Mode = community.ModeScroll
	}
	if !community.ValidMode(in.Mode) {
		return badRequest("mode must be scroll, top or bottom", nil)
	}
	if !community.ValidLang(in.Lang) {
		return badRequest("invalid lang", nil)
	}
	if in.SourceID < 0 {
		return badRequest("invalid source_id", nil)
	}
	return nil
}

// postDanmaku: POST /api/releases/{slug}/danmaku (login required).
func (srv *Server) postDanmaku(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.community == nil {
		reply(w, nil, errUnavailable)
		return
	}
	var in danmakuInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if err := in.validate(); err != nil {
		reply(w, nil, err)
		return
	}
	ctx := r.Context()
	tl, err := srv.community.DanmakuTimelineFor(ctx, r.PathValue("slug"), in.SourceID)
	if err != nil {
		reply(w, nil, communityErr(err))
		return
	}
	if tl.DurationMS > 0 && *in.TMs > tl.DurationMS+danmakuTailSlackMS {
		reply(w, nil, badRequest("t_ms is past the end of the video", nil))
		return
	}
	if _, err := srv.allowPost(ctx, "danmaku", uid); err != nil {
		reply(w, nil, err)
		return
	}
	status, err := srv.moderationStatus(ctx, in.Body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	d, err := srv.community.CreateDanmaku(ctx, store.NewDanmaku{ReleaseID: tl.ReleaseID, VersionKey: tl.VersionKey, TMs: *in.TMs,
		Body: in.Body, Color: in.Color, Mode: in.Mode, Lang: community.DetectLang(in.Body, in.Lang), UserID: uid, Status: status})
	if err != nil {
		reply(w, nil, err)
		return
	}
	// A shadowed danmaku looks posted to its author (it is shown to them).
	replyStatus(w, http.StatusCreated, d)
}

// --- ratings ---------------------------------------------------------------

// voterKeyFor identifies the caller for my_score without failing: a valid
// token, else a valid X-Device-ID, else nobody.
func (srv *Server) voterKeyFor(r *http.Request) string {
	if uid := srv.viewerID(r); uid != 0 {
		return community.VoterKey(community.Voter{UserID: uid})
	}
	if d := strings.TrimSpace(r.Header.Get(deviceHeader)); community.ValidDeviceID(d) {
		return community.VoterKey(community.Voter{DeviceID: d})
	}
	return ""
}

// limitAnonVoteIP bounds anonymous votes (ratings and 赞 / 踩) per client
// address. Each anonymous vote counts once per X-Device-ID, but the device
// id is the caller's own choice: without a per-address limit one client
// could cast any number of votes by sending a new id with each.
var limitAnonVoteIP = rateRule{60, 10 * time.Minute}

// allowAnonVote applies limitAnonVoteIP to an anonymous voter (signed-in
// voters are one vote per account and have their own limits).
func (srv *Server) allowAnonVote(r *http.Request, v community.Voter) error {
	if v.UserID != 0 || srv.allow(r, "anon-vote:"+srv.ip(r), limitAnonVoteIP) {
		return nil
	}
	return tooManyRequests("too many votes from this address, try again later")
}

// voter identifies who casts a vote: a token (must be valid when sent) or an
// anonymous X-Device-ID.
func (srv *Server) voter(r *http.Request) (community.Voter, error) {
	if bearerToken(r) != "" {
		if srv.accounts == nil {
			return community.Voter{}, errUnauthorized
		}
		uid, err := srv.userID(r)
		if err != nil {
			return community.Voter{}, err
		}
		p, err := srv.community.PosterInfo(r.Context(), uid)
		if err != nil {
			return community.Voter{}, err
		}
		return community.Voter{UserID: uid, AccountAge: p.AccountAge, Abnormal: p.Abnormal()}, nil
	}
	d := strings.TrimSpace(r.Header.Get(deviceHeader))
	if !community.ValidDeviceID(d) {
		return community.Voter{}, badRequest("sign in or send a valid "+deviceHeader+" header", nil)
	}
	return community.Voter{DeviceID: d}, nil
}

func (srv *Server) ratingSummary(w http.ResponseWriter, r *http.Request, releaseID int64) {
	v, err := srv.community.RatingSummary(r.Context(), releaseID, srv.voterKeyFor(r))
	w.Header().Set("Cache-Control", "private, no-store")
	reply(w, v, err)
}

// getRating: GET /api/releases/{slug}/rating — 10-point score (null under 50
// votes), count and the caller's own score.
func (srv *Server) getRating(w http.ResponseWriter, r *http.Request) {
	id, err := srv.community.ReleaseIDBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	srv.ratingSummary(w, r, id)
}

// putRating: PUT /api/releases/{slug}/rating {"score": 1-5}. Signed-in users
// vote as themselves; anonymous callers vote per device with lower weight.
func (srv *Server) putRating(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Score int `json:"score"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Score < community.MinScore || in.Score > community.MaxScore {
		reply(w, nil, badRequest("score must be 1-5", nil))
		return
	}
	v, err := srv.voter(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.allowAnonVote(r, v); err != nil {
		reply(w, nil, err)
		return
	}
	id, err := srv.community.ReleaseIDBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	if err := srv.community.RateRelease(r.Context(), id, v, in.Score); err != nil {
		reply(w, nil, err)
		return
	}
	srv.ratingSummary(w, r, id)
}

// --- reviews ---------------------------------------------------------------

// listReviews: GET /api/releases/{slug}/reviews?sort=helpful|newest|finished&limit=&cursor=.
func (srv *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ReviewFilter{Sort: strings.TrimSpace(q.Get("sort"))}
	if f.Sort == "" {
		f.Sort = community.ReviewSortHelpful
	}
	if !community.ValidReviewSort(f.Sort) {
		reply(w, nil, badRequest("sort must be helpful, newest or finished", nil))
		return
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		reply(w, nil, err)
		return
	}
	if f.Cursor, err = cursorParam(q, f.Sort); err != nil {
		reply(w, nil, err)
		return
	}
	if f.ReleaseID, err = srv.community.ReleaseIDBySlug(r.Context(), r.PathValue("slug")); err != nil {
		reply(w, nil, err)
		return
	}
	f.ViewerID = srv.viewerID(r)
	items, next, err := srv.community.Reviews(r.Context(), f)
	reply(w, page(items, next), err)
}

func cleanReview(body string) (string, error) {
	b, err := community.CleanBody(body, community.MaxReviewRunes, true)
	if err != nil {
		return "", badRequest("body: "+err.Error()+" (1-140 characters)", nil)
	}
	return b, nil
}

// createReview: POST /api/releases/{slug}/reviews {"body", "spoiler"}.
func (srv *Server) createReview(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.community == nil {
		reply(w, nil, errUnavailable)
		return
	}
	var in struct {
		Body    string `json:"body"`
		Spoiler bool   `json:"spoiler"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	body, err := cleanReview(in.Body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	ctx := r.Context()
	releaseID, err := srv.community.ReleaseIDBySlug(ctx, r.PathValue("slug"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	if _, err := srv.allowPost(ctx, "review", uid); err != nil {
		reply(w, nil, err)
		return
	}
	status, err := srv.moderationStatus(ctx, body)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.community.CreateReview(ctx, releaseID, uid, body, in.Spoiler, status)
	if err != nil {
		if code, _ := errorStatus(err); code == http.StatusConflict {
			err = conflict("you already reviewed this release; edit it instead", err)
		}
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// updateReview: PATCH /api/reviews/{id} {"body"?, "spoiler"?} (own review).
func (srv *Server) updateReview(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.community == nil {
		reply(w, nil, errUnavailable)
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in struct {
		Body    *string `json:"body"`
		Spoiler *bool   `json:"spoiler"`
	}
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	if in.Body == nil && in.Spoiler == nil {
		reply(w, nil, badRequest("nothing to update", nil))
		return
	}
	e := store.ReviewEdit{Spoiler: in.Spoiler}
	ctx := r.Context()
	if in.Body != nil {
		body, err := cleanReview(*in.Body)
		if err != nil {
			reply(w, nil, err)
			return
		}
		if _, err := srv.allowPost(ctx, "review", uid); err != nil {
			reply(w, nil, err)
			return
		}
		status, err := srv.moderationStatus(ctx, body)
		if err != nil {
			reply(w, nil, err)
			return
		}
		e.Body, e.KeywordHit = &body, status == community.StatusShadow
	}
	v, err := srv.community.UpdateReview(ctx, id, uid, e)
	reply(w, v, communityErr(err))
}

// deleteReview: DELETE /api/reviews/{id} (own review).
func (srv *Server) deleteReview(w http.ResponseWriter, r *http.Request, uid int64) {
	if srv.community == nil {
		reply(w, nil, errUnavailable)
		return
	}
	id, err := pathInt64(r, "id")
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = communityErr(srv.community.DeleteReview(r.Context(), id, uid))
	reply(w, map[string]bool{"deleted": err == nil}, err)
}

// likeReview: PUT / DELETE /api/reviews/{id}/like.
func (srv *Server) likeReview(like bool) userHandler {
	return func(w http.ResponseWriter, r *http.Request, uid int64) {
		if srv.community == nil {
			reply(w, nil, errUnavailable)
			return
		}
		id, err := pathInt64(r, "id")
		if err != nil {
			reply(w, nil, err)
			return
		}
		n, err := srv.community.SetReviewLike(r.Context(), id, uid, like)
		reply(w, map[string]any{"liked": like, "likes": n}, err)
	}
}

// --- reports ---------------------------------------------------------------

type reportInput struct {
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
	Reason     string `json:"reason"`
	Note       string `json:"note"`
}

func (srv *Server) fileReport(w http.ResponseWriter, r *http.Request, uid int64, in reportInput) {
	if srv.community == nil {
		reply(w, nil, errUnavailable)
		return
	}
	if !community.ValidReportTarget(in.TargetType) || in.TargetID <= 0 {
		reply(w, nil, badRequest("invalid target", nil))
		return
	}
	if !community.ValidReportReason(in.Reason) {
		reply(w, nil, badRequest("reason must be one of "+strings.Join(community.ReportReasons, ", "), nil))
		return
	}
	in.Note = strings.TrimSpace(in.Note)
	if utf8.RuneCountInString(in.Note) > maxReportNoteRunes {
		reply(w, nil, badRequest("note too long", nil))
		return
	}
	rep, created, err := srv.community.CreateReport(r.Context(), in.TargetType, in.TargetID, uid, in.Reason, in.Note)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = store.ErrTargetNotFound
		}
		reply(w, nil, communityErr(err))
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	replyStatus(w, code, map[string]any{"id": rep.ID, "status": rep.Status, "created": created})
}

// reportTarget: POST /api/danmaku/{id}/report and /api/reviews/{id}/report
// with {"reason", "note"?}.
func (srv *Server) reportTarget(targetType string) userHandler {
	return func(w http.ResponseWriter, r *http.Request, uid int64) {
		id, err := pathInt64(r, "id")
		if err != nil {
			reply(w, nil, err)
			return
		}
		var in reportInput
		if err := readJSON(r, &in, false); err != nil {
			reply(w, nil, err)
			return
		}
		if in.TargetType != "" && in.TargetType != targetType || in.TargetID != 0 && in.TargetID != id {
			reply(w, nil, badRequest("target does not match the path", nil))
			return
		}
		in.TargetType, in.TargetID = targetType, id
		srv.fileReport(w, r, uid, in)
	}
}

// createReport: POST /api/reports {"target_type", "target_id", "reason", "note"?}
// for every target type (release, performer_info, source, rating included).
func (srv *Server) createReport(w http.ResponseWriter, r *http.Request, uid int64) {
	var in reportInput
	if err := readJSON(r, &in, false); err != nil {
		reply(w, nil, err)
		return
	}
	srv.fileReport(w, r, uid, in)
}
