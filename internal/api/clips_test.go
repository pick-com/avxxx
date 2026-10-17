package api

import (
	"avxxx/internal/community"
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubClips is an in-memory clipStore: "abcdefghij" is a visible clip owned
// by the stub user, everything else is missing. Releases: "abc-1" (10 min).
type stubClips struct {
	created []store.ClipInput
	calls   []string
	cursor  *store.Cursor
	limit   int
}

const stubClipID = "abcdefghij"

func (s *stubClips) CreateClip(_ context.Context, _ int64, in store.ClipInput, titleStatus string) (store.Clip, error) {
	s.calls = append(s.calls, "Create")
	if in.Release != "abc-1" {
		return store.Clip{}, pgx.ErrNoRows
	}
	if err := store.ValidateClipRange(in.StartMs, in.EndMs, 600_000); err != nil {
		return store.Clip{}, err
	}
	s.created = append(s.created, in)
	return store.Clip{ID: stubClipID, ReleaseSlug: in.Release, StartMs: in.StartMs, EndMs: in.EndMs, Title: in.Title, Status: "visible", TitleStatus: titleStatus, Mine: true}, nil
}

func (s *stubClips) ClipByPublicID(_ context.Context, id string, viewer int64) (store.Clip, error) {
	s.calls = append(s.calls, "Get")
	if id != stubClipID {
		return store.Clip{}, pgx.ErrNoRows
	}
	return store.Clip{ID: id, ReleaseSlug: "abc-1", StartMs: 1000, EndMs: 16000, Status: "visible", Mine: viewer != 0, RowID: 9}, nil
}

func (s *stubClips) DeleteClip(_ context.Context, _ int64, id string) error {
	s.calls = append(s.calls, "Delete")
	if id != stubClipID {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *stubClips) MyClips(_ context.Context, uid int64, c *store.Cursor, limit int) ([]store.MyClip, string, error) {
	s.calls = append(s.calls, "Mine")
	s.cursor, s.limit = c, limit
	return []store.MyClip{{Clip: store.Clip{ID: stubClipID, ReleaseSlug: "abc-1", ReleaseTitle: "ABC", StartMs: 1000, EndMs: 16000, Status: "visible", Mine: true, RowID: 9}, ReleaseID: 77}},
		store.EncodeCursor(store.Cursor{Sort: store.ClipSort, Key: "2026-10-16T00:00:00Z", ID: 9}), nil
}

func newClipServer(c *stubClips) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, clips: c}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestClipRoutesRequireUser(t *testing.T) {
	c := &stubClips{}
	h := newClipServer(c)
	for _, rt := range []struct{ method, path string }{{"GET", "/api/me/clips"}, {"POST", "/api/me/clips"}, {"DELETE", "/api/me/clips/" + stubClipID}, {"POST", "/api/clips/" + stubClipID + "/report"}} {
		for _, tok := range []string{"", "bad"} {
			if rec := do(h, rt.method, rt.path, tok, `{"release":"abc-1","start_ms":0,"end_ms":15000}`); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s token=%q: status %d, want 401", rt.method, rt.path, tok, rec.Code)
			}
		}
	}
	if len(c.calls) != 0 {
		t.Errorf("store called without auth: %v", c.calls)
	}
}

func TestClipHandlerStatus(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
		token, body  string
		want         int
	}{
		{"create", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":60000,"end_ms":75000,"title":"  名场面  "}`, 201},
		{"create no title", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":0,"end_ms":5000}`, 201},
		{"too short", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":0,"end_ms":4999}`, 400},
		{"too long", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":0,"end_ms":60001}`, 400},
		{"negative start", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":-1,"end_ms":9000}`, 400},
		{"past the end", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":590000,"end_ms":605000}`, 400},
		{"long title", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":0,"end_ms":9000,"title":"` + strings.Repeat("长", 101) + `"}`, 400},
		{"no release", "POST", "/api/me/clips", "good", `{"start_ms":0,"end_ms":9000}`, 400},
		{"unknown release", "POST", "/api/me/clips", "good", `{"release":"nope","start_ms":0,"end_ms":9000}`, 404},
		{"unknown field", "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":0,"end_ms":9000,"user_id":2}`, 400},
		{"get public", "GET", "/api/clips/" + stubClipID, "", "", 200},
		{"get missing", "GET", "/api/clips/zzzzzzzzzz", "", "", 404},
		{"delete own", "DELETE", "/api/me/clips/" + stubClipID, "good", "", 200},
		{"delete other or missing", "DELETE", "/api/me/clips/zzzzzzzzzz", "good", "", 404},
		{"report missing", "POST", "/api/clips/zzzzzzzzzz/report", "good", `{"reason":"spam"}`, 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newClipServer(&stubClips{}), tc.method, tc.path, tc.token, tc.body)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestCreateClipTrimsTitle(t *testing.T) {
	c := &stubClips{}
	rec := do(newClipServer(c), "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":1000,"end_ms":16000,"title":"  名场面  "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Clip         store.Clip `json:"clip"`
		TitlePending bool       `json:"title_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Clip.Title != "名场面" || out.Clip.ID != stubClipID || out.Clip.Status != "visible" || out.Clip.TitleStatus != "visible" || out.TitlePending {
		t.Errorf("clip = %+v pending=%v", out.Clip, out.TitlePending)
	}
	if strings.Contains(rec.Body.String(), `"RowID"`) || strings.Contains(rec.Body.String(), `"row_id"`) {
		t.Error("internal id leaked")
	}
}

// A keyword hit keeps the title but shadows it (author only, 待审片段);
// a clean title, or none, is visible.
func TestCreateClipShadowsKeywordTitle(t *testing.T) {
	for _, tc := range []struct {
		title, want string
		pending     bool
	}{
		{"加微信看全集", "shadow", true},
		{"名场面", "visible", false},
		{"", "visible", false},
	} {
		c := &stubClips{}
		srv := &Server{accounts: &stubAccounts{}, clips: c, community: &stubCommunity{words: []community.Keyword{{Word: "加微信", Category: "ad"}}}}
		mux := http.NewServeMux()
		for _, rt := range srv.routes() {
			mux.HandleFunc(rt.pattern, rt.handler)
		}
		rec := do(mux, "POST", "/api/me/clips", "good", `{"release":"abc-1","start_ms":1000,"end_ms":16000,"title":"`+tc.title+`"}`)
		var out struct {
			Clip         store.Clip `json:"clip"`
			TitlePending bool       `json:"title_pending"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusCreated {
			t.Fatalf("%q: %d %s", tc.title, rec.Code, rec.Body)
		}
		if out.Clip.Title != tc.title || out.Clip.TitleStatus != tc.want || out.TitlePending != tc.pending {
			t.Errorf("%q: clip %+v pending=%v, want %s", tc.title, out.Clip, out.TitlePending, tc.want)
		}
	}
}

func TestMyClipsHandler(t *testing.T) {
	c := &stubClips{}
	h := newClipServer(c)
	rec := do(h, "GET", "/api/me/clips?limit=5", "good", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0]["id"] != stubClipID || out.Items[0]["release_title"] != "ABC" || out.NextCursor == "" || c.limit != 5 || c.cursor != nil {
		t.Errorf("page = %s (limit %d, cursor %v)", rec.Body.String(), c.limit, c.cursor)
	}
	if _, ok := out.Items[0]["release_cover"]; !ok {
		t.Error("release_cover missing")
	}
	for _, leak := range []string{`"RowID"`, `"ReleaseID"`, `"release_id"`, "77"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Errorf("internal id leaked: %s", leak)
		}
	}
	// The next page passes the decoded cursor through.
	if rec := do(h, "GET", "/api/me/clips?cursor="+out.NextCursor, "good", ""); rec.Code != http.StatusOK || c.cursor == nil || c.cursor.ID != 9 {
		t.Errorf("next page: %d cursor %+v", rec.Code, c.cursor)
	}
	for _, q := range []string{"?cursor=not*a*cursor", "?cursor=" + store.EncodeCursor(store.Cursor{Sort: "newest", Key: "x", ID: 1}), "?limit=-1"} {
		if rec := do(h, "GET", "/api/me/clips"+q, "good", ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/me/clips%s = %d, want 400", q, rec.Code)
		}
	}
}
