package acceptance

import (
	"avxxx/internal/catalog"
	"strings"
	"testing"
)

// TestRecrawlNeverDuplicatesIntegration crawls every adapter's fixture site
// (javdatabase, xcity, javguru, dmm ItemList + ActressSearch, RSS) through
// baseline → incremental → second baseline → noisy re-ingest (reordered
// listings and lists, different casing and whitespace) → plain again, and
// requires the entity, identifier, alias, credit, tag, source record and
// feed item tables to keep exactly the rows of the first pass.
func TestRecrawlNeverDuplicatesIntegration(t *testing.T) {
	e := newEnv(t)
	c := e.newCatalog(t)

	e.crawlAll(c, catalog.PhaseBaseline)
	e.mustSucceed(e.run(c.rssID, catalog.PhaseIncremental), "rss")
	first := e.snap()
	t.Logf("after first baseline: %s (field history rows %d)", first.counts(identityTables), first.history)

	// The fixtures produce what they should (the comparison is not vacuous).
	for _, w := range []struct {
		table string
		min   int
	}{{"performers", 6}, {"releases", 6}, {"release_identifiers", 6}, {"release_performers", 6}, {"release_tags", 6},
		{"source_records", 17}, {"performer_external_ids", 2}, {"items", 3}} {
		if n := first.count(w.table); n < w.min {
			t.Fatalf("first pass: %s has %d rows, want >= %d:\n%v", w.table, n, w.min, first.rows[w.table])
		}
	}
	// Cross-source consolidation: one release per DVD code, one performer per person.
	for _, q := range []struct{ what, sql string }{
		{"juq-743 releases", `SELECT count(*) FROM releases WHERE slug='juq-743'`},
		{"juq-743 source records", `SELECT count(*) FROM source_records sr JOIN releases r ON r.id=sr.release_id WHERE r.slug='juq-743'`},
		{"abc-001 source records", `SELECT count(*) FROM source_records sr JOIN releases r ON r.id=sr.release_id WHERE r.slug='abc-001'`},
		{"jane-doe source records", `SELECT count(*) FROM source_records sr JOIN performers p ON p.id=sr.performer_id WHERE p.slug='jane-doe'`},
	} {
		want := map[string]int64{"juq-743 releases": 1, "juq-743 source records": 2, "abc-001 source records": 3, "jane-doe source records": 2}[q.what]
		if got := e.int64(q.sql); got != want {
			t.Errorf("%s = %d, want %d", q.what, got, want)
		}
	}
	// DMM's actress id lands on the performer javdatabase created (matched by Japanese name).
	if got := e.str(`SELECT p.slug FROM performer_external_ids x JOIN performers p ON p.id=x.performer_id WHERE x.namespace='dmm_actress' AND x.external_id='1044099'`); got != "yurine-tsukino" {
		t.Errorf("DMM actress 1044099 -> %s, want yurine-tsukino", got)
	}

	steps := []struct {
		name string
		v    variant
		run  func()
	}{
		{"incremental", variant{}, func() { e.crawlAll(c, catalog.PhaseIncremental) }},
		{"second baseline", variant{}, func() { e.crawlAll(c, catalog.PhaseBaseline) }},
		{"noisy re-ingest (reordered, re-cased, re-spaced)", variant{noisy: true}, func() {
			e.crawlAll(c, catalog.PhaseBaseline)
			e.crawlAll(c, catalog.PhaseIncremental)
		}},
		{"code-less release retitled at the source", variant{}, func() {
			p := c.xc.get("/title/detail/5001/")
			c.xc.set("/title/detail/5001/", strings.Replace(p.body, "Summer Story XC</h1>", "Summer Story XC (Remastered)</h1>", 1))
			e.mustSucceed(e.run(c.xcID, catalog.PhaseBaseline), "xcity baseline after the retitle")
		}},
		{"plain again", variant{}, func() { e.crawlAll(c, catalog.PhaseBaseline) }},
	}
	for _, st := range steps {
		c.load(st.v)
		st.run()
		now := e.snap()
		if d := diffCounts(first, now, identityTables); d != "" {
			t.Errorf("%s created or lost rows:%s", st.name, d)
		}
		if d := keptLastSeen(first, now); d != "" {
			t.Errorf("%s: source records lost last_seen_at:%s", st.name, d)
		}
		t.Logf("after %s: %s (field history rows %d)", st.name, now.counts(identityTables), now.history)
	}
	// Plain fixtures again: every value is back to the first pass, so the
	// noisy pass changed nothing that a later pass cannot restore.
	if d := diffRows(first, e.snap(), []string{"performers", "release_identifiers", "release_performers", "release_tags", "performer_external_ids", "items"}); d != "" {
		t.Errorf("values after the plain re-crawl differ from the first pass:%s", d)
	}
	// Unchanged values are not re-recorded: a re-crawl of the same pages adds
	// no field history.
	before := e.snap()
	e.crawlAll(c, catalog.PhaseBaseline)
	if after := e.snap(); after.history != before.history {
		t.Errorf("re-crawl of unchanged pages added %d source_field_values history rows", after.history-before.history)
	}
}
