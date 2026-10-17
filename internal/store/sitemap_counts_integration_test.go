package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// SitemapCounts groups distinct tags instead of count(DISTINCT) and caches
// the result for sitemapCountsTTL: same counts, at most one reload at a time.
func TestSitemapCountsCachedIntegration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO releases(slug,canonical_title,status) SELECT $1||g,'Sitemap '||g,CASE WHEN g%5=0 THEN 'unpublished' ELSE 'published' END FROM generate_series(1,20) g`, "smc-"+sfx+"-")
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT r.id,'T'||(g%4),CASE WHEN g=3 THEN '' ELSE 'smc-t'||(g%4) END
		FROM releases r, generate_series(1,3) g WHERE r.slug LIKE $1`, "smc-"+sfx+"-%")
	exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url,status) SELECT id,'external_link','x','main','https://x.invalid/'||id,'active' FROM releases WHERE slug LIKE $1`, "smc-"+sfx+"-1%")

	var want SitemapCounts
	if err := s.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM releases r WHERE `+playableReleaseSQL+`),
		(SELECT count(*) FROM releases r WHERE r.status='published' AND r.merged_into_id IS NULL),
		(SELECT count(*) FROM performers p WHERE `+sitemapPerformerSQL+`),
		(SELECT count(DISTINCT t.normalized_tag) FROM release_tags t JOIN releases r ON r.id=t.release_id
			WHERE r.status='published' AND r.merged_into_id IS NULL AND t.normalized_tag<>''),
		(SELECT count(*) FROM playlists p WHERE `+sitemapPlaylistSQL+`)`).
		Scan(&want.Releases, &want.PublishedReleases, &want.Performers, &want.Tags, &want.Playlists); err != nil {
		t.Fatal(err)
	}
	got, err := s.SitemapCounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || got.Tags < 2 || got.PublishedReleases < 16 {
		t.Fatalf("SitemapCounts = %+v, reference %+v", got, want)
	}
	// Cached: a new published release shows up only after the TTL.
	exec(`INSERT INTO releases(slug,canonical_title,status) VALUES($1,'Late','published')`, "smc-"+sfx+"-late")
	if again, err := s.SitemapCounts(ctx); err != nil || again != got {
		t.Fatalf("within the TTL: %+v %v, want cached %+v", again, err, got)
	}
	if now, err := s.sitemapCountsNow(ctx); err != nil || now.PublishedReleases != got.PublishedReleases+1 {
		t.Fatalf("uncached count %+v %v", now, err)
	}
}

// The default first page of SitemapTags is cached like the counts; other
// pages and sizes still query.
func TestSitemapTagsFirstPageCachedIntegration(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO releases(slug,canonical_title,status) SELECT $1||g,'Sitemap tag '||g,'published' FROM generate_series(1,6) g`, "smt-"+sfx+"-")
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT id,'T','smt-a-'||id FROM releases WHERE slug LIKE $1`, "smt-"+sfx+"-%")

	first, next, err := s.SitemapTags(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	want, wantNext, err := s.sitemapTagsNow(ctx, "", 0)
	if err != nil || next != wantNext || len(first) != len(want) || len(first) < 6 {
		t.Fatalf("first page %d %q, query %d %q (%v)", len(first), next, len(want), wantNext, err)
	}
	exec(`INSERT INTO release_tags(release_id,tag,normalized_tag) SELECT id,'T','smt-b-'||id FROM releases WHERE slug LIKE $1`, "smt-"+sfx+"-%")
	if again, _, err := s.SitemapTags(ctx, "", defaultSitemapLimit); err != nil || len(again) != len(first) {
		t.Fatalf("within the TTL: %d %v, want cached %d", len(again), err, len(first))
	}
	if small, next, err := s.SitemapTags(ctx, "", 3); err != nil || len(small) != 3 || next == "" {
		t.Fatalf("limit 3: %d %q %v", len(small), next, err)
	}
	if all, _, err := s.sitemapTagsNow(ctx, "", 0); err != nil || len(all) != len(first)+6 {
		t.Fatalf("uncached list %d %v, want %d", len(all), err, len(first)+6)
	}
}
