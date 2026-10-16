package api

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// stubInteractions is an in-memory interactionStore.
type stubInteractions struct {
	edits    []store.CommentEdit
	votes    []int
	voters   []community.Voter
	keys     []string // voter keys passed to ReactionSummary
	comments []store.NewComment
	filters  []store.CommentFilter
	likes    []bool
}

func (s *stubInteractions) ReactionSummary(_ context.Context, _ int64, key string) (store.ReactionSummary, error) {
	s.keys = append(s.keys, key)
	out := store.ReactionSummary{Likes: 7}
	if len(s.votes) > 0 {
		out.MyVote = s.votes[len(s.votes)-1]
	}
	return out, nil
}
func (s *stubInteractions) SetReaction(_ context.Context, _ int64, v community.Voter, value int) error {
	s.voters = append(s.voters, v)
	s.votes = append(s.votes, value)
	return nil
}
func (s *stubInteractions) Comments(_ context.Context, f store.CommentFilter) ([]store.Comment, string, error) {
	s.filters = append(s.filters, f)
	return []store.Comment{{ID: 1, Body: "hi", Author: store.ReviewAuthor{ID: 2, DisplayName: "Ann"}}}, "", nil
}
func (s *stubInteractions) CommentReplies(_ context.Context, parentID, _ int64, _ *store.Cursor, _ int) ([]store.Comment, string, error) {
	if parentID == 404 {
		return nil, "", pgx.ErrNoRows
	}
	return []store.Comment{}, "", nil
}
func (s *stubInteractions) CreateComment(_ context.Context, n store.NewComment) (store.Comment, error) {
	if n.ParentID == 404 {
		return store.Comment{}, store.ErrCommentsParent
	}
	s.comments = append(s.comments, n)
	return store.Comment{ID: 9, Body: n.Body, Mine: true, Pending: n.Status == community.StatusShadow}, nil
}
func (s *stubInteractions) EditComment(_ context.Context, id, _ int64, e store.CommentEdit) (store.Comment, error) {
	switch id {
	case 403:
		return store.Comment{}, store.ErrNotOwner
	case 404:
		return store.Comment{}, pgx.ErrNoRows
	case 409:
		return store.Comment{}, store.ErrEditWindow
	}
	s.edits = append(s.edits, e)
	now := time.Now()
	return store.Comment{ID: id, Body: e.Body, Mine: true, Pending: e.KeywordHit, EditedAt: &now}, nil
}
func (s *stubInteractions) DeleteComment(_ context.Context, id, _ int64) error {
	switch id {
	case 403:
		return store.ErrNotOwner
	case 404:
		return pgx.ErrNoRows
	}
	return nil
}
func (s *stubInteractions) SetCommentLike(_ context.Context, _, _ int64, like bool) (int, error) {
	s.likes = append(s.likes, like)
	if like {
		return 1, nil
	}
	return 0, nil
}

func newInteractionServer(i *stubInteractions, c *stubCommunity, commentsOff bool) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, community: c, interactions: i, moderation: &stubModeration{},
		limiter: community.NewMemoryLimiter(), adminToken: "admintok", adminAuth: newFakeAdmins(), commentsOff: commentsOff}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestReactionRoutes(t *testing.T) {
	i := &stubInteractions{}
	h := newInteractionServer(i, &stubCommunity{poster: oldPoster}, false)
	auth := map[string]string{"Authorization": "Bearer good"}
	device := map[string]string{"X-Device-ID": "device-abc-123"}
	path := "/api/releases/abc-123/reaction"

	rec := doH(h, "GET", path, "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"likes":7`) || strings.Contains(rec.Body.String(), "dislike") {
		t.Fatalf("GET: %d %s (only the 赞 count is public)", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("vote summary carries my_vote, must be private: %q", cc)
	}
	if rec := doH(h, "PUT", path, `{"value":1}`, nil); rec.Code != 400 {
		t.Errorf("anonymous vote without device id: %d, want 400", rec.Code)
	}
	// Ordered: the stored votes are checked in request order below.
	for _, c := range []struct {
		body string
		want int
	}{{`{"value":2}`, 400}, {`{}`, 400}, {`{"value":"x"}`, 400}, {`{"value":-1}`, 200}, {`{"value":0}`, 200}} {
		if rec := doH(h, "PUT", path, c.body, device); rec.Code != c.want {
			t.Errorf("PUT %s: %d, want %d (%s)", c.body, rec.Code, c.want, rec.Body)
		}
	}
	if rec := doH(h, "PUT", path, `{"value":1}`, auth); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"my_vote":1`) {
		t.Errorf("user vote: %d %s", rec.Code, rec.Body)
	}
	if rec := doH(h, "DELETE", path, "", auth); rec.Code != 200 {
		t.Errorf("undo: %d", rec.Code)
	}
	if got := i.votes; len(got) != 4 || got[0] != -1 || got[1] != 0 || got[2] != 1 || got[3] != 0 {
		t.Errorf("votes %v", got)
	}
	if i.voters[0].DeviceID != "device-abc-123" || i.voters[2].UserID != 1 {
		t.Errorf("voters %+v", i.voters)
	}
	if rec := doH(h, "PUT", "/api/releases/nope/reaction", `{"value":1}`, auth); rec.Code != 404 {
		t.Errorf("unknown release: %d", rec.Code)
	}
	// Toggling is rate limited per voter.
	for n := 0; n < community.ReactionPerMinute; n++ {
		doH(h, "PUT", path, `{"value":1}`, map[string]string{"X-Device-ID": "flooding-device"})
	}
	if rec := doH(h, "PUT", path, `{"value":1}`, map[string]string{"X-Device-ID": "flooding-device"}); rec.Code != 429 {
		t.Errorf("vote flood: %d, want 429", rec.Code)
	}
}

func TestCommentRoutes(t *testing.T) {
	i := &stubInteractions{}
	c := &stubCommunity{poster: oldPoster, words: []community.Keyword{{Word: "加微信", Category: "ad"}}}
	h := newInteractionServer(i, c, false)
	auth := map[string]string{"Authorization": "Bearer good"}

	rec := doH(h, "GET", "/api/releases/abc-123/comments", "", nil)
	if rec.Code != 200 || i.filters[0].Sort != community.CommentSortTop || i.filters[0].ViewerID != 0 {
		t.Fatalf("list: %d %+v", rec.Code, i.filters)
	}
	var page struct {
		Items []map[string]any `json:"items"`
	}
	json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0]["author"].(map[string]any)["display_name"] != "Ann" {
		t.Errorf("list body %s", rec.Body)
	}
	for path, want := range map[string]int{
		"/api/releases/abc-123/comments?sort=newest":   200,
		"/api/releases/abc-123/comments?sort=helpful":  400,
		"/api/releases/abc-123/comments?cursor=zz":     400,
		"/api/releases/nope/comments":                  404,
		"/api/comments/5/replies":                      200,
		"/api/comments/404/replies":                    404,
		"/api/comments/x/replies":                      400,
		"/api/comments/5/replies?cursor=not-a-cursor!": 400,
	} {
		if rec := doH(h, "GET", path, "", nil); rec.Code != want {
			t.Errorf("GET %s: %d, want %d", path, rec.Code, want)
		}
	}

	post := "/api/releases/abc-123/comments"
	if rec := doH(h, "POST", post, `{"body":"hi"}`, nil); rec.Code != 401 {
		t.Errorf("anonymous post: %d", rec.Code)
	}
	// Ordered: i.comments is checked by index below.
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"body":""}`, 400},
		{`{"body":"` + strings.Repeat("评", community.MaxCommentRunes+1) + `"}`, 400},
		{`{"body":"hi","parent_id":-1}`, 400},
		{`{"body":"hi","parent_id":404}`, 404},
		{`{"body":"hi","extra":1}`, 400},
		{`{"body":"第一行\n第二行","spoiler":true}`, 201},
		{`{"body":"reply","parent_id":5}`, 201},
	} {
		if rec := doH(h, "POST", post, c.body, auth); rec.Code != c.want {
			t.Errorf("POST %s: %d, want %d (%s)", c.body, rec.Code, c.want, rec.Body)
		}
	}
	rec = doH(h, "POST", post, `{"body":"快来 加 微 信"}`, auth)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("keyword hit still looks posted, as pending: %d %s", rec.Code, rec.Body)
	}
	last := i.comments[len(i.comments)-1]
	if last.Status != community.StatusShadow || i.comments[0].Status != community.StatusVisible || !i.comments[0].Spoiler {
		t.Errorf("statuses %+v", i.comments)
	}
	if i.comments[1].ParentID != 5 || i.comments[1].ReleaseID != 10 || i.comments[1].UserID != 1 {
		t.Errorf("reply %+v", i.comments[1])
	}

	for path, want := range map[string]int{"/api/comments/5": 200, "/api/comments/403": 403, "/api/comments/404": 404} {
		if rec := doH(h, "DELETE", path, "", auth); rec.Code != want {
			t.Errorf("DELETE %s: %d, want %d", path, rec.Code, want)
		}
	}
	if rec := doH(h, "PUT", "/api/comments/5/like", "", auth); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"likes":1`) {
		t.Errorf("like: %d %s", rec.Code, rec.Body)
	}
	if rec := doH(h, "DELETE", "/api/comments/5/like", "", auth); rec.Code != 200 {
		t.Errorf("unlike: %d", rec.Code)
	}
	if rec := doH(h, "POST", "/api/comments/5/report", `{"reason":"spam"}`, auth); rec.Code != 201 {
		t.Errorf("report: %d %s", rec.Code, rec.Body)
	}
	if c.calls[len(c.calls)-1] != "CreateReport:comment" {
		t.Errorf("report target: %v", c.calls)
	}
}

func TestCommentPostLimitsAndMutes(t *testing.T) {
	auth := map[string]string{"Authorization": "Bearer good"}
	post := "/api/releases/abc-123/comments"
	i := &stubInteractions{}
	h := newInteractionServer(i, &stubCommunity{poster: oldPoster}, false)
	for n := 0; n < community.CommentPerMinute; n++ {
		if rec := doH(h, "POST", post, `{"body":"hi"}`, auth); rec.Code != 201 {
			t.Fatalf("post %d: %d", n, rec.Code)
		}
	}
	if rec := doH(h, "POST", post, `{"body":"hi"}`, auth); rec.Code != 429 {
		t.Errorf("post over the limit: %d, want 429", rec.Code)
	}
	until := oldPoster
	mutedUntil := time.Now().Add(time.Hour)
	until.MutedUntil = &mutedUntil
	h = newInteractionServer(&stubInteractions{}, &stubCommunity{poster: until}, false)
	if rec := doH(h, "POST", post, `{"body":"hi"}`, auth); rec.Code != 403 {
		t.Errorf("muted: %d, want 403", rec.Code)
	}
}

func TestCommentsDisabled(t *testing.T) {
	h := newInteractionServer(&stubInteractions{}, &stubCommunity{poster: oldPoster}, true)
	auth := map[string]string{"Authorization": "Bearer good"}
	for _, rt := range []struct{ method, path, body string }{
		{"GET", "/api/releases/abc-123/comments", ""},
		{"POST", "/api/releases/abc-123/comments", `{"body":"hi"}`},
		{"GET", "/api/comments/5/replies", ""},
		{"DELETE", "/api/comments/5", ""},
		{"PUT", "/api/comments/5/like", ""},
		{"POST", "/api/comments/5/report", `{"reason":"spam"}`},
	} {
		if rec := doH(h, rt.method, rt.path, rt.body, auth); rec.Code != 404 {
			t.Errorf("%s %s with comments off: %d, want 404", rt.method, rt.path, rec.Code)
		}
	}
	// Votes are not part of the switch.
	if rec := doH(h, "GET", "/api/releases/abc-123/reaction", "", nil); rec.Code != 200 {
		t.Errorf("reaction with comments off: %d", rec.Code)
	}
	for v, want := range map[string]bool{"": true, "true": true, "1": true, "false": false, "0": false, " OFF ": false, "no": false} {
		if got := commentsEnabled(v); got != want {
			t.Errorf("commentsEnabled(%q) = %v", v, got)
		}
	}
}

func TestCommentModerationRoute(t *testing.T) {
	m := &stubModeration{}
	srv := &Server{accounts: &stubAccounts{}, moderation: m, adminToken: "admintok", adminAuth: newFakeAdmins()}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	admin := adminAs("mod", "moderator")
	rec := doH(mux, "POST", "/api/admin/moderation/comments/7/action", `{"action":"delete","reason":"spam"}`, admin)
	if rec.Code != 200 || len(m.actions) != 1 || m.actions[0].TargetType != "comment" || m.actions[0].ID != 7 || m.actions[0].Actor != "mod" {
		t.Fatalf("comment action: %d %+v", rec.Code, m.actions)
	}
	if rec := doH(mux, "POST", "/api/admin/moderation/comments/7/action", `{"action":"hide"}`, admin); rec.Code != 400 {
		t.Errorf("unknown action: %d", rec.Code)
	}
	if rec := doH(mux, "GET", "/api/admin/moderation/queue?kind=comments", "", admin); rec.Code != 200 {
		t.Errorf("comments queue: %d %s", rec.Code, rec.Body)
	}
}

func TestEditCommentRoute(t *testing.T) {
	i := &stubInteractions{}
	c := &stubCommunity{poster: oldPoster, words: []community.Keyword{{Word: "加微信", Category: "ad"}}}
	h := newInteractionServer(i, c, false)
	auth := map[string]string{"Authorization": "Bearer good"}
	if rec := doH(h, "PATCH", "/api/comments/5", `{"body":"fixed"}`, nil); rec.Code != 401 {
		t.Errorf("anonymous edit: %d", rec.Code)
	}
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/comments/5", `{}`, 400},
		{"/api/comments/5", `{"body":""}`, 400},
		{"/api/comments/5", `{"body":"x","extra":1}`, 400},
		{"/api/comments/x", `{"body":"x"}`, 400},
		{"/api/comments/403", `{"body":"x"}`, 403},
		{"/api/comments/404", `{"body":"x"}`, 404},
		{"/api/comments/409", `{"body":"x"}`, 409},
		{"/api/comments/5", `{"body":"fixed typo","spoiler":true}`, 200},
		{"/api/comments/5", `{"body":"快来 加 微 信"}`, 200},
	} {
		if rec := doH(h, "PATCH", tc.path, tc.body, auth); rec.Code != tc.want {
			t.Errorf("PATCH %s %s: %d, want %d (%s)", tc.path, tc.body, rec.Code, tc.want, rec.Body)
		}
	}
	if len(i.edits) != 2 || i.edits[0].KeywordHit || i.edits[0].Spoiler == nil || !*i.edits[0].Spoiler || !i.edits[1].KeywordHit {
		t.Errorf("edits %+v (the blocked-word filter must run on edit)", i.edits)
	}
	// Comments switched off: 404 like every public comment route.
	off := newInteractionServer(&stubInteractions{}, c, true)
	if rec := doH(off, "PATCH", "/api/comments/5", `{"body":"x"}`, auth); rec.Code != 404 {
		t.Errorf("comments off: %d", rec.Code)
	}
}
