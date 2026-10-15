package migrations

import (
	"embed"
	"strings"
)

//go:embed 001_init.sql 002_catalog.sql
var schemaFiles embed.FS

var Schema = func() string {
	a, _ := schemaFiles.ReadFile("001_init.sql")
	b, _ := schemaFiles.ReadFile("002_catalog.sql")
	return strings.Join([]string{string(a), string(b)}, "\n")
}()
