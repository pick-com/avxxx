package migrations

import (
	"embed"
	"strings"
)

//go:embed 001_init.sql 002_catalog.sql 003_media_assets.sql 004_auth.sql
var schemaFiles embed.FS

var Schema = func() string {
	a, _ := schemaFiles.ReadFile("001_init.sql")
	b, _ := schemaFiles.ReadFile("002_catalog.sql")
	c, _ := schemaFiles.ReadFile("003_media_assets.sql")
	d, _ := schemaFiles.ReadFile("004_auth.sql")
	return strings.Join([]string{string(a), string(b), string(c), string(d)}, "\n")
}()
