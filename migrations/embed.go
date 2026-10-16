package migrations

import (
	"embed"
	"strings"
)

//go:embed 001_init.sql 002_catalog.sql 003_media_assets.sql 004_auth.sql 005_watch_history.sql 006_favorites.sql 007_auth_hardening.sql 008_playback_sources.sql 009_crawl_budget.sql 010_events.sql 011_catalog_indexes.sql 012_identity_merge.sql 013_audit.sql 014_social.sql
var schemaFiles embed.FS

var Schema = func() string {
	a, _ := schemaFiles.ReadFile("001_init.sql")
	b, _ := schemaFiles.ReadFile("002_catalog.sql")
	c, _ := schemaFiles.ReadFile("003_media_assets.sql")
	d, _ := schemaFiles.ReadFile("004_auth.sql")
	e, _ := schemaFiles.ReadFile("005_watch_history.sql")
	f, _ := schemaFiles.ReadFile("006_favorites.sql")
	g, _ := schemaFiles.ReadFile("007_auth_hardening.sql")
	h, _ := schemaFiles.ReadFile("008_playback_sources.sql")
	i, _ := schemaFiles.ReadFile("009_crawl_budget.sql")
	j, _ := schemaFiles.ReadFile("010_events.sql")
	k, _ := schemaFiles.ReadFile("011_catalog_indexes.sql")
	l, _ := schemaFiles.ReadFile("012_identity_merge.sql")
	m, _ := schemaFiles.ReadFile("013_audit.sql")
	n, _ := schemaFiles.ReadFile("014_social.sql")
	return strings.Join([]string{string(a), string(b), string(c), string(d), string(e), string(f), string(g), string(h), string(i), string(j), string(k), string(l), string(m), string(n)}, "\n")
}()
