// Command fixturecheck runs every catalog adapter's parser over the fixtures
// recorded under internal/catalog/testdata/<source>/ (manifest.json) and
// reports per-source field completeness against the acceptance target of
// 20 performers and 100 releases per source. It never touches the network.
//
//	go run ./cmd/fixturecheck            # table
//	go run ./cmd/fixturecheck -json      # machine-readable
//	go run ./cmd/fixturecheck -strict    # exit 1 unless every source meets the target
package main

import (
	"avxxx/internal/catalog"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func main() {
	dir := flag.String("dir", "internal/catalog/testdata", "fixture root (one directory per adapter)")
	asJSON := flag.Bool("json", false, "print JSON")
	strict := flag.Bool("strict", false, "exit 1 unless every source meets the recorded-fixture target")
	flag.Parse()
	results, err := run(context.Background(), *dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixturecheck:", err)
		os.Exit(2)
	}
	if *asJSON {
		out := make([]map[string]any, 0, len(results))
		for _, r := range results {
			out = append(out, map[string]any{"source": r.Source, "recorded": r.Recorded, "synthetic": r.Synthetic,
				"discovered": r.Discovered, "discovery_failed": r.DiscoveryFailed, "parse": r.Report.Summary(),
				"target":       map[string]int{"performers": catalog.FixtureTargetPerformers, "releases": catalog.FixtureTargetReleases},
				"meets_target": r.MeetsTarget()})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
	} else {
		fmt.Print(catalog.FormatFixtureResults(results))
	}
	failed := false
	for _, r := range results {
		if len(r.Report.Failures) > 0 || r.DiscoveryFailed > 0 || (*strict && !r.MeetsTarget()) {
			failed = true
		}
	}
	if failed {
		os.Exit(1)
	}
}

func run(ctx context.Context, dir string) ([]catalog.FixtureResult, error) {
	var out []catalog.FixtureResult
	for _, a := range catalog.Adapters() {
		r, err := catalog.RunFixtures(ctx, a, dir)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
