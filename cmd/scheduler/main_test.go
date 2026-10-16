package main

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// The scheduler only creates jobs; workers fetch. Guard against the scheduler
// growing a direct dependency on adapters, fetchers or HTTP.
func TestSchedulerNeverFetches(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		switch {
		case path == "avxxx/internal/catalog", path == "avxxx/internal/scraper", path == "avxxx/internal/media",
			path == "net/http", strings.HasPrefix(path, "net/http/"):
			t.Errorf("scheduler must not import %s: cron only creates jobs", path)
		}
	}
}
