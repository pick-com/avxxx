package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestPlaylistOrderIntegration covers migration 048's position backfill,
// new items first, manual reorder (owner only) and the public-only playlist
// sitemap list.
func TestPlaylistOrderIntegration(t *testing.T) {
	openIsolatedTestDB(t, "playlist_order")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var alice, bob int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('a@t','x') RETURNING id`).Scan(&alice))
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('b@t','x') RETURNING id`).Scan(&bob))
	for _, slug := range []string{"r1", "r2", "r3", "r4"} {
		_, err := s.Exec(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,$1,'published')`, slug)
		must(err)
	}
	slugs := func(id int64) []string {
		t.Helper()
		_, items, err := s.PlaylistWithItems(ctx, alice, id)
		must(err)
		out := []string{}
		for _, it := range items {
			out = append(out, it.Slug)
		}
		return out
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s = %v, want %v", what, got, want)
			}
		}
	}

	name, vis := "x", VisibilityPublic
	p, err := s.CreatePlaylist(ctx, alice, PlaylistInput{Name: &name, Visibility: &vis})
	must(err)
	// Rows written without a position (pre-048 rows or an older binary).
	_, err = s.Exec(ctx, `INSERT INTO playlist_items(playlist_id,release_id,created_at)
		SELECT $1,id,CASE slug WHEN 'r1' THEN timestamptz '2026-01-01' ELSE timestamptz '2026-01-02' END
		FROM releases WHERE slug IN ('r1','r2')`, p.ID)
	must(err)
	s.Close()
	if s, err = Open(ctx); err != nil { // 048 backfills newest-first
		t.Fatal(err)
	}
	eq("after backfill", slugs(p.ID), "r2", "r1")
	must(s.AddPlaylistItem(ctx, alice, p.ID, "r3"))
	eq("new item first", slugs(p.ID), "r3", "r2", "r1")

	must(s.ReorderPlaylist(ctx, alice, p.ID, []string{"r1", "r3"}))
	eq("after reorder", slugs(p.ID), "r1", "r3", "r2")
	if err := s.ReorderPlaylist(ctx, alice, p.ID, []string{"r4"}); !errors.Is(err, ErrInvalidOrder) {
		t.Errorf("reorder with a slug not in the playlist: %v", err)
	}
	if err := s.ReorderPlaylist(ctx, bob, p.ID, []string{"r2"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("bob reorders alice's playlist: %v", err)
	}
	eq("unchanged by bob", slugs(p.ID), "r1", "r3", "r2")
	// An old binary's insert (no position) shows first and is numbered on restart.
	_, err = s.Exec(ctx, `INSERT INTO playlist_items(playlist_id,release_id) SELECT $1,id FROM releases WHERE slug='r4'`, p.ID)
	must(err)
	eq("unpositioned row", slugs(p.ID), "r4", "r1", "r3", "r2")
	s.Close()
	if s, err = Open(ctx); err != nil {
		t.Fatal(err)
	}
	eq("after second backfill", slugs(p.ID), "r4", "r1", "r3", "r2")
	var nulls int
	must(s.QueryRow(ctx, `SELECT count(*) FROM playlist_items WHERE position IS NULL`).Scan(&nulls))
	if nulls != 0 {
		t.Errorf("%d rows still without a position", nulls)
	}

	// Sitemap: public playlists with a published release only.
	lname, link := "l", VisibilityLink
	lp, err := s.CreatePlaylist(ctx, alice, PlaylistInput{Name: &lname, Visibility: &link})
	must(err)
	must(s.AddPlaylistItem(ctx, alice, lp.ID, "r1"))
	ename := "empty"
	_, err = s.CreatePlaylist(ctx, alice, PlaylistInput{Name: &ename, Visibility: &vis})
	must(err)
	entries, next, err := s.SitemapPlaylists(ctx, "", 10)
	must(err)
	if len(entries) != 1 || entries[0].Slug != p.ShareToken || next != "" {
		t.Errorf("sitemap playlists = %+v next=%q, want only %s", entries, next, p.ShareToken)
	}
	c, err := s.SitemapCounts(ctx)
	must(err)
	if c.Playlists != 1 {
		t.Errorf("SitemapCounts.Playlists = %d, want 1", c.Playlists)
	}
}
