package store

import (
	"context"
	"testing"
)

// TestPreferredProviderIntegration covers migration 047's
// user_settings.preferred_provider (patch round trip, the other settings
// columns untouched), PreferredProvider and the provider list behind
// Settings 默认片源.
func TestPreferredProviderIntegration(t *testing.T) {
	openIsolatedTestDB(t, "prefprovider")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var uid int64
	must(s.QueryRow(ctx, `INSERT INTO users(email,password_hash) VALUES('pp@t','x') RETURNING id`).Scan(&uid))
	if p, err := s.PreferredProvider(ctx, uid); err != nil || p != "" {
		t.Fatalf("no settings row: %q %v", p, err)
	}
	_, err = s.PatchSettings(ctx, uid, []byte(`{"theme":"dark"}`))
	must(err)
	u, err := s.PatchSettings(ctx, uid, []byte(`{"preferred_provider":"javtiful"}`))
	must(err)
	if u.PreferredProvider != "javtiful" || u.Theme != "dark" {
		t.Errorf("patched %+v", u)
	}
	got, err := s.Settings(ctx, uid)
	must(err)
	if got.PreferredProvider != "javtiful" {
		t.Errorf("stored %q", got.PreferredProvider)
	}
	if p, _ := s.PreferredProvider(ctx, uid); p != "javtiful" {
		t.Errorf("PreferredProvider %q", p)
	}

	rel := func(slug, status string) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status) VALUES($1,$1,$2) RETURNING id`, slug, status).Scan(&id))
		return id
	}
	src := func(rid int64, provider, status string) {
		_, err := s.Exec(ctx, `INSERT INTO playback_sources(release_id,kind,provider,label,url,status) VALUES($1,'external_link',$2,'x','https://e/',$3)`, rid, provider, status)
		must(err)
	}
	a, b, d := rel("pp-a", "published"), rel("pp-b", "published"), rel("pp-d", "draft")
	src(a, "javtiful", "active")
	src(b, "javtiful", "degraded")
	src(b, "other", "active")
	src(a, "gone", "disabled")
	src(d, "draftonly", "active")
	ps, err := s.PlaybackProviders(ctx)
	must(err)
	if len(ps) != 2 || ps[0].Provider != "javtiful" || ps[0].Releases != 2 || ps[1].Provider != "other" {
		t.Errorf("providers %+v", ps)
	}
}
