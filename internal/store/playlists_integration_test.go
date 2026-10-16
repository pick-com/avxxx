package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestPlaylistsIntegration covers migration 036 (favorites -> default
// playlist backfill, idempotent re-runs), owner scoping, share links and the
// watch-later auto-remove rule against an isolated database.
func TestPlaylistsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "playlists")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		must(s.QueryRow(ctx, sql, args...).Scan(&n))
		return n
	}
	var alice, bob int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash,display_name) VALUES('a@t','x','Alice') RETURNING id`).Scan(&alice))
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('b@t','x') RETURNING id`).Scan(&bob))
	release := func(slug string, runtime int) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,runtime_minutes) VALUES($1,$1,'published',NULLIF($2,0)) RETURNING id`, slug, runtime).Scan(&id))
		return id
	}
	r1, r2, r3 := release("r1", 100), release("r2", 0), release("r3", 10)

	// --- migration backfill: pretend 036 has not run yet, with legacy favorites.
	exec(`DROP TABLE playlist_items`)
	exec(`DROP TABLE playlists`)
	exec(`INSERT INTO favorites(user_id,release_id,created_at) VALUES($1,$2,'2026-01-02'),($1,$3,'2026-01-03'),($4,$2,'2026-02-01')`, alice, r1, r2, bob)
	s.Close()
	for i := 0; i < 2; i++ { // the second Open must not duplicate anything
		if s, err = Open(ctx); err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if i == 0 {
			s.Close()
		}
	}
	defer s.Close()
	if n := count(`SELECT count(*) FROM playlists WHERE is_default`); n != 2 {
		t.Fatalf("default playlists after backfill = %d, want 2", n)
	}
	favs, err := s.Favorites(ctx, alice)
	must(err)
	if len(favs) != 2 || favs[0]["slug"] != "r2" || favs[1]["slug"] != "r1" {
		t.Fatalf("alice favorites after backfill = %v", favs)
	}
	if n := count(`SELECT count(*) FROM favorites`); n != 3 {
		t.Errorf("legacy favorites rows = %d, want 3 (left untouched)", n)
	}
	// Removing an item must stick across restarts (backfill runs once only).
	must(s.SetFavorite(ctx, alice, "r1", false))
	s.Close()
	if s, err = Open(ctx); err != nil {
		t.Fatal(err)
	}
	if favs, _ = s.Favorites(ctx, alice); len(favs) != 1 {
		t.Errorf("favorites after re-open = %v, want only r2", favs)
	}

	// --- playlists and owner scoping.
	lists, err := s.Playlists(ctx, alice, "r2")
	must(err)
	if len(lists) != 1 || !lists[0].IsDefault || lists[0].Contains == nil || !*lists[0].Contains || lists[0].ShareToken == "" {
		t.Fatalf("alice playlists = %+v", lists)
	}
	def := lists[0]
	name, vis := "夜里看", VisibilityLink
	p, err := s.CreatePlaylist(ctx, alice, PlaylistInput{Name: &name, Visibility: &vis})
	must(err)
	must(s.AddPlaylistItem(ctx, alice, p.ID, "r1"))
	must(s.AddPlaylistItem(ctx, alice, p.ID, "r1")) // idempotent
	if _, items, err := s.PlaylistWithItems(ctx, alice, p.ID); err != nil || len(items) != 1 {
		t.Fatalf("items = %v %v", items, err)
	}
	if err := s.AddPlaylistItem(ctx, alice, p.ID, "nope"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("add unknown release: %v", err)
	}
	// Bob cannot see or touch Alice's playlist by id.
	for name, err := range map[string]error{
		"get":    func() error { _, _, e := s.PlaylistWithItems(ctx, bob, p.ID); return e }(),
		"update": func() error { _, e := s.UpdatePlaylist(ctx, bob, p.ID, PlaylistInput{Name: &name}); return e }(),
		"rotate": func() error { _, e := s.RotatePlaylistToken(ctx, bob, p.ID); return e }(),
		"add":    s.AddPlaylistItem(ctx, bob, p.ID, "r3"),
		"remove": s.RemovePlaylistItem(ctx, bob, p.ID, "r1"),
		"move":   s.MoveWatchLaterToPlaylist(ctx, bob, p.ID, "r3"),
		"delete": s.DeletePlaylist(ctx, bob, p.ID),
	} {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("bob %s alice's playlist: err = %v, want ErrNoRows", name, err)
		}
	}
	if n := count(`SELECT count(*) FROM playlist_items WHERE playlist_id=$1`, p.ID); n != 1 {
		t.Errorf("alice's playlist changed by bob: %d items", n)
	}
	if err := s.DeletePlaylist(ctx, alice, def.ID); !errors.Is(err, ErrDefaultPlaylist) {
		t.Errorf("delete default: %v", err)
	}

	// --- share links: link-only readable by token, private not, rotate revokes.
	pub, items, err := s.PublicPlaylist(ctx, p.ShareToken)
	if err != nil || len(items) != 1 || pub.ShareToken != "" || pub.OwnerName != "Alice" {
		t.Fatalf("public link playlist = %+v %v %v", pub, items, err)
	}
	if _, _, err := s.PublicPlaylist(ctx, def.ShareToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("private playlist by token: %v, want ErrNoRows", err)
	}
	priv := VisibilityPrivate
	_, err = s.UpdatePlaylist(ctx, alice, p.ID, PlaylistInput{Visibility: &priv})
	must(err)
	if _, _, err := s.PublicPlaylist(ctx, p.ShareToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("playlist made private still public: %v", err)
	}
	pubVis := VisibilityPublic
	_, err = s.UpdatePlaylist(ctx, alice, p.ID, PlaylistInput{Visibility: &pubVis})
	must(err)
	rotated, err := s.RotatePlaylistToken(ctx, alice, p.ID)
	must(err)
	if _, _, err := s.PublicPlaylist(ctx, p.ShareToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("old token still works after rotate: %v", err)
	}
	if _, _, err := s.PublicPlaylist(ctx, rotated.ShareToken); err != nil {
		t.Errorf("new token: %v", err)
	}

	// --- watch-later: move to playlist, auto-remove at 90 %.
	for _, slug := range []string{"r1", "r2", "r3"} {
		must(s.AddWatchLater(ctx, alice, slug))
	}
	must(s.MoveWatchLaterToPlaylist(ctx, alice, p.ID, "r3"))
	if n := count(`SELECT count(*) FROM watch_later WHERE user_id=$1 AND release_id=$2`, alice, r3); n != 0 {
		t.Error("moved item still in watch-later")
	}
	if n := count(`SELECT count(*) FROM playlist_items WHERE playlist_id=$1 AND release_id=$2`, p.ID, r3); n != 1 {
		t.Error("moved item not in playlist")
	}
	removed, err := s.LaterAutoRemove(ctx, alice, "r1", 80, 100) // 80 %
	must(err)
	if removed {
		t.Error("removed at 80 %")
	}
	if removed, err = s.LaterAutoRemove(ctx, alice, "r1", 5400, 0); err != nil || !removed { // 90 % of the 100-minute runtime
		t.Errorf("runtime fallback: removed=%v err=%v", removed, err)
	}
	if removed, _ = s.LaterAutoRemove(ctx, alice, "r2", 99999, 0); removed {
		t.Error("removed with no known duration")
	}
	_, err = s.PatchSettings(ctx, alice, []byte(`{"later_auto_remove":false}`))
	must(err)
	if removed, _ = s.LaterAutoRemove(ctx, alice, "r2", 100, 100); removed {
		t.Error("removed with later_auto_remove off")
	}
	if n := count(`SELECT count(*) FROM watch_later WHERE user_id=$1`, alice); n != 1 {
		t.Errorf("watch-later left = %d, want 1 (r2)", n)
	}

	// --- deleting a playlist removes its items only.
	must(s.DeletePlaylist(ctx, alice, p.ID))
	if n := count(`SELECT count(*) FROM playlist_items WHERE playlist_id=$1`, p.ID); n != 0 {
		t.Errorf("items of deleted playlist = %d", n)
	}
	if favs, _ = s.Favorites(ctx, alice); len(favs) != 1 {
		t.Errorf("default playlist affected by delete: %v", favs)
	}
}
