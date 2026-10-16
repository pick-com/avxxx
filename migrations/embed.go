package migrations

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

// Every NNN_name.sql file is part of the schema; files are applied in
// filename order, so new migrations only need a new, higher-numbered file.
//
//go:embed *.sql
var schemaFiles embed.FS

// Schema is every embedded migration joined in sorted filename order.
var Schema = mustJoin(schemaFiles)

func mustJoin(fsys fs.FS) string {
	s, err := join(fsys)
	if err != nil {
		panic(err)
	}
	return s
}

func join(fsys fs.FS) (string, error) {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return "", err
		}
		parts[i] = string(b)
	}
	return strings.Join(parts, "\n"), nil
}
