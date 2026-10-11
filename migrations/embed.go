package migrations

import _ "embed"

//go:embed 001_init.sql
var Schema string
