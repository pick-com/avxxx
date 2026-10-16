package migrations

import (
	"embed"
	"strings"
)

//go:embed 001_init.sql 002_catalog.sql 003_media_assets.sql 004_auth.sql 005_watch_history.sql 006_favorites.sql 007_auth_hardening.sql 008_playback_sources.sql 011_catalog_indexes.sql
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
	k, _ := schemaFiles.ReadFile("011_catalog_indexes.sql")
	return strings.Join([]string{string(a), string(b), string(c), string(d), string(e), string(f), string(g), string(h), string(k)}, "\n")
}()
