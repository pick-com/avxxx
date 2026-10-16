package api

import (
	"avxxx/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// stubLibrary is an in-memory libraryStore. Playlist ids other than 1 (or 0,
// the default) do not belong to the caller and answer pgx.ErrNoRows, like the
// store's user_id-scoped SQL. Public tokens: "pub" (public), "lnk" (link).
type stubLibrary struct {
	err          error
	laterRemoved bool
	calls        []string
}

func (s *stubLibrary) rec(name string) { s.calls = append(s.calls, name) }

func (s *stubLibrary) owned(id int64) error {
	if id != 0 && id != 1 {
		return pgx.ErrNoRows
	}
	return s.err
}

func (s *stubLibrary) Playlists(context.Context, int64, string) ([]store.Playlist, error) {
	s.rec("Playlists")
	return []store.Playlist{{ID: 1, Name: "x", Visibility: "private", IsDefault: true}}, s.err
}
func (s *stubLibrary) CreatePlaylist(_ context.Context, _ int64, in store.PlaylistInput) (store.Playlist, error) {
	s.rec("Create")
	if err := in.Normalize(true); err != nil {
		return store.Playlist{}, err
	}
	return store.Playlist{ID: 2, Name: *in.Name}, s.err
}
func (s *stubLibrary) UpdatePlaylist(_ context.Context, _ int64, id int64, in store.PlaylistInput) (store.Playlist, error) {
	s.rec("Update")
	if err := in.Normalize(false); err != nil {
		return store.Playlist{}, err
	}
	return store.Playlist{ID: id}, s.owned(id)
}
func (s *stubLibrary) RotatePlaylistToken(_ context.Context, _ int64, id int64) (store.Playlist, error) {
	s.rec("Rotate")
	return store.Playlist{ID: id, ShareToken: "new"}, s.owned(id)
}
func (s *stubLibrary) DeletePlaylist(_ context.Context, _ int64, id int64) error {
	s.rec("Delete")
	return s.owned(id)
}
func (s *stubLibrary) PlaylistWithItems(_ context.Context, _ int64, id int64) (store.Playlist, []store.PlaylistItem, error) {
	s.rec("Get")
	return store.Playlist{ID: id}, []store.PlaylistItem{}, s.owned(id)
}
func (s *stubLibrary) PublicPlaylist(_ context.Context, token string) (store.Playlist, []store.PlaylistItem, error) {
	switch token {
	case "pub":
		return store.Playlist{ID: 1, Visibility: store.VisibilityPublic}, []store.PlaylistItem{}, nil
	case "lnk":
		return store.Playlist{ID: 1, Visibility: store.VisibilityLink}, []store.PlaylistItem{}, nil
	}
	return store.Playlist{}, nil, pgx.ErrNoRows
}
func (s *stubLibrary) AddPlaylistItem(_ context.Context, _ int64, id int64, _ string) error {
	s.rec("Add")
	return s.owned(id)
}
func (s *stubLibrary) RemovePlaylistItem(_ context.Context, _ int64, id int64, _ string) error {
	s.rec("Remove")
	return s.owned(id)
}
func (s *stubLibrary) ReorderPlaylist(_ context.Context, _ int64, id int64, ordered []string) error {
	s.rec("Reorder")
	if err := s.owned(id); err != nil {
		return err
	}
	_, err := store.ReorderPositions([]string{"abc-1", "abc-2"}, ordered)
	return err
}
func (s *stubLibrary) MoveWatchLaterToPlaylist(_ context.Context, _ int64, id int64, _ string) error {
	s.rec("Move")
	return s.owned(id)
}
func (s *stubLibrary) LaterAutoRemove(_ context.Context, _ int64, _ string, progress, duration int) (bool, error) {
	return s.laterRemoved && store.LaterFinished(progress, duration), nil
}

func newLibraryServer(lib *stubLibrary, soc *stubSocial) http.Handler {
	srv := &Server{accounts: &stubAccounts{}, social: soc, playlists: lib}
	mux := http.NewServeMux()
	for _, rt := range srv.routes() {
		mux.HandleFunc(rt.pattern, rt.handler)
	}
	return mux
}

func TestPlaylistRoutesRequireUser(t *testing.T) {
	lib := &stubLibrary{}
	h := newLibraryServer(lib, &stubSocial{})
	nilStore := New(nil, nil)
	srv := &Server{}
	for _, rt := range srv.playlistRoutes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if !strings.HasPrefix(path, "/api/me/") {
			continue
		}
		path = samplePath(path)
		for _, tok := range []string{"", "bad"} {
			if rec := do(h, method, path, tok, "{}"); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s token=%q: status %d, want 401", rt.pattern, tok, rec.Code)
			}
		}
		if rec := do(nilStore, method, path, "good", "{}"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without store: status %d, want 401", rt.pattern, rec.Code)
		}
	}
	if len(lib.calls) != 0 {
		t.Errorf("store called without auth: %v", lib.calls)
	}
}

func TestPlaylistHandlerStatus(t *testing.T) {
	tests := []struct {
		name         string
		method, path string
		body         string
		want         int
	}{
		{"list", "GET", "/api/me/playlists", "", 200},
		{"list for release", "GET", "/api/me/playlists?release=abc-1", "", 200},
		{"create", "POST", "/api/me/playlists", `{"name":"夜里看","visibility":"link"}`, 200},
		{"create no name", "POST", "/api/me/playlists", `{"visibility":"link"}`, 400},
		{"create blank name", "POST", "/api/me/playlists", `{"name":"   "}`, 400},
		{"create long name", "POST", "/api/me/playlists", `{"name":"` + strings.Repeat("长", 61) + `"}`, 400},
		{"create bad visibility", "POST", "/api/me/playlists", `{"name":"a","visibility":"friends"}`, 400},
		{"create unknown field", "POST", "/api/me/playlists", `{"name":"a","owner":2}`, 400},
		{"create no body", "POST", "/api/me/playlists", "", 400},
		{"get own", "GET", "/api/me/playlists/1", "", 200},
		{"get other user's", "GET", "/api/me/playlists/7", "", 404},
		{"get bad id", "GET", "/api/me/playlists/x", "", 400},
		{"update own", "PATCH", "/api/me/playlists/1", `{"visibility":"public"}`, 200},
		{"update other user's", "PATCH", "/api/me/playlists/7", `{"name":"mine now"}`, 404},
		{"update bad visibility", "PATCH", "/api/me/playlists/1", `{"visibility":"x"}`, 400},
		{"delete own", "DELETE", "/api/me/playlists/1", "", 200},
		{"delete other user's", "DELETE", "/api/me/playlists/7", "", 404},
		{"rotate own", "POST", "/api/me/playlists/1/share-token", "", 200},
		{"rotate other user's", "POST", "/api/me/playlists/7/share-token", "", 404},
		{"add item", "PUT", "/api/me/playlists/1/items/abc-1", "", 200},
		{"add to other user's", "PUT", "/api/me/playlists/7/items/abc-1", "", 404},
		{"remove item", "DELETE", "/api/me/playlists/1/items/abc-1", "", 200},
		{"remove from other user's", "DELETE", "/api/me/playlists/7/items/abc-1", "", 404},
		{"reorder", "POST", "/api/me/playlists/1/reorder", `{"slugs":["abc-2","abc-1"]}`, 200},
		{"reorder other user's", "POST", "/api/me/playlists/7/reorder", `{"slugs":["abc-1"]}`, 404},
		{"reorder unknown slug", "POST", "/api/me/playlists/1/reorder", `{"slugs":["zzz"]}`, 400},
		{"reorder duplicate", "POST", "/api/me/playlists/1/reorder", `{"slugs":["abc-1","abc-1"]}`, 400},
		{"reorder empty", "POST", "/api/me/playlists/1/reorder", `{"slugs":[]}`, 400},
		{"move later", "POST", "/api/me/watch-later/abc-1/move", `{"playlist_id":1}`, 200},
		{"move later other user's", "POST", "/api/me/watch-later/abc-1/move", `{"playlist_id":7}`, 404},
		{"move later no playlist", "POST", "/api/me/watch-later/abc-1/move", `{}`, 400},
		{"public", "GET", "/api/playlists/pub", "", 200},
		{"link", "GET", "/api/playlists/lnk", "", 200},
		{"private or unknown", "GET", "/api/playlists/priv", "", 404},
	}
	for _, tc := range tests {
		h := newLibraryServer(&stubLibrary{}, &stubSocial{})
		rec := do(h, tc.method, tc.path, "good", tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: %s %s -> %d (%s), want %d", tc.name, tc.method, tc.path, rec.Code, strings.TrimSpace(rec.Body.String()), tc.want)
		}
	}
}

func TestDeleteDefaultPlaylistConflict(t *testing.T) {
	h := newLibraryServer(&stubLibrary{err: store.ErrDefaultPlaylist}, &stubSocial{})
	if rec := do(h, "DELETE", "/api/me/playlists/1", "good", ""); rec.Code != http.StatusConflict {
		t.Errorf("status %d, want 409", rec.Code)
	}
}

func TestPublicPlaylistRobots(t *testing.T) {
	h := newLibraryServer(&stubLibrary{}, &stubSocial{})
	for token, want := range map[string]string{"pub": "", "lnk": "noindex"} {
		rec := do(h, "GET", "/api/playlists/"+token, "", "")
		if got := rec.Header().Get("X-Robots-Tag"); got != want {
			t.Errorf("%s: X-Robots-Tag %q, want %q", token, got, want)
		}
		if strings.Contains(rec.Body.String(), "share_token") {
			t.Errorf("%s: public body leaks share_token: %s", token, rec.Body.String())
		}
	}
}

func TestSaveWatchReportsLaterRemoved(t *testing.T) {
	for _, paused := range []bool{false, true} {
		h := newLibraryServer(&stubLibrary{laterRemoved: true}, &stubSocial{paused: paused})
		for body, want := range map[string]bool{
			`{"slug":"abc-1","progress_seconds":95,"duration_seconds":100}`: true,
			`{"slug":"abc-1","progress_seconds":50,"duration_seconds":100}`: false,
		} {
			rec := do(h, "POST", "/api/me/watch-history", "good", body)
			var out map[string]bool
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code != 200 || out["later_removed"] != want {
				t.Errorf("paused=%v %s: %d %v, want later_removed=%v", paused, body, rec.Code, out, want)
			}
		}
	}
}
